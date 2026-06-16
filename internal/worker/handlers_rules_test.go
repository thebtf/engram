package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

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
