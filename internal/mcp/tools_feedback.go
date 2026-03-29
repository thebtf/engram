package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// handleFeedbackConsolidated routes feedback tool actions to the appropriate handler.
func (s *Server) handleFeedbackConsolidated(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	action := coerceString(m["action"], "")
	if action == "" {
		return "", fmt.Errorf("action required for feedback tool (valid: rate, suppress, outcome)")
	}

	switch action {
	case "rate":
		return s.handleRateMemory(ctx, args)
	case "suppress":
		return s.handleSuppressMemory(ctx, args)
	case "outcome":
		return s.handleSetSessionOutcomeMCP(ctx, args)
	default:
		return "", fmt.Errorf("unknown feedback action: %q (valid: rate, suppress, outcome)", action)
	}
}
