package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
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

func newMemoryDomainWriteTestService(t *testing.T, project string) (*Service, *dbgorm.Store, func()) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)

	memoryStore := dbgorm.NewMemoryStore(store)
	domainOwnerStore := dbgorm.NewDomainOwnerStore(store)
	auditStore := dbgorm.NewAuditStore(store.GetDB())
	service := &Service{
		memoryStore:           memoryStore,
		domainOwnerStore:      domainOwnerStore,
		domainRegistryService: principalmemory.NewDomainRegistryService(domainOwnerStore, auditStore),
	}

	cleanup := func() {
		require.NoError(t, store.DB.WithContext(context.Background()).Exec("DELETE FROM memories WHERE project = ?", project).Error)
		require.NoError(t, store.DB.WithContext(context.Background()).Exec("DELETE FROM memory_domain_owners WHERE domain LIKE 'test-domain-%'").Error)
		require.NoError(t, store.DB.WithContext(context.Background()).Exec("DELETE FROM audit_log WHERE action IN (?, ?)", principalmemory.AuditActionDomainWriteWarn, principalmemory.AuditActionDomainWriteReject).Error)
		require.NoError(t, store.Close())
	}
	return service, store, cleanup
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

func TestHandleStoreMemoryExplicit_PrincipalOwnerDerivedFromIdentity(t *testing.T) {
	project := "test-memory-handler-principal-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	body := []byte(`{
		"project": "` + project + `",
		"content": "REST principal-owned memory",
		"owner_principal": "agent/spoofed",
		"agent_visibility": "private",
		"domain": "memory-lab"
	}`)
	id := auth.ClientWithPrincipal("read-write", "keycard-rest-principal", "agent/jeeves", auth.PrincipalKindAgent)
	storeReq := httptest.NewRequest(http.MethodPost, "/api/memories", bytes.NewReader(body)).
		WithContext(auth.WithIdentity(context.Background(), id))
	storeW := httptest.NewRecorder()
	service.handleStoreMemoryExplicit(storeW, storeReq)

	require.Equal(t, http.StatusCreated, storeW.Code, storeW.Body.String())

	var created models.Memory
	require.NoError(t, json.Unmarshal(storeW.Body.Bytes(), &created))
	require.Equal(t, "agent/jeeves", created.OwnerPrincipal)
	require.Equal(t, "agent", created.OwnerPrincipalKind)
	require.Equal(t, models.AgentVisibilityPrivate, created.AgentVisibility)
	require.Equal(t, "memory-lab", created.Domain)
	require.NotEqual(t, "agent/spoofed", created.OwnerPrincipal)
}

func TestApplyPrincipalMemoryMetadataREST_NonEmptyDomainRequiresPrincipal(t *testing.T) {
	mem := &models.Memory{}

	err := applyPrincipalMemoryMetadataREST(context.Background(), mem, "", "memory-lab")

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_domain")
	require.Empty(t, mem.Domain, "denied domain writes must not leave partial metadata on the memory")
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

func TestHandleStoreMemoryDomainRegistry_WarnRejectAndCompatibility(t *testing.T) {
	project := "test-memory-domain-registry-" + uuid.NewString()
	service, store, cleanup := newMemoryDomainWriteTestService(t, project)
	defer cleanup()
	domains := dbgorm.NewDomainOwnerStore(store)

	t.Run("missing row preserves current behavior", func(t *testing.T) {
		domain := "test-domain-missing-" + uuid.NewString()
		w := postMemoryWithDomain(t, service, project, domain, "agent/bob")
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	})

	t.Run("off allows cross owner", func(t *testing.T) {
		domain := "test-domain-off-" + uuid.NewString()
		_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
			Domain:             domain,
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Mode:               dbgorm.DomainOwnerModeOff,
		})
		require.NoError(t, err)
		w := postMemoryWithDomain(t, service, project, domain, "agent/bob")
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	})

	t.Run("same owner allows without warning", func(t *testing.T) {
		domain := "test-domain-same-" + uuid.NewString()
		_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
			Domain:             domain,
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Mode:               dbgorm.DomainOwnerModeWarn,
		})
		require.NoError(t, err)
		w := postMemoryWithDomain(t, service, project, domain, "agent/alice")
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.NotContains(t, body, "domain_warning")
	})

	t.Run("warn allows with structured warning", func(t *testing.T) {
		domain := "test-domain-warn-" + uuid.NewString()
		_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
			Domain:             domain,
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Mode:               dbgorm.DomainOwnerModeWarn,
		})
		require.NoError(t, err)
		w := postMemoryWithDomain(t, service, project, domain, "agent/bob")
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		warning, ok := body["domain_warning"].(map[string]any)
		require.True(t, ok, "warn mode response must include structured domain_warning")
		assert.Equal(t, principalmemory.DomainWriteWarningCrossOwner, warning["code"])
	})

	t.Run("reject denies before persistence", func(t *testing.T) {
		domain := "test-domain-reject-" + uuid.NewString()
		_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
			Domain:             domain,
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Mode:               dbgorm.DomainOwnerModeReject,
		})
		require.NoError(t, err)
		w := postMemoryWithDomain(t, service, project, domain, "agent/bob")
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		assert.Equal(t, int64(0), countMemoriesByDomain(t, store, project, domain))
	})
}

func TestHandleStoreMemoryDomainRegistry_AuditFailureBlocksBeforePersistence(t *testing.T) {
	project := "test-memory-domain-registry-audit-" + uuid.NewString()
	service, store, cleanup := newMemoryDomainWriteTestService(t, project)
	defer cleanup()
	service.domainRegistryService = &fakeRESTDomainRegistryService{err: errors.New("domain write audit: unavailable")}

	domain := "test-domain-audit-" + uuid.NewString()
	w := postMemoryWithDomain(t, service, project, domain, "agent/bob")
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.Equal(t, int64(0), countMemoriesByDomain(t, store, project, domain))
}

func postMemoryWithDomain(t *testing.T, service *Service, project, domain, principal string) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewReader([]byte(`{"project":"` + project + `","content":"domain governed memory","domain":"` + domain + `"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/memories", body).
		WithContext(auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-domain-test", principal, auth.PrincipalKindAgent)))
	w := httptest.NewRecorder()
	service.handleStoreMemoryExplicit(w, req)
	return w
}

func countMemoriesByDomain(t *testing.T, store *dbgorm.Store, project, domain string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, store.DB.Model(&dbgorm.Memory{}).Where("project = ? AND domain = ? AND deleted_at IS NULL", project, domain).Count(&count).Error)
	return count
}

type fakeRESTDomainRegistryService struct {
	decision *principalmemory.DomainWriteDecision
	err      error
	calls    int
}

func (f *fakeRESTDomainRegistryService) CheckWrite(ctx context.Context, req principalmemory.DomainWriteCheckRequest) (*principalmemory.DomainWriteDecision, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.decision != nil {
		return f.decision, nil
	}
	return &principalmemory.DomainWriteDecision{Allowed: true}, nil
}

func TestHandleListMemories_EmptyProjectReturnsJSONArray(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	service := &Service{memoryStoreSeam: &fakeMemoryListStore{rows: []*models.Memory{}}}
	req := httptest.NewRequest(http.MethodGet, "/api/memories?project=empty-project&limit=50", nil)
	w := httptest.NewRecorder()

	service.handleListMemories(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.NotEmpty(t, w.Body.String())
	assert.JSONEq(t, "[]", w.Body.String())
}

func TestHandleListMemories_NonFiniteScoresReturnJSON(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	service := &Service{memoryStoreSeam: &fakeMemoryListStore{rows: []*models.Memory{{
		ID:         42,
		Project:    "score-project",
		Content:    "memory with non-finite score",
		Tags:       []string{"score"},
		Confidence: math.NaN(),
		Stability:  math.Inf(1),
	}}}}
	req := httptest.NewRequest(http.MethodGet, "/api/memories?project=score-project&limit=50", nil)
	w := httptest.NewRecorder()

	service.handleListMemories(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, float64(42), rows[0]["id"])
	assert.Equal(t, "memory with non-finite score", rows[0]["content"])
	assert.NotContains(t, rows[0], "confidence")
	assert.NotContains(t, rows[0], "stability")
}

func TestHandleListMemories_PrincipalPrivateCrossPrincipalInvisible_FlagOff(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	privateOther := &models.Memory{
		ID:                 50,
		Project:            "principal-list-project",
		Content:            "private bob row",
		AgentVisibility:    models.AgentVisibilityPrivate,
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
	}
	visible := &models.Memory{
		ID:      51,
		Project: "principal-list-project",
		Content: "visible legacy row",
	}
	service := &Service{memoryStoreSeam: &fakeMemoryListStore{rows: []*models.Memory{privateOther, visible}}}
	id := auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent)
	req := httptest.NewRequest(http.MethodGet, "/api/memories?project=principal-list-project&limit=50", nil).
		WithContext(auth.WithIdentity(context.Background(), id))
	w := httptest.NewRecorder()

	service.handleListMemories(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, float64(51), rows[0]["id"])
	assert.Equal(t, "visible legacy row", rows[0]["content"])
}

func TestHandleListMemories_DomainOwnedCrossPrincipalInvisible_FlagOff(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	hiddenOther := &models.Memory{
		ID:                 60,
		Project:            "domain-list-project",
		Content:            "hidden bob domain row",
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	}
	visibleOwn := &models.Memory{
		ID:                 61,
		Project:            "domain-list-project",
		Content:            "visible alice domain row",
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	}
	service := &Service{memoryStoreSeam: &fakeMemoryListStore{rows: []*models.Memory{hiddenOther, visibleOwn}}}
	id := auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent)
	req := httptest.NewRequest(http.MethodGet, "/api/memories?project=domain-list-project&limit=50", nil).
		WithContext(auth.WithIdentity(context.Background(), id))
	w := httptest.NewRecorder()

	service.handleListMemories(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, float64(61), rows[0]["id"])
	assert.Equal(t, "visible alice domain row", rows[0]["content"])
}

func TestMemoryDomainManageAllowedREST_DomainOwnedCrossPrincipalDenied(t *testing.T) {
	mem := &models.Memory{
		ID:                 62,
		Project:            "domain-delete-project",
		Content:            "bob domain row",
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	}
	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))

	require.False(t, memoryDomainManageAllowedREST(ctx, mem))
}

func TestHandleListMemories_OutOfRangeTimestampsReturnJSON(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	outOfRange := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	service := &Service{memoryStoreSeam: &fakeMemoryListStore{rows: []*models.Memory{{
		ID:              43,
		Project:         "time-project",
		Content:         "memory with out-of-range timestamp",
		Tags:            []string{"time"},
		CreatedAt:       outOfRange,
		UpdatedAt:       outOfRange,
		LastRetrievedAt: &outOfRange,
		LastConfirmed:   &outOfRange,
		ReviewAfter:     &outOfRange,
		ValidFrom:       &outOfRange,
		ValidUntil:      &outOfRange,
	}}}}
	req := httptest.NewRequest(http.MethodGet, "/api/memories?project=time-project&limit=50", nil)
	w := httptest.NewRecorder()

	service.handleListMemories(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, float64(43), rows[0]["id"])
	assert.Equal(t, "memory with out-of-range timestamp", rows[0]["content"])
	assert.NotContains(t, rows[0], "last_retrieved_at")
	assert.NotContains(t, rows[0], "last_confirmed")
	assert.NotContains(t, rows[0], "review_after")
	assert.NotContains(t, rows[0], "valid_from")
	assert.NotContains(t, rows[0], "valid_until")
	assert.NotEqual(t, "+10000-01-01T00:00:00Z", rows[0]["created_at"])
	assert.NotEqual(t, "+10000-01-01T00:00:00Z", rows[0]["updated_at"])
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

func TestHandleSuppressMemoryByID_RoundTrip(t *testing.T) {
	project := "test-memory-handler-suppress-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	storeReq := httptest.NewRequest(http.MethodPost, "/api/memories", bytes.NewReader([]byte(`{"project":"`+project+`","content":"suppress-me"}`)))
	storeW := httptest.NewRecorder()
	service.handleStoreMemoryExplicit(storeW, storeReq)
	require.Equal(t, http.StatusCreated, storeW.Code)

	var created models.Memory
	require.NoError(t, json.Unmarshal(storeW.Body.Bytes(), &created))

	body := bytes.NewReader([]byte(`{"reason":"operator marked as noise"}`))
	suppressReq := newCHIRequest(http.MethodPost, "/api/memories/"+strconv.FormatInt(created.ID, 10)+"/suppress", "id", strconv.FormatInt(created.ID, 10))
	suppressReq.Body = io.NopCloser(body)
	suppressW := httptest.NewRecorder()
	service.handleSuppressMemoryByID(suppressW, suppressReq)

	require.Equal(t, http.StatusOK, suppressW.Code, suppressW.Body.String())

	var suppressResp map[string]any
	require.NoError(t, json.Unmarshal(suppressW.Body.Bytes(), &suppressResp))
	assert.Equal(t, "ok", suppressResp["status"])
	assert.Equal(t, "suppress", suppressResp["action"])
	assert.Equal(t, float64(created.ID), suppressResp["id"])
	assert.Equal(t, "operator marked as noise", suppressResp["reason"])

	listReq := httptest.NewRequest(http.MethodGet, "/api/memories?project="+project, nil)
	listW := httptest.NewRecorder()
	service.handleListMemories(listW, listReq)
	require.Equal(t, http.StatusOK, listW.Code)

	var list []models.Memory
	require.NoError(t, json.Unmarshal(listW.Body.Bytes(), &list))
	require.Len(t, list, 0)
}

func TestHandleSuppressMemoryByID_NotFound(t *testing.T) {
	project := "test-memory-handler-suppress-not-found-" + uuid.NewString()
	service := newMemoryTestService(t, project)

	nonExistentID := int64(999999999)
	suppressReq := newCHIRequest(http.MethodPost, "/api/memories/"+strconv.FormatInt(nonExistentID, 10)+"/suppress", "id", strconv.FormatInt(nonExistentID, 10))
	suppressW := httptest.NewRecorder()
	service.handleSuppressMemoryByID(suppressW, suppressReq)

	require.Equal(t, http.StatusNotFound, suppressW.Code)
}
