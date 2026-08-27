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
	LastDomain     string
	LastRecordType string
	LastTTL        int
	LastValues     []string
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
func (m *MockProvider) UpdateRecords(ctx context.Context, domain string, recordType string, ttl int, values []string) error {
	m.CallCount++
	m.LastDomain = domain
	m.LastRecordType = recordType
	m.LastTTL = ttl
	m.LastValues = values
	if m.ShouldError {
		return errors.New("mock error")
	}
	return nil
}
