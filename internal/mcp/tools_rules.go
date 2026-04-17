package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/pkg/models"
)

// handleStoreRule creates a new behavioral rule via the BehavioralRulesStore.
// Input schema: {project?: string, content: string (required), priority?: number (default 0)}
// Returns JSON: {id, project, content, priority, created_at}
func (s *Server) handleStoreRule(ctx context.Context, args json.RawMessage) (string, error) {
	if s.behavioralRulesStore == nil {
		return "", fmt.Errorf("behavioral rules store not initialised")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	content := coerceString(m["content"], "")
	if content == "" {
		return "", fmt.Errorf("store_rule: content is required and must not be empty")
	}

	priority := int(coerceFloat64(m["priority"], 0))

	var project *string
	if raw, ok := m["project"]; ok {
		if sv, ok2 := raw.(string); ok2 && sv != "" {
			cp := sv
			project = &cp
		}
	}

	rule := &models.BehavioralRule{
		Project:  project,
		Content:  content,
		Priority: priority,
	}

	created, err := s.behavioralRulesStore.Create(ctx, rule)
	if err != nil {
		return "", fmt.Errorf("store_rule: %w", err)
	}

	type response struct {
		CreatedAt any    `json:"created_at"`
		Project   any    `json:"project"`
		Content   string `json:"content"`
		ID        int64  `json:"id"`
		Priority  int    `json:"priority"`
	}

	var projOut any
	if created.Project != nil {
		projOut = *created.Project
	}
	createdAtOut := created.CreatedAt.Format("2006-01-02T15:04:05Z07:00")

	resp := response{
		ID:        created.ID,
		Project:   projOut,
		Content:   created.Content,
		Priority:  created.Priority,
		CreatedAt: createdAtOut,
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("store_rule: marshal response: %w", err)
	}
	return string(out), nil
}

// handleListRules lists behavioral rules via the BehavioralRulesStore.
// Input schema: {project?: string, limit?: number (default 50, max 500)}
// Returns a JSON array of rule objects.
func (s *Server) handleListRules(ctx context.Context, args json.RawMessage) (string, error) {
	if s.behavioralRulesStore == nil {
		return "", fmt.Errorf("behavioral rules store not initialised")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	limit := int(coerceFloat64(m["limit"], 50))
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	var project *string
	if raw, ok := m["project"]; ok {
		if sv, ok2 := raw.(string); ok2 && sv != "" {
			cp := sv
			project = &cp
		}
	}

	rules, err := s.behavioralRulesStore.List(ctx, project, limit)
	if err != nil {
		return "", fmt.Errorf("list_rules: %w", err)
	}

	type ruleItem struct {
		CreatedAt any    `json:"created_at"`
		UpdatedAt any    `json:"updated_at"`
		Project   any    `json:"project"`
		Content   string `json:"content"`
		EditedBy  string `json:"edited_by,omitempty"`
		ID        int64  `json:"id"`
		Priority  int    `json:"priority"`
		Version   int    `json:"version"`
	}

	items := make([]ruleItem, 0, len(rules))
	for _, r := range rules {
		var projOut any
		if r.Project != nil {
			projOut = *r.Project
		}
		createdAtOut := r.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
		updatedAtOut := r.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
		items = append(items, ruleItem{
			ID:        r.ID,
			Project:   projOut,
			Content:   r.Content,
			Priority:  r.Priority,
			Version:   r.Version,
			EditedBy:  r.EditedBy,
			CreatedAt: createdAtOut,
			UpdatedAt: updatedAtOut,
		})
	}

	out, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("list_rules: marshal response: %w", err)
	}
	return string(out), nil
}

