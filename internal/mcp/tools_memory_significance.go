package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s6"
)

type memorySignificanceUpdater interface {
	RateMemorySignificance(ctx context.Context, id int64, rating string) error
}

type memoryEditorSignificanceUpdater struct {
	editor memoryEditor
}

func newMemoryEditorSignificanceUpdater(editor memoryEditor) memorySignificanceUpdater {
	if editor == nil {
		return nil
	}
	return &memoryEditorSignificanceUpdater{editor: editor}
}

func s6OutcomeEnabledFromEnv() bool {
	return cognitivecore.LoadFlagConfigFromEnv().IsSubsystemEnabled("s6")
}

func (s *Server) effectiveMemorySignificanceUpdater() memorySignificanceUpdater {
	if s.testMemorySignificanceUpdater != nil {
		return s.testMemorySignificanceUpdater
	}
	return newMemoryEditorSignificanceUpdater(s.effectiveMemoryEditor())
}

func (s *Server) currentMemorySignificanceUpdater() (memorySignificanceUpdater, error) {
	if !s6OutcomeEnabledFromEnv() {
		return nil, fmt.Errorf("rate_memory_significance feature flag required")
	}
	updater := s.effectiveMemorySignificanceUpdater()
	if updater == nil {
		return nil, fmt.Errorf("memory significance updater not available")
	}
	return updater, nil
}

func rateMemorySignificanceTool() Tool {
	return Tool{
		Name:        "rate_memory_significance",
		Description: "Explicitly rate a memory as useful or not_useful for S6 outcome policy learning.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"id", "rating"},
			"properties": map[string]any{
				"id":     map[string]any{"type": "integer", "description": "Memory ID to rate"},
				"rating": map[string]any{"type": "string", "enum": []string{s6.RatingUseful, s6.RatingNotUseful}, "description": "Rating: useful or not_useful"},
			},
		},
	}
}

func (s *Server) handleRateMemorySignificance(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	updater, err := s.currentMemorySignificanceUpdater()
	if err != nil {
		return "", err
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	id := coerceInt64(m["id"], 0)
	if id <= 0 {
		return "", fmt.Errorf("id required and must be > 0")
	}

	rating := coerceString(m["rating"], "")
	if rating != s6.RatingUseful && rating != s6.RatingNotUseful {
		return "", fmt.Errorf("rating must be '%s' or '%s'", s6.RatingUseful, s6.RatingNotUseful)
	}

	if err := updater.RateMemorySignificance(ctx, id, rating); err != nil {
		return "", err
	}

	return marshalJSON(map[string]any{
		"status": "rated",
		"id":     id,
		"rating": rating,
	})
}

func (u *memoryEditorSignificanceUpdater) RateMemorySignificance(ctx context.Context, id int64, rating string) error {
	if u == nil || u.editor == nil {
		return fmt.Errorf("memory significance updater not available")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	before, err := u.editor.Get(ctx, id)
	if err != nil {
		return err
	}
	if before == nil {
		return fmt.Errorf("memory %d not found", id)
	}

	updated := *before
	if err := s6.ApplySignificanceRating(&updated, rating); err != nil {
		return err
	}
	_, err = u.editor.Update(ctx, &updated)
	return err
}
