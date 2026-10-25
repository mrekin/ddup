package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/italypaleale/ddup/pkg/config"
)

// CNAMEUpdater is an optional interface that providers can implement to support CNAME records
type CNAMEUpdater interface {
	// UpdateCNAME updates the CNAME record for the given domain, pointing it at target
	UpdateCNAME(ctx context.Context, domain string, ttl int, target string) error
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
