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
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"golang.org/x/crypto/bcrypt"
)

// sessionPayload is the data stored inside the signed session cookie.
type sessionPayload struct {
	Role string `json:"role"`
	Exp  int64  `json:"exp"`
}

// sessionCookieName is the name of the session cookie.
const sessionCookieName = "engram_session"

// sessionMaxAge is the session cookie lifetime (30 days).
const sessionMaxAge = 30 * 24 * 3600

// tokenRawPrefix is the prefix for generated client API tokens.
const tokenRawPrefix = "engram_"

// tokenPrefixLen is the number of hex chars after "engram_" used for prefix lookup.
const tokenPrefixLen = 8

// loginRequest is the JSON body for POST /api/auth/login.
type loginRequest struct {
	Token string `json:"token"`
}

// tokenCreateRequest is the JSON body for POST /api/auth/tokens.
type tokenCreateRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
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

// handleAuthLogout godoc
// @Summary Logout
// @Description Clears the session cookie.
// @Tags Auth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/auth/logout [post]
func (s *Service) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
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
	// This endpoint is exempt from auth middleware so the SPA can check auth status.
	// We manually verify auth here and return the result.
	role := getAuthRole(r)
	if role != "" {
		writeJSON(w, map[string]any{
			"authenticated": true,
			"role":          role,
		})
		return
	}

	// Check cookie manually (since middleware was bypassed)
	if s.tokenAuth != nil {
		s.tokenAuth.mu.RLock()
		cookieKey := s.tokenAuth.cookieKey
		s.tokenAuth.mu.RUnlock()

		if cookie, err := r.Cookie("engram_session"); err == nil && len(cookieKey) > 0 {
			parts := strings.SplitN(cookie.Value, ".", 2)
			if len(parts) == 2 {
				payload, _ := base64.RawURLEncoding.DecodeString(parts[0])
				sig, _ := base64.RawURLEncoding.DecodeString(parts[1])
				expectedSig := computeHMAC(payload, cookieKey)
				if hmac.Equal(sig, expectedSig) {
					writeJSON(w, map[string]any{
						"authenticated": true,
						"role":          "admin",
					})
					return
				}
			}
		}
	}

	// Not authenticated
	w.WriteHeader(http.StatusUnauthorized)
	writeJSON(w, map[string]any{
		"authenticated": false,
	})
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
		ID           string     `json:"id"`
		Name         string     `json:"name"`
		TokenPrefix  string     `json:"token_prefix"`
		Scope        string     `json:"scope"`
		CreatedAt    time.Time  `json:"created_at"`
		LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
		RequestCount int64      `json:"request_count"`
		ErrorCount   int64      `json:"error_count"`
		Revoked      bool       `json:"revoked"`
		RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	}

	resp := make([]tokenResponse, len(tokens))
	for i, t := range tokens {
		resp[i] = tokenResponse{
			ID:           t.ID,
			Name:         t.Name,
			TokenPrefix:  t.TokenPrefix,
			Scope:        t.Scope,
			CreatedAt:    t.CreatedAt,
			LastUsedAt:   t.LastUsedAt,
			RequestCount: t.RequestCount,
			ErrorCount:   t.ErrorCount,
			Revoked:      t.Revoked,
			RevokedAt:    t.RevokedAt,
		}
	}

	writeJSON(w, map[string]any{
		"tokens": resp,
	})
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
// @Failure 409 {string} string "conflict"
// @Failure 500 {string} string "internal error"
// @Router /api/auth/tokens [post]
func (s *Service) handleCreateToken(w http.ResponseWriter, r *http.Request) {
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

	// Generate raw token: eng_ + 32 hex chars (16 random bytes)
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

	token, err := tokenStore.Create(r.Context(), req.Name, string(hash), prefix, scope)
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

	writeJSON(w, map[string]any{
		"id":    token.ID,
		"name":  token.Name,
		"token": rawToken,
		"scope": token.Scope,
	})
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
