// Package worker provides memory REST handlers for the dashboard.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// storeMemoryRequest is the JSON body for POST /api/memories.
//
// T004 (engram vNext Milestone F TG1) added the optional `privacy_scope`
// and `session_id` fields. Both are honored only when ENGRAM_VNEXT_F_ENABLED
// is "true"; with the flag OFF the request shape is byte-identical to v6.4.x
// (Go's encoding/json silently drops unknown fields by default, and the
// existing fields are unchanged).
type storeMemoryRequest struct {
	Project         string   `json:"project"`
	Content         string   `json:"content"`
	Tags            []string `json:"tags,omitempty"`
	SourceAgent     string   `json:"source_agent,omitempty"`
	PrivacyScope    string   `json:"privacy_scope,omitempty"` // T004 — vNext F, 4-tier enum
	SessionID       string   `json:"session_id,omitempty"`    // T004 — caller session for SourceSessions
	AgentVisibility string   `json:"agent_visibility,omitempty" enums:"private,shared"`
	Domain          string   `json:"domain,omitempty"`
}

type suppressMemoryRequest struct {
	Reason string `json:"reason,omitempty"`
}

type suppressMemoriesRequest struct {
	IDs    []int64 `json:"ids"`
	Reason string  `json:"reason,omitempty"`
}

type memoryActionReceipt struct {
	Status string `json:"status"`
	Action string `json:"action"`
	ID     int64  `json:"id"`
	Reason string `json:"reason,omitempty"`
}

type memoryAuditResponse struct {
	MemoryID int64                      `json:"memory_id"`
	Entries  []memoryAuditEntryResponse `json:"entries"`
}

type memoryAuditEntryResponse struct {
	ID                 int64     `json:"id"`
	MemoryID           int64     `json:"memory_id"`
	Action             string    `json:"action"`
	Actor              string    `json:"actor"`
	SourceSessionID    string    `json:"source_session_id,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	BeforeStatePresent bool      `json:"before_state_present"`
	AfterStatePresent  bool      `json:"after_state_present"`
	CreatedAt          time.Time `json:"created_at"`
}

// isValidPrivacyScopeREST mirrors the migration 125 CHECK constraint enum.
// Duplicated from internal/mcp/tools_memory.go to keep the worker layer
// free of MCP imports; the canonical contract lives in the spec.
func isValidPrivacyScopeREST(s string) bool {
	switch s {
	case "private", "project", "shared", "global":
		return true
	default:
		return false
	}
}

func applyPrincipalMemoryMetadataREST(ctx context.Context, mem *models.Memory, agentVisibility, domain string) error {
	visibility := strings.TrimSpace(agentVisibility)
	if visibility != "" && !models.IsValidAgentVisibility(visibility) {
		return fmt.Errorf("invalid_agent_visibility: %q must be one of private, shared", visibility)
	}

	normalizedDomain := strings.TrimSpace(domain)
	var ownerPrincipal, ownerPrincipalKind string
	if id, ok := auth.IdentityFrom(ctx); ok {
		if principal, principalKind, hasOwner := id.MemoryOwner(); hasOwner {
			ownerPrincipal = principal
			ownerPrincipalKind = principalKind
		}
	}
	caller := scope.KeycardContext{
		Principal:     ownerPrincipal,
		PrincipalKind: ownerPrincipalKind,
	}
	decision := scope.DomainOwnershipPolicy{}.Decide(caller, scope.DomainPolicyRequest{
		Operation:          scope.DomainOperationWrite,
		Domain:             normalizedDomain,
		OwnerPrincipal:     ownerPrincipal,
		OwnerPrincipalKind: ownerPrincipalKind,
	})
	if !decision.Allowed {
		return fmt.Errorf("invalid_domain: %s", decision.Reason)
	}

	mem.Domain = normalizedDomain
	mem.OwnerPrincipal = ownerPrincipal
	mem.OwnerPrincipalKind = ownerPrincipalKind
	if visibility != "" {
		if mem.OwnerPrincipal == "" {
			return fmt.Errorf("invalid_agent_visibility: principal is required for agent_visibility")
		}
		mem.AgentVisibility = visibility
	} else if mem.OwnerPrincipal != "" {
		mem.AgentVisibility = models.AgentVisibilityShared
	}
	return nil
}

// handleStoreMemoryExplicit godoc
// @Summary Store an explicit memory note
// @Description Creates a new memory entry for the given project.
// @Tags Memories
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body storeMemoryRequest true "Memory to store"
// @Success 201 {object} models.Memory
// @Failure 400 {string} string "bad request"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories [post]
func (s *Service) handleStoreMemoryExplicit(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	var req storeMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	mem := &models.Memory{
		Project:     req.Project,
		Content:     req.Content,
		Tags:        req.Tags,
		SourceAgent: req.SourceAgent,
	}

	// T004 (engram vNext Milestone F TG1) — populate the new lifecycle/
	// identity fields when ENGRAM_VNEXT_F_ENABLED=true. With the flag OFF
	// the new columns get their DB defaults (privacy_scope='project',
	// source_workstation_id='', source_sessions=ARRAY[]::TEXT[]) and the
	// response shape stays v6.4.x-identical via the omitempty JSON tags
	// already on Memory.
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
		if req.PrivacyScope != "" {
			if !isValidPrivacyScopeREST(req.PrivacyScope) {
				http.Error(w, "invalid privacy_scope: must be one of private, project, shared, global", http.StatusBadRequest)
				return
			}
			mem.PrivacyScope = req.PrivacyScope
		}
		if id, ok := auth.IdentityFrom(r.Context()); ok {
			mem.SourceWorkstationID = id.WorkstationID()
		}
		if req.SessionID != "" {
			mem.SourceSessions = []string{req.SessionID}
		}
		// Codex P1 cycle-5 fix on b5ac7ec: mirror the MCP-side guard
		// (`internal/mcp/tools_memory.go` private-write check from
		// `4cb71be`/`b5ac7ec`) on the REST surface so the two paths do
		// not diverge. scope.Resolve fail-closes private memories whose
		// source_workstation_id is empty (`internal/scope/filter.go`),
		// so persisting a private write from a non-SourceClient caller
		// (master/session, or no identity) would create a permanently-
		// unreadable row.
		if mem.PrivacyScope == "private" && mem.SourceWorkstationID == "" {
			http.Error(w, "invalid privacy_scope: private requires a non-empty workstation identity from a SourceClient keycard (master/session sources cannot write private-scope memories)", http.StatusBadRequest)
			return
		}
	}
	if err := applyPrincipalMemoryMetadataREST(r.Context(), mem, req.AgentVisibility, req.Domain); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	domainDecision, err := s.checkDomainWriteREST(r.Context(), mem, req.SessionID)
	if err != nil {
		if errors.Is(err, principalmemory.ErrDomainWriteRejected) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		log.Error().Err(err).Str("project", req.Project).Str("domain", mem.Domain).Msg("domain registry check failed")
		http.Error(w, "domain registry check failed", http.StatusInternalServerError)
		return
	}
	if domainDecision != nil && !domainDecision.Allowed {
		http.Error(w, "domain write rejected", http.StatusForbidden)
		return
	}

	created, err := s.memoryStore.Create(r.Context(), mem)
	if err != nil {
		log.Error().Err(err).Str("project", req.Project).Msg("store memory failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeStoreMemoryResponse(w, created, domainDecision)
}

func (s *Service) checkDomainWriteREST(ctx context.Context, mem *models.Memory, sourceSessionID string) (*principalmemory.DomainWriteDecision, error) {
	if mem == nil || strings.TrimSpace(mem.Domain) == "" {
		return nil, nil
	}
	svc := s.currentDomainRegistryService()
	if svc == nil {
		return nil, nil
	}
	return svc.CheckWrite(ctx, principalmemory.DomainWriteCheckRequest{
		Project:         mem.Project,
		Domain:          mem.Domain,
		Writer:          principalmemory.PrincipalRef{Principal: mem.OwnerPrincipal, PrincipalKind: mem.OwnerPrincipalKind},
		SourceSessionID: strings.TrimSpace(sourceSessionID),
	})
}

func (s *Service) currentDomainRegistryService() domainRegistryService {
	s.initMu.RLock()
	svc := s.domainRegistryService
	s.initMu.RUnlock()
	return svc
}

func writeStoreMemoryResponse(w http.ResponseWriter, mem *models.Memory, decision *principalmemory.DomainWriteDecision) {
	safe := jsonSafeMemory(mem)
	if decision == nil || decision.Warning == nil {
		writeJSON(w, safe)
		return
	}
	resp := struct {
		*models.Memory
		DomainWarning     *principalmemory.DomainWriteWarning `json:"domain_warning,omitempty"`
		DomainAuditStatus string                              `json:"domain_audit_status,omitempty"`
	}{
		Memory:            safe,
		DomainWarning:     decision.Warning,
		DomainAuditStatus: decision.AuditStatus,
	}
	writeJSON(w, resp)
}

// handleListMemories godoc
// @Summary List memory notes for a project
// @Description Returns stored memories for the given project, newest first.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param project query string true "Project identifier"
// @Param limit query int false "Maximum number of results (default 50)"
// @Success 200 {array} models.Memory
// @Failure 400 {string} string "project is required"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories [get]
func (s *Service) handleListMemories(w http.ResponseWriter, r *http.Request) {
	store := s.memListStore()
	if store == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}

	const maxLimit = 500
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		if n > maxLimit {
			http.Error(w, fmt.Sprintf("limit must not exceed %d", maxLimit), http.StatusBadRequest)
			return
		}
		limit = n
	}

	// Codex P1 cycle-11 fix on 034f14f: REST GET /api/memories must enforce
	// the same vNext-F visibility model as MCP recall surfaces, otherwise a
	// private memory written via POST /api/memories (allowed since T004 +
	// cycle-5 c6006f7) can be retrieved here by any caller knowing the
	// project — bypassing scope.Resolve. This is the 4th cross-surface
	// symmetry break the review cycles have closed (after MCP store, REST
	// store, MCP recall, MCP recall_memory). Under flag ON: build caller
	// KeycardContext from auth.Identity, use ListWithOffset batch-loop so
	// scope-invisible newest rows do not truncate the visible result set
	// before the requested limit is reached. Flag-OFF path preserves the
	// original single-call List shape for v6.4.x byte-identity (RI-F1).
	mems, err := listVisibleMemoriesREST(r.Context(), store, project, limit)
	if err != nil {
		log.Error().Err(err).Str("project", project).Msg("list memories failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Return an empty array rather than null when there are no results.
	if len(mems) == 0 {
		writeJSON(w, make([]*models.Memory, 0))
		return
	}

	writeJSON(w, jsonSafeMemories(mems))
}

func jsonSafeMemories(mems []*models.Memory) []*models.Memory {
	safe := make([]*models.Memory, 0, len(mems))
	for _, mem := range mems {
		safe = append(safe, jsonSafeMemory(mem))
	}

	return safe
}

func jsonSafeMemory(mem *models.Memory) *models.Memory {
	if mem == nil {
		return nil
	}

	copy := *mem
	copy.CreatedAt = jsonSafeTime(copy.CreatedAt)
	copy.UpdatedAt = jsonSafeTime(copy.UpdatedAt)
	copy.DeletedAt = jsonSafeTimePtr(copy.DeletedAt)
	copy.LastRetrievedAt = jsonSafeTimePtr(copy.LastRetrievedAt)
	copy.LastConfirmed = jsonSafeTimePtr(copy.LastConfirmed)
	copy.ReviewAfter = jsonSafeTimePtr(copy.ReviewAfter)
	copy.ValidFrom = jsonSafeTimePtr(copy.ValidFrom)
	copy.ValidUntil = jsonSafeTimePtr(copy.ValidUntil)
	copy.ImportanceBase = finiteOrZero(copy.ImportanceBase)
	copy.TsAlpha = finiteOrZero(copy.TsAlpha)
	copy.TsBeta = finiteOrZero(copy.TsBeta)
	copy.Confidence = finiteOrZero(copy.Confidence)
	copy.Stability = finiteOrZero(copy.Stability)
	copy.Retrievability = finiteOrZero(copy.Retrievability)
	return &copy
}

func jsonSafeTime(value time.Time) time.Time {
	if !canMarshalJSONTime(value) {
		return time.Time{}
	}

	return value
}

func jsonSafeTimePtr(value *time.Time) *time.Time {
	if value == nil || !canMarshalJSONTime(*value) {
		return nil
	}

	safe := *value
	return &safe
}

func canMarshalJSONTime(value time.Time) bool {
	_, err := value.MarshalJSON()
	return err == nil
}

func finiteOrZero(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}

	return value
}

func memoryVisibilityCaller(ctx context.Context, sessionID string) scope.KeycardContext {
	caller := scope.KeycardContext{SessionID: sessionID}
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
		caller.Principal = id.Principal
		if _, principalKind, hasOwner := id.MemoryOwner(); hasOwner {
			caller.PrincipalKind = principalKind
		} else {
			caller.PrincipalKind = string(id.PrincipalKind)
		}
	}
	return caller
}

func memoryVisibilityOptions() scope.MemoryVisibilityOptions {
	return scope.MemoryVisibilityOptions{
		ApplyPrivacyScope: os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true",
	}
}

// memoryVisibleREST applies the same caller-aware read policy as list and search.
func memoryVisibleREST(ctx context.Context, mem *models.Memory) bool {
	return scope.ResolveMemory(memoryVisibilityCaller(ctx, ""), mem, memoryVisibilityOptions())
}

// listVisibleMemoriesREST returns up to `limit` memories from the given project
// that are visible to the caller. It always pages with ListWithOffset so
// invisible principal-private rows cannot truncate visible results; the
// ENGRAM_VNEXT_F_ENABLED flag gates only the legacy privacy_scope layer.
func listVisibleMemoriesREST(ctx context.Context, store memoryListStore, project string, limit int) ([]*models.Memory, error) {
	caller := memoryVisibilityCaller(ctx, "")
	opts := memoryVisibilityOptions()
	visible := make([]*models.Memory, 0, limit)
	const batchSize = 500
	offset := 0
	for len(visible) < limit {
		batch, err := store.ListWithOffset(ctx, project, batchSize, offset)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, mem := range batch {
			if !scope.ResolveMemory(caller, mem, opts) {
				continue
			}
			visible = append(visible, mem)
			if len(visible) >= limit {
				break
			}
		}
		offset += len(batch)
		if len(batch) < batchSize {
			break
		}
	}
	return visible, nil
}

func memoryDomainManageAllowedREST(ctx context.Context, mem *models.Memory) bool {
	return scope.ResolveMemoryManage(memoryVisibilityCaller(ctx, ""), mem)
}

func parseMemoryAuditLimit(raw string) (int, error) {
	const (
		defaultLimit = 50
		maxLimit     = 200
	)

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultLimit, nil
	}

	limit, err := strconv.Atoi(trimmed)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if limit > maxLimit {
		return 0, fmt.Errorf("limit must not exceed %d", maxLimit)
	}
	return limit, nil
}

func mapMemoryAuditResponse(memoryID int64, entries []dbgorm.AuditLogEntry) memoryAuditResponse {
	response := memoryAuditResponse{
		MemoryID: memoryID,
		Entries:  make([]memoryAuditEntryResponse, 0, len(entries)),
	}

	for _, entry := range entries {
		entryMemoryID := memoryID
		if entry.MemoryID != nil {
			entryMemoryID = *entry.MemoryID
		}
		response.Entries = append(response.Entries, memoryAuditEntryResponse{
			ID:                 entry.ID,
			MemoryID:           entryMemoryID,
			Action:             entry.Action,
			Actor:              entry.Actor,
			SourceSessionID:    entry.SourceSessionID,
			Reason:             entry.Reason,
			BeforeStatePresent: entry.BeforeState != nil,
			AfterStatePresent:  entry.AfterState != nil,
			CreatedAt:          jsonSafeTime(entry.CreatedAt),
		})
	}

	return response
}

// memoryListStore is the subset of the MemoryStore surface that
// listVisibleMemoriesREST needs. Defined as a small interface so the
// function can be unit-tested with a fake without pulling in the full
// store dependency.
type memoryListStore interface {
	List(ctx context.Context, project string, limit int) ([]*models.Memory, error)
	ListWithOffset(ctx context.Context, project string, limit int, offset int) ([]*models.Memory, error)
}

// memoryGetStore is the subset of MemoryStore needed for exact-ID reads.
// It permits focused handler tests without a database.
type memoryGetStore interface {
	Get(ctx context.Context, id int64) (*models.Memory, error)
}

func (s *Service) memGetStore() memoryGetStore {
	if s.memoryGetStoreSeam != nil {
		return s.memoryGetStoreSeam
	}
	if s.memoryStore == nil {
		return nil
	}
	return s.memoryStore
}

// handleGetMemoryByID godoc
// @Summary Get a memory note by ID
// @Description Returns one active memory entry by numeric ID.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Memory ID"
// @Success 200 {object} models.Memory
// @Failure 400 {string} string "invalid id"
// @Failure 404 {string} string "not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories/{id} [get]
func (s *Service) handleGetMemoryByID(w http.ResponseWriter, r *http.Request) {
	store := s.memGetStore()
	if store == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return
	}

	memory, err := store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("get memory failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !memoryVisibleREST(r.Context(), memory) {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}

	writeJSON(w, jsonSafeMemory(memory))
}

// injectionCandidateStore is the subset of the MemoryStore surface that
// listVisibleForInjection needs. Defined as a small interface to allow
// unit-testing without the full store dependency.
type injectionCandidateStore interface {
	ListForInjection(ctx context.Context, project string, limit int) ([]*models.Memory, error)
}

// listVisibleForInjection fetches injection candidates and removes rows that
// the caller cannot see per scope.ResolveMemory. It operates on the injection
// candidate set (importance-ordered, topK*3 pre-inflated by the caller).
//
// T004 (codex P1 PR #221): ListForInjection previously returned every active
// row for the project without checking visibility, so a private memory written
// by workstation or principal A could be injected into context for caller B in
// the same project. This helper closes that gap at the worker/mcp boundary
// without adding auth/scope imports to the db/gorm layer.
func listVisibleForInjection(ctx context.Context, store injectionCandidateStore, project string, limit int) ([]*models.Memory, error) {
	candidates, err := store.ListForInjection(ctx, project, limit)
	if err != nil {
		return nil, err
	}
	caller := memoryVisibilityCaller(ctx, "")
	opts := memoryVisibilityOptions()
	visible := make([]*models.Memory, 0, len(candidates))
	for _, mem := range candidates {
		if scope.ResolveMemory(caller, mem, opts) {
			visible = append(visible, mem)
		}
	}
	return visible, nil
}

// handleGetMemoryAudit godoc
// @Summary Get memory audit history
// @Description Returns safe audit summaries for a memory visible to the operator.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Memory ID"
// @Param limit query int false "Maximum number of audit rows (default 50, max 200)"
// @Success 200 {object} memoryAuditResponse
// @Failure 400 {string} string "invalid id or limit"
// @Failure 404 {string} string "not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories/{id}/audit [get]
func (s *Service) handleGetMemoryAudit(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}
	if s.auditStore == nil {
		http.Error(w, "audit store not available", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return
	}

	limit, err := parseMemoryAuditLimit(r.URL.Query().Get("limit"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	memory, err := s.memoryStore.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("get memory before audit failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if memory == nil || !memoryDomainManageAllowedREST(r.Context(), memory) {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}

	entries, err := s.auditStore.GetByMemory(r.Context(), id, limit)
	if err != nil {
		log.Error().Err(err).Int64("id", id).Msg("get memory audit failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, mapMemoryAuditResponse(id, entries))
}

// handleDeleteMemoryByID godoc
// @Summary Delete a memory note by ID
// @Description Soft-deletes a memory entry by its numeric ID.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Memory ID"
// @Success 200 {object} map[string]string
// @Failure 400 {string} string "invalid id"
// @Failure 404 {string} string "not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories/{id} [delete]
func (s *Service) handleDeleteMemoryByID(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return
	}

	before, err := s.memoryStore.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("get memory before delete failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if before == nil {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}
	if !memoryDomainManageAllowedREST(r.Context(), before) {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}

	if err := s.memoryStore.Delete(r.Context(), id); err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("delete memory failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

// handleSuppressMemoryByID godoc
// @Summary Suppress a memory note by ID
// @Description Soft-deletes a memory entry by its numeric ID and returns an operator action receipt.
// @Tags Memories
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Memory ID"
// @Param body body suppressMemoryRequest false "Suppression reason"
// @Success 200 {object} memoryActionReceipt
// @Failure 400 {string} string "invalid id"
// @Failure 404 {string} string "not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories/{id}/suppress [post]
func (s *Service) handleSuppressMemoryByID(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return
	}

	var req suppressMemoryRequest
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	before, err := s.memoryStore.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("get memory before suppress failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if before == nil {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}
	if !memoryDomainManageAllowedREST(r.Context(), before) {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}

	if err := s.memoryStore.Delete(r.Context(), id); err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("suppress memory failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	reason := strings.TrimSpace(req.Reason)
	log.Info().Int64("id", id).Str("reason", reason).Msg("memory suppressed")
	writeJSON(w, memoryActionReceipt{
		Status: "ok",
		Action: "suppress",
		ID:     id,
		Reason: reason,
	})
}

// handleSuppressMemories godoc
// @Summary Suppress multiple memory notes
// @Description Validates all requested memory IDs before soft-deleting them and returns operator action receipts.
// @Tags Memories
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body suppressMemoriesRequest true "Memory IDs and suppression reason"
// @Success 200 {array} memoryActionReceipt
// @Failure 400 {string} string "invalid request"
// @Failure 404 {string} string "not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories/suppress [post]
func (s *Service) handleSuppressMemories(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	var req suppressMemoriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	seen := map[int64]struct{}{}
	ids := make([]int64, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 {
			http.Error(w, "invalid memory id", http.StatusBadRequest)
			return
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		http.Error(w, "at least one memory id is required", http.StatusBadRequest)
		return
	}

	before := make([]*models.Memory, 0, len(ids))
	for _, id := range ids {
		memory, err := s.memoryStore.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				http.Error(w, "memory not found", http.StatusNotFound)
				return
			}
			log.Error().Err(err).Int64("id", id).Msg("get memory before bulk suppress failed")
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if memory == nil || !memoryDomainManageAllowedREST(r.Context(), memory) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		before = append(before, memory)
	}

	reason := strings.TrimSpace(req.Reason)
	receipts := make([]memoryActionReceipt, 0, len(before))
	for _, memory := range before {
		if err := s.memoryStore.Delete(r.Context(), memory.ID); err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				http.Error(w, "memory not found", http.StatusNotFound)
				return
			}
			log.Error().Err(err).Int64("id", memory.ID).Msg("bulk suppress memory failed")
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		receipts = append(receipts, memoryActionReceipt{
			Status: "ok",
			Action: "suppress",
			ID:     memory.ID,
			Reason: reason,
		})
	}

	log.Info().Int("count", len(receipts)).Str("reason", reason).Msg("memories suppressed")
	writeJSON(w, receipts)
}

const memoryCollectionSelectionDomain = "memory"

var (
	errMemorySelectionDenied   = errors.New("memory selection denied")
	errMemorySelectionConflict = errors.New("memory selection conflict")
)

type memoryCollectionSelectionRequest struct {
	Kind        dbgorm.CollectionSelectionKind     `json:"kind"`
	Targets     []dbgorm.CollectionSelectionTarget `json:"targets,omitempty"`
	Cursor      string                             `json:"cursor,omitempty"`
	Project     string                             `json:"project,omitempty"`
	ExcludedIDs []string                           `json:"excluded_ids,omitempty"`
}

type memoryCollectionSelectionSnapshotRequest struct {
	Selection memoryCollectionSelectionRequest `json:"selection"`
}

type memoryCollectionSelectionOperationRequest struct {
	RequestID string                                      `json:"request_id"`
	Action    string                                      `json:"action"`
	Selection memoryCollectionSelectionOperationSelection `json:"selection"`
}

type memoryCollectionSelectionOperationSelection struct {
	Kind    dbgorm.CollectionSelectionKind `json:"kind"`
	Version int64                          `json:"selection_version"`
	Token   string                         `json:"selection_token,omitempty"`
}

type memoryCollectionSelectionPageRequest struct {
	Project string `json:"project"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit"`
}

type memoryCollectionSelectionPageCursor struct {
	Project  string `json:"project"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
	Revision string `json:"revision"`
}

type memoryCollectionSelectionResponse struct {
	Kind                   dbgorm.CollectionSelectionKind `json:"kind"`
	Version                int64                          `json:"selection_version"`
	Token                  string                         `json:"selection_token,omitempty"`
	TargetCount            int                            `json:"target_count"`
	ReconfirmationRequired bool                           `json:"reconfirmation_required"`
	ReconfirmationReason   string                         `json:"reconfirmation_reason,omitempty"`
}

type memoryCollectionSelectionPageResponse struct {
	Cursor     string                             `json:"cursor"`
	Targets    []dbgorm.CollectionSelectionTarget `json:"targets"`
	NextCursor string                             `json:"next_cursor,omitempty"`
	Total      int                                `json:"total"`
}

type memoryCollectionSelectionItemResponse struct {
	TargetID        string `json:"target_id"`
	Outcome         string `json:"outcome"`
	ObservedVersion *int   `json:"observed_version,omitempty"`
}

type memoryCollectionSelectionCurrentState struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Version int    `json:"version"`
}

type memoryCollectionSelectionReadback struct {
	Authoritative   bool                                    `json:"authoritative"`
	Kind            string                                  `json:"kind"`
	CurrentState    []memoryCollectionSelectionCurrentState `json:"current_state,omitempty"`
	OperationStatus string                                  `json:"operation_status,omitempty"`
}

type memoryCollectionSelectionOperationResponse struct {
	RequestID      string                                  `json:"request_id"`
	OperationState string                                  `json:"operation_state"`
	ItemResults    []memoryCollectionSelectionItemResponse `json:"item_results"`
	Readback       *memoryCollectionSelectionReadback      `json:"readback,omitempty"`
}

// registerMemoryCollectionOperationRoutes declares Memory's domain-local
// selection and operation surface. T031 owns invoking it from setupRoutes.
func (s *Service) registerMemoryCollectionOperationRoutes(r chi.Router) {
	r.Post("/api/memories/selection", s.handleMemoryCollectionSelection)
	r.Post("/api/memories/selection/current", s.handleMemoryCollectionSelectionCurrent)
	r.Post("/api/memories/selection/page", s.handleMemoryCollectionSelectionPage)
	r.Post("/api/memories/operations", s.handleMemoryCollectionSelectionOperation)
}

func memoryCollectionSelectionScope(r *http.Request) (dbgorm.CollectionSelectionScope, error) {
	identity, found := auth.IdentityFrom(r.Context())
	if !found {
		return dbgorm.CollectionSelectionScope{}, errMemorySelectionDenied
	}
	subject, found := identity.SessionBrowserSubject()
	if !found {
		return dbgorm.CollectionSelectionScope{}, errMemorySelectionDenied
	}
	sessionID, found := operatorCodeSessionID(r)
	if !found {
		return dbgorm.CollectionSelectionScope{}, errMemorySelectionDenied
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("memory-collection-scope/v1\x00%d\x00%s", subject.UserID, sessionID)))
	return dbgorm.CollectionSelectionScope{
		SubjectUserID:      subject.UserID,
		SessionID:          sessionID,
		Domain:             memoryCollectionSelectionDomain,
		ContextFingerprint: fmt.Sprintf("sha256:%x", fingerprint),
		AuthorizationEpoch: 1,
		CollectionVersion:  1,
	}, nil
}

func (s *Service) memoryCollectionSelectionStore() (*dbgorm.CollectionSelectionStore, error) {
	if s == nil || s.memoryStore == nil || s.memoryStore.GetDB() == nil {
		return nil, errors.New("memory store not available")
	}
	return dbgorm.NewCollectionSelectionStore(s.memoryStore.GetDB()), nil
}

func validMemoryCollectionProject(project string) bool {
	return project != "" && project == strings.TrimSpace(project) && len(project) <= 256
}

func memoryCollectionFilterFingerprint(project string) string {
	digest := sha256.Sum256([]byte("memory-collection-filter/v1\x00" + project))
	return fmt.Sprintf("sha256:%x", digest)
}

func memoryCollectionRevision(project string, targets []dbgorm.CollectionSelectionTarget) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("memory-collection-revision/v1\x00" + project + "\x00"))
	for _, target := range targets {
		_, _ = hash.Write([]byte(target.ID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(strconv.FormatUint(target.ExpectedVersion, 10)))
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func (s *Service) memoryCollectionTargets(ctx context.Context, project string) ([]dbgorm.CollectionSelectionTarget, error) {
	if !validMemoryCollectionProject(project) {
		return nil, dbgorm.ErrCollectionSelectionInvalid
	}
	if s == nil || s.memoryStore == nil {
		return nil, errors.New("memory store not available")
	}
	memories, err := listVisibleMemoriesREST(ctx, s.memoryStore, project, dbgorm.CollectionSelectionMaxTargets+1)
	if err != nil {
		return nil, err
	}
	if len(memories) > dbgorm.CollectionSelectionMaxTargets {
		return nil, dbgorm.ErrCollectionSelectionInvalid
	}
	targets := make([]dbgorm.CollectionSelectionTarget, 0, len(memories))
	for _, memory := range memories {
		if memoryDomainManageAllowedREST(ctx, memory) {
			targets = append(targets, dbgorm.CollectionSelectionTarget{ID: strconv.FormatInt(memory.ID, 10), ExpectedVersion: uint64(memory.Version)})
		}
	}
	return targets, nil
}

func (s *Service) memoryCollectionExplicitTargets(ctx context.Context, targets []dbgorm.CollectionSelectionTarget) ([]dbgorm.CollectionSelectionTarget, error) {
	if s == nil || s.memoryStore == nil || len(targets) == 0 || len(targets) > dbgorm.CollectionSelectionMaxTargets {
		return nil, dbgorm.ErrCollectionSelectionInvalid
	}
	resolved := make([]dbgorm.CollectionSelectionTarget, 0, len(targets))
	seen := make(map[int64]struct{}, len(targets))
	for _, target := range targets {
		id, err := strconv.ParseInt(target.ID, 10, 64)
		if err != nil || id <= 0 {
			return nil, dbgorm.ErrCollectionSelectionInvalid
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, dbgorm.ErrCollectionSelectionInvalid
		}
		seen[id] = struct{}{}
		memory, err := s.memoryStore.Get(ctx, id)
		if err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				return nil, errMemorySelectionDenied
			}
			return nil, err
		}
		if memory == nil || !memoryVisibleREST(ctx, memory) || !memoryDomainManageAllowedREST(ctx, memory) {
			return nil, errMemorySelectionDenied
		}
		resolved = append(resolved, dbgorm.CollectionSelectionTarget{ID: target.ID, ExpectedVersion: uint64(memory.Version)})
	}
	return resolved, nil
}

func newMemoryCollectionSelectionResponse(selection dbgorm.CollectionSelection) memoryCollectionSelectionResponse {
	return memoryCollectionSelectionResponse{
		Kind:                   selection.Kind,
		Version:                selection.Version,
		Token:                  selection.Token,
		TargetCount:            len(selection.Targets) - len(selection.ExcludedIDs),
		ReconfirmationRequired: selection.ReconfirmationRequired,
		ReconfirmationReason:   string(selection.ReconfirmationReason),
	}
}

func memoryCollectionPageCursorEncode(cursor memoryCollectionSelectionPageCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(encoded) > 512 {
		return "", dbgorm.ErrCollectionSelectionInvalid
	}
	return encoded, nil
}

func memoryCollectionPageCursorDecode(value string) (memoryCollectionSelectionPageCursor, error) {
	if value == "" || len(value) > 512 {
		return memoryCollectionSelectionPageCursor{}, dbgorm.ErrCollectionSelectionInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return memoryCollectionSelectionPageCursor{}, dbgorm.ErrCollectionSelectionInvalid
	}
	var cursor memoryCollectionSelectionPageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || !validMemoryCollectionProject(cursor.Project) || cursor.Offset < 0 || cursor.Limit < 1 || cursor.Limit > dbgorm.CollectionPageMaxSize || !strings.HasPrefix(cursor.Revision, "sha256:") {
		return memoryCollectionSelectionPageCursor{}, dbgorm.ErrCollectionSelectionInvalid
	}
	return cursor, nil
}

func (s *Service) memoryCollectionPage(ctx context.Context, project, cursor string, limit int) (memoryCollectionSelectionPageResponse, error) {
	if limit < 1 || limit > dbgorm.CollectionPageMaxSize || !validMemoryCollectionProject(project) {
		return memoryCollectionSelectionPageResponse{}, dbgorm.ErrCollectionSelectionInvalid
	}
	targets, err := s.memoryCollectionTargets(ctx, project)
	if err != nil {
		return memoryCollectionSelectionPageResponse{}, err
	}
	revision := memoryCollectionRevision(project, targets)
	offset := 0
	if cursor != "" {
		decoded, err := memoryCollectionPageCursorDecode(cursor)
		if err != nil {
			return memoryCollectionSelectionPageResponse{}, err
		}
		if decoded.Project != project || decoded.Limit != limit || decoded.Revision != revision {
			return memoryCollectionSelectionPageResponse{}, dbgorm.ErrCollectionSelectionReconfirmationRequired
		}
		offset = decoded.Offset
	}
	if offset >= len(targets) {
		return memoryCollectionSelectionPageResponse{}, dbgorm.ErrCollectionSelectionInvalid
	}
	end := offset + limit
	if end > len(targets) {
		end = len(targets)
	}
	pageCursor, err := memoryCollectionPageCursorEncode(memoryCollectionSelectionPageCursor{Project: project, Offset: offset, Limit: limit, Revision: revision})
	if err != nil {
		return memoryCollectionSelectionPageResponse{}, err
	}
	response := memoryCollectionSelectionPageResponse{Cursor: pageCursor, Targets: append([]dbgorm.CollectionSelectionTarget(nil), targets[offset:end]...), Total: len(targets)}
	if end < len(targets) {
		response.NextCursor, err = memoryCollectionPageCursorEncode(memoryCollectionSelectionPageCursor{Project: project, Offset: end, Limit: limit, Revision: revision})
		if err != nil {
			return memoryCollectionSelectionPageResponse{}, err
		}
	}
	return response, nil
}

const memorySelectionForbiddenMessage = "memory selection forbidden"

func memoryCollectionSelectionSnapshotError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMemorySelectionDenied):
		http.Error(w, "memory selection unavailable", http.StatusNotFound)
	case errors.Is(err, dbgorm.ErrCollectionSelectionReconfirmationRequired):
		http.Error(w, "memory selection is stale", http.StatusPreconditionFailed)
	case errors.Is(err, dbgorm.ErrCollectionSelectionInvalid):
		http.Error(w, "invalid memory selection", http.StatusBadRequest)
	default:
		log.Error().Err(err).Msg("memory selection snapshot failed")
		http.Error(w, "memory selection failed", http.StatusInternalServerError)
	}
}

func (s *Service) handleMemoryCollectionSelection(w http.ResponseWriter, r *http.Request) {
	var request memoryCollectionSelectionSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid memory selection", http.StatusBadRequest)
		return
	}
	scope, err := memoryCollectionSelectionScope(r)
	if err != nil {
		http.Error(w, memorySelectionForbiddenMessage, http.StatusForbidden)
		return
	}
	store, err := s.memoryCollectionSelectionStore()
	if err != nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}
	selection := dbgorm.CollectionSelection{Kind: request.Selection.Kind}
	switch request.Selection.Kind {
	case dbgorm.CollectionSelectionNone:
	case dbgorm.CollectionSelectionExplicit:
		selection.Targets, err = s.memoryCollectionExplicitTargets(r.Context(), request.Selection.Targets)
	case dbgorm.CollectionSelectionPage:
		cursor, decodeErr := memoryCollectionPageCursorDecode(request.Selection.Cursor)
		if decodeErr != nil {
			err = decodeErr
			break
		}
		page, pageErr := s.memoryCollectionPage(r.Context(), cursor.Project, request.Selection.Cursor, cursor.Limit)
		if pageErr != nil {
			err = pageErr
			break
		}
		selection.Cursor = page.Cursor
		selection.Targets = page.Targets
	case dbgorm.CollectionSelectionFrozenFilter:
		selection.Targets, err = s.memoryCollectionTargets(r.Context(), request.Selection.Project)
		if err == nil {
			selection.FilterFingerprint = memoryCollectionFilterFingerprint(request.Selection.Project)
			selection.ExcludedIDs = append([]string(nil), request.Selection.ExcludedIDs...)
			selection.ExpiresAt = time.Now().UTC().Add(15 * time.Minute)
		}
	default:
		err = dbgorm.ErrCollectionSelectionInvalid
	}
	if err != nil {
		memoryCollectionSelectionSnapshotError(w, err)
		return
	}
	saved, err := store.Save(r.Context(), scope, selection)
	if err != nil {
		memoryCollectionSelectionSnapshotError(w, err)
		return
	}
	writeJSON(w, map[string]memoryCollectionSelectionResponse{"selection": newMemoryCollectionSelectionResponse(saved)})
}

func (s *Service) handleMemoryCollectionSelectionCurrent(w http.ResponseWriter, r *http.Request) {
	scope, err := memoryCollectionSelectionScope(r)
	if err != nil {
		http.Error(w, memorySelectionForbiddenMessage, http.StatusForbidden)
		return
	}
	store, err := s.memoryCollectionSelectionStore()
	if err != nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}
	selection, err := store.Current(r.Context(), scope)
	if err != nil {
		memoryCollectionSelectionSnapshotError(w, err)
		return
	}
	writeJSON(w, map[string]memoryCollectionSelectionResponse{"selection": newMemoryCollectionSelectionResponse(selection)})
}

func (s *Service) handleMemoryCollectionSelectionPage(w http.ResponseWriter, r *http.Request) {
	var request memoryCollectionSelectionPageRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid memory page", http.StatusBadRequest)
		return
	}
	if _, err := memoryCollectionSelectionScope(r); err != nil {
		http.Error(w, memorySelectionForbiddenMessage, http.StatusForbidden)
		return
	}
	page, err := s.memoryCollectionPage(r.Context(), request.Project, request.Cursor, request.Limit)
	if err != nil {
		memoryCollectionSelectionSnapshotError(w, err)
		return
	}
	writeJSON(w, page)
}

func (s *Service) memoryCollectionOperationSelection(ctx context.Context, store *dbgorm.CollectionSelectionStore, scope dbgorm.CollectionSelectionScope, request memoryCollectionSelectionOperationSelection) (dbgorm.CollectionSelection, error) {
	if request.Version < 1 {
		return dbgorm.CollectionSelection{}, dbgorm.ErrCollectionSelectionInvalid
	}
	var (
		selection dbgorm.CollectionSelection
		err       error
	)
	if request.Kind == dbgorm.CollectionSelectionFrozenFilter {
		selection, err = store.Frozen(ctx, scope, request.Token)
	} else {
		if request.Token != "" {
			return dbgorm.CollectionSelection{}, dbgorm.ErrCollectionSelectionInvalid
		}
		selection, err = store.Current(ctx, scope)
	}
	if err != nil {
		return dbgorm.CollectionSelection{}, err
	}
	if selection.Kind != request.Kind || selection.Version != request.Version || selection.ReconfirmationRequired {
		return dbgorm.CollectionSelection{}, errMemorySelectionConflict
	}
	return selection, nil
}

func memoryCollectionOperationTargets(selection dbgorm.CollectionSelection) ([]dbgorm.CollectionSelectionTarget, error) {
	excluded := make(map[string]struct{}, len(selection.ExcludedIDs))
	for _, id := range selection.ExcludedIDs {
		excluded[id] = struct{}{}
	}
	targets := make([]dbgorm.CollectionSelectionTarget, 0, len(selection.Targets))
	for _, target := range selection.Targets {
		if _, skip := excluded[target.ID]; !skip {
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 || len(targets) > dbgorm.CollectionSelectionMaxTargets {
		return nil, dbgorm.ErrCollectionSelectionInvalid
	}
	return targets, nil
}

func memoryCollectionNextStatus(action, status string) (string, bool) {
	switch action {
	case "suppress":
		return "flagged", status == "active"
	case "unsuppress":
		return "active", status == "flagged"
	case "archive":
		return "archived", status == "active" || status == "flagged"
	default:
		return "", false
	}
}

func memoryCollectionSelectionOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMemorySelectionDenied), errors.Is(err, dbgorm.ErrCollectionSelectionDenied):
		http.Error(w, "memory operation forbidden", http.StatusForbidden)
	case errors.Is(err, dbgorm.ErrCollectionSelectionReconfirmationRequired):
		http.Error(w, "memory selection is stale", http.StatusPreconditionFailed)
	case errors.Is(err, errMemorySelectionConflict):
		http.Error(w, "memory selection conflicts with current state", http.StatusConflict)
	case errors.Is(err, dbgorm.ErrCollectionSelectionInvalid):
		http.Error(w, "invalid memory operation", http.StatusBadRequest)
	default:
		log.Error().Err(err).Msg("memory selection operation failed")
		http.Error(w, "memory operation failed", http.StatusInternalServerError)
	}
}

func (s *Service) handleMemoryCollectionSelectionOperation(w http.ResponseWriter, r *http.Request) {
	var request memoryCollectionSelectionOperationRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !operatorCodeText(request.RequestID) || r.Header.Get("X-Engram-Request-ID") != request.RequestID {
		http.Error(w, "invalid memory operation request reference", http.StatusBadRequest)
		return
	}
	if _, allowed := memoryCollectionNextStatus(request.Action, "active"); !allowed && request.Action != "unsuppress" {
		http.Error(w, "invalid memory operation", http.StatusBadRequest)
		return
	}
	scope, err := memoryCollectionSelectionScope(r)
	if err != nil {
		http.Error(w, "memory operation forbidden", http.StatusForbidden)
		return
	}
	store, err := s.memoryCollectionSelectionStore()
	if err != nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}
	selection, err := s.memoryCollectionOperationSelection(r.Context(), store, scope, request.Selection)
	if err != nil {
		memoryCollectionSelectionOperationError(w, err)
		return
	}
	targets, err := memoryCollectionOperationTargets(selection)
	if err != nil {
		memoryCollectionSelectionOperationError(w, err)
		return
	}

	response := memoryCollectionSelectionOperationResponse{RequestID: request.RequestID, ItemResults: make([]memoryCollectionSelectionItemResponse, 0, len(targets))}
	current := make([]memoryCollectionSelectionCurrentState, 0, len(targets))
	partial := false
	readbackPending := false
	nonDisclosing := false
	for _, target := range targets {
		id, parseErr := strconv.ParseInt(target.ID, 10, 64)
		if parseErr != nil || id <= 0 || target.ExpectedVersion == 0 {
			memoryCollectionSelectionOperationAppend(&response, "redacted", "validation_error", nil)
			partial = true
			continue
		}
		memory, getErr := s.memoryStore.Get(r.Context(), id)
		if getErr != nil {
			outcome := "failed"
			if errors.Is(getErr, gormlib.ErrRecordNotFound) {
				outcome = "conflict"
			}
			memoryCollectionSelectionOperationAppend(&response, "redacted", outcome, nil)
			partial = true
			continue
		}
		if memory == nil || !memoryVisibleREST(r.Context(), memory) || !memoryDomainManageAllowedREST(r.Context(), memory) {
			memoryCollectionSelectionOperationAppend(&response, "redacted", "denied", nil)
			partial = true
			continue
		}
		if uint64(memory.Version) != target.ExpectedVersion {
			memoryCollectionSelectionOperationAppend(&response, "redacted", "conflict", nil)
			partial = true
			continue
		}
		nextStatus, validTransition := memoryCollectionNextStatus(request.Action, memory.Status)
		if !validTransition {
			memoryCollectionSelectionOperationAppend(&response, "redacted", "conflict", nil)
			partial = true
			continue
		}
		update := s.memoryStore.GetDB().WithContext(r.Context()).Model(&dbgorm.Memory{}).
			Where("id = ? AND deleted_at IS NULL AND version = ? AND status = ?", memory.ID, memory.Version, memory.Status).
			Updates(map[string]any{"status": nextStatus, "updated_at": time.Now().UTC(), "version": gormlib.Expr("version + 1")})
		if update.Error != nil {
			memoryCollectionSelectionOperationAppend(&response, "redacted", "failed", nil)
			partial = true
			continue
		}
		if update.RowsAffected != 1 {
			memoryCollectionSelectionOperationAppend(&response, "redacted", "conflict", nil)
			partial = true
			continue
		}
		observedVersion := memory.Version + 1
		memoryCollectionSelectionOperationAppend(&response, target.ID, "committed", &observedVersion)
		after, afterErr := s.memoryStore.Get(r.Context(), memory.ID)
		if afterErr != nil || after == nil {
			readbackPending = true
			continue
		}
		if !memoryVisibleREST(r.Context(), after) || !memoryDomainManageAllowedREST(r.Context(), after) {
			nonDisclosing = true
			continue
		}
		current = append(current, memoryCollectionSelectionCurrentState{ID: after.ID, Status: after.Status, Version: after.Version})
	}
	if partial {
		response.OperationState = "partial"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMultiStatus)
		writeJSON(w, response)
		return
	}
	if readbackPending {
		response.OperationState = "committed"
		writeJSON(w, response)
		return
	}
	response.OperationState = "completed"
	if nonDisclosing {
		response.Readback = &memoryCollectionSelectionReadback{Authoritative: true, Kind: "non_disclosing", OperationStatus: request.Action}
	} else {
		response.Readback = &memoryCollectionSelectionReadback{Authoritative: true, Kind: "current", CurrentState: current}
	}
	writeJSON(w, response)
}

func memoryCollectionSelectionOperationAppend(response *memoryCollectionSelectionOperationResponse, targetID, outcome string, observedVersion *int) {
	response.ItemResults = append(response.ItemResults, memoryCollectionSelectionItemResponse{TargetID: targetID, Outcome: outcome, ObservedVersion: observedVersion})
}
