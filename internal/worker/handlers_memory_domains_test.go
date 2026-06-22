package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

func TestMemoryDomainsHandlers_AdminListAndPut(t *testing.T) {
	service, cleanup := newMemoryDomainTestService(t)
	defer cleanup()

	router := memoryDomainTestRouter(service)
	body := bytes.NewReader([]byte(`{
		"owner_principal": "agent/alice",
		"owner_principal_kind": "agent",
		"mode": "warn"
	}`))
	putReq := httptest.NewRequest(http.MethodPut, "/api/memory-domains/memory-lab", body).
		WithContext(auth.WithIdentity(context.Background(), auth.Admin()))
	putW := httptest.NewRecorder()
	router.ServeHTTP(putW, putReq)
	require.Equal(t, http.StatusOK, putW.Code, putW.Body.String())

	var putResp memoryDomainResponse
	require.NoError(t, json.Unmarshal(putW.Body.Bytes(), &putResp))
	assert.Equal(t, "memory-lab", putResp.Domain)
	assert.Equal(t, "agent/alice", putResp.OwnerPrincipal)
	assert.Equal(t, "agent", putResp.OwnerPrincipalKind)
	assert.Equal(t, "warn", putResp.Mode)

	listReq := httptest.NewRequest(http.MethodGet, "/api/memory-domains?owner_principal=agent/alice&mode=warn", nil).
		WithContext(auth.WithIdentity(context.Background(), auth.Admin()))
	listW := httptest.NewRecorder()
	router.ServeHTTP(listW, listReq)
	require.Equal(t, http.StatusOK, listW.Code, listW.Body.String())

	var listResp memoryDomainsListResponse
	require.NoError(t, json.Unmarshal(listW.Body.Bytes(), &listResp))
	require.NotEmpty(t, listResp.Domains)
	assert.Equal(t, "memory-lab", listResp.Domains[0].Domain)
}

func TestMemoryDomainsHandlers_AdminRequired(t *testing.T) {
	service, cleanup := newMemoryDomainTestService(t)
	defer cleanup()

	router := memoryDomainTestRouter(service)
	body := bytes.NewReader([]byte(`{"owner_principal":"agent/alice","owner_principal_kind":"agent","mode":"warn"}`))

	noIdentityReq := httptest.NewRequest(http.MethodPut, "/api/memory-domains/memory-lab", body)
	noIdentityW := httptest.NewRecorder()
	router.ServeHTTP(noIdentityW, noIdentityReq)
	require.Equal(t, http.StatusUnauthorized, noIdentityW.Code)

	clientBody := bytes.NewReader([]byte(`{"owner_principal":"agent/alice","owner_principal_kind":"agent","mode":"warn"}`))
	clientReq := httptest.NewRequest(http.MethodPut, "/api/memory-domains/memory-lab", clientBody).
		WithContext(auth.WithIdentity(context.Background(), auth.Client("read-write", "client-keycard")))
	clientW := httptest.NewRecorder()
	router.ServeHTTP(clientW, clientReq)
	require.Equal(t, http.StatusForbidden, clientW.Code)
}

func TestMemoryDomainsHandlers_Validation(t *testing.T) {
	service, cleanup := newMemoryDomainTestService(t)
	defer cleanup()

	router := memoryDomainTestRouter(service)
	admin := auth.WithIdentity(context.Background(), auth.Admin())
	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "empty domain",
			path: "/api/memory-domains/%20",
			body: `{"owner_principal":"agent/alice","owner_principal_kind":"agent","mode":"warn"}`,
		},
		{
			name: "empty owner",
			path: "/api/memory-domains/memory-lab",
			body: `{"owner_principal":"","owner_principal_kind":"agent","mode":"warn"}`,
		},
		{
			name: "bad kind",
			path: "/api/memory-domains/memory-lab",
			body: `{"owner_principal":"agent/alice","owner_principal_kind":"daemon","mode":"warn"}`,
		},
		{
			name: "bad mode",
			path: "/api/memory-domains/memory-lab",
			body: `{"owner_principal":"agent/alice","owner_principal_kind":"agent","mode":"observe"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, tc.path, bytes.NewReader([]byte(tc.body))).
				WithContext(admin)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
}

func memoryDomainTestRouter(service *Service) http.Handler {
	router := chi.NewRouter()
	router.Get("/api/memory-domains", service.handleListMemoryDomains)
	router.Put("/api/memory-domains/{domain}", service.handleUpsertMemoryDomain)
	return router
}

func newMemoryDomainTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)

	service := &Service{domainOwnerStore: dbgorm.NewDomainOwnerStore(store)}
	cleanup := func() {
		require.NoError(t, store.DB.WithContext(context.Background()).Exec("DELETE FROM memory_domain_owners WHERE domain = 'memory-lab'").Error)
		require.NoError(t, store.Close())
	}
	return service, cleanup
}
