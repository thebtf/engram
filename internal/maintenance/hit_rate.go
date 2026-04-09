package maintenance

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

// analyzeHitRate examines injection_log to identify noise (frequently injected,
// never cited) and star (frequently injected, often cited) observations.
// Recalculates flags each cycle — previous noise_candidate/high_value flags are
// cleared before recomputation so observations that become useful lose the penalty.
func (s *Service) analyzeHitRate(ctx context.Context) (int, error) {
	db := s.store.GetDB().WithContext(ctx)
	log := s.log.With().Str("task", "hit_rate").Logger()

	// Guard: need statistical significance
	var totalEntries int64
	if err := db.Raw(`SELECT COUNT(*) FROM injection_log`).Scan(&totalEntries).Error; err != nil {
		return 0, err
	}
	if totalEntries < 50 {
		log.Debug().Int64("entries", totalEntries).Msg("Skipping hit rate analysis: insufficient data")
		return 0, nil
	}

	// Step 1: Clear previous flags from all observations that have them.
	// Remove 'noise_candidate' and 'high_value' from concepts JSONB arrays,
	// then reset their importance multiplier (undo 0.5x noise / 1.2x star).
	if err := clearHitRateFlag(db, "noise_candidate", 1.0/0.5); err != nil {
		log.Warn().Err(err).Msg("Failed to clear noise_candidate flags")
	}
	if err := clearHitRateFlag(db, "high_value", 1.0/1.2); err != nil {
		log.Warn().Err(err).Msg("Failed to clear high_value flags")
	}

	// Step 2: Find noise candidates (10+ injections, 0 citations)
	type hitRateRow struct {
		ObservationID int64 `gorm:"column:observation_id"`
		Injections    int64 `gorm:"column:injections"`
		Citations     int64 `gorm:"column:citations"`
	}

	var noiseRows []hitRateRow
	if err := db.Raw(`
		SELECT observation_id, COUNT(*) as injections,
			SUM(CASE WHEN cited THEN 1 ELSE 0 END) as citations
		FROM injection_log
		GROUP BY observation_id
		HAVING COUNT(*) >= 10 AND SUM(CASE WHEN cited THEN 1 ELSE 0 END) = 0
	`).Scan(&noiseRows).Error; err != nil {
		return 0, err
	}

	noiseCount := 0
	for _, row := range noiseRows {
		if err := appendConceptAndScale(db, row.ObservationID, "noise_candidate", 0.5); err != nil {
			log.Warn().Err(err).Int64("obs_id", row.ObservationID).Msg("Failed to flag noise candidate")
			continue
		}
		noiseCount++
	}

	// Step 3: Find star candidates (5+ injections, >50% citation rate)
	var starRows []hitRateRow
	if err := db.Raw(`
		SELECT observation_id, COUNT(*) as injections,
			SUM(CASE WHEN cited THEN 1 ELSE 0 END) as citations
		FROM injection_log
		GROUP BY observation_id
		HAVING COUNT(*) >= 5 AND SUM(CASE WHEN cited THEN 1 ELSE 0 END)::float / COUNT(*) > 0.5
	`).Scan(&starRows).Error; err != nil {
		return 0, err
	}

	starCount := 0
	for _, row := range starRows {
		if err := appendConceptAndScale(db, row.ObservationID, "high_value", 1.2); err != nil {
			log.Warn().Err(err).Int64("obs_id", row.ObservationID).Msg("Failed to flag star")
			continue
		}
		starCount++
	}

	total := noiseCount + starCount
	if total > 0 {
		log.Info().
			Int("noise", noiseCount).
			Int("stars", starCount).
			Msg("Hit rate analysis complete")
	}

	return total, nil
}

// clearHitRateFlag removes a specific concept tag from all observations that have it,
// and reverses the importance multiplier that was applied when the flag was set.
func clearHitRateFlag(db *gorm.DB, flag string, reverseMultiplier float64) error {
	// Find observations with this flag in concepts
	var obsIDs []int64
	if err := db.Raw(`
		SELECT id FROM observations
		WHERE concepts::text LIKE ?
		AND status = 'active'
	`, "%"+flag+"%").Scan(&obsIDs).Error; err != nil {
		return err
	}

	if len(obsIDs) == 0 {
		return nil
	}

	// Remove the flag from concepts and reverse the multiplier
	for _, id := range obsIDs {
		// Read current concepts
		var conceptsJSON string
		if err := db.Raw(`SELECT COALESCE(concepts::text, '[]') FROM observations WHERE id = ?`, id).Scan(&conceptsJSON).Error; err != nil {
			continue
		}

		var concepts []string
		if err := json.Unmarshal([]byte(conceptsJSON), &concepts); err != nil {
			continue
		}

		// Filter out the flag
		filtered := make([]string, 0, len(concepts))
		for _, c := range concepts {
			if c != flag {
				filtered = append(filtered, c)
			}
		}

		filteredJSON, err := json.Marshal(filtered)
		if err != nil {
			continue
		}

		// Update concepts and reverse importance multiplier
		db.Exec(`UPDATE observations SET concepts = ?, importance_score = importance_score * ? WHERE id = ?`,
			string(filteredJSON), reverseMultiplier, id)
	}

	return nil
}

// appendConceptAndScale adds a concept tag to an observation's concepts array
// and multiplies its importance_score by the given multiplier.
func appendConceptAndScale(db *gorm.DB, obsID int64, concept string, multiplier float64) error {
	// Read current concepts
	var conceptsJSON string
	if err := db.Raw(`SELECT COALESCE(concepts::text, '[]') FROM observations WHERE id = ?`, obsID).Scan(&conceptsJSON).Error; err != nil {
		return err
	}

	var concepts []string
	if err := json.Unmarshal([]byte(conceptsJSON), &concepts); err != nil {
		concepts = []string{}
	}

	// Check if already present
	for _, c := range concepts {
		if c == concept {
			return nil // Already flagged
		}
	}

	concepts = append(concepts, concept)
	updated, err := json.Marshal(concepts)
	if err != nil {
		return err
	}

	return db.Exec(`UPDATE observations SET concepts = ?, importance_score = importance_score * ? WHERE id = ?`,
		string(updated), multiplier, obsID).Error
}
