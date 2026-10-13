package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/italypaleale/ddup/pkg/config"
)

// maxMessageLength is the maximum length of a notification body
// ntfy's limit is 4096 bytes, so we keep some headroom
const maxMessageLength = 4000

// NtfyNotifier is a Notifier that publishes events to an ntfy server, such as ntfy.sh
type NtfyNotifier struct {
	server     string // Without trailing slash
	topic      string
	token      string
	timeout    time.Duration
	httpClient *http.Client

	// Guards lastStatus and attempted, updated from delivery goroutines
	mu         sync.Mutex
	lastStatus Status
	attempted  bool
}

// NewNtfyNotifier creates a new NtfyNotifier from the validated notifications configuration
func NewNtfyNotifier(cfg *config.ConfigNotifications) (*NtfyNotifier, error) {
	if !cfg.IsEnabled() {
		return nil, errors.New("notifications are not configured: topic is required")
	}
	server := cfg.Server
	if server == "" {
		server = config.DefaultNtfyServer
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &NtfyNotifier{
		server:     strings.TrimRight(server, "/"),
		topic:      strings.TrimSpace(cfg.Topic),
		token:      cfg.Token,
		timeout:    timeout,
		httpClient: http.DefaultClient,
	}, nil
}

// EndpointDown implements the Notifier interface.
func (n *NtfyNotifier) EndpointDown(ctx context.Context, domain string, endpoint string, ip string, err error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Domain %s: endpoint %s (%s) failed its health checks and was removed from the DNS rotation", domain, endpoint, ip)
	if err != nil {
		fmt.Fprintf(&b, "\nLast error: %s", err.Error())
	}
	n.send(ctx, fmt.Sprintf("ddup: %s is down", endpoint), "high", "rotating_light", b.String())
}

// EndpointUp implements the Notifier interface.
func (n *NtfyNotifier) EndpointUp(ctx context.Context, domain string, endpoint string, ip string) {
	n.send(ctx, fmt.Sprintf("ddup: %s recovered", endpoint), "default", "white_check_mark",
		fmt.Sprintf("Domain %s: endpoint %s (%s) is healthy again and was added back to the DNS rotation", domain, endpoint, ip))
}

// DNSUpdated implements the Notifier interface.
func (n *NtfyNotifier) DNSUpdated(ctx context.Context, domain string, recordType string, values []string, previous []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Records for %s (%s) were updated\nPrevious: %s\nNew: %s", domain, recordName(recordType), valuesList(previous), valuesList(values))
	n.send(ctx, fmt.Sprintf("ddup: DNS records updated for %s", domain), "default", "arrows_counterclockwise", b.String())
}

// DNSUpdateFailed implements the Notifier interface.
func (n *NtfyNotifier) DNSUpdateFailed(ctx context.Context, domain string, recordType string, err error) {
	n.send(ctx, fmt.Sprintf("ddup: DNS update failed for %s", domain), "high", "warning",
		fmt.Sprintf("Failed to update %s records for %s: %s", recordName(recordType), domain, err.Error()))
}

// SendDigest implements the Service interface.
func (n *NtfyNotifier) SendDigest(ctx context.Context, digest string) {
	n.send(ctx, "ddup: current status", "default", "information_source", digest)
}

// recordName returns the display name for a DNS record type, defaulting to "A" for the empty value
func recordName(recordType string) string {
	if recordType == "" {
		return config.RecordTypeA
	}
	return recordType
}

// valuesList renders a list of values for a message body
func valuesList(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

// send delivers a notification asynchronously: it never blocks the caller, which must not depend on the delivery
func (n *NtfyNotifier) send(ctx context.Context, title string, priority string, tags string, message string) {
	// The health checker's context may be canceled on shutdown while a notification is still in flight
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), n.timeout)
	go func() {
		defer cancel()
		n.doSend(sendCtx, title, priority, tags, message)
	}()
}

func (n *NtfyNotifier) doSend(ctx context.Context, title string, priority string, tags string, message string) {
	if len(message) > maxMessageLength {
		message = message[:maxMessageLength]
	}
	publishURL := n.server + "/" + url.PathEscape(n.topic)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, publishURL, strings.NewReader(message))
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create notification request", "error", err)
		return
	}
	req.Header.Set("User-Agent", "ddup/1.0")
	req.Header.Set("Title", asciiFold(title))
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}

	start := time.Now()
	resp, err := n.httpClient.Do(req)
	n.setResult(start, statusError(err, resp))
	if err != nil {
		slog.ErrorContext(ctx, "Failed to send notification", "error", err)
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		slog.ErrorContext(ctx, "Notification was rejected by the ntfy server", "status", resp.StatusCode, "body", string(body))
		return
	}
	slog.DebugContext(ctx, "Sent notification", "title", title)
}

// asciiFold replaces every non-printable-ASCII rune with '?', since HTTP header values (and ntfy titles) must be ASCII
func asciiFold(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return '?'
		}
		return r
	}, s)
}

// statusError returns a generic description of a delivery failure that never includes the destination URL or topic
func statusError(err error, resp *http.Response) string {
	switch {
	case err == nil && resp.StatusCode >= 200 && resp.StatusCode <= 299:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case resp != nil:
		return fmt.Sprintf("HTTP %d", resp.StatusCode)
	default:
		return "request failed"
	}
}

// setResult records the outcome of a delivery attempt for the dashboard
func (n *NtfyNotifier) setResult(start time.Time, failure string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.attempted = true
	n.lastStatus.LastAttempt = start
	if failure == "" {
		n.lastStatus.OK = true
		n.lastStatus.LastSuccess = time.Now()
		n.lastStatus.LastError = ""
	} else {
		n.lastStatus.OK = false
		n.lastStatus.LastError = failure
	}
}

// NotificationStatus implements the StatusReporter interface.
func (n *NtfyNotifier) NotificationStatus() (Status, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastStatus, n.attempted
}
