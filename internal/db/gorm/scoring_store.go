// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thebtf/engram/pkg/models"
)

// UpdateObservationFeedback updates the user feedback for an observation.
// Feedback values: -1 (thumbs down), 0 (neutral), 1 (thumbs up).
func (s *ObservationStore) UpdateObservationFeedback(ctx context.Context, id int64, feedback int) error {
	now := time.Now().UnixMilli()

	result := s.db.WithContext(ctx).
		Model(&Observation{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"user_feedback":          feedback,
			"score_updated_at_epoch": now,
		})

	return result.Error
}

// IncrementRetrievalCount increments the retrieval counter for the given observation IDs.
// This is called when observations are returned in search results.
func (s *ObservationStore) IncrementRetrievalCount(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	now := time.Now().UnixMilli()

	// Use raw SQL for increment expression
	result := s.db.WithContext(ctx).
		Exec("UPDATE observations SET retrieval_count = COALESCE(retrieval_count, 0) + 1, last_retrieved_at_epoch = ? WHERE id IN ?",
			now, ids)

	return result.Error
}

// IncrementInjectionCounts increments the injection counter for the given observation IDs.
// Called when observations are injected into Claude Code context.
func (s *ObservationStore) IncrementInjectionCounts(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	result := s.db.WithContext(ctx).
		Exec("UPDATE observations SET injection_count = COALESCE(injection_count, 0) + 1 WHERE id IN ?", ids)

	return result.Error
}

// GetUtilityScore returns the current utility_score for the given observation.
func (s *ObservationStore) GetUtilityScore(ctx context.Context, id int64) (float64, error) {
	var obs Observation
	if err := s.db.WithContext(ctx).Select("utility_score").First(&obs, id).Error; err != nil {
		return 0, err
	}
	return obs.UtilityScore, nil
}

// UpdateUtilityScore updates the utility score for a single observation using EMA.
// signal: 1.0 = used, 0.0 = corrected/ignored
// alpha: EMA smoothing factor (default 0.1 for slow adaptation)
// maxDelta: maximum score change per session (default 0.05 for confidence cap)
func (s *ObservationStore) UpdateUtilityScore(ctx context.Context, id int64, signal, alpha, maxDelta float64) error {
	// Fetch current score
	var obs Observation
	if err := s.db.WithContext(ctx).Select("utility_score").First(&obs, id).Error; err != nil {
		return err
	}

	// EMA: new = alpha * signal + (1-alpha) * old
	newScore := alpha*signal + (1-alpha)*obs.UtilityScore

	// Apply confidence cap: limit change per session
	delta := newScore - obs.UtilityScore
	if delta > maxDelta {
		newScore = obs.UtilityScore + maxDelta
	} else if delta < -maxDelta {
		newScore = obs.UtilityScore - maxDelta
	}

	// Clamp to [0, 1]
	if newScore < 0 {
		newScore = 0
	} else if newScore > 1 {
		newScore = 1
	}

	return s.db.WithContext(ctx).
		Model(&Observation{}).
		Where("id = ?", id).
		Update("utility_score", newScore).Error
}

// UpdateImportanceScore updates the importance score for a single observation.
func (s *ObservationStore) UpdateImportanceScore(ctx context.Context, id int64, score float64) error {
	now := time.Now().UnixMilli()

	result := s.db.WithContext(ctx).
		Model(&Observation{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"importance_score":       score,
			"score_updated_at_epoch": now,
		})

	return result.Error
}

// UpdateImportanceScores bulk updates importance scores for multiple observations.
// Uses a single SQL statement with CASE/WHEN for efficient batch updates.
func (s *ObservationStore) UpdateImportanceScores(ctx context.Context, scores map[int64]float64) error {
	if len(scores) == 0 {
		return nil
	}

	// For small batches, use simple individual updates
	if len(scores) <= 5 {
		return s.updateScoresIndividually(ctx, scores)
	}

	// For larger batches, use CASE/WHEN SQL for single-query update
	return s.updateScoresBatch(ctx, scores)
}

// updateScoresIndividually updates scores one at a time (efficient for small batches).
func (s *ObservationStore) updateScoresIndividually(ctx context.Context, scores map[int64]float64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UnixMilli()

		for id, score := range scores {
			err := tx.Model(&Observation{}).
				Where("id = ?", id).
				Updates(map[string]interface{}{
					"importance_score":       score,
					"score_updated_at_epoch": now,
				}).Error

			if err != nil {
				return err
			}
		}

		return nil
	})
}

// updateScoresBatch updates multiple scores in a single SQL statement using CASE/WHEN.
// This is much more efficient for large batches (O(1) queries instead of O(n)).
func (s *ObservationStore) updateScoresBatch(ctx context.Context, scores map[int64]float64) error {
	now := time.Now().UnixMilli()

	// Build CASE/WHEN clause for importance_score
	// UPDATE observations SET
	//   importance_score = CASE id WHEN 1 THEN 0.5 WHEN 2 THEN 0.8 ... END,
	//   score_updated_at_epoch = ?
	// WHERE id IN (1, 2, ...)

	ids := make([]int64, 0, len(scores))
	caseBuilder := strings.Builder{}
	caseBuilder.WriteString("CASE id ")

	for id, score := range scores {
		ids = append(ids, id)
		caseBuilder.WriteString(fmt.Sprintf("WHEN %d THEN %f ", id, score))
	}
	caseBuilder.WriteString("END")

	// Use raw SQL for the batch update
	sql := fmt.Sprintf(
		"UPDATE observations SET importance_score = %s, score_updated_at_epoch = ? WHERE id IN ?",
		caseBuilder.String(),
	)

	return s.db.WithContext(ctx).Exec(sql, now, ids).Error
}

// GetObservationsNeedingScoreUpdate returns observations that need their importance score recalculated.
// Returns observations where score_updated_at_epoch is NULL or older than the threshold.
func (s *ObservationStore) GetObservationsNeedingScoreUpdate(ctx context.Context, threshold time.Duration, limit int) ([]*models.Observation, error) {
	cutoff := time.Now().Add(-threshold).UnixMilli()

	var observations []Observation

	err := s.db.WithContext(ctx).
		Where("score_updated_at_epoch IS NULL OR score_updated_at_epoch < ?", cutoff).
		Order("created_at_epoch DESC").
		Limit(limit).
		Find(&observations).Error

	if err != nil {
		return nil, err
	}

	return toModelObservations(observations), nil
}

// GetConceptWeights returns all concept weights from the database.
func (s *ObservationStore) GetConceptWeights(ctx context.Context) (map[string]float64, error) {
	var weights []struct {
		Concept string
		Weight  float64
	}

	err := s.db.WithContext(ctx).
		Table("concept_weights").
		Select("concept, weight").
		Scan(&weights).Error

	if err != nil {
		return models.DefaultConceptWeights, nil
	}

	if len(weights) == 0 {
		return models.DefaultConceptWeights, nil
	}

	result := make(map[string]float64, len(weights))
	for _, w := range weights {
		result[w.Concept] = w.Weight
	}

	return result, nil
}

// SetConceptWeights stores concept weights in the database using UPSERT.
func (s *ObservationStore) SetConceptWeights(ctx context.Context, weights map[string]float64) error {
	if len(weights) == 0 {
		return nil
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for concept, weight := range weights {
			// UPSERT using raw SQL since GORM's ON CONFLICT is complex for this case
			err := tx.Exec(`
				INSERT INTO concept_weights (concept, weight, updated_at)
				VALUES (?, ?, datetime('now'))
				ON CONFLICT(concept) DO UPDATE SET weight = excluded.weight, updated_at = excluded.updated_at
			`, concept, weight).Error

			if err != nil {
				return err
			}
		}

		return nil
	})
}

// UpdateConceptWeight updates a single concept weight in the database using UPSERT.
func (s *ObservationStore) UpdateConceptWeight(ctx context.Context, concept string, weight float64) error {
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO concept_weights (concept, weight, updated_at)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(concept) DO UPDATE SET weight = excluded.weight, updated_at = excluded.updated_at
	`, concept, weight).Error
}

// FeedbackStats contains statistics about observation feedback and scoring.
type FeedbackStats struct {
	Total        int     `json:"total"`
	Positive     int     `json:"positive"`
	Negative     int     `json:"negative"`
	Neutral      int     `json:"neutral"`
	AvgScore     float64 `json:"avg_score"`
	AvgRetrieval float64 `json:"avg_retrieval"`
}

// GetObservationFeedbackStats returns statistics about user feedback.
func (s *ObservationStore) GetObservationFeedbackStats(ctx context.Context, project string) (*FeedbackStats, error) {
	var stats FeedbackStats

	query := s.db.WithContext(ctx).
		Model(&Observation{}).
		Select(`
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN user_feedback = 1 THEN 1 ELSE 0 END), 0) as positive,
			COALESCE(SUM(CASE WHEN user_feedback = -1 THEN 1 ELSE 0 END), 0) as negative,
			COALESCE(SUM(CASE WHEN user_feedback = 0 THEN 1 ELSE 0 END), 0) as neutral,
			COALESCE(AVG(COALESCE(importance_score, 1.0)), 0) as avg_score,
			COALESCE(AVG(COALESCE(retrieval_count, 0)), 0) as avg_retrieval
		`)

	if project != "" {
		query = query.Where("project = ? OR scope = 'global'", project)
	}

	err := query.Scan(&stats).Error
	if err != nil {
		return nil, err
	}

	return &stats, nil
}

// GetTopScoringObservations returns the highest-scoring observations.
func (s *ObservationStore) GetTopScoringObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error) {
	var observations []Observation

	query := s.db.WithContext(ctx).
		Order("COALESCE(importance_score, 1.0) DESC, created_at_epoch DESC").
		Limit(limit)

	if project != "" {
		query = query.Where("project = ? OR scope = 'global'", project)
	}

	err := query.Find(&observations).Error
	if err != nil {
		return nil, err
	}

	return toModelObservations(observations), nil
}

// GetMostRetrievedObservations returns the most frequently retrieved observations.
func (s *ObservationStore) GetMostRetrievedObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error) {
	var observations []Observation

	query := s.db.WithContext(ctx).
		Where("retrieval_count > 0").
		Order("retrieval_count DESC, created_at_epoch DESC").
		Limit(limit)

	if project != "" {
		query = query.Where("project = ? OR scope = 'global'", project)
	}

	err := query.Find(&observations).Error
	if err != nil {
		return nil, err
	}

	return toModelObservations(observations), nil
}

// GetRecentlyInjectedObservations returns observations that have been injected at least once.
// Used by the stop hook to detect utility signals in the transcript.
func (s *ObservationStore) GetRecentlyInjectedObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error) {
	var observations []Observation

	query := s.db.WithContext(ctx).
		Where("injection_count > 0").
		Order("injection_count DESC, created_at_epoch DESC").
		Limit(limit)

	if project != "" {
		query = query.Where("project = ? OR scope = 'global'", project)
	}

	err := query.Find(&observations).Error
	if err != nil {
		return nil, err
	}

	return toModelObservations(observations), nil
}

// ResetObservationScores resets all observation scores to their default values.
// This is useful for testing or when changing the scoring algorithm.
func (s *ObservationStore) ResetObservationScores(ctx context.Context) error {
	// Use Where("1 = 1") to explicitly allow bulk update of all rows
	result := s.db.WithContext(ctx).
		Model(&Observation{}).
		Where("1 = 1").
		Updates(map[string]interface{}{
			"importance_score":       1.0,
			"score_updated_at_epoch": nil,
		})

	return result.Error
}

// GetOldestObservations retrieves the oldest non-archived observations for a project.
// Used by consolidation for stratified sampling to find cross-temporal associations.
func (s *ObservationStore) GetOldestObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error) {
	var dbObservations []Observation
	query := s.db.WithContext(ctx).
		Where("archived = ? OR archived IS NULL", false).
		Order("created_at_epoch ASC").
		Limit(limit)

	if project != "" {
		query = query.Where("project = ? OR scope = 'global'", project)
	}

	err := query.Find(&dbObservations).Error
	if err != nil {
		return nil, err
	}

	return toModelObservations(dbObservations), nil
}

// sessionObservationInjection maps to the session_observation_injections table.
type sessionObservationInjection struct {
	ID            int64     `gorm:"primaryKey"`
	SessionID     int64     `gorm:"column:session_id;not null"`
	ObservationID int64     `gorm:"column:observation_id;not null"`
	InjectedAt    time.Time `gorm:"column:injected_at;autoCreateTime"`
}

func (sessionObservationInjection) TableName() string {
	return "session_observation_injections"
}

// RecordSessionInjections records which observations were injected into a specific session.
// Uses ON CONFLICT DO NOTHING for idempotency (safe to call multiple times per session).
func (s *ObservationStore) RecordSessionInjections(ctx context.Context, sessionID int64, observationIDs []int64) error {
	if len(observationIDs) == 0 {
		return nil
	}

	rows := make([]sessionObservationInjection, len(observationIDs))
	for i, oid := range observationIDs {
		rows[i] = sessionObservationInjection{
			SessionID:     sessionID,
			ObservationID: oid,
		}
	}

	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows).Error
}

// GetSessionInjectedObservations returns observation IDs that were injected into a specific session.
func (s *ObservationStore) GetSessionInjectedObservations(ctx context.Context, sessionID int64) ([]int64, error) {
	var ids []int64
	err := s.db.WithContext(ctx).
		Raw("SELECT observation_id FROM session_observation_injections WHERE session_id = ?", sessionID).
		Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// IncrementImportanceScores atomically increments importance scores for multiple observations.
// Each observation's score is increased by its delta, capped at the given maximum.
// Uses atomic SQL to avoid read-then-write race with concurrent decay cycles.
func (s *ObservationStore) IncrementImportanceScores(ctx context.Context, deltas map[int64]float64, cap float64) error {
	if len(deltas) == 0 {
		return nil
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UnixMilli()
		for id, delta := range deltas {
			// Atomic: UPDATE SET importance_score = LEAST(importance_score + delta, cap)
			err := tx.Exec(
				"UPDATE observations SET importance_score = LEAST(COALESCE(importance_score, 1.0) + ?, ?), score_updated_at_epoch = ? WHERE id = ?",
				delta, cap, now, id,
			).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// EffectivenessDistribution holds aggregated counts of observations grouped by effectiveness tier.
type EffectivenessDistribution struct {
	High        int64 `json:"high"`
	Medium      int64 `json:"medium"`
	Low         int64 `json:"low"`
	Insufficient int64 `json:"insufficient"`
	Total       int64 `json:"total"`
}

// GetEffectivenessDistribution returns aggregated effectiveness tier counts using a single SQL
// aggregation query. Excludes archived and suppressed observations.
func (s *ObservationStore) GetEffectivenessDistribution(ctx context.Context) (EffectivenessDistribution, error) {
	var result EffectivenessDistribution

	err := s.db.WithContext(ctx).Raw(`
		SELECT
			COUNT(*) FILTER (WHERE effectiveness_injections >= 10 AND effectiveness_score >= 0.7) AS high,
			COUNT(*) FILTER (WHERE effectiveness_injections >= 10 AND effectiveness_score >= 0.4 AND effectiveness_score < 0.7) AS medium,
			COUNT(*) FILTER (WHERE effectiveness_injections >= 10 AND effectiveness_score < 0.4) AS low,
			COUNT(*) FILTER (WHERE effectiveness_injections > 0 AND effectiveness_injections < 10) AS insufficient,
			COUNT(*) AS total
		FROM observations
		WHERE COALESCE(is_archived, 0) = 0 AND COALESCE(is_suppressed, FALSE) = FALSE
		AND COALESCE(effectiveness_injections, 0) > 0
	`).Scan(&result).Error
	if err != nil {
		return EffectivenessDistribution{}, fmt.Errorf("failed to query effectiveness distribution: %w", err)
	}

	return result, nil
}

// UpdateEffectivenessStats updates the effectiveness counters and recomputes effectiveness_score
// for a single observation. Called after outcome propagation for each injected observation.
func (s *ObservationStore) UpdateEffectivenessStats(ctx context.Context, id int64, addInjections, addSuccesses int, newUtilityScore float64) error {
	return s.db.WithContext(ctx).Exec(`
		UPDATE observations
		SET
			effectiveness_injections = COALESCE(effectiveness_injections, 0) + ?,
			effectiveness_successes  = COALESCE(effectiveness_successes, 0) + ?,
			effectiveness_score      = CASE
				WHEN (COALESCE(effectiveness_injections, 0) + ?) > 0
				THEN CAST(COALESCE(effectiveness_successes, 0) + ? AS REAL) / CAST(COALESCE(effectiveness_injections, 0) + ? AS REAL)
				ELSE 0
			END,
			utility_score = ?
		WHERE id = ?
	`, addInjections, addSuccesses, addInjections, addSuccesses, addInjections, newUtilityScore, id).Error
}
