// Package search provides unified search capabilities for engram.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/strutil"
)

// llmFilterSystemPrompt is the system prompt for the LLM relevance filter.
const llmFilterSystemPrompt = `You are a memory relevance filter for an AI coding agent. Given the agent's current project and task, determine which memories would change the agent's behavior. Return ONLY a JSON array of relevant observation IDs.`

// llmFilterNarrativeTruncate is the maximum characters of narrative included in each candidate.
const llmFilterNarrativeTruncate = 200

// LLMFilter evaluates observation candidates for behavioral relevance using an LLM.
type LLMFilter struct {
	client  learning.LLMClient
	timeout time.Duration
}

// NewLLMFilter creates a new LLM-based relevance filter.
func NewLLMFilter(client learning.LLMClient, timeout time.Duration) *LLMFilter {
	return &LLMFilter{
		client:  client,
		timeout: timeout,
	}
}

// FilterByRelevance evaluates candidates against the current task context.
// Returns the subset of observation IDs that the LLM considers behaviorally relevant.
// On timeout or error, returns all candidate IDs (fallback to composite scoring).
func (f *LLMFilter) FilterByRelevance(ctx context.Context, candidates []*models.Observation, project, taskContext string) []int64 {
	// Build full ID list for fallback
	allIDs := make([]int64, len(candidates))
	for i, obs := range candidates {
		allIDs[i] = obs.ID
	}

	if len(candidates) == 0 {
		return allIDs
	}

	// Build the user prompt
	userPrompt := buildLLMFilterPrompt(candidates, project, taskContext)

	// Apply timeout
	filterCtx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	response, err := f.client.Complete(filterCtx, llmFilterSystemPrompt, userPrompt)
	if err != nil {
		log.Warn().Err(err).
			Str("project", project).
			Int("candidates", len(candidates)).
			Msg("LLM filter failed, returning all candidates")
		return allIDs
	}

	relevantIDs, err := parseLLMFilterResponse(response)
	if err != nil {
		log.Warn().Err(err).
			Int("response_len", len(response)).
			Msg("LLM filter response parse failed, returning all candidates")
		return allIDs
	}

	if len(relevantIDs) == 0 {
		// LLM explicitly returned empty set — honor it as "nothing relevant".
		// This is the silence gate: do NOT fall back to top-N.
		log.Debug().
			Str("project", project).
			Int("total", len(candidates)).
			Msgf("LLM filter silenced injection for project %s (no relevant candidates)", project)
		return []int64{}
	}

	log.Debug().
		Str("project", project).
		Int("total", len(candidates)).
		Int("relevant", len(relevantIDs)).
		Msg("LLM filter applied")

	return relevantIDs
}

// buildLLMFilterPrompt constructs the user prompt for the LLM filter call.
func buildLLMFilterPrompt(candidates []*models.Observation, project, taskContext string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Project: %s\nTask: %s\n\nCandidate memories:\n", project, taskContext))

	for _, obs := range candidates {
		narrative := ""
		if obs.Narrative.Valid {
			narrative = strutil.Truncate(obs.Narrative.String, llmFilterNarrativeTruncate)
		}
		title := ""
		if obs.Title.Valid {
			title = obs.Title.String
		}
		sb.WriteString(fmt.Sprintf("[ID: %d] (%s) %s: %s\n", obs.ID, obs.Type, title, narrative))
	}

	sb.WriteString("\nReturn a JSON array of IDs that are behaviorally relevant to the current task. Only include memories that would actually change what the agent does. Example: [123, 456, 789]")
	return sb.String()
}

// parseLLMFilterResponse extracts the JSON array of IDs from the LLM response.
// The LLM is instructed to return only a JSON array, but may include extra text.
func parseLLMFilterResponse(response string) ([]int64, error) {
	response = strings.TrimSpace(response)

	// Find the JSON array in the response (the LLM may wrap it in prose)
	start := strings.Index(response, "[")
	end := strings.LastIndex(response, "]")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON array found in response")
	}

	arrayStr := response[start : end+1]
	var ids []int64
	if err := json.Unmarshal([]byte(arrayStr), &ids); err != nil {
		return nil, fmt.Errorf("unmarshal ID array: %w", err)
	}

	return ids, nil
}
