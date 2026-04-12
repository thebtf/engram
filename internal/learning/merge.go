package learning

import (
	"context"
	"fmt"
	"strings"
	"time"

	json "github.com/goccy/go-json"
	"github.com/thebtf/engram/pkg/models"
)

const (
	MergeActionCreateNew = "CREATE_NEW"
	MergeActionUpdate    = "UPDATE"
	MergeActionSupersede = "SUPERSEDE"
	MergeActionSkip      = "SKIP"
)

const (
	// MergeSystemPrompt is the system prompt used for write-time merge decisions.
	MergeSystemPrompt  = "You decide whether a new observation should be CREATE_NEW, UPDATE, SUPERSEDE, or SKIP. Return JSON only."
	decideMergeTimeout = 3 * time.Second
)

// MergeDecision is the write-time merge decision contract for new observations.
type MergeDecision struct {
	Action   string `json:"action"`
	TargetID int64  `json:"target_id"`
}

// DecideMerge asks the LLM for a write-time merge action and validates the response.
func DecideMerge(ctx context.Context, llm LLMClient, newObs *models.Observation, candidates []*models.Observation) (MergeDecision, error) {
	if llm == nil {
		return MergeDecision{Action: MergeActionCreateNew}, nil
	}

	mergeCtx, cancel := context.WithTimeout(ctx, decideMergeTimeout)
	defer cancel()

	systemPrompt := MergeSystemPrompt
	userPrompt := buildDecideMergePrompt(newObs, candidates)
	response, err := llm.Complete(mergeCtx, systemPrompt, userPrompt)
	if err != nil {
		return MergeDecision{Action: MergeActionCreateNew}, nil
	}

	decision, ok := parseMergeDecision(response)
	if !ok {
		return MergeDecision{Action: MergeActionCreateNew}, nil
	}
	return decision, nil
}

func buildDecideMergePrompt(newObs *models.Observation, candidates []*models.Observation) string {
	var sb strings.Builder
	sb.WriteString("New observation:\n")
	if newObs != nil {
		sb.WriteString(formatMergeObservation(newObs))
	}
	sb.WriteString("\nCandidates:\n")
	maxCandidates := min(5, len(candidates))
	for i := 0; i < maxCandidates; i++ {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i, formatMergeObservation(candidates[i])))
	}
	sb.WriteString("Respond as JSON: {\"action\":\"CREATE_NEW|UPDATE|SUPERSEDE|SKIP\",\"target_id\":number}\n")
	return sb.String()
}

func formatMergeObservation(obs *models.Observation) string {
	if obs == nil {
		return "<nil>"
	}
	parts := []string{fmt.Sprintf("type=%s", obs.Type)}
	if obs.Title.Valid {
		parts = append(parts, fmt.Sprintf("title=%s", obs.Title.String))
	}
	if obs.Narrative.Valid {
		parts = append(parts, fmt.Sprintf("narrative=%s", obs.Narrative.String))
	}
	if obs.ID > 0 {
		parts = append(parts, fmt.Sprintf("id=%d", obs.ID))
	}
	return strings.Join(parts, "; ")
}

func parseMergeDecision(raw string) (MergeDecision, bool) {
	var parsed MergeDecision
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return MergeDecision{}, false
	}
	switch parsed.Action {
	case MergeActionCreateNew, MergeActionSkip:
		return parsed, true
	case MergeActionUpdate, MergeActionSupersede:
		if parsed.TargetID <= 0 {
			return MergeDecision{}, false
		}
		return parsed, true
	default:
		return MergeDecision{}, false
	}
}
