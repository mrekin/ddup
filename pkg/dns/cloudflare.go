package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
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

// UpdateRecords updates DNS records for the given domain with the provided IPs
func (c *CloudflareProvider) UpdateRecords(ctx context.Context, domain string, ttl int, ips []string) (UpdateResult, error) {
	canonicalIPs, err := canonicalizeIPs(ips)
	if err != nil {
		return UpdateResult{}, err
	}

	// First, get existing A and AAAA records.
	existingRecords, err := c.getExistingRecords(ctx, domain)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("error getting existing records: %w", err)
	}

	// Map of existing IPs and record IDs
	existingIPs := make(map[string]string)
	for _, record := range existingRecords {
		ip, err := canonicalizeIP(record.Content)
		if err != nil {
			return UpdateResult{}, fmt.Errorf("invalid IP address in existing record %s: %w", record.ID, err)
		}
		existingIPs[ip] = record.ID
	}

	result := UpdateResult{
		Previous: slices.Sorted(maps.Keys(existingIPs)),
	}

	// Map of IPs we want to preserve
	desiredIPs := make(map[string]struct{})
	for _, ip := range canonicalIPs {
		desiredIPs[ip] = struct{}{}
	}

	// Create replacements before removing stale records so a failed create does not leave the domain empty.
	createdIPs := make(map[string]struct{})
	for _, ip := range canonicalIPs {
		_, exists := existingIPs[ip]
		_, created := createdIPs[ip]
		if exists || created {
			continue
		}

		slog.DebugContext(ctx, "Creating record for healthy IP", "ip", ip)

		recordType, err := recordTypeForIP(ip)
		if err != nil {
			return UpdateResult{}, fmt.Errorf("invalid IP address %s: %w", ip, err)
		}
		err = c.createRecord(ctx, domain, recordType, ip, ttl)
		if err != nil {
			return UpdateResult{}, fmt.Errorf("error creating record for IP %s: %w", ip, err)
		}
		createdIPs[ip] = struct{}{}
		result.Changed = true
	}

	// Delete records for IPs that are no longer healthy.
	for ip, recordID := range existingIPs {
		_, ok := desiredIPs[ip]
		if ok {
			continue
		}

		slog.DebugContext(ctx, "Deleting record for unhealthy IP", "ip", ip, "recordID", recordID)

		err = c.deleteRecord(ctx, recordID)
		if err != nil {
			return UpdateResult{}, fmt.Errorf("error deleting record %s for IP %s: %w", recordID, ip, err)
		}
		result.Changed = true
	}

	return result, nil
}

// UpdateCNAME updates the CNAME record for the given domain, pointing it at target
// It implements the CNAMEUpdater interface
func (c *CloudflareProvider) UpdateCNAME(ctx context.Context, domain string, ttl int, target string) error {
	// List all records at the host, of any type
	// A type-filtered list would miss conflicting A/AAAA records, which Cloudflare refuses to overwrite with a CNAME (error 81053)
	existingRecords, err := c.listRecords(ctx, domain, "")
	if err != nil {
		return fmt.Errorf("error getting existing records: %w", err)
	}

	// Classify the records: the CNAME to update in place, duplicate CNAMEs, and A/AAAA records that cannot coexist with a CNAME
	var hasCNAME bool
	var cnameID, cnameContent string
	var duplicateCNAMEIDs, conflictingIDs []string
	for _, record := range existingRecords {
		switch record.Type {
		case config.RecordTypeCNAME:
			if hasCNAME {
				duplicateCNAMEIDs = append(duplicateCNAMEIDs, record.ID)
				continue
			}
			hasCNAME = true
			cnameID = record.ID
			cnameContent = record.Content
		case recordTypeA, recordTypeAAAA:
			conflictingIDs = append(conflictingIDs, record.ID)
		}
	}

	if hasCNAME {
		// Update the existing record in place: creating a second CNAME for the host is rejected by Cloudflare (error 81053)
		if cnameContent != target {
			slog.DebugContext(ctx, "Updating CNAME record for healthy target", "domain", domain, "target", target, "recordID", cnameID)

			err = c.updateRecord(ctx, cnameID, domain, config.RecordTypeCNAME, target, ttl)
			if err != nil {
				return fmt.Errorf("error updating record %s for target %s: %w", cnameID, target, err)
			}
		}

		// Delete duplicate CNAME records
		for _, recordID := range duplicateCNAMEIDs {
			slog.DebugContext(ctx, "Deleting duplicate CNAME record", "domain", domain, "recordID", recordID)

			err = c.deleteRecord(ctx, recordID)
			if err != nil {
				return fmt.Errorf("error deleting record %s: %w", recordID, err)
			}
		}

		return nil
	}

	// A/AAAA records must be removed before a CNAME can be created for the host
	for _, recordID := range conflictingIDs {
		slog.DebugContext(ctx, "Deleting record conflicting with CNAME", "domain", domain, "recordID", recordID)

		err = c.deleteRecord(ctx, recordID)
		if err != nil {
			return fmt.Errorf("error deleting record %s: %w", recordID, err)
		}
	}

	slog.DebugContext(ctx, "Creating CNAME record for healthy target", "domain", domain, "target", target)

	err = c.createRecord(ctx, domain, config.RecordTypeCNAME, target, ttl)
	if err != nil {
		return fmt.Errorf("error creating record for target %s: %w", target, err)
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

func (c *CloudflareProvider) getExistingRecords(ctx context.Context, domain string) ([]CloudflareRecord, error) {
	var records []CloudflareRecord

	for _, recordType := range []string{recordTypeA, recordTypeAAAA} {
		typeRecords, err := c.listRecords(ctx, domain, recordType)
		if err != nil {
			return nil, err
		}

		records = append(records, typeRecords...)
	}

	return records, nil
}

// listRecords lists the DNS records for the given domain
// An empty recordType lists records of any type at the host
func (c *CloudflareProvider) listRecords(ctx context.Context, domain string, recordType string) ([]CloudflareRecord, error) {
	start := time.Now()
	var success bool

	if c.metrics != nil {
		defer func() {
			c.metrics.RecordAPICall(
				"cloudflare",
				http.MethodGet,
				fmt.Sprintf("/v4/zones/%s/dns_records", c.zoneID),
				success,
				time.Since(start),
			)
		}()
	}

	url := fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/zones/%s/dns_records?name=%s",
		c.zoneID,
		domain,
	)
	if recordType != "" {
		url += "&type=" + recordType
	}
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

// updateRecord updates an existing DNS record in place via PATCH, which preserves its settings
func (c *CloudflareProvider) updateRecord(ctx context.Context, recordID, domain, recordType, content string, ttl int) error {
	start := time.Now()
	var success bool
	if c.metrics != nil {
		defer func() {
			c.metrics.RecordAPICall("cloudflare", http.MethodPatch, fmt.Sprintf("/v4/zones/%s/dns_records", c.zoneID), success, time.Since(start))
		}()
	}

	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", c.zoneID, recordID)

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
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, url, bytes.NewReader(jsonData))
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

func (c *CloudflareProvider) createRecord(ctx context.Context, domain, recordType, content string, ttl int) error {
	start := time.Now()
	var success bool
	if c.metrics != nil {
		defer func() {
			c.metrics.RecordAPICall("cloudflare", http.MethodPost, fmt.Sprintf("/v4/zones/%s/dns_records", c.zoneID), success, time.Since(start))
		}()
	}

	url := fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/zones/%s/dns_records",
		c.zoneID,
	)

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
