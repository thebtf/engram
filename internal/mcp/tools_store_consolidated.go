package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/dedup"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/pkg/models"
)

// rawContentExtractionPrompt is sent to the LLM to extract structured
// observations from arbitrary content provided by the user.
const rawContentExtractionPrompt = `Analyze this content and extract memorable observations.

For each observation, output:
- type: decision | bugfix | feature | refactor | change | discovery | guidance
- title: Short descriptive title (max 80 chars)
- narrative: What happened and why it matters (max 300 chars)
- concepts: From list: how-it-works, why-it-exists, what-changed, problem-solution, gotcha, pattern, trade-off, best-practice, anti-pattern, architecture, security, performance, testing, debugging, workflow, tooling, refactoring, api, database, configuration, error-handling

Output valid JSON only:
{
  "observations": [
    {"type": "decision", "title": "...", "narrative": "...", "concepts": ["architecture"]}
  ]
}

Maximum 5 observations per content block. If no clear observations, return {"observations": []}.`

// handleStoreConsolidated routes store tool actions to the appropriate handler.
func (s *Server) handleStoreConsolidated(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	action := coerceString(m["action"], "create")

	switch action {
	case "create":
		return s.handleStoreMemory(ctx, args)
	case "edit":
		return s.handleEditObservation(ctx, args)
	case "merge":
		return s.handleMergeObservations(ctx, args)
	case "import":
		return s.handleImportInstincts(ctx, args)
	case "extract":
		return s.handleExtractAndOperate(ctx, args)
	default:
		return "", fmt.Errorf("unknown store action: %q (valid: create, edit, merge, import, extract)", action)
	}
}

// handleExtractAndOperate uses an LLM to extract structured observations from
// arbitrary content and stores them as individual observations.
func (s *Server) handleExtractAndOperate(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	content := coerceString(m["content"], "")
	if content == "" {
		return "", fmt.Errorf("content is required for extract action")
	}
	if len(content) < 50 {
		return "Content too short for extraction (minimum 50 characters).", nil
	}

	// Truncate to ~8000 tokens (~32000 chars)
	if len(content) > 32000 {
		content = content[:32000]
	}

	// Redact secrets before sending to LLM.
	if privacy.ContainsSecrets(content) {
		log.Warn().Msg("extract: content contains secrets — redacting before LLM call")
		content = privacy.RedactSecrets(content)
	}

	project := coerceString(m["project"], "")
	scope := coerceString(m["scope"], "project")

	// Create LLM client for extraction.
	llmCfg := learning.DefaultOpenAIConfig()
	llmClient := learning.NewOpenAIClient(llmCfg)
	if !llmClient.IsConfigured() {
		return "LLM not configured — cannot extract observations. Set ENGRAM_LLM_URL and ENGRAM_LLM_API_KEY.", nil
	}

	// Call LLM for extraction.
	response, err := llmClient.Complete(ctx, rawContentExtractionPrompt, content)
	if err != nil {
		return "", fmt.Errorf("extraction LLM call failed: %w", err)
	}

	// Strip markdown code fences if present.
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var result struct {
		Observations []struct {
			Type      string   `json:"type"`
			Title     string   `json:"title"`
			Narrative string   `json:"narrative"`
			Concepts  []string `json:"concepts"`
		} `json:"observations"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return "Failed to parse LLM extraction response: " + err.Error(), nil
	}

	if len(result.Observations) == 0 {
		return `{"extracted": 0, "stored": 0, "duplicates": 0}`, nil
	}

	// Valid observation types for validation.
	validTypes := map[string]bool{
		"decision": true, "bugfix": true, "feature": true,
		"refactor": true, "change": true, "discovery": true, "guidance": true,
	}

	stored := 0
	duplicates := 0
	var titles []string

	for _, obs := range result.Observations {
		if obs.Title == "" || obs.Narrative == "" {
			continue
		}

		// Default to "discovery" if the LLM returns an invalid type.
		obsType := obs.Type
		if !validTypes[obsType] {
			obsType = "discovery"
		}

		parsedObs := &models.ParsedObservation{
			Type:       models.ObservationType(obsType),
			SourceType: models.SourceLLMDerived,
			MemoryType: models.ClassifyMemoryType(&models.ParsedObservation{
				Type:      models.ObservationType(obsType),
				Narrative: obs.Narrative,
				Concepts:  obs.Concepts,
			}),
			Title:     obs.Title,
			Narrative: obs.Narrative,
			Concepts:  obs.Concepts,
			Scope:     models.ObservationScope(scope),
		}

		// Dedup check: skip near-duplicates, supersede contradictions (shared Mem0 Algorithm 1).
		dedupResult, dedupErr := dedup.CheckDuplicate(ctx, s.vectorClient, s.observationStore.GetDB(), project, obs.Narrative, 0)
		if dedupErr != nil {
			log.Debug().Err(dedupErr).Str("title", obs.Title).Msg("extract: dedup check failed, proceeding with ADD")
		}
		if dedupResult != nil && dedupResult.Action == dedup.ActionNoop {
			duplicates++
			continue
		}

		// Generate a unique session ID to avoid duplicate key violations.
		extractSessionID := "extract-" + uuid.NewString()

		obsID, _, err := s.observationStore.StoreObservation(ctx, extractSessionID, project, parsedObs, 0, 0)
		if err != nil {
			log.Warn().Err(err).Str("title", obs.Title).Msg("Failed to store extracted observation")
			continue
		}

		// If UPDATE: supersede existing observation.
		if dedupResult != nil && dedupResult.Action == dedup.ActionUpdate && dedupResult.ExistingID > 0 {
			if supersErr := s.observationStore.MarkAsSuperseded(ctx, dedupResult.ExistingID); supersErr != nil {
				log.Warn().Err(supersErr).Int64("id", dedupResult.ExistingID).Msg("extract: failed to mark superseded")
			}
			if s.relationStore != nil {
				_, _ = s.relationStore.StoreRelation(ctx, &models.ObservationRelation{
					SourceID:        obsID,
					TargetID:        dedupResult.ExistingID,
					RelationType:    models.RelationEvolvesFrom,
					Confidence:      dedupResult.Similarity,
					DetectionSource: models.DetectionSourceEmbeddingSimilarity,
				})
			}
		}

		stored++
		titles = append(titles, obs.Title)
	}

	summary := fmt.Sprintf(`{"extracted": %d, "stored": %d, "duplicates": %d, "titles": %s}`,
		len(result.Observations), stored, duplicates, marshalTitles(titles))
	return summary, nil
}

// marshalTitles converts a string slice to a JSON array string.
func marshalTitles(titles []string) string {
	b, _ := json.Marshal(titles)
	return string(b)
}
