package synthesis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
)

// StoreEntitiesResult holds counts of created and updated entities.
type StoreEntitiesResult struct {
	Created int
	Updated int
}

// StoreEntitiesParams holds all dependencies needed by StoreEntities.
type StoreEntitiesParams struct {
	DB         *gorm.DB
	GraphStore graph.GraphStore
	Project    string
	Result     *ExtractionResult
	SourceIDs  []int64
}

// StoreEntities persists extracted entities as observations and creates FalkorDB edges.
// Entities are deduped by (lower(title), project, entity_type).
func StoreEntities(ctx context.Context, p StoreEntitiesParams) (*StoreEntitiesResult, error) {
	if p.Result == nil || len(p.Result.Entities) == 0 {
		return &StoreEntitiesResult{}, nil
	}

	counts := &StoreEntitiesResult{}
	now := time.Now().UTC().Format(time.RFC3339)
	entityIDsByName := make(map[string]int64)

	for _, entity := range p.Result.Entities {
		meta := EntityMetadata{
			EntityType:           entity.Type,
			ObservationCount:     len(p.SourceIDs),
			Relations:            extractRelationsFor(entity.Name, p.Result.Relations),
			SourceObservationIDs: p.SourceIDs,
			LastExtracted:        now,
		}
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return nil, fmt.Errorf("marshal entity metadata: %w", err)
		}

		// Dedup: find existing entity with same (lower(title), project, type=entity)
		var existingID int64
		var existingNarrative string
		err = p.DB.WithContext(ctx).
			Table("observations").
			Select("id, narrative").
			Where("LOWER(title) = ? AND project = ? AND type = 'entity' AND is_superseded = 0 AND is_archived = 0",
				strings.ToLower(entity.Name), p.Project).
			Row().Scan(&existingID, &existingNarrative)

		if err == nil && existingID > 0 {
			// Update existing: merge metadata
			var existingMeta EntityMetadata
			if unmarshalErr := json.Unmarshal([]byte(existingNarrative), &existingMeta); unmarshalErr != nil {
				// Corrupted metadata: skip merge to avoid overwriting with zero values.
				// The entity record will be left unchanged for this extraction cycle.
				counts.Updated++
				entityIDsByName[strings.ToLower(entity.Name)] = existingID
				continue
			}

			existingMeta.ObservationCount += len(p.SourceIDs)
			existingMeta.SourceObservationIDs = appendUnique(existingMeta.SourceObservationIDs, p.SourceIDs)
			existingMeta.Relations = mergeRelations(existingMeta.Relations, meta.Relations)
			existingMeta.LastExtracted = now

			updatedJSON, _ := json.Marshal(existingMeta)
			if err := p.DB.WithContext(ctx).
				Table("observations").
				Where("id = ?", existingID).
				Update("narrative", string(updatedJSON)).Error; err != nil {
				return nil, fmt.Errorf("update entity %q: %w", entity.Name, err)
			}

			entityIDsByName[strings.ToLower(entity.Name)] = existingID
			counts.Updated++
		} else {
			// Create new entity observation via direct insert
			nowEpoch := time.Now().UnixMilli()
			conceptsJSON, _ := json.Marshal([]string{"entity", entity.Type})

			var newID int64
			if err := p.DB.WithContext(ctx).Raw(
				`INSERT INTO observations (project, scope, type, source_type, title, subtitle, narrative, concepts, created_at, created_at_epoch, is_superseded, is_archived)
				 VALUES (?, ?, 'entity', 'llm_derived', ?, ?, ?, ?, ?, ?, 0, 0) RETURNING id`,
				p.Project,
				models.DetermineScope([]string{"entity", entity.Type}),
				entity.Name,
				entity.Type,
				string(metaJSON),
				string(conceptsJSON),
				time.Now().Format(time.RFC3339),
				nowEpoch,
			).Scan(&newID).Error; err != nil {
				return nil, fmt.Errorf("store entity %q: %w", entity.Name, err)
			}
			entityIDsByName[strings.ToLower(entity.Name)] = newID

			counts.Created++
		}
	}

	// Create FalkorDB edges
	if p.GraphStore != nil {
		var edges []graph.RelationEdge

		// Entity → source observations
		for _, entity := range p.Result.Entities {
			entityID, ok := entityIDsByName[strings.ToLower(entity.Name)]
			if !ok {
				continue
			}
			for _, srcID := range p.SourceIDs {
				edges = append(edges, graph.RelationEdge{
					SourceID:     entityID,
					TargetID:     srcID,
					RelationType: "extracted_from",
					Confidence:   0.8,
				})
			}
		}

		// Entity → entity edges
		for _, rel := range p.Result.Relations {
			fromID := entityIDsByName[strings.ToLower(rel.From)]
			toID := entityIDsByName[strings.ToLower(rel.To)]
			if fromID > 0 && toID > 0 {
				edges = append(edges, graph.RelationEdge{
					SourceID:     fromID,
					TargetID:     toID,
					RelationType: models.RelationType(rel.RelType),
					Confidence:   0.7,
				})
			}
		}

		if len(edges) > 0 {
			// Non-fatal: graph edges are supplementary
			_ = p.GraphStore.StoreEdgesBatch(ctx, edges)
		}
	}

	return counts, nil
}

// extractRelationsFor returns entity relations where the given entity is "from".
func extractRelationsFor(entityName string, relations []Relation) []EntityRelation {
	var result []EntityRelation
	lower := strings.ToLower(entityName)
	for _, r := range relations {
		if strings.ToLower(r.From) == lower {
			result = append(result, EntityRelation{To: r.To, Rel: r.RelType})
		}
	}
	return result
}

// appendUnique appends IDs not already present.
func appendUnique(existing, newIDs []int64) []int64 {
	seen := make(map[int64]bool, len(existing))
	for _, id := range existing {
		seen[id] = true
	}
	result := make([]int64, len(existing))
	copy(result, existing)
	for _, id := range newIDs {
		if !seen[id] {
			result = append(result, id)
		}
	}
	return result
}

// mergeRelations deduplicates by (to, rel) pair.
func mergeRelations(existing, newRels []EntityRelation) []EntityRelation {
	seen := make(map[string]bool, len(existing))
	for _, r := range existing {
		seen[strings.ToLower(r.To)+"|"+r.Rel] = true
	}
	result := make([]EntityRelation, len(existing))
	copy(result, existing)
	for _, r := range newRels {
		key := strings.ToLower(r.To) + "|" + r.Rel
		if !seen[key] {
			result = append(result, r)
			seen[key] = true
		}
	}
	return result
}
