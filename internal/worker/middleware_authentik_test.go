package worker

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
)

func TestTokenAuth_AuthentikProvisioningRequiresInitialAdminSetup(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping Authentik provisioning integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	var existing int64
	require.NoError(t, store.DB.Model(&gormdb.User{}).Count(&existing).Error)
	require.Zero(t, existing, "Authentik provisioning regression requires an empty test database")

	prefix := fmt.Sprintf("zz-middleware-authentik-%d", time.Now().UnixNano())
	adminEmail := prefix + "-admin@example.com"
	operatorEmail := prefix + "-operator@example.com"
	t.Cleanup(func() {
		_ = store.DB.Exec(`DELETE FROM audit_log WHERE action = 'auth_setup_completed' AND actor = ?`, adminEmail).Error
		_ = store.DB.Exec(`DELETE FROM users WHERE email IN (?, ?)`, adminEmail, operatorEmail).Error
	})

	tokenAuth, err := NewTokenAuth("test-token")
	require.NoError(t, err)
	tokenAuth.SetAuthStores(gormdb.NewUserStore(store.DB), gormdb.NewAuthSessionStore(store.DB))
	tokenAuth.SetAuthentikConfig(true, true, []string{"192.0.2.1"})

	var role string
	handler := tokenAuth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role = getAuthRole(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
		req.RemoteAddr = "192.0.2.1:443"
		req.Header.Set("X-Authentik-Email", operatorEmail)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	require.Equal(t, http.StatusUnauthorized, request().Code)
	var stranded int64
	require.NoError(t, store.DB.Model(&gormdb.User{}).Count(&stranded).Error)
	require.Zero(t, stranded, "pre-setup rejection must not persist an operator")

	_, err = gormdb.NewUserStore(store.DB).CreateInitialAdmin(t.Context(), adminEmail, "hash", gormdb.NewDomainOwnerStore(store))
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, request().Code)
	require.Equal(t, gormdb.DashboardRoleOperator, role)

	var operatorCount, auditCount int64
	require.NoError(t, store.DB.Model(&gormdb.User{}).Where("email = ?", operatorEmail).Count(&operatorCount).Error)
	require.Equal(t, int64(1), operatorCount)
	require.NoError(t, store.DB.Model(&gormdb.AuditLogEntry{}).Where("action = ? AND actor = ?", "auth_setup_completed", adminEmail).Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestTokenAuth_AuthentikCodeExplorerUsesTrustedBrowserSession(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping Authentik Code Explorer integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	email := fmt.Sprintf("zz-authentik-code-%d@example.com", time.Now().UnixNano())
	user, err := gormdb.NewUserStore(store.DB).CreateUser(email, "", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DB.Delete(&gormdb.User{}, user.ID).Error })

	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.grants.current.SubjectUserID = user.ID
	fixture.binding.expectedUserID = user.ID
	fixture.binding.expectedSessionID = fmt.Sprintf("authentik/%d", user.ID)
	fixture.app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)

	tokenAuth, err := NewTokenAuth("test-token")
	require.NoError(t, err)
	tokenAuth.SetAuthStores(gormdb.NewUserStore(store.DB), gormdb.NewAuthSessionStore(store.DB))
	tokenAuth.SetAuthentikConfig(true, false, []string{"192.0.2.1"})
	handler := tokenAuth.Middleware(http.HandlerFunc(adapter.HandleSearch))
	body := `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture"}`

	trusted := httptest.NewRequest(http.MethodPost, "/api/code/search", bytes.NewBufferString(body))
	trusted.RemoteAddr = "192.0.2.1:443"
	trusted.Header.Set("X-Authentik-Email", email)
	trusted.Header.Set("X-Engram-Request-ID", "authentik-code-request")
	trusted.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "attacker-supplied-cookie"})
	trustedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(trustedRecorder, trusted)
	require.Equal(t, http.StatusOK, trustedRecorder.Code, trustedRecorder.Body.String())
	require.Equal(t, []string{fmt.Sprintf("authentik/%d", user.ID)}, fixture.app.sourceSessions)

	spoofed := httptest.NewRequest(http.MethodPost, "/api/code/search", bytes.NewBufferString(body))
	spoofed.RemoteAddr = "198.51.100.2:443"
	spoofed.Header.Set("X-Authentik-Email", email)
	spoofed.Header.Set("X-Engram-Request-ID", "spoofed-code-request")
	spoofedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(spoofedRecorder, spoofed)
	require.Equal(t, http.StatusUnauthorized, spoofedRecorder.Code, spoofedRecorder.Body.String())

	unauthenticated := httptest.NewRequest(http.MethodPost, "/api/code/search", bytes.NewBufferString(body))
	unauthenticated.Header.Set("X-Engram-Request-ID", "unauthenticated-code-request")
	unauthenticatedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedRecorder, unauthenticated)
	require.Equal(t, http.StatusUnauthorized, unauthenticatedRecorder.Code, unauthenticatedRecorder.Body.String())
	require.Equal(t, 1, fixture.app.searchCalls, "denied requests must not reach Code Explorer")
}
