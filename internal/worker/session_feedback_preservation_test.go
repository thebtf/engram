package worker

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/feedback"
	"github.com/thebtf/engram/internal/scope"
)

func TestSessionEndFeedbackPreservesHistoricalSelectors(t *testing.T) {
	store := openContextSearchPrivacyStore(t)
	db := store.DB
	ctx := context.Background()
	project := uuid.NewString()
	require.NoError(t, db.Create(&gormdb.Project{ID: project}).Error)
	seed := db.Begin()
	require.NoError(t, seed.Error)
	defer seed.Rollback()
	require.NoError(t, seed.Exec("SET LOCAL session_replication_role=replica").Error)
	rows := []gormdb.Memory{
		{Project: "feedback-former", Content: "historical feedback example", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: "feedback-race", Content: "membership race feedback example", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: project, Content: "canonical feedback example", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: "", Content: "global feedback example", Status: "active", PrivacyScope: "project", Version: 1},
	}
	for i := range rows {
		require.NoError(t, seed.Create(&rows[i]).Error)
	}
	for i := range 2 {
		require.NoError(t, seed.Create(&gormdb.ConsolidationMembership{Reference: fmt.Sprintf("urn:feedback:%d", i), Table: "memories", RowKey: gormdb.JSONRaw(fmt.Sprintf(`{"id":%d}`, rows[i].ID)), FieldPath: gormdb.JSONRaw(`["project"]`), ProjectID: &project, PlanFingerprint: "feedback-test"}).Error)
	}
	require.NoError(t, seed.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?,?,?::uuid,?)`, rows[0].Project, "", project, "feedback-test").Error)
	require.NoError(t, seed.Commit().Error)
	memories := gormdb.NewMemoryStore(store)
	for i := range 2 {
		visible, err := memories.GetVisible(ctx, rows[i].ID, project, scope.KeycardContext{}, scope.MemoryVisibilityOptions{})
		require.NoError(t, err)
		require.Equal(t, project, visible.Project)
	}
	// Membership changes after selection must not make automatic updates rewrite a selector.
	require.NoError(t, db.Where("reference = ?", "urn:feedback:1").Delete(&gormdb.ConsolidationMembership{}).Error)
	capture := func(id int64) string {
		t.Helper()
		var raw string
		require.NoError(t, db.Raw("SELECT row_to_json(m)::text FROM memories m WHERE id = ?", id).Row().Scan(&raw))
		return raw
	}
	before := []string{capture(rows[0].ID), capture(rows[1].ID)}
	injections := gormdb.NewInjectionLogStore(store)
	citations := gormdb.NewCitationLogStore(store)
	session := uuid.NewString()
	require.NoError(t, injections.Record(ctx, session, project, []int64{rows[0].ID, rows[2].ID, rows[3].ID}))
	s := &Service{ctx: ctx, injectionLogStore: injections, memoryStore: memories, citationLogStore: citations, feedbackUpdater: feedback.NewUpdater(memories)}
	body := fmt.Sprintf(`{"session_id":%q,"project":%q,"agent_output_text":"unrelated response"}`, session, project)
	response := httptest.NewRecorder()
	s.handleSessionEnd(response, httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewBufferString(body)))
	require.Equal(t, http.StatusAccepted, response.Code)
	s.wg.Wait()
	logged, err := citations.GetBySession(ctx, session)
	require.NoError(t, err)
	require.Len(t, logged, 3)
	for _, entry := range logged {
		require.False(t, entry.Cited)
	}
	for i := range 2 {
		require.Equal(t, before[i], capture(rows[i].ID), "session-end feedback must preserve the historical row")
	}
	for i := 2; i < 4; i++ {
		var got gormdb.Memory
		require.NoError(t, db.First(&got, rows[i].ID).Error)
		require.InDelta(t, rows[i].TsBeta+1, got.TsBeta, 0.00001)
		require.Equal(t, rows[i].Project, got.Project)
	}
	for _, update := range []struct {
		name string
		call func(context.Context, []int64) error
	}{
		{"cited", func(ctx context.Context, ids []int64) error { return memories.BatchIncrementCitedN(ctx, ids, 1, 1) }},
		{"uncited", func(ctx context.Context, ids []int64) error { return memories.BatchIncrementUncitedN(ctx, ids, 1) }},
		{"violated", func(ctx context.Context, ids []int64) error { return memories.BatchIncrementViolated(ctx, ids, 3) }},
	} {
		t.Run(update.name, func(t *testing.T) {
			var prior, priorGlobal gormdb.Memory
			require.NoError(t, db.First(&prior, rows[2].ID).Error)
			require.NoError(t, db.First(&priorGlobal, rows[3].ID).Error)
			require.NoError(t, update.call(ctx, []int64{rows[0].ID, rows[1].ID, rows[2].ID, rows[3].ID}))
			for i := range 2 {
				require.Equal(t, before[i], capture(rows[i].ID))
			}
			var after, afterGlobal gormdb.Memory
			require.NoError(t, db.First(&after, rows[2].ID).Error)
			require.NoError(t, db.First(&afterGlobal, rows[3].ID).Error)
			if update.name == "cited" {
				require.Greater(t, after.TsAlpha, prior.TsAlpha)
				require.Greater(t, afterGlobal.TsAlpha, priorGlobal.TsAlpha)
			} else {
				require.Greater(t, after.TsBeta, prior.TsBeta)
				require.Greater(t, afterGlobal.TsBeta, priorGlobal.TsBeta)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			require.ErrorIs(t, update.call(canceled, []int64{rows[2].ID}), context.Canceled)
		})
	}
	require.ErrorContains(t, memories.UpdateLifecycleFields(ctx, rows[1].ID, map[string]any{"access_count": 2}), "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	require.NoError(t, db.Exec(fmt.Sprintf("ALTER TABLE memories ADD CONSTRAINT feedback_test_reject CHECK (id <> %d OR ts_alpha < 0) NOT VALID", rows[2].ID)).Error)
	require.ErrorContains(t, memories.BatchIncrementCitedN(ctx, []int64{rows[2].ID}, 1, 1), "feedback_test_reject")
	require.ErrorContains(t, memories.BatchIncrementUncitedN(ctx, []int64{rows[2].ID}, 1), "feedback_test_reject")
	require.ErrorContains(t, memories.BatchIncrementViolated(ctx, []int64{rows[2].ID}, 1), "feedback_test_reject")
}
