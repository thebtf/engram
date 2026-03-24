// Package worker provides the main worker service for engram.
package worker

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
)

// requestIDKey is the context key for request IDs.
type requestIDKey struct{}

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

// SecurityHeaders middleware adds essential security headers to all responses.
// These protect against common web vulnerabilities.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")

		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Enable XSS filter
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// Restrict referrer information
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Content Security Policy - granular directives
		// TODO: Remove 'unsafe-inline' from style-src and migrate inline styles to nonce/hash-based CSP.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'")

		// Permissions Policy - disable unnecessary features
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		// CORS: Use exact match whitelist to prevent bypass attacks
		origin := r.Header.Get("Origin")
		if allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Auth-Token, Authorization, X-Request-ID")
		}

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// MaxBodySize middleware limits the size of incoming request bodies.
// This prevents denial of service attacks via large payloads.
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
	"/api/context/search":         true,
	"/api/context/inject":         true,
	"/api/decisions/search":       true,
	"/api/analytics/search-misses": true,
}

// TokenAuth provides token-based authentication for the worker HTTP API.
// Supports three auth methods:
//  1. Master token (ENGRAM_API_TOKEN env var) via X-Auth-Token or Authorization: Bearer header -> admin
//  2. Client API tokens (engram_* prefix, bcrypt-hashed in DB) via same headers -> scoped access
//  3. HMAC-signed session cookie (engram_session) -> admin (dashboard)
type TokenAuth struct {
	ExemptPaths map[string]bool
	token       string
	cookieKey   []byte
	tokenStore  *gormdb.TokenStore
	statsCh     chan string // buffered channel for async stats increment
	mu          sync.RWMutex
	enabled     bool
}

// NewTokenAuth creates a new TokenAuth using a provided token.
// If token is empty and ENGRAM_AUTH_DISABLED is set, authentication is skipped.
// Otherwise, authentication will be enforced at startup (see Service.Start).
func NewTokenAuth(token string) (*TokenAuth, error) {
	authDisabled := strings.EqualFold(strings.TrimSpace(os.Getenv("ENGRAM_AUTH_DISABLED")), "true")

	// Derive HMAC cookie key from master token using SHA-256 (deterministic, no extra config).
	var cookieKey []byte
	if token != "" {
		h := sha256.Sum256([]byte(token))
		cookieKey = h[:]
	}

	ta := &TokenAuth{
		enabled:   token != "" && !authDisabled,
		token:     token,
		cookieKey: cookieKey,
		statsCh:   make(chan string, 256),
		ExemptPaths: map[string]bool{
			"/":                true, // SPA index.html (dashboard handles auth client-side)
			"/health":          true,
			"/api/health":      true,
			"/api/ready":       true,
			"/api/version":     true,
			"/api/auth/login":  true,
			"/api/auth/logout": true,
			"/api/auth/me":     true, // Must be accessible to check auth status (returns 401 if not authed)
		},
	}

	if token == "" && !authDisabled {
		log.Warn().Msg("auth: ENGRAM_API_TOKEN not set and ENGRAM_AUTH_DISABLED not set — authentication will be enforced at startup")
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

// StatsCh returns the buffered channel for async token stats increment.
func (ta *TokenAuth) StatsCh() chan string {
	return ta.statsCh
}

// Middleware returns HTTP middleware that enforces token authentication.
// Auth priority: header token (master or client) > session cookie > 401.
func (ta *TokenAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ta.mu.RLock()
		enabled := ta.enabled
		masterToken := ta.token
		exempt := ta.ExemptPaths[r.URL.Path]
		cookieKey := ta.cookieKey
		store := ta.tokenStore
		ta.mu.RUnlock()

		// Skip auth if disabled or path is exempt
		if !enabled || exempt {
			next.ServeHTTP(w, r)
			return
		}

		// Also exempt static assets and docs
		if strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasPrefix(r.URL.Path, "/api/docs") {
			next.ServeHTTP(w, r)
			return
		}

		// 1. Check for token in header
		providedToken := r.Header.Get("X-Auth-Token")
		if providedToken == "" {
			auth := r.Header.Get("Authorization")
			if bearer, found := strings.CutPrefix(auth, "Bearer "); found {
				providedToken = bearer
			}
		}

		if providedToken != "" {
			// 1a. Check if it matches the master token -> admin
			if subtle.ConstantTimeCompare([]byte(providedToken), []byte(masterToken)) == 1 {
				ctx := context.WithValue(r.Context(), authRoleKey{}, "admin")
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// 1b. Check if it's a client token (eng_* prefix)
			if strings.HasPrefix(providedToken, "engram_") && store != nil {
				if ta.authenticateClientToken(w, r, next, providedToken, store) {
					return
				}
				// authenticateClientToken wrote the error response if it failed
				return
			}

			// Token provided but doesn't match anything
			log.Warn().Str("path", r.URL.Path).Str("remote_addr", r.RemoteAddr).Msg("auth: rejected request with invalid token")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Check session cookie
		if cookieKey != nil {
			cookie, err := r.Cookie(sessionCookieName)
			if err == nil && cookie.Value != "" {
				if ta.authenticateSessionCookie(cookie.Value, cookieKey) {
					ctx := context.WithValue(r.Context(), authRoleKey{}, "admin")
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
		}

		// 3. No valid auth
		log.Warn().Str("path", r.URL.Path).Str("remote_addr", r.RemoteAddr).Msg("auth: rejected unauthenticated request")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// authenticateClientToken validates a client API token and enforces scope.
// Returns true if the request was handled (either forwarded or rejected).
func (ta *TokenAuth) authenticateClientToken(w http.ResponseWriter, r *http.Request, next http.Handler, rawToken string, store *gormdb.TokenStore) bool {
	// Extract prefix: chars 7-15 (first 8 hex chars after "engram_")
	if len(rawToken) < 15 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	}
	prefix := rawToken[7:15]

	candidates, err := store.FindByPrefix(r.Context(), prefix)
	if err != nil {
		log.Error().Err(err).Msg("auth: token store lookup failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return true
	}
	if len(candidates) == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	}

	// Find the matching token by bcrypt comparison (handles prefix collisions).
	var token *gormdb.APIToken
	for i := range candidates {
		if bcrypt.CompareHashAndPassword([]byte(candidates[i].TokenHash), []byte(rawToken)) == nil {
			token = &candidates[i]
			break
		}
	}
	if token == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	}

	// Check revoked
	if token.Revoked {
		http.Error(w, "token revoked", http.StatusUnauthorized)
		return true
	}

	// Enforce read-only scope
	if token.Scope == "read-only" {
		method := r.Method
		if method == "POST" || method == "PUT" || method == "DELETE" || method == "PATCH" {
			if !readOnlyAllowedPosts[r.URL.Path] {
				http.Error(w, "forbidden: read-only token", http.StatusForbidden)
				return true
			}
		}
	}

	// Increment stats asynchronously
	select {
	case ta.statsCh <- token.ID:
	default:
		// Channel full — skip stats update rather than block auth
	}

	ctx := context.WithValue(r.Context(), authRoleKey{}, token.Scope)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
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

// ExpensiveOperationLimiter provides stricter rate limiting for expensive operations.
// It wraps the base per-client rate limiter with additional per-operation limits.
type ExpensiveOperationLimiter struct {
	// Track last execution time per operation type
	lastRebuild     int64 // Unix timestamp
	rebuildCooldown int64 // Minimum seconds between rebuilds

	mu sync.Mutex
}

// NewExpensiveOperationLimiter creates a limiter for expensive operations.
func NewExpensiveOperationLimiter() *ExpensiveOperationLimiter {
	return &ExpensiveOperationLimiter{
		rebuildCooldown: 300, // 5 minutes between rebuilds
	}
}

// CanRebuild checks if a vector rebuild operation is allowed.
// Returns false if a rebuild was triggered too recently.
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

// unixNow returns current Unix timestamp.
// Separated for easier testing.
func unixNow() int64 {
	return time.Now().Unix()
}

// RequestID middleware adds a unique request ID to each request.
// The ID is added to the context and response headers for tracing.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check for existing request ID from client
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			// Generate new request ID
			idBytes := make([]byte, 8)
			if _, err := rand.Read(idBytes); err == nil {
				requestID = hex.EncodeToString(idBytes)
			} else {
				requestID = fmt.Sprintf("%d", time.Now().UnixNano())
			}
		}

		// Add to response header
		w.Header().Set("X-Request-ID", requestID)

		// Add to context
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
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

// RequireJSONContentType middleware validates that POST/PUT/PATCH requests
// have application/json Content-Type header.
func RequireJSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only check for methods that typically have bodies
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
			ct := r.Header.Get("Content-Type")
			// Allow empty Content-Type for requests without body
			if ct != "" && !strings.HasPrefix(ct, "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ValidateProjectName checks if a project name is safe to use.
// Returns an error if the name contains path traversal or invalid characters.
func ValidateProjectName(project string) error {
	if project == "" {
		return nil // Empty is allowed (means no filter)
	}

	// Check for path traversal
	if strings.Contains(project, "..") {
		return fmt.Errorf("invalid project name: path traversal detected")
	}

	// Check for valid characters
	if !projectNamePattern.MatchString(project) {
		return fmt.Errorf("invalid project name: only alphanumeric, underscore, dash, dot, and slash allowed")
	}

	// Max length check
	if len(project) > 500 {
		return fmt.Errorf("project name too long (max 500 chars)")
	}

	return nil
}

