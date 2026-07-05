package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s3ambient"
	"github.com/thebtf/engram/pkg/cognitive"
)

type ambientHintItem struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags,omitempty"`
	Score  float32  `json:"score,omitempty"`
	Source string   `json:"source,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

type ambientHintsOutput struct {
	Hints []ambientHintItem `json:"hints,omitempty"`
}

func ambientHintsEnabledFromEnv() bool {
	cfg := cognitivecore.LoadFlagConfigFromEnv()
	return cfg.IsPlugEnabled() && cfg.IsSubsystemEnabled("s3")
}

func ambientHintsTool() Tool {
	return Tool{
		Name:        "get_ambient_hints",
		Description: "Drain the current session's bounded ambient hint queue for S3 fallback polling.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"session_id"},
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string", "description": "Session ID whose ambient queue should be drained"},
				"limit":      map[string]any{"type": "integer", "description": "Optional result cap (default 3, max 3)"},
			},
		},
	}
}

func (s *Server) handleGetAmbientHints(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !ambientHintsEnabledFromEnv() || s == nil || s.hintQueue == nil {
		return marshalJSON(ambientHintsOutput{Hints: []ambientHintItem{}})
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	sessionID := coerceString(m["session_id"], "")
	if sessionID == "" {
		return "", errMissingSessionID()
	}
	limit := normalizeAmbientHintsToolLimit(coerceInt(m["limit"], 3))

	stats := s.hintQueue.Stats(sessionID)
	if stats.QueuedNow == 0 {
		return marshalJSON(ambientHintsOutput{Hints: []ambientHintItem{}})
	}
	proposals := s3ambient.DrainQueuedProposals(s.hintQueue, sessionID, time.Now().UTC())
	if len(proposals) == 0 {
		return marshalJSON(ambientHintsOutput{Hints: []ambientHintItem{}})
	}

	emitter := s3ambient.NewEmitter(true)
	delivery, err := emitter.Render(ctx, cognitive.HintSurfaceMCPPoll, sessionID, proposals)
	if err != nil || len(delivery.Hints) == 0 {
		return marshalJSON(ambientHintsOutput{Hints: []ambientHintItem{}})
	}
	if len(delivery.Hints) > limit {
		delivery.Hints = delivery.Hints[:limit]
	}
	return marshalJSON(ambientHintsOutput{Hints: ambientHintItems(delivery.Hints)})
}

func normalizeAmbientHintsToolLimit(limit int) int {
	if limit <= 0 {
		return 3
	}
	if limit > 3 {
		return 3
	}
	return limit
}

func ambientHintItems(hints []cognitive.HintProposal) []ambientHintItem {
	if len(hints) == 0 {
		return []ambientHintItem{}
	}
	out := make([]ambientHintItem, 0, len(hints))
	for _, hint := range hints {
		out = append(out, ambientHintItem{
			ID:     hint.ID,
			Title:  hint.Title,
			Tags:   append([]string(nil), hint.Tags...),
			Score:  hint.Score,
			Source: hint.Source,
			Reason: hint.Reason,
		})
	}
	return out
}

func errMissingSessionID() error {
	return errors.New("session_id required")
}
