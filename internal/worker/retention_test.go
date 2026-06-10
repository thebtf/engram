package worker

// retention_test.go — TDD tests for audit_log retention in runRetentionCleanup (T005).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAuditRetainer implements auditRetainer for unit tests.
type mockAuditRetainer struct {
	mu      sync.Mutex
	calls   []time.Time // cutoff values passed to DeleteOlderThan
	deleted int64
	err     error
}

func (m *mockAuditRetainer) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, cutoff)
	return m.deleted, m.err
}

// TestRetention_AuditLogCleanedWith90DayCutoff verifies that runRetentionCleanup
// calls DeleteOlderThan on the audit retainer with a cutoff approximately 90 days
// in the past.
func TestRetention_AuditLogCleanedWith90DayCutoff(t *testing.T) {
	mock := &mockAuditRetainer{deleted: 3}
	svc := &Service{testAuditRetainer: mock}

	before := time.Now().Add(-90 * 24 * time.Hour)
	svc.runRetentionCleanup(context.Background())
	after := time.Now().Add(-90 * 24 * time.Hour)

	require.Len(t, mock.calls, 1, "expected exactly one DeleteOlderThan call")
	cutoff := mock.calls[0]
	// cutoff must be between 'before' and 'after' to confirm 90-day window
	assert.True(t, !cutoff.Before(before), "cutoff should be >= 90 days ago at test start")
	assert.True(t, !cutoff.After(after.Add(2*time.Second)), "cutoff should not be in the future")
}

// TestRetention_NilAuditStoreNoPanic verifies that runRetentionCleanup does not
// panic when both auditStore and testAuditRetainer are nil.
func TestRetention_NilAuditStoreNoPanic(t *testing.T) {
	svc := &Service{} // both auditStore and testAuditRetainer are nil
	assert.NotPanics(t, func() {
		svc.runRetentionCleanup(context.Background())
	})
}

// TestRetention_AuditErrorLogged verifies that a DeleteOlderThan error does not
// propagate (fire-and-forget pattern) — the function returns without panicking.
func TestRetention_AuditErrorLogged(t *testing.T) {
	mock := &mockAuditRetainer{err: assert.AnError}
	svc := &Service{testAuditRetainer: mock}

	assert.NotPanics(t, func() {
		svc.runRetentionCleanup(context.Background())
	})
	assert.Len(t, mock.calls, 1, "DeleteOlderThan must still be called even when error returned")
}
