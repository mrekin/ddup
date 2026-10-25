package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// Supported DNS record types
const (
	// RecordTypeA manages IPv4 and IPv6 address records (the default; the record type
	// is detected automatically from each endpoint's IP address)
	RecordTypeA = "A"
	// RecordTypeCNAME manages a CNAME record; the endpoints' `ip` field holds the target hostname
	RecordTypeCNAME = "CNAME"
)

// validateRecordType normalizes and validates the record type of a domain
// CNAME records are implemented by the Cloudflare provider only
func validateRecordType(d *ConfigDomain, p ConfigProvider) error {
	d.RecordType = strings.ToUpper(strings.TrimSpace(d.RecordType))
	switch d.RecordType {
	case "", RecordTypeA:
		d.RecordType = RecordTypeA
	case RecordTypeCNAME:
		if p.Cloudflare == nil {
			return fmt.Errorf("domain %s is invalid: recordType '%s' is only supported by the Cloudflare provider, but provider '%s' is not a Cloudflare provider", d.RecordName, d.RecordType, d.Provider)
		}
	default:
		return fmt.Errorf("domain %s is invalid: recordType must be %q or %q (got '%s')", d.RecordName, RecordTypeA, RecordTypeCNAME, d.RecordType)
	}
	return nil
}

// validateEndpointIP validates and normalizes an endpoint's `ip` value
// For A-record domains it must be an IP address; for CNAME domains it is the target hostname
func validateEndpointIP(d *ConfigDomain, ei int, v *ConfigEndpoint) error {
	if d.RecordType == RecordTypeCNAME {
		// A CNAME target must be a hostname, not an IP address
		_, err := netip.ParseAddr(strings.TrimSpace(v.IP))
		if err == nil {
			return fmt.Errorf("domain %s endpoint %d is invalid: ip '%s' is an IP address, which is not a valid target for a CNAME record", d.RecordName, ei, v.IP)
		}
		return nil
	}

	ip, err := netip.ParseAddr(v.IP)
	if err != nil {
		return fmt.Errorf("domain %s endpoint %d is invalid: IP %q is not a valid IPv4 or IPv6 address", d.RecordName, ei, v.IP)
	}
	v.IP = ip.String()
	return nil
}
