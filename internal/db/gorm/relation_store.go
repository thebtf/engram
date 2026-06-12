// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thebtf/engram/pkg/models"
)

// RelationCallback is called after relations are stored in the database.
// The callback fires AFTER the transaction commits so callers observe a
// consistent DB state when they react to new relations (e.g. triggering
// graph re-indexing or SSE broadcasts).
type RelationCallback func(relations []*models.ObservationRelation)

// RelationStore provides relation-related database operations using GORM.
type RelationStore struct {
	db       *gorm.DB
	callback RelationCallback
}

// NewRelationStore creates a new relation store backed by the given Store.
// The callback is not set at construction because it is wired after the store
// is registered in the dependency graph (avoids initialization-order cycles).
func NewRelationStore(store *Store) *RelationStore {
	return &RelationStore{
		db: store.DB,
	}
}

// SetCallback registers a callback that fires after relations are stored.
func (s *RelationStore) SetCallback(cb RelationCallback) {
	s.callback = cb
}

// toDBRelation converts a pkg/models relation into the GORM model used for
// persistence. The Reason field is stored as a nullable SQL string because
// many relations are detected automatically without a human-readable reason.
func toDBRelation(rel *models.ObservationRelation) *ObservationRelation {
	db := &ObservationRelation{
		SourceID:        rel.SourceID,
		TargetID:        rel.TargetID,
		RelationType:    rel.RelationType,
		Confidence:      rel.Confidence,
		DetectionSource: rel.DetectionSource,
		CreatedAt:       rel.CreatedAt,
		CreatedAtEpoch:  rel.CreatedAtEpoch,
	}
	if rel.Reason != "" {
		db.Reason = sql.NullString{String: rel.Reason, Valid: true}
	}
	return db
}

// StoreRelation persists a new observation relation. Duplicate
// (source_id, target_id, relation_type) combinations are silently ignored via
// INSERT … ON CONFLICT DO NOTHING; the existing row's ID is returned instead.
func (s *RelationStore) StoreRelation(ctx context.Context, relation *models.ObservationRelation) (int64, error) {
	dbRelation := toDBRelation(relation)

	result := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_id"}, {Name: "target_id"}, {Name: "relation_type"}},
			DoNothing: true,
		}).
		Create(dbRelation)

	if result.Error != nil {
		return 0, result.Error
	}

	if result.RowsAffected == 0 {
		// The insert was suppressed by the conflict clause. Look up the
		// pre-existing relation so callers get a valid ID to work with.
		var existing ObservationRelation
		err := s.db.Where("source_id = ? AND target_id = ? AND relation_type = ?",
			relation.SourceID, relation.TargetID, relation.RelationType).
			First(&existing).Error
		if err != nil {
			return 0, err
		}
		return existing.ID, nil
	}

	// Fire callback AFTER the implicit auto-transaction commits, not inside it,
	// so listeners see a fully committed state.
	if s.callback != nil {
		s.callback([]*models.ObservationRelation{relation})
	}

	return dbRelation.ID, nil
}

// StoreRelations persists multiple relations in a single transaction.
// Each relation uses INSERT … ON CONFLICT DO NOTHING, so partial overlap with
// existing rows does not cause the whole batch to fail.
// The callback fires once after the transaction commits, with all input relations.
func (s *RelationStore) StoreRelations(ctx context.Context, relations []*models.ObservationRelation) error {
	if len(relations) == 0 {
		return nil
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, rel := range relations {
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "source_id"}, {Name: "target_id"}, {Name: "relation_type"}},
				DoNothing: true,
			}).Create(toDBRelation(rel))

			if result.Error != nil {
				return result.Error
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Callback fires after the transaction commits — see StoreRelation for rationale.
	if s.callback != nil {
		s.callback(relations)
	}

	return nil
}

// GetRelationsByObservationID retrieves all relations in which obsID appears
// as either the source or the target, ordered by descending confidence then
// descending epoch.
func (s *RelationStore) GetRelationsByObservationID(ctx context.Context, obsID int64) ([]*models.ObservationRelation, error) {
	var relations []ObservationRelation

	err := s.db.WithContext(ctx).
		Where("source_id = ? OR target_id = ?", obsID, obsID).
		Order("confidence DESC, created_at_epoch DESC").
		Find(&relations).Error

	if err != nil {
		return nil, err
	}

	return toModelRelations(relations), nil
}

// GetOutgoingRelations retrieves relations where obsID is the source node.
func (s *RelationStore) GetOutgoingRelations(ctx context.Context, obsID int64) ([]*models.ObservationRelation, error) {
	var relations []ObservationRelation

	err := s.db.WithContext(ctx).
		Where("source_id = ?", obsID).
		Order("confidence DESC, created_at_epoch DESC").
		Find(&relations).Error

	if err != nil {
		return nil, err
	}

	return toModelRelations(relations), nil
}

// GetIncomingRelations retrieves relations where obsID is the target node.
func (s *RelationStore) GetIncomingRelations(ctx context.Context, obsID int64) ([]*models.ObservationRelation, error) {
	var relations []ObservationRelation

	err := s.db.WithContext(ctx).
		Where("target_id = ?", obsID).
		Order("confidence DESC, created_at_epoch DESC").
		Find(&relations).Error

	if err != nil {
		return nil, err
	}

	return toModelRelations(relations), nil
}

// GetRelationsByType retrieves up to limit relations of the specified type,
// ordered by descending confidence then descending epoch.
func (s *RelationStore) GetRelationsByType(ctx context.Context, relationType models.RelationType, limit int) ([]*models.ObservationRelation, error) {
	var relations []ObservationRelation

	err := s.db.WithContext(ctx).
		Where("relation_type = ?", relationType).
		Order("confidence DESC, created_at_epoch DESC").
		Limit(limit).
		Find(&relations).Error

	if err != nil {
		return nil, err
	}

	return toModelRelations(relations), nil
}

// DeleteRelationsByObservationID deletes all relations involving obsID.
// Called as part of cascading observation deletion.
func (s *RelationStore) DeleteRelationsByObservationID(ctx context.Context, obsID int64) error {
	result := s.db.WithContext(ctx).
		Where("source_id = ? OR target_id = ?", obsID, obsID).
		Delete(&ObservationRelation{})

	return result.Error
}

// GetRelationCount returns the total number of relations (in + out) for obsID.
func (s *RelationStore) GetRelationCount(ctx context.Context, obsID int64) (int, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&ObservationRelation{}).
		Where("source_id = ? OR target_id = ?", obsID, obsID).
		Count(&count).Error

	return int(count), err
}

// GetRelationCountsBatch returns a map of obsID → relation count for all
// requested observation IDs. Uses a UNION ALL subquery so both directions
// (source and target) are counted in a single round-trip.
func (s *RelationStore) GetRelationCountsBatch(ctx context.Context, obsIDs []int64) (map[int64]int, error) {
	counts := make(map[int64]int, len(obsIDs))
	if len(obsIDs) == 0 {
		return counts, nil
	}

	var rows []struct {
		ObsID int64 `gorm:"column:obs_id"`
		Cnt   int64 `gorm:"column:cnt"`
	}

	err := s.db.WithContext(ctx).
		Raw("SELECT obs_id, COUNT(*) AS cnt FROM (SELECT source_id AS obs_id FROM observation_relations WHERE source_id IN (?) UNION ALL SELECT target_id AS obs_id FROM observation_relations WHERE target_id IN (?)) t GROUP BY obs_id", obsIDs, obsIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		counts[row.ObsID] = int(row.Cnt)
	}

	return counts, nil
}

// GetAvgConfidenceBatch returns a map of obsID → average confidence for all
// requested observation IDs. Uses a UNION ALL subquery for both edge directions.
func (s *RelationStore) GetAvgConfidenceBatch(ctx context.Context, obsIDs []int64) (map[int64]float64, error) {
	avgConfidence := make(map[int64]float64, len(obsIDs))
	if len(obsIDs) == 0 {
		return avgConfidence, nil
	}

	var rows []struct {
		ObsID   int64   `gorm:"column:obs_id"`
		AvgConf float64 `gorm:"column:avg_conf"`
	}

	err := s.db.WithContext(ctx).
		Raw("SELECT obs_id, AVG(confidence) AS avg_conf FROM (SELECT source_id AS obs_id, confidence FROM observation_relations WHERE source_id IN (?) UNION ALL SELECT target_id AS obs_id, confidence FROM observation_relations WHERE target_id IN (?)) t GROUP BY obs_id", obsIDs, obsIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		avgConfidence[row.ObsID] = row.AvgConf
	}

	return avgConfidence, nil
}

// GetTotalRelationCount returns the total number of relations across all observations.
func (s *RelationStore) GetTotalRelationCount(ctx context.Context) (int, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&ObservationRelation{}).
		Count(&count).Error

	return int(count), err
}

// GetDistinctNodeCount returns the number of unique observation IDs that
// participate in at least one relation (as source or target).
func (s *RelationStore) GetDistinctNodeCount(ctx context.Context) (int, error) {
	var count int64
	err := s.db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM (SELECT source_id AS id FROM observation_relations UNION SELECT target_id FROM observation_relations) AS nodes`,
	).Scan(&count).Error
	return int(count), err
}

// GetMaxDegree returns the maximum node degree (combined in-edges + out-edges)
// across the whole relation graph. Returns 0 when the graph is empty.
func (s *RelationStore) GetMaxDegree(ctx context.Context) (int, error) {
	var maxDeg int64
	err := s.db.WithContext(ctx).Raw(`
		SELECT COALESCE(MAX(degree), 0) FROM (
			SELECT id, COUNT(*) AS degree FROM (
				SELECT source_id AS id FROM observation_relations
				UNION ALL
				SELECT target_id AS id FROM observation_relations
			) AS all_nodes
			GROUP BY id
		) AS degrees
	`).Scan(&maxDeg).Error
	return int(maxDeg), err
}

// GetHighConfidenceRelations retrieves up to limit relations with
// confidence ≥ minConfidence, ordered by descending confidence then epoch.
func (s *RelationStore) GetHighConfidenceRelations(ctx context.Context, minConfidence float64, limit int) ([]*models.ObservationRelation, error) {
	var relations []ObservationRelation

	err := s.db.WithContext(ctx).
		Where("confidence >= ?", minConfidence).
		Order("confidence DESC, created_at_epoch DESC").
		Limit(limit).
		Find(&relations).Error

	if err != nil {
		return nil, err
	}

	return toModelRelations(relations), nil
}

// UpdateRelationConfidence updates the confidence score of a single relation.
func (s *RelationStore) UpdateRelationConfidence(ctx context.Context, relationID int64, newConfidence float64) error {
	result := s.db.WithContext(ctx).
		Model(&ObservationRelation{}).
		Where("id = ?", relationID).
		Update("confidence", newConfidence)

	return result.Error
}

// GetRelatedObservationIDs returns the IDs of observations directly connected
// to obsID by at least one relation with confidence ≥ minConfidence. The CASE
// expression resolves bidirectional edges (source→target and target→source) to
// the peer ID. Raw SQL is used here because GORM's query builder does not have
// a clean way to express CASE inside a SELECT column list without raw fragments.
func (s *RelationStore) GetRelatedObservationIDs(ctx context.Context, obsID int64, minConfidence float64) ([]int64, error) {
	var ids []int64

	err := s.db.WithContext(ctx).
		Raw("SELECT DISTINCT CASE WHEN source_id = ? THEN target_id ELSE source_id END as related_id "+
			"FROM observation_relations "+
			"WHERE (source_id = ? OR target_id = ?) AND confidence >= ?",
			obsID, obsID, obsID, minConfidence).
		Pluck("related_id", &ids).Error

	return ids, err
}

// toModelRelation converts a GORM ObservationRelation to the pkg/models type.
// ValidFrom and ValidTo are propagated so temporal-validity queries (GetRelationsAsOf)
// can filter correctly at the model layer without re-hitting the database.
func toModelRelation(r *ObservationRelation) *models.ObservationRelation {
	relation := &models.ObservationRelation{
		ID:              r.ID,
		SourceID:        r.SourceID,
		TargetID:        r.TargetID,
		RelationType:    r.RelationType,
		Confidence:      r.Confidence,
		DetectionSource: r.DetectionSource,
		CreatedAt:       r.CreatedAt,
		CreatedAtEpoch:  r.CreatedAtEpoch,
		ValidFrom:       r.ValidFrom,
		ValidTo:         r.ValidTo,
	}

	if r.Reason.Valid {
		relation.Reason = r.Reason.String
	}

	return relation
}

// toModelRelations converts a slice of GORM relations to pkg/models relations.
func toModelRelations(relations []ObservationRelation) []*models.ObservationRelation {
	result := make([]*models.ObservationRelation, len(relations))
	for i, r := range relations {
		result[i] = toModelRelation(&r)
	}
	return result
}

// InvalidateRelation sets valid_to = now() on the specified relation, making it
// temporally bounded. The row is retained for historical queries; active queries
// filter it out via as-of predicates.
func (s *RelationStore) InvalidateRelation(ctx context.Context, relationID int64) error {
	return s.db.WithContext(ctx).
		Table("observation_relations").
		Where("id = ? AND valid_to IS NULL", relationID).
		Update("valid_to", time.Now()).Error
}

// GetRelationsAsOf returns relations that were valid at asOf for obsID.
// A relation is valid when:
//   - valid_from IS NULL (no explicit start) OR valid_from ≤ asOf
//   - valid_to   IS NULL (still open)       OR valid_to   ≥ asOf
func (s *RelationStore) GetRelationsAsOf(ctx context.Context, obsID int64, asOf time.Time) ([]*models.ObservationRelation, error) {
	var relations []ObservationRelation

	err := s.db.WithContext(ctx).
		Where("(source_id = ? OR target_id = ?)", obsID, obsID).
		Where("(valid_from IS NULL OR valid_from <= ?)", asOf).
		Where("(valid_to IS NULL OR valid_to >= ?)", asOf).
		Order("confidence DESC").
		Find(&relations).Error
	if err != nil {
		return nil, err
	}

	return toModelRelations(relations), nil
}
