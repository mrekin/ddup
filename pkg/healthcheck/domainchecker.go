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
}

func (dc *domainChecker) setError(err string) {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	dc.lastUpdated = time.Now()
	dc.lastError = err
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
