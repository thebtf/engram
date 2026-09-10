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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// newRulesTestService constructs a Service wired with a real BehavioralRulesStore
// backed by the required DATABASE_DSN integration database.
func newRulesTestService(t *testing.T, project string) (*Service, *dbgorm.BehavioralRulesStore) {
	t.Helper()
	svc, brs, _, _ := newRulesSelectionTestService(t, project)
	return svc, brs
}

func newRulesSelectionTestService(t *testing.T, project string) (*Service, *dbgorm.BehavioralRulesStore, *dbgorm.CollectionSelectionStore, *dbgorm.Store) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	require.NotEmpty(t, dsn, "DATABASE_DSN is required for Rules PostgreSQL tests")
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)

	brs := dbgorm.NewBehavioralRulesStore(store)
	svc := &Service{behavioralRulesStore: brs}
	t.Cleanup(func() {
		require.NoError(t, store.DB.WithContext(context.Background()).
			Exec("DELETE FROM behavioral_rules WHERE project = ?", project).Error)
		require.NoError(t, store.Close())
	})
	return svc, brs, dbgorm.NewCollectionSelectionStore(store.DB), store
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

func TestHandleListBehavioralRules_ProjectScope(t *testing.T) {
	project := "test-rules-handler-list-project-scope"
	svc, brs := newRulesTestService(t, project)

	globalContent := "handler test: global rule"
	projectContent := "handler test: project rule"
	otherProject := "test-rules-handler-list-other-project"

	globalRule, err := brs.Create(context.Background(), &models.BehavioralRule{
		Content:  globalContent,
		Priority: 5,
	})
	require.NoError(t, err)

	projectPtr := project
	projectRule, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  projectContent,
		Priority: 10,
	})
	require.NoError(t, err)

	otherProjectPtr := otherProject
	_, err = brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &otherProjectPtr,
		Content:  "handler test: should not leak",
		Priority: 20,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, brs.Delete(context.Background(), globalRule.ID))
		require.NoError(t, storeDeleteRuleByProject(context.Background(), brs, otherProject))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/rules?project="+project+"&limit=100", nil)
	w := httptest.NewRecorder()
	svc.handleListBehavioralRules(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var rows []models.BehavioralRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 2)
	assert.Equal(t, projectRule.ID, rows[0].ID)
	assert.Equal(t, globalRule.ID, rows[1].ID)
}

func TestHandleListBehavioralRules_AllScopes(t *testing.T) {
	project := "test-rules-handler-list-all-scope"
	svc, brs := newRulesTestService(t, project)

	globalRule, err := brs.Create(context.Background(), &models.BehavioralRule{
		Content:  "handler test: global all-scope rule",
		Priority: 5,
	})
	require.NoError(t, err)

	projectPtr := project
	projectRule, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: project all-scope rule",
		Priority: 10,
	})
	require.NoError(t, err)

	otherProject := project + "-other"
	otherProjectPtr := otherProject
	otherRule, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &otherProjectPtr,
		Content:  "handler test: other project all-scope rule",
		Priority: 20,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, brs.Delete(context.Background(), globalRule.ID))
		require.NoError(t, storeDeleteRuleByProject(context.Background(), brs, otherProject))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/rules?all=true&limit=100", nil)
	w := httptest.NewRecorder()
	svc.handleListBehavioralRules(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var rows []models.BehavioralRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.GreaterOrEqual(t, len(rows), 3)

	ids := map[int64]bool{}
	for _, row := range rows {
		ids[row.ID] = true
	}
	assert.True(t, ids[globalRule.ID], "all=true must include global rules")
	assert.True(t, ids[projectRule.ID], "all=true must include selected project-scoped rules")
	assert.True(t, ids[otherRule.ID], "all=true must include other project-scoped rules")
}

func TestHandleCreateBehavioralRule_Success(t *testing.T) {
	project := "test-rules-handler-create-success"
	svc, brs := newRulesTestService(t, project)

	body := `{"project":"` + project + `","content":"handler test: created over HTTP","priority":12,"edited_by":"operator-console"}`
	req := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleCreateBehavioralRule(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var created models.BehavioralRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.Greater(t, created.ID, int64(0))
	require.Equal(t, "handler test: created over HTTP", created.Content)
	require.Equal(t, 12, created.Priority)
	require.True(t, created.Enabled)
	require.NotNil(t, created.Project)
	require.Equal(t, project, *created.Project)

	rows, err := brs.List(context.Background(), &project, 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, created.ID, rows[0].ID)
}

func TestHandleUpdateBehavioralRule_PartialSuccess(t *testing.T) {
	project := "test-rules-handler-update-success"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: update me",
		Priority: 1,
		EditedBy: "seed",
	})
	require.NoError(t, err)

	idStr := strconv.FormatInt(created.ID, 10)
	req := newCHIRequest(http.MethodPatch, "/api/rules/"+idStr, "id", idStr)
	req.Body = ioNopCloser(`{"priority":7,"edited_by":"operator-console"}`)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleUpdateBehavioralRule(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var updated models.BehavioralRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Equal(t, created.ID, updated.ID)
	assert.Equal(t, created.Content, updated.Content)
	assert.Equal(t, 7, updated.Priority)
	assert.Equal(t, "operator-console", updated.EditedBy)
	assert.True(t, updated.Enabled, "partial update must preserve enabled state")
	assert.Greater(t, updated.Version, created.Version)
}

func TestHandleSetBehavioralRuleEnabled_Success(t *testing.T) {
	project := "test-rules-handler-enabled-success"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: toggle me",
		Priority: 11,
		EditedBy: "seed",
	})
	require.NoError(t, err)
	require.True(t, created.Enabled)

	idStr := strconv.FormatInt(created.ID, 10)
	req := newCHIRequest(http.MethodPatch, "/api/rules/"+idStr+"/enabled", "id", idStr)
	req.Body = ioNopCloser(`{"enabled":false,"edited_by":"operator-console"}`)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleSetBehavioralRuleEnabled(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var updated models.BehavioralRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Equal(t, created.ID, updated.ID)
	assert.False(t, updated.Enabled)
	assert.Equal(t, "operator-console", updated.EditedBy)
	assert.Greater(t, updated.Version, created.Version)

	operatorRows, err := brs.List(context.Background(), &projectPtr, 100)
	require.NoError(t, err)
	require.Len(t, operatorRows, 1)
	assert.False(t, operatorRows[0].Enabled, "disabled rule remains visible to operator list")

	injectionRows, err := brs.ListEnabled(context.Background(), &projectPtr, 100)
	require.NoError(t, err)
	require.Empty(t, injectionRows, "disabled rule must not be injected")
}

func TestHandleSetBehavioralRuleEnabled_RequiresEnabled(t *testing.T) {
	project := "test-rules-handler-enabled-required"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: missing enabled field",
		Priority: 1,
	})
	require.NoError(t, err)

	idStr := strconv.FormatInt(created.ID, 10)
	req := newCHIRequest(http.MethodPatch, "/api/rules/"+idStr+"/enabled", "id", idStr)
	req.Body = ioNopCloser(`{"edited_by":"operator-console"}`)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleSetBehavioralRuleEnabled(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetBehavioralRuleEnabled_NotFound(t *testing.T) {
	project := "test-rules-handler-enabled-not-found"
	svc, _ := newRulesTestService(t, project)

	req := newCHIRequest(http.MethodPatch, "/api/rules/999999999/enabled", "id", "999999999")
	req.Body = ioNopCloser(`{"enabled":false,"edited_by":"operator-console"}`)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleSetBehavioralRuleEnabled(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleSetBehavioralRuleEnabled_OmittedEditedByPreservesAudit(t *testing.T) {
	project := "test-rules-handler-enabled-preserve-edited-by"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	created, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: preserve edited_by",
		Priority: 3,
		EditedBy: "seed-operator",
	})
	require.NoError(t, err)

	idStr := strconv.FormatInt(created.ID, 10)
	req := newCHIRequest(http.MethodPatch, "/api/rules/"+idStr+"/enabled", "id", idStr)
	req.Body = ioNopCloser(`{"enabled":false}`)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleSetBehavioralRuleEnabled(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var updated models.BehavioralRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.False(t, updated.Enabled)
	assert.Equal(t, "seed-operator", updated.EditedBy, "omitted edited_by must preserve the existing audit actor")
}

func TestSearchFallbackObservations_UsesEnabledBehavioralRulesOnly(t *testing.T) {
	project := "test-rules-search-fallback-enabled-only"
	svc, brs := newRulesTestService(t, project)

	projectPtr := project
	enabledRule, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: enabled fallback guidance",
		Priority: 20,
		EditedBy: "seed",
	})
	require.NoError(t, err)

	disabledRule, err := brs.Create(context.Background(), &models.BehavioralRule{
		Project:  &projectPtr,
		Content:  "handler test: disabled fallback guidance",
		Priority: 30,
		EditedBy: "seed",
	})
	require.NoError(t, err)
	editedBy := "operator-console"
	_, err = brs.SetEnabled(context.Background(), disabledRule.ID, false, &editedBy)
	require.NoError(t, err)

	observations, err := svc.searchFallbackObservations(context.Background(), "", retrievalScope{Project: project}, 100)
	require.NoError(t, err)

	byTitle := map[string]bool{}
	for _, observation := range observations {
		byTitle[observation.Title.String] = true
	}
	assert.True(t, byTitle[enabledRule.Content], "enabled rule must remain visible to fallback observations")
	assert.False(t, byTitle[disabledRule.Content], "disabled rule must not leak into data-plane fallback observations")
}

func TestRulesCollectionBridgeRoutesFreezeAndPageOverTwoHundredRules(t *testing.T) {
	ruleStore := openRulesCollectionBridgeStore(t)
	store := dbgorm.NewBehavioralRulesStore(ruleStore)
	ctx := context.Background()
	project := "test-rules-collection-bridge-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		require.NoError(t, ruleStore.DB.Exec("DELETE FROM behavioral_rules WHERE project = ?", project).Error)
	})

	rules := make([]*models.BehavioralRule, 205)
	for index := range rules {
		created, err := store.Create(ctx, &models.BehavioralRule{
			Project:  &project,
			Content:  "collection bridge rule " + strconv.Itoa(index),
			Priority: len(rules) - index,
		})
		require.NoError(t, err)
		rules[index] = created
	}

	adapter, err := composeOperatorCollectionHTTPAdapter(ruleStore.DB)
	require.NoError(t, err)
	service := newOperatorCollectionRouteTestService(adapter)
	identity := auth.SessionForBrowserUser("operator", 41)
	sessionID := "rules-collection-bridge-session-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		require.NoError(t, ruleStore.DB.Exec(
			"DELETE FROM collection_selections WHERE subject_user_id = ? AND session_id = ? AND domain = ?",
			41, sessionID, operatorCollectionSelectionDomain,
		).Error)
	})
	call := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Engram-Request-ID", "rules-collection-bridge-request")
		request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sessionID})
		request = request.WithContext(auth.WithIdentity(request.Context(), identity))
		recorder := httptest.NewRecorder()
		service.router.ServeHTTP(recorder, request)
		return recorder
	}

	firstPage := call("/api/collections/selection/page", `{"domain":"rules","filter":{"scope":"`+project+`"},"limit":200}`)
	require.Equal(t, http.StatusOK, firstPage.Code, firstPage.Body.String())
	var first struct {
		FilterFingerprint string                             `json:"filter_fingerprint"`
		Cursor            string                             `json:"cursor"`
		Targets           []dbgorm.CollectionSelectionTarget `json:"targets"`
		NextCursor        string                             `json:"next_cursor"`
		Total             *int64                             `json:"total"`
	}
	require.NoError(t, json.Unmarshal(firstPage.Body.Bytes(), &first))
	require.NotEmpty(t, first.FilterFingerprint)
	require.NotEmpty(t, first.Cursor)
	require.NotEmpty(t, first.NextCursor)
	require.Len(t, first.Targets, 200)
	require.NotNil(t, first.Total)
	require.EqualValues(t, len(rules), *first.Total)
	require.Equal(t, dbgorm.CollectionSelectionTarget{ID: strconv.FormatInt(rules[0].ID, 10), ExpectedVersion: uint64(rules[0].Version)}, first.Targets[0])

	pageSelection := call("/api/collections/selection", `{"domain":"rules","selection":{"kind":"page","cursor":"`+first.Cursor+`","targets":[{"id":"999999","expected_version":9}]}}`)
	require.Equal(t, http.StatusOK, pageSelection.Code, pageSelection.Body.String())
	var pageSnapshot struct {
		Selection struct {
			Targets []dbgorm.CollectionSelectionTarget `json:"targets"`
			Cursor  string                             `json:"cursor"`
		} `json:"selection"`
	}
	require.NoError(t, json.Unmarshal(pageSelection.Body.Bytes(), &pageSnapshot))
	require.Equal(t, first.Cursor, pageSnapshot.Selection.Cursor)
	require.Equal(t, first.Targets, pageSnapshot.Selection.Targets, "the bridge replaces caller targets with the server-issued page")

	secondPage := call("/api/collections/selection/page", `{"domain":"rules","filter":{"scope":"`+project+`"},"cursor":"`+first.NextCursor+`","limit":200}`)
	require.Equal(t, http.StatusOK, secondPage.Code, secondPage.Body.String())
	var second struct {
		Cursor     string                             `json:"cursor"`
		Targets    []dbgorm.CollectionSelectionTarget `json:"targets"`
		NextCursor string                             `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(secondPage.Body.Bytes(), &second))
	require.Equal(t, first.NextCursor, second.Cursor)
	require.Len(t, second.Targets, 5)
	require.Empty(t, second.NextCursor)
	require.Equal(t, dbgorm.CollectionSelectionTarget{ID: strconv.FormatInt(rules[200].ID, 10), ExpectedVersion: uint64(rules[200].Version)}, second.Targets[0])

	frozenSnapshot := call("/api/collections/selection", `{"domain":"rules","selection":{"kind":"frozen_filter","filter":{"scope":"`+project+`"},"excluded_ids":["`+strconv.FormatInt(rules[0].ID, 10)+`"]}}`)
	require.Equal(t, http.StatusOK, frozenSnapshot.Code, frozenSnapshot.Body.String())
	var frozen struct {
		Selection struct {
			FilterFingerprint string                             `json:"filter_fingerprint"`
			SelectionToken    string                             `json:"selection_token"`
			TargetCount       int                                `json:"target_count"`
			Targets           []dbgorm.CollectionSelectionTarget `json:"targets"`
			ExcludedIDs       []string                           `json:"excluded_ids"`
		} `json:"selection"`
	}
	require.NoError(t, json.Unmarshal(frozenSnapshot.Body.Bytes(), &frozen))
	require.Equal(t, first.FilterFingerprint, frozen.Selection.FilterFingerprint)
	require.NotEmpty(t, frozen.Selection.SelectionToken)
	require.Equal(t, len(rules)-1, frozen.Selection.TargetCount)
	require.Equal(t, []string{strconv.FormatInt(rules[0].ID, 10)}, frozen.Selection.ExcludedIDs)
	require.Len(t, frozen.Selection.Targets, len(rules))
	require.Equal(t, first.Targets[0], frozen.Selection.Targets[0])

	forgedToken := call("/api/collections/selection", `{"domain":"rules","selection":{"kind":"frozen_filter","selection_token":"`+frozen.Selection.SelectionToken+`","filter":{"scope":"`+project+`"}}}`)
	require.Equal(t, http.StatusBadRequest, forgedToken.Code)
	unchanged, err := store.Get(ctx, rules[0].ID)
	require.NoError(t, err)
	require.Equal(t, rules[0].Version, unchanged.Version, "a selection token is not a mutation capability")

	badOrder := call("/api/collections/selection/page", `{"domain":"rules","filter":{"scope":"`+project+`","order":"id DESC"},"limit":1}`)
	require.Equal(t, http.StatusBadRequest, badOrder.Code)
	tooLarge := call("/api/collections/selection/page", `{"domain":"rules","filter":{"scope":"`+project+`"},"limit":201}`)
	require.Equal(t, http.StatusBadRequest, tooLarge.Code)
	crossScope := call("/api/collections/selection/page", `{"domain":"rules","filter":{"scope":"global"},"cursor":"`+first.NextCursor+`","limit":200}`)
	require.Equal(t, http.StatusBadRequest, crossScope.Code)

	_, err = store.SetEnabled(ctx, rules[3].ID, false, nil)
	require.NoError(t, err)
	stale := call("/api/collections/selection/page", `{"domain":"rules","filter":{"scope":"`+project+`"},"cursor":"`+first.NextCursor+`","limit":200}`)
	require.Equal(t, http.StatusPreconditionFailed, stale.Code, stale.Body.String())
}

func openRulesCollectionBridgeStore(t *testing.T) *dbgorm.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	require.NotEmpty(t, dsn, "DATABASE_DSN is required for Rules PostgreSQL tests")
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func storeDeleteRuleByProject(ctx context.Context, brs *dbgorm.BehavioralRulesStore, project string) error {
	rows, err := brs.List(ctx, &project, 200)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Project != nil && *row.Project == project {
			if deleteErr := brs.Delete(ctx, row.ID); deleteErr != nil {
				return deleteErr
			}
		}
	}
	return nil
}

type nopReadCloser struct {
	*strings.Reader
}

func (n nopReadCloser) Close() error { return nil }

func ioNopCloser(body string) nopReadCloser {
	return nopReadCloser{Reader: strings.NewReader(body)}
}

func TestHandleCreateBehavioralRule_SelectionOperationReauthorizesAndReportsTruth(t *testing.T) {
	project := "test-rules-handler-selection-operation"
	const sessionID = "rules-selection-operation-session"
	svc, brs, selections, ruleStore := newRulesSelectionTestService(t, project)
	ctx := context.Background()
	identity := auth.SessionForBrowserUser("operator", 41)
	first, err := brs.Create(ctx, &models.BehavioralRule{Project: &project, Content: "selection operation first", Priority: 20})
	require.NoError(t, err)
	second, err := brs.Create(ctx, &models.BehavioralRule{Project: &project, Content: "selection operation second", Priority: 10})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, ruleStore.DB.Exec(
			"DELETE FROM collection_selections WHERE subject_user_id = ? AND session_id = ? AND domain = ?",
			41, sessionID, operatorCollectionSelectionDomain,
		).Error)
	})
	scope, err := (operatorCollectionScopeAuthority{}).ResolveOperatorCollectionScope(ctx, identity, sessionID, operatorCollectionSelectionDomain)
	require.NoError(t, err)
	explicit, err := selections.Save(ctx, scope, dbgorm.CollectionSelection{
		Kind:    dbgorm.CollectionSelectionExplicit,
		Targets: []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(first.ID, 10), ExpectedVersion: uint64(first.Version)}},
	})
	require.NoError(t, err)

	body := `{"request_id":"rules-operation-disable","action":"disable","selection":{"kind":"explicit","selection_version":` + strconv.FormatInt(explicit.Version, 10) + `}}`
	request := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Engram-Request-ID", "rules-operation-disable")
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sessionID})
	request = request.WithContext(auth.WithIdentity(request.Context(), identity))
	recorder := httptest.NewRecorder()
	svc.handleCreateBehavioralRule(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var completed struct {
		RequestID      string `json:"request_id"`
		OperationState string `json:"operation_state"`
		ItemResults    []struct {
			TargetID int64  `json:"target_id"`
			Outcome  string `json:"outcome"`
		} `json:"item_results"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &completed))
	assert.Equal(t, "rules-operation-disable", completed.RequestID)
	assert.Equal(t, "completed", completed.OperationState)
	require.Equal(t, []struct {
		TargetID int64  `json:"target_id"`
		Outcome  string `json:"outcome"`
	}{{TargetID: first.ID, Outcome: "committed"}}, completed.ItemResults)
	disabled, err := brs.Get(ctx, first.ID)
	require.NoError(t, err)
	assert.False(t, disabled.Enabled)
	assert.Greater(t, disabled.Version, first.Version)

	stale, err := selections.Save(ctx, scope, dbgorm.CollectionSelection{
		Kind:    dbgorm.CollectionSelectionExplicit,
		Targets: []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(second.ID, 10), ExpectedVersion: uint64(second.Version)}},
	})
	require.NoError(t, err)
	_, err = brs.SetEnabled(ctx, second.ID, false, nil)
	require.NoError(t, err)
	body = `{"request_id":"rules-operation-stale","action":"disable","selection":{"kind":"explicit","selection_version":` + strconv.FormatInt(stale.Version, 10) + `}}`
	request = httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Engram-Request-ID", "rules-operation-stale")
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sessionID})
	request = request.WithContext(auth.WithIdentity(request.Context(), identity))
	recorder = httptest.NewRecorder()
	svc.handleCreateBehavioralRule(recorder, request)
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), strconv.FormatInt(second.ID, 10), "an aborted conflict must not disclose a stale target row")

	current, err := brs.Get(ctx, first.ID)
	require.NoError(t, err)
	frozen, err := selections.Save(ctx, scope, dbgorm.CollectionSelection{
		Kind:              dbgorm.CollectionSelectionFrozenFilter,
		FilterFingerprint: "sha256:" + strings.Repeat("a", 64),
		Targets:           []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(first.ID, 10), ExpectedVersion: uint64(current.Version)}},
		ExpiresAt:         time.Now().UTC().Add(15 * time.Minute),
	})
	require.NoError(t, err)
	body = `{"request_id":"rules-operation-forbidden","action":"delete","selection":{"kind":"frozen_filter","selection_version":` + strconv.FormatInt(frozen.Version, 10) + `,"selection_token":"` + frozen.Token + `"}}`
	request = httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Engram-Request-ID", "rules-operation-forbidden")
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sessionID})
	request = request.WithContext(auth.WithIdentity(request.Context(), auth.SessionForBrowserUser("operator", 42)))
	recorder = httptest.NewRecorder()
	svc.handleCreateBehavioralRule(recorder, request)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), strconv.FormatInt(first.ID, 10), "denied selection use must not disclose target rows")
	remaining, err := brs.Get(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, current.Version, remaining.Version)

	currentSecond, err := brs.Get(ctx, second.ID)
	require.NoError(t, err)
	deletion, err := selections.Save(ctx, scope, dbgorm.CollectionSelection{
		Kind:    dbgorm.CollectionSelectionExplicit,
		Targets: []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(second.ID, 10), ExpectedVersion: uint64(currentSecond.Version)}},
	})
	require.NoError(t, err)
	body = `{"request_id":"rules-operation-delete","action":"delete","selection":{"kind":"explicit","selection_version":` + strconv.FormatInt(deletion.Version, 10) + `}}`
	request = httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Engram-Request-ID", "rules-operation-delete")
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sessionID})
	request = request.WithContext(auth.WithIdentity(request.Context(), identity))
	recorder = httptest.NewRecorder()
	svc.handleCreateBehavioralRule(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var deleted struct {
		OperationState string `json:"operation_state"`
		Readback       struct {
			Authoritative bool   `json:"authoritative"`
			Kind          string `json:"kind"`
		} `json:"readback"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &deleted))
	assert.Equal(t, "completed", deleted.OperationState)
	assert.True(t, deleted.Readback.Authoritative)

	assert.Equal(t, "authorized_absence", deleted.Readback.Kind)
	assert.NotContains(t, recorder.Body.String(), second.Content, "destructive readback must not disclose deleted row content")
	assert.NotContains(t, recorder.Body.String(), "current_state", "destructive readback must not serialize a deleted row")
}

func TestHandleCreateBehavioralRule_SelectionOperationSafetyBranches(t *testing.T) {
	project := "test-rules-handler-selection-safety-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const sessionID = "rules-selection-safety-session"
	svc, brs, selections, ruleStore := newRulesSelectionTestService(t, project)
	ctx := context.Background()
	identity := auth.SessionForBrowserUser("operator", 41)
	scope, err := (operatorCollectionScopeAuthority{}).ResolveOperatorCollectionScope(ctx, identity, sessionID, operatorCollectionSelectionDomain)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, ruleStore.DB.Exec(
			"DELETE FROM collection_selections WHERE subject_user_id = ? AND session_id = ? AND domain = ?",
			41, sessionID, operatorCollectionSelectionDomain,
		).Error)
	})

	first, err := brs.Create(ctx, &models.BehavioralRule{Project: &project, Content: "selection safety first", Priority: 30})
	require.NoError(t, err)
	second, err := brs.Create(ctx, &models.BehavioralRule{Project: &project, Content: "selection safety second", Priority: 20})
	require.NoError(t, err)
	third, err := brs.Create(ctx, &models.BehavioralRule{Project: &project, Content: "selection safety third", Priority: 10})
	require.NoError(t, err)

	call := func(requestID, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Engram-Request-ID", requestID)
		request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sessionID})
		request = request.WithContext(auth.WithIdentity(request.Context(), identity))
		recorder := httptest.NewRecorder()
		svc.handleCreateBehavioralRule(recorder, request)
		return recorder
	}

	t.Run("target and selection version changes reject without disclosure", func(t *testing.T) {
		currentFirst, getErr := brs.Get(ctx, first.ID)
		require.NoError(t, getErr)
		currentSecond, getErr := brs.Get(ctx, second.ID)
		require.NoError(t, getErr)
		selection, saveErr := selections.Save(ctx, scope, dbgorm.CollectionSelection{
			Kind: dbgorm.CollectionSelectionExplicit,
			Targets: []dbgorm.CollectionSelectionTarget{
				{ID: strconv.FormatInt(currentFirst.ID, 10), ExpectedVersion: uint64(currentFirst.Version)},
				{ID: strconv.FormatInt(currentSecond.ID, 10), ExpectedVersion: uint64(currentSecond.Version)},
			},
		})
		require.NoError(t, saveErr)

		_, setErr := brs.SetEnabled(ctx, second.ID, false, nil)
		require.NoError(t, setErr)
		conflict := call("rules-operation-target-version", `{"request_id":"rules-operation-target-version","action":"disable","selection":{"kind":"explicit","selection_version":`+strconv.FormatInt(selection.Version, 10)+`}}`)
		require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
		assert.NotContains(t, conflict.Body.String(), strconv.FormatInt(first.ID, 10), "a stale target conflict must not disclose selected rows")
		afterFirst, getErr := brs.Get(ctx, first.ID)
		require.NoError(t, getErr)
		assert.Equal(t, currentFirst.Enabled, afterFirst.Enabled, "a target version conflict must not partially mutate an earlier target")
		assert.Equal(t, currentFirst.Version, afterFirst.Version, "a target version conflict must not partially advance an earlier target")

		currentThird, getErr := brs.Get(ctx, third.ID)
		require.NoError(t, getErr)
		staleSelection, saveErr := selections.Save(ctx, scope, dbgorm.CollectionSelection{
			Kind:    dbgorm.CollectionSelectionExplicit,
			Targets: []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(currentThird.ID, 10), ExpectedVersion: uint64(currentThird.Version)}},
		})
		require.NoError(t, saveErr)
		_, saveErr = selections.Save(ctx, scope, dbgorm.CollectionSelection{
			Kind:    dbgorm.CollectionSelectionExplicit,
			Targets: []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(first.ID, 10), ExpectedVersion: uint64(afterFirst.Version)}},
		})
		require.NoError(t, saveErr)
		selectionVersionConflict := call("rules-operation-selection-version", `{"request_id":"rules-operation-selection-version","action":"disable","selection":{"kind":"explicit","selection_version":`+strconv.FormatInt(staleSelection.Version, 10)+`}}`)
		require.Equal(t, http.StatusConflict, selectionVersionConflict.Code, selectionVersionConflict.Body.String())
		assert.NotContains(t, selectionVersionConflict.Body.String(), strconv.FormatInt(third.ID, 10), "a replaced selection version must not disclose its prior target")
		afterThird, getErr := brs.Get(ctx, third.ID)
		require.NoError(t, getErr)
		assert.Equal(t, currentThird, afterThird, "a replaced selection version must not mutate its prior target")
	})

	t.Run("expired and permission-changed selections require reconfirmation", func(t *testing.T) {
		currentFirst, getErr := brs.Get(ctx, first.ID)
		require.NoError(t, getErr)
		expiring, saveErr := selections.Save(ctx, scope, dbgorm.CollectionSelection{
			Kind:              dbgorm.CollectionSelectionFrozenFilter,
			FilterFingerprint: "sha256:" + strings.Repeat("b", 64),
			Targets:           []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(first.ID, 10), ExpectedVersion: uint64(currentFirst.Version)}},
			ExpiresAt:         time.Now().UTC().Add(time.Minute),
		})
		require.NoError(t, saveErr)
		require.NoError(t, ruleStore.DB.Exec(
			"UPDATE collection_selections SET created_at = ?, frozen_expires_at = ?, reconfirmation_required = TRUE, reconfirmation_reason = ?, selection_version = selection_version + 1 WHERE subject_user_id = ? AND session_id = ? AND domain = ?",
			time.Now().UTC().Add(-2*time.Minute), time.Now().UTC().Add(-time.Minute), dbgorm.CollectionSelectionReconfirmExpired, scope.SubjectUserID, scope.SessionID, scope.Domain,
		).Error)
		expired := call("rules-operation-expired", `{"request_id":"rules-operation-expired","action":"delete","selection":{"kind":"frozen_filter","selection_version":`+strconv.FormatInt(expiring.Version, 10)+`,"selection_token":"`+expiring.Token+`"}}`)
		require.Equal(t, http.StatusPreconditionFailed, expired.Code, expired.Body.String())
		assert.NotContains(t, expired.Body.String(), currentFirst.Content, "an expired destructive selection must not disclose its target")
		afterExpired, getErr := brs.Get(ctx, first.ID)
		require.NoError(t, getErr)
		assert.Equal(t, currentFirst, afterExpired, "selection expiry must reject destructive mutation")
		reconfirmed, currentErr := selections.Current(ctx, scope)
		require.NoError(t, currentErr)
		assert.True(t, reconfirmed.ReconfirmationRequired)
		assert.Equal(t, dbgorm.CollectionSelectionReconfirmExpired, reconfirmed.ReconfirmationReason)
		assert.Greater(t, reconfirmed.Version, expiring.Version)

		permissionSelection, saveErr := selections.Save(ctx, scope, dbgorm.CollectionSelection{
			Kind:    dbgorm.CollectionSelectionExplicit,
			Targets: []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(first.ID, 10), ExpectedVersion: uint64(afterExpired.Version)}},
		})
		require.NoError(t, saveErr)
		require.NoError(t, ruleStore.DB.Exec(
			"UPDATE collection_selections SET authorization_epoch = authorization_epoch + 1 WHERE subject_user_id = ? AND session_id = ? AND domain = ?",
			scope.SubjectUserID, scope.SessionID, scope.Domain,
		).Error)
		permissionChanged := call("rules-operation-permission-changed", `{"request_id":"rules-operation-permission-changed","action":"disable","selection":{"kind":"explicit","selection_version":`+strconv.FormatInt(permissionSelection.Version, 10)+`}}`)
		require.Equal(t, http.StatusPreconditionFailed, permissionChanged.Code, permissionChanged.Body.String())
		assert.NotContains(t, permissionChanged.Body.String(), strconv.FormatInt(first.ID, 10), "a permission change must not disclose selected rows")
		reconfirmed, currentErr = selections.Current(ctx, scope)
		require.NoError(t, currentErr)
		assert.True(t, reconfirmed.ReconfirmationRequired)
		assert.Equal(t, dbgorm.CollectionSelectionReconfirmGrantChanged, reconfirmed.ReconfirmationReason)
	})

	t.Run("injected reorder conflict rolls back the complete scope", func(t *testing.T) {
		current := make([]*models.BehavioralRule, 0, 3)
		for _, rule := range []*models.BehavioralRule{first, second, third} {
			loaded, getErr := brs.Get(ctx, rule.ID)
			require.NoError(t, getErr)
			current = append(current, loaded)
		}
		selection, saveErr := selections.Save(ctx, scope, dbgorm.CollectionSelection{
			Kind: dbgorm.CollectionSelectionExplicit,
			Targets: []dbgorm.CollectionSelectionTarget{
				{ID: strconv.FormatInt(current[0].ID, 10), ExpectedVersion: uint64(current[0].Version)},
				{ID: strconv.FormatInt(current[1].ID, 10), ExpectedVersion: uint64(current[1].Version)},
				{ID: strconv.FormatInt(current[2].ID, 10), ExpectedVersion: uint64(current[2].Version)},
			},
		})
		require.NoError(t, saveErr)

		var before []dbgorm.BehavioralRule
		require.NoError(t, ruleStore.DB.Where("project = ?", project).Order("id ASC").Find(&before).Error)
		functionName := "rules_reorder_conflict_" + strconv.FormatInt(time.Now().UnixNano(), 10)
		triggerName := functionName + "_trigger"
		require.NoError(t, ruleStore.DB.Exec(
			"CREATE FUNCTION "+functionName+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id = "+strconv.FormatInt(current[0].ID, 10)+" AND pg_trigger_depth() = 1 THEN UPDATE behavioral_rules SET version = version + 1 WHERE id = "+strconv.FormatInt(current[1].ID, 10)+"; END IF; RETURN NEW; END; $$",
		).Error)
		require.NoError(t, ruleStore.DB.Exec("CREATE TRIGGER "+triggerName+" BEFORE UPDATE ON behavioral_rules FOR EACH ROW EXECUTE FUNCTION "+functionName+"()").Error)
		t.Cleanup(func() {
			require.NoError(t, ruleStore.DB.Exec("DROP TRIGGER IF EXISTS "+triggerName+" ON behavioral_rules").Error)
			require.NoError(t, ruleStore.DB.Exec("DROP FUNCTION IF EXISTS "+functionName+"()").Error)
		})

		reorder := call("rules-operation-reorder-conflict", `{"request_id":"rules-operation-reorder-conflict","action":"reorder","selection":{"kind":"explicit","selection_version":`+strconv.FormatInt(selection.Version, 10)+`},"scope":{"project":"`+project+`"},"order":[{"rule_id":`+strconv.FormatInt(current[0].ID, 10)+`,"expected_version":`+strconv.FormatUint(uint64(current[0].Version), 10)+`},{"rule_id":`+strconv.FormatInt(current[1].ID, 10)+`,"expected_version":`+strconv.FormatUint(uint64(current[1].Version), 10)+`},{"rule_id":`+strconv.FormatInt(current[2].ID, 10)+`,"expected_version":`+strconv.FormatUint(uint64(current[2].Version), 10)+`}]}`)
		require.Equal(t, http.StatusConflict, reorder.Code, reorder.Body.String())
		assert.NotContains(t, reorder.Body.String(), strconv.FormatInt(current[0].ID, 10), "a rejected reorder must not disclose scoped rows")
		var after []dbgorm.BehavioralRule
		require.NoError(t, ruleStore.DB.Where("project = ?", project).Order("id ASC").Find(&after).Error)
		assert.Equal(t, before, after, "an injected reorder conflict must leave the declared scope unchanged")
	})
}
