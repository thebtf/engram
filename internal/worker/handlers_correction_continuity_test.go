package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

func TestProcessCorrectionAsyncClearsContinuitySlotWithSupersession(t *testing.T) {
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	store := openIntegrationStore(t)
	ctx := context.Background()
	project := fmt.Sprintf("test-correction-continuity-%d", time.Now().UnixNano())
	memoryStore := gormdb.NewMemoryStore(store)
	slotStore := gormdb.NewContinuitySlotStore(store.DB)
	t.Cleanup(func() {
		require.NoError(t, store.DB.Where("action = ? AND reason LIKE ?", "continuity_slot_lifecycle_clear", "%"+project+"%").Delete(&gormdb.AuditLogEntry{}).Error)
		require.NoError(t, store.DB.Exec("DELETE FROM project_continuity_slots WHERE project = ?", project).Error)
		require.NoError(t, store.DB.Unscoped().Where("project = ?", project).Delete(&gormdb.Memory{}).Error)
	})

	old, err := memoryStore.Create(ctx, &models.Memory{
		Project:     project,
		Content:     "continuity correction target",
		SourceAgent: "continuity-correction-test",
	})
	require.NoError(t, err)
	require.NoError(t, slotStore.Upsert(ctx, gormdb.ContinuitySlot{
		Project:                     project,
		MemoryID:                    old.ID,
		ExpiresAt:                   time.Now().UTC().Add(time.Hour),
		AuthorityDomain:             "continuity-correction-domain",
		AuthorityOwnerPrincipal:     "agent:continuity-correction",
		AuthorityOwnerPrincipalKind: "agent",
	}))

	service := &Service{ctx: ctx, memoryStore: memoryStore}
	service.processCorrectionAsync(correctionRequest{
		SessionID:   "correction-audit-session",
		Project:     project,
		UserMessage: old.Content,
	})

	_, err = slotStore.Get(ctx, project)
	require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
	updated, err := memoryStore.GetForSnapshot(ctx, old.ID)
	require.NoError(t, err)
	assert.Equal(t, "superseded", updated.Status)
	require.NotNil(t, updated.SupersededBy)
	require.NotZero(t, *updated.SupersededBy)
	assert.NotNil(t, updated.ValidUntil)

	var entry gormdb.AuditLogEntry
	require.NoError(t, store.DB.Where("action = ? AND reason LIKE ?", "continuity_slot_lifecycle_clear", "%"+project+"%").First(&entry).Error)
	assert.Equal(t, "system", entry.Actor)
	assert.Equal(t, "correction-audit-session", entry.SourceSessionID)
	assert.Contains(t, entry.Reason, fmt.Sprintf("target_memory_id=%d", old.ID))
	assert.Contains(t, entry.Reason, "invalidation_action=mark_superseded")
	assert.NotContains(t, entry.Reason, old.Content)
	assert.Nil(t, entry.BeforeState)
	assert.Nil(t, entry.AfterState)
}
