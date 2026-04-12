// Package relation provides causal classification for observation pairs.
package relation

import (
	"context"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/pkg/models"
)

const causalClassifierSystemPrompt = `You are an observation relationship classifier for a memory system.
Given two observations from coding sessions, classify their relationship.

Respond with EXACTLY ONE of these labels:
- fixed_by: Observation A describes an error/bug, Observation B describes its fix
- corrects: Observation B is a behavioral rule that corrects the behavior in Observation A
- unrelated: The observations are not causally related

Output format: just the label, nothing else. Example: "fixed_by"`

// CausalClassifier uses LLM to classify causal relationships between observation pairs.
type CausalClassifier struct {
	llmClient learning.LLMClient
}

// NewCausalClassifier creates a new classifier. Returns nil if no LLM client available.
func NewCausalClassifier(llmClient learning.LLMClient) *CausalClassifier {
	if llmClient == nil {
		return nil
	}
	return &CausalClassifier{llmClient: llmClient}
}

// ClassifyPair determines the causal relationship between two observations.
// Returns one of: "fixed_by", "corrects", "unrelated", or "" on error.
func (c *CausalClassifier) ClassifyPair(ctx context.Context, obsA, obsB *models.Observation) (string, error) {
	if c == nil || c.llmClient == nil {
		return "", fmt.Errorf("causal classifier not available")
	}

	userPrompt := fmt.Sprintf(
		"Observation A (type: %s):\nTitle: %s\nNarrative: %s\n\nObservation B (type: %s):\nTitle: %s\nNarrative: %s",
		obsA.Type, obsA.Title.String, obsA.Narrative.String,
		obsB.Type, obsB.Title.String, obsB.Narrative.String,
	)

	response, err := c.llmClient.Complete(ctx, causalClassifierSystemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("LLM classification: %w", err)
	}

	// Parse response — expect a single label
	label := strings.TrimSpace(strings.ToLower(response))
	switch label {
	case "fixed_by", "corrects", "unrelated":
		return label, nil
	default:
		// Try to extract label from verbose response
		for _, candidate := range []string{"fixed_by", "corrects", "unrelated"} {
			if strings.Contains(label, candidate) {
				return candidate, nil
			}
		}
		return "unrelated", nil
	}
}

// ShouldClassify returns true if this observation type warrants causal classification.
func ShouldClassify(obs *models.Observation) bool {
	switch obs.Type {
	case models.ObsTypeBugfix, models.ObsTypeGuidance:
		return true
	default:
		return false
	}
}
