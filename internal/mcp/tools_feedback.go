package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

var feedbackActions = []string{"suppress", "outcome"}

// handleFeedbackConsolidated routes feedback tool actions to the appropriate handler.
func (s *Server) handleFeedbackConsolidated(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	action := coerceString(m["action"], "")
	if action == "" {
		return "", fmt.Errorf("action required for feedback tool (valid: %s)", strings.Join(feedbackActions, ", "))
	}

	switch action {
	case "suppress":
		return s.handleSuppressMemory(ctx, args)
	case "outcome":
		return s.handleSetSessionOutcome(ctx, args)
	default:
		return "", fmt.Errorf("unknown feedback action: %q (valid: %s)", action, strings.Join(feedbackActions, ", "))
	}
}

// handleSetSessionOutcome records the outcome of a Claude session.
func (s *Server) handleSetSessionOutcome(ctx context.Context, args json.RawMessage) (string, error) {
	if s.sessionStore == nil {
		return "", fmt.Errorf("session store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	sessionID := coerceString(m["session_id"], "")
	if sessionID == "" {
		return "", fmt.Errorf("session_id required")
	}
	outcome := coerceString(m["outcome"], "")
	if outcome == "" {
		return "", fmt.Errorf("outcome required")
	}
	switch outcome {
	case "success", "partial", "failure", "abandoned":
	default:
		return "", fmt.Errorf("invalid outcome %q (valid: success, partial, failure, abandoned)", outcome)
	}
	reason := coerceString(m["reason"], "")

	if err := s.sessionStore.UpdateSessionOutcome(ctx, sessionID, outcome, reason); err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Msg("feedback outcome failed")
		return "", fmt.Errorf("failed to record session outcome")
	}

	return marshalJSON(map[string]any{
		"status":     "recorded",
		"session_id": sessionID,
		"outcome":    outcome,
	})
}
