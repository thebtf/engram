package learning

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/strutil"
)

const ProjectBriefingNoChange = "NO_CHANGE"

const projectBriefingSystemPrompt = "You are a concise technical analyst. Produce a compact per-project briefing for future coding sessions."

const projectBriefingPromptTemplate = `Update the project briefing for project %q.

If the existing briefing is still materially accurate, reply with EXACTLY: NO_CHANGE

Write plain text only. Maximum 4000 characters.
Organize the result with these sections:
- Active Work
- Recent Decisions
- Known Pitfalls
- Stack and Conventions

Existing briefing:
%s

Recent observations:
%s`

func GenerateProjectBriefing(ctx context.Context, llm LLMClient, project string, existingBriefing string, observations []*models.Observation) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("LLM client not available")
	}
	if len(observations) == 0 {
		return "", nil
	}

	limit := len(observations)
	if limit > 20 {
		limit = 20
	}

	var sb strings.Builder
	for i := 0; i < limit; i++ {
		obs := observations[i]
		title := ""
		if obs.Title.Valid {
			title = obs.Title.String
		}
		narrative := ""
		if obs.Narrative.Valid {
			narrative = strutil.Truncate(obs.Narrative.String, 250)
		}
		sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", obs.Type, title, narrative))
	}

	if strings.TrimSpace(existingBriefing) == "" {
		existingBriefing = "(none)"
	}

	userPrompt := fmt.Sprintf(projectBriefingPromptTemplate, project, existingBriefing, sb.String())
	result, err := llm.Complete(ctx, projectBriefingSystemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("LLM completion failed: %w", err)
	}

	result = strings.TrimSpace(result)
	if result == "" {
		return "", nil
	}
	if result == ProjectBriefingNoChange {
		return ProjectBriefingNoChange, nil
	}
	return strutil.Truncate(result, 4000), nil
}

func NewProjectBriefingObservation(project, content string) *models.ParsedObservation {
	return &models.ParsedObservation{
		Type:       models.ObsTypeWiki,
		MemoryType: models.MemTypeContext,
		SourceType: models.SourceLLMDerived,
		Title:      fmt.Sprintf("Project Briefing: %s", project),
		Narrative:  content,
		Concepts:   []string{"project-briefing", "wiki"},
		Scope:      models.ScopeProject,
	}
}

func ObservationNarrativeString(obs *models.Observation) string {
	if obs == nil {
		return ""
	}
	if obs.Narrative.Valid {
		return obs.Narrative.String
	}
	return ""
}

func ObservationTitleString(obs *models.Observation) string {
	if obs == nil {
		return ""
	}
	if obs.Title.Valid {
		return obs.Title.String
	}
	return ""
}

func ObservationWithNarrative(title, narrative string, obsType models.ObservationType) *models.Observation {
	return &models.Observation{
		Type:      obsType,
		Title:     sql.NullString{String: title, Valid: title != ""},
		Narrative: sql.NullString{String: narrative, Valid: narrative != ""},
	}
}
