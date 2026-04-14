// Package worker provides HTTP handlers for email/password authentication.
package worker

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptCost            = 12
	sessionDuration       = 7 * 24 * time.Hour // 7 days
	authSessionCookieName = "engram_auth"
)

// AuthHandlers provides HTTP handlers for email/password authentication.
// This is separate from the master-token (HMAC) auth in handlers_auth.go.
type AuthHandlers struct {
	users       *gormdb.UserStore
	invitations *gormdb.InvitationStore
	sessions    *gormdb.AuthSessionStore

	// Rate limiting: IP -> mutex + []time.Time (last N attempts)
	loginAttempts sync.Map
}

// NewAuthHandlers creates AuthHandlers wired to the given stores.
func NewAuthHandlers(users *gormdb.UserStore, invitations *gormdb.InvitationStore, sessions *gormdb.AuthSessionStore) *AuthHandlers {
	return &AuthHandlers{
		users:       users,
		invitations: invitations,
		sessions:    sessions,
	}
}

// handleSetupNeeded returns {"needed": true} when no users exist yet.
func (h *AuthHandlers) handleSetupNeeded(w http.ResponseWriter, r *http.Request) {
	count, err := h.users.CountUsers()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to count users")
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]bool{"needed": count == 0}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode setup-needed response")
	}
}

// handleSetup creates the first admin user (no invitation required).
// Returns 409 Conflict if any users already exist.
func (h *AuthHandlers) handleSetup(w http.ResponseWriter, r *http.Request) {
	count, err := h.users.CountUsers()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to count users during setup")
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	if count > 0 {
		http.Error(w, `{"error":"setup already completed"}`, http.StatusConflict)
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		http.Error(w, `{"error":"email and password required"}`, http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		log.Error().Err(err).Msg("auth: bcrypt failed during setup")
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	user, err := h.users.CreateUser(req.Email, string(hash), "admin")
	if err != nil {
		log.Error().Err(err).Str("email", req.Email).Msg("auth: failed to create admin user during setup")
		http.Error(w, `{"error":"failed to create user"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"user": map[string]any{"id": user.ID, "email": user.Email, "role": user.Role},
	}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode setup response")
	}
}

// handleLogin authenticates with email+password and creates a DB-backed session.
// Sets the engram_auth HttpOnly cookie on success.
func (h *AuthHandlers) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if !h.checkRateLimit(ip) {
		http.Error(w, `{"error":"too many login attempts, try again later"}`, http.StatusTooManyRequests)
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		http.Error(w, `{"error":"email and password required"}`, http.StatusBadRequest)
		return
	}

	user, err := h.users.GetUserByEmail(req.Email)
	if err != nil {
		// Don't disclose whether the email exists.
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	if user.Disabled {
		http.Error(w, `{"error":"account disabled"}`, http.StatusForbidden)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	sess, err := h.sessions.CreateSession(user.ID, sessionDuration)
	if err != nil {
		log.Error().Err(err).Int64("user_id", user.ID).Msg("auth: failed to create session")
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	// Update last login asynchronously — failure here is non-fatal.
	now := time.Now()
	if err := h.users.UpdateUser(user.ID, map[string]any{"last_login_at": now}); err != nil {
		log.Warn().Err(err).Int64("user_id", user.ID).Msg("auth: failed to update last_login_at")
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authSessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionDuration.Seconds()),
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"user": map[string]any{"id": user.ID, "email": user.Email, "role": user.Role},
	}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode login response")
	}
}

// handleLogout invalidates the DB session and clears the engram_auth cookie.
func (h *AuthHandlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(authSessionCookieName)
	if err == nil && cookie.Value != "" {
		if delErr := h.sessions.DeleteSession(cookie.Value); delErr != nil {
			log.Warn().Err(delErr).Msg("auth: failed to delete session on logout")
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode logout response")
	}
}

// handleMe returns the current authenticated user from the engram_auth session cookie.
func (h *AuthHandlers) handleMe(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(authSessionCookieName)
	if err != nil || cookie.Value == "" {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}

	sess, err := h.sessions.GetSession(cookie.Value)
	if err != nil {
		// Expired or invalid — clear the stale cookie.
		http.SetCookie(w, &http.Cookie{
			Name:     authSessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
		})
		http.Error(w, `{"error":"session expired"}`, http.StatusUnauthorized)
		return
	}

	user, err := h.users.GetUserByID(sess.UserID)
	if err != nil || user.Disabled {
		http.Error(w, `{"error":"account not found or disabled"}`, http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"user": map[string]any{"id": user.ID, "email": user.Email, "role": user.Role},
	}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode me response")
	}
}

// handleCreateInvitation generates a new invitation code (admin only).
func (h *AuthHandlers) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	role, _ := r.Context().Value(authRoleKey{}).(string)
	if role != "admin" {
		http.Error(w, `{"error":"admin access required"}`, http.StatusForbidden)
		return
	}

	code, err := h.invitations.GenerateCode()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to generate invitation code")
		http.Error(w, `{"error":"failed to generate code"}`, http.StatusInternalServerError)
		return
	}

	// Get user ID from session for created_by
	cookie, err := r.Cookie(authSessionCookieName)
	if err != nil || cookie.Value == "" {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	sess, err := h.sessions.GetSession(cookie.Value)
	if err != nil {
		http.Error(w, `{"error":"session expired"}`, http.StatusUnauthorized)
		return
	}

	inv, err := h.invitations.CreateInvitation(code, sess.UserID)
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to create invitation")
		http.Error(w, `{"error":"failed to create invitation"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]any{"code": inv.Code, "id": inv.ID}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode create-invitation response")
	}
}

// handleListInvitations returns all invitation codes (admin only).
func (h *AuthHandlers) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	role, _ := r.Context().Value(authRoleKey{}).(string)
	if role != "admin" {
		http.Error(w, `{"error":"admin access required"}`, http.StatusForbidden)
		return
	}

	invitations, err := h.invitations.ListInvitations()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list invitations")
		http.Error(w, `{"error":"failed to list invitations"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"invitations": invitations}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode list-invitations response")
	}
}

// handleRegister creates a new user account using an invitation code.
func (h *AuthHandlers) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email      string `json:"email"`
		Password   string `json:"password"`
		Invitation string `json:"invitation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" || req.Invitation == "" {
		http.Error(w, `{"error":"email, password, and invitation code required"}`, http.StatusBadRequest)
		return
	}

	if len(req.Password) < 8 {
		http.Error(w, `{"error":"password must be at least 8 characters"}`, http.StatusBadRequest)
		return
	}

	// Validate invitation
	if _, err := h.invitations.GetValidInvitation(req.Invitation); err != nil {
		http.Error(w, `{"error":"invalid or used invitation code"}`, http.StatusForbidden)
		return
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		log.Error().Err(err).Msg("auth: bcrypt failed during register")
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	// Create user
	user, err := h.users.CreateUser(req.Email, string(hash), "operator")
	if err != nil {
		log.Error().Err(err).Str("email", req.Email).Msg("auth: failed to create user during register")
		http.Error(w, `{"error":"email already registered"}`, http.StatusConflict)
		return
	}

	// Consume invitation — user already created; log but don't fail if this step errors.
	if err := h.invitations.ConsumeInvitation(req.Invitation, user.ID); err != nil {
		log.Warn().Err(err).Str("code", req.Invitation).Int64("user_id", user.ID).Msg("auth: failed to consume invitation after registration")
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"user": map[string]any{"id": user.ID, "email": user.Email, "role": user.Role},
	}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode register response")
	}
}

// checkRateLimit allows at most 5 login attempts per minute per IP.
// Returns true when the attempt is permitted.
func (h *AuthHandlers) checkRateLimit(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-time.Minute)

	// Each IP gets a dedicated mutex stored in the sync.Map.
	muKey := ip + ":mu"
	muVal, _ := h.loginAttempts.LoadOrStore(muKey, &sync.Mutex{})
	mu := muVal.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	attemptsKey := ip + ":attempts"
	attemptsVal, _ := h.loginAttempts.LoadOrStore(attemptsKey, &[]time.Time{})
	attempts := attemptsVal.(*[]time.Time)

	// Discard attempts older than the window.
	valid := (*attempts)[:0]
	for _, t := range *attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= 5 {
		*attempts = valid
		return false
	}

	*attempts = append(valid, now)
	return true
}

// handleListUsers returns all users (admin only, no password hashes).
func (h *AuthHandlers) handleListUsers(w http.ResponseWriter, r *http.Request) {
	role, _ := r.Context().Value(authRoleKey{}).(string)
	if role != "admin" {
		http.Error(w, `{"error":"admin access required"}`, http.StatusForbidden)
		return
	}

	users, err := h.users.ListUsers()
	if err != nil {
		log.Error().Err(err).Msg("auth: failed to list users")
		http.Error(w, `{"error":"failed to list users"}`, http.StatusInternalServerError)
		return
	}

	// Strip password hashes
	type safeUser struct {
		ID          int64      `json:"id"`
		Email       string     `json:"email"`
		Role        string     `json:"role"`
		Disabled    bool       `json:"disabled"`
		CreatedAt   time.Time  `json:"created_at"`
		LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	}
	safe := make([]safeUser, len(users))
	for i, u := range users {
		safe[i] = safeUser{u.ID, u.Email, u.Role, u.Disabled, u.CreatedAt, u.LastLoginAt}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"users": safe}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode list-users response")
	}
}

// handleUpdateUser updates user disabled/role (admin only).
func (h *AuthHandlers) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	role, _ := r.Context().Value(authRoleKey{}).(string)
	if role != "admin" {
		http.Error(w, `{"error":"admin access required"}`, http.StatusForbidden)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid user ID"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Disabled *bool   `json:"disabled,omitempty"`
		Role     *string `json:"role,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	updates := map[string]any{}
	if req.Disabled != nil {
		if *req.Disabled {
			adminCount, err := h.users.CountAdmins()
			if err != nil {
				log.Error().Err(err).Msg("auth: failed to count admins for disable check")
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}
			targetUser, err := h.users.GetUserByID(id)
			if err != nil {
				log.Error().Err(err).Int64("user_id", id).Msg("auth: failed to get user for disable check")
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}
			if targetUser.Role == "admin" && adminCount <= 1 {
				http.Error(w, `{"error":"cannot disable the last admin"}`, http.StatusBadRequest)
				return
			}
		}
		updates["disabled"] = *req.Disabled
		if *req.Disabled {
			// Delete all sessions for disabled user
			if err := h.sessions.DeleteUserSessions(id); err != nil {
				log.Warn().Err(err).Int64("user_id", id).Msg("auth: failed to delete sessions for disabled user")
			}
		}
	}
	if req.Role != nil {
		if *req.Role != "admin" && *req.Role != "operator" {
			http.Error(w, `{"error":"role must be admin or operator"}`, http.StatusBadRequest)
			return
		}
		if *req.Role != "admin" {
			adminCount, err := h.users.CountAdmins()
			if err != nil {
				log.Error().Err(err).Msg("auth: failed to count admins for demote check")
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}
			targetUser, err := h.users.GetUserByID(id)
			if err != nil {
				log.Error().Err(err).Int64("user_id", id).Msg("auth: failed to get user for demote check")
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}
			if targetUser.Role == "admin" && adminCount <= 1 {
				http.Error(w, `{"error":"cannot demote the last admin"}`, http.StatusBadRequest)
				return
			}
		}
		updates["role"] = *req.Role
	}

	if len(updates) == 0 {
		http.Error(w, `{"error":"no updates provided"}`, http.StatusBadRequest)
		return
	}

	if err := h.users.UpdateUser(id, updates); err != nil {
		log.Error().Err(err).Int64("user_id", id).Msg("auth: failed to update user")
		http.Error(w, `{"error":"failed to update user"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		log.Error().Err(err).Msg("auth: failed to encode update-user response")
	}
}

// Service-level delegation methods
// These are registered on the chi router in setupRoutes and delegate to s.authHandlers,
// returning 503 Service Unavailable if the handler is not yet initialized (async init).

func (s *Service) handleUserSetupNeeded(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleSetupNeeded(w, r)
}

func (s *Service) handleUserSetup(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleSetup(w, r)
}

func (s *Service) handleUserLogin(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleLogin(w, r)
}

func (s *Service) handleUserLogout(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleLogout(w, r)
}

func (s *Service) handleUserRegister(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleRegister(w, r)
}

func (s *Service) handleAdminCreateInvitation(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleCreateInvitation(w, r)
}

func (s *Service) handleAdminListInvitations(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleListInvitations(w, r)
}

func (s *Service) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleListUsers(w, r)
}

func (s *Service) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	h := s.authHandlers
	s.initMu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	h.handleUpdateUser(w, r)
}
