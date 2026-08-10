// Package worker provides HTTP handlers for email/password authentication.
package worker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	authpkg "github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"golang.org/x/crypto/bcrypt"
	gormlib "gorm.io/gorm"
)

const (
	bcryptCost            = 12
	sessionDuration       = 7 * 24 * time.Hour
	defaultInvitationTTL  = 7 * 24 * time.Hour
	defaultAccessListSize = 100
	authSessionCookieName = "engram_auth"
)

// AuthHandlers provides HTTP handlers for email/password authentication.
// This is separate from the master-token (HMAC) auth in handlers_auth.go.
type AuthHandlers struct {
	users       *gormdb.UserStore
	invitations *gormdb.InvitationStore
	sessions    *gormdb.AuthSessionStore
	access      *gormdb.DomainOwnerStore

	// Rate limiting: IP -> recent attempts in the last minute. Guarded by
	// loginAttemptsMu; stale keys are pruned on each call so the structure does
	// not grow without bound.
	loginAttemptsMu sync.Mutex
	loginAttempts   map[string][]time.Time

	// beforeAccessSessionCheck is a test seam used by the lifecycle race tests.
	beforeAccessSessionCheck func()
	// beforeInitialAdminCreate is a test seam used to align concurrent setup
	// requests after bcrypt but before the authoritative database operation.
	beforeInitialAdminCreate func()
}

// NewAuthHandlers creates AuthHandlers wired to the given stores.
func NewAuthHandlers(users *gormdb.UserStore, invitations *gormdb.InvitationStore, sessions *gormdb.AuthSessionStore, access *gormdb.DomainOwnerStore) *AuthHandlers {
	return &AuthHandlers{
		users:         users,
		invitations:   invitations,
		sessions:      sessions,
		access:        access,
		loginAttempts: make(map[string][]time.Time),
	}
}

type safeUser struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type accessProvidersResponse struct {
	Providers                  []accessProviderView `json:"providers"`
	AuthDisabled               bool                 `json:"auth_disabled"`
	LocalLoginEnabled          bool                 `json:"local_login_enabled"`
	AuthentikTrustedProxyCount int                  `json:"authentik_trusted_proxy_count"`
}

type accessProviderView struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	Configured  bool   `json:"configured"`
	Operable    bool   `json:"operable"`
	Honesty     string `json:"honesty"`
	Evidence    string `json:"evidence"`
	Description string `json:"description"`
}

type createInvitationRequest struct {
	Email          string `json:"email"`
	Role           string `json:"role"`
	ExpiresAt      string `json:"expires_at"`
	ExpiresInHours int    `json:"expires_in_hours"`
}

type revokeReasonRequest struct {
	Reason string `json:"reason"`
}

type updateUserRequest struct {
	Disabled *bool   `json:"disabled,omitempty"`
	Role     *string `json:"role,omitempty"`
}

func writeAuthJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func requireLegacyAdminRole(w http.ResponseWriter, r *http.Request) bool {
	role, _ := r.Context().Value(authRoleKey{}).(string)
	if role != gormdb.DashboardRoleAdmin {
		writeAuthJSONError(w, http.StatusForbidden, "admin access required")
		return false
	}
	return true
}

func toSafeUser(u *gormdb.User) safeUser {
	return safeUser{
		ID:          u.ID,
		Email:       u.Email,
		Role:        u.Role,
		Disabled:    u.Disabled,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

func actorAuditID(user *gormdb.User) *int64 {
	if user == nil || user.ID <= 0 {
		return nil
	}
	id := user.ID
	return &id
}

func actorAuditValue(user *gormdb.User) any {
	if user == nil || user.ID <= 0 {
		return nil
	}
	return user.ID
}

func (h *AuthHandlers) requireStores(w http.ResponseWriter, users, invitations, sessions, access bool) bool {
	if users && h.users == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "user store not ready")
		return false
	}
	if invitations && h.invitations == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "invitation store not ready")
		return false
	}
	if sessions && h.sessions == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "session store not ready")
		return false
	}
	if access && h.access == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "access store not ready")
		return false
	}
	return true
}

func (h *AuthHandlers) currentCookieSessionUser(w http.ResponseWriter, r *http.Request) (*gormdb.AuthSession, *gormdb.User, bool) {
	if !h.requireStores(w, true, false, true, false) {
		return nil, nil, false
	}
	cookie, err := r.Cookie(authSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		writeAuthJSONError(w, http.StatusUnauthorized, "dashboard session required")
		return nil, nil, false
	}
	sess, err := h.sessions.GetSession(cookie.Value)
	if err != nil {
		switch {
		case errors.Is(err, gormdb.ErrAuthSessionRevoked):
			writeAuthJSONError(w, http.StatusUnauthorized, "session revoked")
		case errors.Is(err, gormdb.ErrAuthSessionExpired):
			writeAuthJSONError(w, http.StatusUnauthorized, "session expired")
		default:
			writeAuthJSONError(w, http.StatusUnauthorized, "session not active")
		}
		return nil, nil, false
	}
	user, err := h.users.GetUserByID(sess.UserID)
	if err != nil {
		writeAuthJSONError(w, http.StatusUnauthorized, "session user not found")
		return nil, nil, false
	}
	if user.Disabled {
		writeAuthJSONError(w, http.StatusForbidden, "account disabled")
		return nil, nil, false
	}
	return sess, user, true
}

func (h *AuthHandlers) requireAccessSessionAdminRead(w http.ResponseWriter, r *http.Request) bool {
	id, ok := authpkg.IdentityFrom(r.Context())
	if !ok {
		writeAuthJSONError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	if !id.IsSessionAdmin() {
		writeAuthJSONError(w, http.StatusForbidden, "access administration requires a browser admin session")
		return false
	}
	if isAuthDisabled() {
		return true
	}
	if cookie, err := r.Cookie(authSessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		if h.beforeAccessSessionCheck != nil {
			h.beforeAccessSessionCheck()
		}
		_, _, ok := h.currentCookieSessionUser(w, r)
		return ok
	}
	return true
}

func (h *AuthHandlers) currentAccessSessionAdminUser(w http.ResponseWriter, r *http.Request) (*gormdb.AuthSession, *gormdb.User, bool) {
	if cookie, err := r.Cookie(authSessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		if h.beforeAccessSessionCheck != nil {
			h.beforeAccessSessionCheck()
		}
		sess, user, ok := h.currentCookieSessionUser(w, r)
		if !ok {
			return nil, nil, false
		}
		return sess, user, true
	}
	if !h.requireStores(w, true, false, false, false) {
		return nil, nil, false
	}
	email := strings.TrimSpace(r.Header.Get("X-Authentik-Email"))
	if email == "" {
		writeAuthJSONError(w, http.StatusUnauthorized, "not authenticated")
		return nil, nil, false
	}
	user, err := h.users.GetUserByEmail(email)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			writeAuthJSONError(w, http.StatusUnauthorized, "not authenticated")
		} else {
			writeAuthJSONError(w, http.StatusInternalServerError, "failed to load session user")
		}
		return nil, nil, false
	}
	if user.Disabled {
		writeAuthJSONError(w, http.StatusForbidden, "account disabled")
		return nil, nil, false
	}
	return nil, user, true
}

func (h *AuthHandlers) requireAccessSessionAdmin(w http.ResponseWriter, r *http.Request) (*gormdb.AuthSession, *gormdb.User, bool) {
	id, ok := authpkg.IdentityFrom(r.Context())
	if !ok {
		writeAuthJSONError(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}
	if !id.IsSessionAdmin() {
		writeAuthJSONError(w, http.StatusForbidden, "access administration requires a browser admin session")
		return nil, nil, false
	}
	if isAuthDisabled() {
		return &gormdb.AuthSession{ID: "auth-disabled"}, &gormdb.User{ID: 0, Email: "auth-disabled", Role: gormdb.DashboardRoleAdmin}, true
	}
	sess, user, ok := h.currentAccessSessionAdminUser(w, r)
	if !ok {
		return nil, nil, false
	}
	if user.Role != gormdb.DashboardRoleAdmin {
		writeAuthJSONError(w, http.StatusForbidden, "admin access required")
		return nil, nil, false
	}
	return sess, user, true

}

func decodeOptionalJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func parseCreateInvitationRequest(r *http.Request) (string, string, time.Time, error) {
	return parseCreateInvitationRequestWithEmailPolicy(r, false)
}

func parseAccessCreateInvitationRequest(r *http.Request) (string, string, time.Time, error) {
	return parseCreateInvitationRequestWithEmailPolicy(r, true)
}

func parseCreateInvitationRequestWithEmailPolicy(r *http.Request, requireEmail bool) (string, string, time.Time, error) {
	var req createInvitationRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		return "", "", time.Time{}, fmt.Errorf("invalid request")
	}
	email := strings.TrimSpace(req.Email)
	if requireEmail && email == "" {
		return "", "", time.Time{}, fmt.Errorf("email is required")
	}
	role, err := gormdb.NormalizeDashboardRole(req.Role)
	if err != nil {
		return "", "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(defaultInvitationTTL)
	if req.ExpiresInHours > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(req.ExpiresInHours) * time.Hour)
	}
	if strings.TrimSpace(req.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ExpiresAt))
		if err != nil {
			return "", "", time.Time{}, fmt.Errorf("expires_at must be RFC3339")
		}
		expiresAt = parsed.UTC()
	}
	if !expiresAt.After(time.Now().UTC()) {
		return "", "", time.Time{}, fmt.Errorf("expires_at must be in the future")
	}
	return email, role, expiresAt, nil
}

func parseRevokeReason(r *http.Request, fallback string) (string, error) {
	var req revokeReasonRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		return "", fmt.Errorf("invalid request")
	}
	if strings.TrimSpace(req.Reason) != "" {
		return strings.TrimSpace(req.Reason), nil
	}
	return fallback, nil
}

func accessLimit(r *http.Request, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 500 {
		return 500
	}
	return value
}

func (h *AuthHandlers) writeUsersResponse(w http.ResponseWriter) {
	users, err := h.users.ListUsers()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list users")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	out := make([]safeUser, len(users))
	for i, u := range users {
		out[i] = toSafeUser(u)
	}
	writeJSON(w, map[string]any{"users": out})
}

func (h *AuthHandlers) applyUserUpdate(id int64, req updateUserRequest, actorID *int64, actorEmail string, audit bool) (*gormdb.User, error) {
	if h.users == nil || h.sessions == nil {
		return nil, fmt.Errorf("stores not ready")
	}
	before, err := h.users.GetUserByID(id)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{}
	var reasonParts []string
	var normalizedRole *string
	if req.Disabled != nil {
		updates["disabled"] = *req.Disabled
		if *req.Disabled {
			reasonParts = append(reasonParts, "disabled=true")
		} else {
			reasonParts = append(reasonParts, "disabled=false")
		}
	}
	if req.Role != nil {
		role, err := gormdb.NormalizeDashboardRole(*req.Role)
		if err != nil {
			return nil, err
		}
		normalizedRole = &role
		updates["role"] = role
		reasonParts = append(reasonParts, "role="+role)
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("no updates provided")
	}
	after, err := h.users.UpdateUserWithLastAdminGuard(id, normalizedRole, req.Disabled)
	if err != nil {
		return nil, err
	}
	if req.Disabled != nil && *req.Disabled {
		reason := "admin disabled user"
		if actorEmail == "" {
			reason = "user disabled"
		}
		if err := h.sessions.DeleteUserSessions(id, actorID, reason); err != nil {
			return nil, err
		}
	}
	if audit && h.access != nil {
		action := "auth_user_updated"
		if req.Role != nil && before.Role != after.Role {
			action = "auth_user_role_updated"
		}
		if req.Disabled != nil && before.Disabled != after.Disabled {
			if after.Disabled {
				action = "auth_user_disabled"
			} else {
				action = "auth_user_enabled"
			}
		}
		_ = h.access.LogAccessEvent(context.Background(), gormdb.AccessAuditRecord{
			Action:      action,
			Actor:       actorEmail,
			Reason:      strings.Join(reasonParts, ", "),
			BeforeState: map[string]any{"user_id": before.ID, "email": before.Email, "role": before.Role, "disabled": before.Disabled},
			AfterState:  map[string]any{"user_id": after.ID, "email": after.Email, "role": after.Role, "disabled": after.Disabled},
			CreatedAt:   time.Now().UTC(),
		})
	}
	return after, nil
}

// handleSetupNeeded returns {"needed": true} when no users exist yet.
func (h *AuthHandlers) handleSetupNeeded(w http.ResponseWriter, r *http.Request) {
	if !h.requireStores(w, true, false, false, false) {
		return
	}
	count, err := h.users.CountUsers()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to count users")
		writeAuthJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, map[string]bool{"needed": count == 0})
}

// handleSetup creates the first admin user (no invitation required). The
// server-host operator token is required even while setup-needed stays public.
// Returns 409 Conflict if any users already exist.
func (h *AuthHandlers) handleSetup(w http.ResponseWriter, r *http.Request) {
	masterToken := os.Getenv("ENGRAM_AUTH_ADMIN_TOKEN")
	providedToken := extractHTTPBearer(r)
	if masterToken == "" || providedToken == "" || subtle.ConstantTimeCompare([]byte(providedToken), []byte(masterToken)) != 1 {
		writeAuthJSONError(w, http.StatusUnauthorized, "operator token required")
		return
	}

	if !h.requireStores(w, true, false, false, false) {
		return
	}
	count, err := h.users.CountUsers()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to count users during setup")
		writeAuthJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if count > 0 {
		writeAuthJSONError(w, http.StatusConflict, "setup already completed")
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" || req.Password == "" {
		writeAuthJSONError(w, http.StatusBadRequest, "email and password required")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		log.Error().Err(err).Msg("auth: bcrypt failed during setup")
		writeAuthJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if h.beforeInitialAdminCreate != nil {
		h.beforeInitialAdminCreate()
	}
	user, err := h.users.CreateInitialAdmin(r.Context(), strings.TrimSpace(req.Email), string(hash), h.access)
	if err != nil {
		if errors.Is(err, gormdb.ErrInitialAdminSetupAlreadyCompleted) {
			writeAuthJSONError(w, http.StatusConflict, err.Error())
			return
		}
		log.Error().Err(err).Str("email", req.Email).Msg("auth: failed to create admin user during setup")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"user": toSafeUser(user)})
}

// handleLogin authenticates with email+password and creates a DB-backed session.
// Sets the engram_auth HttpOnly cookie on success.
func (h *AuthHandlers) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !h.requireStores(w, true, false, true, false) {
		return
	}
	ip := r.RemoteAddr
	if !h.checkRateLimit(ip) {
		writeAuthJSONError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" || req.Password == "" {
		writeAuthJSONError(w, http.StatusBadRequest, "email and password required")
		return
	}
	email := strings.TrimSpace(req.Email)
	user, err := h.users.GetUserByEmail(email)
	if err != nil {
		writeAuthJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if user.Disabled {
		writeAuthJSONError(w, http.StatusForbidden, "account disabled")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeAuthJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	sess, err := h.sessions.CreateSession(user.ID, sessionDuration, r.UserAgent(), r.RemoteAddr)
	if err != nil {
		log.Error().Err(err).Int64("user_id", user.ID).Msg("auth: failed to create session")
		writeAuthJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.users.UpdateUser(user.ID, map[string]any{"last_login_at": time.Now().UTC()}); err != nil {
		log.Warn().Err(err).Int64("user_id", user.ID).Msg("auth: failed to update last_login_at")
	}
	if h.access != nil {
		_ = h.access.LogAccessEvent(r.Context(), gormdb.AccessAuditRecord{
			Action:     "auth_login",
			Actor:      user.Email,
			Reason:     "dashboard login",
			AfterState: map[string]any{"user_id": user.ID, "session_id": sess.ID, "remote_addr": sess.RemoteAddr, "user_agent": sess.UserAgent},
			CreatedAt:  time.Now().UTC(),
		})
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authSessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionDuration.Seconds()),
	})
	writeJSON(w, map[string]any{"user": toSafeUser(user)})
}

// handleLogout invalidates the DB session and clears the engram_auth cookie.
func (h *AuthHandlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	if h.sessions != nil {
		if cookie, err := r.Cookie(authSessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
			_ = h.sessions.DeleteSession(cookie.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleCreateInvitation generates a new invitation code (legacy admin route).
func (h *AuthHandlers) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	if !requireLegacyAdminRole(w, r) {
		return
	}
	if !h.requireStores(w, true, true, true, false) {
		return
	}
	_, actor, ok := h.currentCookieSessionUser(w, r)
	if !ok {
		return
	}
	email, role, expiresAt, err := parseCreateInvitationRequest(r)
	if err != nil {
		writeAuthJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	code, err := h.invitations.GenerateCode()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to generate invitation code")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to generate code")
		return
	}
	inv, err := h.invitations.CreateInvitation(code, actor.ID, email, role, expiresAt)
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to create invitation")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"code": inv.Code, "id": inv.ID, "email": inv.Email, "role": inv.Role, "expires_at": inv.ExpiresAt})
}

// handleListInvitations returns invitation metadata without one-time codes (legacy admin route).
func (h *AuthHandlers) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	if !requireLegacyAdminRole(w, r) {
		return
	}
	if !h.requireStores(w, false, false, false, true) {
		return
	}
	rows, err := h.access.ListAccessInvitations(r.Context(), accessLimit(r, defaultAccessListSize))
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list invitations")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}
	writeJSON(w, map[string]any{"invitations": rows})
}

// handleRegister creates a new user account using an invitation code.
func (h *AuthHandlers) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !h.requireStores(w, false, false, false, true) {
		return
	}
	var req struct {
		Email      string `json:"email"`
		Password   string `json:"password"`
		Invitation string `json:"invitation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" || req.Password == "" || strings.TrimSpace(req.Invitation) == "" {
		writeAuthJSONError(w, http.StatusBadRequest, "email, password, and invitation code required")
		return
	}
	if len(req.Password) < 8 {
		writeAuthJSONError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		log.Error().Err(err).Msg("auth: bcrypt failed during register")
		writeAuthJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	user, err := h.access.RegisterUserFromInvitation(r.Context(), gormdb.InvitationRegistrationRequest{
		Code:         strings.TrimSpace(req.Invitation),
		Email:        strings.TrimSpace(req.Email),
		PasswordHash: string(hash),
	})
	if err != nil {
		switch {
		case errors.Is(err, gormlib.ErrRecordNotFound):
			writeAuthJSONError(w, http.StatusForbidden, "invalid invitation code")
		case errors.Is(err, gormdb.ErrInvitationUsed):
			writeAuthJSONError(w, http.StatusConflict, "invitation already used")
		case errors.Is(err, gormdb.ErrInvitationExpired):
			writeAuthJSONError(w, http.StatusForbidden, "invitation expired")
		case errors.Is(err, gormdb.ErrInvitationRevoked):
			writeAuthJSONError(w, http.StatusForbidden, "invitation revoked")
		case errors.Is(err, gormdb.ErrInvitationEmailMismatch):
			writeAuthJSONError(w, http.StatusForbidden, "invitation email does not match")
		default:
			log.Error().Err(err).Str("email", req.Email).Msg("auth: failed to register user")
			if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique") {
				writeAuthJSONError(w, http.StatusConflict, "email already registered")
			} else {
				writeAuthJSONError(w, http.StatusInternalServerError, "failed to create user")
			}
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"user": toSafeUser(user)})
}

// checkRateLimit allows at most 5 login attempts per minute per IP.
// Returns true when the attempt is permitted while pruning stale IP entries so
// the limiter map cannot grow forever.
func (h *AuthHandlers) checkRateLimit(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-time.Minute)

	h.loginAttemptsMu.Lock()
	defer h.loginAttemptsMu.Unlock()

	for key, attempts := range h.loginAttempts {
		valid := attempts[:0]
		for _, t := range attempts {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(h.loginAttempts, key)
			continue
		}
		h.loginAttempts[key] = valid
	}

	attempts := append([]time.Time(nil), h.loginAttempts[ip]...)
	if len(attempts) >= 5 {
		return false
	}
	attempts = append(attempts, now)
	h.loginAttempts[ip] = attempts
	return true
}

// handleListUsers returns all users (legacy admin route, no password hashes).
func (h *AuthHandlers) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !requireLegacyAdminRole(w, r) {
		return
	}
	if !h.requireStores(w, true, false, false, false) {
		return
	}
	h.writeUsersResponse(w)
}

// handleUpdateUser updates user disabled/role (legacy admin route).
func (h *AuthHandlers) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !requireLegacyAdminRole(w, r) {
		return
	}
	if !h.requireStores(w, true, false, true, false) {
		return
	}
	id, req, err := parseUpdateUserRequest(r)
	if err != nil {
		writeAuthJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := h.applyUserUpdate(id, req, nil, "", false); err != nil {
		handleUserUpdateError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAccessProviders returns the live auth-provider posture for the access page.
func (h *AuthHandlers) handleAccessProviders(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessSessionAdminRead(w, r) {
		return
	}
	cfg := config.Get()
	providers := []accessProviderView{
		{
			ID:          "local-password",
			Label:       "Email/password",
			Kind:        "local",
			Enabled:     !cfg.AuthSkipLocal,
			Configured:  true,
			Operable:    !cfg.AuthSkipLocal,
			Honesty:     map[bool]string{true: "live", false: "dormant"}[!cfg.AuthSkipLocal],
			Evidence:    "/api/auth/user-login",
			Description: "Local dashboard login backed by the users/sessions tables.",
		},
		{
			ID:          "authentik-forward-auth",
			Label:       "Authentik / OIDC proxy",
			Kind:        "oidc-proxy",
			Enabled:     cfg.AuthentikEnabled,
			Configured:  cfg.AuthentikEnabled,
			Operable:    cfg.AuthentikEnabled,
			Honesty:     map[bool]string{true: "live", false: "mustbuild"}[cfg.AuthentikEnabled],
			Evidence:    "ENGRAM_AUTHENTIK_ENABLED",
			Description: "Trusted-proxy header login via X-Authentik-Email with optional auto-provisioning.",
		},
	}
	writeJSON(w, accessProvidersResponse{
		Providers:                  providers,
		AuthDisabled:               isAuthDisabled(),
		LocalLoginEnabled:          !cfg.AuthSkipLocal,
		AuthentikTrustedProxyCount: len(cfg.AuthentikTrustedProxies),
	})
}

// handleAccessCreateInvitation creates a lifecycle-managed invitation (session-admin only).
func (h *AuthHandlers) handleAccessCreateInvitation(w http.ResponseWriter, r *http.Request) {
	_, actor, ok := h.requireAccessSessionAdmin(w, r)
	if !ok {
		return
	}
	if !h.requireStores(w, false, true, false, true) {
		return
	}
	if actor.ID <= 0 {
		writeAuthJSONError(w, http.StatusConflict, "invitation lifecycle is unavailable while auth is disabled")
		return
	}
	email, role, expiresAt, err := parseAccessCreateInvitationRequest(r)
	if err != nil {
		writeAuthJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	code, err := h.invitations.GenerateCode()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to generate invitation code")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to generate code")
		return
	}
	inv, err := h.invitations.CreateInvitation(code, actor.ID, email, role, expiresAt)
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to create invitation")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}
	if h.access != nil {
		_ = h.access.LogAccessEvent(r.Context(), gormdb.AccessAuditRecord{
			Action:     "auth_invitation_created",
			Actor:      actor.Email,
			Reason:     "invitation issued",
			AfterState: map[string]any{"invite_id": inv.ID, "email": inv.Email, "role": inv.Role, "expires_at": inv.ExpiresAt},
			CreatedAt:  time.Now().UTC(),
		})
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{
		"invitation": gormdb.AccessInvitationView{
			ID:             inv.ID,
			Code:           inv.Code,
			Email:          inv.Email,
			Role:           inv.Role,
			CreatedBy:      inv.CreatedBy,
			CreatedByEmail: actor.Email,
			ExpiresAt:      inv.ExpiresAt,
			CreatedAt:      inv.CreatedAt,
			Status:         "pending",
		},
	})
}

// handleAccessListInvitations returns lifecycle-managed invitations.
func (h *AuthHandlers) handleAccessListInvitations(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessSessionAdminRead(w, r) {
		return
	}
	if !h.requireStores(w, false, false, false, true) {
		return
	}
	rows, err := h.access.ListAccessInvitations(r.Context(), accessLimit(r, defaultAccessListSize))
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list access invitations")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}
	writeJSON(w, map[string]any{"invitations": rows})
}

// handleAccessRevokeInvitation revokes one invitation without deleting audit evidence.
func (h *AuthHandlers) handleAccessRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	_, actor, ok := h.requireAccessSessionAdmin(w, r)
	if !ok {
		return
	}
	if !h.requireStores(w, false, true, false, true) {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "id")), 10, 64)
	if err != nil || id <= 0 {
		writeAuthJSONError(w, http.StatusBadRequest, "invalid invitation ID")
		return
	}
	before, err := h.invitations.GetInvitationByID(id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			writeAuthJSONError(w, http.StatusNotFound, "invitation not found")
		} else {
			writeAuthJSONError(w, http.StatusInternalServerError, "failed to load invitation")
		}
		return
	}
	reason, err := parseRevokeReason(r, "admin revoked invitation")
	if err != nil {
		writeAuthJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	changed, err := h.invitations.RevokeInvitation(id, actorAuditID(actor), reason)
	if err != nil {
		switch {
		case errors.Is(err, gormdb.ErrInvitationUsed):
			writeAuthJSONError(w, http.StatusConflict, "invitation already used")
		case errors.Is(err, gormlib.ErrRecordNotFound):
			writeAuthJSONError(w, http.StatusNotFound, "invitation not found")
		default:
			log.Error().Err(err).Int64("invite_id", id).Msg("auth: failed to revoke invitation")
			writeAuthJSONError(w, http.StatusInternalServerError, "failed to revoke invitation")
		}
		return
	}
	if changed && h.access != nil {
		_ = h.access.LogAccessEvent(r.Context(), gormdb.AccessAuditRecord{
			Action:      "auth_invitation_revoked",
			Actor:       actor.Email,
			Reason:      reason,
			BeforeState: map[string]any{"invite_id": before.ID, "email": before.Email, "role": before.Role},
			AfterState:  map[string]any{"invite_id": before.ID, "revoked_by": actorAuditValue(actor), "reason": reason},
			CreatedAt:   time.Now().UTC(),
		})
	}
	writeJSON(w, map[string]any{"status": "ok", "id": id})
}

// handleAccessListUsers returns the access user table.
func (h *AuthHandlers) handleAccessListUsers(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessSessionAdminRead(w, r) {
		return
	}
	if !h.requireStores(w, true, false, false, false) {
		return
	}
	h.writeUsersResponse(w)
}

// handleAccessGetUserDrilldown returns the user detail side panel data.
func (h *AuthHandlers) handleAccessGetUserDrilldown(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessSessionAdminRead(w, r) {
		return
	}
	if !h.requireStores(w, false, false, false, true) {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "id")), 10, 64)
	if err != nil || id <= 0 {
		writeAuthJSONError(w, http.StatusBadRequest, "invalid user ID")
		return
	}
	detail, err := h.access.GetAccessUserDrilldown(r.Context(), id, accessLimit(r, 20))
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			writeAuthJSONError(w, http.StatusNotFound, "user not found")
		} else {
			log.Error().Err(err).Int64("user_id", id).Msg("auth: failed to load access user drilldown")
			writeAuthJSONError(w, http.StatusInternalServerError, "failed to load user detail")
		}
		return
	}
	writeJSON(w, detail)
}

// handleAccessUpdateUser updates one user from the access page.
func (h *AuthHandlers) handleAccessUpdateUser(w http.ResponseWriter, r *http.Request) {
	_, actor, ok := h.requireAccessSessionAdmin(w, r)
	if !ok {
		return
	}
	if !h.requireStores(w, true, false, true, true) {
		return
	}
	id, req, err := parseUpdateUserRequest(r)
	if err != nil {
		writeAuthJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := h.applyUserUpdate(id, req, actorAuditID(actor), actor.Email, true)
	if err != nil {
		handleUserUpdateError(w, err)
		return
	}
	writeJSON(w, map[string]any{"user": toSafeUser(updated)})
}

// handleAccessListRoles returns the supported dashboard roles with live counts.
func (h *AuthHandlers) handleAccessListRoles(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessSessionAdminRead(w, r) {
		return
	}
	if !h.requireStores(w, false, false, false, true) {
		return
	}
	roles, err := h.access.ListAccessRoles(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list access roles")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to list roles")
		return
	}
	writeJSON(w, map[string]any{"roles": roles})
}

// handleAccessListSessions returns access sessions with lifecycle fields.
func (h *AuthHandlers) handleAccessListSessions(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessSessionAdminRead(w, r) {
		return
	}
	if !h.requireStores(w, false, false, false, true) {
		return
	}
	includeRevoked := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_revoked")), "true")
	rows, err := h.access.ListAccessSessions(r.Context(), accessLimit(r, defaultAccessListSize), includeRevoked)
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list access sessions")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}
	writeJSON(w, map[string]any{"sessions": rows})
}

// handleAccessRevokeSession revokes one dashboard session atomically.
func (h *AuthHandlers) handleAccessRevokeSession(w http.ResponseWriter, r *http.Request) {
	_, actor, ok := h.requireAccessSessionAdmin(w, r)
	if !ok {
		return
	}
	if !h.requireStores(w, false, false, true, true) {
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeAuthJSONError(w, http.StatusBadRequest, "invalid session ID")
		return
	}
	before, err := h.sessions.GetAnySession(id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			writeAuthJSONError(w, http.StatusNotFound, "session not found")
		} else {
			log.Error().Err(err).Str("session_id", id).Msg("auth: failed to load session")
			writeAuthJSONError(w, http.StatusInternalServerError, "failed to load session")
		}
		return
	}
	reason, err := parseRevokeReason(r, "admin revoked session")
	if err != nil {
		writeAuthJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	changed, err := h.sessions.RevokeSession(id, actorAuditID(actor), reason)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			writeAuthJSONError(w, http.StatusNotFound, "session not found")
		} else {
			log.Error().Err(err).Str("session_id", id).Msg("auth: failed to revoke session")
			writeAuthJSONError(w, http.StatusInternalServerError, "failed to revoke session")
		}
		return
	}
	if changed && h.access != nil {
		_ = h.access.LogAccessEvent(r.Context(), gormdb.AccessAuditRecord{
			Action:      "auth_session_revoked",
			Actor:       actor.Email,
			Reason:      reason,
			BeforeState: map[string]any{"session_id": before.ID, "user_id": before.UserID, "user_agent": before.UserAgent, "remote_addr": before.RemoteAddr, "expires_at": before.ExpiresAt},
			AfterState:  map[string]any{"session_id": before.ID, "revoked_by": actorAuditValue(actor), "reason": reason},
			CreatedAt:   time.Now().UTC(),
		})
	}
	writeJSON(w, map[string]any{"status": "ok", "id": id})
}

// handleAccessListAudit returns the access/auth audit trail.
func (h *AuthHandlers) handleAccessListAudit(w http.ResponseWriter, r *http.Request) {
	if !h.requireAccessSessionAdminRead(w, r) {
		return
	}
	if !h.requireStores(w, false, false, false, true) {
		return
	}
	entries, err := h.access.ListAccessAudit(r.Context(), accessLimit(r, defaultAccessListSize))
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list access audit")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to list access log")
		return
	}
	writeJSON(w, map[string]any{"entries": entries})
}

func parseUpdateUserRequest(r *http.Request) (int64, updateUserRequest, error) {
	idStr := strings.TrimSpace(chi.URLParam(r, "id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return 0, updateUserRequest{}, fmt.Errorf("invalid user ID")
	}
	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return 0, updateUserRequest{}, fmt.Errorf("invalid request")
	}
	return id, req, nil
}

func handleUserUpdateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormlib.ErrRecordNotFound):
		writeAuthJSONError(w, http.StatusNotFound, "user not found")
	case strings.Contains(err.Error(), "last admin"):
		writeAuthJSONError(w, http.StatusBadRequest, err.Error())
	case strings.Contains(err.Error(), "role ") || strings.Contains(err.Error(), "no updates provided") || strings.Contains(err.Error(), "invalid"):
		writeAuthJSONError(w, http.StatusBadRequest, err.Error())
	default:
		log.Error().Err(err).Msg("auth: failed to update user")
		writeAuthJSONError(w, http.StatusInternalServerError, "failed to update user")
	}
}

// Service-level delegation methods.
// These are registered on the chi router in setupRoutes and delegate to s.authHandlers,
// returning 503 Service Unavailable if the handler is not yet initialized (async init).

func (s *Service) handleUserSetupNeeded(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleSetupNeeded(w, r)
}

func (s *Service) handleUserSetup(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleSetup(w, r)
}

func (s *Service) handleUserLogin(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleLogin(w, r)
}

func (s *Service) handleUserLogout(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleLogout(w, r)
}

func (s *Service) handleUserRegister(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleRegister(w, r)
}

func (s *Service) handleAdminCreateInvitation(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleCreateInvitation(w, r)
}

func (s *Service) handleAdminListInvitations(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleListInvitations(w, r)
}

func (s *Service) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleListUsers(w, r)
}

func (s *Service) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleUpdateUser(w, r)
}

func (s *Service) handleAccessProviders(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessProviders(w, r)
}

func (s *Service) handleAccessCreateInvitation(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessCreateInvitation(w, r)
}

func (s *Service) handleAccessListInvitations(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessListInvitations(w, r)
}

func (s *Service) handleAccessRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessRevokeInvitation(w, r)
}

func (s *Service) handleAccessListUsers(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessListUsers(w, r)
}

func (s *Service) handleAccessGetUserDrilldown(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessGetUserDrilldown(w, r)
}

func (s *Service) handleAccessUpdateUser(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessUpdateUser(w, r)
}

func (s *Service) handleAccessListRoles(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessListRoles(w, r)
}

func (s *Service) handleAccessListSessions(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessListSessions(w, r)
}

func (s *Service) handleAccessRevokeSession(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessRevokeSession(w, r)
}

func (s *Service) handleAccessListAudit(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		writeAuthJSONError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	h.handleAccessListAudit(w, r)
}
