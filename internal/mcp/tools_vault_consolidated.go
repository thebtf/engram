package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// handleVaultConsolidated routes vault tool actions to the appropriate handler.
func (s *Server) handleVaultConsolidated(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	action := coerceString(m["action"], "")
	if action == "" {
		return "", fmt.Errorf("action required for vault tool (valid: store, get, list, delete, status)")
	}

	switch action {
	case "store":
		return s.handleStoreCredential(ctx, args)
	case "get":
		return s.handleGetCredential(ctx, args)
	case "list":
		return s.handleListCredentials(ctx, args)
	case "delete":
		return s.handleDeleteCredential(ctx, args)
	case "status":
		return s.handleVaultStatus(ctx, args)
	default:
		return "", fmt.Errorf("unknown vault action: %q (valid: store, get, list, delete, status)", action)
	}
}
