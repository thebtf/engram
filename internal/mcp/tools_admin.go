package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *Server) handleAdmin(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	action := coerceString(m["action"], "")
	if action == "" {
		return "", fmt.Errorf("action required for admin tool (valid: bulk_delete, bulk_supersede, bulk_boost, tag, by_tag, batch_tag, graph, graph_stats, stats, trends, quality, importance, search_analytics, obs_quality, scoring, consolidations, maintenance, maintenance_stats, consolidation, export, backfill_status)")
	}

	switch action {
	case "bulk_delete":
		return s.handleBulkDeleteObservations(ctx, args)
	case "bulk_supersede":
		return s.handleBulkMarkSuperseded(ctx, args)
	case "bulk_boost":
		return s.handleBulkBoostObservations(ctx, args)
	case "tag":
		return s.handleTagObservation(ctx, args)
	case "by_tag":
		return s.handleGetObservationsByTag(ctx, args)
	case "batch_tag":
		return s.handleBatchTagByPattern(ctx, args)
	case "graph":
		return s.callTool(ctx, "graph_query", args)
	case "graph_stats":
		return s.handleGetGraphStats(ctx)
	case "stats":
		return s.handleGetMemoryStats(ctx)
	case "trends":
		return s.handleGetTemporalTrends(ctx, args)
	case "quality":
		return s.handleGetDataQualityReport(ctx, args)
	case "importance":
		return s.handleAnalyzeObservationImportance(ctx, args)
	case "search_analytics":
		return s.handleAnalyzeSearchPatterns(ctx, args)
	case "obs_quality":
		return s.handleGetObservationQuality(ctx, args)
	case "scoring":
		return s.handleGetObservationScoringBreakdown(ctx, args)
	case "consolidations":
		return s.handleSuggestConsolidations(ctx, args)
	case "maintenance":
		return s.handleTriggerMaintenance(ctx)
	case "maintenance_stats":
		return s.handleGetMaintenanceStats(ctx)
	case "consolidation":
		return s.handleRunConsolidation(ctx, args)
	case "export":
		return s.handleExportObservations(ctx, args)
	case "backfill_status":
		return s.handleBackfillStatus()
	default:
		return "", fmt.Errorf("unknown admin action: %q (valid: bulk_delete, bulk_supersede, bulk_boost, tag, by_tag, batch_tag, graph, graph_stats, stats, trends, quality, importance, search_analytics, obs_quality, scoring, consolidations, maintenance, maintenance_stats, consolidation, export, backfill_status)", action)
	}
}
