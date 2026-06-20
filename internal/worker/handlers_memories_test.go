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

func TestHandleDeleteMemoryByID_NotFound(t *testing.T) {
	project := "test-memory-handler-delete-not-found-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	nonExistentID := int64(999999999)
	deleteReq := newCHIRequest(http.MethodDelete, "/api/memories/"+strconv.FormatInt(nonExistentID, 10), "id", strconv.FormatInt(nonExistentID, 10))
	deleteW := httptest.NewRecorder()
	service.handleDeleteMemoryByID(deleteW, deleteReq)

	require.Equal(t, http.StatusNotFound, deleteW.Code)
}
