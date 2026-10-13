package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/ddup/pkg/config"
)

// capturedRequest holds a request received by the test ntfy server
type capturedRequest struct {
	method string
	path   string
	header http.Header
	body   string
}

// newTestServer starts an ntfy-like server that captures requests and signals on received
func newTestServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request) bool) (*httptest.Server, chan capturedRequest) {
	t.Helper()
	received := make(chan capturedRequest, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		received <- capturedRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: string(body)}
		if handler == nil || handler(w, r) {
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, received
}

// waitForReceived reads a captured request or fails the test after a short timeout
func waitForReceived(t *testing.T, received chan capturedRequest) capturedRequest {
	t.Helper()
	select {
	case req := <-received:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("notification was not delivered")
		return capturedRequest{}
	}
}

func TestNtfyNotifier_Events(t *testing.T) {
	tests := []struct {
		name         string
		call         func(n *NtfyNotifier)
		title        string
		priority     string
		tags         string
		bodyContains []string
	}{
		{
			name: "endpoint down",
			call: func(n *NtfyNotifier) {
				n.EndpointDown(context.Background(), "example.com", "ep1", "1.1.1.1", errors.New("connection refused"))
			},
			title:        "ddup: ep1 is down",
			priority:     "high",
			tags:         "rotating_light",
			bodyContains: []string{"Domain example.com: endpoint ep1 (1.1.1.1) failed its health checks", "Last error: connection refused"},
		},
		{
			name: "endpoint down without error",
			call: func(n *NtfyNotifier) {
				n.EndpointDown(context.Background(), "example.com", "ep1", "1.1.1.1", nil)
			},
			title:        "ddup: ep1 is down",
			priority:     "high",
			tags:         "rotating_light",
			bodyContains: []string{"endpoint ep1 (1.1.1.1) failed"},
		},
		{
			name: "endpoint up",
			call: func(n *NtfyNotifier) {
				n.EndpointUp(context.Background(), "example.com", "ep1", "1.1.1.1")
			},
			title:        "ddup: ep1 recovered",
			priority:     "default",
			tags:         "white_check_mark",
			bodyContains: []string{"Domain example.com: endpoint ep1 (1.1.1.1) is healthy again"},
		},
		{
			name: "dns updated with empty record type defaults to A",
			call: func(n *NtfyNotifier) {
				n.DNSUpdated(context.Background(), "example.com", "", []string{"2.2.2.2", "1.1.1.1"}, []string{"9.9.9.9"})
			},
			title:        "ddup: DNS records updated for example.com",
			priority:     "default",
			tags:         "arrows_counterclockwise",
			bodyContains: []string{"Records for example.com (A) were updated", "Previous: 9.9.9.9", "New: 2.2.2.2, 1.1.1.1"},
		},
		{
			name: "dns update failed",
			call: func(n *NtfyNotifier) {
				n.DNSUpdateFailed(context.Background(), "example.com", "CNAME", errors.New("mock error"))
			},
			title:        "ddup: DNS update failed for example.com",
			priority:     "high",
			tags:         "warning",
			bodyContains: []string{"Failed to update CNAME records for example.com: mock error"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, received := newTestServer(t, nil)
			n := &NtfyNotifier{
				server:     srv.URL,
				topic:      "my-topic",
				timeout:    5 * time.Second,
				httpClient: srv.Client(),
			}

			tc.call(n)
			req := waitForReceived(t, received)

			assert.Equal(t, http.MethodPost, req.method)
			assert.Equal(t, "/my-topic", req.path)
			assert.Equal(t, tc.title, req.header.Get("Title"))
			assert.Equal(t, tc.priority, req.header.Get("Priority"))
			assert.Equal(t, tc.tags, req.header.Get("Tags"))
			assert.Empty(t, req.header.Get("Authorization"), "no token configured")
			for _, substr := range tc.bodyContains {
				assert.Contains(t, req.body, substr)
			}
			if tc.name == "endpoint down without error" {
				assert.NotContains(t, req.body, "Last error")
			}

			// The delivery is recorded in the status
			assert.Eventually(t, func() bool {
				status, attempted := n.NotificationStatus()
				return attempted && status.OK && !status.LastAttempt.IsZero()
			}, 2*time.Second, 10*time.Millisecond)
		})
	}
}

func TestNtfyNotifier_Token(t *testing.T) {
	srv, received := newTestServer(t, nil)
	n, err := NewNtfyNotifier(&config.ConfigNotifications{
		Server: srv.URL + "/", // A trailing slash must be trimmed
		Topic:  "sec-topic",
		Token:  "tk_secret",
	})
	require.NoError(t, err)
	n.httpClient = srv.Client()

	n.EndpointUp(context.Background(), "example.com", "ep1", "1.1.1.1")
	req := waitForReceived(t, received)

	assert.Equal(t, "/sec-topic", req.path, "trailing slash is trimmed")
	assert.Equal(t, "Bearer tk_secret", req.header.Get("Authorization"))
}

func TestNtfyNotifier_ServerError(t *testing.T) {
	srv, received := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(http.StatusForbidden)
		return false
	})
	n := &NtfyNotifier{
		server:     srv.URL,
		topic:      "my-topic",
		timeout:    5 * time.Second,
		httpClient: srv.Client(),
	}

	// The method returns without error even though the server rejects the notification
	n.EndpointUp(context.Background(), "example.com", "ep1", "1.1.1.1")
	waitForReceived(t, received)

	// The status eventually reports the failure, without the server URL or topic
	assert.Eventually(t, func() bool {
		status, attempted := n.NotificationStatus()
		return attempted && !status.OK
	}, 2*time.Second, 10*time.Millisecond)
	status, _ := n.NotificationStatus()
	assert.Equal(t, "HTTP 403", status.LastError)
	assert.NotContains(t, status.LastError, srv.URL)
	assert.NotContains(t, status.LastError, "my-topic")
}

func TestNtfyNotifier_Timeout(t *testing.T) {
	srv, _ := newTestServer(t, func(_ http.ResponseWriter, _ *http.Request) bool {
		// Slower than the notifier's timeout
		time.Sleep(300 * time.Millisecond)
		return true
	})
	n := &NtfyNotifier{
		server:     srv.URL,
		topic:      "my-topic",
		timeout:    50 * time.Millisecond,
		httpClient: srv.Client(),
	}

	// The send returns immediately even though the server is slow: the caller is never blocked
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.EndpointUp(context.Background(), "example.com", "ep1", "1.1.1.1")
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("notification send blocked the caller")
	}

	assert.Eventually(t, func() bool {
		status, attempted := n.NotificationStatus()
		return attempted && !status.OK
	}, 2*time.Second, 10*time.Millisecond)
	status, _ := n.NotificationStatus()
	assert.Equal(t, "timed out", status.LastError)
}

func TestNtfyNotifier_NoAttemptsYet(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	n := &NtfyNotifier{
		server:     srv.URL,
		topic:      "my-topic",
		timeout:    time.Second,
		httpClient: srv.Client(),
	}

	_, attempted := n.NotificationStatus()
	assert.False(t, attempted, "no delivery has been attempted yet")
}

func TestNewNtfyNotifier(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		_, err := NewNtfyNotifier(&config.ConfigNotifications{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "topic is required")
	})

	t.Run("defaults are applied", func(t *testing.T) {
		n, err := NewNtfyNotifier(&config.ConfigNotifications{Topic: " my-topic "})
		require.NoError(t, err)
		assert.Equal(t, config.DefaultNtfyServer, n.server)
		assert.Equal(t, "my-topic", n.topic)
		assert.Equal(t, 10*time.Second, n.timeout)
		assert.Empty(t, n.token)
	})

	t.Run("custom server is normalized", func(t *testing.T) {
		n, err := NewNtfyNotifier(&config.ConfigNotifications{Topic: "t", Server: "http://localhost:8080//", Timeout: time.Second})
		require.NoError(t, err)
		assert.Equal(t, "http://localhost:8080", n.server)
		assert.Equal(t, time.Second, n.timeout)
	})
}

func TestNtfyNotifier_MessageTruncated(t *testing.T) {
	srv, received := newTestServer(t, nil)
	n := &NtfyNotifier{
		server:     srv.URL,
		topic:      "my-topic",
		timeout:    5 * time.Second,
		httpClient: srv.Client(),
	}

	longErr := errors.New(strings.Repeat("x", maxMessageLength+100))
	n.EndpointDown(context.Background(), "example.com", "ep1", "1.1.1.1", longErr)
	req := waitForReceived(t, received)

	assert.LessOrEqual(t, len(req.body), maxMessageLength, "body is truncated to ntfy's limit")
}

func TestNtfyNotifier_NonASCIITitle(t *testing.T) {
	srv, received := newTestServer(t, nil)
	n := &NtfyNotifier{
		server:     srv.URL,
		topic:      "my-topic",
		timeout:    5 * time.Second,
		httpClient: srv.Client(),
	}

	// Endpoint and domain names come from the config and may contain non-ASCII: the title must be folded to ASCII, while the body keeps the original text
	n.EndpointDown(context.Background(), "bücher.example", "ändpoint", "1.1.1.1", nil)
	req := waitForReceived(t, received)

	assert.Equal(t, "ddup: ?ndpoint is down", req.header.Get("Title"))
	assert.Contains(t, req.body, "bücher.example")
	assert.Contains(t, req.body, "ändpoint")
}

func TestNtfyNotifier_SendDigest(t *testing.T) {
	srv, received := newTestServer(t, nil)
	n := &NtfyNotifier{
		server:     srv.URL,
		topic:      "my-topic",
		timeout:    5 * time.Second,
		httpClient: srv.Client(),
	}

	n.SendDigest(context.Background(), "example.com: healthy, 2/2 endpoints up")
	req := waitForReceived(t, received)

	assert.Equal(t, "ddup: current status", req.header.Get("Title"))
	assert.Equal(t, "default", req.header.Get("Priority"))
	assert.Equal(t, "information_source", req.header.Get("Tags"))
	assert.Equal(t, "example.com: healthy, 2/2 endpoints up", req.body)
}
