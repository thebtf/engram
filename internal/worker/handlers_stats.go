package worker

import (
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/embedding"
)

// vnextStatsResponse is the JSON shape returned by GET /api/stats/vnext.
type vnextStatsResponse struct {
	InjectionCount int64                      `json:"injection_count"`
	CitationCount  int64                      `json:"citation_count"`
	UncitedCount   int64                      `json:"uncited_count"`
	NoiseRatio     float64                    `json:"noise_ratio"`
	WriteGateStats map[string]int64           `json:"write_gate_stats"`
	GeneratedAt    time.Time                  `json:"generated_at"`
	Embedding      *embedding.EmbeddingStats  `json:"embedding,omitempty"`
}

// handleStatsVnext godoc
// @Summary vNext pipeline metrics
// @Description Returns injection/citation counts, noise ratio, and write-gate memory stats.
// @Tags Analytics
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} vnextStatsResponse
// @Router /api/stats/vnext [get]
func (s *Service) handleStatsVnext(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	s.initMu.RLock()
	store := s.store
	s.initMu.RUnlock()
	if store == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	var injectionCount int64
	if err := store.DB.WithContext(ctx).Raw(`SELECT count(*) FROM injection_log`).Scan(&injectionCount).Error; err != nil {
		http.Error(w, "failed to query injection_log", http.StatusInternalServerError)
		return
	}

	var citationCount int64
	if err := store.DB.WithContext(ctx).Raw(`SELECT count(*) FROM citation_log WHERE cited = true`).Scan(&citationCount).Error; err != nil {
		http.Error(w, "failed to query citation_log (cited)", http.StatusInternalServerError)
		return
	}

	var uncitedCount int64
	if err := store.DB.WithContext(ctx).Raw(`SELECT count(*) FROM citation_log WHERE cited = false`).Scan(&uncitedCount).Error; err != nil {
		http.Error(w, "failed to query citation_log (uncited)", http.StatusInternalServerError)
		return
	}

	// noise_ratio = uncited / (cited + uncited); guard division-by-zero.
	var noiseRatio float64
	total := citationCount + uncitedCount
	if total > 0 {
		noiseRatio = float64(uncitedCount) / float64(total)
	}

	// Write-gate stats: count memories by status where not soft-deleted.
	type statusRow struct {
		Status string
		Count  int64
	}
	var rows []statusRow
	if err := store.DB.WithContext(ctx).
		Raw(`SELECT status, count(*) AS count FROM memories WHERE deleted_at IS NULL GROUP BY status`).
		Scan(&rows).Error; err != nil {
		http.Error(w, "failed to query memories write-gate stats", http.StatusInternalServerError)
		return
	}

	writeGate := make(map[string]int64)
	for _, row := range rows {
		writeGate[row.Status] = row.Count
	}

	resp := vnextStatsResponse{
		InjectionCount: injectionCount,
		CitationCount:  citationCount,
		UncitedCount:   uncitedCount,
		NoiseRatio:     noiseRatio,
		WriteGateStats: writeGate,
		GeneratedAt:    time.Now().UTC(),
	}

	// Populate embedding telemetry when the embedding store is available.
	// Errors are non-fatal: log and leave the field nil so the stats response
	// always succeeds even when the embedding subsystem is unhealthy.
	s.initMu.RLock()
	embStore := s.embeddingStore
	s.initMu.RUnlock()
	if embStore != nil {
		if embStats, err := embStore.Stats(ctx); err != nil {
			log.Debug().Err(err).Msg("stats/vnext: embedding stats unavailable")
		} else {
			resp.Embedding = &embStats
		}
	}

	writeJSON(w, resp)
}
