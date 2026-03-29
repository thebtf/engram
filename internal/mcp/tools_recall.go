// Package mcp — tools_recall.go routes consolidated "recall" tool actions
// to existing handler functions on *Server. This is the single entry point
// for all memory retrieval operations, dispatching by action parameter.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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
		// Delegate to the full search dispatch in callTool.
		return s.callTool(ctx, "search", args)

	case "preset":
		preset := coerceString(m["preset"], "")
		switch preset {
		case "decisions", "changes", "how_it_works":
			return s.callTool(ctx, preset, args)
		default:
			return "", fmt.Errorf("recall: unknown preset %q (valid: decisions, changes, how_it_works)", preset)
		}

	case "by_file":
		return s.callTool(ctx, "find_by_file", args)

	case "by_concept":
		return s.callTool(ctx, "find_by_concept", args)

	case "by_type":
		return s.callTool(ctx, "find_by_type", args)

	case "similar":
		return s.handleFindSimilarObservations(ctx, args)

	case "timeline":
		return s.callTool(ctx, "timeline", args)

	case "related":
		return s.handleFindRelatedObservations(ctx, args)

	case "patterns":
		return s.handleGetPatterns(ctx, args)

	case "get":
		return s.handleGetObservation(ctx, args)

	case "sessions":
		query := coerceString(m["query"], "")
		if query != "" {
			return s.handleSearchSessions(ctx, args)
		}
		return s.handleListSessions(ctx, args)

	case "explain":
		return s.handleExplainSearchRanking(ctx, args)

	default:
		return "", fmt.Errorf(
			"unknown recall action: %q (valid: search, preset, by_file, by_concept, by_type, similar, timeline, related, patterns, get, sessions, explain)",
			action,
		)
	}
}
