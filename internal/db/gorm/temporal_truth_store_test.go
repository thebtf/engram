package gorm

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/temporaltruth"
	"github.com/thebtf/engram/pkg/cognitive"
)

func TestTemporalTruthStore_RefreshProjectAdmitsAllowlistedSupersessionChain(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewTemporalTruthStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("temporal-refresh-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM temporal_truth_records WHERE project = ?`, project).Error
		_ = db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error
	})

	validFromThen := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	validFromNow := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	first := insertTemporalTruthMemory(t, db, &Memory{
		Project:       project,
		Content:       "v6",
		Status:        "active",
		Tier:          "episodic",
		EpistemicType: "decision",
		Domain:        "release",
		ValidFrom:     &validFromThen,
		CreatedAt:     validFromThen,
		UpdatedAt:     validFromThen,
		Version:       1,
	})
	second := insertTemporalTruthMemory(t, db, &Memory{
		Project:       project,
		Content:       "v7",
		Status:        "active",
		Tier:          "episodic",
		EpistemicType: "decision",
		Domain:        "release",
		SupersedesID:  &first.ID,
		ValidFrom:     &validFromNow,
		CreatedAt:     validFromNow,
		UpdatedAt:     validFromNow,
		Version:       1,
	})
	require.NoError(t, db.Model(&Memory{}).Where("id = ?", first.ID).Updates(map[string]any{
		"status":        "superseded",
		"superseded_by": second.ID,
		"valid_until":   validFromNow,
		"updated_at":    validFromNow,
	}).Error)

	stats, err := store.RefreshProject(ctx, project)
	require.NoError(t, err)
	require.Equal(t, project, stats.Project)
	require.Equal(t, 1, stats.AdmittedFacts)
	require.Equal(t, 2, stats.AdmittedRecords)
	require.Zero(t, stats.ExcludedSingleWrite)
	require.Zero(t, stats.ExcludedUnsupported)

	service := temporaltruth.NewStoreBackedService(store)
	asOfThen := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	response, err := service.QueryTemporalTruth(ctx, cognitive.TemporalTruthQueryRequest{
		FactID:  strconv.FormatInt(first.ID, 10),
		Project: project,
		AsOf:    &asOfThen,
		Limit:   5,
	})
	require.NoError(t, err)
	require.Equal(t, cognitive.TemporalTruthFound, response.State)
	require.Equal(t, "release_policy", response.Scope.FactClass)
	require.NotNil(t, response.TrueNow)
	require.Equal(t, "v7", response.TrueNow.Value)
	require.NotNil(t, response.TrueThen)
	require.Equal(t, "v6", response.TrueThen.Value)
	require.Equal(t, fmt.Sprintf("superseded by memory %d", second.ID), response.TrueThen.InvalidationRationale)
	require.Len(t, response.History, 2)
	require.Len(t, response.TrueThen.Provenance, 1)
	require.Equal(t, fmt.Sprintf("memory:%d", first.ID), response.TrueThen.Provenance[0].ID)
}

func TestTemporalTruthStore_RefreshProjectRejectsSingleWriteAndUnsupportedChains(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewTemporalTruthStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("temporal-reject-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM temporal_truth_records WHERE project = ?`, project).Error
		_ = db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error
	})

	singleAt := time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)
	single := insertTemporalTruthMemory(t, db, &Memory{
		Project:       project,
		Content:       "only-once",
		Status:        "active",
		Tier:          "episodic",
		EpistemicType: "decision",
		Domain:        "release",
		ValidFrom:     &singleAt,
		CreatedAt:     singleAt,
		UpdatedAt:     singleAt,
		Version:       1,
	})

	unsupportedFrom := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	unsupportedTo := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	unsupportedFirst := insertTemporalTruthMemory(t, db, &Memory{
		Project:       project,
		Content:       "auth-policy-v1",
		Status:        "active",
		Tier:          "episodic",
		EpistemicType: "decision",
		Domain:        "auth",
		ValidFrom:     &unsupportedFrom,
		CreatedAt:     unsupportedFrom,
		UpdatedAt:     unsupportedFrom,
		Version:       1,
	})
	unsupportedSecond := insertTemporalTruthMemory(t, db, &Memory{
		Project:       project,
		Content:       "auth-policy-v2",
		Status:        "active",
		Tier:          "episodic",
		EpistemicType: "decision",
		Domain:        "auth",
		SupersedesID:  &unsupportedFirst.ID,
		ValidFrom:     &unsupportedTo,
		CreatedAt:     unsupportedTo,
		UpdatedAt:     unsupportedTo,
		Version:       1,
	})
	require.NoError(t, db.Model(&Memory{}).Where("id = ?", unsupportedFirst.ID).Updates(map[string]any{
		"status":        "superseded",
		"superseded_by": unsupportedSecond.ID,
		"valid_until":   unsupportedTo,
		"updated_at":    unsupportedTo,
	}).Error)

	stats, err := store.RefreshProject(ctx, project)
	require.NoError(t, err)
	require.Equal(t, 0, stats.AdmittedFacts)
	require.Equal(t, 0, stats.AdmittedRecords)
	require.Equal(t, 1, stats.ExcludedSingleWrite)
	require.Equal(t, 1, stats.ExcludedUnsupported)

	service := temporaltruth.NewStoreBackedService(store)
	response, err := service.QueryTemporalTruth(ctx, cognitive.TemporalTruthQueryRequest{
		FactID:  strconv.FormatInt(single.ID, 10),
		Project: project,
	})
	require.NoError(t, err)
	require.Equal(t, cognitive.TemporalTruthNotSelected, response.State)

	unsupportedResponse, err := service.QueryTemporalTruth(ctx, cognitive.TemporalTruthQueryRequest{
		FactID:  strconv.FormatInt(unsupportedFirst.ID, 10),
		Project: project,
	})
	require.NoError(t, err)
	require.Equal(t, cognitive.TemporalTruthNotSelected, unsupportedResponse.State)
}

func TestTemporalTruthStore_LoadSelectedRecordsUsesDBNowForValidFrom(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewTemporalTruthStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("temporal-now-%d", time.Now().UnixNano())
	factID := "42"
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM temporal_truth_records WHERE project = ?`, project).Error
	})

	futureStart := time.Now().UTC().Add(2 * time.Hour)
	pastStart := futureStart.Add(-24 * time.Hour)
	openEnded := temporalTruthOpenEndedSentinel
	require.NoError(t, db.Create(&temporalTruthRecordRow{
		FactID:          factID,
		FactClass:       "release_policy",
		Project:         project,
		Value:           "v7",
		ValidFrom:       pastStart,
		ValidUntil:      &futureStart,
		SourceMemoryIDs: Int64Array{11},
	}).Error)
	require.NoError(t, db.Create(&temporalTruthRecordRow{
		FactID:          factID,
		FactClass:       "release_policy",
		Project:         project,
		Value:           "v8",
		ValidFrom:       futureStart,
		ValidUntil:      &openEnded,
		SourceMemoryIDs: Int64Array{12},
	}).Error)

	records, err := store.LoadSelectedRecords(ctx, cognitive.TemporalTruthQueryRequest{FactID: factID, Project: project})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "v7", records[0].Value)

	service := temporaltruth.NewStoreBackedService(store)
	response, err := service.QueryTemporalTruth(ctx, cognitive.TemporalTruthQueryRequest{FactID: factID, Project: project})
	require.NoError(t, err)
	require.NotNil(t, response.TrueNow)
	require.Equal(t, "v7", response.TrueNow.Value)
	require.Len(t, response.History, 1)
}

func insertTemporalTruthMemory(t *testing.T, db *gorm.DB, row *Memory) *Memory {
	t.Helper()
	require.NoError(t, db.Create(row).Error)
	return row
}
