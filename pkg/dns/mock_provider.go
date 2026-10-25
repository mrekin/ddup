//go:build unit

package dns

import (
	"context"
	"errors"
)

// MockProvider is a mock implementation of the Provider interface for testing.
type MockProvider struct {
	// If true, UpdateRecords will return an error
	ShouldError bool
	CallCount   int
	// If true, UpdateRecords reports that nothing changed
	NoChange bool
	// IPs reported as previously in DNS
	Previous []string
	// IPs passed to the last call of UpdateRecords
	LastIPs []string
	// Arguments captured from the last UpdateRecords call
	LastDomain string
	LastTTL    int
	// Arguments captured from the last UpdateCNAME call
	CNAMETarget  string
	CNAMEUpdated bool
}

// NewMockProvider creates a new MockProvider.
func NewMockProvider(shouldError bool) *MockProvider {
	return &MockProvider{ShouldError: shouldError}
}

// Name implements the Provider interface.
func (m *MockProvider) Name() string {
	return "mock"
}

// UpdateRecords implements the Provider interface.
func (m *MockProvider) UpdateRecords(ctx context.Context, domain string, ttl int, ips []string) (UpdateResult, error) {
	m.CallCount++
	m.LastDomain = domain
	m.LastTTL = ttl
	m.LastIPs = ips
	if m.ShouldError {
		return UpdateResult{}, errors.New("mock error")
	}
	return UpdateResult{Changed: !m.NoChange, Previous: m.Previous}, nil
}

// UpdateCNAME implements the CNAMEUpdater interface
func (m *MockProvider) UpdateCNAME(ctx context.Context, domain string, ttl int, target string) error {
	m.CallCount++
	m.LastDomain = domain
	m.LastTTL = ttl
	m.CNAMETarget = target
	m.CNAMEUpdated = true
	if m.ShouldError {
		return errors.New("mock error")
	}
	return nil
}
