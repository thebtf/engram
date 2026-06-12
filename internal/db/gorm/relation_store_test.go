// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
)

// openRelationTestDB opens a test PostgreSQL connection with migrations applied.
// Skips the test when DATABASE_DSN is unset.
func openRelationTestDB(t *testing.T) (*RelationStore, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping relation store integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "open test DB")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	require.NoError(t, runMigrations(db), "runMigrations")

	rs := &RelationStore{db: db}

	cleanup := func() { sqlDB.Close() }
	return rs, cleanup
}

// makeRelation constructs a minimal relation for testing with unique source/target.
func makeRelation(sourceID, targetID int64, relType models.RelationType) *models.ObservationRelation {
	now := time.Now()
	return &models.ObservationRelation{
		SourceID:        sourceID,
		TargetID:        targetID,
		RelationType:    relType,
		Confidence:      0.8,
		DetectionSource: models.DetectionSourceFileOverlap,
		CreatedAt:       now.Format(time.RFC3339),
		CreatedAtEpoch:  now.UnixMilli(),
	}
}

// TestRelationStore_StoreRelation verifies a relation can be stored and returns a positive ID.
func TestRelationStore_StoreRelation(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id = 8001 AND target_id = 8002")

	ctx := context.Background()
	rel := makeRelation(8001, 8002, models.RelationCauses)
	rel.Reason = "test-reason"

	id, err := rs.StoreRelation(ctx, rel)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))
}

// TestRelationStore_StoreRelation_Idempotency verifies that storing the same
// (source, target, type) triple twice returns the same ID.
func TestRelationStore_StoreRelation_Idempotency(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id = 8003 AND target_id = 8004")

	ctx := context.Background()
	rel := makeRelation(8003, 8004, models.RelationCauses)

	id1, err := rs.StoreRelation(ctx, rel)
	require.NoError(t, err)

	id2, err := rs.StoreRelation(ctx, rel)
	require.NoError(t, err)

	assert.Equal(t, id1, id2, "idempotent: second StoreRelation must return the same ID")
}

// TestRelationStore_StoreRelations verifies batch storage stores all relations.
func TestRelationStore_StoreRelations(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8010, 8011)")

	ctx := context.Background()
	rels := []*models.ObservationRelation{
		makeRelation(8010, 8011, models.RelationCauses),
		makeRelation(8011, 8012, models.RelationFixes),
	}

	err := rs.StoreRelations(ctx, rels)
	require.NoError(t, err)

	total, err := rs.GetTotalRelationCount(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, total, 2)
}

// TestRelationStore_StoreRelations_Empty verifies empty batch is a no-op.
func TestRelationStore_StoreRelations_Empty(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	err := rs.StoreRelations(ctx, nil)
	require.NoError(t, err)

	err = rs.StoreRelations(ctx, []*models.ObservationRelation{})
	require.NoError(t, err)
}

// TestRelationStore_GetRelationsByObservationID verifies bidirectional edge lookup.
func TestRelationStore_GetRelationsByObservationID(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8020, 8021) OR target_id IN (8020, 8021, 8022)")

	ctx := context.Background()
	// obs 8021 is in the middle of two edges.
	err := rs.StoreRelations(ctx, []*models.ObservationRelation{
		makeRelation(8020, 8021, models.RelationCauses),
		makeRelation(8021, 8022, models.RelationFixes),
	})
	require.NoError(t, err)

	result, err := rs.GetRelationsByObservationID(ctx, 8021)
	require.NoError(t, err)
	assert.Len(t, result, 2, "obs 8021 is both source and target")
}

// TestRelationStore_GetOutgoingAndIncomingRelations verifies direction-specific queries.
func TestRelationStore_GetOutgoingAndIncomingRelations(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8030, 8031, 8032) OR target_id IN (8030, 8031, 8032)")

	ctx := context.Background()
	err := rs.StoreRelations(ctx, []*models.ObservationRelation{
		makeRelation(8030, 8031, models.RelationCauses), // 8030 → 8031
		makeRelation(8032, 8030, models.RelationFixes),  // 8032 → 8030
	})
	require.NoError(t, err)

	outgoing, err := rs.GetOutgoingRelations(ctx, 8030)
	require.NoError(t, err)
	require.Len(t, outgoing, 1)
	assert.Equal(t, int64(8031), outgoing[0].TargetID)

	incoming, err := rs.GetIncomingRelations(ctx, 8030)
	require.NoError(t, err)
	require.Len(t, incoming, 1)
	assert.Equal(t, int64(8032), incoming[0].SourceID)
}

// TestRelationStore_GetRelationsByType verifies type-filtered retrieval.
func TestRelationStore_GetRelationsByType(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8040, 8041)")

	ctx := context.Background()
	err := rs.StoreRelations(ctx, []*models.ObservationRelation{
		makeRelation(8040, 8041, models.RelationDependsOn),
		makeRelation(8041, 8042, models.RelationCauses),
	})
	require.NoError(t, err)

	results, err := rs.GetRelationsByType(ctx, models.RelationDependsOn, 10)
	require.NoError(t, err)
	for _, r := range results {
		assert.Equal(t, models.RelationDependsOn, r.RelationType)
	}
}

// TestRelationStore_GetRelationCount verifies per-observation edge counting.
func TestRelationStore_GetRelationCount(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8050, 8051) OR target_id IN (8050, 8051, 8052)")

	ctx := context.Background()
	err := rs.StoreRelations(ctx, []*models.ObservationRelation{
		makeRelation(8050, 8051, models.RelationCauses),
		makeRelation(8051, 8052, models.RelationFixes),
	})
	require.NoError(t, err)

	// 8051 appears in both edges.
	count, err := rs.GetRelationCount(ctx, 8051)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// 8050 appears in only one edge.
	count, err = rs.GetRelationCount(ctx, 8050)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestRelationStore_DeleteRelationsByObservationID verifies cascading deletion.
func TestRelationStore_DeleteRelationsByObservationID(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8060, 8061, 8062, 8063) OR target_id IN (8060, 8061, 8062, 8063)")

	ctx := context.Background()
	err := rs.StoreRelations(ctx, []*models.ObservationRelation{
		makeRelation(8060, 8061, models.RelationCauses),
		makeRelation(8061, 8062, models.RelationFixes),
		makeRelation(8062, 8063, models.RelationRelatesTo),
	})
	require.NoError(t, err)

	err = rs.DeleteRelationsByObservationID(ctx, 8061)
	require.NoError(t, err)

	count, err := rs.GetRelationCount(ctx, 8061)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// 8062→8063 should still exist.
	count, err = rs.GetRelationCount(ctx, 8062)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestRelationStore_GetRelationCountsBatch verifies batch count lookup.
func TestRelationStore_GetRelationCountsBatch(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8070, 8071, 8072, 8073) OR target_id IN (8070, 8071, 8072, 8073)")

	ctx := context.Background()
	now := time.Now()
	rels := []*models.ObservationRelation{
		{SourceID: 8070, TargetID: 8071, RelationType: models.RelationCauses, Confidence: 0.4, DetectionSource: models.DetectionSourceFileOverlap, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
		{SourceID: 8071, TargetID: 8072, RelationType: models.RelationCauses, Confidence: 0.8, DetectionSource: models.DetectionSourceTemporalProximity, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
		{SourceID: 8071, TargetID: 8073, RelationType: models.RelationFixes, Confidence: 0.6, DetectionSource: models.DetectionSourceConceptOverlap, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
		{SourceID: 8073, TargetID: 8071, RelationType: models.RelationRelatesTo, Confidence: 0.9, DetectionSource: models.DetectionSourceFileOverlap, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
	}

	err := rs.StoreRelations(ctx, rels)
	require.NoError(t, err)

	counts, err := rs.GetRelationCountsBatch(ctx, []int64{8071, 8072})
	require.NoError(t, err)
	assert.Equal(t, 3, counts[8071]) // source of 2 edges, target of 1
	assert.Equal(t, 1, counts[8072]) // target of 1 edge
}

// TestRelationStore_GetRelationCountsBatch_Empty verifies empty input returns empty map.
func TestRelationStore_GetRelationCountsBatch_Empty(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	counts, err := rs.GetRelationCountsBatch(ctx, []int64{})
	require.NoError(t, err)
	assert.Empty(t, counts)
}

// TestRelationStore_GetAvgConfidenceBatch verifies batch average confidence.
func TestRelationStore_GetAvgConfidenceBatch(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8080, 8081) OR target_id IN (8080, 8081, 8082, 8083)")

	ctx := context.Background()
	now := time.Now()
	rels := []*models.ObservationRelation{
		{SourceID: 8080, TargetID: 8081, RelationType: models.RelationCauses, Confidence: 0.5, DetectionSource: models.DetectionSourceFileOverlap, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
		{SourceID: 8081, TargetID: 8082, RelationType: models.RelationCauses, Confidence: 0.7, DetectionSource: models.DetectionSourceTemporalProximity, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
		{SourceID: 8081, TargetID: 8083, RelationType: models.RelationFixes, Confidence: 0.9, DetectionSource: models.DetectionSourceConceptOverlap, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
	}
	err := rs.StoreRelations(ctx, rels)
	require.NoError(t, err)

	avg, err := rs.GetAvgConfidenceBatch(ctx, []int64{8081, 8083})
	require.NoError(t, err)
	// 8081 is source of 2 edges (0.7, 0.9) and target of 1 edge (0.5) → avg of 3 = (0.5+0.7+0.9)/3
	assert.InDelta(t, (0.5+0.7+0.9)/3.0, avg[8081], 1e-6)
	assert.InDelta(t, 0.9, avg[8083], 1e-6)
}

// TestRelationStore_GetAvgConfidenceBatch_Empty verifies empty input returns empty map.
func TestRelationStore_GetAvgConfidenceBatch_Empty(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	avg, err := rs.GetAvgConfidenceBatch(ctx, []int64{})
	require.NoError(t, err)
	assert.Empty(t, avg)
}

// TestRelationStore_GetHighConfidenceRelations verifies confidence threshold filtering.
func TestRelationStore_GetHighConfidenceRelations(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8090, 8091)")

	ctx := context.Background()
	now := time.Now()
	err := rs.StoreRelations(ctx, []*models.ObservationRelation{
		{SourceID: 8090, TargetID: 8091, RelationType: models.RelationCauses, Confidence: 0.9, DetectionSource: models.DetectionSourceFileOverlap, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
		{SourceID: 8091, TargetID: 8092, RelationType: models.RelationFixes, Confidence: 0.3, DetectionSource: models.DetectionSourceTemporalProximity, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
	})
	require.NoError(t, err)

	results, err := rs.GetHighConfidenceRelations(ctx, 0.8, 100)
	require.NoError(t, err)
	for _, r := range results {
		assert.GreaterOrEqual(t, r.Confidence, 0.8)
	}
}

// TestRelationStore_UpdateRelationConfidence verifies confidence score updates.
func TestRelationStore_UpdateRelationConfidence(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id = 8095 AND target_id = 8096")

	ctx := context.Background()
	rel := makeRelation(8095, 8096, models.RelationCauses)
	rel.Confidence = 0.5
	id, err := rs.StoreRelation(ctx, rel)
	require.NoError(t, err)

	err = rs.UpdateRelationConfidence(ctx, id, 0.95)
	require.NoError(t, err)

	// Verify via GetHighConfidenceRelations (≥0.9 should include the updated row).
	results, err := rs.GetHighConfidenceRelations(ctx, 0.9, 100)
	require.NoError(t, err)
	found := false
	for _, r := range results {
		if r.ID == id {
			found = true
			assert.InDelta(t, 0.95, r.Confidence, 1e-6)
		}
	}
	assert.True(t, found, "updated relation should appear in high-confidence results")
}

// TestRelationStore_GetRelatedObservationIDs verifies peer ID resolution.
func TestRelationStore_GetRelatedObservationIDs(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8100, 8101, 8102) OR target_id IN (8100, 8101, 8102)")

	ctx := context.Background()
	now := time.Now()
	err := rs.StoreRelations(ctx, []*models.ObservationRelation{
		{SourceID: 8100, TargetID: 8101, RelationType: models.RelationCauses, Confidence: 0.9, DetectionSource: models.DetectionSourceFileOverlap, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
		{SourceID: 8102, TargetID: 8100, RelationType: models.RelationFixes, Confidence: 0.7, DetectionSource: models.DetectionSourceTemporalProximity, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli()},
	})
	require.NoError(t, err)

	// Node 8100 connects to 8101 (outgoing) and 8102 (incoming peer).
	ids, err := rs.GetRelatedObservationIDs(ctx, 8100, 0.5)
	require.NoError(t, err)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, int64(8101))
	assert.Contains(t, ids, int64(8102))
}

// TestRelationStore_InvalidateRelation verifies temporal invalidation sets valid_to.
func TestRelationStore_InvalidateRelation(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id = 8105 AND target_id = 8106")

	ctx := context.Background()
	rel := makeRelation(8105, 8106, models.RelationCauses)
	id, err := rs.StoreRelation(ctx, rel)
	require.NoError(t, err)

	err = rs.InvalidateRelation(ctx, id)
	require.NoError(t, err)

	// After invalidation the relation should not appear in GetRelationsAsOf with a
	// future as-of time. InvalidateRelation sets valid_to ≈ now; querying at
	// now+1h means valid_to < asOf, so the row no longer satisfies the filter.
	future := time.Now().Add(time.Hour)
	results, err := rs.GetRelationsAsOf(ctx, 8105, future)
	require.NoError(t, err)
	for _, r := range results {
		assert.NotEqual(t, id, r.ID, "invalidated relation must not appear for future time")
	}
}

// TestRelationStore_GetRelationsAsOf verifies temporal-validity filtering.
func TestRelationStore_GetRelationsAsOf(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id = 8110 AND target_id = 8111")

	ctx := context.Background()
	before := time.Now().Add(-time.Second)
	rel := makeRelation(8110, 8111, models.RelationCauses)
	_, err := rs.StoreRelation(ctx, rel)
	require.NoError(t, err)

	// Query for future instant: relation has no valid_to so it should be returned.
	results, err := rs.GetRelationsAsOf(ctx, 8110, time.Now().Add(time.Hour))
	require.NoError(t, err)

	found := false
	for _, r := range results {
		if r.SourceID == 8110 && r.TargetID == 8111 {
			found = true
		}
	}
	assert.True(t, found, "open-ended relation should be visible at a future instant")
	_ = before
}

// TestRelationStore_GetDistinctNodeCount verifies distinct node counting.
func TestRelationStore_GetDistinctNodeCount(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8120, 8121) OR target_id IN (8120, 8121, 8122)")

	ctx := context.Background()
	before, err := rs.GetDistinctNodeCount(ctx)
	require.NoError(t, err)

	err = rs.StoreRelations(ctx, []*models.ObservationRelation{
		makeRelation(8120, 8121, models.RelationCauses),
		makeRelation(8121, 8122, models.RelationFixes),
	})
	require.NoError(t, err)

	after, err := rs.GetDistinctNodeCount(ctx)
	require.NoError(t, err)
	// Three new nodes (8120, 8121, 8122) should be added.
	assert.Equal(t, before+3, after)
}

// TestRelationStore_GetMaxDegree verifies max degree computation returns a non-negative value.
func TestRelationStore_GetMaxDegree(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	deg, err := rs.GetMaxDegree(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, deg, 0)
}

// TestRelationStore_CallbackFiringOnStoreRelation verifies the callback fires after commit.
func TestRelationStore_CallbackFiringOnStoreRelation(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id = 8130 AND target_id = 8131")

	ctx := context.Background()
	var called int
	rs.SetCallback(func(rels []*models.ObservationRelation) {
		called++
	})

	_, err := rs.StoreRelation(ctx, makeRelation(8130, 8131, models.RelationCauses))
	require.NoError(t, err)
	assert.Equal(t, 1, called, "callback must fire exactly once after StoreRelation")
}

// TestRelationStore_CallbackFiringOnStoreRelations verifies batch callback fires once.
func TestRelationStore_CallbackFiringOnStoreRelations(t *testing.T) {
	rs, cleanup := openRelationTestDB(t)
	defer cleanup()
	defer rs.db.Exec("DELETE FROM observation_relations WHERE source_id IN (8140, 8141)")

	ctx := context.Background()
	var called int
	rs.SetCallback(func(rels []*models.ObservationRelation) {
		called++
	})

	err := rs.StoreRelations(ctx, []*models.ObservationRelation{
		makeRelation(8140, 8141, models.RelationCauses),
		makeRelation(8141, 8142, models.RelationFixes),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, called, "StoreRelations must fire the callback exactly once per batch")
}

// TestRelationStore_ToDBRelation_ReasonMapping verifies Reason field is set correctly.
func TestRelationStore_ToDBRelation_ReasonMapping(t *testing.T) {
	rel := &models.ObservationRelation{
		SourceID:        1,
		TargetID:        2,
		RelationType:    models.RelationCauses,
		Confidence:      0.8,
		DetectionSource: models.DetectionSourceFileOverlap,
		Reason:          "explicit reason",
	}
	db := toDBRelation(rel)
	assert.True(t, db.Reason.Valid)
	assert.Equal(t, "explicit reason", db.Reason.String)

	relNoReason := &models.ObservationRelation{
		SourceID:        3,
		TargetID:        4,
		RelationType:    models.RelationFixes,
		Confidence:      0.5,
		DetectionSource: models.DetectionSourceTemporalProximity,
	}
	dbNoReason := toDBRelation(relNoReason)
	assert.False(t, dbNoReason.Reason.Valid)
}

// TestRelationStore_ToModelRelation_ReasonMapping verifies round-trip Reason propagation.
func TestRelationStore_ToModelRelation_ReasonMapping(t *testing.T) {
	dbRel := &ObservationRelation{
		ID:              42,
		SourceID:        1,
		TargetID:        2,
		RelationType:    models.RelationCauses,
		Confidence:      0.9,
		DetectionSource: models.DetectionSourceFileOverlap,
		Reason:          sqlNullString("from db"),
	}
	m := toModelRelation(dbRel)
	assert.Equal(t, "from db", m.Reason)

	dbNoReason := &ObservationRelation{
		ID:              43,
		SourceID:        3,
		TargetID:        4,
		RelationType:    models.RelationFixes,
		Confidence:      0.5,
		DetectionSource: models.DetectionSourceTemporalProximity,
	}
	mNoReason := toModelRelation(dbNoReason)
	assert.Equal(t, "", mNoReason.Reason)
}
