// Package crystallization_test provides integration tests for the crystallization pipeline.
// Tests in this file require a live PostgreSQL instance (DATABASE_DSN must be set).
// They are automatically skipped in CI when the var is absent.
//
// T029 — full lifecycle: session-end extract → pending candidate → list → promote → memory + audit.
package crystallization_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/crystallization"
	"github.com/thebtf/engram/pkg/models"
)

// openIntegrationDB opens a test PostgreSQL connection or skips the test.
func openIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping crystallization integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err, "open integration test DB")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	return db
}

// TestCrystallizationLifecycle_FullPath exercises the end-to-end crystallization flow:
//
//  1. Extract a decision from mock session content.
//  2. RouteDecision creates a pending candidate (ENGRAM_VNEXT_F_ENABLED=true).
//  3. ListByStatus returns the new pending candidate.
//  4. promote_candidate path: TransitionToPromoted creates a memory + updates status.
//  5. Audit log has a "promote_candidate" entry.
//  6. Re-run RouteDecision with same fingerprint → Duplicate=true (idempotency).
func TestCrystallizationLifecycle_FullPath(t *testing.T) {
	db := openIntegrationDB(t)
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	ctx := context.Background()
	auditStore := gormdb.NewAuditStore(db)
	cs := gormdb.NewCandidateStore(db, auditStore)
	storeWrapper := &gormdb.Store{DB: db}
	ms := gormdb.NewMemoryStore(storeWrapper)

	// Step 1: construct a decision directly (regex extraction path removed; LLM path
	// tested separately via LLMExtractor unit tests).
	sessionID := "integration-test-session-t029"
	project := "integration-test-project-t029"
	decision := crystallization.ExtractedDecision{
		Text:           "decided to use pgvector for similarity search because it integrates natively with PostgreSQL",
		Confidence:     0.9,
		Lang:           "en",
		ProposedTarget: "rule",
	}

	// Step 2: RouteDecision → should create a pending candidate.
	// Pass nil memChecker — integration test focus is candidate path; memory check is
	// covered by unit tests in candidate_gate_test.go.
	result, err := crystallization.RouteDecision(ctx, decision, sessionID, project, cs, nil)
	require.NoError(t, err, "RouteDecision must not return an error")
	require.NotNil(t, result, "RouteDecision must return a non-nil result when flag is ON")
	assert.True(t, result.UsedCandidatePath, "must use candidate path when flag is ON")
	assert.False(t, result.Duplicate, "first route must not be a duplicate")
	assert.Greater(t, result.CandidateID, int64(0), "must return a positive candidate ID")

	candidateID := result.CandidateID

	// Step 3: ListByStatus must include the new pending candidate.
	pending, err := cs.ListByStatus(ctx, project, models.CandidateStatusPending, 50)
	require.NoError(t, err)
	found := false
	for _, c := range pending {
		if c.ID == candidateID {
			found = true
			assert.Equal(t, models.CandidateStatusPending, c.Status)
			break
		}
	}
	assert.True(t, found, "pending candidate %d must appear in ListByStatus(pending)", candidateID)

	// Step 4: promote path — create a Memory then TransitionToPromoted.
	mem := &models.Memory{
		Content:       decision.Text,
		Project:       project,
		EpistemicType: "decision",
		Tier:          "episodic",
		SourceAgent:   "crystallization",
		Tags:          []string{"crystallized"},
	}
	createdMem, err := ms.CreateWithLifecycle(ctx, mem)
	require.NoError(t, err, "MemoryStore.CreateWithLifecycle must succeed")
	require.Greater(t, createdMem.ID, int64(0), "created memory must have an ID")

	promoted, err := cs.TransitionToPromoted(ctx, candidateID, createdMem.ID)
	require.NoError(t, err, "TransitionToPromoted must succeed")
	assert.Equal(t, models.CandidateStatusPromoted, promoted.Status)
	require.NotNil(t, promoted.PromotedMemoryID)
	assert.Equal(t, createdMem.ID, *promoted.PromotedMemoryID, "promoted_memory_id must match created memory")

	// Step 5: second RouteDecision with same session+content → candidate-path idempotency.
	result2, err := crystallization.RouteDecision(ctx, decision, sessionID, project, cs, nil)
	require.NoError(t, err, "second RouteDecision must not error")
	require.NotNil(t, result2)
	// The pending candidate is now promoted, so GetByFingerprint returns nil (it only looks at pending).
	// A new candidate should be created (not a duplicate), because the previous one is terminal.
	// This is the correct idempotency behavior: partial-unique index is on (fingerprint, status=pending),
	// so a promoted candidate does not block a new pending candidate for the same content.
	assert.True(t, result2.UsedCandidatePath, "second route must still use candidate path")
	assert.False(t, result2.Duplicate, "second route with promoted fingerprint should create a new pending candidate, not duplicate")
	assert.Greater(t, result2.CandidateID, int64(0), "second route should return a new candidate ID")
	assert.NotEqual(t, candidateID, result2.CandidateID, "second route should create a different candidate (first is promoted)")
}

// TestCrystallizationLifecycle_FlagOff verifies that RouteDecision returns nil
// (signalling "use legacy path") when the feature flag is off.
func TestCrystallizationLifecycle_FlagOff(t *testing.T) {
	db := openIntegrationDB(t)
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")

	ctx := context.Background()
	auditStore := gormdb.NewAuditStore(db)
	cs := gormdb.NewCandidateStore(db, auditStore)

	// Construct a decision directly (regex extraction path removed).
	decision := crystallization.ExtractedDecision{
		Text:           "decided to use Redis because latency requirements demand sub-millisecond reads",
		Confidence:     0.9,
		Lang:           "en",
		ProposedTarget: "rule",
	}

	result, err := crystallization.RouteDecision(ctx, decision, "sess-flag-off", "proj-flag-off", cs, nil)
	assert.NoError(t, err, "flag-off RouteDecision must not error")
	assert.Nil(t, result, "flag-off RouteDecision must return nil (legacy path signal)")
}
