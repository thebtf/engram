package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authpkg "github.com/thebtf/engram/internal/auth"
)

func TestAuthDisabledMeReturnsSyntheticAdmin(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")

	ta, err := NewTokenAuth("")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}
	svc := &Service{tokenAuth: ta}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec := httptest.NewRecorder()
	svc.handleAuthMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("disabled-auth /api/auth/me status=%d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/auth/me response: %v", err)
	}
	if body["authenticated"] != true {
		t.Fatalf("authenticated=%v, want true", body["authenticated"])
	}
	if body["role"] != "admin" {
		t.Fatalf("role=%v, want admin", body["role"])
	}
	if body["auth_disabled"] != true {
		t.Fatalf("auth_disabled=%v, want true", body["auth_disabled"])
	}
	if body["source"] != "auth-disabled" {
		t.Fatalf("source=%v, want auth-disabled", body["source"])
	}
	if body["synthetic"] != true {
		t.Fatalf("synthetic=%v, want true", body["synthetic"])
	}
}

func TestAuthOnMeWithoutSessionReturnsUnauthorized(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "")

	ta, err := NewTokenAuth("secret-token-xyz")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}
	svc := &Service{tokenAuth: ta}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec := httptest.NewRecorder()
	svc.handleAuthMe(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("auth-on /api/auth/me status=%d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/auth/me response: %v", err)
	}
	if body["authenticated"] != false {
		t.Fatalf("authenticated=%v, want false", body["authenticated"])
	}
	if body["auth_disabled"] != false {
		t.Fatalf("auth_disabled=%v, want false", body["auth_disabled"])
	}
}

func TestRequireSessionAdmin_SourceGate(t *testing.T) {
	svc := &Service{}

	cases := []struct {
		name         string
		withIdentity bool
		identity     authpkg.Identity
		wantRejected bool
		wantStatus   int
	}{
		{
			name:         "missing identity rejected",
			wantRejected: true,
			wantStatus:   http.StatusUnauthorized,
		},
		{
			name:         "master source rejected",
			withIdentity: true,
			identity:     authpkg.Admin(),
			wantRejected: true,
			wantStatus:   http.StatusForbidden,
		},
		{
			name:         "session admin accepted",
			withIdentity: true,
			identity:     authpkg.Session("admin"),
			wantRejected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.withIdentity {
				ctx = buildAuthCtx(ctx, tc.identity)
			}

			req := httptest.NewRequest(http.MethodGet, "/api/auth/tokens", nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			rejected := svc.requireSessionAdmin(rec, req)

			if rejected != tc.wantRejected {
				t.Fatalf("rejected=%v, want %v", rejected, tc.wantRejected)
			}
			if tc.wantRejected && rec.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestAuthDisabledTokenProbeReachesStoreReadiness(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")

	ta, err := NewTokenAuth("")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}
	svc := &Service{tokenAuth: ta}

	h := ta.Middleware(http.HandlerFunc(svc.handleListTokens))
	req := httptest.NewRequest(http.MethodGet, "/api/auth/tokens", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("disabled-auth token probe stopped at auth gate: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled-auth token probe status=%d, want store readiness %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestAuthDisabledUsersProbeReachesStoreReadiness(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")

	ta, err := NewTokenAuth("")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}
	authHandlers := NewAuthHandlers(nil, nil, nil)

	h := ta.Middleware(http.HandlerFunc(authHandlers.handleListUsers))
	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("disabled-auth users probe stopped at auth gate: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled-auth users probe status=%d, want store readiness %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestAuthOnUsersWithoutAdminRoleStillForbidden(t *testing.T) {
	authHandlers := NewAuthHandlers(nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	rec := httptest.NewRecorder()
	authHandlers.handleListUsers(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("users without admin role status=%d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}
