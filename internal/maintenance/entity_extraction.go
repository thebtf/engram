package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/thebtf/engram/internal/synthesis"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
)

// extractEntities finds recent observations not yet processed for entity extraction,
// runs LLM entity extraction in batches, and stores resulting entity observations.
// Returns the total number of entities created + updated.
func (s *Service) extractEntities(ctx context.Context) (int, error) {
	if s.llmClient == nil {
		return 0, fmt.Errorf("LLM client not available")
	}

	db := s.store.GetDB()
	limit := s.config.EntityExtractionLimit
	if limit <= 0 {
		limit = 20
	}

	// Find recent observations (last 24h) not yet processed for entity extraction.
	// We use the subtitle field as a processing flag: observations with subtitle
	// containing "entity_extracted" are skipped.
	// Only process substantive types (not entity, wiki, credential, guidance).
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	var observations []struct {
		ID             int64
		Project        string
		Type           string
		Title          string
		Narrative      string
		Concepts       string
		CreatedAtEpoch int64
	}

	err := db.WithContext(ctx).
		Table("observations").
		Select("id, project, type, title, COALESCE(narrative, '') as narrative, COALESCE(concepts, '[]') as concepts, created_at_epoch").
		Where("created_at_epoch >= ?", cutoff).
		Where("type NOT IN ('entity', 'wiki', 'credential')").
		Where("is_superseded = 0 AND is_archived = 0").
		Where("NOT (concepts @> '\"entity_extracted\"'::jsonb)").
		Order("created_at_epoch DESC").
		Limit(limit).
		Find(&observations).Error
	if err != nil {
		return 0, fmt.Errorf("query recent observations: %w", err)
	}

	if len(observations) == 0 {
		return 0, nil
	}

	// Group by project for batch processing
	byProject := make(map[string][]struct {
		ID             int64
		Project        string
		Type           string
		Title          string
		Narrative      string
		Concepts       string
		CreatedAtEpoch int64
	})
	for _, obs := range observations {
		byProject[obs.Project] = append(byProject[obs.Project], obs)
	}

	extractor := &synthesis.EntityExtractor{}
	totalEntities := 0

	for project, projectObs := range byProject {
		// Process in batches of 5
		for i := 0; i < len(projectObs); i += 5 {
			end := i + 5
			if end > len(projectObs) {
				end = len(projectObs)
			}
			batch := projectObs[i:end]

			// Convert to models.Observation for the extractor
			var modelObs []*models.Observation
			var sourceIDs []int64
			for _, o := range batch {
				modelObs = append(modelObs, &models.Observation{
					ID:             o.ID,
					Type:           models.ObservationType(o.Type),
					Title:          sql.NullString{String: o.Title, Valid: o.Title != ""},
					Narrative:      sql.NullString{String: o.Narrative, Valid: o.Narrative != ""},
					CreatedAtEpoch: o.CreatedAtEpoch,
				})
				sourceIDs = append(sourceIDs, o.ID)
			}

			result, err := extractor.Extract(ctx, s.llmClient, modelObs)
			if err != nil {
				s.log.Warn().Err(err).Str("project", project).Msg("Entity extraction failed for batch")
				continue
			}

			if result == nil || len(result.Entities) == 0 {
				// Mark observations as processed even if no entities found
				s.markEntityExtracted(ctx, sourceIDs)
				continue
			}

			counts, err := synthesis.StoreEntities(ctx, synthesis.StoreEntitiesParams{
				DB:         db,
				GraphStore: s.graphStore,
				Project:    project,
				Result:     result,
				SourceIDs:  sourceIDs,
			})
			if err != nil {
				s.log.Warn().Err(err).Str("project", project).Msg("Entity storage failed")
				continue
			}

			totalEntities += counts.Created + counts.Updated

			// Mark source observations as processed
			s.markEntityExtracted(ctx, sourceIDs)
		}
	}

	return totalEntities, nil
}

// markEntityExtracted marks observations as processed by appending "entity_extracted"
// to their concepts array. Uses JSONB append to avoid overwriting existing data.
func (s *Service) markEntityExtracted(ctx context.Context, ids []int64) {
	if len(ids) == 0 {
		return
	}
	db := s.store.GetDB()
	// Append "entity_extracted" concept via JSONB concatenation (no schema change per NFR-5).
	// Avoids overwriting subtitle or other fields.
	if err := db.WithContext(ctx).
		Table("observations").
		Where("id IN ?", ids).
		Update("concepts", gorm.Expr(`concepts || '["entity_extracted"]'::jsonb`)).Error; err != nil {
		s.log.Warn().Err(err).Msg("Failed to mark observations as entity-extracted")
	}
}
