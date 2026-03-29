package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// handleStoreConsolidated routes store tool actions to the appropriate handler.
func (s *Server) handleStoreConsolidated(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	action := coerceString(m["action"], "create")

	switch action {
	case "create":
		return s.handleStoreMemory(ctx, args)
	case "edit":
		return s.handleEditObservation(ctx, args)
	case "merge":
		return s.handleMergeObservations(ctx, args)
	case "import":
		return s.handleImportInstincts(ctx, args)
	default:
		return "", fmt.Errorf("unknown store action: %q (valid: create, edit, merge, import)", action)
	}
}
