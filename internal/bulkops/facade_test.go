// Package bulkops_test provides TDD tests for the bulk-op facade.
// Engram vNext Milestone F TG6 / T041.
//
// Unit tests cover:
//   - Non-admin callers receive ErrAdminRequired (no DB required — auth gate fires first)
//   - Dry-run paths for all 4 op_types return a preview without any DB writes (no DB required)
//
// Integration tests (skip when DATABASE_DSN is absent) cover:
//   - Committed paths for bulk_delete and bulk_supersede
//   - Audit log is written after committed ops (§FR-F5)
package bulkops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMarshalMemoryRowSnapshot_NormalizesDatabaseSentinelToUTC(t *testing.T) {
	local := time.FixedZone("UTC+3", 3*60*60)
	validUntil := time.Date(10000, time.January, 1, 2, 59, 59, 0, local)
	require.Equal(t, 9999, validUntil.UTC().Year(), "fixture must represent a JSON-safe UTC instant")

	mem := &gormdb.Memory{
		ID:         42,
		Project:    "snapshot-time-normalization",
		Content:    "preserve temporal semantics",
		CreatedAt:  time.Date(2026, time.July, 10, 2, 0, 0, 0, local),
		UpdatedAt:  time.Date(2026, time.July, 10, 2, 1, 0, 0, local),
		ValidUntil: &validUntil,
	}

	raw, err := marshalMemoryRowSnapshot(mem)
	require.NoError(t, err)

	var restored models.Memory
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.NotNil(t, restored.ValidUntil)
	assert.Equal(t, validUntil.UTC(), restored.ValidUntil.UTC())
	assert.Equal(t, mem.CreatedAt.UTC(), restored.CreatedAt)
	assert.Equal(t, mem.UpdatedAt.UTC(), restored.UpdatedAt)
}

// --- helpers ---

func adminIdentity() auth.Identity {
	return auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
}

func operatorIdentity() auth.Identity {
	// "operator" SSO autoprovisioned role is NOT admin (spec CHK010-ADDED).
	return auth.Identity{Role: auth.RoleReadWrite, Source: auth.SourceClient, KeycardID: "operator-test"}
}

func readOnlyIdentity() auth.Identity {
	return auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient}
}

// newNilFacade builds a Facade with all nil stores — valid for auth/dry-run paths only.
func newNilFacade() *Facade {
	return NewFacade(nil, nil, nil, nil)
}

// --- Unit: admin gate ---

// TestFacade_NonAdmin_ReturnsErrAdminRequired verifies all 4 op_types reject non-admin.
// No DB required: admin gate fires before any store access.
func TestFacade_NonAdmin_ReturnsErrAdminRequired(t *testing.T) {
	f := newNilFacade()
	ctx := context.Background()

	nonAdminCases := []struct {
		name     string
		identity auth.Identity
	}{
		{"read_only", readOnlyIdentity()},
		{"operator_rw", operatorIdentity()},
	}

	opTypes := []BulkOpType{
		models.SnapshotOpBulkPromote,
		models.SnapshotOpBulkDelete,
		models.SnapshotOpBulkSupersede,
		models.SnapshotOpIngestDoc,
	}

	for _, nc := range nonAdminCases {
		for _, opType := range opTypes {
			t.Run(nc.name+"/"+string(opType), func(t *testing.T) {
				_, err := f.Execute(ctx, nc.identity, BulkOp{
					Type:      opType,
					DryRun:    false,
					MemoryIDs: []int64{1},
				})
				require.ErrorIs(t, err, ErrAdminRequired,
					"non-admin caller must receive ErrAdminRequired for op %q", opType)
			})
		}
	}
}

// --- Unit: dry-run paths (no DB required) ---

// TestFacade_DryRun_AllOpTypes verifies every op_type returns a preview with DryRun=true
// and no DB mutations (facade has nil stores — any store call would panic).
func TestFacade_DryRun_AllOpTypes(t *testing.T) {
	// snapshotStore must not be nil for non-dryrun paths, but for dryrun all paths
	// return before any store access. We pass nil to prove it.
	f := newNilFacade()
	ctx := context.Background()
	admin := adminIdentity()

	cases := []struct {
		opType       BulkOpType
		candidateIDs []int64
		memoryIDs    []int64
		wantAffect   int
	}{
		{models.SnapshotOpBulkPromote, []int64{10, 20, 30}, nil, 3},
		{models.SnapshotOpBulkDelete, nil, []int64{11, 22}, 2},
		{models.SnapshotOpBulkSupersede, nil, []int64{13, 14, 15}, 3},
		{models.SnapshotOpIngestDoc, nil, nil, 0},
	}

	for _, c := range cases {
		t.Run(string(c.opType), func(t *testing.T) {
			op := BulkOp{
				Type:         c.opType,
				DryRun:       true,
				CandidateIDs: c.candidateIDs,
				MemoryIDs:    c.memoryIDs,
				Actor:        "test-actor",
				Parameters:   json.RawMessage(`{}`),
			}
			result, err := f.Execute(ctx, admin, op)
			require.NoError(t, err, "dry-run must not return an error for op %q", c.opType)
			require.NotNil(t, result)
			assert.True(t, result.DryRun, "DryRun must be true in result")
			assert.Empty(t, result.SnapshotID, "no snapshot created for dry-run")
			assert.Equal(t, c.wantAffect, result.WouldAffect, "WouldAffect must equal input length for op %q", c.opType)
		})
	}
}

// --- Integration: committed paths + audit log (require DATABASE_DSN) ---

func openTestDB(t *testing.T) (*gorm.DB, *gormdb.Store) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	// NewStore opens the connection AND runs all migrations — ensures schema is current
	// on a fresh test database. runMigrations is package-private to internal/db/gorm
	// so we use the exported NewStore path (consistent with other external test helpers).
	store, err := gormdb.NewStore(gormdb.Config{
		DSN:      dsn,
		LogLevel: logger.Warn,
	})
	require.NoError(t, err, "openTestDB: NewStore (applies migrations)")
	t.Cleanup(func() { store.Close() })
	return store.DB, store
}

// TestFacade_BulkDelete_Committed_AuditLogWritten verifies:
//   - bulk_delete committed: snapshot created, memory rows soft-deleted, audit log entry written.
//   - Spec §FR-F6, §FR-F5 enum, EC-F3 (no conflict for fresh rows).
func TestFacade_BulkDelete_Committed_AuditLogWritten(t *testing.T) {
	db, store := openTestDB(t)

	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	f := NewFacade(snapStore, nil, memStore, auditStore)

	ctx := context.Background()
	admin := adminIdentity()

	// Create a test memory row to delete.
	mem := &models.Memory{
		Content:     "bulk_delete_test_memory",
		Project:     "tg6-facade-test",
		SourceAgent: "claude-code",
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action = 'bulk_delete' AND reason LIKE '%tg6-facade-test%'`).Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE actor = 'master' AND op_type = 'bulk_delete'`).Error
	})

	op := BulkOp{
		Type:       models.SnapshotOpBulkDelete,
		MemoryIDs:  []int64{created.ID},
		DryRun:     false,
		Parameters: json.RawMessage(`{"test":"bulk_delete_committed"}`),
	}

	// Capture audit count BEFORE Execute to avoid false pass from historical records (§FR-F5).
	var auditCountBefore int64
	db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ?", "bulk_delete", "master").
		Count(&auditCountBefore)

	result, err := f.Execute(ctx, admin, op)
	require.NoError(t, err)
	assert.False(t, result.DryRun)
	assert.NotEmpty(t, result.SnapshotID, "committed op must return a snapshot_id")
	assert.Equal(t, 1, result.AffectedCount)
	assert.Empty(t, result.Errors)

	// Verify snapshot was stored.
	snap, err := snapStore.Get(ctx, result.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotOpBulkDelete, snap.OpType)
	assert.Equal(t, models.SnapshotStatusCommitted, snap.Status)
	assert.Contains(t, snap.AffectedMemoryIDs, created.ID)

	// Verify audit log entry written (§FR-F5): delta >= 1 ties the entry to this run.
	var auditCountAfter int64
	db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ?", "bulk_delete", "master").
		Count(&auditCountAfter)
	assert.GreaterOrEqual(t, auditCountAfter-auditCountBefore, int64(1),
		"audit log must have at least 1 new bulk_delete entry from this Execute call")
}

// TestFacade_BulkSupersede_Committed_AuditLogWritten verifies the supersede path.
func TestFacade_BulkSupersede_Committed_AuditLogWritten(t *testing.T) {
	db, store := openTestDB(t)

	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	f := NewFacade(snapStore, nil, memStore, auditStore)

	ctx := context.Background()
	admin := adminIdentity()

	mem := &models.Memory{
		Content:     "bulk_supersede_test_memory",
		Project:     "tg6-facade-test",
		SourceAgent: "claude-code",
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action = 'bulk_supersede' AND actor = 'master'`).Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE actor = 'master' AND op_type = 'bulk_supersede'`).Error
	})

	op := BulkOp{
		Type:      models.SnapshotOpBulkSupersede,
		MemoryIDs: []int64{created.ID},
		DryRun:    false,
	}

	// Capture audit count BEFORE Execute to avoid false pass from historical records.
	var auditCountBefore int64
	db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ?", "bulk_supersede", "master").
		Count(&auditCountBefore)

	result, err := f.Execute(ctx, admin, op)
	require.NoError(t, err)
	assert.False(t, result.DryRun)
	assert.NotEmpty(t, result.SnapshotID)
	assert.Equal(t, 1, result.AffectedCount)

	// Verify audit log: delta >= 1 ties the entry to this run.
	var auditCountAfter int64
	db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ?", "bulk_supersede", "master").
		Count(&auditCountAfter)
	assert.GreaterOrEqual(t, auditCountAfter-auditCountBefore, int64(1),
		"audit log must have at least 1 new bulk_supersede entry from this Execute call")
}

func TestCaptureMemoryBeforeState_ReturnsPersistedAuthoritativeBoundary(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	f := NewFacade(snapStore, nil, memStore, nil)
	ctx := context.Background()

	created, err := memStore.Create(ctx, &models.Memory{
		Content:     "authoritative capture boundary",
		Project:     "tg6-capture-boundary",
		SourceAgent: "claude-code",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
		_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE actor = 'capture-boundary-test'").Error
	})

	var startedAt time.Time
	require.NoError(t, db.Raw("SELECT clock_timestamp()").Scan(&startedAt).Error)
	snapshotID, beforeState, capturedAt, err := f.captureMemoryBeforeState(ctx, []int64{created.ID})
	var finishedAt time.Time
	require.NoError(t, db.Raw("SELECT clock_timestamp()").Scan(&finishedAt).Error)
	require.NoError(t, err)
	require.False(t, capturedAt.Before(startedAt))
	require.False(t, capturedAt.After(finishedAt))

	snap, err := models.NewBulkOpSnapshot(
		snapshotID,
		models.SnapshotOpBulkDelete,
		"capture-boundary-test",
		beforeState,
	)
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{created.ID}
	snap.CreatedAt = capturedAt

	persisted, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)
	require.True(t, persisted.CreatedAt.Equal(capturedAt),
		"the exact capture boundary returned with before_state must be persisted")
}

func createBulkPromoteCandidate(t *testing.T, candidateStore *gormdb.CandidateStore, suffix string) *models.CrystallizationCandidate {
	t.Helper()
	candidate, err := candidateStore.Create(context.Background(), &models.CrystallizationCandidate{
		SourceSessionID:         "bulk-promote-" + suffix,
		ProposedContent:         "bulk promote " + suffix,
		ProposedTier:            "semantic",
		ProposedEpistemicType:   "decision",
		ProposedPromotionTarget: "semantic",
		EvidenceHandles:         []string{"session:bulk-promote-" + suffix},
		PrivacyScope:            "project",
		Status:                  models.CandidateStatusPending,
		Fingerprint:             fmt.Sprintf("bulk-promote-%s-%d", suffix, time.Now().UnixNano()),
		AffectedProjects:        []string{"bulk-promote-" + suffix},
		Confidence:              0.9,
		RecurrenceCount:         2,
	})
	require.NoError(t, err)
	return candidate
}

func reserveEqualCandidateAndMemoryID(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var target int64
	require.NoError(t, db.Raw(`
		SELECT GREATEST(
			COALESCE((SELECT MAX(id) FROM memories), 0),
			COALESCE((SELECT MAX(id) FROM crystallization_candidates), 0),
			(SELECT last_value FROM memories_id_seq),
			(SELECT last_value FROM crystallization_candidates_id_seq)
		) + 1000
	`).Scan(&target).Error)
	require.NoError(t, db.Exec("SELECT setval('memories_id_seq', ?, true)", target-1).Error)
	require.NoError(t, db.Exec("SELECT setval('crystallization_candidates_id_seq', ?, true)", target-1).Error)
	return target
}

func TestFacade_BulkPromote_EqualCandidateAndMemoryIDsRemainDomainDisjointAndRollbackable(t *testing.T) {
	db, store := openTestDB(t)
	ctx := context.Background()
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	candidateStore := gormdb.NewCandidateStore(db, nil)
	facade := NewFacade(snapStore, candidateStore, memStore, nil)

	targetID := reserveEqualCandidateAndMemoryID(t, db)
	candidate := createBulkPromoteCandidate(t, candidateStore, "id-collision")
	require.Equal(t, targetID, candidate.ID)

	result, err := facade.Execute(ctx, adminIdentity(), BulkOp{
		Type:            models.SnapshotOpBulkPromote,
		CandidateIDs:    []int64{candidate.ID},
		SourceSessionID: "bulk-promote-id-collision",
	})
	require.NoError(t, err)
	require.Len(t, result.Promoted, 1)
	require.Equal(t, candidate.ID, result.Promoted[0],
		"fresh aligned independent sequences must exercise the equal-ID collision")

	persisted, err := snapStore.Get(ctx, result.SnapshotID)
	require.NoError(t, err)
	var entries map[string]models.SnapshotEntry
	require.NoError(t, json.Unmarshal(persisted.BeforeState, &entries))
	require.Len(t, entries, 2)
	require.Equal(t, models.EntryKindRestore, entries[fmt.Sprintf("candidate:%d", candidate.ID)].Kind)
	require.NotEmpty(t, entries[fmt.Sprintf("candidate:%d", candidate.ID)].Before)
	require.Equal(t, models.EntryKindDelete, entries[fmt.Sprintf("%d", result.Promoted[0])].Kind)

	rollbackResult, err := Rollback(ctx, adminIdentity(), result.SnapshotID, snapStore, memStore, nil, candidateStore)
	require.NoError(t, err)
	require.Equal(t, 1, rollbackResult.RestoredCount)

	restoredCandidate, err := candidateStore.Get(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPending, restoredCandidate.Status)
	require.Nil(t, restoredCandidate.PromotedMemoryID)
	var memoryCount int64
	require.NoError(t, db.Unscoped().Model(&gormdb.Memory{}).
		Where("id = ?", result.Promoted[0]).Count(&memoryCount).Error)
	require.Zero(t, memoryCount)
}

func TestFacade_BulkPromote_AmendFailureRollsBackAndRetryRemainsSafe(t *testing.T) {
	db, store := openTestDB(t)
	ctx := context.Background()
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	candidateStore := gormdb.NewCandidateStore(db, nil)
	facade := NewFacade(snapStore, candidateStore, memStore, nil)
	suffix := fmt.Sprintf("amend-failure-%d", time.Now().UnixNano())
	sourceSessionID := "bulk-promote-" + suffix

	_, err := memStore.Create(ctx, &models.Memory{
		Content:     "bulk promote sequence spacer",
		Project:     "bulk-promote-sequence-spacer",
		SourceAgent: "test",
	})
	require.NoError(t, err)
	candidate := createBulkPromoteCandidate(t, candidateStore, suffix)

	require.NoError(t, db.Exec(`
		CREATE OR REPLACE FUNCTION test_reject_bulk_promote_snapshot_update() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced AmendPromoteEntries failure';
		END
		$$
	`).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`
		CREATE TRIGGER test_reject_bulk_promote_snapshot_update
		BEFORE UPDATE ON bulk_op_snapshots
		FOR EACH ROW
		WHEN (OLD.op_type = 'bulk_promote' AND OLD.source_session_id = '%s')
		EXECUTE FUNCTION test_reject_bulk_promote_snapshot_update()
	`, sourceSessionID)).Error)
	triggerInstalled := true
	t.Cleanup(func() {
		if triggerInstalled {
			_ = db.Exec("DROP TRIGGER IF EXISTS test_reject_bulk_promote_snapshot_update ON bulk_op_snapshots").Error
		}
		_ = db.Exec("DROP FUNCTION IF EXISTS test_reject_bulk_promote_snapshot_update()").Error
	})

	op := BulkOp{
		Type:            models.SnapshotOpBulkPromote,
		CandidateIDs:    []int64{candidate.ID},
		SourceSessionID: sourceSessionID,
	}
	result, executeErr := facade.Execute(ctx, adminIdentity(), op)
	require.Error(t, executeErr)
	require.Nil(t, result)

	unchangedCandidate, err := candidateStore.Get(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPending, unchangedCandidate.Status)
	require.Nil(t, unchangedCandidate.PromotedMemoryID)
	var promotedMemoryCount int64
	require.NoError(t, db.Unscoped().Model(&gormdb.Memory{}).
		Where("content = ?", candidate.ProposedContent).Count(&promotedMemoryCount).Error)
	require.Zero(t, promotedMemoryCount)
	var snapshotCount int64
	require.NoError(t, db.Table("bulk_op_snapshots").
		Where("source_session_id = ?", sourceSessionID).Count(&snapshotCount).Error)
	require.Zero(t, snapshotCount)

	require.NoError(t, db.Exec("DROP TRIGGER test_reject_bulk_promote_snapshot_update ON bulk_op_snapshots").Error)
	triggerInstalled = false
	require.NoError(t, db.Exec("DROP FUNCTION test_reject_bulk_promote_snapshot_update()").Error)

	retryResult, err := facade.Execute(ctx, adminIdentity(), op)
	require.NoError(t, err)
	require.Len(t, retryResult.Promoted, 1)
	require.NoError(t, db.Unscoped().Model(&gormdb.Memory{}).
		Where("content = ?", candidate.ProposedContent).Count(&promotedMemoryCount).Error)
	require.Equal(t, int64(1), promotedMemoryCount)

	_, err = Rollback(ctx, adminIdentity(), retryResult.SnapshotID, snapStore, memStore, nil, candidateStore)
	require.NoError(t, err)
	restoredCandidate, err := candidateStore.Get(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPending, restoredCandidate.Status)
	require.Nil(t, restoredCandidate.PromotedMemoryID)
	require.NoError(t, db.Unscoped().Model(&gormdb.Memory{}).
		Where("content = ?", candidate.ProposedContent).Count(&promotedMemoryCount).Error)
	require.Zero(t, promotedMemoryCount)

	_, err = Rollback(ctx, adminIdentity(), retryResult.SnapshotID, snapStore, memStore, nil, candidateStore)
	require.ErrorIs(t, err, ErrSnapshotNotRollbackable)
}
