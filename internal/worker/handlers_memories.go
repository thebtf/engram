// Package worker provides memory REST handlers for the dashboard.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/auth"
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
	Project      string   `json:"project"`
	Content      string   `json:"content"`
	Tags         []string `json:"tags,omitempty"`
	SourceAgent  string   `json:"source_agent,omitempty"`
	PrivacyScope string   `json:"privacy_scope,omitempty"` // T004 — vNext F, 4-tier enum
	SessionID    string   `json:"session_id,omitempty"`    // T004 — caller session for SourceSessions
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

	created, err := s.memoryStore.Create(r.Context(), mem)
	if err != nil {
		log.Error().Err(err).Str("project", req.Project).Msg("store memory failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, created)
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
	if s.memoryStore == nil {
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
	mems, err := listVisibleMemoriesREST(r.Context(), s.memoryStore, project, limit)
	if err != nil {
		log.Error().Err(err).Str("project", project).Msg("list memories failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Return an empty array rather than null when there are no results.
	if mems == nil {
		mems = []*models.Memory{}
	}

	writeJSON(w, mems)
}

// listVisibleMemoriesREST returns up to `limit` memories from the given
// project that are visible to the caller. Behavior is gated by
// ENGRAM_VNEXT_F_ENABLED:
//
//	flag OFF — single-call s.memoryStore.List(project, limit), preserving
//	           v6.4.x byte-identity per RI-F1.
//	flag ON  — ListWithOffset batch-loop (batchSize=500) accumulating up to
//	           `limit` visible rows; scope-invisible rows are skipped without
//	           truncating the visible result set. Mirrors the cycle-3
//	           handleRecallSearch fix and the cycle-6 handleRecallMemory
//	           fix so all three surfaces share identical visibility
//	           semantics.
func listVisibleMemoriesREST(ctx context.Context, store memoryListStore, project string, limit int) ([]*models.Memory, error) {
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") != "true" {
		return store.List(ctx, project, limit)
	}
	var caller scope.KeycardContext
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
	}
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
			memScope := mem.PrivacyScope
			if memScope == "" {
				memScope = "project"
			}
			meta := scope.SourceMeta{
				WorkstationID: mem.SourceWorkstationID,
				Sessions:      mem.SourceSessions,
			}
			if !scope.Resolve(caller, memScope, meta) {
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

// memoryListStore is the subset of the MemoryStore surface that
// listVisibleMemoriesREST needs. Defined as a small interface so the
// function can be unit-tested with a fake without pulling in the full
// store dependency.
type memoryListStore interface {
	List(ctx context.Context, project string, limit int) ([]*models.Memory, error)
	ListWithOffset(ctx context.Context, project string, limit int, offset int) ([]*models.Memory, error)
}

// injectionCandidateStore is the subset of the MemoryStore surface that
// listVisibleForInjection needs. Defined as a small interface to allow
// unit-testing without the full store dependency.
type injectionCandidateStore interface {
	ListForInjection(ctx context.Context, project string, limit int) ([]*models.Memory, error)
}

// listVisibleForInjection fetches injection candidates and, when
// ENGRAM_VNEXT_F_ENABLED=true, removes rows that the caller cannot see per
// scope.Resolve. Mirrors the visibility-filter logic of listVisibleMemoriesREST
// but operates on the injection candidate set (importance-ordered, topK*3
// pre-inflated by the caller).
//
// T004 (codex P1 PR #221): ListForInjection previously returned every active
// row for the project without checking privacy_scope, so a private memory
// written by workstation A could be injected into context for workstation B in
// the same project (handlers_context.go vnext path, handlers_reinject.go, and
// mcp/tools_brief.go handleGetMemoryBrief). This helper closes that gap at the
// worker/mcp boundary without adding auth/scope imports to the db/gorm layer.
func listVisibleForInjection(ctx context.Context, store injectionCandidateStore, project string, limit int) ([]*models.Memory, error) {
	candidates, err := store.ListForInjection(ctx, project, limit)
	if err != nil {
		return nil, err
	}
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") != "true" {
		return candidates, nil
	}
	var caller scope.KeycardContext
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
	}
	visible := make([]*models.Memory, 0, len(candidates))
	for _, mem := range candidates {
		memScope := mem.PrivacyScope
		if memScope == "" {
			memScope = "project"
		}
		meta := scope.SourceMeta{
			WorkstationID: mem.SourceWorkstationID,
			Sessions:      mem.SourceSessions,
		}
		if scope.Resolve(caller, memScope, meta) {
			visible = append(visible, mem)
		}
	}
	return visible, nil
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

// handleGetMemoryByID godoc
// @Summary Get a single memory note by ID
// @Description Returns the full memory row for the given numeric ID. Under
// @Description ENGRAM_VNEXT_F_ENABLED the caller's privacy-scope visibility is
// @Description enforced via scope.Resolve: a memory the caller cannot see returns
// @Description 404 (not 403) so the endpoint never leaks the existence of a private
// @Description memory — mirroring the edit_memory cross-project deny contract and
// @Description the listVisibleMemoriesREST visibility model.
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

	mem, err := s.memoryStore.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("get memory failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Scope visibility: under ENGRAM_VNEXT_F_ENABLED a caller may only read a
	// memory their keycard can see. An invisible memory returns 404 — never 403 —
	// so the endpoint does not reveal that a private memory exists. This is the
	// single-row counterpart to listVisibleMemoriesREST and the edit_memory
	// existence-leak guard; without it, GET /api/memories/{id} would be a new
	// cross-surface symmetry break re-exposing private rows by ID.
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" && !memoryVisibleToCaller(r.Context(), mem) {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}

	writeJSON(w, mem)
}

// memoryVisibleToCaller reports whether the request's keycard identity may see
// the given memory under the privacy-scope model. Mirrors the per-row predicate
// in listVisibleMemoriesREST so all read surfaces share identical semantics.
func memoryVisibleToCaller(ctx context.Context, mem *models.Memory) bool {
	if mem == nil {
		return false
	}
	var caller scope.KeycardContext
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
	}
	memScope := mem.PrivacyScope
	if memScope == "" {
		memScope = "project"
	}
	meta := scope.SourceMeta{
		WorkstationID: mem.SourceWorkstationID,
		Sessions:      mem.SourceSessions,
	}
	return scope.Resolve(caller, memScope, meta)
}
