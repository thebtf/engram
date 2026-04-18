package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// adminActions is the single source of truth for valid admin tool actions.
// It is referenced by handleAdmin for validation messages and by the tool
// registration in server.go for the tool description.
var adminActions = []string{
	"bulk_delete", "bulk_supersede", "bulk_boost",
	"tag", "by_tag", "batch_tag",
	"stats", "trends", "quality", "importance",
	"search_analytics", "obs_quality", "scoring",
	"export", "backfill_status",
	"compress_aaak", "set_aaak_code", "taxonomy_stats",
}

func (s *Server) handleAdmin(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	action := coerceString(m["action"], "")
	if action == "" {
		return "", fmt.Errorf("action required for admin tool (valid: %s)", strings.Join(adminActions, ", "))
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
	case "export":
		return s.handleExportObservations(ctx, args)
	case "backfill_status":
		return s.handleBackfillStatus()
	// Palace actions
	case "compress_aaak":
		return s.handleCompressAAK(ctx, args)
	case "set_aaak_code":
		return s.handleSetAAKCode(ctx, args)
	case "taxonomy_stats":
		return s.handleTaxonomyStats(ctx, args)
	default:
		return "", fmt.Errorf("unknown admin action: %q (valid: %s)", action, strings.Join(adminActions, ", "))
	}
}
