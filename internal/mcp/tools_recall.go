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
	"strings"
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

	case "by_file":
		return s.callTool(ctx, "find_by_file", args)

	case "by_concept":
		// Dropped in v5 (US9): concept index backed by search.Manager.
		return "", fmt.Errorf("recall: action %q not supported in v5 (concept search removed — use recall(action=\"search\") instead)", action)

	case "by_type":
		// Dropped in v5 (US9): type-lane search backed by search.Manager.
		return "", fmt.Errorf("recall: action %q not supported in v5 (type-lane search removed — use recall(action=\"search\") instead)", action)

	case "similar":
		// Dropped in v5 (US9): vector similarity search removed (content_chunks dropped).
		return "", fmt.Errorf("recall: action %q not supported in v5 (vector similarity removed)", action)

	case "timeline":
		// Dropped in v5 (US9): timeline backed by search.Manager.
		return "", fmt.Errorf("recall: action %q not supported in v5 (timeline search removed — use recall(action=\"search\") instead)", action)

	case "related":
		return s.handleFindRelatedObservations(ctx, args)

	case "sessions":
		query := coerceString(m["query"], "")
		if query != "" {
			return s.handleSearchSessions(ctx, args)
		}
		return s.handleListSessions(ctx, args)

	case "explain":
		// Dropped in v5 (US9): explain ranked search results using search.Manager.
		return "", fmt.Errorf("recall: action %q not supported in v5 (search ranking removed)", action)

	case "reasoning":
		return s.handleReasoningSearch(ctx, args)

	default:
		return "", fmt.Errorf(
			"unknown recall action: %q (valid: search, by_file, related, reasoning)",
			action,
		)
	}
}

// handleRecallSearch performs trivial SQL-based memory retrieval.
// It filters the memories table by project (required when non-empty) and
// optionally applies a case-insensitive substring match on content when a
// query string is provided. Results are ordered by created_at DESC.
func (s *Server) handleRecallSearch(ctx context.Context, m map[string]any) (string, error) {
	project := coerceString(m["project"], "")
	query := coerceString(m["query"], "")
	limit := coerceInt(m["limit"], 20)
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	if s.memoryStore == nil {
		return "", fmt.Errorf("recall: memory store not configured")
	}

	// List returns created_at DESC, project-filtered results.
	if project == "" {
		// No project scope: return a helpful message rather than silently
		// returning zero rows (the project param is required by List).
		return `{"memories":[],"count":0,"note":"project parameter required for memory search in v5"}`, nil
	}

	// When a query filter is provided, fetch a wider candidate pool so that
	// older matching memories are not excluded by the limit before filtering.
	// The final result is capped at `limit` after filtering.
	fetchLimit := limit
	query = strings.TrimSpace(query)
	if query != "" {
		const candidateMultiplier = 10
		const minCandidatePool = 1000
		fetchLimit = limit * candidateMultiplier
		if fetchLimit < minCandidatePool {
			fetchLimit = minCandidatePool
		}
	}

	memories, err := s.memoryStore.List(ctx, project, fetchLimit)
	if err != nil {
		return "", fmt.Errorf("recall search: %w", err)
	}

	// Apply optional query filter in-memory (case-insensitive substring),
	// then cap at the originally requested limit.
	if query != "" {
		queryLower := strings.ToLower(query)
		filtered := memories[:0:0] // same element type, zero length, zero cap (avoids models import)
		for _, mem := range memories {
			if strings.Contains(strings.ToLower(mem.Content), queryLower) {
				filtered = append(filtered, mem)
				if len(filtered) == limit {
					break
				}
			}
		}
		memories = filtered
	}

	type memoryResult struct {
		Tags        []string `json:"tags,omitempty"`
		Content     string   `json:"content"`
		SourceAgent string   `json:"source_agent,omitempty"`
		Project     string   `json:"project"`
		ID          int64    `json:"id"`
		Version     int      `json:"version"`
	}
	results := make([]memoryResult, 0, len(memories))
	for _, mem := range memories {
		results = append(results, memoryResult{
			ID:          mem.ID,
			Project:     mem.Project,
			Content:     mem.Content,
			Tags:        mem.Tags,
			SourceAgent: mem.SourceAgent,
			Version:     mem.Version,
		})
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


// handleReasoningSearch retrieves reasoning traces by project.
func (s *Server) handleReasoningSearch(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	project := coerceString(m["project"], "")
	limit := coerceInt(m["limit"], 5)

	if s.reasoningStore == nil {
		return "Reasoning traces not available (store not configured).", nil
	}

	traces, err := s.reasoningStore.SearchByProject(ctx, project, limit)
	if err != nil {
		return "", fmt.Errorf("reasoning search: %w", err)
	}

	if len(traces) == 0 {
		return "No reasoning traces found for this project.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Reasoning Traces (%d found)\n\n", len(traces)))

	for i, t := range traces {
		sb.WriteString(fmt.Sprintf("## Trace %d (quality: %.0f%%)\n", i+1, t.QualityScore*100))

		// Parse steps from JSONB string
		var steps []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if jsonErr := json.Unmarshal([]byte(t.Steps), &steps); jsonErr == nil {
			for _, step := range steps {
				sb.WriteString(fmt.Sprintf("  [%s] %s\n", strings.ToUpper(step.Type), step.Content))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}
