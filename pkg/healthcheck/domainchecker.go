package healthcheck

import (
	"sync"
	"time"

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
}

func (dc *domainChecker) getState() (healthyIPs []string, failedIPs map[string]int, dnsValues []string, lastUpdated time.Time, lastError string) {
	dc.lock.Lock()
	defer dc.lock.Unlock()

	return dc.healthyIPs, dc.failedIPs, dc.dnsValues, dc.lastUpdated, dc.lastError
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
