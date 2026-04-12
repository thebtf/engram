// Package sdk provides SDK agent integration for engram.
package sdk

import "strings"

// ReasoningStep represents one step in a reasoning trace.
type ReasoningStep struct {
	Type    string `json:"type"`    // thought, action, observation, decision, conclusion
	Content string `json:"content"`
}

// ReasoningTrace represents an extracted reasoning chain.
type ReasoningTrace struct {
	Steps       []ReasoningStep        `json:"steps"`
	TaskContext ReasoningTraceContext   `json:"task_context"`
	QualityScore float64               `json:"quality_score"`
}

// ReasoningTraceContext describes the task that triggered the reasoning.
type ReasoningTraceContext struct {
	Goal       string `json:"goal,omitempty"`
	Domain     string `json:"domain,omitempty"`
	Complexity string `json:"complexity,omitempty"` // low, medium, high
}

// reasoningPatterns are indicators of multi-step reasoning in agent output.
var reasoningPatterns = []string{
	"because", "therefore", "considering", "after analyzing",
	"first i", "then i", "finally i",
	"option a", "option b", "alternative",
	"pros and cons", "trade-off", "weighed",
	"investigated", "root cause", "concluded",
	"hypothesis", "verified", "confirmed",
}

// minReasoningPatternMatches is the minimum number of reasoning indicators
// required for text to be considered worth extracting.
const minReasoningPatternMatches = 3

// minReasoningTextLength is the minimum text length for reasoning detection.
const minReasoningTextLength = 200

// DetectReasoning checks if text contains reasoning patterns worth extracting.
// Returns true when at least minReasoningPatternMatches indicators are found
// and the text is at least minReasoningTextLength characters long.
func DetectReasoning(text string) bool {
	if len(text) < minReasoningTextLength {
		return false
	}
	lower := strings.ToLower(text)
	matches := 0
	for _, p := range reasoningPatterns {
		if strings.Contains(lower, p) {
			matches++
		}
		if matches >= minReasoningPatternMatches {
			return true
		}
	}
	return false
}
