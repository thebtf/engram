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
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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

// --- Unit: dry-run path without stores ---

func TestFacade_DryRun_IngestDoesNotRequireStores(t *testing.T) {
	result, err := newNilFacade().Execute(context.Background(), adminIdentity(), BulkOp{
		Type:   models.SnapshotOpIngestDoc,
		DryRun: true,
	})
	require.NoError(t, err)
	assert.True(t, result.DryRun)
	assert.Empty(t, result.SnapshotID)
	assert.Zero(t, result.WouldAffect)
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
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	db, store := openTestDB(t)

	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	f := NewFacade(snapStore, nil, memStore, auditStore)

	ctx := context.Background()
	admin := adminIdentity()

	// Create a test memory row to delete.
	mem := &models.Memory{
		Content:             "bulk_delete_test_memory",
		Project:             "tg6-facade-test",
		SourceAgent:         "claude-code",
		PrivacyScope:        "private",
		SourceWorkstationID: "facade-workstation",
		SourceSessions:      pq.StringArray{"facade-session-a", "facade-session-b"},
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
	entries, err := decodeTypedBeforeState(snap.BeforeState)
	require.NoError(t, err)
	entry, ok := entries[fmt.Sprintf("%d", created.ID)]
	require.True(t, ok, "snapshot must contain the affected memory")
	assert.NotEmpty(t, entry.PostStateToken)
	var captured models.Memory
	require.NoError(t, json.Unmarshal(entry.Before, &captured))
	assert.Equal(t, "private", captured.PrivacyScope)
	assert.Equal(t, "facade-workstation", captured.SourceWorkstationID)
	assert.Equal(t, []string{"facade-session-a", "facade-session-b"}, []string(captured.SourceSessions))

	// Verify audit log entry written (§FR-F5): delta >= 1 ties the entry to this run.
	var auditCountAfter int64
	db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ?", "bulk_delete", "master").
		Count(&auditCountAfter)
	assert.GreaterOrEqual(t, auditCountAfter-auditCountBefore, int64(1),
		"audit log must have at least 1 new bulk_delete entry from this Execute call")
}

func TestFacade_DryRunCountsOnlyEligibleUniqueRows(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	auditStore := gormdb.NewAuditStore(db)
	candidateStore := gormdb.NewCandidateStore(db, auditStore)
	facade := NewFacade(gormdb.NewSnapshotStore(db), candidateStore, memStore, auditStore)
	ctx := context.Background()
	project := fmt.Sprintf("bulk-dry-run-%d", time.Now().UnixNano())
	active, err := memStore.Create(ctx, &models.Memory{Content: "active", Project: project, SourceAgent: "test"})
	require.NoError(t, err)
	deleted, err := memStore.Create(ctx, &models.Memory{Content: "deleted", Project: project, SourceAgent: "test"})
	require.NoError(t, err)
	require.NoError(t, memStore.Delete(ctx, deleted.ID))
	superseded, err := memStore.Create(ctx, &models.Memory{Content: "superseded", Project: project, SourceAgent: "test"})
	require.NoError(t, err)
	_, err = memStore.Supersede(ctx, superseded.ID)
	require.NoError(t, err)
	newCandidate := func(status models.CandidateStatus, suffix string) *models.CrystallizationCandidate {
		candidate, err := candidateStore.Create(ctx, &models.CrystallizationCandidate{
			SourceSessionID: "bulk-dry-run", ProposedContent: suffix, ProposedTier: "semantic", ProposedPromotionTarget: "semantic",
			PrivacyScope: "project", Status: status, Fingerprint: project + "-" + suffix, AffectedProjects: []string{project}, Confidence: 0.9, RecurrenceCount: 1,
		})
		require.NoError(t, err)
		return candidate
	}
	pending := newCandidate(models.CandidateStatusPending, "pending")
	rejected := newCandidate(models.CandidateStatusRejected, "rejected")
	t.Cleanup(func() {
		_ = db.Unscoped().Delete(&gormdb.Memory{}, "id IN ?", []int64{active.ID, superseded.ID, deleted.ID}).Error
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id IN ?", []int64{pending.ID, rejected.ID}).Error
	})

	var snapshotsBefore, auditsBefore int64
	require.NoError(t, db.Table("bulk_op_snapshots").Count(&snapshotsBefore).Error)
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Count(&auditsBefore).Error)
	deletePreview, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkDelete, DryRun: true, MemoryIDs: []int64{active.ID, active.ID, deleted.ID, 999999999}})
	require.NoError(t, err)
	assert.Equal(t, 1, deletePreview.WouldAffect)
	require.Len(t, deletePreview.Errors, 2)
	supersedePreview, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkSupersede, DryRun: true, MemoryIDs: []int64{active.ID, superseded.ID, deleted.ID, 999999999}})
	require.NoError(t, err)
	assert.Equal(t, 1, supersedePreview.WouldAffect)
	require.Len(t, supersedePreview.Errors, 3)
	promotePreview, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkPromote, DryRun: true, CandidateIDs: []int64{pending.ID, pending.ID, rejected.ID, 999999999}})
	require.NoError(t, err)
	assert.Equal(t, 1, promotePreview.WouldAffect)
	require.Len(t, promotePreview.Errors, 2)

	var snapshotsAfter, auditsAfter int64
	require.NoError(t, db.Table("bulk_op_snapshots").Count(&snapshotsAfter).Error)
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Count(&auditsAfter).Error)
	assert.Equal(t, snapshotsBefore, snapshotsAfter)
	assert.Equal(t, auditsBefore, auditsAfter)
	stillActive, err := memStore.Get(ctx, active.ID)
	require.NoError(t, err)
	assert.Equal(t, "active", stillActive.Status)
	stillPending, err := candidateStore.Get(ctx, pending.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusPending, stillPending.Status)
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

func TestFacade_BulkMemoryRollback_RestoresOriginalVersion(t *testing.T) {
	for _, opType := range []models.SnapshotOpType{models.SnapshotOpBulkDelete, models.SnapshotOpBulkSupersede} {
		t.Run(string(opType), func(t *testing.T) {
			db, store := openTestDB(t)
			memStore := gormdb.NewMemoryStore(store)
			snapStore := gormdb.NewSnapshotStore(db)
			auditStore := gormdb.NewAuditStore(db)
			facade := NewFacade(snapStore, nil, memStore, auditStore)
			ctx := context.Background()

			before, err := memStore.Create(ctx, &models.Memory{
				Content:     "bulk rollback original " + string(opType),
				Project:     "bulk-rollback-version",
				SourceAgent: "test",
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error })

			result, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: opType, MemoryIDs: []int64{before.ID}})
			require.NoError(t, err)
			require.Equal(t, 1, result.AffectedCount)
			var forward gormdb.Memory
			require.NoError(t, db.Unscoped().Where("id = ?", before.ID).First(&forward).Error)
			assert.Equal(t, before.Version+1, forward.Version)
			t.Cleanup(func() { _ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error })

			snap, err := snapStore.Get(ctx, result.SnapshotID)
			require.NoError(t, err)
			entries, err := decodeTypedBeforeState(snap.BeforeState)
			require.NoError(t, err)
			entry := entries[fmt.Sprintf("%d", before.ID)]
			assert.NotEmpty(t, entry.PostStateToken)

			rollback, err := Rollback(ctx, adminIdentity(), result.SnapshotID, snapStore, memStore, auditStore, nil)
			require.NoError(t, err)
			assert.Equal(t, 1, rollback.RestoredCount)
			restored, err := memStore.GetForSnapshot(ctx, before.ID)
			require.NoError(t, err)
			assert.Equal(t, before.Version, restored.Version)
			assert.Equal(t, before.Status, restored.Status)
			assert.Equal(t, before.DeletedAt, restored.DeletedAt)
			assert.Equal(t, before.Content, restored.Content)
		})
	}
}

func TestFacade_BulkSupersedeRollback_ConflictsAfterVersionAdvance(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	facade := NewFacade(snapStore, nil, memStore, auditStore)
	ctx := context.Background()

	before, err := memStore.Create(ctx, &models.Memory{Content: "rollback conflict source", Project: "bulk-rollback-conflict", SourceAgent: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error })
	result, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkSupersede, MemoryIDs: []int64{before.ID}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error })

	forward, err := memStore.GetForSnapshot(ctx, before.ID)
	require.NoError(t, err)
	forward.Content = "post-operation normal edit"
	_, err = memStore.Update(ctx, forward)
	require.NoError(t, err)

	rollback, err := Rollback(ctx, adminIdentity(), result.SnapshotID, snapStore, memStore, auditStore, nil)

	require.ErrorIs(t, err, ErrRollbackConflict)
	require.NotNil(t, rollback)
	assert.Contains(t, rollback.ConflictIDs, before.ID)
}

func TestFacade_BulkDelete_CaptureFailureDoesNotMutate(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	before, err := memStore.Create(context.Background(), &models.Memory{Content: "capture failure source", Project: "bulk-capture-failure", SourceAgent: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error })

	callbackName := fmt.Sprintf("bulkops_capture_failure_%d", time.Now().UnixNano())
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		tx.AddError(errors.New("forced snapshot read failure"))
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	facade := NewFacade(gormdb.NewSnapshotStore(db), nil, memStore, gormdb.NewAuditStore(db))
	_, err = facade.Execute(context.Background(), adminIdentity(), BulkOp{Type: models.SnapshotOpBulkDelete, MemoryIDs: []int64{before.ID}})
	require.NoError(t, db.Callback().Query().Remove(callbackName))
	require.Error(t, err)

	after, err := memStore.Get(context.Background(), before.ID)
	require.NoError(t, err)
	assert.Nil(t, after.DeletedAt)
	assert.Equal(t, before.Version, after.Version)
}

func TestFacade_BulkDelete_PartialSuccessSnapshotExcludesFailedRows(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	ctx := context.Background()
	before, err := memStore.Create(ctx, &models.Memory{Content: "partial delete source", Project: "bulk-partial-delete", SourceAgent: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error })

	facade := NewFacade(snapStore, nil, memStore, auditStore)
	result, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkDelete, MemoryIDs: []int64{before.ID, before.ID}})
	require.NoError(t, err)
	require.Equal(t, 1, result.AffectedCount)
	require.Len(t, result.Errors, 1)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error })

	snap, err := snapStore.Get(ctx, result.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, []int64{before.ID}, snap.AffectedMemoryIDs)
	entries, err := decodeTypedBeforeState(snap.BeforeState)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	_, err = Rollback(ctx, adminIdentity(), result.SnapshotID, snapStore, memStore, auditStore, nil)
	require.NoError(t, err)
	restored, err := memStore.GetForSnapshot(ctx, before.ID)
	require.NoError(t, err)
	assert.Equal(t, before.Version, restored.Version)
}

func TestFacade_BulkPromote_AmendFailureRollsBack(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	candidateStore := gormdb.NewCandidateStore(db, nil)
	f := NewFacade(snapStore, candidateStore, memStore, gormdb.NewAuditStore(db))

	project := fmt.Sprintf("tg6-promote-amend-failure-%d", time.Now().UnixNano())
	candidate, err := candidateStore.Create(context.Background(), &models.CrystallizationCandidate{
		SourceSessionID:         "tg6-promote-amend-failure",
		ProposedContent:         "snapshot amendment failure must not promote this candidate",
		ProposedTier:            "semantic",
		ProposedPromotionTarget: "semantic",
		EvidenceHandles:         []string{"session:tg6-promote-amend-failure"},
		PrivacyScope:            "project",
		Status:                  models.CandidateStatusPending,
		Fingerprint:             project,
		AffectedProjects:        []string{project},
		Confidence:              0.9,
		RecurrenceCount:         1,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Exec("DELETE FROM memories WHERE project = ?", project).Error
	})

	function := fmt.Sprintf("bulkops_reject_amend_%d", time.Now().UnixNano())
	trigger := function + "_trigger"
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced snapshot amend failure'; END; $$`, function)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON bulk_op_snapshots FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, function)).Error)
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON bulk_op_snapshots", trigger)).Error
		_ = db.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", function)).Error
	})

	var snapshotsBefore int64
	require.NoError(t, db.Table("bulk_op_snapshots").Where("op_type = ? AND actor = ?", models.SnapshotOpBulkPromote, "master").Count(&snapshotsBefore).Error)

	result, err := f.Execute(context.Background(), adminIdentity(), BulkOp{
		Type:         models.SnapshotOpBulkPromote,
		CandidateIDs: []int64{candidate.ID},
	})
	require.Error(t, err)
	assert.Nil(t, result)

	after, err := candidateStore.Get(context.Background(), candidate.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusPending, after.Status)
	assert.Nil(t, after.PromotedMemoryID)

	var memories, snapshotsAfter int64
	require.NoError(t, db.Model(&gormdb.Memory{}).Where("project = ?", project).Count(&memories).Error)
	require.NoError(t, db.Table("bulk_op_snapshots").Where("op_type = ? AND actor = ?", models.SnapshotOpBulkPromote, "master").Count(&snapshotsAfter).Error)
	assert.Zero(t, memories, "a failed audit snapshot amendment must roll back promoted memories")
	assert.Equal(t, snapshotsBefore, snapshotsAfter, "a failed audit snapshot amendment must roll back its snapshot")
}

func TestFacade_BulkPromote_PreservesCandidateAuditAndSeparatesIDs(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	candidateStore := gormdb.NewCandidateStore(db, auditStore)
	f := NewFacade(snapStore, candidateStore, memStore, auditStore)

	project := fmt.Sprintf("tg6-promote-audit-%d", time.Now().UnixNano())
	candidate, err := candidateStore.Create(context.Background(), &models.CrystallizationCandidate{
		SourceSessionID:         "tg6-promote-audit",
		ProposedContent:         "transaction-owned promotion keeps candidate audit",
		ProposedTier:            "semantic",
		ProposedPromotionTarget: "semantic",
		PrivacyScope:            "project",
		Status:                  models.CandidateStatusPending,
		Fingerprint:             project,
		AffectedProjects:        []string{project},
		Confidence:              0.9,
		RecurrenceCount:         1,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Exec("DELETE FROM memories WHERE project = ?", project).Error
	})

	var auditsBefore int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ? AND actor = ? AND reason LIKE ?", "promote_candidate", "master", fmt.Sprintf("candidate %d promoted%%", candidate.ID)).Count(&auditsBefore).Error)
	result, err := f.Execute(context.Background(), adminIdentity(), BulkOp{Type: models.SnapshotOpBulkPromote, CandidateIDs: []int64{candidate.ID}})
	require.NoError(t, err)
	require.Len(t, result.Promoted, 1)

	var auditsAfter int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ? AND actor = ? AND reason LIKE ?", "promote_candidate", "master", fmt.Sprintf("candidate %d promoted%%", candidate.ID)).Count(&auditsAfter).Error)
	assert.Equal(t, auditsBefore+1, auditsAfter, "bulk promotion audit must commit with the bulk actor")

	snap, err := snapStore.Get(context.Background(), result.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, result.Promoted, snap.AffectedMemoryIDs,
		"affected IDs must be exactly the promoted memory IDs even when candidate and memory sequences collide")
	entries, err := decodeTypedBeforeState(snap.BeforeState)
	require.NoError(t, err)
	assert.Contains(t, entries, fmt.Sprintf("candidate:%d", candidate.ID))
	assert.Contains(t, entries, fmt.Sprintf("memory:%d", result.Promoted[0]))
}

func TestFacade_BulkPromote_RollbackExcludesFailedCandidates(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	candidateStore := gormdb.NewCandidateStore(db, auditStore)
	f := NewFacade(snapStore, candidateStore, memStore, auditStore)

	project := fmt.Sprintf("tg6-promote-partial-rollback-%d", time.Now().UnixNano())
	makeCandidate := func(content string) *models.CrystallizationCandidate {
		candidate, err := candidateStore.Create(context.Background(), &models.CrystallizationCandidate{
			SourceSessionID:         "tg6-promote-partial-rollback",
			ProposedContent:         content,
			ProposedTier:            "semantic",
			ProposedPromotionTarget: "semantic",
			PrivacyScope:            "project",
			Status:                  models.CandidateStatusPending,
			Fingerprint:             fmt.Sprintf("%s-%s", project, content),
			AffectedProjects:        []string{project},
			Confidence:              0.9,
			RecurrenceCount:         1,
		})
		require.NoError(t, err)
		return candidate
	}
	successful := makeCandidate("successful candidate")
	failed := makeCandidate("failed candidate")
	snapshotID := ""
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id IN (?, ?)", successful.ID, failed.ID).Error
		_ = db.Exec("DELETE FROM memories WHERE project = ?", project).Error
		if snapshotID != "" {
			_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", snapshotID).Error
		}
	})
	// A promoted candidate cannot transition to promoted again, producing the
	// partial-success path without failing the transaction.
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET status = ? WHERE id = ?", models.CandidateStatusPromoted, failed.ID).Error)

	result, err := f.Execute(context.Background(), adminIdentity(), BulkOp{
		Type:         models.SnapshotOpBulkPromote,
		CandidateIDs: []int64{failed.ID, successful.ID},
	})
	require.NoError(t, err)
	snapshotID = result.SnapshotID
	require.Equal(t, 1, result.AffectedCount)
	require.Len(t, result.Promoted, 1)
	require.Len(t, result.Errors, 1)

	snap, err := snapStore.Get(context.Background(), result.SnapshotID)
	require.NoError(t, err)
	entries, err := decodeTypedBeforeState(snap.BeforeState)
	require.NoError(t, err)
	assert.Contains(t, entries, fmt.Sprintf("candidate:%d", successful.ID))
	assert.NotContains(t, entries, fmt.Sprintf("candidate:%d", failed.ID))
	var parameters struct {
		CandidateIDs []int64 `json:"candidate_ids"`
	}
	require.NoError(t, json.Unmarshal(snap.Parameters, &parameters))
	require.Equal(t, []int64{successful.ID}, parameters.CandidateIDs)
	require.Equal(t, result.Promoted, snap.AffectedMemoryIDs)

	// Simulate a later change to the candidate that this operation did not mutate.
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET status = ?, proposed_content = ? WHERE id = ?", models.CandidateStatusRejected, "failed candidate changed after bulk", failed.ID).Error)
	require.NoError(t, db.Exec("DELETE FROM memories WHERE id = ?", result.Promoted[0]).Error)

	rollback, err := Rollback(context.Background(), adminIdentity(), result.SnapshotID, snapStore, memStore, auditStore, candidateStore)
	require.NoError(t, err)
	assert.Equal(t, 1, rollback.RestoredCount)

	failedAfter, err := candidateStore.Get(context.Background(), failed.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusRejected, failedAfter.Status)
	assert.Equal(t, "failed candidate changed after bulk", failedAfter.ProposedContent)
	var promotedMemoryCount int64
	require.NoError(t, db.Unscoped().Model(&gormdb.Memory{}).Where("id = ?", result.Promoted[0]).Count(&promotedMemoryCount).Error)
	assert.Zero(t, promotedMemoryCount)
}

func TestFacade_BulkRollbackRejectsInjectionCountMutation(t *testing.T) {
	db, store := openTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	audits := gormdb.NewAuditStore(db)
	before, err := memories.Create(context.Background(), &models.Memory{Content: "token injection source", Project: "bulk-token-injection", SourceAgent: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error })

	result, err := NewFacade(snapshots, nil, memories, audits).Execute(context.Background(), adminIdentity(), BulkOp{Type: models.SnapshotOpBulkSupersede, MemoryIDs: []int64{before.ID}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error })
	require.NoError(t, memories.IncrementInjectionCount(context.Background(), before.ID))

	rollback, err := Rollback(context.Background(), adminIdentity(), result.SnapshotID, snapshots, memories, audits, nil)
	require.ErrorIs(t, err, ErrRollbackConflict)
	assert.Contains(t, rollback.ConflictIDs, before.ID)
}

func TestFacade_BulkPromoteRejectsConcurrentCandidateEdit(t *testing.T) {
	db, store := openTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	audits := gormdb.NewAuditStore(db)
	snapshots := gormdb.NewSnapshotStore(db)
	candidates := gormdb.NewCandidateStore(db, audits)
	project := fmt.Sprintf("bulk-token-candidate-%d", time.Now().UnixNano())
	candidate, err := candidates.Create(context.Background(), &models.CrystallizationCandidate{SourceSessionID: "bulk-token", ProposedContent: "candidate token source", ProposedTier: "semantic", ProposedPromotionTarget: "semantic", PrivacyScope: "project", Status: models.CandidateStatusPending, Fingerprint: project, AffectedProjects: []string{project}, Confidence: 0.9, RecurrenceCount: 1})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Exec("DELETE FROM memories WHERE project = ?", project).Error
	})

	result, err := NewFacade(snapshots, candidates, memories, audits).Execute(context.Background(), adminIdentity(), BulkOp{Type: models.SnapshotOpBulkPromote, CandidateIDs: []int64{candidate.ID}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error })
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET proposed_content = ? WHERE id = ?", "concurrently reviewed", candidate.ID).Error)

	rollback, err := Rollback(context.Background(), adminIdentity(), result.SnapshotID, snapshots, memories, audits, candidates)
	require.ErrorIs(t, err, ErrRollbackConflict)
	assert.Contains(t, rollback.ConflictIDs, candidate.ID)
}

func TestFacade_BulkPromoteAllFailedLeavesNoSnapshotOrAudit(t *testing.T) {
	db, store := openTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	audits := gormdb.NewAuditStore(db)
	snapshots := gormdb.NewSnapshotStore(db)
	candidates := gormdb.NewCandidateStore(db, audits)
	project := fmt.Sprintf("bulk-all-failed-%d", time.Now().UnixNano())
	candidate, err := candidates.Create(context.Background(), &models.CrystallizationCandidate{SourceSessionID: "bulk-all-failed", ProposedContent: "cannot promote", ProposedTier: "semantic", ProposedPromotionTarget: "semantic", PrivacyScope: "project", Status: models.CandidateStatusPromoted, Fingerprint: project, AffectedProjects: []string{project}, Confidence: 0.9, RecurrenceCount: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error })

	var snapshotsBefore, auditsBefore int64
	require.NoError(t, db.Table("bulk_op_snapshots").Where("op_type = ?", models.SnapshotOpBulkPromote).Count(&snapshotsBefore).Error)
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "bulk_promote").Count(&auditsBefore).Error)
	result, err := NewFacade(snapshots, candidates, memories, audits).Execute(context.Background(), adminIdentity(), BulkOp{Type: models.SnapshotOpBulkPromote, CandidateIDs: []int64{candidate.ID}})
	require.NoError(t, err)
	assert.Empty(t, result.SnapshotID)
	assert.Zero(t, result.AffectedCount)
	require.Len(t, result.Errors, 1)
	var snapshotsAfter, auditsAfter int64
	require.NoError(t, db.Table("bulk_op_snapshots").Where("op_type = ?", models.SnapshotOpBulkPromote).Count(&snapshotsAfter).Error)
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "bulk_promote").Count(&auditsAfter).Error)
	assert.Equal(t, snapshotsBefore, snapshotsAfter)
	assert.Equal(t, auditsBefore, auditsAfter)
}

func TestFacade_BulkSupersedeReverseOrderDoesNotDeadlock(t *testing.T) {
	db, store := openTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	audits := gormdb.NewAuditStore(db)
	facade := NewFacade(snapshots, nil, memories, audits)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	project := fmt.Sprintf("bulk-reverse-locks-%d", time.Now().UnixNano())
	first, err := memories.Create(ctx, &models.Memory{Content: "reverse lock first", Project: project, SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}})
	require.NoError(t, err)
	second, err := memories.Create(ctx, &models.Memory{Content: "reverse lock second", Project: project, SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}})
	require.NoError(t, err)

	callbackName := fmt.Sprintf("bulk_reverse_lock_barrier_%d", time.Now().UnixNano())
	release := make(chan struct{})
	var arrivals atomic.Int32
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "memories" {
			return
		}
		arrival := arrivals.Add(1)
		if arrival > 2 {
			return
		}
		if arrival == 2 {
			close(release)
		}
		select {
		case <-release:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}))
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
		_ = db.Exec("DELETE FROM memories WHERE id IN ?", []int64{first.ID, second.ID}).Error
	})

	orders := [][]int64{{first.ID, second.ID}, {second.ID, first.ID}}
	results := make([]*ExecuteResult, len(orders))
	errs := make([]error, len(orders))
	var group sync.WaitGroup
	group.Add(len(orders))
	start := make(chan struct{})
	for index, ids := range orders {
		go func(index int, ids []int64) {
			defer group.Done()
			<-start
			results[index], errs[index] = facade.Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkSupersede, MemoryIDs: ids, SourceSessionID: project})
		}(index, ids)
	}
	close(start)
	group.Wait()
	require.GreaterOrEqual(t, arrivals.Load(), int32(2))
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 2, results[0].AffectedCount+results[1].AffectedCount)
	for _, result := range results {
		if result.SnapshotID == "" {
			continue
		}
		t.Cleanup(func() {
			_ = db.Exec("DELETE FROM audit_log WHERE reason LIKE ?", "%snapshot="+result.SnapshotID+"%").Error
			_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error
		})
	}
}
