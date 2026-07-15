package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

var docsActions = []string{"create", "read", "list", "history", "comment", "collections", "documents", "get_doc", "remove", "ingest"}

// handleDocsConsolidated routes docs tool actions to the appropriate handler.
func (s *Server) handleDocsConsolidated(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	action := coerceString(m["action"], "")
	if action == "" {
		return "", fmt.Errorf("action required for docs tool (valid: %s)", strings.Join(docsActions, ", "))
	}

	switch action {
	case "create":
		return s.handleDocCreate(ctx, args)
	case "read":
		return s.handleDocRead(ctx, args)
	case "list":
		return s.handleDocList(ctx, args)
	case "history":
		return s.handleDocHistory(ctx, args)
	case "comment":
		return s.handleDocComment(ctx, args)
	case "collections":
		return s.handleListCollections(ctx)
	case "documents":
		return s.handleListDocuments(ctx, args)
	case "get_doc":
		return s.handleGetDocument(ctx, args)
	case "remove":
		return s.handleRemoveDocument(ctx, args)
	case "ingest":
		return s.handleIngestDocument(ctx, args)
	default:
		return "", fmt.Errorf("unknown docs action: %q (valid: %s)", action, strings.Join(docsActions, ", "))
	}
}
