// Package worker provides authentication HTTP handlers for the dashboard.
package worker

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	authpkg "github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// isAuthDisabled returns true when ENGRAM_AUTH_DISABLED enables disabled-auth mode.
func isAuthDisabled() bool {
	return authDisabledFromEnv()
}

// sessionPayload is the data stored inside the signed session cookie.
type sessionPayload struct {
	Role string `json:"role"`
	Exp  int64  `json:"exp"`
}

// sessionCookieName is the name of the session cookie.
const sessionCookieName = "engram_session"

// sessionMaxAge is the session cookie lifetime (30 days).
const sessionMaxAge = 30 * 24 * 3600

// Token-shape constants are owned by internal/auth (single source of truth)
// and re-exported here as package-local aliases so the issuance code stays
// readable. Editing the literal values is a contract change and must happen
// in internal/auth/validator.go, not here.
const (
	tokenRawPrefix = authpkg.TokenRawPrefix
	tokenPrefixLen = authpkg.TokenPrefixLen
)

// loginRequest is the JSON body for POST /api/auth/login.
type loginRequest struct {
	Token string `json:"token"`
}

// tokenCreateRequest is the JSON body for POST /api/auth/tokens.
type tokenCreateRequest struct {
	Name          string     `json:"name"`
	Scope         string     `json:"scope"`
	Principal     string     `json:"principal"`
	PrincipalKind string     `json:"principal_kind" enums:"human,agent,service"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// handleAuthLogin godoc
// @Summary Login with master token
// @Description Validates the master admin token and returns an HMAC-signed session cookie.
// @Tags Auth
// @Accept json
// @Produce json
// @Param body body loginRequest true "Login credentials"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 401 {string} string "unauthorized"
// @Router /api/auth/login [post]
func (s *Service) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	masterToken := s.tokenAuth.Token()
	if masterToken == "" {
		http.Error(w, "authentication not configured", http.StatusInternalServerError)
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(masterToken)) != 1 {
		log.Warn().Str("remote_addr", r.RemoteAddr).Msg("auth: failed login attempt")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Create signed session cookie
	cookieKey := s.tokenAuth.CookieKey()
	payload := sessionPayload{
		Role: "admin",
		Exp:  time.Now().Unix() + int64(sessionMaxAge),
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sig := computeHMAC(payloadBytes, cookieKey)
	cookieValue := base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    cookieValue,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})

	writeJSON(w, map[string]any{
		"authenticated": true,
		"role":          "admin",
	})
}

// requireLogoutOrigin blocks browser CSRF before either logout handler reads or revokes a session.
func (s *Service) requireLogoutOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site := r.Header.Values("Sec-Fetch-Site")
		if len(site) > 1 || (len(site) == 1 && site[0] != "same-origin") {
			writeAuthJSONError(w, http.StatusForbidden, "cross-origin logout denied")
			return
		}
		origins := r.Header.Values("Origin")
		if len(origins) > 1 {
			writeAuthJSONError(w, http.StatusForbidden, "cross-origin logout denied")
			return
		}
		if len(origins) == 1 {
			scheme, host := "http", r.Host
			if r.TLS != nil {
				scheme = "https"
			}
			if s.tokenAuth != nil {
				s.tokenAuth.mu.RLock()
				trusted := isTrustedProxy(r, s.tokenAuth.authentikTrustedProxies)
				s.tokenAuth.mu.RUnlock()
				cfg := config.Get()
				if !trusted && cfg.AuthTrustedProxy != "" {
					trusted = isTrustedProxy(r, []string{cfg.AuthTrustedProxy})
				}
				if trusted {
					if values := r.Header.Values("X-Forwarded-Proto"); len(values) == 1 && (values[0] == "http" || values[0] == "https") {
						scheme = values[0]
					} else if len(values) != 0 {
						writeAuthJSONError(w, http.StatusForbidden, "cross-origin logout denied")
						return
					}
					if values := r.Header.Values("X-Forwarded-Host"); len(values) == 1 && !strings.Contains(values[0], ",") {
						host = values[0]
					} else if len(values) != 0 {
						writeAuthJSONError(w, http.StatusForbidden, "cross-origin logout denied")
						return
					}
				}
			}
			if !sameLogoutOrigin(origins[0], scheme, host) {
				writeAuthJSONError(w, http.StatusForbidden, "cross-origin logout denied")
				return
			}
		} else if len(site) == 0 {
			// A browser can send a bodyless or simple form POST without Origin.
			// Only non-browser JSON requests without Fetch Metadata retain legacy logout.
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeAuthJSONError(w, http.StatusForbidden, "cross-origin logout denied")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameLogoutOrigin(origin, scheme, host string) bool {
	actual, err := url.Parse(origin)
	if err != nil || actual.User != nil || actual.Path != "" || actual.RawQuery != "" || actual.Fragment != "" || actual.Opaque != "" {
		return false
	}
	expected, err := url.Parse(scheme + "://" + host)
	if err != nil || expected.User != nil || expected.Path != "" || expected.RawQuery != "" || expected.Fragment != "" || expected.Hostname() == "" || actual.Scheme != scheme || !strings.EqualFold(actual.Hostname(), expected.Hostname()) {
		return false
	}
	actualPort, expectedPort := actual.Port(), expected.Port()
	if actualPort == "" {
		if scheme == "https" {
			actualPort = "443"
		} else {
			actualPort = "80"
		}
	}
	if expectedPort == "" {
		if scheme == "https" {
			expectedPort = "443"
		} else {
			expectedPort = "80"
		}
	}
	return actualPort == expectedPort
}

// handleAuthLogout godoc
// @Summary Logout
// @Description Revokes the DB-backed session (when present) and clears both browser session cookies.
// @Tags Auth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/auth/logout [post]
func (s *Service) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(authSessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		s.initMu.RLock()
		h := s.authHandlers
		s.initMu.RUnlock()
		if h == nil {
			writeAuthJSONError(w, http.StatusServiceUnavailable, "auth store unavailable")
			return
		}
		if !h.revokeBrowserSession(w, r) {
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	writeJSON(w, map[string]any{
		"authenticated": false,
	})
}

// handleAuthMe godoc
// @Summary Check authentication status
// @Description Returns the current authentication state and role.
// @Tags Auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 401 {string} string "unauthorized"
// @Router /api/auth/me [get]
func (s *Service) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	authDisabled := isAuthDisabled()
	if authDisabled {
		writeJSON(w, map[string]any{
			"authenticated": true,
			"role":          "admin",
			"auth_disabled": true,
			"source":        "auth-disabled",
			"auth_source":   "auth-disabled",
			"sso_active":    false,
			"synthetic":     true,
		})
		return
	}

	role := getAuthRole(r)
	if role != "" {
		response := map[string]any{
			"authenticated": true,
			"role":          role,
			"auth_disabled": false,
		}
		if id, ok := authpkg.IdentityFrom(r.Context()); ok {
			credentialSource := s.authMeSource(r, id)
			ssoActive := credentialSource == "authentik" || s.trustedAuthentikIngress(r)
			response["auth_source"] = credentialSource
			response["sso_active"] = ssoActive
			response["source"] = credentialSource
			if ssoActive && credentialSource != "authentik" && id.Source == authpkg.SourceSession {
				response["source"] = credentialSource + "+authentik"
			}
			if subject, ok := id.SessionBrowserSubject(); ok {
				if s.authHandlers == nil || s.authHandlers.users == nil {
					http.Error(w, "auth store unavailable", http.StatusInternalServerError)
					return
				}
				user, err := s.authHandlers.users.GetUserByID(subject.UserID)
				if err != nil || user.Disabled {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				response["user"] = map[string]any{"id": user.ID, "email": user.Email, "role": user.Role}
			}
		}
		writeJSON(w, response)
		return
	}

	// Not authenticated
	w.WriteHeader(http.StatusUnauthorized)
	writeJSON(w, map[string]any{
		"authenticated": false,
		"auth_disabled": authDisabled,
	})
}

// authMeSource identifies the credential selected by middleware, independent
// of other upstream sessions that may remain active after local logout.
func (s *Service) authMeSource(r *http.Request, id authpkg.Identity) string {
	if subject, ok := id.SessionBrowserSubject(); ok {
		if sessionID, ok := authenticatedBrowserSessionID(r.Context()); ok && sessionID == authentikBrowserSessionID(subject.UserID) {
			return "authentik"
		}
		return "local"
	}
	return string(id.Source)
}

func (s *Service) trustedAuthentikIngress(r *http.Request) bool {
	if s.tokenAuth == nil || r.Header.Get("X-Authentik-Email") == "" {
		return false
	}
	s.tokenAuth.mu.RLock()
	trusted := s.tokenAuth.authentikEnabled && isTrustedProxy(r, s.tokenAuth.authentikTrustedProxies)
	s.tokenAuth.mu.RUnlock()
	return trusted
}

// handleListTokens godoc
// @Summary List API tokens
// @Description Returns all API tokens (excluding hashes) for admin management.
// @Tags Auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Router /api/auth/tokens [get]
func (s *Service) handleListTokens(w http.ResponseWriter, r *http.Request) {
	if s.requireSessionAdmin(w, r) {
		return
	}

	s.initMu.RLock()
	tokenStore := s.tokenStore
	s.initMu.RUnlock()

	if tokenStore == nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}

	tokens, err := tokenStore.List(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list tokens")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Exclude token_hash from response
	type tokenResponse struct {
		ID            string     `json:"id"`
		Name          string     `json:"name"`
		TokenPrefix   string     `json:"token_prefix"`
		Scope         string     `json:"scope"`
		Principal     string     `json:"principal"`
		PrincipalKind string     `json:"principal_kind"`
		ExpiresAt     *time.Time `json:"expires_at,omitempty"`
		CreatedAt     time.Time  `json:"created_at"`
		LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
		RequestCount  int64      `json:"request_count"`
		ErrorCount    int64      `json:"error_count"`
		Revoked       bool       `json:"revoked"`
		RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	}

	resp := make([]tokenResponse, len(tokens))
	for i, t := range tokens {
		resp[i] = tokenResponse{
			ID:            t.ID,
			Name:          t.Name,
			TokenPrefix:   t.TokenPrefix,
			Scope:         t.Scope,
			Principal:     t.Principal,
			PrincipalKind: t.PrincipalKind,
			ExpiresAt:     t.ExpiresAt,
			CreatedAt:     t.CreatedAt,
			LastUsedAt:    t.LastUsedAt,
			RequestCount:  t.RequestCount,
			ErrorCount:    t.ErrorCount,
			Revoked:       t.Revoked,
			RevokedAt:     t.RevokedAt,
		}
	}

	writeJSON(w, map[string]any{
		"tokens": resp,
	})
}

// requireSessionAdmin enforces FR-6 / Clarification C4: keycard issuance and
// management endpoints accept ONLY browser-session admins. Bearer-authenticated
// callers — operator key OR worker keycard — are rejected with 403 even when
// they would otherwise carry admin role.
//
// Returns true when the caller is rejected (and the response has been written);
// the handler should return immediately. Returns false when the caller passes
// the gate.
func (s *Service) requireSessionAdmin(w http.ResponseWriter, r *http.Request) bool {
	id, ok := authpkg.IdentityFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	}
	if !id.IsSessionAdmin() {
		// JSON body — set Content-Type explicitly. http.Error would emit
		// text/plain even with a JSON-shaped string.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "issuance requires browser session — log in to the dashboard at /login",
		})
		return true
	}
	return false
}

// handleCreateToken godoc
// @Summary Create a new API token
// @Description Generates a new client API token with the specified name and scope. The raw token is returned only once.
// @Tags Auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body tokenCreateRequest true "Token creation parameters"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 403 {string} string "forbidden — issuance requires browser session"
// @Failure 409 {string} string "conflict"
// @Failure 500 {string} string "internal error"
// @Router /api/auth/tokens [post]
func (s *Service) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if s.requireSessionAdmin(w, r) {
		return
	}

	s.initMu.RLock()
	tokenStore := s.tokenStore
	s.initMu.RUnlock()

	if tokenStore == nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}

	var req tokenCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	scope := req.Scope
	if scope == "" {
		scope = "read-write"
	}
	if scope != "read-write" && scope != "read-only" {
		http.Error(w, "scope must be 'read-write' or 'read-only'", http.StatusBadRequest)
		return
	}

	principal, principalKind, err := normalizeTokenPrincipal(req.Principal, req.PrincipalKind)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if err := authpkg.ValidateHAPPrincipalForIssuance(principal, authpkg.PrincipalKind(principalKind), req.ExpiresAt, now); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(now) {
		http.Error(w, "expires_at must be in the future", http.StatusBadRequest)
		return
	}

	// Generate raw token: engram_ + 32 hex chars (16 random bytes)
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rawToken := tokenRawPrefix + hex.EncodeToString(randomBytes)
	prefix := rawToken[len(tokenRawPrefix) : len(tokenRawPrefix)+tokenPrefixLen]

	// Hash with bcrypt
	hash, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	token, err := tokenStore.CreateWithPrincipal(r.Context(), gormdb.TokenCreatePrincipalInput{
		Name:          req.Name,
		TokenHash:     string(hash),
		TokenPrefix:   prefix,
		Scope:         scope,
		Principal:     principal,
		PrincipalKind: principalKind,
		ExpiresAt:     req.ExpiresAt,
	})
	if err != nil {
		// Check for unique constraint violation (duplicate name)
		if isDuplicateKeyError(err) {
			http.Error(w, "token name already exists", http.StatusConflict)
			return
		}
		log.Error().Err(err).Msg("auth: failed to create token")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	response := map[string]any{
		"id":             token.ID,
		"name":           token.Name,
		"token":          rawToken,
		"prefix":         token.TokenPrefix,
		"token_prefix":   token.TokenPrefix,
		"scope":          token.Scope,
		"principal":      token.Principal,
		"principal_kind": token.PrincipalKind,
	}
	if token.ExpiresAt != nil {
		response["expires_at"] = token.ExpiresAt
	}
	writeJSON(w, response)
}

// handleRevokeToken godoc
// @Summary Revoke an API token
// @Description Revokes the specified API token, preventing further authentication.
// @Tags Auth
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Token ID (UUID)"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {string} string "not found"
// @Failure 500 {string} string "internal error"
// @Router /api/auth/tokens/{id} [delete]
func (s *Service) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if s.requireSessionAdmin(w, r) {
		return
	}

	s.initMu.RLock()
	tokenStore := s.tokenStore
	s.initMu.RUnlock()

	if tokenStore == nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "token id required", http.StatusBadRequest)
		return
	}

	if err := tokenStore.Revoke(r.Context(), id); err != nil {
		log.Error().Err(err).Str("token_id", id).Msg("auth: failed to revoke token")
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	writeJSON(w, map[string]any{
		"revoked": true,
	})
}

// handleGetTokenStats godoc
// @Summary Get API token usage stats
// @Description Returns request count and last-used timestamp for a specific token.
// @Tags Auth
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Token ID (UUID)"
// @Success 200 {object} object
// @Failure 404 {string} string "not found"
// @Failure 500 {string} string "internal error"
// @Router /api/auth/tokens/{id}/stats [get]
func (s *Service) handleGetTokenStats(w http.ResponseWriter, r *http.Request) {
	if s.requireSessionAdmin(w, r) {
		return
	}

	s.initMu.RLock()
	tokenStore := s.tokenStore
	s.initMu.RUnlock()

	if tokenStore == nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "token id required", http.StatusBadRequest)
		return
	}

	token, err := tokenStore.GetByID(r.Context(), id)
	if err != nil {
		log.Error().Err(err).Str("token_id", id).Msg("auth: failed to get token stats")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if token == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	response := map[string]any{
		"id":             token.ID,
		"name":           token.Name,
		"token_prefix":   token.TokenPrefix,
		"scope":          token.Scope,
		"principal":      token.Principal,
		"principal_kind": token.PrincipalKind,
		"request_count":  token.RequestCount,
		"error_count":    token.ErrorCount,
		"last_used_at":   token.LastUsedAt,
		"revoked":        token.Revoked,
		"revoked_at":     token.RevokedAt,
	}
	if token.ExpiresAt != nil {
		response["expires_at"] = token.ExpiresAt
	}
	writeJSON(w, response)
}

func normalizeTokenPrincipal(principal, principalKind string) (string, string, error) {
	principal = strings.TrimSpace(principal)
	principalKind = strings.TrimSpace(principalKind)

	if principal == "" {
		if principalKind != "" {
			return "", "", errors.New("principal is required when principal_kind is set")
		}
		return "", "", nil
	}
	if principalKind == "" {
		principalKind = string(authpkg.PrincipalKindHuman)
	}
	if !authpkg.IsValidPrincipalKind(authpkg.PrincipalKind(principalKind)) {
		return "", "", errors.New("principal_kind must be 'human', 'agent', or 'service'")
	}
	return principal, principalKind, nil
}

// authRoleKey is the context key for the authenticated role.
type authRoleKey struct{}

// getAuthRole extracts the auth role from the request context.
// Returns "admin" for master token or session cookie auth, or the scope for client tokens.
func getAuthRole(r *http.Request) string {
	if role, ok := r.Context().Value(authRoleKey{}).(string); ok {
		return role
	}
	return "" // no role = not authenticated (middleware didn't set context)
}

// computeHMAC computes an HMAC-SHA256 signature.
func computeHMAC(data, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// isDuplicateKeyError checks if the error is a unique constraint violation.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// PostgreSQL unique violation error code 23505
	return containsDuplicateKey(msg)
}

// containsDuplicateKey checks error message for duplicate key indicators.
func containsDuplicateKey(msg string) bool {
	for _, s := range []string{"duplicate key", "23505", "UNIQUE constraint"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
