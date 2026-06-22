package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	authpkg "github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func openWorkerAuthTokenStore(t *testing.T) (*gormdb.Store, *gormdb.TokenStore) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping auth token handler integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store, gormdb.NewTokenStore(store)
}

func sessionAdminRequest(method, path string, body *bytes.Reader) *http.Request {
	ctx := authpkg.WithIdentity(context.Background(), authpkg.Session("admin"))
	return httptest.NewRequest(method, path, body).WithContext(ctx)
}

func TestAuthTokenHandlers_PrincipalMetadataRoundTrip(t *testing.T) {
	store, tokenStore := openWorkerAuthTokenStore(t)
	svc := &Service{tokenStore: tokenStore}

	name := fmt.Sprintf("zz-test-handler-principal-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = store.DB.Exec(`DELETE FROM api_tokens WHERE name = ?`, name).Error
	})

	createBody := bytes.NewReader([]byte(fmt.Sprintf(`{
		"name": %q,
		"scope": "read-write",
		"principal": "agent/codex",
		"principal_kind": "agent"
	}`, name)))
	createReq := sessionAdminRequest(http.MethodPost, "/api/auth/tokens", createBody)
	createRec := httptest.NewRecorder()
	svc.handleCreateToken(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	var createResp map[string]any
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &createResp))
	require.NotEmpty(t, createResp["token"])
	require.NotContains(t, createResp, "token_hash")
	require.Equal(t, name, createResp["name"])
	require.Equal(t, "read-write", createResp["scope"])
	require.Equal(t, "agent/codex", createResp["principal"])
	require.Equal(t, "agent", createResp["principal_kind"])
	id, ok := createResp["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, id)

	listReq := sessionAdminRequest(http.MethodGet, "/api/auth/tokens", bytes.NewReader(nil))
	listRec := httptest.NewRecorder()
	svc.handleListTokens(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())

	var listResp struct {
		Tokens []map[string]any `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listResp))
	var listed map[string]any
	for _, token := range listResp.Tokens {
		if token["id"] == id {
			listed = token
			break
		}
	}
	require.NotNil(t, listed, "created token must appear in list response")
	require.NotContains(t, listed, "token")
	require.NotContains(t, listed, "token_hash")
	require.Equal(t, "agent/codex", listed["principal"])
	require.Equal(t, "agent", listed["principal_kind"])

	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	statsCtx := context.WithValue(authpkg.WithIdentity(context.Background(), authpkg.Session("admin")), chi.RouteCtxKey, routeCtx)
	statsReq := httptest.NewRequest(http.MethodGet, "/api/auth/tokens/"+id+"/stats", nil).WithContext(statsCtx)
	statsRec := httptest.NewRecorder()
	svc.handleGetTokenStats(statsRec, statsReq)
	require.Equal(t, http.StatusOK, statsRec.Code, statsRec.Body.String())

	var statsResp map[string]any
	require.NoError(t, json.Unmarshal(statsRec.Body.Bytes(), &statsResp))
	require.Equal(t, id, statsResp["id"])
	require.Equal(t, name, statsResp["name"])
	require.Equal(t, "agent/codex", statsResp["principal"])
	require.Equal(t, "agent", statsResp["principal_kind"])
	require.NotContains(t, statsResp, "token")
	require.NotContains(t, statsResp, "token_hash")
}

func TestHandleCreateTokenRejectsPrincipalKindWithoutPrincipal(t *testing.T) {
	_, tokenStore := openWorkerAuthTokenStore(t)
	svc := &Service{tokenStore: tokenStore}

	body := bytes.NewReader([]byte(`{
		"name": "zz-test-invalid-principal-kind",
		"scope": "read-write",
		"principal_kind": "agent"
	}`))
	req := sessionAdminRequest(http.MethodPost, "/api/auth/tokens", body)
	rec := httptest.NewRecorder()
	svc.handleCreateToken(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.True(t, strings.Contains(rec.Body.String(), "principal is required"), rec.Body.String())
}
