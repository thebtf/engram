package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func TestHandleListBehavioralRules_Success(t *testing.T) {
	project := "test-rules-handler-list-success"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	_, err := brs.Create(context.Background(), &models.BehavioralRule{
		Content:  "handler test: global rule",
		Priority: 90,
	})
	require.NoError(t, err)
	_, err = brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: project rule",
		Priority: 70,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
	w := httptest.NewRecorder()
	svc.handleListBehavioralRules(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp ruleListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Rules, 2)
	require.Equal(t, 2, resp.Total)
	assert.Equal(t, "handler test: global rule", resp.Rules[0].Content)
	assert.Equal(t, "handler test: project rule", resp.Rules[1].Content)

	reqProject := httptest.NewRequest(http.MethodGet, "/api/rules?project="+project, nil)
	wProject := httptest.NewRecorder()
	svc.handleListBehavioralRules(wProject, reqProject)

	require.Equal(t, http.StatusOK, wProject.Code)
	var respProject ruleListResponse
	require.NoError(t, json.Unmarshal(wProject.Body.Bytes(), &respProject))
	require.Len(t, respProject.Rules, 2, "project filter must include global + project scoped rules")
}

func TestHandleCreateBehavioralRule_Success(t *testing.T) {
	project := "test-rules-handler-create-success"
	svc, brs := newRulesTestService(t, project)

	body := `{"project":"` + project + `","content":"handler test: created via REST","priority":42,"edited_by":"operator"}`
	req := newCHIRequestBody(http.MethodPost, "/api/rules", "", "", body)
	w := httptest.NewRecorder()
	svc.handleCreateBehavioralRule(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var created models.BehavioralRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.Greater(t, created.ID, int64(0))
	assert.Equal(t, "handler test: created via REST", created.Content)
	assert.Equal(t, 42, created.Priority)
	assert.Equal(t, "operator", created.EditedBy)
	if assert.NotNil(t, created.Project) {
		assert.Equal(t, project, *created.Project)
	}

	got, err := brs.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.Content, got.Content)
}

// newCHIRequestBody is newCHIRequest with a JSON request body, for PATCH/POST
// handlers that decode r.Body. Mirrors the body-less helper in handlers_projects_test.go.
func newCHIRequestBody(method, target, paramName, paramValue, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(paramName, paramValue)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// newRulesTestService constructs a Service wired with a real BehavioralRulesStore
// backed by the DATABASE_DSN integration database. Skips when DATABASE_DSN is unset.
func newRulesTestService(t *testing.T, project string) (*Service, *dbgorm.BehavioralRulesStore) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)

	brs := dbgorm.NewBehavioralRulesStore(store)
	svc := &Service{behavioralRulesStore: brs}

	t.Cleanup(func() {
		require.NoError(t, store.DB.WithContext(context.Background()).
			Exec("DELETE FROM behavioral_rules WHERE project = ?", project).Error)
		require.NoError(t, store.Close())
	})

	return svc, brs
}

// TestHandleDeleteBehavioralRule_Success verifies that a valid DELETE request
// returns 200 with a JSON receipt {deleted: <id>} and that a subsequent List
// no longer returns the rule.
func TestHandleDeleteBehavioralRule_Success(t *testing.T) {
	project := "test-rules-handler-delete-success"
	svc, brs := newRulesTestService(t, project)

	// Seed a rule.
	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: soft-delete me",
		Priority: 1,
	})
	require.NoError(t, err)
	require.Greater(t, created.ID, int64(0))

	// DELETE /api/rules/{id}
	idStr := strconv.FormatInt(created.ID, 10)
	req := newCHIRequest(http.MethodDelete, "/api/rules/"+idStr, "id", idStr)
	w := httptest.NewRecorder()
	svc.handleDeleteBehavioralRule(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// JSON numbers decode as float64 by default.
	assert.Equal(t, float64(created.ID), resp["deleted"], "receipt must echo the deleted ID")

	// Verify the rule is no longer in List output.
	list, err := brs.List(context.Background(), &projectPtr, 100)
	require.NoError(t, err)
	for _, r := range list {
		assert.NotEqual(t, created.ID, r.ID, "deleted rule must not appear in List")
	}
}

// TestHandleDeleteBehavioralRule_NotFound verifies that deleting a non-existent
// or already-deleted rule returns 404.
func TestHandleDeleteBehavioralRule_NotFound(t *testing.T) {
	project := "test-rules-handler-delete-notfound"
	svc, _ := newRulesTestService(t, project)

	req := newCHIRequest(http.MethodDelete, "/api/rules/999999999", "id", "999999999")
	w := httptest.NewRecorder()
	svc.handleDeleteBehavioralRule(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandleUpdateBehavioralRule_Success verifies that a valid PATCH updates the
// rule's content and priority, bumps its version, and returns the updated row.
func TestHandleUpdateBehavioralRule_Success(t *testing.T) {
	project := "test-rules-handler-update-success"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: original content",
		Priority: 1,
	})
	require.NoError(t, err)
	require.Equal(t, 1, created.Version)

	idStr := strconv.FormatInt(created.ID, 10)
	body := `{"content":"handler test: edited content","priority":7,"edited_by":"operator"}`
	req := newCHIRequestBody(http.MethodPatch, "/api/rules/"+idStr, "id", idStr, body)
	w := httptest.NewRecorder()
	svc.handleUpdateBehavioralRule(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var updated models.BehavioralRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Equal(t, created.ID, updated.ID)
	assert.Equal(t, "handler test: edited content", updated.Content)
	assert.Equal(t, 7, updated.Priority)
	assert.Equal(t, "operator", updated.EditedBy)
	assert.Equal(t, 2, updated.Version, "version must bump on update")

	// Verify persisted.
	got, err := brs.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "handler test: edited content", got.Content)
	assert.Equal(t, 7, got.Priority)
}

// TestHandleUpdateBehavioralRule_PartialPriorityOnly is the regression test for
// the PATCH data-loss bug (gemini HIGH on PR #308): a priority-only update must
// NOT wipe the rule's content. priority is the injection order, so a reorder
// that silently blanked content would poison every future session.
func TestHandleUpdateBehavioralRule_PartialPriorityOnly(t *testing.T) {
	project := "test-rules-handler-update-partial-priority"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: keep this content",
		Priority: 1,
	})
	require.NoError(t, err)

	idStr := strconv.FormatInt(created.ID, 10)
	// Only priority is sent — content and edited_by are omitted (nil).
	req := newCHIRequestBody(http.MethodPatch, "/api/rules/"+idStr, "id", idStr, `{"priority":9}`)
	w := httptest.NewRecorder()
	svc.handleUpdateBehavioralRule(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	got, err := brs.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, 9, got.Priority, "priority must update")
	assert.Equal(t, "handler test: keep this content", got.Content,
		"content must be preserved when only priority is sent (no data loss)")
}

// TestHandleUpdateBehavioralRule_PartialContentOnly verifies a content-only edit
// preserves the existing priority (the mirror of the priority-only case).
func TestHandleUpdateBehavioralRule_PartialContentOnly(t *testing.T) {
	project := "test-rules-handler-update-partial-content"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: original",
		Priority: 5,
	})
	require.NoError(t, err)

	idStr := strconv.FormatInt(created.ID, 10)
	req := newCHIRequestBody(http.MethodPatch, "/api/rules/"+idStr, "id", idStr, `{"content":"handler test: new text"}`)
	w := httptest.NewRecorder()
	svc.handleUpdateBehavioralRule(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	got, err := brs.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "handler test: new text", got.Content, "content must update")
	assert.Equal(t, 5, got.Priority, "priority must be preserved when only content is sent")
}

// TestHandleUpdateBehavioralRule_ExplicitEmptyContent verifies that explicitly
// sending content:"" is rejected with 400 (distinct from omitting content).
func TestHandleUpdateBehavioralRule_ExplicitEmptyContent(t *testing.T) {
	project := "test-rules-handler-update-explicit-empty"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: must survive",
		Priority: 2,
	})
	require.NoError(t, err)

	idStr := strconv.FormatInt(created.ID, 10)
	req := newCHIRequestBody(http.MethodPatch, "/api/rules/"+idStr, "id", idStr, `{"content":""}`)
	w := httptest.NewRecorder()
	svc.handleUpdateBehavioralRule(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "explicit empty content must be rejected")

	// Original content untouched.
	got, err := brs.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "handler test: must survive", got.Content)
}

// TestHandleUpdateBehavioralRule_NotFound verifies a PATCH to a non-existent rule
// returns 404.
func TestHandleUpdateBehavioralRule_NotFound(t *testing.T) {
	project := "test-rules-handler-update-notfound"
	svc, _ := newRulesTestService(t, project)

	body := `{"content":"x","priority":0}`
	req := newCHIRequestBody(http.MethodPatch, "/api/rules/999999999", "id", "999999999", body)
	w := httptest.NewRecorder()
	svc.handleUpdateBehavioralRule(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandleUpdateBehavioralRule_NoFields verifies that a PATCH with no updatable
// fields ({}) is rejected with 400 and does NOT bump the rule's version (no
// spurious DB write). Codex/Gemini review on PR #308.
func TestHandleUpdateBehavioralRule_NoFields(t *testing.T) {
	project := "test-rules-handler-update-nofields"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: untouched",
		Priority: 4,
	})
	require.NoError(t, err)
	require.Equal(t, 1, created.Version)

	idStr := strconv.FormatInt(created.ID, 10)
	req := newCHIRequestBody(http.MethodPatch, "/api/rules/"+idStr, "id", idStr, `{}`)
	w := httptest.NewRecorder()
	svc.handleUpdateBehavioralRule(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	got, err := brs.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Version, "version must NOT bump on a no-op PATCH")
	assert.Equal(t, "handler test: untouched", got.Content)
	assert.Equal(t, 4, got.Priority)
}

// (explicit-empty-content rejection is covered by
// TestHandleUpdateBehavioralRule_ExplicitEmptyContent, which seeds a real rule
// so the empty-content 400 is reached after the existence fetch.)

// TestHandleUpdateBehavioralRule_InvalidID verifies non-numeric/zero IDs return
// 400 (with a non-nil store) or 503 (nil store), mirroring the delete handler.
func TestHandleUpdateBehavioralRule_InvalidID(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		svcNilStore := &Service{}
		req := newCHIRequestBody(http.MethodPatch, "/api/rules/abc", "id", "abc", `{"content":"x"}`)
		w := httptest.NewRecorder()
		svcNilStore.handleUpdateBehavioralRule(w, req)
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		return
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	svcWithStore := &Service{behavioralRulesStore: dbgorm.NewBehavioralRulesStore(store)}

	for _, badID := range []string{"abc", "0", "-1", "1.5", ""} {
		t.Run("id="+badID, func(t *testing.T) {
			req := newCHIRequestBody(http.MethodPatch, "/api/rules/"+badID, "id", badID, `{"content":"x"}`)
			w := httptest.NewRecorder()
			svcWithStore.handleUpdateBehavioralRule(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code, "expected 400 for id=%q", badID)
		})
	}
}

// TestHandleDeleteBehavioralRule_InvalidID verifies that a non-numeric path
// parameter returns 400 without touching the store.
func TestHandleDeleteBehavioralRule_InvalidID(t *testing.T) {
	// No DB needed — the handler rejects before any store call.
	svc := &Service{behavioralRulesStore: nil}

	// Wire a non-nil placeholder so the nil-store guard does not trigger first.
	// Use a real (but unconnected) value obtained from a zero Store to satisfy
	// the type; the handler short-circuits on invalid id before any DB call.
	// Simplest approach: use a Service with a nil store and rely on the fact that
	// the id parse happens before the nil-store check... but looking at the handler,
	// the nil-store check is FIRST. So we need a non-nil store for the id=abc case.
	// Use a minimal approach: set behavioralRulesStore to a non-nil pointer by
	// opening a real store only when DATABASE_DSN is available; otherwise test with
	// a different strategy.
	//
	// Actually: the handler checks nil store first, THEN parses id. To reach the
	// 400 path without a DB we need a non-nil store. We create a zero-value
	// BehavioralRulesStore via the exported constructor with a nil-DB store — the
	// parse error fires before any method is called on the store.
	_ = svc // superseded below

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		// Without a DB we cannot create a non-nil store. Run the nil-store path
		// instead: confirm the handler returns 503 (not a panic).
		svcNilStore := &Service{}
		req := newCHIRequest(http.MethodDelete, "/api/rules/abc", "id", "abc")
		w := httptest.NewRecorder()
		svcNilStore.handleDeleteBehavioralRule(w, req)
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		return
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	svcWithStore := &Service{behavioralRulesStore: dbgorm.NewBehavioralRulesStore(store)}

	for _, badID := range []string{"abc", "0", "-1", "1.5", ""} {
		t.Run("id="+badID, func(t *testing.T) {
			req := newCHIRequest(http.MethodDelete, "/api/rules/"+badID, "id", badID)
			w := httptest.NewRecorder()
			svcWithStore.handleDeleteBehavioralRule(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code, "expected 400 for id=%q", badID)
		})
	}
}
