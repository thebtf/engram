package learning

import (
	"context"
	"fmt"
	"strings"
)

const apoSystemPrompt = "You are a concise technical writer specializing in AI agent guidance rules. Rewrite rules to be clear, specific, and actionable."

// apoPromptTemplate is the prompt used to rewrite a guidance observation via LLM.
// Placeholders: {injections}, {successes}, {rate}, {narrative}.
const apoPromptTemplate = `This guidance rule is injected into AI agent context to influence behavior.
It was injected %d times but only helped in %d sessions (effectiveness: %.0f%%).

Current text:
"%s"

Rewrite this rule to be more specific, actionable, and context-aware.
Keep the same intent but make it clearer and more likely to be followed.
Output the rewritten rule only, no commentary.`

// APOEffectivenessData holds the effectiveness stats used to build the APO rewrite prompt.
type APOEffectivenessData struct {
	// Injections is the total number of times this observation was injected.
	Injections int
	// Successes is the number of sessions that had a successful outcome after injection.
	Successes int
}

// RewriteGuidance calls the LLM to produce an improved version of a guidance observation narrative.
// It returns the rewritten text, which the caller can store as a new ObservationVersion.
// Returns an error when the LLM is unavailable or the response is empty.
func RewriteGuidance(ctx context.Context, llm LLMClient, narrative string, data APOEffectivenessData) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("LLM client not available")
	}
	if strings.TrimSpace(narrative) == "" {
		return "", fmt.Errorf("narrative is empty")
	}

	var rate float64
	if data.Injections > 0 {
		rate = float64(data.Successes) / float64(data.Injections) * 100
	}

	userPrompt := fmt.Sprintf(apoPromptTemplate, data.Injections, data.Successes, rate, narrative)

	result, err := llm.Complete(ctx, apoSystemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("LLM rewrite failed: %w", err)
	}

	rewritten := strings.TrimSpace(result)
	if rewritten == "" {
		return "", fmt.Errorf("LLM returned empty rewrite")
	}

	return rewritten, nil
}
