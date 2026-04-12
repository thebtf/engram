package synthesis

import (
	"context"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/strutil"
)

// maxObservationsPerBatch is the maximum number of observations sent to the LLM in a single
// extraction call. This is separate from config.EntityExtractionLimit, which controls how
// many observations are fetched per maintenance cycle.
const maxObservationsPerBatch = 10

// EntityExtractor extracts structured entities from observations via LLM.
type EntityExtractor struct{}

// Extract processes a batch of observations and returns extracted entities and relations.
// Observations are capped at maxObservationsPerBatch per call, with narrative truncated to 200 chars.
func (e *EntityExtractor) Extract(ctx context.Context, llm learning.LLMClient, observations []*models.Observation) (*ExtractionResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("LLM client not available")
	}
	if len(observations) == 0 {
		return nil, nil
	}

	limit := len(observations)
	if limit > maxObservationsPerBatch {
		limit = maxObservationsPerBatch
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
			narrative = strutil.Truncate(obs.Narrative.String, 200)
		}
		concepts := ""
		if len(obs.Concepts) > 0 {
			concepts = " [" + strings.Join(obs.Concepts, ", ") + "]"
		}
		sb.WriteString(fmt.Sprintf("- [%s] %s: %s%s\n", obs.Type, title, narrative, concepts))
	}

	userPrompt := fmt.Sprintf(entityExtractionPromptTemplate, sb.String())
	raw, err := llm.Complete(ctx, entityExtractionSystemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLM entity extraction failed: %w", err)
	}

	result, err := ParseEntityExtractionResponse(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse entity extraction response: %w", err)
	}

	return result, nil
}
