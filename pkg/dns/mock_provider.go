//go:build unit

package dns

import (
	"context"
	"errors"
)

// MockProvider is a mock implementation of the Provider interface for testing.
//
//nolint:recvcheck
type MockProvider struct {
	// If true, UpdateRecords will return an error
	ShouldError bool
	CallCount   int
	// Arguments captured from the last UpdateRecords call
	LastDomain   string
	LastTTL      int
	LastIPs      []string
	// Arguments captured from the last UpdateCNAME call
	CNAMETarget  string
	CNAMEUpdated bool
}

// NewMockProvider creates a new MockProvider.
func NewMockProvider(shouldError bool) *MockProvider {
	return &MockProvider{ShouldError: shouldError}
}

// Name implements the Provider interface.
func (m MockProvider) Name() string {
	return "mock"
}

// UpdateRecords implements the Provider interface.
func (m *MockProvider) UpdateRecords(ctx context.Context, domain string, ttl int, ips []string) error {
	m.CallCount++
	m.LastDomain = domain
	m.LastTTL = ttl
	m.LastIPs = ips
	if m.ShouldError {
		return errors.New("mock error")
	}
	return nil
}

// UpdateCNAME implements the CNAMEUpdater interface.
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
