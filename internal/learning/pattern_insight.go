package learning

import (
	"context"
	"fmt"
	"strings"

	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/strutil"
)

const patternInsightSystemPrompt = "You are a concise technical analyst. Write clear, actionable summaries."

const patternInsightPromptTemplate = `Analyze these observations that form a recurring pattern and write a 2-3 sentence summary.
Explain: WHAT this pattern is, WHY it matters, and WHEN to apply it.
Be specific and actionable. Output plain text only, no markdown.

Observations:
%s`

// GeneratePatternInsight generates a 2-3 sentence LLM summary for a pattern from its source observations.
// Returns an empty string (not an error) when the LLM is unavailable or returns nothing.
func GeneratePatternInsight(ctx context.Context, llm LLMClient, observations []*models.Observation) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("LLM client not available")
	}

	if len(observations) == 0 {
		return "", nil
	}

	// Build observation list — cap at 10 to stay within token budget
	limit := len(observations)
	if limit > 10 {
		limit = 10
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
		sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", obs.Type, title, narrative))
	}

	userPrompt := fmt.Sprintf(patternInsightPromptTemplate, sb.String())
	result, err := llm.Complete(ctx, patternInsightSystemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("LLM completion failed: %w", err)
	}

	return strings.TrimSpace(result), nil
}

// IsGenericDescription reports whether desc is the auto-generated placeholder
// set when a pattern is first detected (before any LLM summarisation).
func IsGenericDescription(desc string) bool {
	return desc == "" || strings.HasPrefix(desc, "Automatically detected pattern")
}
