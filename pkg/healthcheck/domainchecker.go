package healthcheck

import (
	"maps"
	"sync"
	"time"

	"github.com/italypaleale/ddup/pkg/config"
	"github.com/italypaleale/ddup/pkg/dns"
	"github.com/italypaleale/ddup/pkg/healthcheck/checker"
)

type domainChecker struct {
	lock sync.Mutex
	// checker performs the health checks for the domain's endpoints
	checker checker.Checker
	ttl     int
	// recordType is the type of DNS record, e.g. "A" or "CNAME"
	recordType string
	// healthyIPs contains all endpoints that passed the last health check
	healthyIPs []string
	// dnsValues contains the values currently written in the DNS record.
	// For CNAME records, this is only the first healthy endpoint
	dnsValues   []string
	failedIPs   map[string]int
	provider    dns.Provider
	lastUpdated time.Time
	lastError   string

	// Consecutive successful checks for endpoints that were removed and are recovering
	recovering map[string]int

	// Priority of each endpoint, by IP
	priorities map[string]int

	// True if no endpoint is healthy, and the all_unhealthy notification was sent
	allUnhealthy bool

	// Error of the latest failed DNS update, and the dns_update_failed notification was sent
	notifiedError string

	// True if the last update of the DNS records failed, so the next check tries again even if the published IPs did not change
	// Only used by the health check loop
	updatePending bool
}

func (dc *domainChecker) getState() (healthyIPs []string, failedIPs map[string]int, dnsValues []string, lastUpdated time.Time, lastError string) {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	return dc.healthyIPs, dc.failedIPs, dc.dnsValues, dc.lastUpdated, dc.lastError
}

// publishedValues returns the values that would be written to the DNS record for a set of healthy endpoints:
// the ones with the lowest priority, restricted to a single value for CNAME records
func (dc *domainChecker) publishedValues(healthy []string) []string {
	values := selectLowestPriority(dc.priorities, healthy)
	if dc.recordType == config.RecordTypeCNAME && len(values) > 1 {
		values = values[:1]
	}
	return values
}

func (dc *domainChecker) setState(healthyIPs []string, dnsValues []string, failedIPs map[string]int) {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	dc.healthyIPs = healthyIPs
	dc.dnsValues = dnsValues
	dc.failedIPs = failedIPs
	dc.lastUpdated = time.Now()
	dc.lastError = ""
	dc.updatePending = false
}

// setStateFailed records a cycle whose DNS update failed: the endpoint state is refreshed, while the DNS values are unchanged and the update is retried on the next cycle
func (dc *domainChecker) setStateFailed(healthyIPs []string, failedIPs map[string]int, updateErr error) {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	dc.healthyIPs = healthyIPs
	dc.failedIPs = failedIPs
	dc.lastUpdated = time.Now()
	dc.lastError = "Error updating DNS records: " + updateErr.Error()
	dc.updatePending = true
}

// getRecovering returns a copy of the recovery counters
func (dc *domainChecker) getRecovering() map[string]int {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	res := make(map[string]int, len(dc.recovering))
	maps.Copy(res, dc.recovering)
	return res
}

func (dc *domainChecker) setRecovering(r map[string]int) {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	dc.recovering = r
}

// swapAllUnhealthy sets whether the domain has no healthy endpoints, and returns the previous value
func (dc *domainChecker) swapAllUnhealthy(v bool) bool {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	prev := dc.allUnhealthy
	dc.allUnhealthy = v
	return prev
}

// swapNotifiedError sets the error of the latest failed DNS update (empty if the update succeeded), and returns the previous value
func (dc *domainChecker) swapNotifiedError(v string) string {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	prev := dc.notifiedError
	dc.notifiedError = v
	return prev
}
