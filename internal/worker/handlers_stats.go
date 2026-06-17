package worker

import (
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/embedding"
)

// embeddingTelemetry is the JSON sub-object surfaced under the "embedding" key
// in the /api/stats/vnext response. It extends the DB-derived CoverageStats with
// process-lifetime backfill counters sourced from BackfillRecorder.
type embeddingTelemetry struct {
	embedding.CoverageStats
	EmbedSuccessCount int64                 `json:"embed_success_count"`
	EmbedFailureCount int64                 `json:"embed_failure_count"`
	LastEmbedError    *embedding.EmbedError `json:"last_embed_error,omitempty"`
}

// projectCitationRate is one per-project row in the citation_rate breakdown. Rate is
// sum(citation_count)/sum(injection_count) over a project's live memories — the rank-7
// "is recall actually improving for this project?" signal. Projects with no injections yet
// are omitted (rate would be undefined), so an empty list means "no usage recorded anywhere".
type projectCitationRate struct {
	Project         string  `json:"project"`
	CitationRate    float64 `json:"citation_rate"`
	TotalCitations  int64   `json:"total_citations"`
	TotalInjections int64   `json:"total_injections"`
	MemoryCount     int64   `json:"memory_count"`
}

// outcomeTelemetry surfaces session-outcome recording health (rank-7). The outcome-modulated
// feedback (ranks 5/6) only does useful work when a session's outcome is recorded, but the
// automatic outcome path is currently inert (it relies on the opt-in set_session_outcome tool).
// UnrecordedSessions / TotalSessions make that starvation visible as a number instead of an
// inference: a high unrecorded fraction means the outcome-weighting is mostly running neutral.
type outcomeTelemetry struct {
	TotalSessions      int64            `json:"total_sessions"`
	UnrecordedSessions int64            `json:"unrecorded_sessions"`
	UnrecordedFraction float64          `json:"unrecorded_fraction"`
	ByOutcome          map[string]int64 `json:"by_outcome"`
}

// vnextStatsResponse is the JSON shape returned by GET /api/stats/vnext.
type vnextStatsResponse struct {
	InjectionCount       int64                 `json:"injection_count"`
	CitationCount        int64                 `json:"citation_count"`
	UncitedCount         int64                 `json:"uncited_count"`
	NoiseRatio           float64               `json:"noise_ratio"`
	WriteGateStats       map[string]int64      `json:"write_gate_stats"`
	ProjectCitationRates []projectCitationRate `json:"project_citation_rates"`
	Outcomes             *outcomeTelemetry     `json:"outcomes,omitempty"`
	GeneratedAt          time.Time             `json:"generated_at"`
	Embedding            *embeddingTelemetry   `json:"embedding,omitempty"`
}

// handleStatsVnext godoc
// @Summary vNext pipeline metrics
// @Description Returns injection/citation counts, noise ratio, write-gate memory stats, and embedding telemetry.
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

	// Per-project citation rate (rank-7): one GROUP BY over the memories table reproduces
	// GetProjectCitationRate's math for every project at once. Only projects with at least one
	// injection are emitted (HAVING) — a 0-injection project has an undefined rate. Non-fatal:
	// on error, leave the slice empty so the core stats still return.
	projectRates := []projectCitationRate{}
	type rateRow struct {
		Project         string
		TotalCitations  int64
		TotalInjections int64
		MemoryCount     int64
	}
	var rateRows []rateRow
	if err := store.DB.WithContext(ctx).Raw(`
		SELECT project,
		       COALESCE(SUM(citation_count), 0) AS total_citations,
		       COALESCE(SUM(injection_count), 0) AS total_injections,
		       COUNT(*) AS memory_count
		FROM memories
		WHERE deleted_at IS NULL AND status != 'flagged'
		GROUP BY project
		HAVING COALESCE(SUM(injection_count), 0) > 0
		ORDER BY project`).Scan(&rateRows).Error; err != nil {
		log.Debug().Err(err).Msg("stats/vnext: project citation rates unavailable")
	} else {
		for _, rr := range rateRows {
			rate := float64(rr.TotalCitations) / float64(rr.TotalInjections)
			if rate > 1.0 {
				rate = 1.0
			}
			projectRates = append(projectRates, projectCitationRate{
				Project:         rr.Project,
				CitationRate:    rate,
				TotalCitations:  rr.TotalCitations,
				TotalInjections: rr.TotalInjections,
				MemoryCount:     rr.MemoryCount,
			})
		}
	}

	// Outcome recording health (rank-7): makes the outcome-starvation problem (ranks 5/6 run
	// neutral when no outcome is recorded) visible. Non-fatal: on error, leave Outcomes nil.
	var outcomes *outcomeTelemetry
	type outcomeRow struct {
		Outcome string
		Count   int64
	}
	var outcomeRows []outcomeRow
	if err := store.DB.WithContext(ctx).Raw(`
		SELECT COALESCE(NULLIF(outcome, ''), '(unrecorded)') AS outcome, COUNT(*) AS count
		FROM sdk_sessions
		GROUP BY COALESCE(NULLIF(outcome, ''), '(unrecorded)')`).Scan(&outcomeRows).Error; err != nil {
		log.Debug().Err(err).Msg("stats/vnext: outcome telemetry unavailable")
	} else {
		ot := &outcomeTelemetry{ByOutcome: make(map[string]int64)}
		for _, orow := range outcomeRows {
			ot.ByOutcome[orow.Outcome] = orow.Count
			ot.TotalSessions += orow.Count
			if orow.Outcome == "(unrecorded)" {
				ot.UnrecordedSessions += orow.Count
			}
		}
		if ot.TotalSessions > 0 {
			ot.UnrecordedFraction = float64(ot.UnrecordedSessions) / float64(ot.TotalSessions)
		}
		outcomes = ot
	}

	resp := vnextStatsResponse{
		InjectionCount:       injectionCount,
		CitationCount:        citationCount,
		UncitedCount:         uncitedCount,
		NoiseRatio:           noiseRatio,
		WriteGateStats:       writeGate,
		ProjectCitationRates: projectRates,
		Outcomes:             outcomes,
		GeneratedAt:          time.Now().UTC(),
	}

	// Populate embedding telemetry when the embedding store is available.
	// StatsWithCoverage is used instead of Stats so that embedding_coverage and
	// active_memory_count are included without a second DB query.
	// Process-lifetime backfill counters are read from the recorder (nil-safe).
	// Errors are non-fatal: log and leave the field nil so the stats response
	// always succeeds even when the embedding subsystem is unhealthy.
	s.initMu.RLock()
	embStore := s.embeddingStore
	embRec := s.embeddingRecorder
	s.initMu.RUnlock()
	if embStore != nil {
		if covStats, err := embStore.StatsWithCoverage(ctx); err != nil {
			log.Debug().Err(err).Msg("stats/vnext: embedding stats unavailable")
		} else {
			tel := &embeddingTelemetry{
				CoverageStats: covStats,
			}
			if embRec != nil {
				succ, fail, lastErr := embRec.Snapshot()
				tel.EmbedSuccessCount = succ
				tel.EmbedFailureCount = fail
				if !lastErr.At.IsZero() {
					tel.LastEmbedError = &lastErr
				}
			}
			resp.Embedding = tel
		}
	}

	writeJSON(w, resp)
}
