package checker

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/italypaleale/ddup/pkg/config"
	appmetrics "github.com/italypaleale/ddup/pkg/metrics"
)

const (
	DefaultTimeout  = 3 * time.Second
	DefaultAttempts = 2
)

// Checker performs health checks on configured endpoints
type Checker interface {
	CheckAll(ctx context.Context) []Result
	GetDomain() string
	GetMaxAttempts() int
}

// Compile time interface check
var _ Checker = (*checker)(nil)

// concrete implementation of the Checker interface
type checker struct {
	domain    string
	endpoints []*config.ConfigEndpoint
	cfg       config.ConfigHealthChecks
	metrics   *appmetrics.AppMetrics
	client    *http.Client
}

// Result represents the result of a health check
type Result struct {
	Endpoint *config.ConfigEndpoint
	Healthy  bool
	Error    error
	Duration time.Duration
}

// New creates a new health checker
func New(domain string, endpoints []*config.ConfigEndpoint, healthCheckConfig config.ConfigHealthChecks, metrics *appmetrics.AppMetrics) *checker {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Set default config value
	if healthCheckConfig.Timeout <= 0 {
		healthCheckConfig.Timeout = DefaultTimeout
	}
	if healthCheckConfig.Attempts <= 0 {
		healthCheckConfig.Attempts = DefaultAttempts
	}

	return &checker{
		domain:    domain,
		endpoints: endpoints,
		cfg:       healthCheckConfig,
		metrics:   metrics,
		client:    client,
	}
}

// CheckAll performs health checks on all configured endpoints concurrently
func (c *checker) CheckAll(ctx context.Context) []Result {
	var wg sync.WaitGroup
	results := make([]Result, len(c.endpoints))

	for i, endpoint := range c.endpoints {
		wg.Add(1)
		go func(i int, endpoint *config.ConfigEndpoint) {
			defer wg.Done()
			results[i] = c.checkEndpoint(ctx, endpoint)

			if c.metrics != nil {
				c.metrics.RecordHealthCheck(c.domain, endpoint.Name, results[i].Healthy)
			}
		}(i, endpoint)
	}

	wg.Wait()
	return results
}

// GetDomain returns the domain this Checker is configured for
func (c *checker) GetDomain() string {
	return c.domain
}

// GetMaxAttempts returns the maximum number attempts the Checker is configured for
func (c *checker) GetMaxAttempts() int {
	return c.cfg.Attempts
}

// checkEndpoint performs a health check on a single endpoint
func (c *checker) checkEndpoint(ctx context.Context, endpoint *config.ConfigEndpoint) Result {
	start := time.Now()

	// Create a context with timeout for this specific endpoint
	endpointCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	// Create HTTP request
	req, err := http.NewRequestWithContext(endpointCtx, http.MethodGet, endpoint.URL, nil)
	if err != nil {
		return Result{
			Endpoint: endpoint,
			Healthy:  false,
			Error:    fmt.Errorf("creating request: %w", err),
			Duration: time.Since(start),
		}
	}

	// Set user agent
	req.Header.Set("User-Agent", "ddup/1.0")

	// If there's a specific host, we need to set it in the request's host
	// For TLS requests, we set it in the TLS client config so the SNI in the TLS handshake works too.
	// Note the client is cloned: requests for different endpoints run concurrently and must never share
	// a transport with a pinned TLS ServerName, or requests to other endpoints would be sent with the wrong SNI
	client := c.client
	if endpoint.Host != "" {
		req.Host = endpoint.Host

		if req.URL.Scheme == "https" {
			transport := &http.Transport{}
			if t, ok := c.client.Transport.(*http.Transport); ok {
				transport = t.Clone()
			}
			if transport.TLSClientConfig == nil {
				transport.TLSClientConfig = &tls.Config{
					MinVersion: tls.VersionTLS12,
				}
			}
			transport.TLSClientConfig.ServerName = endpoint.Host

			cloned := *c.client
			cloned.Transport = transport
			client = &cloned
		}
	}

	// Perform the request
	resp, err := client.Do(req)
	if err != nil {
		return Result{
			Endpoint: endpoint,
			Healthy:  false,
			Error:    fmt.Errorf("HTTP request failed: %w", err),
			Duration: time.Since(start),
		}
	}
	_ = resp.Body.Close() //nolint:errcheck

	// Check if status code indicates health
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{
			Endpoint: endpoint,
			Healthy:  false,
			Error:    fmt.Errorf("status code %d", resp.StatusCode),
			Duration: time.Since(start),
		}
	}

	return Result{
		Endpoint: endpoint,
		Healthy:  true,
		Error:    nil,
		Duration: time.Since(start),
	}
}
