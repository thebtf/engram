package lifecycle

// sleep_audit_test.go — TDD tests for audit logging in the sleep cycle promotion path (T004).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockPromoLogger struct {
	mu      sync.Mutex
	entries []promoEntry
}

type promoEntry struct {
	memoryID         int64
	fromTier, toTier string
	reason           string
}

func (m *mockPromoLogger) LogPromotion(_ context.Context, memoryID int64, fromTier, toTier, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, promoEntry{memoryID, fromTier, toTier, reason})
	return nil
}

type mockAuditLogger struct {
	mu      sync.Mutex
	actions []auditEntry
}

type auditEntry struct {
	memoryID        int64
	action          string
	actor           string
}

func (m *mockAuditLogger) LogAudit(_ context.Context, memoryID int64, action, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.actions = append(m.actions, auditEntry{memoryID, action, actor})
	return nil
}

func (m *mockAuditLogger) waitN(n int) []auditEntry {
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		if len(m.actions) >= n {
			out := make([]auditEntry, len(m.actions))
			copy(out, m.actions)
			m.mu.Unlock()
			return out
		}
		m.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	m.mu.Lock()
	out := make([]auditEntry, len(m.actions))
	copy(out, m.actions)
	m.mu.Unlock()
	return out
}

// mockMemStore satisfies MemoryUpdater with a single batch of memories.
type mockMemStore struct {
	memories []*models.Memory
	calls    int
}

func (m *mockMemStore) ListAllActive(_ context.Context, _ int, offset int) ([]*models.Memory, error) {
	if offset > 0 {
		return nil, nil
	}
	m.calls++
	return m.memories, nil
}

func (m *mockMemStore) UpdateLifecycleFields(_ context.Context, _ int64, _ map[string]any) error {
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestPromotion_AuditLogCalledOnPromotion verifies that RunSleepCycle calls
// auditLog.LogAudit with action="promote" when a memory is promoted.
func TestPromotion_AuditLogCalledOnPromotion(t *testing.T) {
	// Build a memory that qualifies for promotion: episodic → semantic
	// RecurrenceCount=3 ensures ComputeConfidence returns 0.8 (≥0.7 threshold).
	// Without it, ComputeConfidence(all-zero) = 0.5 which fails the promotion
	// criteria check in EvaluatePromotion, giving promotions=0.
	mem := &models.Memory{
		ID:              101,
		Tier:            TierEpisodic,
		AccessCount:     5,
		RecurrenceCount: 3,
		Confidence:      0.9,
		Retrievability:  0.95,
		Stability:       30.0,
		CreatedAt:       time.Now().Add(-24 * time.Hour),
	}

	store := &mockMemStore{memories: []*models.Memory{mem}}
	promoLog := &mockPromoLogger{}
	auditLog := &mockAuditLogger{}

	RunSleepCycle(context.Background(), store, promoLog, auditLog)

	entries := auditLog.waitN(1)
	require.Len(t, entries, 1, "expected one audit entry for promotion")
	assert.Equal(t, int64(101), entries[0].memoryID)
	assert.Equal(t, "promote", entries[0].action)
	assert.Equal(t, "system", entries[0].actor)
}

// TestPromotion_AuditLogCalledOnDemotion verifies action="demote" on demotion.
func TestPromotion_AuditLogCalledOnDemotion(t *testing.T) {
	// Build a memory that qualifies for demotion: procedural → semantic
	mem := &models.Memory{
		ID:             202,
		Tier:           TierProcedural,
		Confidence:     0.4, // < 0.6 triggers demotion
		Retrievability: 0.9,
		Stability:      30.0,
		CreatedAt:      time.Now().Add(-24 * time.Hour),
	}

	store := &mockMemStore{memories: []*models.Memory{mem}}
	promoLog := &mockPromoLogger{}
	auditLog := &mockAuditLogger{}

	RunSleepCycle(context.Background(), store, promoLog, auditLog)

	entries := auditLog.waitN(1)
	require.Len(t, entries, 1, "expected one audit entry for demotion")
	assert.Equal(t, int64(202), entries[0].memoryID)
	assert.Equal(t, "demote", entries[0].action)
	assert.Equal(t, "system", entries[0].actor)
}

// TestPromotion_NilAuditLogNoPanic verifies nil auditLog causes no panic.
func TestPromotion_NilAuditLogNoPanic(t *testing.T) {
	mem := &models.Memory{
		ID:             303,
		Tier:           TierEpisodic,
		AccessCount:    5,
		Confidence:     0.9,
		Retrievability: 0.95,
		Stability:      30.0,
		CreatedAt:      time.Now().Add(-24 * time.Hour),
	}

	store := &mockMemStore{memories: []*models.Memory{mem}}
	assert.NotPanics(t, func() {
		RunSleepCycle(context.Background(), store, nil, nil)
	})
}

// TestPromotion_NoAuditOnNoTierChange verifies no audit entry when no tier change occurs.
func TestPromotion_NoAuditOnNoTierChange(t *testing.T) {
	// stable memory with no promotion/demotion criteria met
	mem := &models.Memory{
		ID:             404,
		Tier:           TierEpisodic,
		AccessCount:    1,   // < 3 — does not promote
		Confidence:     0.5, // < 0.7 — does not promote
		Retrievability: 0.9,
		Stability:      30.0,
		CreatedAt:      time.Now().Add(-24 * time.Hour),
	}

	store := &mockMemStore{memories: []*models.Memory{mem}}
	auditLog := &mockAuditLogger{}

	RunSleepCycle(context.Background(), store, nil, auditLog)

	time.Sleep(30 * time.Millisecond)
	assert.Empty(t, auditLog.waitN(0), "no audit entry when no tier change")
}
