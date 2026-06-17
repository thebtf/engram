package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func newMemoryTestService(t *testing.T, project string) *Service {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)

	memoryStore := dbgorm.NewMemoryStore(store)
	service := &Service{memoryStore: memoryStore}

	t.Cleanup(func() {
		require.NoError(t, store.DB.WithContext(context.Background()).Exec("DELETE FROM memories WHERE project = ?", project).Error)
		require.NoError(t, store.Close())
	})

	return service
}

func TestHandleStoreMemoryExplicit_RoundTrip(t *testing.T) {
	project := "test-memory-handler-roundtrip-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	reqBody := storeMemoryRequest{
		Project:     project,
		Content:     "Observed that vault keys must be rotation-safe",
		Tags:        []string{"vault", "security"},
		SourceAgent: "integration-test",
	}

	body, err := json.Marshal(reqBody)
	require.NoError(t, err)

	storeReq := httptest.NewRequest(http.MethodPost, "/api/memories", bytes.NewReader(body))
	storeW := httptest.NewRecorder()
	service.handleStoreMemoryExplicit(storeW, storeReq)

	require.Equal(t, http.StatusCreated, storeW.Code)

	var created models.Memory
	require.NoError(t, json.Unmarshal(storeW.Body.Bytes(), &created))
	require.Equal(t, project, created.Project)
	require.Equal(t, reqBody.Content, created.Content)
	require.Equal(t, reqBody.Tags, created.Tags)

	listReq := httptest.NewRequest(http.MethodGet, "/api/memories?project="+project+"&limit=50", nil)
	listW := httptest.NewRecorder()
	service.handleListMemories(listW, listReq)

	require.Equal(t, http.StatusOK, listW.Code)

	var list []models.Memory
	require.NoError(t, json.Unmarshal(listW.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, created.Content, list[0].Content)
	assert.Equal(t, created.Project, list[0].Project)
}

func TestHandleStoreMemoryExplicit_ValidationErrors(t *testing.T) {
	project := "test-memory-handler-validation-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	tests := []struct {
		name    string
		request storeMemoryRequest
	}{
		{
			name: "empty project",
			request: storeMemoryRequest{
				Project: "",
				Content: "content",
			},
		},
		{
			name: "empty content",
			request: storeMemoryRequest{
				Project: project,
				Content: "",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.request)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/api/memories", bytes.NewReader(body))
			w := httptest.NewRecorder()
			service.handleStoreMemoryExplicit(w, req)

			require.Equal(t, http.StatusBadRequest, w.Code, "expected 400 for %s", tc.name)
		})
	}
}

func TestHandleDeleteMemoryByID_RoundTrip(t *testing.T) {
	project := "test-memory-handler-delete-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	storeReq := httptest.NewRequest(http.MethodPost, "/api/memories", bytes.NewReader([]byte(`{"project":"`+project+`","content":"delete-me"}`)))
	storeW := httptest.NewRecorder()
	service.handleStoreMemoryExplicit(storeW, storeReq)
	require.Equal(t, http.StatusCreated, storeW.Code)

	var created models.Memory
	require.NoError(t, json.Unmarshal(storeW.Body.Bytes(), &created))

	deleteReq := newCHIRequest(http.MethodDelete, "/api/memories/"+strconv.FormatInt(created.ID, 10), "id", strconv.FormatInt(created.ID, 10))
	deleteW := httptest.NewRecorder()
	service.handleDeleteMemoryByID(deleteW, deleteReq)

	require.Equal(t, http.StatusOK, deleteW.Code)

	var deleteResp map[string]any
	require.NoError(t, json.Unmarshal(deleteW.Body.Bytes(), &deleteResp))
	assert.Equal(t, "ok", deleteResp["status"])

	listReq := httptest.NewRequest(http.MethodGet, "/api/memories?project="+project, nil)
	listW := httptest.NewRecorder()
	service.handleListMemories(listW, listReq)
	require.Equal(t, http.StatusOK, listW.Code)

	var list []models.Memory
	require.NoError(t, json.Unmarshal(listW.Body.Bytes(), &list))
	require.Len(t, list, 0)
}

func TestHandleGetMemoryByID_RoundTrip(t *testing.T) {
	project := "test-memory-handler-get-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	storeReq := httptest.NewRequest(http.MethodPost, "/api/memories", bytes.NewReader([]byte(`{"project":"`+project+`","content":"fetch-me-by-id","tags":["a","b"]}`)))
	storeW := httptest.NewRecorder()
	service.handleStoreMemoryExplicit(storeW, storeReq)
	require.Equal(t, http.StatusCreated, storeW.Code)

	var created models.Memory
	require.NoError(t, json.Unmarshal(storeW.Body.Bytes(), &created))
	require.Greater(t, created.ID, int64(0))

	idStr := strconv.FormatInt(created.ID, 10)
	getReq := newCHIRequest(http.MethodGet, "/api/memories/"+idStr, "id", idStr)
	getW := httptest.NewRecorder()
	service.handleGetMemoryByID(getW, getReq)

	require.Equal(t, http.StatusOK, getW.Code)

	var got models.Memory
	require.NoError(t, json.Unmarshal(getW.Body.Bytes(), &got))
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "fetch-me-by-id", got.Content)
	assert.Equal(t, project, got.Project)
}

func TestHandleGetMemoryByID_NotFound(t *testing.T) {
	project := "test-memory-handler-get-not-found-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	idStr := "999999999"
	getReq := newCHIRequest(http.MethodGet, "/api/memories/"+idStr, "id", idStr)
	getW := httptest.NewRecorder()
	service.handleGetMemoryByID(getW, getReq)

	require.Equal(t, http.StatusNotFound, getW.Code)
}

func TestHandleGetMemoryByID_InvalidID(t *testing.T) {
	// nil store short-circuits to 503 before id parse; with a store, bad ids → 400.
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		svcNil := &Service{}
		req := newCHIRequest(http.MethodGet, "/api/memories/abc", "id", "abc")
		w := httptest.NewRecorder()
		svcNil.handleGetMemoryByID(w, req)
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		return
	}
	service := newMemoryTestService(t, "test-memory-handler-get-invalid-"+uuid.NewString())
	for _, badID := range []string{"abc", "0", "-1", "1.5", ""} {
		t.Run("id="+badID, func(t *testing.T) {
			req := newCHIRequest(http.MethodGet, "/api/memories/"+badID, "id", badID)
			w := httptest.NewRecorder()
			service.handleGetMemoryByID(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code, "expected 400 for id=%q", badID)
		})
	}
}

func TestHandleDeleteMemoryByID_NotFound(t *testing.T) {
	project := "test-memory-handler-delete-not-found-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	nonExistentID := int64(999999999)
	deleteReq := newCHIRequest(http.MethodDelete, "/api/memories/"+strconv.FormatInt(nonExistentID, 10), "id", strconv.FormatInt(nonExistentID, 10))
	deleteW := httptest.NewRecorder()
	service.handleDeleteMemoryByID(deleteW, deleteReq)

	require.Equal(t, http.StatusNotFound, deleteW.Code)
}
