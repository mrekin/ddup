package server

import (
	"slices"
	"time"

	"github.com/italypaleale/ddup/pkg/healthcheck"
)

// Values for the "status" field of summary domains
const (
	domainStatusHealthy   = "healthy"
	domainStatusWarning   = "warning"
	domainStatusUnhealthy = "unhealthy"
)

// StatusSummary is the aggregated status of all domains, computed server-side with the same information rendered by the web dashboard
type StatusSummary struct {
	Timestamp time.Time       `json:"timestamp"`
	Summary   SummaryTotals   `json:"summary"`
	Domains   []SummaryDomain `json:"domains"`
}

// SummaryTotals contains the counters displayed in the dashboard's stats overview
type SummaryTotals struct {
	TotalDomains     int `json:"totalDomains"`
	HealthyDomains   int `json:"healthyDomains"`
	WarningDomains   int `json:"warningDomains"`
	UnhealthyDomains int `json:"unhealthyDomains"`
}

// SummaryDomain is the status of a single domain, as displayed in the dashboard's domain cards
type SummaryDomain struct {
	Name             string                             `json:"name"`
	Status           string                             `json:"status"`
	Provider         string                             `json:"provider"`
	LastUpdated      time.Time                          `json:"lastUpdated"`
	Error            string                             `json:"error,omitempty"`
	EndpointsHealthy int                                `json:"endpointsHealthy"`
	EndpointsTotal   int                                `json:"endpointsTotal"`
	Endpoints        []healthcheck.DomainStatusEndpoint `json:"endpoints"`
}

// getStatusSummary computes the StatusSummary from the raw status of all domains
func getStatusSummary(statuses map[string]healthcheck.DomainStatus) StatusSummary {
	names := make([]string, 0, len(statuses))
	for name := range statuses {
		names = append(names, name)
	}
	// Sort domain names so the response is deterministic
	slices.Sort(names)

	res := StatusSummary{
		Timestamp: time.Now(),
		Summary: SummaryTotals{
			TotalDomains: len(names),
		},
		Domains: make([]SummaryDomain, 0, len(names)),
	}

	for _, name := range names {
		status := statuses[name]

		healthy := 0
		for _, ep := range status.Endpoints {
			if ep.Healthy {
				healthy++
			}
		}

		// Classify the domain with the same logic used by the dashboard:
		// domains with an error or no endpoints are unhealthy, then it depends on the number of healthy endpoints
		var state string
		switch {
		case status.Error != "" || len(status.Endpoints) == 0:
			state = domainStatusUnhealthy
		case healthy == len(status.Endpoints):
			state = domainStatusHealthy
		case healthy > 0:
			state = domainStatusWarning
		default:
			state = domainStatusUnhealthy
		}

		switch state {
		case domainStatusHealthy:
			res.Summary.HealthyDomains++
		case domainStatusWarning:
			res.Summary.WarningDomains++
		default:
			res.Summary.UnhealthyDomains++
		}

		res.Domains = append(res.Domains, SummaryDomain{
			Name:             name,
			Status:           state,
			Provider:         status.Provider,
			LastUpdated:      status.LastUpdated,
			Error:            status.Error,
			EndpointsHealthy: healthy,
			EndpointsTotal:   len(status.Endpoints),
			Endpoints:        status.Endpoints,
		})
	}

	return res
}
