package worker

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/injection"
	"github.com/thebtf/engram/pkg/models"
)

type reinjectRequest struct {
	Project   string `json:"project"`
	Topic     string `json:"topic"`
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit"`
}

// handleReinject returns topic-relevant memories scored by Thompson Sampling,
// formatted for mid-session re-injection. Called by the PreCompact hook.
func (s *Service) handleReinject(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENGRAM_ADAPTIVE_ENABLED") != "true" {
		writeJSON(w, map[string]string{"status": "disabled"})
		return
	}

	var req reinjectRequest
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	} else {
		req.Project = r.URL.Query().Get("project")
		req.Topic = r.URL.Query().Get("topic")
		req.SessionID = r.URL.Query().Get("session_id")
	}

	if req.Project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 30 {
		req.Limit = 30
	}

	s.initMu.RLock()
	memStore := s.memoryStore
	s.initMu.RUnlock()

	if memStore == nil {
		http.Error(w, "memory store not ready", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()

	candidates, err := memStore.ListForInjection(ctx, req.Project, req.Limit*3)
	if err != nil {
		log.Error().Err(err).Str("project", req.Project).Msg("reinject: list failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var scoreOpts injection.ScoreOpts
	citRate, crErr := memStore.GetProjectCitationRate(ctx, req.Project, 10)
	if crErr == nil && citRate != 0.5 {
		scoreOpts.DynamicPrior = true
		scoreOpts.ProjectCitationRate = citRate
	}

	scored := injection.Score(candidates, req.Limit, scoreOpts)

	var memories []map[string]any
	for _, sm := range scored {
		if !sm.Selected || sm.Memory == nil {
			break
		}
		memories = append(memories, map[string]any{
			"id":      sm.Memory.ID,
			"content": truncate(sm.Memory.Content, 500),
			"tags":    sm.Memory.Tags,
			"score":   sm.Score,
		})
	}

	writeJSON(w, map[string]any{
		"status":   "ok",
		"project":  req.Project,
		"topic":    req.Topic,
		"memories": memories,
	})
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func mapMemoriesToBrief(memories []*models.Memory, limit int) []map[string]any {
	var result []map[string]any
	for i, m := range memories {
		if i >= limit {
			break
		}
		result = append(result, map[string]any{
			"id":      m.ID,
			"content": truncate(m.Content, 200),
			"tags":    m.Tags,
		})
	}
	return result
}
