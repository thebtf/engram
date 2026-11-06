package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

var errMemoryContextInvalid = errors.New("invalid memory context selector")

// handleEnrollMemoryPrincipal godoc
// @Summary Deliberately enroll an existing canonical principal for own memory
// @Description In either auth mode, deliberately enroll a canonical principal/kind pair using a current DB-backed browser session of an enabled admin user. The persisted user and session are revalidated under transaction locks. HMAC-only sessions, master tokens, keycards and synthetic identities cannot enroll; explicit keycard opt-in never falls back to the browser cookie. Omitted/empty principal_kind defaults to human; valid kinds are human, agent, service. Issuing a keycard does not enroll or backfill its principal, and enrollment may precede issuance. Creates no user, credential, project or memory; returns the canonical pair with 201 for first enrollment or 200 if already enrolled.
// @Tags Auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body memoryPrincipalEnrollmentRequest true "Existing canonical principal pair"
// @Success 201 {object} models.MemoryAuthor "New enrollment"
// @Success 200 {object} models.MemoryAuthor "Already enrolled"
// @Failure 400 {string} string "invalid JSON, empty principal or invalid principal_kind"
// @Failure 401 {string} string "authentication required"
// @Failure 403 {string} string "current persisted browser admin required"
// @Failure 500 {string} string "principal enrollment failed"
// @Failure 503 {string} string "memory store unavailable"
// @Router /api/auth/principals [post]
func (s *Service) handleEnrollMemoryPrincipal(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok {
		http.Error(w, "a current persisted browser-admin session is required", http.StatusForbidden)
		return
	}
	subject, ok := identity.SessionBrowserSubject()
	if !ok {
		http.Error(w, "a current persisted browser-admin session is required", http.StatusForbidden)
		return
	}
	sessionID, ok := authenticatedBrowserSessionID(r.Context())
	if !ok || sessionID == "" {
		http.Error(w, "a persisted browser session is required", http.StatusForbidden)
		return
	}
	if s.memoryStore == nil {
		http.Error(w, "memory store unavailable", http.StatusServiceUnavailable)
		return
	}
	var request memoryPrincipalEnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid principal enrollment", http.StatusBadRequest)
		return
	}
	principal, kind, err := normalizeTokenPrincipal(request.Principal, request.PrincipalKind)
	if err != nil || principal == "" {
		http.Error(w, "a nonempty principal and valid principal_kind are required", http.StatusBadRequest)
		return
	}
	created, err := s.memoryStore.EnrollPrincipal(r.Context(), kind, principal, subject.UserID, sessionID)
	if err != nil {
		if errors.Is(err, gormdb.ErrMemoryEnrollmentForbidden) {
			http.Error(w, "a current persisted browser-admin session is required", http.StatusForbidden)
			return
		}
		http.Error(w, "principal enrollment failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	writeJSON(w, models.MemoryAuthor{PrincipalKind: kind, Principal: principal})
}

type memoryPrincipalEnrollmentRequest struct {
	// PrincipalKind is trimmed; omission or an empty trimmed value defaults to human.
	PrincipalKind string `json:"principal_kind" binding:"optional" enums:",human,agent,service"`
	// Principal is trimmed and must remain nonempty after trimming.
	Principal string `json:"principal" binding:"required" minLength:"1"`
}

// storeMemoryContextSchema documents the optional context object for Swag only.
// Runtime decoding still uses RawMessage and rejects unknown context fields.
type storeMemoryContextSchema struct {
	// Kind is required when context is supplied; the object accepts no other fields at runtime.
	Kind string `json:"kind" binding:"required" enums:"self,project,global"`
}

// UnmarshalJSON retains project presence using the ordinary decoder's field
// matching, without copying the entire body into a second raw map.
func (request *storeMemoryRequest) UnmarshalJSON(data []byte) error {
	type plain storeMemoryRequest
	var decoded plain
	wire := struct {
		*plain
		Project json.RawMessage `json:"project"`
	}{plain: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	supplied := len(wire.Project) != 0
	if supplied {
		if bytes.Equal(bytes.TrimSpace(wire.Project), []byte("null")) || json.Unmarshal(wire.Project, &decoded.Project) != nil {
			return fmt.Errorf("project must be a nonempty canonical string when supplied")
		}
	}
	*request = storeMemoryRequest(decoded)
	request.projectSupplied = supplied
	return nil
}

func memoryRequestContext(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, fmt.Errorf("context must be an object with a supported kind")
	}
	var selection struct {
		Kind string `json:"kind"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&selection); err != nil {
		return "", true, fmt.Errorf("invalid context selector")
	}
	switch selection.Kind {
	case models.MemoryContextSelf, models.MemoryContextProject, models.MemoryContextGlobal:
		return selection.Kind, true, nil
	default:
		return "", true, fmt.Errorf("context.kind must be self, project or global")
	}
}

func memoryRequestKey(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var key string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &key) != nil || key == "" || len(key) > 1024 {
		return "", fmt.Errorf("request_key must be a nonempty string of at most 1024 UTF-8 bytes")
	}
	return key, nil
}

func (s *Service) resolveMemoryOwningContext(r *http.Request, kind string, contextSupplied bool, project string, projectSupplied bool) (models.MemoryContext, error) {
	if !contextSupplied {
		kind = models.MemoryContextSelf
		if projectSupplied {
			kind = models.MemoryContextProject
		}
	}
	owning := models.MemoryContext{Kind: kind}
	if kind == models.MemoryContextProject {
		if !projectSupplied || !projectidentity.ValidProjectID(project) {
			return owning, errMemoryContextInvalid
		}
		found, err := s.memoryStore.CanonicalProjectExists(r.Context(), project)
		if err != nil {
			return owning, err
		}
		if !found {
			return owning, gormdb.ErrKnowledgeProjectForbidden
		}
		return owning, nil
	}
	if (kind != models.MemoryContextSelf && kind != models.MemoryContextGlobal) || projectSupplied {
		return owning, errMemoryContextInvalid
	}
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok || identity.Source != auth.SourceClient || identity.KeycardID == "" {
		return owning, gormdb.ErrMemoryPrincipalRequired
	}
	principal, principalKind, ok := identity.MemoryOwner()
	if !ok {
		return owning, gormdb.ErrMemoryPrincipalRequired
	}
	found, err := s.memoryStore.PrincipalEnrolled(r.Context(), principalKind, principal)
	if err != nil {
		return owning, err
	}
	if !found {
		return owning, gormdb.ErrMemoryPrincipalRequired
	}
	if kind == models.MemoryContextSelf {
		owning.Principal, owning.PrincipalKind = principal, principalKind
	}
	return owning, nil
}

func writeMemoryContextError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrMemoryPrincipalRequired), errors.Is(err, gormdb.ErrMemoryRequestForbidden):
		http.Error(w, "memory context access denied", http.StatusForbidden)
	case errors.Is(err, gormdb.ErrMemoryRequestConflict), errors.Is(err, gormdb.ErrMemoryRequestRetired):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, gormdb.ErrKnowledgeProjectForbidden), errors.Is(err, gormlib.ErrRecordNotFound):
		http.Error(w, "memory context or record not found", http.StatusNotFound)
	case errors.Is(err, errMemoryContextInvalid):
		http.Error(w, "invalid memory context", http.StatusBadRequest)
	default:
		http.Error(w, "memory context unavailable", http.StatusInternalServerError)
	}
}

// handleContextMemoryRead leaves ordinary no-opt-in project reads on their
// established reader, while self and explicit typed selectors use exact ownership.
func (s *Service) handleContextMemoryRead(w http.ResponseWriter, r *http.Request, id int64) bool {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeMemoryContextError(w, errMemoryContextInvalid)
		return true
	}
	projects, projectSupplied := params["project"]
	contexts, contextSupplied := params["context"]
	_, explicitIdentity := r.Header["X-Engram-Identity"]
	if projectSupplied && len(projects) != 1 {
		writeMemoryContextError(w, errMemoryContextInvalid)
		return true
	}
	if projectSupplied && !contextSupplied && !explicitIdentity {
		return false
	}
	if s.memoryStore == nil {
		return false
	}
	project, kind := "", ""
	if projectSupplied {
		if len(projects) != 1 || !projectidentity.ValidProjectID(projects[0]) {
			writeMemoryContextError(w, errMemoryContextInvalid)
			return true
		}
		project = projects[0]
	}
	if contextSupplied {
		if len(contexts) != 1 {
			writeMemoryContextError(w, errMemoryContextInvalid)
			return true
		}
		kind = contexts[0]
	}
	owning, err := s.resolveMemoryOwningContext(r, kind, contextSupplied, project, projectSupplied)
	if err != nil {
		writeMemoryContextError(w, err)
		return true
	}
	memory, err := s.memoryStore.GetInContext(r.Context(), id, owning, project, memoryVisibilityCaller(r.Context(), ""))
	if err != nil {
		writeMemoryContextError(w, err)
		return true
	}
	writeJSON(w, jsonSafeMemory(memory))
	return true
}
