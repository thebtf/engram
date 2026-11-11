// Package bulkops_test provides TDD tests for the bulk-op facade.
// Engram vNext Milestone F TG6 / T041.
//
// Unit tests cover:
//   - Non-admin callers receive ErrAdminRequired (no DB required — auth gate fires first)
//   - Dry-run paths for all 4 op_types return a preview without any DB writes (no DB required)
//
// Integration tests require an explicitly designated disposable test database:
//   - Committed paths for bulk_delete and bulk_supersede
//   - Audit log is written after committed ops (§FR-F5)
package bulkops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// --- Integration: committed paths + audit log (require disposable test DB) ---

func bulkDisposableTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("ENGRAM_BULKOPS_TEST_DSN"))
	if dsn == "" {
		t.Skip("ENGRAM_BULKOPS_TEST_DSN must identify an exclusively disposable database")
	}
	if os.Getenv("ENGRAM_BULKOPS_DISPOSABLE_DB") != "yes" {
		t.Fatal("ENGRAM_BULKOPS_DISPOSABLE_DB=yes is required for migrations and cleanup")
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "PG") && value != "" {
			t.Fatal("bulkops fixture refuses inherited PostgreSQL settings")
		}
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.User == nil || parsed.User.Username() == "" || parsed.Port() == "" || parsed.Fragment != "" {
		t.Fatal("bulkops fixture requires an explicit numeric loopback PostgreSQL URL")
	}
	password, present := parsed.User.Password()
	host := parsed.Hostname()
	database := strings.ToLower(strings.TrimPrefix(parsed.Path, "/"))
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	if !present || password == "" || (host != "127.0.0.1" && host != "::1") || strings.Contains(database, "/") || !strings.HasSuffix(database, "_test") || strings.Contains(database, "prod") || strings.Contains(database, "staging") || queryErr != nil {
		t.Fatal("bulkops fixture requires an explicit numeric loopback test database")
	}
	for key, values := range query {
		if key != "sslmode" || len(values) != 1 {
			t.Fatal("bulkops fixture refuses PostgreSQL connection overrides")
		}
	}
	return dsn
}

func openTestDB(t *testing.T) (*gorm.DB, *gormdb.Store) {
	t.Helper()
	dsn := bulkDisposableTestDSN(t)
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

func newBulkTestProject(t *testing.T, db *gorm.DB) string {
	t.Helper()
	project := uuid.NewString()
	require.NoError(t, db.Create(&gormdb.Project{ID: project}).Error)
	t.Cleanup(func() { require.NoError(t, db.Delete(&gormdb.Project{}, "id = ?", project).Error) })
	return project
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
		Project:             newBulkTestProject(t, db),
		SourceAgent:         "claude-code",
		PrivacyScope:        "private",
		SourceWorkstationID: "facade-workstation",
		SourceSessions:      pq.StringArray{"facade-session-a", "facade-session-b"},
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
	})
	var snapshotID string
	t.Cleanup(func() {
		if snapshotID != "" {
			_ = db.Exec(`DELETE FROM audit_log WHERE action = ? AND actor = ? AND reason LIKE ?`, "bulk_delete", "master", "bulk_delete snapshot="+snapshotID+" %").Error
			_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?`, snapshotID).Error
		}
	})

	op := BulkOp{
		Type:       models.SnapshotOpBulkDelete,
		MemoryIDs:  []int64{created.ID},
		DryRun:     false,
		Parameters: json.RawMessage(`{"test":"bulk_delete_committed"}`),
	}

	result, err := f.Execute(ctx, admin, op)
	if result != nil {
		snapshotID = result.SnapshotID
	}
	require.NoError(t, err)
	assert.False(t, result.DryRun)
	assert.NotEmpty(t, result.SnapshotID, "committed op must return a snapshot_id")
	assert.Equal(t, 1, result.AffectedCount)
	assert.Empty(t, result.Errors)
	var deleted gormdb.Memory
	require.NoError(t, db.Unscoped().Where("id = ?", created.ID).First(&deleted).Error)
	assert.NotNil(t, deleted.DeletedAt)
	assert.Equal(t, created.Version+1, deleted.Version)

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

	// Verify the audit entry belongs to this snapshot (§FR-F5).
	var auditCount int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ? AND reason LIKE ?", "bulk_delete", "master", "bulk_delete snapshot="+result.SnapshotID+" %").
		Count(&auditCount).Error)
	assert.Equal(t, int64(1), auditCount, "audit log must record this bulk_delete operation")
}

func TestFacade_DryRunCountsOnlyEligibleUniqueRows(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	auditStore := gormdb.NewAuditStore(db)
	candidateStore := gormdb.NewCandidateStore(db, auditStore)
	facade := NewFacade(gormdb.NewSnapshotStore(db), candidateStore, memStore, auditStore)
	ctx := context.Background()
	project := newBulkTestProject(t, db)
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
		Content:        "bulk_supersede_test_memory",
		Project:        newBulkTestProject(t, db),
		SourceAgent:    "claude-code",
		ImportanceBase: 0.8,
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
	})
	var snapshotID string
	t.Cleanup(func() {
		if snapshotID != "" {
			_ = db.Exec(`DELETE FROM audit_log WHERE action = ? AND actor = ? AND reason LIKE ?`, "bulk_supersede", "master", "bulk_supersede snapshot="+snapshotID+" %").Error
			_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?`, snapshotID).Error
		}
	})

	op := BulkOp{
		Type:      models.SnapshotOpBulkSupersede,
		MemoryIDs: []int64{created.ID},
		DryRun:    false,
	}

	result, err := f.Execute(ctx, admin, op)
	if result != nil {
		snapshotID = result.SnapshotID
	}
	require.NoError(t, err)
	assert.False(t, result.DryRun)
	assert.NotEmpty(t, result.SnapshotID)
	assert.Equal(t, 1, result.AffectedCount)
	var superseded gormdb.Memory
	require.NoError(t, db.Unscoped().Where("id = ?", created.ID).First(&superseded).Error)
	assert.Equal(t, "superseded", superseded.Status)
	assert.Equal(t, created.Version+1, superseded.Version)
	assert.InDelta(t, created.ImportanceBase*0.1, superseded.ImportanceBase, 1e-6)
	snap, err := snapStore.Get(ctx, result.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotOpBulkSupersede, snap.OpType)
	assert.Equal(t, []int64{created.ID}, snap.AffectedMemoryIDs)

	// Verify the audit entry belongs to this snapshot.
	var auditCount int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ? AND reason LIKE ?", "bulk_supersede", "master", "bulk_supersede snapshot="+result.SnapshotID+" %").
		Count(&auditCount).Error)
	assert.Equal(t, int64(1), auditCount, "audit log must record this bulk_supersede operation")
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
				Content:        "bulk rollback original " + string(opType),
				Project:        newBulkTestProject(t, db),
				SourceAgent:    "test",
				ImportanceBase: 0.8,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error })

			result, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: opType, MemoryIDs: []int64{before.ID}})
			require.NoError(t, err)
			require.Equal(t, 1, result.AffectedCount)
			var forward gormdb.Memory
			require.NoError(t, db.Unscoped().Where("id = ?", before.ID).First(&forward).Error)
			assert.Equal(t, before.Version+1, forward.Version)
			if opType == models.SnapshotOpBulkDelete {
				assert.NotNil(t, forward.DeletedAt)
			} else {
				assert.Equal(t, "superseded", forward.Status)
				assert.InDelta(t, before.ImportanceBase*0.1, forward.ImportanceBase, 1e-6)
			}
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
			assert.InDelta(t, before.ImportanceBase, restored.ImportanceBase, 1e-6)
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

	before, err := memStore.Create(ctx, &models.Memory{Content: "rollback conflict source", Project: newBulkTestProject(t, db), SourceAgent: "test"})
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
	project := newBulkTestProject(t, db)
	before, err := memStore.Create(context.Background(), &models.Memory{Content: "capture failure source", Project: project, SourceAgent: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error })
	second, err := memStore.Create(context.Background(), &models.Memory{Content: "capture failure second", Project: project, SourceAgent: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", second.ID).Error })

	callbackName := "bulkops_capture_failure_" + uuid.NewString()
	var reads, updates atomic.Int32
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "memories" && reads.Add(1) == 2 {
			tx.AddError(errors.New("forced second snapshot read failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "memories" {
			updates.Add(1)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })
	facade := NewFacade(gormdb.NewSnapshotStore(db), nil, memStore, gormdb.NewAuditStore(db))
	_, err = facade.Execute(context.Background(), adminIdentity(), BulkOp{Type: models.SnapshotOpBulkDelete, MemoryIDs: []int64{before.ID, second.ID}})
	require.NoError(t, db.Callback().Query().Remove(callbackName))
	require.NoError(t, db.Callback().Update().Remove(callbackName))
	require.ErrorContains(t, err, "snapshot capture")
	require.ErrorContains(t, err, "forced second snapshot read failure")
	assert.Equal(t, int32(2), reads.Load())
	assert.Zero(t, updates.Load(), "capture must finish before any memory update is attempted")

	for _, original := range []*models.Memory{before, second} {
		after, err := memStore.Get(context.Background(), original.ID)
		require.NoError(t, err)
		assert.Nil(t, after.DeletedAt)
		assert.Equal(t, original.Version, after.Version)
	}
}

func TestFacade_BulkDelete_PartialSuccessSnapshotExcludesFailedRows(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	ctx := context.Background()
	before, err := memStore.Create(ctx, &models.Memory{Content: "partial delete source", Project: newBulkTestProject(t, db), SourceAgent: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error })

	facade := NewFacade(snapStore, nil, memStore, auditStore)
	result, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkDelete, MemoryIDs: []int64{before.ID, before.ID}})
	if result != nil && result.SnapshotID != "" {
		t.Cleanup(func() {
			_ = db.Exec("DELETE FROM audit_log WHERE reason LIKE ? OR reason LIKE ?", "bulk_delete snapshot="+result.SnapshotID+" %", "snapshot="+result.SnapshotID+" %").Error
			_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error
		})
	}
	require.NoError(t, err)
	require.Equal(t, 1, result.AffectedCount)
	require.Len(t, result.Errors, 1)

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

func TestFacade_BulkSupersede_PartialSuccessExcludesDistinctFailedRow(t *testing.T) {
	db, store := openTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	audits := gormdb.NewAuditStore(db)
	ctx := context.Background()
	project := newBulkTestProject(t, db)
	successful, err := memories.Create(ctx, &models.Memory{Content: "partial success", Project: project, SourceAgent: "test", Status: "active"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", successful.ID).Error })
	failed, err := memories.Create(ctx, &models.Memory{Content: "partial failure", Project: project, SourceAgent: "test", Status: "active"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", failed.ID).Error })
	_, err = memories.Supersede(ctx, failed.ID)
	require.NoError(t, err)
	failedBefore, err := memories.GetForSnapshot(ctx, failed.ID)
	require.NoError(t, err)

	facade := NewFacade(snapshots, nil, memories, audits)
	result, err := facade.Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkSupersede, MemoryIDs: []int64{failed.ID, successful.ID}})
	if result != nil && result.SnapshotID != "" {
		t.Cleanup(func() {
			_ = db.Exec("DELETE FROM audit_log WHERE reason LIKE ? OR reason LIKE ?", "bulk_supersede snapshot="+result.SnapshotID+" %", "snapshot="+result.SnapshotID+" %").Error
			_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error
		})
	}
	require.NoError(t, err)
	require.Equal(t, 1, result.AffectedCount)
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], fmt.Sprintf("memory %d:", failed.ID))
	snap, err := snapshots.Get(ctx, result.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, []int64{successful.ID}, snap.AffectedMemoryIDs)
	entries, err := decodeTypedBeforeState(snap.BeforeState)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries, fmt.Sprint(successful.ID))
	assert.NotContains(t, entries, fmt.Sprint(failed.ID))
	changed, err := memories.GetForSnapshot(ctx, successful.ID)
	require.NoError(t, err)
	assert.Equal(t, "superseded", changed.Status)
	assert.Greater(t, changed.Version, successful.Version)
	_, err = Rollback(ctx, adminIdentity(), result.SnapshotID, snapshots, memories, audits, nil)
	require.NoError(t, err)
	restored, err := memories.GetForSnapshot(ctx, successful.ID)
	require.NoError(t, err)
	assert.Equal(t, successful.Status, restored.Status)
	assert.Equal(t, successful.Version, restored.Version)
	failedAfter, err := memories.GetForSnapshot(ctx, failed.ID)
	require.NoError(t, err)
	assert.Equal(t, failedBefore.Status, failedAfter.Status)
	assert.Equal(t, failedBefore.Version, failedAfter.Version)
	assert.Equal(t, failedBefore.Content, failedAfter.Content)
}

func TestFacade_BulkPromote_AmendFailureRollsBack(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	candidateStore := gormdb.NewCandidateStore(db, nil)
	f := NewFacade(snapStore, candidateStore, memStore, gormdb.NewAuditStore(db))

	project := newBulkTestProject(t, db)
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

	callbackName := "bulkops_reject_amend_" + uuid.NewString()
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "bulk_op_snapshots" {
			tx.AddError(errors.New("forced snapshot amend failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	var snapshotsBefore int64
	require.NoError(t, db.Table("bulk_op_snapshots").Where("op_type = ? AND actor = ?", models.SnapshotOpBulkPromote, "master").Count(&snapshotsBefore).Error)

	result, err := f.Execute(context.Background(), adminIdentity(), BulkOp{
		Type:         models.SnapshotOpBulkPromote,
		CandidateIDs: []int64{candidate.ID},
	})
	require.Error(t, err)
	assert.Nil(t, result)
	require.ErrorContains(t, err, "forced snapshot amend failure")

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

	project := newBulkTestProject(t, db)
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
		_ = db.Exec("DELETE FROM audit_log WHERE action = ? AND actor = ? AND reason LIKE ?", "promote_candidate", "master", fmt.Sprintf("candidate %d promoted%%", candidate.ID)).Error
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Exec("DELETE FROM memories WHERE project = ?", project).Error
	})

	var auditsBefore int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ? AND actor = ? AND reason LIKE ?", "promote_candidate", "master", fmt.Sprintf("candidate %d promoted%%", candidate.ID)).Count(&auditsBefore).Error)
	result, err := f.Execute(context.Background(), adminIdentity(), BulkOp{Type: models.SnapshotOpBulkPromote, CandidateIDs: []int64{candidate.ID}})
	if result != nil && result.SnapshotID != "" {
		t.Cleanup(func() {
			_ = db.Exec("DELETE FROM audit_log WHERE action = ? AND actor = ? AND reason LIKE ?", "bulk_promote", "master", "bulk_promote snapshot="+result.SnapshotID+" %").Error
			_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error
		})
	}
	require.NoError(t, err)
	require.Len(t, result.Promoted, 1)

	var auditsAfter int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ? AND actor = ? AND reason LIKE ?", "promote_candidate", "master", fmt.Sprintf("candidate %d promoted%%", candidate.ID)).Count(&auditsAfter).Error)
	assert.Equal(t, auditsBefore+1, auditsAfter, "bulk promotion audit must commit with the bulk actor")

	snap, err := snapStore.Get(context.Background(), result.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, result.Promoted, snap.AffectedMemoryIDs)
	entries, err := decodeTypedBeforeState(snap.BeforeState)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	candidateEntry, ok := entries[fmt.Sprintf("candidate:%d", candidate.ID)]
	require.True(t, ok)
	assert.Equal(t, models.EntryKindRestore, candidateEntry.Kind)
	assert.NotEmpty(t, candidateEntry.Before)
	assert.NotEmpty(t, candidateEntry.PostStateToken)
	memoryEntry, ok := entries[fmt.Sprintf("memory:%d", result.Promoted[0])]
	require.True(t, ok)
	assert.Equal(t, models.EntryKindDelete, memoryEntry.Kind)
	assert.NotEmpty(t, memoryEntry.PostStateToken)
}

func TestFacade_BulkPromote_RollbackExcludesFailedCandidates(t *testing.T) {
	db, store := openTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	candidateStore := gormdb.NewCandidateStore(db, auditStore)
	f := NewFacade(snapStore, candidateStore, memStore, auditStore)

	project := newBulkTestProject(t, db)
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
	before, err := memories.Create(context.Background(), &models.Memory{Content: "token injection source", Project: newBulkTestProject(t, db), SourceAgent: "test"})
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
	project := newBulkTestProject(t, db)
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
	project := newBulkTestProject(t, db)
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
	project := newBulkTestProject(t, db)
	first, err := memories.Create(ctx, &models.Memory{Content: "reverse lock first", Project: project, SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM memories WHERE id = ?", first.ID).Error })
	second, err := memories.Create(ctx, &models.Memory{Content: "reverse lock second", Project: project, SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM memories WHERE id = ?", second.ID).Error })

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
	for _, result := range results {
		if result == nil || result.SnapshotID == "" {
			continue
		}
		snapshotID := result.SnapshotID
		t.Cleanup(func() {
			_ = db.Exec("DELETE FROM audit_log WHERE action = ? AND actor = ? AND reason LIKE ?", "bulk_supersede", "master", "bulk_supersede snapshot="+snapshotID+" %").Error
			_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", snapshotID).Error
		})
	}
	require.GreaterOrEqual(t, arrivals.Load(), int32(2))
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 2, results[0].AffectedCount+results[1].AffectedCount)
	for _, id := range []int64{first.ID, second.ID} {
		row, err := memories.GetForSnapshot(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "superseded", row.Status)
	}
}

func TestFacade_BulkSupersede_CapturesLocksInAscendingOrder(t *testing.T) {
	db, store := openTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	audits := gormdb.NewAuditStore(db)
	ctx := context.Background()
	project := newBulkTestProject(t, db)
	first, err := memories.Create(ctx, &models.Memory{Content: "lock order first", Project: project, SourceAgent: "test", Status: "active"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM memories WHERE id = ?", first.ID).Error })
	second, err := memories.Create(ctx, &models.Memory{Content: "lock order second", Project: project, SourceAgent: "test", Status: "active"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM memories WHERE id = ?", second.ID).Error })
	require.Less(t, first.ID, second.ID)

	callbackName := "bulk_capture_order_" + uuid.NewString()
	var captured []int64
	var lockStrengths []string
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "memories" && tx.Error == nil && len(captured) < 2 {
			if row, ok := tx.Statement.Dest.(*gormdb.Memory); ok {
				captured = append(captured, row.ID)
				locking, _ := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
				lockStrengths = append(lockStrengths, locking.Strength)
			}
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	result, err := NewFacade(snapshots, nil, memories, audits).Execute(ctx, adminIdentity(), BulkOp{Type: models.SnapshotOpBulkSupersede, MemoryIDs: []int64{second.ID, first.ID}})
	if result != nil && result.SnapshotID != "" {
		t.Cleanup(func() {
			_ = db.Exec("DELETE FROM audit_log WHERE action = ? AND actor = ? AND reason LIKE ?", "bulk_supersede", "master", "bulk_supersede snapshot="+result.SnapshotID+" %").Error
			_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", result.SnapshotID).Error
		})
	}
	require.NoError(t, err)
	require.Equal(t, 2, result.AffectedCount)
	require.Empty(t, result.Errors)
	assert.Equal(t, []int64{first.ID, second.ID}, captured)
	assert.Equal(t, []string{"UPDATE", "UPDATE"}, lockStrengths)
}
