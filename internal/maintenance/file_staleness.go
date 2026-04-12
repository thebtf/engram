package maintenance

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// checkFileStaleness detects observations whose referenced files have been
// modified by newer observations in the same project. If >50% of an observation's
// file references appear in newer observations' files_modified, it is marked as
// stale_files in concepts and its importance is reduced by 0.7x.
//
// This is purely database-driven (no filesystem access, per Constitution #1).
func (s *Service) checkFileStaleness(ctx context.Context) (int, error) {
	db := s.store.GetDB().WithContext(ctx)
	log := s.log.With().Str("task", "file_staleness").Logger()

	cutoff := time.Now().Add(-90 * 24 * time.Hour).UnixMilli()

	// Query observations with file references from last 90 days
	type fileObs struct {
		ID            int64  `gorm:"column:id"`
		Project       string `gorm:"column:project"`
		FilesRead     string `gorm:"column:files_read"`
		FilesModified string `gorm:"column:files_modified"`
		Concepts      string `gorm:"column:concepts"`
		CreatedAt     int64  `gorm:"column:created_at_epoch"`
	}

	var observations []fileObs
	if err := db.Raw(`
		SELECT id, project, COALESCE(files_read::text, '[]') as files_read,
			COALESCE(files_modified::text, '[]') as files_modified,
			COALESCE(concepts::text, '[]') as concepts, created_at_epoch
		FROM observations
		WHERE created_at_epoch > ?
		AND status = 'active'
		AND (
			(files_read IS NOT NULL AND files_read::text != '[]' AND files_read::text != 'null')
			OR (files_modified IS NOT NULL AND files_modified::text != '[]' AND files_modified::text != 'null')
		)
		AND COALESCE(concepts::text, '[]') NOT LIKE '%stale_files%'
		ORDER BY created_at_epoch DESC
		LIMIT 100
	`, cutoff).Scan(&observations).Error; err != nil {
		return 0, err
	}

	if len(observations) == 0 {
		return 0, nil
	}

	// Build per-project set of recently modified files (from newer observations)
	projectModifiedFiles := buildProjectModifiedFiles(db, cutoff)

	staleCount := 0
	for _, obs := range observations {
		// Skip already marked
		var concepts []string
		if err := json.Unmarshal([]byte(obs.Concepts), &concepts); err != nil {
			concepts = []string{}
		}
		if containsConcept(concepts, "stale_files") {
			continue
		}

		// Collect all file references
		allFiles := collectFiles(obs.FilesRead, obs.FilesModified)
		if len(allFiles) == 0 {
			continue
		}

		// Check how many of this observation's files were modified by NEWER observations
		modifiedSet := projectModifiedFiles[obs.Project]
		if len(modifiedSet) == 0 {
			continue
		}

		staleFileCount := 0
		for _, f := range allFiles {
			if epoch, ok := modifiedSet[f]; ok && epoch > obs.CreatedAt {
				staleFileCount++
			}
		}

		// If >50% of referenced files were modified by newer observations → stale
		if float64(staleFileCount)/float64(len(allFiles)) > 0.5 {
			conceptJSON := `["stale_files"]`
			result := db.Exec(
				`UPDATE observations
				 SET concepts = COALESCE(concepts, '[]'::jsonb) || ?::jsonb,
				     importance_score = importance_score * ?
				 WHERE id = ?`,
				conceptJSON, 0.7, obs.ID,
			)
			if result.Error != nil {
				log.Warn().Err(result.Error).Int64("obs_id", obs.ID).Msg("Failed to mark stale")
				continue
			}
			staleCount++
		}
	}

	if staleCount > 0 {
		log.Info().Int("stale", staleCount).Msg("File staleness detection complete")
	}

	return staleCount, nil
}

// buildProjectModifiedFiles builds a map of project → (file → latest modified epoch)
// from all observations' files_modified columns.
func buildProjectModifiedFiles(db *gorm.DB, cutoff int64) map[string]map[string]int64 {
	type modRow struct {
		Project       string `gorm:"column:project"`
		FilesModified string `gorm:"column:files_modified"`
		CreatedAt     int64  `gorm:"column:created_at_epoch"`
	}

	var rows []modRow
	if err := db.Raw(`
		SELECT project, COALESCE(files_modified::text, '[]') as files_modified, created_at_epoch
		FROM observations
		WHERE created_at_epoch > ?
		AND files_modified IS NOT NULL AND files_modified::text != '[]' AND files_modified::text != 'null'
		AND status = 'active'
	`, cutoff).Scan(&rows).Error; err != nil {
		return nil
	}

	result := make(map[string]map[string]int64)
	for _, row := range rows {
		var files []string
		if err := json.Unmarshal([]byte(row.FilesModified), &files); err != nil {
			continue
		}
		if _, ok := result[row.Project]; !ok {
			result[row.Project] = make(map[string]int64)
		}
		for _, f := range files {
			if f == "" {
				continue
			}
			if existing, ok := result[row.Project][f]; !ok || row.CreatedAt > existing {
				result[row.Project][f] = row.CreatedAt
			}
		}
	}

	return result
}

// collectFiles parses files_read and files_modified JSON arrays into a deduplicated slice.
func collectFiles(filesReadJSON, filesModifiedJSON string) []string {
	seen := make(map[string]struct{})
	var result []string

	for _, raw := range []string{filesReadJSON, filesModifiedJSON} {
		var files []string
		if err := json.Unmarshal([]byte(raw), &files); err != nil {
			continue
		}
		for _, f := range files {
			if f == "" {
				continue
			}
			if _, ok := seen[f]; !ok {
				seen[f] = struct{}{}
				result = append(result, f)
			}
		}
	}

	return result
}

// containsConcept checks if a concept string exists in the concepts slice.
func containsConcept(concepts []string, target string) bool {
	for _, c := range concepts {
		if c == target {
			return true
		}
	}
	return false
}
