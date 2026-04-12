package synthesis

import (
	"context"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/strutil"
)

// WikiGenerator generates wiki summaries for entities from their source observations.
type WikiGenerator struct{}

// Generate creates a wiki summary for an entity from its source observations.
// Source observations are capped at 10, with content truncated to fit ~4000 token budget.
func (w *WikiGenerator) Generate(ctx context.Context, llm learning.LLMClient, entity *models.Observation, sourceObs []*models.Observation) (*WikiResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("LLM client not available")
	}
	if entity == nil {
		return nil, fmt.Errorf("entity observation is nil")
	}

	entityName := ""
	if entity.Title.Valid {
		entityName = entity.Title.String
	}
	entityDesc := ""
	if entity.Subtitle.Valid {
		entityDesc = entity.Subtitle.String // subtitle stores entity_type (technology, concept, etc.)
	}

	if entityName == "" {
		return nil, fmt.Errorf("entity has no name")
	}

	// Build entity context line
	entityContext := entityName
	if entityDesc != "" {
		entityContext = fmt.Sprintf("%s (%s)", entityName, entityDesc)
	}

	// Build source observation list (cap at 10, ~4000 token budget ≈ 16000 chars)
	limit := len(sourceObs)
	if limit > 10 {
		limit = 10
	}

	var sb strings.Builder
	totalChars := 0
	const maxChars = 14000 // ~3500 tokens, leaving room for prompt template

	for i := 0; i < limit && totalChars < maxChars; i++ {
		obs := sourceObs[i]
		title := ""
		if obs.Title.Valid {
			title = obs.Title.String
		}
		narrative := ""
		if obs.Narrative.Valid {
			remaining := maxChars - totalChars - 100 // overhead per entry
			maxNarrative := 500
			if remaining < maxNarrative {
				maxNarrative = remaining
			}
			if maxNarrative > 0 {
				narrative = strutil.Truncate(obs.Narrative.String, maxNarrative)
			}
		}

		entry := fmt.Sprintf("- [%s] %s: %s\n", obs.Type, title, narrative)
		totalChars += len(entry)
		sb.WriteString(entry)
	}

	userPrompt := fmt.Sprintf(wikiGenerationPromptTemplate, entityContext, sb.String())
	content, err := llm.Complete(ctx, wikiGenerationSystemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLM wiki generation failed: %w", err)
	}

	return &WikiResult{
		EntityName: entityName,
		Content:    strings.TrimSpace(content),
	}, nil
}
