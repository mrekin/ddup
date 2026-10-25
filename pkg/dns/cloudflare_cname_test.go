package dns

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)


// The 81053 error is what the real Cloudflare API returns when a POST tries to create
// a record for a host that already has an A, AAAA or CNAME record
// It is wired into the POST mocks below so any regression to create-on-existing fails the test
const cloudflare81053Body = `{"success":false,"errors":[{"code":81053,"message":"An A, AAAA, or CNAME record with that host already exists"}],"result":[]}`

func TestCloudflareProvider_UpdateCNAME(t *testing.T) {
	// listHostRecords returns the mock GET response listing all records at the host, without a type filter, like the real API
	listHostRecords := func(t *testing.T, mockTransport *MockHTTPTransport, domain, records string) {
		t.Helper()
		mockTransport.SetResponse(http.MethodGet, "/client/v4/zones/test-zone-id/dns_records?name="+domain, &MockResponse{
			StatusCode: 200,
			Body: `{
				"success": true,
				"errors": [],
				"result": ` + records + `
			}`,
			Headers: map[string]string{"Content-Type": "application/json"},
		})
	}

	t.Run("Create record when the host has none", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()
		listHostRecords(t, mockTransport, "example.com", "[]")

		// Mock response for creating a record
		mockTransport.SetResponse(http.MethodPost, "/client/v4/zones/test-zone-id/dns_records", &MockResponse{
			StatusCode: 200,
			Body: `{
				"success": true,
				"errors": [],
				"result": {
					"id": "record-123",
					"type": "CNAME",
					"name": "example.com",
					"content": "target.example.com",
					"ttl": 300
				}
			}`,
			Headers: map[string]string{"Content-Type": "application/json"},
		})

		err := provider.UpdateCNAME(t.Context(), "example.com", 300, "target.example.com")
		require.NoError(t, err)

		// Verify the requests: GET and POST only, and the list is not filtered by record type
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 2)

		assert.Equal(t, http.MethodGet, requests[0].Method)
		assert.Contains(t, requests[0].URL.RawQuery, "name=example.com")
		assert.NotContains(t, requests[0].URL.RawQuery, "type=")

		assert.Equal(t, http.MethodPost, requests[1].Method)
		body, err := io.ReadAll(requests[1].Body)
		require.NoError(t, err)

		var createReq map[string]any
		err = json.Unmarshal(body, &createReq)
		require.NoError(t, err)
		assert.Equal(t, "CNAME", createReq["type"])
		assert.Equal(t, "target.example.com", createReq["content"])
		assert.InDelta(t, 300, createReq["ttl"], 0)
	})

	t.Run("Update existing CNAME in place when the target differs", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()
		listHostRecords(t, mockTransport, "example.com", `[
			{
				"id": "record-456",
				"type": "CNAME",
				"name": "example.com",
				"content": "old.example.com",
				"ttl": 300
			}
		]`)

		// PATCH updates the record in place, like the real API
		mockTransport.SetResponse(http.MethodPatch, "/client/v4/zones/test-zone-id/dns_records/record-456", &MockResponse{
			StatusCode: 200,
			Body: `{
				"success": true,
				"errors": [],
				"result": {
					"id": "record-456",
					"type": "CNAME",
					"name": "example.com",
					"content": "target.example.com",
					"ttl": 300
				}
			}`,
			Headers: map[string]string{"Content-Type": "application/json"},
		})

		// The real API rejects a POST while the host still has a record: this must never be called here
		mockTransport.SetResponse(http.MethodPost, "/client/v4/zones/test-zone-id/dns_records", &MockResponse{
			StatusCode: 400,
			Body:       cloudflare81053Body,
			Headers:    map[string]string{"Content-Type": "application/json"},
		})

		err := provider.UpdateCNAME(t.Context(), "example.com", 300, "target.example.com")
		require.NoError(t, err)

		// Verify the requests: the existing record is PATCHed, nothing is created or deleted
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 2)

		assert.Equal(t, http.MethodGet, requests[0].Method)

		patchReq := requests[1]
		assert.Equal(t, http.MethodPatch, patchReq.Method)
		assert.Equal(t, "/client/v4/zones/test-zone-id/dns_records/record-456", patchReq.URL.Path)

		body, err := io.ReadAll(patchReq.Body)
		require.NoError(t, err)

		var patchBody map[string]any
		err = json.Unmarshal(body, &patchBody)
		require.NoError(t, err)
		assert.Equal(t, "CNAME", patchBody["type"])
		assert.Equal(t, "example.com", patchBody["name"])
		assert.Equal(t, "target.example.com", patchBody["content"])
		assert.InDelta(t, 300, patchBody["ttl"], 0)
	})

	t.Run("Existing CNAME with the same target is left alone", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()
		listHostRecords(t, mockTransport, "example.com", `[
			{
				"id": "record-789",
				"type": "CNAME",
				"name": "example.com",
				"content": "target.example.com",
				"ttl": 300
			}
		]`)

		err := provider.UpdateCNAME(t.Context(), "example.com", 300, "target.example.com")
		require.NoError(t, err)

		// Verify only the GET request was made
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 1)
		assert.Equal(t, http.MethodGet, requests[0].Method)
	})

	t.Run("A records conflicting with the CNAME are deleted before creating", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()
		listHostRecords(t, mockTransport, "example.com", `[
			{
				"id": "record-111",
				"type": "A",
				"name": "example.com",
				"content": "203.0.113.7",
				"ttl": 300
			}
		]`)

		mockTransport.SetResponse(http.MethodDelete, "/client/v4/zones/test-zone-id/dns_records/record-111", &MockResponse{
			StatusCode: 200,
			Body:       `{"success": true, "errors": [], "result": {}}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		})

		// Once the conflicting record is gone, the create is legitimate
		mockTransport.SetResponse(http.MethodPost, "/client/v4/zones/test-zone-id/dns_records", &MockResponse{
			StatusCode: 200,
			Body:       `{"success": true, "errors": [], "result": {"id": "record-222"}}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		})

		err := provider.UpdateCNAME(t.Context(), "example.com", 300, "target.example.com")
		require.NoError(t, err)

		// Verify the requests: the conflict is removed first, because creating a CNAME while the A record exists is rejected by the API
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 3)

		assert.Equal(t, http.MethodGet, requests[0].Method)
		assert.Equal(t, http.MethodDelete, requests[1].Method)
		assert.Equal(t, "/client/v4/zones/test-zone-id/dns_records/record-111", requests[1].URL.Path)
		assert.Equal(t, http.MethodPost, requests[2].Method)
	})

	t.Run("Duplicate CNAME records are collapsed", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()
		listHostRecords(t, mockTransport, "example.com", `[
			{
				"id": "record-1",
				"type": "CNAME",
				"name": "example.com",
				"content": "old1.example.com",
				"ttl": 300
			},
			{
				"id": "record-2",
				"type": "CNAME",
				"name": "example.com",
				"content": "old2.example.com",
				"ttl": 300
			}
		]`)

		mockTransport.SetResponse(http.MethodPatch, "/client/v4/zones/test-zone-id/dns_records/record-1", &MockResponse{
			StatusCode: 200,
			Body:       `{"success": true, "errors": [], "result": {}}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		})

		mockTransport.SetResponse(http.MethodDelete, "/client/v4/zones/test-zone-id/dns_records/record-2", &MockResponse{
			StatusCode: 200,
			Body:       `{"success": true, "errors": [], "result": {}}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		})

		err := provider.UpdateCNAME(t.Context(), "example.com", 300, "target.example.com")
		require.NoError(t, err)

		// Verify the requests: the first record is PATCHed to the target, the duplicate is deleted
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 3)

		assert.Equal(t, http.MethodGet, requests[0].Method)
		assert.Equal(t, http.MethodPatch, requests[1].Method)
		assert.Equal(t, "/client/v4/zones/test-zone-id/dns_records/record-1", requests[1].URL.Path)
		assert.Equal(t, http.MethodDelete, requests[2].Method)
		assert.Equal(t, "/client/v4/zones/test-zone-id/dns_records/record-2", requests[2].URL.Path)
	})

	t.Run("Failed PATCH propagates without deleting", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()
		listHostRecords(t, mockTransport, "example.com", `[
			{
				"id": "record-456",
				"type": "CNAME",
				"name": "example.com",
				"content": "old.example.com",
				"ttl": 300
			}
		]`)

		mockTransport.SetResponse(http.MethodPatch, "/client/v4/zones/test-zone-id/dns_records/record-456", &MockResponse{
			StatusCode: http.StatusInternalServerError,
			Body:       `{"success": false, "errors": [{"code": 10000, "message": "Internal error"}]}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		})

		err := provider.UpdateCNAME(t.Context(), "example.com", 300, "target.example.com")
		require.ErrorContains(t, err, "error updating record record-456 for target target.example.com")
		require.ErrorContains(t, err, "HTTP 500")

		// Verify the requests: nothing else is attempted after the failed update
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 2)
		assert.Equal(t, http.MethodGet, requests[0].Method)
		assert.Equal(t, http.MethodPatch, requests[1].Method)
	})

	t.Run("Implement CNAMEUpdater", func(t *testing.T) {
		provider, _ := newCloudflareTestProviderWithMock()

		assert.Implements(t, (*CNAMEUpdater)(nil), provider)
	})
}
