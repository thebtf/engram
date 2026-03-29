package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/strutil"
)

// ExtractedLearning represents a single learning extracted by the LLM.
type ExtractedLearning struct {
	Title     string   `json:"title"`
	Narrative string   `json:"narrative"`
	Concepts  []string `json:"concepts"`
	Type      string   `json:"type"`   // "guidance", "decision", "bugfix", "discovery", etc.
	Signal    string   `json:"signal"` // legacy: "correction", "preference", "pattern"
}

// ExtractionResult is the LLM response structure.
type ExtractionResult struct {
	Learnings []ExtractedLearning `json:"learnings"`
}

// Extractor handles LLM-based extraction of behavioral patterns from transcripts.
type Extractor struct {
	llm LLMClient
}

// NewExtractor creates a new learning extractor.
func NewExtractor(llm LLMClient) *Extractor {
	return &Extractor{llm: llm}
}

// IsEnabled returns true if learning extraction is enabled and configured.
func IsEnabled() bool {
	flag := os.Getenv("ENGRAM_LEARNING_ENABLED")
	return flag == "true" || flag == "1"
}

// ExtractGuidance analyzes a session transcript and returns guidance observations.
func (e *Extractor) ExtractGuidance(ctx context.Context, messages []Message, project string) ([]*models.ParsedObservation, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	// Sanitize transcript for LLM input
	sanitized := SanitizeTranscript(messages, DefaultMaxMessages, DefaultMaxMessageLen)
	if len(sanitized) == 0 {
		return nil, nil
	}

	// Build prompt
	userPrompt := FormatTranscriptForExtraction(sanitized)

	// Call LLM
	response, err := e.llm.Complete(ctx, extractionSystemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLM extraction failed: %w", err)
	}

	// Parse response
	learnings, err := parseLearnings(response)
	if err != nil {
		log.Warn().Err(err).Str("response", strutil.Truncate(response, 200)).Msg("Failed to parse LLM extraction response")
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	if len(learnings) == 0 {
		return nil, nil
	}

	// Convert to parsed observations
	observations := make([]*models.ParsedObservation, 0, len(learnings))
	for _, l := range learnings {
		// Validate concepts against allowed list
		validConcepts := filterValidConcepts(l.Concepts)

		observations = append(observations, &models.ParsedObservation{
			Type:      learningToObsType(l),
			Title:     l.Title,
			Narrative: l.Narrative,
			Concepts:  validConcepts,
			Scope:     models.ScopeGlobal,
		})
	}

	return observations, nil
}

// parseLearnings extracts learnings from the LLM response string.
func parseLearnings(response string) ([]ExtractedLearning, error) {
	// Try to find JSON in the response (LLM might add markdown fences)
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var result ExtractionResult
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Validate and filter
	valid := make([]ExtractedLearning, 0, len(result.Learnings))
	for _, l := range result.Learnings {
		if l.Title == "" || l.Narrative == "" {
			continue
		}
		// Cap title length
		if len(l.Title) > 100 {
			l.Title = l.Title[:100]
		}
		// Cap narrative length
		if len(l.Narrative) > 500 {
			l.Narrative = l.Narrative[:500]
		}
		// Validate type/signal — at least one must be set
		if l.Type == "" {
			switch l.Signal {
			case "correction", "preference", "pattern":
				// valid legacy signal
			default:
				l.Signal = "pattern"
			}
		}
		valid = append(valid, l)
	}

	// Cap at 5 learnings max
	if len(valid) > 5 {
		valid = valid[:5]
	}

	return valid, nil
}

// allowedConcepts is the set of valid concept values.
var allowedConcepts = map[string]bool{
	"security": true, "gotcha": true, "best-practice": true, "anti-pattern": true,
	"architecture": true, "performance": true, "error-handling": true, "pattern": true,
	"testing": true, "debugging": true, "problem-solution": true, "trade-off": true,
	"workflow": true, "tooling": true, "how-it-works": true, "why-it-exists": true,
	"what-changed": true,
}

// learningToObsType determines observation type from extracted learning.
// Prefers the new "type" field; falls back to legacy "signal" mapping.
func learningToObsType(l ExtractedLearning) models.ObservationType {
	// New prompt returns type directly
	switch models.ObservationType(l.Type) {
	case models.ObsTypeGuidance, models.ObsTypeDecision, models.ObsTypeBugfix,
		models.ObsTypeDiscovery, models.ObsTypeFeature, models.ObsTypeRefactor,
		models.ObsTypeChange:
		return models.ObservationType(l.Type)
	}
	// Legacy signal fallback
	switch l.Signal {
	case "correction", "preference":
		return models.ObsTypeGuidance
	case "pattern":
		return models.ObsTypeDiscovery
	}
	return models.ObsTypeGuidance
}

// filterValidConcepts returns only concepts from the allowed set.
func filterValidConcepts(concepts []string) []string {
	var valid []string
	for _, c := range concepts {
		if allowedConcepts[c] {
			valid = append(valid, c)
		}
	}
	return valid
}

