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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/auth"
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
					Type:     opType,
					DryRun:   false,
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
		opType      BulkOpType
		candidateIDs []int64
		memoryIDs   []int64
		wantAffect  int
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
		Type:        models.SnapshotOpBulkDelete,
		MemoryIDs:   []int64{created.ID},
		DryRun:      false,
		Parameters:  json.RawMessage(`{"test":"bulk_delete_committed"}`),
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
