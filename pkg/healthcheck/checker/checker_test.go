package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/ddup/pkg/config"
)

// MockRoundTripper is a mock implementation of http.RoundTripper for testing
// Note: use this with HTTP endpoints too, not HTTPS
type MockRoundTripper struct {
	Response        *http.Response
	Error           error
	CapturedRequest *http.Request
	RoundTripFunc   func(req *http.Request) (*http.Response, error)
}

func (m *MockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Capture the request for inspection
	m.CapturedRequest = req

	// Use custom function if provided
	if m.RoundTripFunc != nil {
		return m.RoundTripFunc(req)
	}

	if m.Error != nil {
		return nil, m.Error
	}
	return m.Response, nil
}

// Helper function to create a test checker with custom HTTP client
func newTestChecker(client *http.Client) *checker {
	return &checker{
		domain: "test.example.com",
		cfg: config.ConfigHealthChecks{
			Timeout:  3 * time.Second,
			Attempts: 2,
		},
		client: client,
	}
}

func TestCheckEndpoint_Success(t *testing.T) {
	// Create a mock response with 200 status
	mockResponse := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       http.NoBody,
	}

	// Create mock round tripper
	mockRT := &MockRoundTripper{
		Response: mockResponse,
		Error:    nil,
	}

	// Create HTTP client with mock transport
	client := &http.Client{
		Transport: mockRT,
	}

	// Create checker with mock client
	checker := newTestChecker(client)

	// Create test endpoint
	endpoint := &config.ConfigEndpoint{
		Name: "test-endpoint",
		URL:  "http://example.com/health",
		IP:   "1.1.1.1",
		Host: "",
	}

	// Perform health check
	result := checker.checkEndpoint(t.Context(), endpoint)

	// Verify results
	assert.True(t, result.Healthy, "Endpoint should be healthy")
	require.NoError(t, result.Error, "No error should be returned")
	assert.Equal(t, endpoint, result.Endpoint, "Endpoint should match")
	assert.Greater(t, result.Duration, time.Duration(0), "Duration should be greater than 0")
}

func TestCheckEndpoint_HTTPError(t *testing.T) {
	// Create mock round tripper that returns an error
	mockRT := &MockRoundTripper{
		Response: nil,
		Error:    errors.New("connection refused"),
	}

	// Create HTTP client with mock transport
	client := &http.Client{
		Transport: mockRT,
	}

	// Create checker with mock client
	checker := newTestChecker(client)

	// Create test endpoint
	endpoint := &config.ConfigEndpoint{
		Name: "test-endpoint",
		URL:  "http://example.com/health",
		IP:   "1.1.1.1",
		Host: "",
	}

	// Perform health check
	result := checker.checkEndpoint(t.Context(), endpoint)

	// Verify results
	assert.False(t, result.Healthy, "Endpoint should be unhealthy")
	require.Error(t, result.Error, "Error should be returned")
	assert.Contains(t, result.Error.Error(), "HTTP request failed", "Error should mention HTTP request failure")
	assert.Equal(t, endpoint, result.Endpoint, "Endpoint should match")
	assert.Greater(t, result.Duration, time.Duration(0), "Duration should be greater than 0")
}

func TestCheckEndpoint_BadStatusCode(t *testing.T) {
	// Create a mock response with 500 status
	mockResponse := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     make(http.Header),
		Body:       http.NoBody,
	}

	// Create mock round tripper
	mockRT := &MockRoundTripper{
		Response: mockResponse,
		Error:    nil,
	}

	// Create HTTP client with mock transport
	client := &http.Client{
		Transport: mockRT,
	}

	// Create checker with mock client
	checker := newTestChecker(client)

	// Create test endpoint
	endpoint := &config.ConfigEndpoint{
		Name: "test-endpoint",
		URL:  "http://example.com/health",
		IP:   "1.1.1.1",
		Host: "",
	}

	// Perform health check
	result := checker.checkEndpoint(t.Context(), endpoint)

	// Verify results
	assert.False(t, result.Healthy, "Endpoint should be unhealthy")
	require.Error(t, result.Error, "Error should be returned")
	assert.Contains(t, result.Error.Error(), "status code 500", "Error should mention status code")
	assert.Equal(t, endpoint, result.Endpoint, "Endpoint should match")
	assert.Greater(t, result.Duration, time.Duration(0), "Duration should be greater than 0")
}

func TestCheckEndpoint_RedirectStatusCode(t *testing.T) {
	// Create a mock response with 302 status (redirect)
	mockResponse := &http.Response{
		StatusCode: http.StatusFound,
		Header:     make(http.Header),
		Body:       http.NoBody,
	}

	// Create mock round tripper
	mockRT := &MockRoundTripper{
		Response: mockResponse,
		Error:    nil,
	}

	// Create HTTP client with mock transport
	client := &http.Client{
		Transport: mockRT,
	}

	// Create checker with mock client
	checker := newTestChecker(client)

	// Create test endpoint
	endpoint := &config.ConfigEndpoint{
		Name: "test-endpoint",
		URL:  "http://example.com/health",
		IP:   "1.1.1.1",
		Host: "",
	}

	// Perform health check
	result := checker.checkEndpoint(t.Context(), endpoint)

	// Verify results
	assert.False(t, result.Healthy, "Endpoint should be unhealthy for redirect")
	require.Error(t, result.Error, "Error should be returned")
	assert.Contains(t, result.Error.Error(), "status code 302", "Error should mention status code")
	assert.Equal(t, endpoint, result.Endpoint, "Endpoint should match")
	assert.Greater(t, result.Duration, time.Duration(0), "Duration should be greater than 0")
}

func TestCheckEndpoint_InvalidURL(t *testing.T) {
	// Create HTTP client (won't be used due to invalid URL)
	client := &http.Client{}

	// Create checker with client
	checker := newTestChecker(client)

	// Create test endpoint with invalid URL
	endpoint := &config.ConfigEndpoint{
		Name: "test-endpoint",
		URL:  "://invalid-url",
		IP:   "1.1.1.1",
		Host: "",
	}

	// Perform health check
	result := checker.checkEndpoint(t.Context(), endpoint)

	// Verify results
	assert.False(t, result.Healthy, "Endpoint should be unhealthy")
	require.Error(t, result.Error, "Error should be returned")
	assert.Contains(t, result.Error.Error(), "creating request", "Error should mention request creation failure")
	assert.Equal(t, endpoint, result.Endpoint, "Endpoint should match")
	assert.Greater(t, result.Duration, time.Duration(0), "Duration should be greater than 0")
}

func TestCheckEndpoint_WithCustomHost(t *testing.T) {
	// Create a mock response with 200 status
	mockResponse := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       http.NoBody,
	}

	// Create mock round tripper
	mockRT := &MockRoundTripper{
		Response: mockResponse,
		Error:    nil,
	}

	// Create HTTP client with mock transport
	client := &http.Client{
		Transport: mockRT,
	}

	// Create checker with mock client
	checker := newTestChecker(client)

	// Create test endpoint with custom host
	endpoint := &config.ConfigEndpoint{
		Name: "test-endpoint",
		URL:  "http://1.1.1.1/health",
		IP:   "1.1.1.1",
		Host: "example.com",
	}

	// Perform health check
	result := checker.checkEndpoint(t.Context(), endpoint)

	// Verify results
	assert.True(t, result.Healthy, "Endpoint should be healthy")
	require.NoError(t, result.Error, "No error should be returned")
	assert.Equal(t, endpoint, result.Endpoint, "Endpoint should match")
	assert.Greater(t, result.Duration, time.Duration(0), "Duration should be greater than 0")

	// Verify that the Host header was set correctly
	assert.NotNil(t, mockRT.CapturedRequest, "Request should have been captured")
	assert.Equal(t, "example.com", mockRT.CapturedRequest.Host, "Host header should be set to custom host")
	assert.Equal(t, "ddup/1.0", mockRT.CapturedRequest.Header.Get("User-Agent"), "User-Agent should be set")
}

func TestCheckEndpoint_ContextTimeout(t *testing.T) {
	// Create mock round tripper that simulates a slow response
	mockRT := &MockRoundTripper{
		Response: nil,
		Error:    context.DeadlineExceeded,
	}

	// Create HTTP client with mock transport
	client := &http.Client{
		Transport: mockRT,
	}

	// Create checker with very short timeout
	checker := &checker{
		domain:    "test.example.com",
		endpoints: nil,
		cfg: config.ConfigHealthChecks{
			Timeout:  1 * time.Millisecond, // Very short timeout
			Attempts: 2,
		},
		metrics: nil,
		client:  client,
	}

	// Create test endpoint
	endpoint := &config.ConfigEndpoint{
		Name: "test-endpoint",
		URL:  "http://example.com/health",
		IP:   "1.1.1.1",
		Host: "",
	}

	// Perform health check
	result := checker.checkEndpoint(t.Context(), endpoint)

	// Verify results
	assert.False(t, result.Healthy, "Endpoint should be unhealthy due to timeout")
	require.Error(t, result.Error, "Error should be returned")
	assert.Contains(t, result.Error.Error(), "HTTP request failed", "Error should mention HTTP request failure")
	assert.Equal(t, endpoint, result.Endpoint, "Endpoint should match")
	assert.Greater(t, result.Duration, time.Duration(0), "Duration should be greater than 0")
}

func TestCheckEndpoint_SuccessStatusCodes(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		shouldPass bool
	}{
		{"Status 200", 200, true},
		{"Status 201", 201, true},
		{"Status 299", 299, true},
		{"Status 199", 199, false},
		{"Status 300", 300, false},
		{"Status 400", 400, false},
		{"Status 500", 500, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock response with test status code
			mockResponse := &http.Response{
				StatusCode: tc.statusCode,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}

			// Create mock round tripper
			mockRT := &MockRoundTripper{
				Response: mockResponse,
				Error:    nil,
			}

			// Create HTTP client with mock transport
			client := &http.Client{
				Transport: mockRT,
			}

			// Create checker with mock client
			checker := newTestChecker(client)

			// Create test endpoint
			endpoint := &config.ConfigEndpoint{
				Name: "test-endpoint",
				URL:  "http://example.com/health",
				IP:   "1.1.1.1",
				Host: "",
			}

			// Perform health check
			result := checker.checkEndpoint(t.Context(), endpoint)

			// Verify results
			if tc.shouldPass {
				assert.True(t, result.Healthy, "Endpoint should be healthy for status %d", tc.statusCode)
				require.NoError(t, result.Error, "No error should be returned for status %d", tc.statusCode)
			} else {
				assert.False(t, result.Healthy, "Endpoint should be unhealthy for status %d", tc.statusCode)
				require.Error(t, result.Error, "Error should be returned for status %d", tc.statusCode)
				assert.Contains(t, result.Error.Error(), "status code", "Error should mention status code")
			}

			assert.Equal(t, endpoint, result.Endpoint, "Endpoint should match")
			assert.Greater(t, result.Duration, time.Duration(0), "Duration should be greater than 0")
		})
	}
}

func TestCheckEndpoint_CustomHostDoesNotMutateSharedClient(t *testing.T) {
	var mu sync.Mutex
	hosts := make([]string, 0, 2)

	// The httptest TLS certificate includes the DNS name "example.com" and 127.0.0.1 as an IP SAN
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hosts = append(hosts, r.Host)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Transport trusting the test server's certificate, without a pinned TLS ServerName
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	baseTransport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}

	c := &checker{
		domain: "test.example.com",
		cfg: config.ConfigHealthChecks{
			Timeout:  3 * time.Second,
			Attempts: 2,
		},
		client: &http.Client{Transport: baseTransport},
		endpoints: []*config.ConfigEndpoint{
			{Name: "with-host", URL: srv.URL + "/health", IP: "1.1.1.1", Host: "example.com"},
			{Name: "no-host", URL: srv.URL + "/health", IP: "1.1.1.1"},
		},
	}

	// Endpoints are checked concurrently: with the old code, the endpoint with a custom host pinned
	// its TLS ServerName onto the shared client, sending the other endpoint's request with the wrong SNI
	results := c.CheckAll(t.Context())
	require.Len(t, results, 2)
	for _, res := range results {
		assert.True(t, res.Healthy, "endpoint %s should be healthy: %v", res.Endpoint.Name, res.Error)
		require.NoError(t, res.Error, "no error expected for endpoint %s", res.Endpoint.Name)
	}

	// Each request carried its own host
	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{"example.com", srv.Listener.Addr().String()}, hosts)

	// The shared client and transport were not modified
	assert.Same(t, baseTransport, c.client.Transport)
	require.NotNil(t, baseTransport.TLSClientConfig)
	assert.Empty(t, baseTransport.TLSClientConfig.ServerName)
}
