package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/thebtf/engram/internal/synthesis"
	"github.com/thebtf/engram/pkg/models"
)

// generateWikiPages finds entities with sufficient sources and generates wiki summaries.
// Returns the number of wiki pages generated.
func (s *Service) generateWikiPages(ctx context.Context) (int, error) {
	if s.llmClient == nil {
		return 0, fmt.Errorf("LLM client not available")
	}

	db := s.store.GetDB()
	limit := s.config.WikiGenerationLimit
	if limit <= 0 {
		limit = 10
	}
	minSources := s.config.WikiMinSources
	if minSources <= 0 {
		minSources = 5
	}
	wikiDataDir := s.config.WikiDataDir

	// Find entity observations with enough sources and no recent wiki
	// (or wiki that needs regeneration because source count changed)
	var entities []struct {
		ID        int64
		Title     string
		Subtitle  string
		Narrative string
		Project   string
	}

	err := db.WithContext(ctx).
		Table("observations").
		Select("id, title, COALESCE(subtitle, '') as subtitle, COALESCE(narrative, '') as narrative, project").
		Where("type = 'entity' AND is_superseded = 0 AND is_archived = 0").
		Order("created_at_epoch DESC").
		Limit(limit * 2). // fetch more to filter
		Find(&entities).Error
	if err != nil {
		return 0, fmt.Errorf("query entity observations: %w", err)
	}

	generator := &synthesis.WikiGenerator{}
	generated := 0
	var indexEntries []synthesis.WikiIndexEntry

	for _, entity := range entities {
		if generated >= limit {
			break
		}

		// Parse entity metadata to check source count
		var meta synthesis.EntityMetadata
		if err := json.Unmarshal([]byte(entity.Narrative), &meta); err != nil {
			continue // skip entities with unparseable metadata
		}

		if meta.ObservationCount < minSources {
			continue
		}

		// Check if wiki already exists and is up-to-date
		var existingWikiID int64
		var existingNarrative string
		_ = db.WithContext(ctx).
			Table("observations").
			Select("id, COALESCE(narrative, '') as narrative").
			Where("type = 'wiki' AND LOWER(title) = LOWER(?) AND project = ? AND is_superseded = 0",
				fmt.Sprintf("Wiki: %s", entity.Title), entity.Project).
			Row().Scan(&existingWikiID, &existingNarrative)

		if existingWikiID > 0 {
			// Check if source count changed enough to regenerate
			var existingWikiMeta synthesis.WikiMetadata
			_ = json.Unmarshal([]byte(existingNarrative), &existingWikiMeta)
			if meta.ObservationCount-existingWikiMeta.SourceCount < 3 {
				// Not enough new sources — skip regeneration
				indexEntries = append(indexEntries, synthesis.WikiIndexEntry{
					EntityName:  entity.Title,
					EntityType:  meta.EntityType,
					Slug:        synthesis.EntitySlug(entity.Title),
					SourceCount: meta.ObservationCount,
				})
				continue
			}
		}

		// Gather source observations
		sourceObs, err := s.getSourceObservations(ctx, meta.SourceObservationIDs)
		if err != nil || len(sourceObs) == 0 {
			continue
		}

		// Build models.Observation for the entity
		entityObs := &models.Observation{
			ID:       entity.ID,
			Title:    sql.NullString{String: entity.Title, Valid: entity.Title != ""},
			Subtitle: sql.NullString{String: entity.Subtitle, Valid: entity.Subtitle != ""},
		}

		result, err := generator.Generate(ctx, s.llmClient, entityObs, sourceObs)
		if err != nil {
			s.log.Warn().Err(err).Str("entity", entity.Title).Msg("Wiki generation failed")
			continue
		}

		// Store wiki observation
		wikiMeta := synthesis.WikiMetadata{
			EntityName:    entity.Title,
			EntityID:      entity.ID,
			SourceCount:   meta.ObservationCount,
			LastGenerated: time.Now().UTC().Format(time.RFC3339),
			WikiFile:      fmt.Sprintf("wiki/%s.md", synthesis.EntitySlug(entity.Title)),
		}
		wikiMetaJSON, _ := json.Marshal(wikiMeta)

		if existingWikiID > 0 {
			// Supersede old wiki
			_ = db.WithContext(ctx).
				Table("observations").
				Where("id = ?", existingWikiID).
				Update("is_superseded", 1).Error
		}

		nowEpoch := time.Now().UnixMilli()
		conceptsJSON, _ := json.Marshal([]string{"wiki", meta.EntityType})

		if err := db.WithContext(ctx).Exec(
			`INSERT INTO observations (project, scope, type, source_type, title, narrative, concepts, created_at, created_at_epoch, is_superseded, is_archived)
			 VALUES (?, ?, 'wiki', 'llm_derived', ?, ?, ?, ?, ?, 0, 0)`,
			entity.Project,
			models.DetermineScope([]string{"wiki", meta.EntityType}),
			fmt.Sprintf("Wiki: %s", entity.Title),
			string(wikiMetaJSON),
			string(conceptsJSON),
			time.Now().Format(time.RFC3339),
			nowEpoch,
		).Error; err != nil {
			s.log.Warn().Err(err).Str("entity", entity.Title).Msg("Failed to store wiki observation")
			continue
		}

		// Write markdown to disk
		if wikiDataDir != "" {
			if err := synthesis.WriteWikiToDisk(wikiDataDir, entity.Title, meta.EntityType, result.Content, meta.ObservationCount); err != nil {
				s.log.Warn().Err(err).Str("entity", entity.Title).Msg("Failed to write wiki to disk")
			}
		}

		indexEntries = append(indexEntries, synthesis.WikiIndexEntry{
			EntityName:  entity.Title,
			EntityType:  meta.EntityType,
			Slug:        synthesis.EntitySlug(entity.Title),
			SourceCount: meta.ObservationCount,
		})

		generated++
	}

	// Update wiki index
	if wikiDataDir != "" && len(indexEntries) > 0 {
		if err := synthesis.UpdateWikiIndex(wikiDataDir, indexEntries); err != nil {
			s.log.Warn().Err(err).Msg("Failed to update wiki index")
		}
	}

	return generated, nil
}

// getSourceObservations fetches observations by their IDs.
func (s *Service) getSourceObservations(ctx context.Context, ids []int64) ([]*models.Observation, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if s.observationStore == nil {
		return nil, fmt.Errorf("observation store not available")
	}
	return s.observationStore.GetObservationsByIDs(ctx, ids, "created_at_epoch DESC", 10)
}

