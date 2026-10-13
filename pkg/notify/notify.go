// Package notify sends notifications about health check and DNS events to external services
package notify

import (
	"context"
	"time"
)

// Notifier receives events from the health checker and delivers them externally
// Implementations must not block the caller and must not return errors: delivery happens asynchronously and failures are logged by the implementation
type Notifier interface {
	// EndpointDown is called when an endpoint was removed from the healthy list after all retries were exhausted
	EndpointDown(ctx context.Context, domain string, endpoint string, ip string, err error)
	// EndpointUp is called when an endpoint re-enters the healthy list, after the recovery period if configured
	EndpointUp(ctx context.Context, domain string, endpoint string, ip string)
	// DNSUpdated is called after DNS records were changed, with the new and previous values
	DNSUpdated(ctx context.Context, domain string, recordType string, values []string, previous []string)
	// DNSUpdateFailed is called when updating DNS records failed
	DNSUpdateFailed(ctx context.Context, domain string, recordType string, err error)
}

// Status describes the outcome of the most recent notification attempts
// It never contains the destination server or topic, so it's safe to expose publicly
type Status struct {
	// OK is true if the most recent attempt was delivered successfully
	OK bool `json:"ok"`
	// LastAttempt is the time of the most recent delivery attempt
	LastAttempt time.Time `json:"lastAttempt"`
	// LastSuccess is the time of the most recent successful delivery
	LastSuccess time.Time `json:"lastSuccess,omitempty"`
	// LastError is a generic description of the most recent failure, such as "HTTP 403"
	LastError string `json:"lastError,omitempty"`
}

// StatusReporter exposes the delivery status of notifications
type StatusReporter interface {
	// NotificationStatus returns the current status; the second return value is false if no delivery was attempted yet
	NotificationStatus() (Status, bool)
}

// Service is the notifications integration used by the server: the delivery status, plus manually-triggered notifications
type Service interface {
	StatusReporter
	// SendDigest publishes a summary of the current status of all domains, triggered manually from the dashboard
	SendDigest(ctx context.Context, digest string)
}
