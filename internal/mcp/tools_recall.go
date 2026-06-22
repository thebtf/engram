// Package mcp — tools_recall.go routes consolidated "recall" tool actions
// to existing handler functions on *Server. This is the single entry point
// for all memory retrieval operations, dispatching by action parameter.
//
// v5 (US9): dropped actions search (was hybrid/fusion), preset, by_concept,
// by_type, similar, timeline, explain. The "search" action now runs a trivial
// SQL filter over the memories store. Dropped handler symbols have been
// removed from server.go.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// handleRecall is the consolidated recall tool handler. It parses the "action"
// parameter and delegates to the appropriate existing handler or callTool dispatch.
func (s *Server) handleRecall(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", fmt.Errorf("recall: %w", err)
	}

	action := coerceString(m["action"], "search")

	switch action {
	case "search":
		return s.handleRecallSearch(ctx, m)

	case "preset":
		// Dropped in v5 (US9): preset (decisions/changes/how_it_works) used search.Manager.
		return "", fmt.Errorf("recall: action %q not supported in v5 (search.Manager removed — use recall(action=\"search\") instead)", action)

	case "by_concept":
		// Dropped in v5 (US9): concept index backed by search.Manager.
		return "", fmt.Errorf("recall: action %q not supported in v5 (concept search removed — use recall(action=\"search\") instead)", action)

	case "by_type":
		// Dropped in v5 (US9): type-lane search backed by search.Manager.
		return "", fmt.Errorf("recall: action %q not supported in v5 (type-lane search removed — use recall(action=\"search\") instead)", action)

	case "similar":
		// The v5 demolition removed observation vector-similarity search (US9).
		// vNext reinstated it on the migration-108 content_chunks table, gated by
		// ENGRAM_VNEXT_ENABLED=true (W3); flag-off returns an empty result.
		if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
			// Under flag-ON: translate min_similarity into the hybrid vector threshold
			// (VecThreshold on HybridOptions) and forward to recall_memory.
			// This preserves legacy min_similarity semantics: callers that relied on
			// a cosine floor will see the same filtering under the hybrid path.
			var mArgs map[string]any
			if jsonErr := json.Unmarshal(args, &mArgs); jsonErr != nil {
				return "", fmt.Errorf("recall similar: %w", jsonErr)
			}
			// Map min_similarity → vec_threshold so handleRecallMemoryHybrid picks it up.
			if ms, ok := mArgs["min_similarity"]; ok {
				mArgs["vec_threshold"] = ms
			}
			// Restrict to vector tier only so callers get the promised cosine floor.
			// Without this, FTS matches that pass text criteria but are below the
			// cosine threshold would still appear in results (codex finding W3-#7).
			mArgs["tier_filter"] = []string{"tier1_vector"}
			patched, marshalErr := json.Marshal(mArgs)
			if marshalErr != nil {
				return "", fmt.Errorf("recall similar: patch args: %w", marshalErr)
			}
			return s.handleRecallMemory(ctx, patched)
		}
		// Flag-OFF: exact tombstone string from origin/main (byte-identical).
		return "", fmt.Errorf("recall: action %q not supported in v5 (vector similarity removed)", action)

	case "timeline":
		// Dropped in v5 (US9): timeline backed by search.Manager.
		return "", fmt.Errorf("recall: action %q not supported in v5 (timeline search removed — use recall(action=\"search\") instead)", action)

	case "sessions":
		query := coerceString(m["query"], "")
		if query != "" {
			return s.handleSearchSessions(ctx, args)
		}
		return s.handleListSessions(ctx, args)

	case "explain":
		// Dropped in v5 (US9): explain ranked search results using search.Manager.
		// Reinstated under flag via recall_memory(explain=true) when ENGRAM_VNEXT_ENABLED=true (W3).
		if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
			// Forward to recall_memory with explain=true injected.
			var m map[string]any
			if jsonErr := json.Unmarshal(args, &m); jsonErr != nil {
				return "", fmt.Errorf("recall explain: %w", jsonErr)
			}
			m["explain"] = true
			patched, marshalErr := json.Marshal(m)
			if marshalErr != nil {
				return "", fmt.Errorf("recall explain: patch args: %w", marshalErr)
			}
			return s.handleRecallMemory(ctx, patched)
		}
		// Flag-OFF: exact tombstone string from origin/main (byte-identical).
		return "", fmt.Errorf("recall: action %q not supported in v5 (search ranking removed)", action)

	default:
		return "", fmt.Errorf(
			"unknown recall action: %q (valid: search)",
			action,
		)
	}
}

// handleRecallSearch performs trivial SQL-based memory retrieval.
// It filters the memories table by project (required when non-empty) and
// optionally applies a case-insensitive substring match on content when a
// query string is provided. Results are ordered by created_at DESC.
//
// T004 (engram vNext Milestone F TG1): when ENGRAM_VNEXT_F_ENABLED=true, the
// fetched memory list is post-filtered by scope.Resolve against the caller's
// keycard identity (auth.Identity.WorkstationID + optional session_id param).
// Each surviving row carries `privacy_scope` + `source_workstation_id` in the
// response per RI-F2 dual-field invariant. With the flag OFF, behavior is
// byte-identical to v6.4.x — no filter applied, no new fields in the
// response.
func (s *Server) handleRecallSearch(ctx context.Context, m map[string]any) (string, error) {
	project := coerceString(m["project"], "")
	query := coerceString(m["query"], "")
	limit := coerceInt(m["limit"], 20)
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	// T004 — caller's session_id (optional). Empty session_id means the
	// resolver's workstation-only-suffices branch handles private-scope
	// rows from the caller's workstation per spec FR-F1 AMEND 2026-05-25.
	callerSessionID := coerceString(m["session_id"], "")

	// T005 — optional include_scopes filter (subset of 4-tier enum). Empty
	// or omitted means all tiers admitted (subject to scope.Resolve). Each
	// value is validated against the migration 125 CHECK enum; invalid
	// values return the structured 'invalid_include_scopes:' error.
	//
	// codex P2 fix-forward on 3e4a4b1: validation is flag-gated. Under
	// ENGRAM_VNEXT_F_ENABLED=false the parameter is not honored at all
	// (T005 contract: "runtime behavior stays env-gated"), so unknown
	// values must not surface a structured error to clients still on the
	// v6.4.x contract who may send experimental fields.
	includeScopes := make(map[string]bool)
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
		includeScopesRaw := coerceStringSlice(m["include_scopes"])
		for _, s := range includeScopesRaw {
			switch s {
			case "private", "project", "shared", "global":
				includeScopes[s] = true
			default:
				return "", fmt.Errorf("invalid_include_scopes: %q must be one of private, project, shared, global", s)
			}
		}
	}

	// T018 TG3 — confidence_min, include_superseded, include_rationale.
	// Schema-unconditional (same as session_id/include_scopes); runtime behavior
	// gated by vnextFEnabled() AND at least one non-default value.
	tg3ConfidenceMin := coerceFloat64(m["confidence_min"], 0.0)
	tg3IncludeSuperseded := coerceBool(m["include_superseded"], false)
	tg3IncludeRationale := coerceBool(m["include_rationale"], false)
	tg3Active := vnextFEnabled() && (tg3ConfidenceMin > 0 || tg3IncludeSuperseded || tg3IncludeRationale)

	if s.memoryStore == nil {
		return "", fmt.Errorf("recall: memory store not configured")
	}

	// List returns created_at DESC, project-filtered results.
	if project == "" {
		// No project scope: return a helpful message rather than silently
		// returning zero rows (the project param is required by List).
		return `{"memories":[],"count":0,"note":"project parameter is required for memory search"}`, nil
	}

	query = strings.TrimSpace(query)
	queryLower := strings.ToLower(query)

	// T004 — build the caller KeycardContext once outside the loop. The
	// ENGRAM_VNEXT_F_ENABLED flag gates only legacy privacy_scope; CR-004
	// principal-private visibility is always enforced.
	scopeEnabled := os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true"
	caller := scope.KeycardContext{SessionID: callerSessionID}
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
		caller.Principal = id.Principal
		caller.PrincipalKind = string(id.PrincipalKind)
	}
	visibilityOpts := scope.MemoryVisibilityOptions{
		ApplyPrivacyScope: scopeEnabled,
		IncludeScopes:     includeScopes,
	}

	type memoryResult struct {
		RankingRationale    *retrieval.RankingRationale `json:"ranking_rationale,omitempty"`
		Tags                []string                    `json:"tags,omitempty"`
		Content             string                      `json:"content"`
		SourceAgent         string                      `json:"source_agent,omitempty"`
		OwnerPrincipal      string                      `json:"owner_principal,omitempty"`
		OwnerPrincipalKind  string                      `json:"owner_principal_kind,omitempty"`
		AgentVisibility     string                      `json:"agent_visibility,omitempty"`
		Domain              string                      `json:"domain,omitempty"`
		PrivacyScope        string                      `json:"privacy_scope,omitempty"`
		SourceWorkstationID string                      `json:"source_workstation_id,omitempty"`
		SourceSessions      []string                    `json:"source_sessions,omitempty"`
		Project             string                      `json:"project"`
		ID                  int64                       `json:"id"`
		Version             int                         `json:"version"`
	}

	// filterMemory applies the optional substring query filter AND shared
	// memory visibility. ENGRAM_VNEXT_F_ENABLED gates only legacy
	// privacy_scope; principal-private rows are filtered fail-safe.
	filterMemory := func(mem *models.Memory) (memoryResult, bool) {
		if queryLower != "" && !strings.Contains(strings.ToLower(mem.Content), queryLower) {
			return memoryResult{}, false
		}
		if !scope.ResolveMemory(caller, mem, visibilityOpts) {
			return memoryResult{}, false
		}
		mr := memoryResult{
			ID:          mem.ID,
			Project:     mem.Project,
			Content:     mem.Content,
			Tags:        mem.Tags,
			SourceAgent: mem.SourceAgent,
			Version:     mem.Version,
		}
		mr.OwnerPrincipal = mem.OwnerPrincipal
		mr.OwnerPrincipalKind = mem.OwnerPrincipalKind
		mr.AgentVisibility = mem.AgentVisibility
		mr.Domain = mem.Domain
		if scopeEnabled {
			mr.PrivacyScope = mem.PrivacyScope
			mr.SourceWorkstationID = mem.SourceWorkstationID
			mr.SourceSessions = mem.SourceSessions
		}
		return mr, true
	}

	// Build filter descriptors for TG3 rationale (used only when tg3IncludeRationale).
	var tg3FilterDescs []string
	if tg3Active {
		tg3FilterDescs = append(tg3FilterDescs, "project="+project)
		if tg3ConfidenceMin > 0 {
			tg3FilterDescs = append(tg3FilterDescs, fmt.Sprintf("confidence_min=%.4g", tg3ConfidenceMin))
		}
		if tg3IncludeSuperseded {
			tg3FilterDescs = append(tg3FilterDescs, "include_superseded=true")
		}
	}

	results := make([]memoryResult, 0, limit)
	if tg3Active {
		// T018 MAJOR fix (review hardening): ListWithFilters cannot be used
		// safely with always-on visibility because invisible rows at the head of
		// the result set can truncate visible recall before older eligible rows
		// are reached. Use the same ListWithOffset batch-loop as non-TG3 recall.
		//
		// include_superseded cannot be honoured by ListWithOffset (hardcoded
		// status='active'). Return a structured error instead of pretending the
		// flag was applied.
		if tg3IncludeSuperseded {
			return "", fmt.Errorf("include_superseded is not supported with visibility-filtered recall search; omit include_superseded")
		}
		const batchSize = 500
		offset := 0
		for len(results) < limit {
			batch, err := s.memoryStore.ListWithOffset(ctx, project, batchSize, offset)
			if err != nil {
				return "", fmt.Errorf("recall search tg3: %w", err)
			}
			if len(batch) == 0 {
				break
			}
			for _, mem := range batch {
				if tg3ConfidenceMin > 0 && mem.Confidence < tg3ConfidenceMin {
					continue
				}
				mr, ok := filterMemory(mem)
				if !ok {
					continue
				}
				if tg3IncludeRationale {
					contentMatched := queryLower != "" && strings.Contains(strings.ToLower(mem.Content), queryLower)
					rat := retrieval.AssembleRationale(mem, query, contentMatched, tg3FilterDescs)
					mr.RankingRationale = &rat
				}
				results = append(results, mr)
				if len(results) >= limit {
					break
				}
			}
			offset += len(batch)
			if len(batch) < batchSize {
				break
			}
		}
	} else {
		// Codex P1 cycle-3 fix on 4cb71be: batch-loop with offset paging so
		// that invisible newest rows do not truncate recall before older
		// visible rows reach the requested limit. ENGRAM_VNEXT_F_ENABLED gates
		// legacy privacy_scope only; principal-private rows still require this
		// backfill behavior when the flag is off.
		const batchSize = 500
		offset := 0
		for len(results) < limit {
			batch, err := s.memoryStore.ListWithOffset(ctx, project, batchSize, offset)
			if err != nil {
				return "", fmt.Errorf("recall search: %w", err)
			}
			if len(batch) == 0 {
				break
			}
			for _, mem := range batch {
				if mr, ok := filterMemory(mem); ok {
					results = append(results, mr)
					if len(results) >= limit {
						break
					}
				}
			}
			offset += len(batch)
			// Stop when the DB returned fewer rows than requested — that
			// means we have reached the end of the project's active rows.
			if len(batch) < batchSize {
				break
			}
		}
	}

	out := map[string]any{
		"memories": results,
		"count":    len(results),
	}
	if query != "" {
		out["query"] = query
	}

	output, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("recall search marshal: %w", err)
	}
	return string(output), nil
}
