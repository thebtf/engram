// Package worker provides memory REST handlers for the dashboard.
package worker

import (
	"context"
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
