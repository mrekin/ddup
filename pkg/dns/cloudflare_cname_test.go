package dns

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/ddup/pkg/config"
)

func TestCloudflareProvider_UpdateCNAME(t *testing.T) {
	t.Run("Create record", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()

		// Mock response for getting existing CNAME records (empty response)
		mockTransport.SetResponse(http.MethodGet, "/client/v4/zones/test-zone-id/dns_records?name=example.com&type=CNAME", &MockResponse{
			StatusCode: 200,
			Body: `{
				"success": true,
				"errors": [],
				"result": []
			}`,
			Headers: map[string]string{"Content-Type": "application/json"},
		})

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

		// Verify the requests: GET and POST only
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 2)

		assert.Equal(t, http.MethodGet, requests[0].Method)
		assert.Contains(t, requests[0].URL.RawQuery, "type=CNAME")

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

	t.Run("Replace stale record", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()

		// Mock response with an existing CNAME record pointing at the old target
		mockTransport.SetResponse(http.MethodGet, "/client/v4/zones/test-zone-id/dns_records?name=example.com&type=CNAME", &MockResponse{
			StatusCode: 200,
			Body: `{
				"success": true,
				"errors": [],
				"result": [
					{
						"id": "record-456",
						"type": "CNAME",
						"name": "example.com",
						"content": "old.example.com",
						"ttl": 300
					}
				]
			}`,
			Headers: map[string]string{"Content-Type": "application/json"},
		})

		mockTransport.SetResponse(http.MethodPost, "/client/v4/zones/test-zone-id/dns_records", &MockResponse{
			StatusCode: 200,
			Body:       `{"success": true, "errors": [], "result": {}}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		})

		mockTransport.SetResponse(http.MethodDelete, "/client/v4/zones/test-zone-id/dns_records/record-456", &MockResponse{
			StatusCode: 200,
			Body:       `{"success": true, "errors": [], "result": {}}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		})

		err := provider.UpdateCNAME(t.Context(), "example.com", 300, "target.example.com")
		require.NoError(t, err)

		// Verify the requests: the new record is created before the stale one is deleted
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 3)

		assert.Equal(t, http.MethodGet, requests[0].Method)
		assert.Equal(t, http.MethodPost, requests[1].Method)
		assert.Equal(t, http.MethodDelete, requests[2].Method)
		assert.Equal(t, "/client/v4/zones/test-zone-id/dns_records/record-456", requests[2].URL.Path)
	})

	t.Run("No change skips update", func(t *testing.T) {
		provider, mockTransport := newCloudflareTestProviderWithMock()

		// Mock response with an existing CNAME record already pointing at the desired target
		mockTransport.SetResponse(http.MethodGet, "/client/v4/zones/test-zone-id/dns_records?name=example.com&type=CNAME", &MockResponse{
			StatusCode: 200,
			Body: `{
				"success": true,
				"errors": [],
				"result": [
					{
						"id": "record-789",
						"type": "CNAME",
						"name": "example.com",
						"content": "target.example.com",
						"ttl": 300
					}
				]
			}`,
			Headers: map[string]string{"Content-Type": "application/json"},
		})

		err := provider.UpdateCNAME(t.Context(), "example.com", 300, "target.example.com")
		require.NoError(t, err)

		// Verify only the GET request was made
		requests := mockTransport.GetRequests()
		require.Len(t, requests, 1)
		assert.Equal(t, http.MethodGet, requests[0].Method)
	})

	t.Run("Implement CNAMEUpdater", func(t *testing.T) {
		provider, _ := newCloudflareTestProviderWithMock()

		var updater CNAMEUpdater = provider
		assert.Implements(t, (*CNAMEUpdater)(nil), updater)
		assert.Equal(t, config.RecordTypeCNAME, "CNAME")
	})
}
