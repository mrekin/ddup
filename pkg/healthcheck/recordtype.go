package healthcheck

import (
	"context"
	"fmt"

	"github.com/italypaleale/ddup/pkg/config"
	"github.com/italypaleale/ddup/pkg/dns"
)

// publishedValues returns the values that would be written to the DNS record for a set of healthy endpoints:
// the ones with the lowest priority, restricted to a single value for CNAME records
func (dc *domainChecker) publishedValues(healthy []string) []string {
	values := selectLowestPriority(dc.priorities, healthy)
	if dc.recordType == config.RecordTypeCNAME && len(values) > 1 {
		values = values[:1]
	}
	return values
}

// updateDNSValues writes the published values to the domain's DNS record, dispatching by record type
func (dc *domainChecker) updateDNSValues(ctx context.Context, publishIPs, previousPublishIPs []string) (dns.UpdateResult, error) {
	if dc.recordType != config.RecordTypeCNAME {
		// The error is reported as-is by the caller, which logs it with the domain context
		return dc.provider.UpdateRecords(ctx, dc.checker.GetDomain(), dc.ttl, publishIPs) //nolint:wrapcheck
	}

	// CNAME records are updated via the optional CNAMEUpdater interface
	cu, ok := dc.provider.(dns.CNAMEUpdater)
	if !ok {
		return dns.UpdateResult{}, fmt.Errorf("provider '%s' does not support CNAME records", dc.provider.Name())
	}
	err := cu.UpdateCNAME(ctx, dc.checker.GetDomain(), dc.ttl, publishIPs[0])
	if err != nil {
		// The error is reported as-is by the caller, which logs it with the domain context
		return dns.UpdateResult{}, err //nolint:wrapcheck
	}

	// CNAME updaters don't report whether anything changed; we only get here when the values differ
	return dns.UpdateResult{Changed: true, Previous: previousPublishIPs}, nil
}
