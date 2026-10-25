package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/ddup/pkg/buildinfo"
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

func (f *fakeProvider) GetDomainStatus(domain string) *healthcheck.DomainStatus {
	status, ok := f.GetAllDomainsStatus()[domain]
	if !ok {
		return nil
	}
	return &status
}

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
		"index.html":       {Data: []byte("<html><head></head></html>")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/app-1a.js": {Data: []byte("console.log(1)")},
	}
	srv, err := NewCachingFileServer(http.FS(fsys), 31536000, buildInfo{})
	require.NoError(t, err)

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
		t.Cleanup(func() {
			// Reset the config so later tests aren't affected by the base path
			*config.Get() = *config.GetDefaultConfig()
		})
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

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ddup/api/status", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		res := w.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"provider":"cloudflare"`)
	})

	t.Run("Build header on API requests under the base path", func(t *testing.T) {
		srv := newServerWithBasePath(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ddup/api/status", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		// The build header middleware must see the stripped path to decorate /api/ requests
		assert.NotEmpty(t, w.Result().Header.Get(headerBuild))
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

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()
		srv.handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
	})
}

func TestStatusEndpoints(t *testing.T) {
	s, err := NewServer(NewServerOpts{HealthChecker: &fakeProvider{}})
	require.NoError(t, err)

	do := func(t *testing.T, method, path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
		return rec
	}

	t.Run("healthz", func(t *testing.T) {
		rec := do(t, http.MethodGet, "/healthz")
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})

	t.Run("all domains", func(t *testing.T) {
		rec := do(t, http.MethodGet, "/api/status")
		require.Equal(t, http.StatusOK, rec.Code)

		var res map[string]healthcheck.DomainStatus
		err := json.Unmarshal(rec.Body.Bytes(), &res)
		require.NoError(t, err)
		require.Contains(t, res, "a.example.com")
		assert.Equal(t, "p", res["a.example.com"].Provider)
	})

	t.Run("one domain", func(t *testing.T) {
		rec := do(t, http.MethodGet, "/api/status/a.example.com")
		require.Equal(t, http.StatusOK, rec.Code)

		var res healthcheck.DomainStatus
		err := json.Unmarshal(rec.Body.Bytes(), &res)
		require.NoError(t, err)
		assert.Equal(t, "p", res.Provider)
	})

	t.Run("unknown domain", func(t *testing.T) {
		rec := do(t, http.MethodGet, "/api/status/missing.example.com")
		require.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Header().Get(headerContentType), "application/json")

		var res apiError
		err := json.Unmarshal(rec.Body.Bytes(), &res)
		require.NoError(t, err)
		assert.Equal(t, "api_status_domain_notfound", res.Code)
		assert.NotEmpty(t, res.Message)
	})

	t.Run("status does not accept POST", func(t *testing.T) {
		rec := do(t, http.MethodPost, "/api/status")
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestCORS(t *testing.T) {
	cfg := config.Get()
	prev := cfg.Dev.EnableCORS
	t.Cleanup(func() {
		cfg.Dev.EnableCORS = prev
	})

	getWithOrigin := func(t *testing.T) *httptest.ResponseRecorder {
		t.Helper()
		s, err := NewServer(NewServerOpts{HealthChecker: &fakeProvider{}})
		require.NoError(t, err)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/status", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("disabled by default", func(t *testing.T) {
		cfg.Dev.EnableCORS = false
		rec := getWithOrigin(t)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("enabled in dev mode", func(t *testing.T) {
		cfg.Dev.EnableCORS = true
		rec := getWithOrigin(t)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	})
}

func TestStaticIfModifiedSince(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html><head></head></html>")},
	}
	srv, err := NewCachingFileServer(http.FS(fsys), 60, buildInfo{})
	require.NoError(t, err)

	get := func(t *testing.T, ifModifiedSince string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		if ifModifiedSince != "" {
			req.Header.Set("If-Modified-Since", ifModifiedSince)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := get(t, "")
	require.Equal(t, http.StatusOK, rec.Code)
	lastModified := rec.Header().Get("Last-Modified")
	require.NotEmpty(t, lastModified)

	// The browser sends back the value it received: nothing has changed since
	rec = get(t, lastModified)
	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Body.String())

	// A copy from before the server started is stale
	rec = get(t, time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<html>")

	// Invalid dates are ignored
	rec = get(t, "not a date")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServerRun(t *testing.T) {
	// Disable keep-alives so no connection outlives the server
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	getStatusCode := func(t *testing.T, url string) int {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		require.NoError(t, err)
		res, err := client.Do(req)
		require.NoError(t, err)
		_ = res.Body.Close()
		return res.StatusCode
	}

	startServer := func(t *testing.T) (ln net.Listener, cancel context.CancelFunc, runErr chan error) {
		t.Helper()
		s, err := NewServer(NewServerOpts{HealthChecker: &fakeProvider{}})
		require.NoError(t, err)

		ln, err = (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		s.appListener = ln

		ctx, cancel := context.WithCancel(t.Context())
		runErr = make(chan error, 1)
		go func() {
			runErr <- s.Run(ctx)
		}()

		// Run starts serving on the listener that is already open, so this request waits for it
		assert.Equal(t, http.StatusNoContent, getStatusCode(t, "http://"+ln.Addr().String()+"/healthz"))

		// A second call fails while the server is running
		err = s.Run(ctx)
		require.ErrorContains(t, err, "server is already running")

		return ln, cancel, runErr
	}

	waitForRun := func(t *testing.T, runErr chan error) error {
		t.Helper()
		select {
		case err := <-runErr:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return")
			return nil
		}
	}

	t.Run("stops when the context is canceled", func(t *testing.T) {
		ln, cancel, runErr := startServer(t)
		defer cancel()

		cancel()
		require.NoError(t, waitForRun(t, runErr))

		// The listener is closed after shutdown
		_, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", ln.Addr().String())
		require.Error(t, err)
	})

	t.Run("returns an error if the server stops unexpectedly", func(t *testing.T) {
		ln, cancel, runErr := startServer(t)
		defer cancel()

		_ = ln.Close()
		require.ErrorContains(t, waitForRun(t, runErr), "app server failed")
	})
}

// Sets the build info for the duration of the test
func setBuildInfo(t *testing.T, version, id, commit, date string) {
	t.Helper()
	oldVersion, oldID, oldCommit, oldDate := buildinfo.AppVersion, buildinfo.BuildId, buildinfo.CommitHash, buildinfo.BuildDate
	t.Cleanup(func() {
		buildinfo.AppVersion, buildinfo.BuildId, buildinfo.CommitHash, buildinfo.BuildDate = oldVersion, oldID, oldCommit, oldDate
	})
	buildinfo.AppVersion, buildinfo.BuildId, buildinfo.CommitHash, buildinfo.BuildDate = version, id, commit, date
}

func TestBuildHeader(t *testing.T) {
	setBuildInfo(t, "1.2.3", "build-42", "0123456789abcdef", "2026-01-02T03:04:05Z")

	s, err := NewServer(NewServerOpts{HealthChecker: &fakeProvider{}})
	require.NoError(t, err)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return rec
	}

	t.Run("API responses have the build", func(t *testing.T) {
		rec := get("/api/status")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "1.2.3|build-42|0123456789abcdef", rec.Header().Get(headerBuild))
		// The response is the same as before
		assert.JSONEq(t, `{"a.example.com":{"lastUpdated":"0001-01-01T00:00:00Z","provider":"p","endpoints":null}}`, rec.Body.String())
	})

	t.Run("matches the build in the page", func(t *testing.T) {
		info := newBuildInfo()
		page, err := info.insertIntoPage([]byte("<head></head>"))
		require.NoError(t, err)

		var got buildInfo
		require.NoError(t, json.Unmarshal([]byte(extractBuildScript(t, string(page))), &got))
		assert.Equal(t, get("/api/status").Header().Get(headerBuild), got.Build)
	})

	t.Run("errors from the API have it too", func(t *testing.T) {
		rec := get("/api/status/unknown")
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.NotEmpty(t, rec.Header().Get(headerBuild))
	})

	t.Run("GET /api/info is gone", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, get("/api/info").Code)
	})

	t.Run("other paths don't have it", func(t *testing.T) {
		assert.Empty(t, get("/healthz").Header().Get(headerBuild))
	})
}

func TestBuildIdentifier(t *testing.T) {
	assert.Equal(t, "1.2.3|build-42|abc", buildIdentifier("1.2.3", "build-42", "abc"))
	assert.Equal(t, "canary||", buildIdentifier("canary", "", ""))
	assert.Equal(t, "a_b_c|_|", buildIdentifier("a\nb\u00e9c", "\x7f", ""))
}
