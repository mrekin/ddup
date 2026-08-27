package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/italypaleale/ddup/pkg/config"
	appmetrics "github.com/italypaleale/ddup/pkg/metrics"
)

// CloudflareProvider implements the Provider interface for Cloudflare DNS
type CloudflareProvider struct {
	name       string
	apiToken   string
	zoneID     string
	metrics    *appmetrics.AppMetrics
	httpClient *http.Client
}

// NewCloudflareProvider creates a new Cloudflare DNS provider
func NewCloudflareProvider(name string, cfg *config.CloudflareConfig, metrics *appmetrics.AppMetrics) (*CloudflareProvider, error) {
	if cfg.APIToken == "" {
		return nil, errors.New("API token is required")
	}
	if cfg.ZoneID == "" {
		return nil, errors.New("zone ID is required")
	}

	return &CloudflareProvider{
		name:       name,
		apiToken:   cfg.APIToken,
		zoneID:     cfg.ZoneID,
		metrics:    metrics,
		httpClient: http.DefaultClient,
	}, nil
}

// Name returns the provider's name
func (c *CloudflareProvider) Name() string {
	return c.name
}

// UpdateRecords updates DNS records of the given type for the given domain.
// values holds the record contents: IP addresses for A records, target hostnames for CNAME records.
func (c *CloudflareProvider) UpdateRecords(ctx context.Context, domain string, recordType string, ttl int, values []string) error {
	switch recordType {
	case config.RecordTypeA, config.RecordTypeCNAME:
	default:
		return fmt.Errorf("unsupported record type '%s'", recordType)
	}

	// First, get existing records
	existingRecords, err := c.getExistingRecords(ctx, domain, recordType)
	if err != nil {
		return fmt.Errorf("error getting existing records: %w", err)
	}

	// Map of existing values and record IDs
	existingValues := make(map[string]string)
	for _, record := range existingRecords {
		existingValues[record.Content] = record.ID
	}

	// Map of values we want to preserve
	desiredValues := make(map[string]struct{})
	for _, value := range values {
		desiredValues[value] = struct{}{}
	}

	// Delete records for values that are no longer healthy
	for value, recordID := range existingValues {
		_, ok := desiredValues[value]
		if ok {
			continue
		}

		slog.DebugContext(ctx, "Deleting record for unhealthy value", "value", value, "recordID", recordID)

		err = c.deleteRecord(ctx, recordID)
		if err != nil {
			return fmt.Errorf("error deleting record %s for value %s: %w", recordID, value, err)
		}
	}

	// Create new records for healthy values that don't exist yet
	for _, value := range values {
		_, exists := existingValues[value]
		if exists {
			continue
		}

		slog.DebugContext(ctx, "Creating record for healthy value", "type", recordType, "value", value)

		err = c.createRecord(ctx, domain, recordType, value, ttl)
		if err != nil {
			return fmt.Errorf("error creating record for value %s: %w", value, err)
		}
	}

	return nil
}

// CloudflareRecord represents a DNS record from Cloudflare API
type CloudflareRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
}

// CloudflareResponse represents the response structure from Cloudflare API
type CloudflareResponse struct {
	Success bool               `json:"success"`
	Errors  []CloudflareError  `json:"errors"`
	Result  []CloudflareRecord `json:"result"`
}

// CloudflareError represents an error from Cloudflare API
type CloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// String implements fmt.Stringer
func (ce CloudflareError) String() string {
	return fmt.Sprintf("(%d) %s", ce.Code, ce.Message)
}

func (c *CloudflareProvider) getExistingRecords(ctx context.Context, domain string, recordType string) ([]CloudflareRecord, error) {
	start := time.Now()
	var success bool
	if c.metrics != nil {
		defer func() {
			c.metrics.RecordAPICall("cloudflare", http.MethodGet, fmt.Sprintf("/v4/zones/%s/dns_records", c.zoneID), success, time.Since(start))
		}()
	}

	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?name=%s&type=%s", c.zoneID, domain, recordType)
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	var cfResp CloudflareResponse
	err = json.NewDecoder(resp.Body).Decode(&cfResp)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	if !cfResp.Success {
		return nil, fmt.Errorf("API error: %v", cfResp.Errors)
	}

	success = true
	return cfResp.Result, nil
}

func (c *CloudflareProvider) deleteRecord(ctx context.Context, recordID string) error {
	start := time.Now()
	var success bool
	if c.metrics != nil {
		defer func() {
			c.metrics.RecordAPICall("cloudflare", http.MethodDelete, fmt.Sprintf("/v4/zones/%s/dns_records", c.zoneID), success, time.Since(start))
		}()
	}

	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", c.zoneID, recordID)
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("invalid response status code HTTP %d; response: %s", resp.StatusCode, string(body))
	}

	success = true
	return nil
}

func (c *CloudflareProvider) createRecord(ctx context.Context, domain string, recordType string, content string, ttl int) error {
	start := time.Now()
	var success bool
	if c.metrics != nil {
		defer func() {
			c.metrics.RecordAPICall("cloudflare", http.MethodPost, fmt.Sprintf("/v4/zones/%s/dns_records", c.zoneID), success, time.Since(start))
		}()
	}

	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records", c.zoneID)

	record := map[string]any{
		"type":    recordType,
		"name":    domain,
		"content": content,
		"ttl":     ttl,
	}

	jsonData, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("error marshalling request body: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("invalid response status code HTTP %d; response: %s", resp.StatusCode, string(body))
	}

	success = true
	return nil
}
