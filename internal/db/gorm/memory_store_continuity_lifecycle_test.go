package gorm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/pkg/models"
)

type continuityLifecycleFixture struct {
	db        *gormlib.DB
	memories  *MemoryStore
	slots     *ContinuitySlotStore
	target    *models.Memory
	successor *models.Memory
	project   string
}

func newContinuityLifecycleFixture(t *testing.T) continuityLifecycleFixture {
	t.Helper()

	db, closeDB := openTestDB(t)
	t.Cleanup(closeDB)

	ctx := context.Background()
	project := fmt.Sprintf("test-continuity-lifecycle-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		require.NoError(t, db.Where("action = ? AND reason LIKE ?", continuitySlotLifecycleAuditAction, "%"+project+"%").Delete(&AuditLogEntry{}).Error)
		cleanupContinuitySlotTestProject(t, db, project)
	})

	memories := NewMemoryStore(&Store{DB: db})
	target, err := memories.Create(ctx, &models.Memory{
		Project:     project,
		Content:     "continuity target",
		SourceAgent: "continuity-lifecycle-test",
	})
	require.NoError(t, err)
	successor, err := memories.Create(ctx, &models.Memory{
		Project:     project,
		Content:     "continuity successor",
		SourceAgent: "continuity-lifecycle-test",
	})
	require.NoError(t, err)

	slots := NewContinuitySlotStore(db)
	require.NoError(t, slots.Upsert(ctx, ContinuitySlot{
		Project:                     project,
		MemoryID:                    target.ID,
		ExpiresAt:                   time.Now().UTC().Add(time.Hour),
		AuthorityDomain:             "continuity-lifecycle-domain",
		AuthorityOwnerPrincipal:     "agent:continuity-lifecycle",
		AuthorityOwnerPrincipalKind: "agent",
	}))

	return continuityLifecycleFixture{
		db:        db,
		memories:  memories,
		slots:     slots,
		target:    target,
		successor: successor,
		project:   project,
	}
}

func continuityLifecycleAuditContext() context.Context {
	ctx := auditcontext.WithActor(context.Background(), "agent/lifecycle-audit")
	return auditcontext.WithSourceSession(ctx, "lifecycle-audit-session")
}

func continuityLifecycleAuditEntries(t *testing.T, fixture continuityLifecycleFixture) []AuditLogEntry {
	t.Helper()
	var entries []AuditLogEntry
	require.NoError(t, fixture.db.Where("action = ? AND reason LIKE ?", continuitySlotLifecycleAuditAction, "%"+fixture.project+"%").Find(&entries).Error)
	return entries
}

type continuityLifecycleQueryProbeKey struct{}

func continuityLifecycleTargetLockProbe(t *testing.T, db *gormlib.DB, ctx context.Context) (context.Context, <-chan struct{}) {
	t.Helper()

	marker := fmt.Sprintf("continuity-lifecycle-target-lock-%d", time.Now().UnixNano())
	ctx = context.WithValue(ctx, continuityLifecycleQueryProbeKey{}, marker)
	started := make(chan struct{})
	var startOnce sync.Once
	callbacks := db.Callback().Query()
	callbackName := "continuity_lifecycle_target_lock_" + marker
	require.NoError(t, callbacks.Before("gorm:query").Register(callbackName, func(tx *gormlib.DB) {
		if tx.Statement.Table == "memories" && tx.Statement.Context.Value(continuityLifecycleQueryProbeKey{}) == marker {
			startOnce.Do(func() { close(started) })
		}
	}))
	t.Cleanup(func() { _ = callbacks.Remove(callbackName) })
	return ctx, started
}

func TestMemoryStoreInvalidationsClearContinuitySlot(t *testing.T) {
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	cases := []struct {
		name               string
		invalidationAction string
		mutate             func(context.Context, continuityLifecycleFixture) error
		verify             func(*testing.T, context.Context, continuityLifecycleFixture)
	}{
		{
			name:               "DeleteTx",
			invalidationAction: "delete",
			mutate: func(ctx context.Context, fixture continuityLifecycleFixture) error {
				return fixture.db.Transaction(func(tx *gormlib.DB) error {
					return fixture.memories.DeleteTx(ctx, tx, fixture.target.ID)
				})
			},
			verify: func(t *testing.T, ctx context.Context, fixture continuityLifecycleFixture) {
				var row Memory
				require.NoError(t, fixture.db.Unscoped().WithContext(ctx).First(&row, fixture.target.ID).Error)
				assert.NotNil(t, row.DeletedAt)
			},
		},
		{
			name:               "SupersedeTx",
			invalidationAction: "supersede",
			mutate: func(ctx context.Context, fixture continuityLifecycleFixture) error {
				return fixture.db.Transaction(func(tx *gormlib.DB) error {
					_, err := fixture.memories.SupersedeTx(ctx, tx, fixture.target.ID)
					return err
				})
			},
			verify: func(t *testing.T, ctx context.Context, fixture continuityLifecycleFixture) {
				row, err := fixture.memories.GetForSnapshot(ctx, fixture.target.ID)
				require.NoError(t, err)
				assert.Equal(t, "superseded", row.Status)
			},
		},
		{
			name:               "MarkSupersededTx",
			invalidationAction: "mark_superseded",
			mutate: func(ctx context.Context, fixture continuityLifecycleFixture) error {
				return fixture.db.Transaction(func(tx *gormlib.DB) error {
					return fixture.memories.MarkSupersededTx(ctx, tx, fixture.target.ID, fixture.successor.ID)
				})
			},
			verify: func(t *testing.T, ctx context.Context, fixture continuityLifecycleFixture) {
				row, err := fixture.memories.GetForSnapshot(ctx, fixture.target.ID)
				require.NoError(t, err)
				assert.Equal(t, "superseded", row.Status)
				require.NotNil(t, row.SupersededBy)
				assert.Equal(t, fixture.successor.ID, *row.SupersededBy)
			},
		},
		{
			name:               "HardDeleteTx",
			invalidationAction: "hard_delete",
			mutate: func(ctx context.Context, fixture continuityLifecycleFixture) error {
				return fixture.db.Transaction(func(tx *gormlib.DB) error {
					return fixture.memories.HardDeleteTx(ctx, tx, fixture.target.ID)
				})
			},
			verify: func(t *testing.T, ctx context.Context, fixture continuityLifecycleFixture) {
				_, err := fixture.memories.GetForSnapshot(ctx, fixture.target.ID)
				require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newContinuityLifecycleFixture(t)
			ctx := continuityLifecycleAuditContext()

			require.NoError(t, tc.mutate(ctx, fixture))
			_, err := fixture.slots.Get(ctx, fixture.project)
			require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
			tc.verify(t, ctx, fixture)

			entries := continuityLifecycleAuditEntries(t, fixture)
			require.Len(t, entries, 1)
			entry := entries[0]
			assert.Equal(t, "agent/lifecycle-audit", entry.Actor)
			assert.Equal(t, "lifecycle-audit-session", entry.SourceSessionID)
			assert.Contains(t, entry.Reason, fmt.Sprintf("project=%q", fixture.project))
			assert.Contains(t, entry.Reason, fmt.Sprintf("target_memory_id=%d", fixture.target.ID))
			assert.Contains(t, entry.Reason, "invalidation_action="+tc.invalidationAction)
			assert.Nil(t, entry.BeforeState)
			assert.Nil(t, entry.AfterState)
			assert.NotContains(t, entry.Reason, fixture.target.Content)
			if tc.name != "HardDeleteTx" {
				require.NotNil(t, entry.MemoryID)
				assert.Equal(t, fixture.target.ID, *entry.MemoryID)
			}
		})
	}
}

func TestMemoryStoreInvalidationRollbackPreservesMemoryAndContinuitySlot(t *testing.T) {
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	sentinel := errors.New("force transaction rollback")
	cases := []struct {
		name   string
		mutate func(context.Context, *gormlib.DB, continuityLifecycleFixture) error
	}{
		{
			name: "DeleteTx",
			mutate: func(ctx context.Context, tx *gormlib.DB, fixture continuityLifecycleFixture) error {
				return fixture.memories.DeleteTx(ctx, tx, fixture.target.ID)
			},
		},
		{
			name: "SupersedeTx",
			mutate: func(ctx context.Context, tx *gormlib.DB, fixture continuityLifecycleFixture) error {
				_, err := fixture.memories.SupersedeTx(ctx, tx, fixture.target.ID)
				return err
			},
		},
		{
			name: "MarkSupersededTx",
			mutate: func(ctx context.Context, tx *gormlib.DB, fixture continuityLifecycleFixture) error {
				return fixture.memories.MarkSupersededTx(ctx, tx, fixture.target.ID, fixture.successor.ID)
			},
		},
		{
			name: "HardDeleteTx",
			mutate: func(ctx context.Context, tx *gormlib.DB, fixture continuityLifecycleFixture) error {
				return fixture.memories.HardDeleteTx(ctx, tx, fixture.target.ID)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newContinuityLifecycleFixture(t)
			ctx := continuityLifecycleAuditContext()

			err := fixture.db.Transaction(func(tx *gormlib.DB) error {
				require.NoError(t, tc.mutate(ctx, tx, fixture))
				return sentinel
			})
			require.ErrorIs(t, err, sentinel)

			_, err = fixture.slots.Get(ctx, fixture.project)
			require.NoError(t, err)
			row, err := fixture.memories.GetForSnapshot(ctx, fixture.target.ID)
			require.NoError(t, err)
			assert.Nil(t, row.DeletedAt)
			assert.NotEqual(t, "superseded", row.Status)
			assert.Empty(t, continuityLifecycleAuditEntries(t, fixture))
		})
	}
}

func TestMemoryStoreUnrelatedLifecycleUpdatesKeepContinuitySlot(t *testing.T) {
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	cases := []struct {
		name   string
		fields map[string]any
	}{
		{
			name:   "status update",
			fields: map[string]any{"status": "flagged"},
		},
		{
			name:   "expiry update",
			fields: map[string]any{"valid_until": time.Now().UTC().Add(time.Hour)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newContinuityLifecycleFixture(t)
			ctx := continuityLifecycleAuditContext()

			require.NoError(t, fixture.memories.UpdateLifecycleFields(ctx, fixture.target.ID, tc.fields))
			slot, err := fixture.slots.Get(ctx, fixture.project)
			require.NoError(t, err)
			assert.Equal(t, fixture.target.ID, slot.MemoryID)
			assert.Empty(t, continuityLifecycleAuditEntries(t, fixture))
		})
	}
}

func TestMemoryStoreInvalidationWithoutSlotDoesNotAudit(t *testing.T) {
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	fixture := newContinuityLifecycleFixture(t)
	ctx := continuityLifecycleAuditContext()
	cleared, err := fixture.slots.Clear(ctx, fixture.project)
	require.NoError(t, err)
	require.True(t, cleared)

	require.NoError(t, fixture.memories.Delete(ctx, fixture.target.ID))
	assert.Empty(t, continuityLifecycleAuditEntries(t, fixture))
}

func TestMemoryStoreHardDeleteClearsContinuitySlotWhenFlagsOff(t *testing.T) {
	cases := []struct {
		name               string
		continuitySlotFlag string
		vnextFlag          string
	}{
		{name: "continuity slot flag off", continuitySlotFlag: "false", vnextFlag: "true"},
		{name: "vnext flag off", continuitySlotFlag: "true", vnextFlag: "false"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", tc.continuitySlotFlag)
			t.Setenv("ENGRAM_VNEXT_ENABLED", tc.vnextFlag)
			fixture := newContinuityLifecycleFixture(t)
			ctx := continuityLifecycleAuditContext()

			require.NoError(t, fixture.db.Transaction(func(tx *gormlib.DB) error {
				return fixture.memories.HardDeleteTx(ctx, tx, fixture.target.ID)
			}))
			_, err := fixture.slots.Get(ctx, fixture.project)
			require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
			_, err = fixture.memories.GetForSnapshot(ctx, fixture.target.ID)
			require.ErrorIs(t, err, gormlib.ErrRecordNotFound)

			entries := continuityLifecycleAuditEntries(t, fixture)
			require.Len(t, entries, 1)
			assert.Contains(t, entries[0].Reason, "invalidation_action=hard_delete")
		})
	}
}

func TestMemoryStoreInvalidationsClearSlotAfterConcurrentSet(t *testing.T) {
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	cases := []struct {
		name               string
		invalidationAction string
		mutate             func(context.Context, *gormlib.DB, continuityLifecycleFixture) error
		verify             func(*testing.T, context.Context, continuityLifecycleFixture)
	}{
		{
			name:               "DeleteTx",
			invalidationAction: "delete",
			mutate: func(ctx context.Context, tx *gormlib.DB, fixture continuityLifecycleFixture) error {
				return fixture.memories.DeleteTx(ctx, tx, fixture.target.ID)
			},
			verify: func(t *testing.T, ctx context.Context, fixture continuityLifecycleFixture) {
				var row Memory
				require.NoError(t, fixture.db.Unscoped().WithContext(ctx).First(&row, fixture.target.ID).Error)
				assert.NotNil(t, row.DeletedAt)
			},
		},
		{
			name:               "SupersedeTx",
			invalidationAction: "supersede",
			mutate: func(ctx context.Context, tx *gormlib.DB, fixture continuityLifecycleFixture) error {
				_, err := fixture.memories.SupersedeTx(ctx, tx, fixture.target.ID)
				return err
			},
			verify: func(t *testing.T, ctx context.Context, fixture continuityLifecycleFixture) {
				row, err := fixture.memories.GetForSnapshot(ctx, fixture.target.ID)
				require.NoError(t, err)
				assert.Equal(t, "superseded", row.Status)
			},
		},
		{
			name:               "MarkSupersededTx",
			invalidationAction: "mark_superseded",
			mutate: func(ctx context.Context, tx *gormlib.DB, fixture continuityLifecycleFixture) error {
				return fixture.memories.MarkSupersededTx(ctx, tx, fixture.target.ID, fixture.successor.ID)
			},
			verify: func(t *testing.T, ctx context.Context, fixture continuityLifecycleFixture) {
				row, err := fixture.memories.GetForSnapshot(ctx, fixture.target.ID)
				require.NoError(t, err)
				assert.Equal(t, "superseded", row.Status)
				require.NotNil(t, row.SupersededBy)
				assert.Equal(t, fixture.successor.ID, *row.SupersededBy)
			},
		},
		{
			name:               "HardDeleteTx",
			invalidationAction: "hard_delete",
			mutate: func(ctx context.Context, tx *gormlib.DB, fixture continuityLifecycleFixture) error {
				return fixture.memories.HardDeleteTx(ctx, tx, fixture.target.ID)
			},
			verify: func(t *testing.T, ctx context.Context, fixture continuityLifecycleFixture) {
				_, err := fixture.memories.GetForSnapshot(ctx, fixture.target.ID)
				require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newContinuityLifecycleFixture(t)
			ctx, cancel := context.WithTimeout(continuityLifecycleAuditContext(), 5*time.Second)
			defer cancel()

			cleared, err := fixture.slots.Clear(ctx, fixture.project)
			require.NoError(t, err)
			require.True(t, cleared)
			_, err = fixture.slots.Get(ctx, fixture.project)
			require.ErrorIs(t, err, gormlib.ErrRecordNotFound)

			setTx := fixture.db.WithContext(ctx).Begin()
			require.NoError(t, setTx.Error)
			defer func() { _ = setTx.Rollback().Error }()
			require.NoError(t, setTx.Exec("SELECT 1 FROM memories WHERE id = ? FOR UPDATE", fixture.target.ID).Error)

			lifecycleCtx, targetLockStarted := continuityLifecycleTargetLockProbe(t, fixture.db, ctx)
			result := make(chan error, 1)
			go func() {
				result <- fixture.db.WithContext(lifecycleCtx).Transaction(func(tx *gormlib.DB) error {
					return tc.mutate(lifecycleCtx, tx, fixture)
				})
			}()

			select {
			case <-targetLockStarted:
			case <-ctx.Done():
				t.Fatal("lifecycle invalidation did not attempt the target lock")
			}

			require.NoError(t, fixture.slots.UpsertTx(ctx, setTx, ContinuitySlot{
				Project:                     fixture.project,
				MemoryID:                    fixture.target.ID,
				ExpiresAt:                   time.Now().UTC().Add(time.Hour),
				AuthorityDomain:             "continuity-lifecycle-domain",
				AuthorityOwnerPrincipal:     "agent:continuity-lifecycle",
				AuthorityOwnerPrincipalKind: "agent",
			}))
			require.NoError(t, setTx.Commit().Error)

			select {
			case err := <-result:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("lifecycle invalidation did not finish after set committed")
			}

			_, err = fixture.slots.Get(ctx, fixture.project)
			require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
			tc.verify(t, ctx, fixture)
			entries := continuityLifecycleAuditEntries(t, fixture)
			require.Len(t, entries, 1)
			assert.Contains(t, entries[0].Reason, "invalidation_action="+tc.invalidationAction)
		})
	}
}
