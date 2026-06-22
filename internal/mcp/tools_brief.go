package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/injection"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

type memoryBriefArgs struct {
	Topic   string `json:"topic"`
	Project string `json:"project"`
	Limit   int    `json:"limit"`
}

// handleGetMemoryBrief returns a compact memory context for sub-agent delegation briefs.
func (s *Server) handleGetMemoryBrief(ctx context.Context, args json.RawMessage) (string, error) {
	if os.Getenv("ENGRAM_ADAPTIVE_ENABLED") != "true" {
		return marshalJSON(map[string]string{"status": "disabled", "message": "set ENGRAM_ADAPTIVE_ENABLED=true to enable"})
	}
	if s.memoryStore == nil {
		return "", fmt.Errorf("memory store not available")
	}

	var a memoryBriefArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if a.Project == "" {
		a.Project = projectFromContext(ctx)
	}
	if a.Project == "" {
		return "", fmt.Errorf("project required")
	}
	if a.Limit <= 0 {
		a.Limit = 5
	}
	if a.Limit > 10 {
		a.Limit = 10
	}

	raw, err := s.memoryStore.ListForInjection(ctx, a.Project, a.Limit*3)
	if err != nil {
		return "", fmt.Errorf("list memories: %w", err)
	}

	// T004 (codex P1 PR #221): apply scope.Resolve filter when
	// ENGRAM_VNEXT_F_ENABLED=true so private memories are not included in
	// briefs for callers that cannot see them.
	candidates := filterInjectionByScope(ctx, raw)

	if len(candidates) == 0 {
		return marshalJSON(map[string]any{
			"project":  a.Project,
			"topic":    a.Topic,
			"memories": []any{},
			"message":  "no memories found for this project",
		})
	}

	var scoreOpts injection.ScoreOpts
	citRate, crErr := s.memoryStore.GetProjectCitationRate(ctx, a.Project, 10)
	if crErr == nil && citRate != 0.5 {
		scoreOpts.DynamicPrior = true
		scoreOpts.ProjectCitationRate = citRate
	}

	scored := injection.Score(candidates, a.Limit, scoreOpts)

	var memories []map[string]any
	for _, sm := range scored {
		if !sm.Selected || sm.Memory == nil {
			break
		}
		content := sm.Memory.Content
		runes := []rune(content)
		if len(runes) > 200 {
			content = string(runes[:200]) + "..."
		}
		memories = append(memories, map[string]any{
			"id":      sm.Memory.ID,
			"content": content,
			"tags":    sm.Memory.Tags,
		})
	}

	return marshalJSON(map[string]any{
		"project":  a.Project,
		"topic":    a.Topic,
		"memories": memories,
	})
}

// filterInjectionByScope applies the shared memory visibility predicate to a
// slice of injection candidates. ENGRAM_VNEXT_F_ENABLED gates only the legacy
// privacy_scope layer; principal-private rows are filtered fail-safe.
func filterInjectionByScope(ctx context.Context, mems []*models.Memory) []*models.Memory {
	caller := scope.KeycardContext{}
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
		caller.Principal = id.Principal
		caller.PrincipalKind = string(id.PrincipalKind)
	}
	opts := scope.MemoryVisibilityOptions{
		ApplyPrivacyScope: os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true",
	}
	visible := make([]*models.Memory, 0, len(mems))
	for _, mem := range mems {
		if scope.ResolveMemory(caller, mem, opts) {
			visible = append(visible, mem)
		}
	}
	return visible
}
