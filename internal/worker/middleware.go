// Package worker provides the main worker service for engram.
package worker

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
	authpkg "github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// requestIDKey is the context key for request IDs.
type requestIDKey struct{}

// emptyTokenStore satisfies auth.TokenStoreReader with an always-empty
// candidate set. Used as the bootstrap reader for the validator until
// SetValidator() swaps in the DB-backed *gormdb.TokenStore.
type emptyTokenStore struct{}

func (emptyTokenStore) FindByPrefix(_ context.Context, _ string) ([]gormdb.APIToken, error) {
	return nil, nil
}

// projectNamePattern validates project names to prevent path traversal.
var projectNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.\\/:-]+$`)

// allowedOrigins is the whitelist of origins allowed for CORS.
// Uses exact matching to prevent bypass attacks like "evil-localhost.com".
var allowedOrigins = map[string]bool{
	"http://localhost":       true,
	"http://localhost:3000":  true,
	"http://localhost:5173":  true, // Vite dev server
	"http://localhost:37777": true, // Worker dashboard
	"http://127.0.0.1":       true,
	"http://127.0.0.1:3000":  true,
	"http://127.0.0.1:5173":  true,
	"http://127.0.0.1:37777": true,
}

const strictContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'"

var inlineScriptPattern = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)

// SecurityHeaders sets defensive HTTP headers on every response.
// Mitigates clickjacking, MIME-sniffing, XSS, and cross-origin data leaks.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()

		// Anti-clickjacking: disallow all framing.
		hdr.Set("X-Frame-Options", "DENY")

		// MIME-type sniffing: force the declared content-type.
		hdr.Set("X-Content-Type-Options", "nosniff")

		// Legacy XSS filter for older browsers.
		hdr.Set("X-XSS-Protection", "1; mode=block")

		// Referrer leakage: send origin only to same-origin destinations.
		hdr.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Content Security Policy — granular per-source directives.
		// TODO: Remove 'unsafe-inline' from style-src and migrate inline styles to nonce/hash-based CSP.
		hdr.Set("Content-Security-Policy", strictContentSecurityPolicy)

		// Permissions Policy — deny access to hardware APIs.
		hdr.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		// CORS — exact-match allowlist prevents suffix-bypass attacks such as "evil-localhost.com".
		if origin := r.Header.Get("Origin"); allowedOrigins[origin] {
			hdr.Set("Access-Control-Allow-Origin", origin)
			hdr.Set("Access-Control-Allow-Credentials", "true")
			hdr.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			hdr.Set("Access-Control-Allow-Headers", "Content-Type, X-Auth-Token, Authorization, X-Request-ID")
		}

		// Preflight requests terminate here; no further processing needed.
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func setOperatorConsoleHTMLSecurityHeaders(hdr http.Header, html []byte) {
	scriptSrc := "script-src 'self'"
	if hashSources := inlineScriptHashSources(html); len(hashSources) > 0 {
		scriptSrc += " " + strings.Join(hashSources, " ")
	}

	hdr.Set("Content-Security-Policy", "default-src 'self'; "+scriptSrc+"; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'")
}

func inlineScriptHashSources(html []byte) []string {
	matches := inlineScriptPattern.FindAllSubmatch(html, -1)
	hashSources := make([]string, 0, len(matches))
	for _, match := range matches {
		attrs := strings.ToLower(string(match[1]))
		content := match[2]
		if strings.Contains(attrs, "src=") || len(content) == 0 {
			continue
		}

		sum := sha256.Sum256(content)
		hashSources = append(hashSources, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}

	return hashSources
}

// MaxBodySize guards against denial-of-service via oversized request bodies.
// Requests whose declared Content-Length exceeds maxBytes are rejected before
// the body is read; all others are wrapped with http.MaxBytesReader to enforce
// the limit during streaming reads.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// readOnlyAllowedPosts is the set of POST endpoints that read-only client tokens may call.
// These are search/analytics endpoints that use POST for request bodies but do not mutate state.
var readOnlyAllowedPosts = map[string]bool{
	"/api/context/search":          true,
	"/api/context/inject":          true,
	"/api/decisions/search":        true,
	"/api/analytics/search-misses": true,
}

func parseAuthDisabledFlag(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1":
		return true
	default:
		return false
	}
}

func authDisabledFromEnv() bool {
	return parseAuthDisabledFlag(os.Getenv("ENGRAM_AUTH_DISABLED"))
}

// TokenAuth provides token-based authentication for the worker HTTP API.
// Supports five auth methods:
//  1. Master operator key (ENGRAM_AUTH_ADMIN_TOKEN env var) via X-Auth-Token or Authorization: Bearer header -> admin (Source=master)
//  2. Client API tokens / worker keycards (engram_* prefix, bcrypt-hashed in DB) via same headers -> scoped access (Source=client)
//  3. HMAC-signed session cookie (engram_session) -> admin (Source=session)
//  4. DB-backed auth session cookie (engram_auth) -> role from users table (Source=session)
//  5. Authentik forward-auth header (X-Authentik-Email) from trusted proxy -> role from users table (Source=session)
//
// Methods 1 + 2 delegate to the shared *auth.Validator (FR-2 / Plan ADR-002),
// the same validator that backs gRPC. Methods 3-5 are HTTP-specific and
// remain inline. Issuance endpoints (handlers_auth.handleCreateToken et al.)
// gate on Source == "session" via auth.Identity.IsSessionAdmin (FR-6 / C4).
type TokenAuth struct {
	ExemptPaths             map[string]bool
	token                   string // master operator key (still cached for legacy session-cookie paths)
	cookieKey               []byte
	validator               *authpkg.Validator // delegate for methods 1 + 2
	tokenStore              *gormdb.TokenStore
	authSessionStore        *gormdb.AuthSessionStore
	userStore               *gormdb.UserStore
	statsCh                 chan string // buffered channel for async token stats increment
	mu                      sync.RWMutex
	enabled                 bool
	authDisabled            bool
	authentikEnabled        bool
	authentikAutoProvision  bool
	authentikTrustedProxies []string
}

// NewTokenAuth creates a new TokenAuth using a provided token.
// If token is empty and ENGRAM_AUTH_DISABLED is set, authentication is skipped.
// Otherwise, authentication will be enforced at startup (see Service.Start).
func NewTokenAuth(token string) (*TokenAuth, error) {
	authDisabled := authDisabledFromEnv()

	// Derive HMAC cookie key from master token using SHA-256 (deterministic, no extra config).
	var cookieKey []byte
	if token != "" {
		h := sha256.Sum256([]byte(token))
		cookieKey = h[:]
	}

	ta := &TokenAuth{
		enabled:      token != "" && !authDisabled,
		authDisabled: authDisabled,
		token:        token,
		cookieKey:    cookieKey,
		// Bootstrap validator handles master-token bearer requests during the
		// initialisation window before the DB-backed TokenStore is wired in.
		// SetValidator replaces this with the full two-tier validator once
		// initializeAsync completes. emptyTokenStore returns no client
		// candidates, so worker keycards are temporarily unauthenticated until
		// the swap — acceptable because bootstrap is sub-second and only
		// service.go ever calls Middleware before SetValidator.
		validator: authpkg.NewValidator(token, emptyTokenStore{}),
		statsCh:   make(chan string, 256),
		ExemptPaths: map[string]bool{
			"/":                      true, // SPA index.html (dashboard handles auth client-side)
			"/health":                true,
			"/api/health":            true,
			"/api/ready":             true,
			"/api/version":           true,
			"/api/auth/login":        true,
			"/api/auth/logout":       true,
			"/api/auth/me":           true, // Must be accessible to check auth status (returns 401 if not authed)
			"/api/auth/setup-needed": true,
			"/api/auth/setup":        true,
			"/api/auth/user-login":   true,
			"/api/auth/register":     true,
		},
	}

	if token == "" && !authDisabled {
		log.Warn().Msg("auth: ENGRAM_AUTH_ADMIN_TOKEN not set and ENGRAM_AUTH_DISABLED not set — authentication will be enforced at startup")
	}

	return ta, nil
}

// Token returns the authentication token.
// Returns empty string if authentication is disabled.
func (ta *TokenAuth) Token() string {
	ta.mu.RLock()
	defer ta.mu.RUnlock()
	return ta.token
}

// CookieKey returns the HMAC key used for signing session cookies.
func (ta *TokenAuth) CookieKey() []byte {
	ta.mu.RLock()
	defer ta.mu.RUnlock()
	return ta.cookieKey
}

// IsEnabled returns whether token authentication is enabled.
func (ta *TokenAuth) IsEnabled() bool {
	ta.mu.RLock()
	defer ta.mu.RUnlock()
	return ta.enabled
}

// SetTokenStore sets the token store for client token lookups.
// Called after DB initialization completes.
func (ta *TokenAuth) SetTokenStore(store *gormdb.TokenStore) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	ta.tokenStore = store
}

// SetValidator wires the shared auth.Validator into the middleware. Called
// once after DB and tokenStore are ready (mirrors SetTokenStore lifecycle).
// When validator is nil, the middleware falls back to its legacy inline
// token compare paths — used only by tests that exercise auth-disabled
// deployments and by the bootstrap window before initializeAsync completes.
func (ta *TokenAuth) SetValidator(v *authpkg.Validator) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	ta.validator = v
}

// SetAuthStores injects the user and auth-session stores used for
// email/password session cookie validation (engram_auth cookie).
// Called after DB initialization completes.
func (ta *TokenAuth) SetAuthStores(userStore *gormdb.UserStore, authSessionStore *gormdb.AuthSessionStore) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	ta.userStore = userStore
	ta.authSessionStore = authSessionStore
}

// SetAuthentikConfig configures Authentik forward-auth integration.
// When enabled, requests with X-Authentik-Email header from a trusted proxy
// are automatically authenticated (and optionally provisioned).
func (ta *TokenAuth) SetAuthentikConfig(enabled, autoProvision bool, trustedProxies []string) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	ta.authentikEnabled = enabled
	ta.authentikAutoProvision = autoProvision
	ta.authentikTrustedProxies = trustedProxies
}

// isTrustedProxy checks whether the request originated from a trusted proxy IP.
// Returns false if no trusted proxies are configured (deny-by-default).
func isTrustedProxy(r *http.Request, trustedProxies []string) bool {
	if len(trustedProxies) == 0 {
		return false // No trusted proxies = don't trust any
	}
	remoteIP := strings.Split(r.RemoteAddr, ":")[0]
	for _, trusted := range trustedProxies {
		if remoteIP == trusted {
			return true
		}
	}
	return false
}

// StatsCh returns the buffered channel for async token stats increment.
func (ta *TokenAuth) StatsCh() chan string {
	return ta.statsCh
}

// Middleware returns HTTP middleware that enforces token authentication.
// Auth priority: header bearer (validator: master OR client keycard) >
// HMAC session cookie > DB auth session cookie > Authentik forward-auth > 401.
func (ta *TokenAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ta.mu.RLock()
		enabled := ta.enabled
		authDisabled := ta.authDisabled
		exempt := ta.ExemptPaths[r.URL.Path]
		cookieKey := ta.cookieKey
		validator := ta.validator
		authSessStore := ta.authSessionStore
		uStore := ta.userStore
		authentikEnabled := ta.authentikEnabled
		authentikAutoProvision := ta.authentikAutoProvision
		authentikTrustedProxies := ta.authentikTrustedProxies
		ta.mu.RUnlock()

		// Explicit disabled-auth mode behaves as a synthetic browser admin
		// session so downstream dashboard handlers see one coherent access map.
		if authDisabled {
			next.ServeHTTP(w, r.WithContext(buildAuthCtx(r.Context(), authpkg.Session("admin"))))
			return
		}

		// Skip auth if not configured or path is exempt.
		if !enabled || exempt {
			next.ServeHTTP(w, r)
			return
		}

		// Also exempt static assets, branding assets, and docs.
		if strings.HasPrefix(r.URL.Path, "/assets/") ||
			strings.HasPrefix(r.URL.Path, "/branding/") ||
			r.URL.Path == "/favicon.svg" ||
			strings.HasPrefix(r.URL.Path, "/api/docs") {
			next.ServeHTTP(w, r)
			return
		}

		// 1. Bearer / X-Auth-Token / SSE-query token — delegate to validator.
		providedToken := extractHTTPBearer(r)
		if providedToken != "" {
			if validator == nil {
				// Bootstrap window before initializeAsync wires the validator.
				// Reject defensively — better than silently allowing.
				log.Warn().Str("path", r.URL.Path).Msg("auth: validator not yet wired, rejecting bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			id, err := validator.Validate(r.Context(), providedToken)
			if err != nil {
				switch {
				case errors.Is(err, authpkg.ErrEmptyToken),
					errors.Is(err, authpkg.ErrInvalidCredentials),
					errors.Is(err, authpkg.ErrRevoked):
					// Auth-class errors — bearer was syntactically present
					// but failed validation. 401 is correct.
					log.Warn().
						Str("path", r.URL.Path).
						Str("remote_addr", r.RemoteAddr).
						Err(err).
						Msg("auth: bearer rejected")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
				default:
					// Wrapped store / bcrypt error — auth machinery itself
					// is broken. Surface as 500 so monitoring distinguishes
					// "rejected credential" from "auth subsystem down" (the
					// gRPC interceptor maps the same case to codes.Internal).
					log.Error().
						Str("path", r.URL.Path).
						Str("remote_addr", r.RemoteAddr).
						Err(err).
						Msg("auth: store/bcrypt failure")
					http.Error(w, "auth store unavailable", http.StatusInternalServerError)
				}
				return
			}

			// Read-only scope gate (FR-6 inheriting v5 behaviour). Applies
			// to client keycards only — operator key is always admin.
			if id.Source == authpkg.SourceClient && id.Role == authpkg.RoleReadOnly {
				if isMutatingMethod(r.Method) && !readOnlyAllowedPosts[r.URL.Path] {
					http.Error(w, "forbidden: read-only token", http.StatusForbidden)
					return
				}
			}

			// Stats increment for client keycards only.
			if id.Source == authpkg.SourceClient && id.KeycardID != "" {
				select {
				case ta.statsCh <- id.KeycardID:
				default:
					// Channel full — skip stats update rather than block auth.
				}
			}

			next.ServeHTTP(w, r.WithContext(buildAuthCtx(r.Context(), id)))
			return
		}

		// 2. HMAC-signed session cookie (master-token login → admin).
		if cookieKey != nil {
			cookie, err := r.Cookie(sessionCookieName)
			if err == nil && cookie.Value != "" {
				if ta.authenticateSessionCookie(cookie.Value, cookieKey) {
					id := authpkg.Session("admin")
					next.ServeHTTP(w, r.WithContext(buildAuthCtx(r.Context(), id)))
					return
				}
			}
		}

		// 3. DB-backed auth session cookie (email/password login).
		if authSessStore != nil && uStore != nil {
			if authCookie, err := r.Cookie("engram_auth"); err == nil && authCookie.Value != "" {
				if sess, err := authSessStore.GetSession(authCookie.Value); err == nil {
					if user, err := uStore.GetUserByID(sess.UserID); err == nil && !user.Disabled {
						id := authpkg.Session(user.Role)
						next.ServeHTTP(w, r.WithContext(buildAuthCtx(r.Context(), id)))
						return
					}
				}
			}
		}

		// 4. Authentik forward-auth header.
		if authentikEnabled && uStore != nil {
			authentikEmail := r.Header.Get("X-Authentik-Email")
			if authentikEmail != "" && isTrustedProxy(r, authentikTrustedProxies) {
				user, err := uStore.GetUserByEmail(authentikEmail)
				if err != nil && authentikAutoProvision {
					// Autoprovisioned users receive the "operator" role —
					// the regular non-admin role. This is intentional:
					// auto-creating SSO callers as admin would mean any
					// user reaching the SSO endpoint receives keycard
					// issuance privileges. Existing admins can promote
					// the new user via PATCH /api/users/{id}/role. v6
					// IsSessionAdmin (admin role only) preserves this
					// boundary; v5 issuance gated on master-token bearer
					// alone, so "operator" session users could never
					// issue keycards either — no regression.
					user, err = uStore.CreateUser(authentikEmail, "", "operator")
				}
				if err == nil && user != nil && !user.Disabled {
					id := authpkg.Session(user.Role)
					next.ServeHTTP(w, r.WithContext(buildAuthCtx(r.Context(), id)))
					return
				}
			}
		}

		// 5. No valid auth.
		log.Warn().Str("path", r.URL.Path).Str("remote_addr", r.RemoteAddr).Msg("auth: rejected unauthenticated request")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// extractHTTPBearer pulls the bearer string from (1) X-Auth-Token header,
// (2) Authorization: Bearer header, (3) ?token= query for SSE endpoints.
// Empty string when no bearer is present.
func extractHTTPBearer(r *http.Request) string {
	if v := r.Header.Get("X-Auth-Token"); v != "" {
		return v
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		if bearer, found := strings.CutPrefix(auth, "Bearer "); found {
			return bearer
		}
	}
	path := r.URL.Path
	if path == "/api/events" || path == "/sse" || strings.HasPrefix(path, "/api/logs") {
		return r.URL.Query().Get("token")
	}
	return ""
}

// mutatingMethods is the set of HTTP verbs that change server state. Used to
// gate read-only keycards (FR-6 inheriting v5 behaviour).
var mutatingMethods = []string{"POST", "PUT", "DELETE", "PATCH"}

// isMutatingMethod returns true for HTTP verbs in mutatingMethods.
func isMutatingMethod(method string) bool {
	return slices.Contains(mutatingMethods, method)
}

// buildAuthCtx attaches the resolved auth.Identity to ctx under both the new
// auth.IdentityKey AND the legacy worker.authRoleKey{} (for handlers that have
// not yet been migrated to read auth.RoleFrom).
func buildAuthCtx(ctx context.Context, id authpkg.Identity) context.Context {
	ctx = authpkg.WithIdentity(ctx, id)
	ctx = context.WithValue(ctx, authRoleKey{}, string(id.Role))
	return ctx
}

// authenticateSessionCookie validates an HMAC-signed session cookie.
func (ta *TokenAuth) authenticateSessionCookie(cookieValue string, key []byte) bool {
	// Cookie format: base64url(payload).base64url(hmac)
	parts := strings.SplitN(cookieValue, ".", 2)
	if len(parts) != 2 {
		return false
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	// Verify HMAC
	expected := computeHMAC(payloadBytes, key)
	if !hmac.Equal(sigBytes, expected) {
		return false
	}

	// Check expiration
	var payload sessionPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return false
	}

	if time.Now().Unix() > payload.Exp {
		return false
	}

	return true
}

// ExpensiveOperationLimiter enforces cooldown periods between heavyweight operations.
// Prevents runaway rebuild loops by recording the last execution timestamp and
// refusing new executions until the cooldown window elapses.
type ExpensiveOperationLimiter struct {
	// lastRebuild holds the Unix timestamp of the most recent allowed rebuild.
	lastRebuild int64
	// rebuildCooldown is the minimum gap in seconds between successive rebuilds.
	rebuildCooldown int64
	mu              sync.Mutex
}

// NewExpensiveOperationLimiter returns a limiter with a 5-minute rebuild cooldown.
func NewExpensiveOperationLimiter() *ExpensiveOperationLimiter {
	return &ExpensiveOperationLimiter{
		rebuildCooldown: 300, // 5 minutes between rebuilds
	}
}

// CanRebuild atomically checks whether a rebuild is permitted and, if so,
// records the current time as the new last-rebuild timestamp.
// Returns false when the cooldown window has not yet elapsed.
func (eol *ExpensiveOperationLimiter) CanRebuild() bool {
	eol.mu.Lock()
	defer eol.mu.Unlock()

	now := unixNow()
	if now-eol.lastRebuild < eol.rebuildCooldown {
		return false
	}
	eol.lastRebuild = now
	return true
}

// unixNow returns the current wall-clock time as a Unix timestamp.
// Isolated into its own function to allow deterministic substitution in tests.
func unixNow() int64 {
	return time.Now().Unix()
}

// RequestID propagates a request correlation ID through the call chain.
// Reuses any X-Request-ID the client sent; otherwise generates a fresh 8-byte
// random hex ID. The ID is attached to both the response header and the
// context so downstream handlers can include it in structured log fields.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			buf := make([]byte, 8)
			if _, err := rand.Read(buf); err == nil {
				id = hex.EncodeToString(buf)
			} else {
				// Fall back to nanosecond timestamp when crypto/rand fails.
				id = fmt.Sprintf("%d", time.Now().UnixNano())
			}
		}

		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestID retrieves the request ID from the context.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// debugRequestLogger emits structured HTTP access logs at DEBUG level using zerolog.
// Chi's built-in middleware.Logger uses the stdlib log package at INFO level, which
// is too noisy for production deployments. Use Service.requestActivityMiddleware
// instead when the per-service idle-gate timestamp also needs updating.
func debugRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		log.Debug().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", ww.Status()).
			Int("bytes", ww.BytesWritten()).
			Dur("duration", time.Since(started)).
			Str("from", r.RemoteAddr).
			Msg("HTTP request")
	})
}

// activityExemptPaths lists HTTP paths that must not update the sleep-cycle idle
// gate. Health probes, readiness checks, and version endpoints are polled by load
// balancers and container runtimes at regular intervals; treating them as user
// activity would prevent the idle gate from ever firing in a normal deployment.
var activityExemptPaths = map[string]bool{
	"/health":      true,
	"/api/health":  true,
	"/api/ready":   true,
	"/api/version": true,
}

// requestActivityMiddleware returns an HTTP middleware that stamps s.lastRequestAt
// with the current Unix nanosecond timestamp on genuine API/MCP requests. Health,
// readiness, and version endpoints are excluded so that monitoring probes do not
// prevent the sleep cycle's idle gate from firing (T014 AC: ">=4h since last active
// session").
func (s *Service) requestActivityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !activityExemptPaths[r.URL.Path] {
			s.lastRequestAt.Store(time.Now().UnixNano())
		}
		next.ServeHTTP(w, r)
	})
}

// RequireJSONContentType rejects body-bearing requests (POST/PUT/PATCH) whose
// Content-Type is not application/json. An empty Content-Type is allowed to
// accommodate body-less usages of these methods.
func RequireJSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
			ct := r.Header.Get("Content-Type")
			if ct != "" && !strings.HasPrefix(ct, "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ValidateProjectName verifies that project is safe for use as a storage key.
// An empty string is valid and signals "no project filter". Non-empty names
// are checked for path-traversal sequences, character set conformance, and
// maximum length.
func ValidateProjectName(project string) error {
	if project == "" {
		return nil
	}

	if strings.Contains(project, "..") {
		return fmt.Errorf("invalid project name: path traversal detected")
	}

	if !projectNamePattern.MatchString(project) {
		return fmt.Errorf("invalid project name: only alphanumeric, underscore, dash, dot, and slash allowed")
	}

	if len(project) > 500 {
		return fmt.Errorf("project name too long (max 500 chars)")
	}

	return nil
}
