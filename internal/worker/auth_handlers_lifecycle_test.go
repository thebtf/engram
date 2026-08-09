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
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	authpkg "github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

type authLifecycleEnv struct {
	store       *gormdb.Store
	users       *gormdb.UserStore
	invitations *gormdb.InvitationStore
	sessions    *gormdb.AuthSessionStore
	access      *gormdb.DomainOwnerStore
	handlers    *AuthHandlers
}

func openAuthLifecycleEnv(t *testing.T) *authLifecycleEnv {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping auth lifecycle integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	env := &authLifecycleEnv{
		store:       store,
		users:       gormdb.NewUserStore(store.DB),
		invitations: gormdb.NewInvitationStore(store.DB),
		sessions:    gormdb.NewAuthSessionStore(store.DB),
		access:      gormdb.NewDomainOwnerStore(store),
	}
	env.handlers = NewAuthHandlers(env.users, env.invitations, env.sessions, env.access)
	t.Cleanup(func() { _ = store.Close() })
	return env
}

func requestWithRoute(method, path string, body []byte, id authpkg.Identity, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	ctx := buildAuthCtx(req.Context(), id)
	if len(params) > 0 {
		rctx := chi.NewRouteContext()
		for key, value := range params {
			rctx.URLParams.Add(key, value)
		}
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

func TestAuthHandlersLifecycle_InvitationSingleUseRace(t *testing.T) {
	env := openAuthLifecycleEnv(t)

	adminEmail := fmt.Sprintf("zz-access-race-admin-%d@example.com", time.Now().UnixNano())
	invitePrefix := fmt.Sprintf("zz-access-race-%d", time.Now().UnixNano())
	userPrefix := fmt.Sprintf("zz-access-race-user-%d", time.Now().UnixNano())
	admin, err := env.users.CreateUser(adminEmail, "hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = env.store.DB.Exec(`DELETE FROM invitations WHERE code LIKE ?`, invitePrefix+"%").Error
		_ = env.store.DB.Exec(`DELETE FROM users WHERE email LIKE ?`, userPrefix+"%@example.com").Error
		_ = env.store.DB.Exec(`DELETE FROM users WHERE email = ?`, adminEmail).Error
		_ = env.store.DB.Exec(`DELETE FROM audit_log WHERE action LIKE 'auth_%' AND actor LIKE ?`, userPrefix+"%@example.com").Error
	})

	code := invitePrefix + "-code"
	_, err = env.invitations.CreateInvitation(code, admin.ID, "", gormdb.DashboardRoleOperator, time.Now().UTC().Add(2*time.Hour))
	require.NoError(t, err)

	payloads := [][]byte{
		[]byte(fmt.Sprintf(`{"email":"%s-a@example.com","password":"password-123","invitation":"%s"}`, userPrefix, code)),
		[]byte(fmt.Sprintf(`{"email":"%s-b@example.com","password":"password-123","invitation":"%s"}`, userPrefix, code)),
	}
	statuses := make([]int, len(payloads))
	bodies := make([]string, len(payloads))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range payloads {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(payloads[idx]))
			rec := httptest.NewRecorder()
			env.handlers.handleRegister(rec, req)
			statuses[idx] = rec.Code
			bodies[idx] = rec.Body.String()
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	for i, status := range statuses {
		if status == http.StatusCreated {
			successes++
			continue
		}
		if status != http.StatusConflict && status != http.StatusForbidden {
			t.Fatalf("register attempt %d status=%d body=%s", i, status, bodies[i])
		}
	}
	require.Equal(t, 1, successes, "exactly one concurrent registration must succeed")

	rows, err := env.users.ListUsers()
	require.NoError(t, err)
	created := 0
	for _, row := range rows {
		if strings.HasPrefix(row.Email, userPrefix+"-") {
			created++
		}
	}
	require.Equal(t, 1, created, "only one invited user row must be committed")

	invitations, err := env.invitations.ListInvitations()
	require.NoError(t, err)
	var matched *gormdb.Invitation
	for _, inv := range invitations {
		if inv.Code == code {
			matched = inv
			break
		}
	}
	require.NotNil(t, matched)
	require.NotNil(t, matched.UsedBy)
	require.NotNil(t, matched.UsedAt)
}

func TestAuthHandlersLifecycle_SetupCreatesOnlyOneInitialAdmin(t *testing.T) {
	env := openAuthLifecycleEnv(t)
	count, err := env.users.CountUsers()
	require.NoError(t, err)
	require.Zero(t, count, "initial-admin setup requires an empty test database")

	prefix := fmt.Sprintf("zz-initial-admin-race-%d", time.Now().UnixNano())
	emails := []string{prefix + "-a@example.com", prefix + "-b@example.com"}
	t.Cleanup(func() {
		_ = env.store.DB.Exec(`DELETE FROM audit_log WHERE action = 'auth_setup_completed' AND actor LIKE ?`, prefix+"%@example.com").Error
		_ = env.store.DB.Exec(`DELETE FROM users WHERE email LIKE ?`, prefix+"%@example.com").Error
	})

	ready := make(chan struct{}, len(emails))
	release := make(chan struct{})
	env.handlers.beforeInitialAdminCreate = func() {
		ready <- struct{}{}
		<-release
	}

	statuses := make([]int, len(emails))
	bodies := make([]string, len(emails))
	var wg sync.WaitGroup
	for i, email := range emails {
		wg.Add(1)
		go func(idx int, email string) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":"password-123"}`, email)))
			rec := httptest.NewRecorder()
			env.handlers.handleSetup(rec, req)
			statuses[idx] = rec.Code
			bodies[idx] = rec.Body.String()
		}(i, email)
	}
	for range emails {
		<-ready
	}
	close(release)
	wg.Wait()

	successes := 0
	for i, status := range statuses {
		switch status {
		case http.StatusCreated:
			successes++
			require.Contains(t, bodies[i], `"user"`)
			require.NotContains(t, bodies[i], "password")
		case http.StatusConflict:
			require.Contains(t, bodies[i], "setup already completed")
		default:
			t.Fatalf("setup attempt %d status=%d body=%s", i, status, bodies[i])
		}
	}
	require.Equal(t, 1, successes, "exactly one concurrent setup response must contain credentials")

	rows, err := env.users.ListUsers()
	require.NoError(t, err)
	require.Len(t, rows, 1, "only one initial admin user may be committed")
	require.Contains(t, emails, rows[0].Email)
	require.Equal(t, gormdb.DashboardRoleAdmin, rows[0].Role)
	var auditEvents int64
	require.NoError(t, env.store.DB.Model(&gormdb.AccessAuditRecord{}).Where("action = ? AND actor LIKE ?", "auth_setup_completed", prefix+"%@example.com").Count(&auditEvents).Error)
	require.Equal(t, int64(1), auditEvents, "only the successful setup may issue an audit credential event")
}

func TestAuthHandlersLifecycle_SessionRevokeWinsInFlightRequest(t *testing.T) {
	env := openAuthLifecycleEnv(t)

	adminEmail := fmt.Sprintf("zz-access-session-admin-%d@example.com", time.Now().UnixNano())
	admin, err := env.users.CreateUser(adminEmail, "hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	sess, err := env.sessions.CreateSession(admin.ID, 2*time.Hour, "test-agent", "127.0.0.1")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = env.store.DB.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID).Error
		_ = env.store.DB.Exec(`DELETE FROM users WHERE email = ?`, adminEmail).Error
		_ = env.store.DB.Exec(`DELETE FROM audit_log WHERE action LIKE 'auth_%' AND actor = ?`, adminEmail).Error
	})

	started := make(chan struct{})
	release := make(chan struct{})
	env.handlers.beforeAccessSessionCheck = func() {
		close(started)
		<-release
	}
	defer func() { env.handlers.beforeAccessSessionCheck = nil }()

	req := requestWithRoute(http.MethodGet, "/api/access/sessions", nil, authpkg.Session("admin"), nil)
	req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handlers.handleAccessListSessions(rec, req)
	}()

	<-started
	changed, err := env.sessions.RevokeSession(sess.ID, &admin.ID, "race revoke")
	require.NoError(t, err)
	require.True(t, changed)
	close(release)
	<-done

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "session revoked")
}

func TestAuthHandlersLifecycle_AccessCreateInvitationAcceptsAuthentikAdminWithoutDashboardCookie(t *testing.T) {
	env := openAuthLifecycleEnv(t)
	adminEmail := fmt.Sprintf("zz-access-authentik-admin-%d@example.com", time.Now().UnixNano())
	inviteEmail := fmt.Sprintf("zz-access-authentik-invite-%d@example.com", time.Now().UnixNano())
	admin, err := env.users.CreateUser(adminEmail, "hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = env.store.DB.Exec(`DELETE FROM invitations WHERE email = ?`, inviteEmail).Error
		_ = env.store.DB.Exec(`DELETE FROM users WHERE email = ?`, adminEmail).Error
		_ = env.store.DB.Exec(`DELETE FROM audit_log WHERE action LIKE 'auth_%' AND actor = ?`, adminEmail).Error
	})
	req := requestWithRoute(http.MethodPost, "/api/access/invitations", []byte(fmt.Sprintf(`{"email":%q,"role":"operator"}`, inviteEmail)), authpkg.Session("admin"), nil)
	req.Header.Set("X-Authentik-Email", adminEmail)
	rec := httptest.NewRecorder()
	env.handlers.handleAccessCreateInvitation(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var payload struct {
		Invitation gormdb.AccessInvitationView `json:"invitation"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, admin.ID, payload.Invitation.CreatedBy)
	require.Equal(t, adminEmail, payload.Invitation.CreatedByEmail)
	require.Equal(t, inviteEmail, payload.Invitation.Email)
	require.NotEmpty(t, payload.Invitation.Code, "create is the only bounded code reveal")
}

func TestAccessInvitationReadShapesOmitCodes(t *testing.T) {
	readBody, err := json.Marshal(gormdb.AccessInvitationView{ID: 1, Email: "operator@example.com"})
	require.NoError(t, err)
	require.NotContains(t, string(readBody), `"code"`)

	createdBody, err := json.Marshal(gormdb.AccessInvitationView{ID: 1, Code: "one-time-secret"})
	require.NoError(t, err)
	require.Contains(t, string(createdBody), `"code":"one-time-secret"`)
}

func TestAuthHandlersLifecycle_LastAdminDemoteRaceLeavesOneAdmin(t *testing.T) {
	env := openAuthLifecycleEnv(t)
	adminAEmail := fmt.Sprintf("zz-access-last-admin-a-%d@example.com", time.Now().UnixNano())
	adminBEmail := fmt.Sprintf("zz-access-last-admin-b-%d@example.com", time.Now().UnixNano())
	adminA, err := env.users.CreateUser(adminAEmail, "hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	adminB, err := env.users.CreateUser(adminBEmail, "hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = env.store.DB.Exec(`DELETE FROM users WHERE email IN (?, ?)`, adminAEmail, adminBEmail).Error
	})

	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := env.handlers.applyUserUpdate(adminA.ID, updateUserRequest{Role: strPtr(gormdb.DashboardRoleOperator)}, nil, "", false)
		results <- err
	}()
	go func() {
		<-start
		_, err := env.handlers.applyUserUpdate(adminB.ID, updateUserRequest{Role: strPtr(gormdb.DashboardRoleOperator)}, nil, "", false)
		results <- err
	}()
	close(start)
	err1 := <-results
	err2 := <-results
	if err1 == nil && err2 == nil {
		t.Fatalf("expected one demotion to fail so at least one admin remains")
	}
	if err1 != nil {
		require.NotContains(t, strings.ToLower(err1.Error()), "deadlock")
	}
	if err2 != nil {
		require.NotContains(t, strings.ToLower(err2.Error()), "deadlock")
	}
	count, err := env.users.CountAdmins()
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}

func TestAuthHandlersLifecycle_DisabledAdminCanBeDemotedWithoutLastAdminError(t *testing.T) {
	env := openAuthLifecycleEnv(t)
	activeEmail := fmt.Sprintf("zz-active-admin-%d@example.com", time.Now().UnixNano())
	disabledEmail := fmt.Sprintf("zz-disabled-admin-%d@example.com", time.Now().UnixNano())
	active, err := env.users.CreateUser(activeEmail, "hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	disabledAdmin, err := env.users.CreateUser(disabledEmail, "hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = env.store.DB.Exec(`DELETE FROM users WHERE email IN (?, ?)`, activeEmail, disabledEmail).Error
	})
	_, err = env.handlers.applyUserUpdate(disabledAdmin.ID, updateUserRequest{Disabled: ptrBool(true)}, nil, "", false)
	require.NoError(t, err)
	updated, err := env.handlers.applyUserUpdate(disabledAdmin.ID, updateUserRequest{Role: strPtr(gormdb.DashboardRoleOperator)}, nil, "", false)
	require.NoError(t, err)
	require.Equal(t, gormdb.DashboardRoleOperator, updated.Role)
	require.True(t, updated.Disabled)
	count, err := env.users.CountAdmins()
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.Equal(t, active.ID, active.ID)
}

func ptrBool(v bool) *bool { return &v }

func strPtr(v string) *string { return &v }

func TestAuthHandlersLifecycle_AccessRoutesRejectNonSessionAdmin(t *testing.T) {
	h := NewAuthHandlers(nil, nil, nil, nil)
	tests := []struct {
		name   string
		req    *http.Request
		handle http.HandlerFunc
	}{
		{name: "providers", req: requestWithRoute(http.MethodGet, "/api/access/providers", nil, authpkg.Admin(), nil), handle: h.handleAccessProviders},
		{name: "list invitations", req: requestWithRoute(http.MethodGet, "/api/access/invitations", nil, authpkg.Admin(), nil), handle: h.handleAccessListInvitations},
		{name: "create invitation", req: requestWithRoute(http.MethodPost, "/api/access/invitations", []byte(`{}`), authpkg.Admin(), nil), handle: h.handleAccessCreateInvitation},
		{name: "revoke invitation", req: requestWithRoute(http.MethodPost, "/api/access/invitations/1/revoke", []byte(`{}`), authpkg.Admin(), map[string]string{"id": "1"}), handle: h.handleAccessRevokeInvitation},
		{name: "list users", req: requestWithRoute(http.MethodGet, "/api/access/users", nil, authpkg.Admin(), nil), handle: h.handleAccessListUsers},
		{name: "user drilldown", req: requestWithRoute(http.MethodGet, "/api/access/users/1", nil, authpkg.Admin(), map[string]string{"id": "1"}), handle: h.handleAccessGetUserDrilldown},
		{name: "update user", req: requestWithRoute(http.MethodPatch, "/api/access/users/1", []byte(`{"role":"operator"}`), authpkg.Admin(), map[string]string{"id": "1"}), handle: h.handleAccessUpdateUser},
		{name: "list roles", req: requestWithRoute(http.MethodGet, "/api/access/roles", nil, authpkg.Admin(), nil), handle: h.handleAccessListRoles},
		{name: "list sessions", req: requestWithRoute(http.MethodGet, "/api/access/sessions", nil, authpkg.Admin(), nil), handle: h.handleAccessListSessions},
		{name: "revoke session", req: requestWithRoute(http.MethodPost, "/api/access/sessions/s1/revoke", []byte(`{}`), authpkg.Admin(), map[string]string{"id": "s1"}), handle: h.handleAccessRevokeSession},
		{name: "list audit", req: requestWithRoute(http.MethodGet, "/api/access/log", nil, authpkg.Admin(), nil), handle: h.handleAccessListAudit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handle(rec, tc.req)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		})
	}
}
