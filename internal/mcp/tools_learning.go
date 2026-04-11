package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/learning"
)

// handleSetSessionOutcomeMCP handles the set_session_outcome MCP tool call.
// Updates the outcome of a session identified by its Claude session ID.
func (s *Server) handleSetSessionOutcomeMCP(ctx context.Context, args json.RawMessage) (string, error) {
	if s.sessionStore == nil {
		return "", fmt.Errorf("session store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	sessionID := coerceString(m["session_id"], "")
	if sessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}

	outcomeStr := coerceString(m["outcome"], "")
	if outcomeStr == "" {
		return "", fmt.Errorf("outcome is required")
	}

	outcome := learning.Outcome(outcomeStr)
	if !learning.IsValidOutcome(outcome) {
		return "", fmt.Errorf("outcome must be one of: success, partial, failure, abandoned")
	}

	reason := coerceString(m["reason"], "")

	if err := s.sessionStore.UpdateSessionOutcome(ctx, sessionID, outcomeStr, reason); err != nil {
		return "", fmt.Errorf("failed to update session outcome: %w", err)
	}

	if s.injectionStore != nil && s.observationStore != nil {
		capturedSessionID := sessionID
		capturedOutcome := outcome
		go func() {
			bgCtx := context.Background()
			if _, err := learning.PropagateOutcome(bgCtx, s.injectionStore, s.observationStore, capturedSessionID, capturedOutcome); err != nil {
				log.Warn().Err(err).Str("session", capturedSessionID).Msg("MCP set_session_outcome: outcome propagation failed")
			}
		}()
	}

	return fmt.Sprintf("Session outcome recorded: %s (session: %s)", outcomeStr, sessionID), nil
}
