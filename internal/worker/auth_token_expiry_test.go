package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authpkg "github.com/thebtf/engram/internal/auth"
)

func TestHandleCreateTokenIssuesExpiringLegacyDirectKeycard(t *testing.T) {
	store, tokenStore := openWorkerAuthTokenStore(t)
	service := &Service{tokenStore: tokenStore}
	name := fmt.Sprintf("zz-legacy-direct-expiry-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = store.DB.Exec(`DELETE FROM api_tokens WHERE name = ?`, name).Error })
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	body := bytes.NewReader([]byte(fmt.Sprintf(`{
		"name": %q,
		"scope": "read-write",
		"principal": %q,
		"principal_kind": "agent",
		"expires_at": %q
	}`, name, authpkg.LegacyDirectPrincipal("canonical-project"), expiresAt.Format(time.RFC3339Nano))))
	recorder := httptest.NewRecorder()
	service.handleCreateToken(recorder, sessionAdminRequest(http.MethodPost, "/api/auth/tokens", body))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response struct {
		ID        string     `json:"id"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotEmpty(t, response.ID)
	require.NotNil(t, response.ExpiresAt)
	require.WithinDuration(t, expiresAt, *response.ExpiresAt, time.Microsecond)
	stored, err := tokenStore.GetByID(context.Background(), response.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NotNil(t, stored.ExpiresAt)
	require.WithinDuration(t, expiresAt, *stored.ExpiresAt, time.Microsecond)
}

func TestHandleCreateTokenRejectsLegacyDirectKeycardWithoutExpiry(t *testing.T) {
	_, tokenStore := openWorkerAuthTokenStore(t)
	service := &Service{tokenStore: tokenStore}
	body := bytes.NewReader([]byte(`{
		"name": "zz-legacy-direct-no-expiry",
		"scope": "read-write",
		"principal": "agent/omp-legacy/canonical-project",
		"principal_kind": "agent"
	}`))
	recorder := httptest.NewRecorder()
	service.handleCreateToken(recorder, sessionAdminRequest(http.MethodPost, "/api/auth/tokens", body))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "expires_at")
}
