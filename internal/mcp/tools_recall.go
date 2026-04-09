// Package mcp — tools_recall.go routes consolidated "recall" tool actions
// to existing handler functions on *Server. This is the single entry point
// for all memory retrieval operations, dispatching by action parameter.
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

	case "reasoning":
		return s.handleReasoningSearch(ctx, args)

	case "hit_rate":
		return s.handleHitRateAnalytics(ctx, args)

	// Palace actions
	case "wake_up":
		return s.handleWakeUp(ctx, args)
	case "taxonomy":
		return s.handleTaxonomy(ctx, args)
	case "tunnels":
		return s.handleTunnels(ctx, args)

	default:
		return "", fmt.Errorf(
			"unknown recall action: %q (valid: search, preset, by_file, by_concept, by_type, similar, timeline, related, patterns, get, sessions, explain, reasoning, hit_rate, wake_up, taxonomy, tunnels)",
			action,
		)
	}
}

// handleHitRateAnalytics returns noise_candidate and high_value observations from concepts.
func (s *Server) handleHitRateAnalytics(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	project := coerceString(m["project"], "")
	limit := coerceInt(m["limit"], 20)

	db := s.observationStore.GetDB().WithContext(ctx)

	type hitRateObs struct {
		ID    int64  `gorm:"column:id"`
		Title string `gorm:"column:title"`
		Type  string `gorm:"column:type"`
		Flag  string `gorm:"column:flag"`
	}

	var results []hitRateObs

	// Find observations with noise_candidate or high_value in concepts
	sql := `
		SELECT id, COALESCE(title, '') as title, type,
			CASE
				WHEN concepts::text LIKE '%noise_candidate%' THEN 'noise_candidate'
				WHEN concepts::text LIKE '%high_value%' THEN 'high_value'
			END as flag
		FROM observations
		WHERE (concepts::text LIKE '%noise_candidate%' OR concepts::text LIKE '%high_value%')
		AND status = 'active'`
	params := []interface{}{}
	if project != "" {
		sql += " AND project = ?"
		params = append(params, project)
	}
	sql += " ORDER BY importance_score DESC LIMIT ?"
	params = append(params, limit)

	if err := db.Raw(sql, params...).Scan(&results).Error; err != nil {
		return "", fmt.Errorf("hit_rate query: %w", err)
	}

	if len(results) == 0 {
		return "No hit rate analytics data yet. Hit rate flags are computed during maintenance cycles and require 50+ injection_log entries.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Hit Rate Analytics (%d observations)\n\n", len(results)))

	noiseCount, starCount := 0, 0
	sb.WriteString("### Noise Candidates (injected 10+ times, never cited)\n")
	for _, r := range results {
		if r.Flag == "noise_candidate" {
			sb.WriteString(fmt.Sprintf("- [%d] %s (%s)\n", r.ID, r.Title, r.Type))
			noiseCount++
		}
	}
	if noiseCount == 0 {
		sb.WriteString("None found.\n")
	}

	sb.WriteString("\n### High Value (injected 5+ times, >50% citation rate)\n")
	for _, r := range results {
		if r.Flag == "high_value" {
			sb.WriteString(fmt.Sprintf("- [%d] %s (%s)\n", r.ID, r.Title, r.Type))
			starCount++
		}
	}
	if starCount == 0 {
		sb.WriteString("None found.\n")
	}

	sb.WriteString(fmt.Sprintf("\nSummary: %d noise, %d high-value\n", noiseCount, starCount))
	return sb.String(), nil
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
