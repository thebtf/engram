package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	authpkg "github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormio "gorm.io/gorm"
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

func openIsolatedAuthSetupPair(t *testing.T) (*authLifecycleEnv, *authLifecycleEnv, *gormio.DB) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping auth setup integration test")
	}

	schema := fmt.Sprintf("auth_setup_test_%d", time.Now().UnixNano())
	rootDB, err := gormio.Open(postgres.Open(dsn), &gormio.Config{})
	require.NoError(t, err)
	rootSQLDB, err := rootDB.DB()
	require.NoError(t, err)
	rootSQLDB.SetMaxOpenConns(1)
	rootSQLDB.SetMaxIdleConns(1)
	require.NoError(t, rootDB.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, schema)).Error)
	t.Cleanup(func() {
		require.NoError(t, rootDB.Exec(fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema)).Error)
		_ = rootSQLDB.Close()
	})

	parsedDSN, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsedDSN.Query()
	query.Set("search_path", schema)
	parsedDSN.RawQuery = query.Encode()
	schemaDSN := parsedDSN.String()

	openDB := func() *gormio.DB {
		db, openErr := gormio.Open(postgres.Open(schemaDSN), &gormio.Config{})
		require.NoError(t, openErr)
		sqlDB, sqlErr := db.DB()
		require.NoError(t, sqlErr)
		sqlDB.SetMaxOpenConns(2)
		sqlDB.SetMaxIdleConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	dbA := openDB()
	dbB := openDB()
	require.NoError(t, dbA.AutoMigrate(
		&gormdb.User{},
		&gormdb.Invitation{},
		&gormdb.AuthSession{},
		&gormdb.AuditLogEntry{},
	))

	newEnv := func(db *gormio.DB) *authLifecycleEnv {
		store := &gormdb.Store{DB: db}
		env := &authLifecycleEnv{
			store:       store,
			users:       gormdb.NewUserStore(db),
			invitations: gormdb.NewInvitationStore(db),
			sessions:    gormdb.NewAuthSessionStore(db),
			access:      gormdb.NewDomainOwnerStore(store),
		}
		env.handlers = NewAuthHandlers(env.users, env.invitations, env.sessions, env.access)
		return env
	}
	return newEnv(dbA), newEnv(dbB), dbA
}

func resetIsolatedAuthSetup(t *testing.T, db *gormio.DB) {
	t.Helper()
	require.NoError(t, db.Exec("DELETE FROM sessions").Error)
	require.NoError(t, db.Exec("DELETE FROM invitations").Error)
	require.NoError(t, db.Exec("DELETE FROM audit_log").Error)
	require.NoError(t, db.Exec("DELETE FROM users").Error)
}

func runConcurrentInitialAdminSetupRequests(
	t *testing.T,
	handlers []*AuthHandlers,
	payloads [][]byte,
) []*httptest.ResponseRecorder {
	t.Helper()
	require.Len(t, payloads, len(handlers))

	ready := make(chan struct{}, len(handlers))
	release := make(chan struct{})
	barrier := func() {
		ready <- struct{}{}
		<-release
	}
	for _, handler := range handlers {
		handler.beforeInitialAdminCreate = barrier
	}
	defer func() {
		for _, handler := range handlers {
			handler.beforeInitialAdminCreate = nil
		}
	}()

	recorders := make([]*httptest.ResponseRecorder, len(handlers))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range handlers {
		recorders[index] = httptest.NewRecorder()
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(payloads[index]))
			handlers[index].handleSetup(recorders[index], req)
		}(index)
	}
	close(start)

	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for range handlers {
		select {
		case <-ready:
		case <-deadline.C:
			close(release)
			wg.Wait()
			t.Fatal("setup request did not reach the pre-create barrier")
		}
	}
	close(release)
	wg.Wait()
	return recorders
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

func TestAuthHandlersLifecycle_ConcurrentInitialAdminSetupExactlyOne(t *testing.T) {
	envA, envB, db := openIsolatedAuthSetupPair(t)

	for iteration := 0; iteration < 20; iteration++ {
		resetIsolatedAuthSetup(t, db)
		payloads := [][]byte{
			[]byte(fmt.Sprintf(`{"email":"initial-%d-a@example.com","password":"password-123"}`, iteration)),
			[]byte(fmt.Sprintf(`{"email":"initial-%d-b@example.com","password":"password-123"}`, iteration)),
		}
		handlers := []*AuthHandlers{envA.handlers, envB.handlers}
		recorders := runConcurrentInitialAdminSetupRequests(t, handlers, payloads)

		created := 0
		conflicts := 0
		for _, recorder := range recorders {
			switch recorder.Code {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				conflicts++
				require.Contains(t, recorder.Body.String(), "setup already completed")
			default:
				t.Fatalf("iteration %d unexpected setup status=%d body=%s", iteration, recorder.Code, recorder.Body.String())
			}
		}
		require.Equal(t, 1, created, "iteration %d", iteration)
		require.Equal(t, 1, conflicts, "iteration %d", iteration)

		var userCount int64
		require.NoError(t, db.Model(&gormdb.User{}).Count(&userCount).Error)
		require.Equal(t, int64(1), userCount, "iteration %d", iteration)
		var adminCount int64
		require.NoError(t, db.Model(&gormdb.User{}).Where("role = ? AND disabled = false", gormdb.DashboardRoleAdmin).Count(&adminCount).Error)
		require.Equal(t, int64(1), adminCount, "iteration %d", iteration)
		var auditCount int64
		require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "auth_setup_completed").Count(&auditCount).Error)
		require.Equal(t, int64(1), auditCount, "iteration %d", iteration)
		var sessionCount int64
		require.NoError(t, db.Model(&gormdb.AuthSession{}).Count(&sessionCount).Error)
		require.Zero(t, sessionCount, "iteration %d", iteration)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewBufferString(`{"email":"after@example.com","password":"password-123"}`))
	envA.handlers.handleSetup(recorder, req)
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "setup already completed")

	var userCount int64
	require.NoError(t, db.Model(&gormdb.User{}).Count(&userCount).Error)
	require.Equal(t, int64(1), userCount)
	var auditCount int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "auth_setup_completed").Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestAuthHandlersLifecycle_ConcurrentInitialAdminSetupDuplicateEmailFailsSafely(t *testing.T) {
	envA, envB, db := openIsolatedAuthSetupPair(t)
	payload := []byte(`{"email":"same@example.com","password":"password-123"}`)
	handlers := []*AuthHandlers{envA.handlers, envB.handlers}
	recorders := runConcurrentInitialAdminSetupRequests(t, handlers, [][]byte{payload, payload})

	created := 0
	conflicts := 0
	for _, recorder := range recorders {
		switch recorder.Code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
			require.Contains(t, recorder.Body.String(), "setup already completed")
		default:
			t.Fatalf("unexpected setup status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	require.Equal(t, 1, created)
	require.Equal(t, 1, conflicts)

	var userCount int64
	require.NoError(t, db.Model(&gormdb.User{}).Count(&userCount).Error)
	require.Equal(t, int64(1), userCount)
	var auditCount int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "auth_setup_completed").Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
	var sessionCount int64
	require.NoError(t, db.Model(&gormdb.AuthSession{}).Count(&sessionCount).Error)
	require.Zero(t, sessionCount)
}

func TestAuthHandlersLifecycle_InitialAdminSetupInvalidRequestLeavesSetupRetryable(t *testing.T) {
	envA, envB, db := openIsolatedAuthSetupPair(t)

	invalid := httptest.NewRecorder()
	invalidReq := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewBufferString(`{"email":"invalid@example.com"}`))
	envA.handlers.handleSetup(invalid, invalidReq)
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())

	var userCount int64
	require.NoError(t, db.Model(&gormdb.User{}).Count(&userCount).Error)
	require.Zero(t, userCount)
	var auditCount int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "auth_setup_completed").Count(&auditCount).Error)
	require.Zero(t, auditCount)

	retry := httptest.NewRecorder()
	retryReq := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewBufferString(`{"email":"retry@example.com","password":"password-123"}`))
	envB.handlers.handleSetup(retry, retryReq)
	require.Equal(t, http.StatusCreated, retry.Code, retry.Body.String())
}

func TestAuthHandlersLifecycle_InitialAdminSetupInsertFailureLeavesSetupRetryable(t *testing.T) {
	envA, envB, db := openIsolatedAuthSetupPair(t)
	tooLongEmail := strings.Repeat("x", 256) + "@example.com"

	failed := httptest.NewRecorder()
	failedReq := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/setup",
		bytes.NewBufferString(fmt.Sprintf(`{"email":%q,"password":"password-123"}`, tooLongEmail)),
	)
	envA.handlers.handleSetup(failed, failedReq)
	require.Equal(t, http.StatusInternalServerError, failed.Code, failed.Body.String())

	var userCount int64
	require.NoError(t, db.Model(&gormdb.User{}).Count(&userCount).Error)
	require.Zero(t, userCount)
	var auditCount int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "auth_setup_completed").Count(&auditCount).Error)
	require.Zero(t, auditCount)

	retry := httptest.NewRecorder()
	retryReq := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewBufferString(`{"email":"retry@example.com","password":"password-123"}`))
	envB.handlers.handleSetup(retry, retryReq)
	require.Equal(t, http.StatusCreated, retry.Code, retry.Body.String())
}

func TestAuthHandlersLifecycle_InitialAdminSetupCancelledContextLeavesSetupRetryable(t *testing.T) {
	envA, envB, db := openIsolatedAuthSetupPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	failed := httptest.NewRecorder()
	failedReq := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/setup",
		bytes.NewBufferString(`{"email":"cancelled@example.com","password":"password-123"}`),
	).WithContext(ctx)
	envA.handlers.handleSetup(failed, failedReq)
	require.Equal(t, http.StatusInternalServerError, failed.Code, failed.Body.String())

	var userCount int64
	require.NoError(t, db.Model(&gormdb.User{}).Count(&userCount).Error)
	require.Zero(t, userCount)
	var auditCount int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "auth_setup_completed").Count(&auditCount).Error)
	require.Zero(t, auditCount)

	retry := httptest.NewRecorder()
	retryReq := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewBufferString(`{"email":"retry@example.com","password":"password-123"}`))
	envB.handlers.handleSetup(retry, retryReq)
	require.Equal(t, http.StatusCreated, retry.Code, retry.Body.String())
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
}

func TestAuthHandlersLifecycle_LastAdminDemoteRaceLeavesOneAdmin(t *testing.T) {
	env := openAuthLifecycleEnv(t)
	baselineAdmins, err := env.users.CountAdmins()
	require.NoError(t, err)
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
	successes := 0
	errorsSeen := 0
	expectedSuccesses := 2
	if baselineAdmins == 0 {
		expectedSuccesses = 1
	}
	for _, updateErr := range []error{err1, err2} {
		if updateErr == nil {
			successes++
			continue
		}
		errorsSeen++
		require.EqualError(t, updateErr, "cannot demote the last admin")
		require.NotContains(t, strings.ToLower(updateErr.Error()), "deadlock")
	}
	require.Equal(t, expectedSuccesses, successes)
	require.Equal(t, 2-expectedSuccesses, errorsSeen)
	count, err := env.users.CountAdmins()
	require.NoError(t, err)
	expectedAdmins := baselineAdmins
	expectedPairAdmins := 0
	if expectedAdmins == 0 {
		expectedAdmins = 1
		expectedPairAdmins = 1
	}
	require.Equal(t, expectedAdmins, count)
	adminA, err = env.users.GetUserByID(adminA.ID)
	require.NoError(t, err)
	adminB, err = env.users.GetUserByID(adminB.ID)
	require.NoError(t, err)
	activePairAdmins := 0
	for _, admin := range []*gormdb.User{adminA, adminB} {
		if admin.Role == gormdb.DashboardRoleAdmin && !admin.Disabled {
			activePairAdmins++
		}
	}
	require.Equal(t, expectedPairAdmins, activePairAdmins)
}

func TestAuthHandlersLifecycle_DisabledAdminCanBeDemotedWithoutLastAdminError(t *testing.T) {
	env := openAuthLifecycleEnv(t)
	baselineAdmins, err := env.users.CountAdmins()
	require.NoError(t, err)
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
	require.Equal(t, baselineAdmins+1, count)
	active, err = env.users.GetUserByID(active.ID)
	require.NoError(t, err)
	require.Equal(t, gormdb.DashboardRoleAdmin, active.Role)
	require.False(t, active.Disabled)
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
