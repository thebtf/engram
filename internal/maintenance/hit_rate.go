package maintenance

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

const (
	minHitRateSampleSize       int64   = 50
	minNoiseCandidateHits      int64   = 10
	minHighValueCandidateHits  int64   = 5
	noiseCandidateMultiplier   float64 = 0.5
	highValueCandidateBoost    float64 = 1.2
	noiseCandidateConcept              = "noise_candidate"
	highValueConcept                   = "high_value"
)

type hitRateCandidateRow struct {
	ObservationID int64 `gorm:"column:observation_id"`
	Injections    int64 `gorm:"column:injections"`
	Citations     int64 `gorm:"column:citations"`
}

func (s *Service) analyzeHitRate(ctx context.Context) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("service is required")
	}
	if s.store == nil {
		return 0, fmt.Errorf("store is required")
	}
	if ctx == nil {
		return 0, fmt.Errorf("context is required")
	}

	baseDB := s.store.GetDB()
	totalLogs, err := countInjectionLogEntries(ctx, baseDB)
	if err != nil {
		return 0, fmt.Errorf("count injection_log entries: %w", err)
	}
	if totalLogs < minHitRateSampleSize {
		s.log.Debug().Int64("injection_logs", totalLogs).Msg("Skipping hit rate analysis due to insufficient injection samples")
		return 0, nil
	}

	// Wrap clear + query + apply in a transaction for atomicity.
	// On error, partial state is rolled back; on success, all changes commit together.
	db := baseDB.WithContext(ctx)
	tx := db.Begin()
	if tx.Error != nil {
		return 0, fmt.Errorf("begin transaction: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	clearedRows, err := clearHitRateFlags(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("clear hit rate flags: %w", err)
	}

	noiseCandidates, err := queryNoiseCandidates(ctx, tx)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("query noise candidates: %w", err)
	}
	noiseUpdates, err := applyHitRateCandidates(ctx, tx, noiseCandidates, noiseCandidateConcept, noiseCandidateMultiplier)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("apply noise candidate updates: %w", err)
	}

	highValueCandidates, err := queryHighValueCandidates(ctx, tx)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("query high-value candidates: %w", err)
	}
	highValueUpdates, err := applyHitRateCandidates(ctx, tx, highValueCandidates, highValueConcept, highValueCandidateBoost)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("apply high-value candidate updates: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return 0, fmt.Errorf("commit hit rate transaction: %w", err)
	}

	totalModified := noiseUpdates + highValueUpdates
	s.log.Info().
		Int64("injection_logs", totalLogs).
		Int64("cleared_flags", clearedRows).
		Int("noise_candidates", len(noiseCandidates)).
		Int("high_value_candidates", len(highValueCandidates)).
		Int("modified_observations", totalModified).
		Msg("Hit rate analysis completed")

	return totalModified, nil
}

func countInjectionLogEntries(ctx context.Context, db *gorm.DB) (int64, error) {
	var totalLogs int64
	if err := db.WithContext(ctx).Table("injection_log").Count(&totalLogs).Error; err != nil {
		return 0, err
	}
	return totalLogs, nil
}

func clearHitRateFlags(ctx context.Context, db *gorm.DB) (int64, error) {
	result := db.WithContext(ctx).Exec(
		`UPDATE observations
		 SET concepts = COALESCE(
			 (
				SELECT jsonb_agg(concept)
				FROM jsonb_array_elements_text(COALESCE(concepts, '[]'::jsonb)) AS concept
				WHERE concept <> ? AND concept <> ?
			 ),
			 '[]'::jsonb
		 )
		 WHERE COALESCE(concepts, '[]'::jsonb) @> ?::jsonb
		    OR COALESCE(concepts, '[]'::jsonb) @> ?::jsonb`,
		noiseCandidateConcept,
		highValueConcept,
		`["noise_candidate"]`,
		`["high_value"]`,
	)
	return result.RowsAffected, result.Error
}

func queryNoiseCandidates(ctx context.Context, db *gorm.DB) ([]hitRateCandidateRow, error) {
	var candidates []hitRateCandidateRow
	err := db.WithContext(ctx).Raw(
		`SELECT observation_id, COUNT(*) AS injections, SUM(CASE WHEN cited THEN 1 ELSE 0 END) AS citations
		 FROM injection_log
		 GROUP BY observation_id
		 HAVING COUNT(*) >= ? AND SUM(CASE WHEN cited THEN 1 ELSE 0 END) = 0`,
		minNoiseCandidateHits,
	).Scan(&candidates).Error
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func queryHighValueCandidates(ctx context.Context, db *gorm.DB) ([]hitRateCandidateRow, error) {
	var candidates []hitRateCandidateRow
	err := db.WithContext(ctx).Raw(
		`SELECT observation_id, COUNT(*) AS injections, SUM(CASE WHEN cited THEN 1 ELSE 0 END) AS citations
		 FROM injection_log
		 GROUP BY observation_id
		 HAVING COUNT(*) >= ?
		    AND SUM(CASE WHEN cited THEN 1 ELSE 0 END)::float / COUNT(*) > ?`,
		minHighValueCandidateHits,
		0.5,
	).Scan(&candidates).Error
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func applyHitRateCandidates(ctx context.Context, db *gorm.DB, candidates []hitRateCandidateRow, concept string, multiplier float64) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}

	conceptJSON := fmt.Sprintf("[\"%s\"]", concept)
	modifiedCount := 0
	for _, candidate := range candidates {
		result := db.WithContext(ctx).Exec(
			`UPDATE observations
			 SET concepts = COALESCE(concepts, '[]'::jsonb) || ?::jsonb,
			     importance_score = importance_score * ?
			 WHERE id = ?`,
			conceptJSON,
			multiplier,
			candidate.ObservationID,
		)
		if result.Error != nil {
			return 0, result.Error
		}
		modifiedCount += int(result.RowsAffected)
	}

	return modifiedCount, nil
}
