package worker

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
)

type segmentCheckRequest struct {
	SessionID  string `json:"session_id"`
	Project    string `json:"project"`
	PromptText string `json:"prompt_text"`
}

const topicShiftThreshold = 0.5

// handleSegmentCheck detects topic shifts in a session by comparing embedding
// similarity of the current prompt against the segment's running topic.
// Creates a new segment when cosine similarity drops below threshold.
func (s *Service) handleSegmentCheck(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENGRAM_ADAPTIVE_ENABLED") != "true" {
		writeJSON(w, map[string]string{"status": "disabled"})
		return
	}

	var req segmentCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.Project == "" || req.PromptText == "" {
		http.Error(w, "session_id, project, and prompt_text required", http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	embClient := s.embeddingClient
	segStore := s.segmentStore
	s.initMu.RUnlock()

	if embClient == nil || segStore == nil {
		writeJSON(w, map[string]any{
			"status":      "skipped",
			"reason":      "embedding or segment store not available",
			"topic_shift": false,
		})
		return
	}

	// Async — don't block the hook.
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"status": "accepted"})

	go s.processSegmentCheckAsync(req, embClient, segStore)
}

func (s *Service) processSegmentCheckAsync(req segmentCheckRequest, embClient *embedding.Client, segStore *gormdb.SegmentStore) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Compute embedding for the current prompt.
	vectors, err := embClient.Embed(ctx, []string{req.PromptText})
	if err != nil {
		log.Debug().Err(err).Str("session_id", req.SessionID).Msg("segment-check: embed failed, skipping")
		return
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return
	}
	currentVec := vectors[0]

	// Get current segment.
	currentSeg, err := segStore.GetCurrentSegment(ctx, req.SessionID)
	if err != nil {
		log.Error().Err(err).Str("session_id", req.SessionID).Msg("segment-check: get current segment failed")
		return
	}

	if currentSeg == nil {
		// First prompt in session — create initial segment.
		if _, err := segStore.CreateSegment(ctx, req.SessionID, req.Project, truncateForTopic(req.PromptText)); err != nil {
			log.Error().Err(err).Msg("segment-check: create initial segment failed")
		}
		// Store this embedding as the segment's reference.
		s.storeSegmentEmbedding(req.SessionID, 0, currentVec)
		return
	}

	// Compare with previous segment's embedding.
	prevVec := s.getSegmentEmbedding(req.SessionID, currentSeg.SegmentIndex)
	if prevVec == nil {
		// No previous embedding stored — update with current and continue.
		s.storeSegmentEmbedding(req.SessionID, currentSeg.SegmentIndex, currentVec)
		return
	}

	similarity := cosineSimilarity(currentVec, prevVec)

	if similarity < topicShiftThreshold {
		// Topic shift detected — create new segment.
		if _, err := segStore.CreateSegment(ctx, req.SessionID, req.Project, truncateForTopic(req.PromptText)); err != nil {
			log.Error().Err(err).Msg("segment-check: create new segment failed")
			return
		}
		s.storeSegmentEmbedding(req.SessionID, currentSeg.SegmentIndex+1, currentVec)
		log.Info().
			Str("session_id", req.SessionID).
			Float64("similarity", float64(similarity)).
			Int("new_segment", currentSeg.SegmentIndex+1).
			Msg("segment-check: topic shift detected")
	} else {
		// Same topic — update running centroid (exponential moving average).
		alpha := float32(0.3)
		updated := make([]float32, len(currentVec))
		for i := range currentVec {
			updated[i] = alpha*currentVec[i] + (1-alpha)*prevVec[i]
		}
		s.storeSegmentEmbedding(req.SessionID, currentSeg.SegmentIndex, updated)
	}
}

func truncateForTopic(s string) string {
	runes := []rune(s)
	if len(runes) > 100 {
		return string(runes[:100])
	}
	return s
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}
