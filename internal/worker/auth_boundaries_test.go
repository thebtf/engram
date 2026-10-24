package worker

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authpkg "github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

// SQLite covers real store reads and closed-pool errors, not PostgreSQL row locking.
func openAuthBoundaryReads(t *testing.T) (*gormlib.DB, *sql.DB) {
	t.Helper()
	pool, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	pool.SetMaxOpenConns(1)
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT, password_hash TEXT, role TEXT, disabled BOOLEAN, created_at DATETIME, last_login_at DATETIME)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, user_id INTEGER, created_at DATETIME, expires_at DATETIME, user_agent TEXT, remote_addr TEXT, revoked_at DATETIME, revoked_by INTEGER, revocation_reason TEXT)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	return db, pool
}

func authBoundaryRouter(t *testing.T, handlers *AuthHandlers) *Service {
	t.Helper()
	ta, err := NewTokenAuth("auth-boundary-test-only")
	require.NoError(t, err)
	if handlers != nil {
		ta.SetAuthStores(handlers.users, handlers.sessions)
	}
	s := &Service{router: chi.NewRouter(), tokenAuth: ta, authHandlers: handlers, config: &config.Config{}}
	s.ready.Store(true)
	s.setupMiddleware()
	s.setupRoutes()
	return s
}

func authBoundaryCookie(t *testing.T, ta *TokenAuth, expiry time.Time) *http.Cookie {
	t.Helper()
	payload, err := json.Marshal(sessionPayload{Role: "admin", Exp: expiry.Unix()})
	require.NoError(t, err)
	return &http.Cookie{Name: sessionCookieName, Value: base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(computeHMAC(payload, ta.CookieKey()))}
}

func TestAuthBoundaryMeCredentialParity(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	db, _ := openAuthBoundaryReads(t)
	users, sessions := gormdb.NewUserStore(db), gormdb.NewAuthSessionStore(db)
	user, err := users.CreateUser("boundary@example.test", "", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	active, err := sessions.CreateSession(user.ID, time.Hour, "fixture", "127.0.0.1")
	require.NoError(t, err)
	expired, err := sessions.CreateSession(user.ID, -time.Hour, "fixture", "127.0.0.1")
	require.NoError(t, err)
	revoked, err := sessions.CreateSession(user.ID, time.Hour, "fixture", "127.0.0.1")
	require.NoError(t, err)
	require.NoError(t, db.Model(revoked).Update("revoked_at", time.Now().UTC()).Error)
	s := authBoundaryRouter(t, NewAuthHandlers(users, nil, sessions, nil))
	s.tokenAuth.SetAuthentikConfig(true, false, []string{"192.0.2.1"})
	validHMAC := authBoundaryCookie(t, s.tokenAuth, time.Now().Add(time.Hour))
	expiredHMAC := authBoundaryCookie(t, s.tokenAuth, time.Now().Add(-time.Hour))
	for _, tc := range []struct {
		name, header, value, session, role string
		cookie                             *http.Cookie
		want                               int
	}{
		{name: "master bearer", header: "Authorization", value: "Bearer " + "auth-boundary-test-only", role: "admin", want: http.StatusOK},
		{name: "master header", header: "X-Auth-Token", value: "auth-boundary-test-only", role: "admin", want: http.StatusOK},
		{name: "trusted Authentik", header: "X-Authentik-Email", value: user.Email, role: user.Role, want: http.StatusOK},
		{name: "valid HMAC", cookie: validHMAC, role: "admin", want: http.StatusOK},
		{name: "expired HMAC", cookie: expiredHMAC, want: http.StatusUnauthorized},
		{name: "valid DB session", session: active.ID, role: user.Role, want: http.StatusOK},
		{name: "expired DB session", session: expired.ID, want: http.StatusUnauthorized},
		{name: "revoked DB session", session: revoked.ID, want: http.StatusUnauthorized},
		{name: "invalid bearer wins over valid HMAC", header: "Authorization", value: "Bearer " + "invalid-fixture-token", cookie: validHMAC, want: http.StatusUnauthorized},
		{name: "HMAC wins over DB user", cookie: validHMAC, session: active.ID, role: "admin", want: http.StatusOK},
		{name: "anonymous", want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/flags", "/api/auth/me"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.RemoteAddr = "192.0.2.1:443"
				if tc.header != "" {
					req.Header.Set(tc.header, tc.value)
				}
				if tc.cookie != nil {
					req.AddCookie(tc.cookie)
				}
				if tc.session != "" {
					req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: tc.session})
				}
				rec := httptest.NewRecorder()
				s.router.ServeHTTP(rec, req)
				assert.Equal(t, tc.want, rec.Code, "%s must resolve the same credential; body=%s", path, rec.Body.String())
				if path == "/api/auth/me" && tc.want == http.StatusOK && rec.Code == http.StatusOK {
					var body struct {
						Authenticated bool           `json:"authenticated"`
						Role          string         `json:"role"`
						User          map[string]any `json:"user"`
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
					assert.True(t, body.Authenticated)
					assert.Equal(t, tc.role, body.Role)
					if tc.session == active.ID && tc.cookie == nil {
						assert.Equal(t, user.Email, body.User["email"], "browser identity shape must survive")
					}
				}
			}
		})
	}
}

func TestAuthBoundaryAuthentikTransportPeer(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	db, _ := openAuthBoundaryReads(t)
	users := gormdb.NewUserStore(db)
	user, err := users.CreateUser("proxy-boundary@example.test", "", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	s := authBoundaryRouter(t, NewAuthHandlers(users, nil, gormdb.NewAuthSessionStore(db), nil))
	for _, tc := range []struct {
		name, peer, header, value string
		trusted                   []string
		enabled                   bool
		want                      int
	}{
		{name: "untrusted plain peer", peer: "198.51.100.2:443", trusted: []string{"192.0.2.1"}, enabled: true, want: http.StatusUnauthorized},
		{name: "spoofed real IP", peer: "198.51.100.2:443", header: "X-Real-IP", value: "192.0.2.1", trusted: []string{"192.0.2.1"}, enabled: true, want: http.StatusUnauthorized},
		{name: "spoofed forwarded for", peer: "198.51.100.2:443", header: "X-Forwarded-For", value: "192.0.2.1", trusted: []string{"192.0.2.1"}, enabled: true, want: http.StatusUnauthorized},
		{name: "trusted plain peer", peer: "192.0.2.1:443", trusted: []string{"192.0.2.1"}, enabled: true, want: http.StatusOK},
		{name: "trusted proxy real client IP", peer: "192.0.2.1:443", header: "X-Real-IP", value: "198.51.100.2", trusted: []string{"192.0.2.1"}, enabled: true, want: http.StatusOK},
		{name: "trusted proxy forwarded client IP", peer: "192.0.2.1:443", header: "X-Forwarded-For", value: "198.51.100.2, 192.0.2.1", trusted: []string{"192.0.2.1"}, enabled: true, want: http.StatusOK},
		{name: "trusted IPv6 proxy", peer: "[2001:db8::1]:443", header: "X-Real-IP", value: "198.51.100.2", trusted: []string{"2001:db8::1"}, enabled: true, want: http.StatusOK},
		{name: "untrusted IPv6 proxy spoof", peer: "[2001:db8::2]:443", header: "X-Forwarded-For", value: "2001:db8::1", trusted: []string{"2001:db8::1"}, enabled: true, want: http.StatusUnauthorized},
		{name: "mapped IPv4 proxy", peer: "[::ffff:192.0.2.1]:443", trusted: []string{"192.0.2.1"}, enabled: true, want: http.StatusOK},
		{name: "malformed peer", peer: "not-an-address", header: "X-Real-IP", value: "192.0.2.1", trusted: []string{"192.0.2.1"}, enabled: true, want: http.StatusUnauthorized},
		{name: "empty trust list", peer: "192.0.2.1:443", enabled: true, want: http.StatusUnauthorized},
		{name: "Authentik disabled", peer: "192.0.2.1:443", trusted: []string{"192.0.2.1"}, want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.tokenAuth.SetAuthentikConfig(tc.enabled, false, tc.trusted)
			req := httptest.NewRequest(http.MethodGet, "/api/flags", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Authentik-Email", user.Email)
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code, "trust must use the transport peer, not client headers; body=%s", rec.Body.String())
		})
	}
}

func TestAuthBoundaryUserLogoutStoreFailure(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	db, pool := openAuthBoundaryReads(t)
	users, sessions := gormdb.NewUserStore(db), gormdb.NewAuthSessionStore(db)
	user, err := users.CreateUser("logout-boundary@example.test", "", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	session, err := sessions.CreateSession(user.ID, time.Hour, "fixture", "127.0.0.1")
	require.NoError(t, err)
	_, err = sessions.GetSession(session.ID)
	require.NoError(t, err, "the session must be active before the store failure")
	s := authBoundaryRouter(t, NewAuthHandlers(users, nil, sessions, nil))
	require.NoError(t, pool.Close())
	require.ErrorContains(t, sessions.DeleteSession(session.ID), "database is closed", "closed pool must produce a real storage error")
	req := httptest.NewRequest(http.MethodPost, "http://localhost:37777/api/auth/user-logout", nil)
	req.Header.Set("Authorization", "Bearer "+"auth-boundary-test-only")
	req.Header.Set("Origin", "http://localhost:37777")
	req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: session.ID})
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	assert.GreaterOrEqual(t, rec.Code, 500, "failed session revocation must not report success; body=%s", rec.Body.String())
	assert.Empty(t, rec.Result().Cookies(), "failed revocation must not clear the browser's retry credential")
}

func TestAuthBoundaryLogoutOriginAdmission(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	s := authBoundaryRouter(t, NewAuthHandlers(nil, nil, nil, nil))
	cookie := authBoundaryCookie(t, s.tokenAuth, time.Now().Add(time.Hour))
	for _, path := range []string{"/api/auth/logout", "/api/auth/user-logout"} {
		t.Run(path, func(t *testing.T) {
			for _, tc := range []struct {
				name, origin, site string
				want               int
			}{
				{name: "same origin", origin: "http://localhost:37777", site: "same-origin", want: http.StatusOK},
				{name: "allowed Vite dev", origin: "http://localhost:5173", site: "same-site", want: http.StatusOK},
				{name: "allowed console", origin: "http://localhost:3000", site: "same-site", want: http.StatusOK},
				{name: "allowed loopback dev", origin: "http://127.0.0.1:5173", site: "cross-site", want: http.StatusOK},
				{name: "non browser caller", want: http.StatusOK},
				{name: "foreign origin", origin: "https://foreign.example.test", site: "cross-site", want: http.StatusForbidden},
				{name: "unlisted same site port", origin: "http://localhost:5180", site: "same-site", want: http.StatusForbidden},
				{name: "opaque origin", origin: "null", site: "cross-site", want: http.StatusForbidden},
				{name: "cross site without origin", site: "cross-site", want: http.StatusForbidden},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodPost, "http://localhost:37777"+path, nil)
					req.AddCookie(cookie)
					if tc.origin != "" {
						req.Header.Set("Origin", tc.origin)
					}
					if tc.site != "" {
						req.Header.Set("Sec-Fetch-Site", tc.site)
					}
					rec := httptest.NewRecorder()
					s.router.ServeHTTP(rec, req)
					assert.Equal(t, tc.want, rec.Code, "logout must admit legitimate callers and reject foreign browser origins; body=%s", rec.Body.String())
					if tc.want == http.StatusForbidden {
						assert.Empty(t, rec.Result().Cookies(), "rejection must happen before cookie mutation")
					}
				})
			}
		})
	}
}

func TestAuthBoundaryDisabledIdentity(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	s := authBoundaryRouter(t, nil)
	for _, path := range []string{"/api/flags", "/api/auth/me"} {
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		if path == "/api/auth/me" {
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, true, body["authenticated"])
			assert.Equal(t, true, body["auth_disabled"])
			assert.Equal(t, true, body["synthetic"])
			assert.Equal(t, "admin", body["role"])
		}
	}
}

func TestAuthBoundaryDirectAuthentikMiddleware(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	db, _ := openAuthBoundaryReads(t)
	users := gormdb.NewUserStore(db)
	user, err := users.CreateUser("direct-proxy@example.test", "", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	s := authBoundaryRouter(t, NewAuthHandlers(users, nil, gormdb.NewAuthSessionStore(db), nil))
	s.tokenAuth.SetAuthentikConfig(true, false, []string{"192.0.2.1", "2001:db8::1"})
	handler := s.tokenAuth.Middleware(http.HandlerFunc(s.handleGetFlags))
	for _, tc := range []struct {
		peer string
		want int
	}{
		{peer: "192.0.2.1:443", want: http.StatusOK},
		{peer: "[2001:db8::1]:443", want: http.StatusOK},
		{peer: "2001:db8::1", want: http.StatusOK},
		{peer: "198.51.100.2:443", want: http.StatusUnauthorized},
		{peer: "malformed", want: http.StatusUnauthorized},
	} {
		t.Run(tc.peer, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/flags", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Authentik-Email", user.Email)
			req.Header.Set("X-Real-IP", "192.0.2.1")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

func TestAuthBoundaryMeBearerStoreErrorParity(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	db, pool := openAuthBoundaryReads(t)
	s := authBoundaryRouter(t, nil)
	s.tokenAuth.SetValidator(authpkg.NewValidator(s.tokenAuth.Token(), gormdb.NewTokenStore(&gormdb.Store{DB: db})))
	cookie := authBoundaryCookie(t, s.tokenAuth, time.Now().Add(time.Hour))
	require.NoError(t, pool.Close())
	for _, path := range []string{"/api/flags", "/api/auth/me"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Auth-Token", authpkg.TokenRawPrefix+strings.Repeat("a", authpkg.TokenBodyLen))
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code, "bearer storage failure must not fall through to valid cookie")
	}
}

func TestAuthBoundaryLogoutIgnoresForgedPublicOrigin(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	db, pool := openAuthBoundaryReads(t)
	s := authBoundaryRouter(t, NewAuthHandlers(nil, nil, gormdb.NewAuthSessionStore(db), nil))
	require.NoError(t, pool.Close())
	for _, path := range []string{"/api/auth/logout", "/api/auth/user-logout"} {
		for _, tc := range []struct {
			origin, target string
			want           int
		}{
			{origin: "https://public.example.test", target: "http://backend.example.test", want: http.StatusForbidden},
			{origin: "https://public.example.test", target: "https://public.example.test", want: http.StatusOK},
		} {
			req := httptest.NewRequest(http.MethodPost, tc.target+path, nil)
			req.Header.Set("X-Auth-Token", s.tokenAuth.Token())
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Forwarded-Host", "public.example.test")
			req.Header.Set("X-Forwarded-Proto", "https")
			if tc.want == http.StatusForbidden {
				req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "origin-rejection-must-precede-store"})
			}
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code, "forwarded origin is not authority; actual same-origin TLS remains valid")
			if tc.want == http.StatusForbidden {
				require.Empty(t, rec.Result().Cookies())
			}
		}
	}
}

func TestAuthBoundarySplitPublicConsoleOrigin(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tc := range []struct {
		name, configured, origin string
		want                     int
	}{
		{name: "unconfigured external origin", origin: "https://console.example.test", want: http.StatusForbidden},
		{name: "explicit HTTPS console", configured: "https://console.example.test", origin: "https://console.example.test", want: http.StatusOK},
		{name: "sibling origin", configured: "https://console.example.test", origin: "https://sibling.example.test", want: http.StatusForbidden},
		{name: "forged suffix", configured: "https://console.example.test", origin: "https://console.example.test.attacker.test", want: http.StatusForbidden},
		{name: "wrong scheme", configured: "https://console.example.test", origin: "http://console.example.test", want: http.StatusForbidden},
		{name: "opaque origin", configured: "https://console.example.test", origin: "null", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_AUTH_ALLOWED_ORIGINS", tc.configured)
			cfg, err := config.Load()
			require.NoError(t, err)
			s := authBoundaryRouter(t, NewAuthHandlers(nil, nil, nil, nil))
			s.config = cfg
			for _, path := range []string{"/api/auth/logout", "/api/auth/user-logout"} {
				req := httptest.NewRequest(http.MethodPost, "http://server:37777"+path, nil)
				req.RemoteAddr = "10.240.250.10:443"
				req.Header.Set("X-Auth-Token", s.tokenAuth.Token())
				req.Header.Set("Origin", tc.origin)
				req.Header.Set("Sec-Fetch-Site", "same-origin")
				req.Header.Set("X-Forwarded-Host", "console.example.test")
				req.Header.Set("X-Forwarded-Proto", "https")
				rec := httptest.NewRecorder()
				s.router.ServeHTTP(rec, req)
				assert.Equal(t, tc.want, rec.Code, "%s: only configured exact public origin may cross the split console/backend hop", path)
				if tc.want == http.StatusForbidden {
					assert.Empty(t, rec.Result().Cookies())
				}
			}
		})
	}
}

func TestAuthBoundaryInvalidOriginStopsWorkerStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ENGRAM_AUTH_ALLOWED_ORIGINS", "https://*.example.test")
	s, err := NewService("invalid-origin-must-not-start", nil)
	require.ErrorContains(t, err, "ENGRAM_AUTH_ALLOWED_ORIGINS")
	require.Nil(t, s)
}
