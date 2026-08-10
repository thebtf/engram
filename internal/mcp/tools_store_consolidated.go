package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

var storeActions = []string{"create", "edit", "import"}

// handleStoreConsolidated routes store tool actions to the appropriate handler.
func (s *Server) handleStoreConsolidated(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	action, present, err := optionalStringArg(m, "action")
	if err != nil {
		return "", err
	}
	if !present || action == "" {
		action = "create"
	}
	if tags, ok := m["tags"].(string); ok {
		parts := strings.Split(tags, ",")
		normalized := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				normalized = append(normalized, part)
			}
		}
		m["tags"] = normalized
		args, err = json.Marshal(m)
		if err != nil {
			return "", fmt.Errorf("normalize store tags: %w", err)
		}
	}
	switch action {
	case "create":
		return s.handleStoreMemory(ctx, args)
	case "edit":
		return s.handleEditMemory(ctx, args)
	case "import":
		return s.handleImportInstincts(ctx, args)
	default:
		return "", fmt.Errorf("unknown store action: %q (valid: %s)", action, strings.Join(storeActions, ", "))
	}
}
