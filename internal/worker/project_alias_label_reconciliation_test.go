package worker

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestProjectAliasPostApplyLabelReconciliationPostgres(t *testing.T) {
	store := reviewDeltaStore(t)
	db := store.DB
	target, source, unrelated := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{target, source, unrelated} {
		require.NoError(t, db.Create(&gormdb.Project{ID: id, DisplayName: sql.NullString{String: "original", Valid: true}}).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'baseline')`, unrelated, unrelated).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'target-baseline')`, target, target).Error)
	svc := &Service{store: store, sessionStore: gormdb.NewSessionStore(store), vault: newTestVault(t)}
	router := chi.NewRouter()
	base := "/api/projects/consolidation/aliases"
	router.Post(base+"/prepare", svc.handleProjectAliasPrepare)
	router.Post(base+"/apply", svc.handleProjectAliasApply)
	router.Get(base+"/status", svc.handleProjectAliasStatus)
	router.Post(base+"/rollback", svc.handleProjectAliasRollback)
	router.Patch("/api/projects/{id}", svc.handlePatchProjectLabel)
	admin := auth.SessionForBrowserUser("admin", 41)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req = req.WithContext(auth.WithIdentity(req.Context(), admin))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	req := gormdb.ProjectAliasAttachRequest{Target: target, Aliases: []gormdb.ProjectAliasExpected{{Source: source}}, Reason: "synthetic label reconciliation"}
	prepared := call(http.MethodPost, base+"/prepare", req)
	require.Equal(t, http.StatusOK, prepared.Code, prepared.Body.String())
	var plan gormdb.ProjectAliasPlan
	require.NoError(t, json.Unmarshal(prepared.Body.Bytes(), &plan))
	apply := gormdb.ProjectAliasApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, IdempotencyKey: uuid.NewString()}
	first := call(http.MethodPost, base+"/apply", apply)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	rename := call(http.MethodPatch, "/api/projects/"+target, map[string]any{"display_name": "renamed", "expected_display_name": "original"})
	require.Equal(t, http.StatusOK, rename.Code, rename.Body.String())
	require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", target).Update("git_remote", "synthetic-drift").Error)
	assert.Equal(t, http.StatusConflict, call(http.MethodGet, base+"/status?idempotency_key="+apply.IdempotencyKey, nil).Code, "non-cosmetic identity drift remains bound")
	require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", target).Update("git_remote", nil).Error)
	status := call(http.MethodGet, base+"/status?idempotency_key="+apply.IdempotencyKey, nil)
	assert.Equal(t, http.StatusOK, status.Code, status.Body.String())
	replay := call(http.MethodPost, base+"/apply", apply)
	assert.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	rollback := gormdb.ProjectAliasRollbackRequest{ApplyKey: apply.IdempotencyKey, IdempotencyKey: uuid.NewString()}
	inverse := call(http.MethodPost, base+"/rollback", rollback)
	assert.Equal(t, http.StatusOK, inverse.Code, inverse.Body.String())
	var label string
	require.NoError(t, db.Raw(`SELECT display_name FROM projects WHERE id = ?`, target).Scan(&label).Error)
	assert.Equal(t, "renamed", label)
	var owned, baseline int64
	require.NoError(t, db.Table("project_consolidation_redirects").Where("selector = ?", source).Count(&owned).Error)
	require.NoError(t, db.Table("project_consolidation_redirects").Where("selector = ? AND project_id = ? AND plan_fingerprint = ?", unrelated, unrelated, "baseline").Count(&baseline).Error)
	var related int64
	require.NoError(t, db.Table("project_consolidation_redirects").Where("selector = ? AND session_id = '' AND project_id = ? AND plan_fingerprint = ?", target, target, "target-baseline").Count(&related).Error)
	assert.EqualValues(t, 1, related, "inverse preserves the related target-self baseline")
	assert.Zero(t, owned, "inverse removes only operation-owned redirects")
	assert.EqualValues(t, 1, baseline, "inverse preserves the baseline redirect")
	t.Logf("rename status=%d replay=%d inverse=%d label=%s source_redirects=%d related_baseline=%d", status.Code, replay.Code, inverse.Code, label, owned, related)
	t.Run("legacy_receipt_remains_strict", func(t *testing.T) {
		oldTarget, oldSource := uuid.NewString(), uuid.NewString()
		for _, id := range []string{oldTarget, oldSource} {
			require.NoError(t, db.Create(&gormdb.Project{ID: id, DisplayName: sql.NullString{String: "old", Valid: true}}).Error)
		}
		oldRequest := gormdb.ProjectAliasAttachRequest{Target: oldTarget, Aliases: []gormdb.ProjectAliasExpected{{Source: oldSource}}, Reason: "legacy receipt fixture"}
		oldPrepared := call(http.MethodPost, base+"/prepare", oldRequest)
		require.Equal(t, http.StatusOK, oldPrepared.Code, oldPrepared.Body.String())
		var oldPlan gormdb.ProjectAliasPlan
		require.NoError(t, json.Unmarshal(oldPrepared.Body.Bytes(), &oldPlan))
		oldApply := gormdb.ProjectAliasApplyRequest{PlanReference: oldPlan.Reference, PlanFingerprint: oldPlan.Fingerprint, IdempotencyKey: uuid.NewString()}
		require.Equal(t, http.StatusOK, call(http.MethodPost, base+"/apply", oldApply).Code)
		require.NoError(t, db.Exec(`UPDATE project_consolidation_operations SET receipt = receipt - 'reconciliation_snapshot' WHERE idempotency_key = ?`, oldApply.IdempotencyKey).Error)
		require.Equal(t, http.StatusOK, call(http.MethodPatch, "/api/projects/"+oldTarget, map[string]any{"display_name": "new", "expected_display_name": "old"}).Code)
		assert.Equal(t, http.StatusConflict, call(http.MethodGet, base+"/status?idempotency_key="+oldApply.IdempotencyKey, nil).Code)
		assert.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/apply", oldApply).Code)
		oldRollback := gormdb.ProjectAliasRollbackRequest{ApplyKey: oldApply.IdempotencyKey, IdempotencyKey: uuid.NewString()}
		assert.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/rollback", oldRollback).Code)
		var retained int64
		require.NoError(t, db.Table("project_consolidation_redirects").Where("selector = ?", oldSource).Count(&retained).Error)
		assert.EqualValues(t, 1, retained)
	})
}
