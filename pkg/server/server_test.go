package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/ddup/pkg/config"
	"github.com/italypaleale/ddup/pkg/healthcheck"
)

type fakeProvider struct {
	forceErr   error
	forceCalls int
}

func (f *fakeProvider) GetAllDomainsStatus() map[string]healthcheck.DomainStatus {
	return map[string]healthcheck.DomainStatus{"a.example.com": {Provider: "p"}}
}

func (f *fakeProvider) GetDomainStatus(domain string) *healthcheck.DomainStatus { return nil }

func (f *fakeProvider) ForceCheck(ctx context.Context) error {
	f.forceCalls++
	return f.forceErr
}

func TestForceCheckEndpoint(t *testing.T) {
	post := func(t *testing.T, fp *fakeProvider, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		s, err := NewServer(NewServerOpts{HealthChecker: fp})
		require.NoError(t, err)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/check", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("requires the header", func(t *testing.T) {
		fp := &fakeProvider{}
		rec := post(t, fp, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Zero(t, fp.forceCalls, "no check is run without the header")

		rec = post(t, fp, map[string]string{headerRequestedBy: "other"})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Zero(t, fp.forceCalls)
	})

	t.Run("runs the check and returns the status", func(t *testing.T) {
		fp := &fakeProvider{}
		rec := post(t, fp, map[string]string{headerRequestedBy: requestedByValue})
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, 1, fp.forceCalls)
		assert.Contains(t, rec.Body.String(), "a.example.com")
	})

	t.Run("maps errors", func(t *testing.T) {
		rec := post(t, &fakeProvider{forceErr: context.DeadlineExceeded}, map[string]string{headerRequestedBy: requestedByValue})
		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)

		rec = post(t, &fakeProvider{forceErr: errors.New("boom")}, map[string]string{headerRequestedBy: requestedByValue})
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("GET does not run a check", func(t *testing.T) {
		fp := &fakeProvider{}
		s, err := NewServer(NewServerOpts{HealthChecker: fp})
		require.NoError(t, err)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/check", nil)
		req.Header.Set(headerRequestedBy, requestedByValue)
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusOK, rec.Code)
		assert.Zero(t, fp.forceCalls)
	})
}

func TestStaticCacheControl(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":       {Data: []byte("<html></html>")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/app-1a.js": {Data: []byte("console.log(1)")},
	}
	srv := NewCachingFileServer(http.FS(fsys), 31536000)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return rec
	}

	// The page and the icon can change on upgrades: browsers must revalidate them
	assert.Equal(t, "no-cache", get("/").Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", get("/index.html").Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", get("/favicon.svg").Header().Get("Cache-Control"))
	// Assets have a hash in their name
	rec := get("/assets/app-1a.js")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
	assert.Contains(t, rec.Header().Get("Cache-Control"), "max-age=31536000")
}

// mockStatusProvider is a StatusProvider that returns static data for testing
type mockStatusProvider struct {
	statuses map[string]healthcheck.DomainStatus
}

func (m mockStatusProvider) GetAllDomainsStatus() map[string]healthcheck.DomainStatus {
	return m.statuses
}

func (m mockStatusProvider) GetDomainStatus(domain string) *healthcheck.DomainStatus {
	s, ok := m.statuses[domain]
	if !ok {
		return nil
	}
	return &s
}

func (m mockStatusProvider) ForceCheck(ctx context.Context) error { return nil }

// newTestServer creates a Server for testing, with the default config and the given statuses
func newTestServer(t *testing.T, statuses map[string]healthcheck.DomainStatus) *Server {
	t.Helper()

	cfg := config.GetDefaultConfig()
	*config.Get() = *cfg

	srv, err := NewServer(NewServerOpts{
		HealthChecker: mockStatusProvider{statuses: statuses},
	})
	require.NoError(t, err)
	return srv
}

func TestServerRoutes(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	statuses := map[string]healthcheck.DomainStatus{
		"api.example.com": {
			LastUpdated: now,
			Provider:    "cloudflare",
			Endpoints: []healthcheck.DomainStatusEndpoint{
				{Healthy: true, IP: "203.0.113.10"},
				{Healthy: false, IP: "203.0.113.11", FailureCount: 5},
			},
		},
	}

	t.Run("GET /api/summary", func(t *testing.T) {
		srv := newTestServer(t, statuses)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/summary", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		res := w.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		assert.Contains(t, res.Header.Get("Content-Type"), "application/json")

		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)

		var summary StatusSummary
		require.NoError(t, json.Unmarshal(body, &summary))
		require.Len(t, summary.Domains, 1)
		assert.Equal(t, "api.example.com", summary.Domains[0].Name)
		assert.Equal(t, domainStatusWarning, summary.Domains[0].Status)
		assert.Equal(t, 1, summary.Domains[0].EndpointsHealthy)
		assert.Equal(t, 2, summary.Domains[0].EndpointsTotal)
		assert.Equal(t, 1, summary.Summary.WarningDomains)
	})

	t.Run("GET /api/status", func(t *testing.T) {
		srv := newTestServer(t, statuses)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		res := w.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"provider":"cloudflare"`)
	})

	t.Run("GET /api/status/{recordname} not found", func(t *testing.T) {
		srv := newTestServer(t, statuses)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/status/nope", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
	})
}

func TestServerBasePath(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	statuses := map[string]healthcheck.DomainStatus{
		"api.example.com": {
			LastUpdated: now,
			Provider:    "cloudflare",
			Endpoints: []healthcheck.DomainStatusEndpoint{
				{Healthy: true, IP: "203.0.113.10"},
			},
		},
	}

	newServerWithBasePath := func(t *testing.T) *Server {
		t.Helper()
		newTestServer(t, statuses) // Reset the config to defaults
		config.Get().Server.BasePath = "ddup/"
		srv, err := NewServer(NewServerOpts{
			HealthChecker: mockStatusProvider{statuses: statuses},
		})
		require.NoError(t, err)
		return srv
	}

	t.Run("Redirect without trailing slash", func(t *testing.T) {
		srv := newServerWithBasePath(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ddup", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		res := w.Result()
		assert.Equal(t, http.StatusMovedPermanently, res.StatusCode)
		assert.Equal(t, "/ddup/", res.Header.Get("Location"))
	})

	t.Run("Routes served under the base path", func(t *testing.T) {
		srv := newServerWithBasePath(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ddup/api/summary", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		res := w.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"totalDomains":1`)
	})

	t.Run("healthz under the base path", func(t *testing.T) {
		srv := newServerWithBasePath(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ddup/healthz", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Result().StatusCode)
	})

	t.Run("API not available at the root", func(t *testing.T) {
		srv := newServerWithBasePath(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/summary", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
	})
}
