package worker

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
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
