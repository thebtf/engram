package learning

import (
	"context"
	"fmt"
	"strings"
)

const condenserSystemPrompt = "You are a concise technical writer. Condense observation text to its essential actionable facts."

// condenserPromptTemplate is the prompt used to condense an observation narrative via LLM.
// Placeholders: {injections}, {narrative}.
const condenserPromptTemplate = `This observation has been injected %d times.
Based on usage data, simplify it to only the essential facts.
Remove any background context that agents don't use.

Original:
"%s"

Output a condensed version (max 200 words), keeping only actionable facts.`

// CondenseUsageData holds the usage stats provided to the condenser prompt.
type CondenseUsageData struct {
	// Injections is the total number of times this observation was injected.
	Injections int
}

// CondenseObservation calls the LLM to produce a condensed version of an observation narrative.
// It strips background context and keeps only actionable facts, targeting a 200-word maximum.
//
// This is a standalone utility — not yet wired into the injection pipeline.
// Future work will use it to auto-condense observations after enough injection data accumulates.
func CondenseObservation(ctx context.Context, llm LLMClient, narrative string, data CondenseUsageData) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("LLM client not available")
	}
	if strings.TrimSpace(narrative) == "" {
		return "", fmt.Errorf("narrative is empty")
	}

	userPrompt := fmt.Sprintf(condenserPromptTemplate, data.Injections, narrative)

	result, err := llm.Complete(ctx, condenserSystemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("LLM condensation failed: %w", err)
	}

	condensed := strings.TrimSpace(result)
	if condensed == "" {
		return "", fmt.Errorf("LLM returned empty condensed text")
	}

	return condensed, nil
}
