package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func TestCorrectionDoesNotSupersedeReadAliasPostgres(t *testing.T) {
	store := openWorkerUCIContextCompositionStore(t)
	memoryStore := gormdb.NewMemoryStore(store)
	ctx := context.Background()
	service := &Service{ctx: ctx, memoryStore: memoryStore}

	for _, tc := range []struct {
		name  string
		alias bool
	}{
		{name: "same_native_project"},
		{name: "read_alias_is_not_mutation_authority", alias: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := uuid.NewString(), uuid.NewString()
			for _, project := range []string{a, b} {
				require.NoError(t, store.DB.Create(&gormdb.Project{ID: project}).Error)
			}
			native := b
			if tc.alias {
				native = a
			}
			old, err := memoryStore.Create(ctx, &models.Memory{
				Project: native, Content: "correction target distinct words", SourceAgent: "correction-fixture",
			})
			require.NoError(t, err)
			var before gormdb.Memory
			require.NoError(t, store.DB.First(&before, old.ID).Error)
			if tc.alias {
				require.NoError(t, store.DB.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'fixture')`, a, b).Error)
			}
			visible, err := memoryStore.List(ctx, b, 100)
			require.NoError(t, err)
			require.Len(t, visible, 1, "the old memory must be visible to the correction reader")
			require.Equal(t, old.ID, visible[0].ID)

			service.processCorrectionAsync(correctionRequest{Project: b, UserMessage: old.Content, SessionID: "correction-fixture"})

			var persistedOld gormdb.Memory
			require.NoError(t, store.DB.First(&persistedOld, old.ID).Error)
			var created []gormdb.Memory
			require.NoError(t, store.DB.Where("source_agent = ? AND project = ?", "user_correction", b).Find(&created).Error)
			require.Len(t, created, 1, "correction must create a B-native memory")
			assert.Equal(t, b, created[0].Project)
			assert.Equal(t, old.Content, created[0].Content)
			assert.Equal(t, "active", created[0].Status)
			assert.Nil(t, created[0].SupersededBy)
			assert.Equal(t, before.ValidUntil, created[0].ValidUntil)
			assert.Equal(t, native, persistedOld.Project)
			if tc.alias {
				assert.Equal(t, "active", persistedOld.Status)
				assert.Nil(t, persistedOld.SupersededBy)
				assert.Equal(t, before.ValidUntil, persistedOld.ValidUntil)
				assert.Nil(t, created[0].SupersedesID)
				assert.NotContains(t, created[0].Tags, "supersedes")
			} else {
				assert.Equal(t, "superseded", persistedOld.Status)
				if assert.NotNil(t, persistedOld.SupersededBy) {
					assert.Equal(t, created[0].ID, *persistedOld.SupersededBy)
				}
				assert.NotNil(t, persistedOld.ValidUntil)
				if assert.NotNil(t, created[0].SupersedesID) {
					assert.Equal(t, old.ID, *created[0].SupersedesID)
				}
				assert.Contains(t, created[0].Tags, "supersedes")
			}
		})
	}
}

func TestCorrectionHTTPSelectsNativeBeforeLimitPostgres(t *testing.T) {
	store := openWorkerUCIContextCompositionStore(t)
	ctx := context.Background()
	memories := gormdb.NewMemoryStore(store)
	a, b := uuid.NewString(), uuid.NewString()
	for _, project := range []string{a, b} {
		require.NoError(t, store.DB.Create(&gormdb.Project{ID: project}).Error)
	}
	const content = "native candidate correction target words"
	old, err := memories.Create(ctx, &models.Memory{Project: b, Content: content, SourceAgent: "fixture"})
	require.NoError(t, err)
	foreign := make([]gormdb.Memory, 101)
	for i := range foreign {
		foreign[i] = gormdb.Memory{Project: a, Content: content, Status: "active", PrivacyScope: "project", Version: 1}
	}
	require.NoError(t, store.DB.Create(&foreign).Error)
	private := gormdb.Memory{Project: b, Content: content, Status: "active", PrivacyScope: "private", Version: 1}
	require.NoError(t, store.DB.Create(&private).Error)
	require.NoError(t, store.DB.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'fixture')`, a, b).Error)
	visible, err := memories.List(ctx, b, 100)
	require.NoError(t, err)
	require.Len(t, visible, 100)
	for _, row := range visible {
		require.NotEqual(t, old.ID, row.ID, "alias-expanded limit must otherwise hide the native candidate")
	}
	body, err := json.Marshal(map[string]string{"project": b, "session_id": "correction-http-fixture", "user_message": content})
	require.NoError(t, err)
	service := &Service{ctx: ctx, memoryStore: memories}
	recorder := httptest.NewRecorder()
	service.handleCorrection(recorder, httptest.NewRequest(http.MethodPost, "/api/hooks/correction", bytes.NewReader(body)))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	service.wg.Wait()
	var updated, untouched gormdb.Memory
	require.NoError(t, store.DB.First(&updated, old.ID).Error)
	require.NoError(t, store.DB.First(&untouched, private.ID).Error)
	assert.Equal(t, "active", untouched.Status)
	assert.Nil(t, untouched.SupersededBy)
	assert.Equal(t, "superseded", updated.Status)
	require.NotNil(t, updated.SupersededBy)
	var successor gormdb.Memory
	require.NoError(t, store.DB.First(&successor, *updated.SupersededBy).Error)
	assert.Equal(t, b, successor.Project)
	require.NotNil(t, successor.SupersedesID)
	assert.Equal(t, old.ID, *successor.SupersedesID)
	var mutated int64
	require.NoError(t, store.DB.Model(&gormdb.Memory{}).Where("id IN ? AND status <> ?", func() []int64 {
		ids := make([]int64, len(foreign))
		for i := range foreign {
			ids[i] = foreign[i].ID
		}
		return ids
	}(), "active").Count(&mutated).Error)
	assert.Zero(t, mutated)
}
