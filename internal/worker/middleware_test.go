package worker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authpkg "github.com/thebtf/engram/internal/auth"
)

// ---------------------------------------------------------------------------
// SecurityHeaders — basic header set
// ---------------------------------------------------------------------------

func TestSecurityHeaders_StandardHeaders(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := SecurityHeaders(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	want := map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"X-XSS-Protection":       "1; mode=block",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for header, expected := range want {
		if got := rec.Header().Get(header); got != expected {
			t.Errorf("header %s = %q, want %q", header, got, expected)
		}
	}
}

func TestSecurityHeaders_CSPValue(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy header must be present")
	}
	if csp != strictContentSecurityPolicy {
		t.Errorf("CSP = %q\nwant %q", csp, strictContentSecurityPolicy)
	}

	if pp := rec.Header().Get("Permissions-Policy"); pp == "" {
		t.Error("Permissions-Policy must be set")
	}
}

func TestOperatorConsoleHTMLSecurityHeadersAllowNuxtInlineBootstrap(t *testing.T) {
	hdr := http.Header{}
	html := []byte(`<script type="module" src="/_nuxt/app.js"></script><script>window.__NUXT__={}</script><script type="application/json">[{"serverRendered":false}]</script>`)

	setOperatorConsoleHTMLSecurityHeaders(hdr, html)

	csp := hdr.Get("Content-Security-Policy")
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("operator console CSP must not allow arbitrary inline scripts, got %q", csp)
	}
	if got := strings.Count(csp, "'sha256-"); got != 3 {
		t.Fatalf("operator console CSP must hash the 2 inline Nuxt scripts plus Nuxt UI colors helper, got %d hash source(s) in %q", got, csp)
	}
	if want := inlineScriptHashSource([]byte(nuxtUIColorCleanupScript)); !strings.Contains(csp, want) {
		t.Fatalf("operator console CSP must allow the Nuxt UI colors cleanup helper by hash %s, got %q", want, csp)
	}
}

func TestSecurityHeaders_OptionsReturns204(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should not reach here for OPTIONS
		w.WriteHeader(http.StatusOK)
	})
	h := SecurityHeaders(inner)

	req := httptest.NewRequest(http.MethodOptions, "/api/memory", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("CORS origin not set for allowed OPTIONS origin")
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("Access-Control-Allow-Methods must be set for preflight")
	}
}

// ---------------------------------------------------------------------------
// SecurityHeaders — CORS whitelist
// ---------------------------------------------------------------------------

func TestSecurityHeaders_CORSWhitelist(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name        string
		origin      string
		wantAllowed bool
	}{
		{"dashboard port", "http://localhost:37777", true},
		{"vite dev", "http://127.0.0.1:5173", true},
		{"plain localhost", "http://localhost", true},
		{"loopback ip", "http://127.0.0.1", true},
		{"port 3000", "http://localhost:3000", true},
		// blocked
		{"external", "http://evil.com", false},
		{"lookalike", "http://evil-localhost.com", false},
		{"subdomain", "http://localhost.evil.com", false},
		{"unknown port", "http://localhost:8888", false},
		{"no origin", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get("Access-Control-Allow-Origin")
			if tc.wantAllowed {
				if got != tc.origin {
					t.Errorf("origin %q: CORS = %q, want %q", tc.origin, got, tc.origin)
				}
			} else {
				if got != "" {
					t.Errorf("origin %q: expected no CORS header, got %q", tc.origin, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MaxBodySize
// ---------------------------------------------------------------------------

func TestMaxBodySize_Table(t *testing.T) {
	const limit = int64(512)

	h := MaxBodySize(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name       string
		length     int64
		wantStatus int
	}{
		{"under limit", 100, http.StatusOK},
		{"at limit", 512, http.StatusOK},
		{"over limit", 513, http.StatusRequestEntityTooLarge},
		{"well over limit", 10000, http.StatusRequestEntityTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/memory", nil)
			req.ContentLength = tc.length
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("ContentLength=%d: status=%d, want %d", tc.length, rec.Code, tc.wantStatus)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TokenAuth
// ---------------------------------------------------------------------------

func TestTokenAuth_DisabledAllowsAll(t *testing.T) {
	ta, err := NewTokenAuth("")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}

	h := ta.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("disabled auth: status=%d, want 200", rec.Code)
	}
}

func TestTokenAuth_DisabledInjectsAdminContext(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")

	ta, err := NewTokenAuth("")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}

	var sawIdentity bool
	var sawSessionAdmin bool
	var sawDisabledSource bool
	var sawLegacyAdmin bool
	h := ta.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := authpkg.IdentityFrom(r.Context())
		sawIdentity = ok
		if ok {
			sawSessionAdmin = id.IsSessionAdmin()
			sawDisabledSource = id.Source == authpkg.SourceAuthDisabled
		}
		sawLegacyAdmin = getAuthRole(r) == "admin"
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("disabled auth: status=%d, want 200", rec.Code)
	}
	if !sawIdentity {
		t.Fatal("disabled auth should expose auth identity to downstream handler")
	}
	if sawSessionAdmin {
		t.Fatal("disabled auth identity must not be a session admin")
	}
	if !sawDisabledSource {
		t.Fatal("disabled auth identity must retain its distinct source")
	}
	if !sawLegacyAdmin {
		t.Fatal(`disabled auth should expose legacy role "admin" to downstream handler`)
	}
}

func TestTokenAuth_DisabledEmptyTokenDoesNotInjectAdminContext(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "")

	ta, err := NewTokenAuth("")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}

	var sawIdentity bool
	var sawLegacyRole string
	h := ta.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawIdentity = authpkg.IdentityFrom(r.Context())
		sawLegacyRole = getAuthRole(r)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("empty-token bootstrap path: status=%d, want 200", rec.Code)
	}
	if sawIdentity {
		t.Fatal("empty token without explicit disabled-auth should not expose auth identity")
	}
	if sawLegacyRole == "admin" {
		t.Fatal("empty token without explicit disabled-auth should not expose legacy admin role")
	}
}

func TestTokenAuth_DisabledStateSeparatesExplicitEnv(t *testing.T) {
	cases := []struct {
		name             string
		token            string
		authDisabledEnv  string
		wantEnabled      bool
		wantAuthDisabled bool
	}{
		{
			name:             "empty token is not explicit disabled auth",
			token:            "",
			authDisabledEnv:  "",
			wantEnabled:      false,
			wantAuthDisabled: false,
		},
		{
			name:             "token with no disabled auth stays enabled",
			token:            "secret-token-xyz",
			authDisabledEnv:  "",
			wantEnabled:      true,
			wantAuthDisabled: false,
		},
		{
			name:             "true disables auth explicitly",
			token:            "secret-token-xyz",
			authDisabledEnv:  "true",
			wantEnabled:      false,
			wantAuthDisabled: true,
		},
		{
			name:             "one disables auth explicitly",
			token:            "secret-token-xyz",
			authDisabledEnv:  "1",
			wantEnabled:      false,
			wantAuthDisabled: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_AUTH_DISABLED", tc.authDisabledEnv)

			ta, err := NewTokenAuth(tc.token)
			if err != nil {
				t.Fatalf("NewTokenAuth: %v", err)
			}

			if ta.IsEnabled() != tc.wantEnabled {
				t.Fatalf("IsEnabled()=%v, want %v", ta.IsEnabled(), tc.wantEnabled)
			}
			if ta.authDisabled != tc.wantAuthDisabled {
				t.Fatalf("authDisabled=%v, want %v", ta.authDisabled, tc.wantAuthDisabled)
			}
			if isAuthDisabled() != tc.wantAuthDisabled {
				t.Fatalf("isAuthDisabled()=%v, want %v", isAuthDisabled(), tc.wantAuthDisabled)
			}
		})
	}
}

func TestTokenAuth_EnabledRequiresToken(t *testing.T) {
	ta, err := NewTokenAuth("secret-token-xyz")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}
	h := ta.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("no token rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("no token: status=%d, want 401", rec.Code)
		}
	})

	t.Run("correct X-Auth-Token accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
		req.Header.Set("X-Auth-Token", ta.Token())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("X-Auth-Token: status=%d, want 200", rec.Code)
		}
	})

	t.Run("correct Bearer token accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
		req.Header.Set("Authorization", "Bearer "+ta.Token())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("Bearer: status=%d, want 200", rec.Code)
		}
	})
}

func TestTokenAuth_ExemptPaths(t *testing.T) {
	ta, err := NewTokenAuth("secret-token-xyz")
	if err != nil {
		t.Fatalf("NewTokenAuth: %v", err)
	}
	h := ta.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	exemptPaths := []string{"/health", "/api/health", "/api/ready"}
	for _, path := range exemptPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			// No token provided
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("exempt path %s: status=%d, want 200", path, rec.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExpensiveOperationLimiter
// ---------------------------------------------------------------------------

func TestExpensiveOperationLimiter_FirstAllowed(t *testing.T) {
	lim := NewExpensiveOperationLimiter()
	if !lim.CanRebuild() {
		t.Error("first CanRebuild should return true")
	}
}

func TestExpensiveOperationLimiter_SecondBlocked(t *testing.T) {
	lim := NewExpensiveOperationLimiter()
	lim.CanRebuild() // consume first slot
	if lim.CanRebuild() {
		t.Error("immediate second CanRebuild should return false (within cooldown)")
	}
}

// ---------------------------------------------------------------------------
// RequestID
// ---------------------------------------------------------------------------

func TestRequestID_GeneratesID(t *testing.T) {
	var capturedID string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = GetRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID response header must be set")
	}
	if capturedID == "" {
		t.Error("request ID must be set in context")
	}
}

func TestRequestID_PropagatesExistingID(t *testing.T) {
	const clientID = "client-provided-req-id-abc"

	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
	req.Header.Set("X-Request-ID", clientID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != clientID {
		t.Errorf("X-Request-ID = %q, want %q", got, clientID)
	}
}

// ---------------------------------------------------------------------------
// RequireJSONContentType
// ---------------------------------------------------------------------------

func TestRequireJSONContentType_Table(t *testing.T) {
	h := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name        string
		method      string
		contentType string
		wantStatus  int
	}{
		{"GET no content-type", http.MethodGet, "", http.StatusOK},
		{"POST application/json", http.MethodPost, "application/json", http.StatusOK},
		{"POST with charset", http.MethodPost, "application/json; charset=utf-8", http.StatusOK},
		{"POST empty body no ct", http.MethodPost, "", http.StatusOK},
		{"POST text/plain", http.MethodPost, "text/plain", http.StatusUnsupportedMediaType},
		{"PUT application/xml", http.MethodPut, "application/xml", http.StatusUnsupportedMediaType},
		{"PATCH form-urlencoded", http.MethodPatch, "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/memory", nil)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("%s %s: status=%d, want %d", tc.method, tc.contentType, rec.Code, tc.wantStatus)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateProjectName
// ---------------------------------------------------------------------------

func TestValidateProjectName_Table(t *testing.T) {
	cases := []struct {
		name      string
		project   string
		wantError bool
	}{
		{"empty allowed", "", false},
		{"simple name", "engram-project", false},
		{"with slash", "org/repo-name", false},
		{"underscore", "my_project_v3", false},
		{"dots", "a.b.c", false},
		// attacks
		{"path traversal relative", "../../../etc/shadow", true},
		{"hidden traversal", "repo/../../secret", true},
		{"semicolon injection", "proj; rm -rf /", true},
		{"backtick injection", "proj`id`", true},
		{"dollar sign", "proj$PATH", true},
		{"too long", string(make([]byte, 501)), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProjectName(tc.project)
			if tc.wantError && err == nil {
				t.Errorf("expected error for %q, got nil", tc.project)
			}
			if !tc.wantError && err != nil {
				t.Errorf("unexpected error for %q: %v", tc.project, err)
			}
		})
	}
}
