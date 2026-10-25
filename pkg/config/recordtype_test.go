package config

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for validateRecordType and validateEndpointIP
func TestValidateRecordType(t *testing.T) {
	// Returns a valid config, which each test case changes
	newConfig := func() *Config {
		return &Config{
			Providers: map[string]ConfigProvider{"test": {Cloudflare: &CloudflareConfig{}}},
			Domains: []ConfigDomain{{
				RecordName: "test.example.com",
				Provider:   "test",
				Endpoints:  []*ConfigEndpoint{{URL: "https://test.example.com/health", IP: "192.0.2.1"}},
			}},
		}
	}

	errorTests := []struct {
		name      string
		mutate    func(c *Config)
		expectErr string
	}{
		{
			name: "CNAME record type with a non-Cloudflare provider",
			mutate: func(c *Config) {
				c.Providers["test"] = ConfigProvider{OVH: &OVHConfig{}}
				c.Domains[0].RecordType = "CNAME"
			},
			expectErr: "recordType 'CNAME' is only supported by the Cloudflare provider, but provider 'test' is not a Cloudflare provider",
		},
		{
			name:      "unknown record type",
			mutate:    func(c *Config) { c.Domains[0].RecordType = "TXT" },
			expectErr: `recordType must be "A" or "CNAME" (got 'TXT')`,
		},
		{
			name:      "CNAME target that is an IP address",
			mutate:    func(c *Config) { c.Domains[0].RecordType = "CNAME" },
			expectErr: "ip '192.0.2.1' is an IP address, which is not a valid target for a CNAME record",
		},
	}

	for _, tc := range errorTests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newConfig()
			tc.mutate(cfg)
			err := cfg.Validate(slog.Default())
			require.ErrorContains(t, err, tc.expectErr)
		})
	}

	t.Run("recordType CNAME is normalized", func(t *testing.T) {
		cfg := newConfig()
		cfg.Domains[0].RecordType = "cname"
		// With a CNAME record, the endpoints' IP field holds the target hostname
		cfg.Domains[0].Endpoints[0].IP = "target.example.com"

		err := cfg.Validate(slog.Default())
		require.NoError(t, err)
		assert.Equal(t, RecordTypeCNAME, cfg.Domains[0].RecordType)
	})
}
