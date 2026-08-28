package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/italypaleale/ddup/pkg/healthcheck"
)

func TestGetStatusSummary(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	t.Run("Healthy domain", func(t *testing.T) {
		s := getStatusSummary(map[string]healthcheck.DomainStatus{
			"api.example.com": {
				LastUpdated: now,
				Provider:    "cloudflare",
				Endpoints: []healthcheck.DomainStatusEndpoint{
					{Healthy: true, IP: "203.0.113.10"},
					{Healthy: true, IP: "203.0.113.11", FailureCount: 2},
				},
			},
		})

		assert.Equal(t, 1, s.Summary.TotalDomains)
		assert.Equal(t, 1, s.Summary.HealthyDomains)
		assert.Equal(t, 0, s.Summary.WarningDomains)
		assert.Equal(t, 0, s.Summary.UnhealthyDomains)
		assert.Len(t, s.Domains, 1)
		assert.Equal(t, SummaryDomain{
			Name:             "api.example.com",
			Status:           domainStatusHealthy,
			Provider:         "cloudflare",
			LastUpdated:      now,
			EndpointsHealthy: 2,
			EndpointsTotal:   2,
			Endpoints: []healthcheck.DomainStatusEndpoint{
				{Healthy: true, IP: "203.0.113.10"},
				{Healthy: true, IP: "203.0.113.11", FailureCount: 2},
			},
		}, s.Domains[0])
	})

	t.Run("Warning domain", func(t *testing.T) {
		s := getStatusSummary(map[string]healthcheck.DomainStatus{
			"api.example.com": {
				Provider: "ovh",
				Endpoints: []healthcheck.DomainStatusEndpoint{
					{Healthy: true, IP: "203.0.113.10"},
					{Healthy: false, IP: "203.0.113.11", FailureCount: 5},
				},
			},
		})

		assert.Equal(t, 1, s.Summary.WarningDomains)
		assert.Equal(t, domainStatusWarning, s.Domains[0].Status)
		assert.Equal(t, 1, s.Domains[0].EndpointsHealthy)
		assert.Equal(t, 2, s.Domains[0].EndpointsTotal)
	})

	t.Run("Unhealthy domains", func(t *testing.T) {
		s := getStatusSummary(map[string]healthcheck.DomainStatus{
			// All endpoints unhealthy
			"api.example.com": {
				Provider: "cloudflare",
				Endpoints: []healthcheck.DomainStatusEndpoint{
					{Healthy: false, IP: "203.0.113.11", FailureCount: 5},
				},
			},
			// With an error
			"db.example.com": {
				Provider: "azure",
				Error:    "Error updating DNS records: oops",
				Endpoints: []healthcheck.DomainStatusEndpoint{
					{Healthy: true, IP: "198.51.100.20"},
				},
			},
			// No endpoints
			"www.example.com": {
				Provider:  "ovh",
				Endpoints: []healthcheck.DomainStatusEndpoint{},
			},
		})

		assert.Equal(t, 3, s.Summary.TotalDomains)
		assert.Equal(t, 0, s.Summary.HealthyDomains)
		assert.Equal(t, 0, s.Summary.WarningDomains)
		assert.Equal(t, 3, s.Summary.UnhealthyDomains)

		// Domains are sorted by name
		assert.Equal(t, []string{"api.example.com", "db.example.com", "www.example.com"},
			[]string{s.Domains[0].Name, s.Domains[1].Name, s.Domains[2].Name})
		assert.Equal(t, domainStatusUnhealthy, s.Domains[0].Status)
		assert.Equal(t, domainStatusUnhealthy, s.Domains[1].Status)
		assert.Equal(t, domainStatusUnhealthy, s.Domains[2].Status)
	})

	t.Run("No domains", func(t *testing.T) {
		s := getStatusSummary(map[string]healthcheck.DomainStatus{})

		assert.Equal(t, 0, s.Summary.TotalDomains)
		assert.Empty(t, s.Domains)
	})
}
