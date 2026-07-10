package gorm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

func TestMemoryStore_RestoreRawTx_RestoresCompleteSnapshotExceptVersion(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	store := NewMemoryStore(&Store{DB: db})

	anchors := []Memory{
		{Project: "restore-raw-anchor", Content: "supersedes anchor", SourceAgent: "test"},
		{Project: "restore-raw-anchor", Content: "superseded-by anchor", SourceAgent: "test"},
	}
	require.NoError(t, db.Create(&anchors).Error)

	createdAt := time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC)
	updatedAt := time.Date(2025, 2, 3, 4, 5, 6, 234567000, time.UTC)
	deletedAt := time.Date(2025, 2, 4, 4, 5, 6, 345678000, time.UTC)
	lastRetrievedAt := time.Date(2025, 1, 20, 1, 2, 3, 456789000, time.UTC)
	lastConfirmed := time.Date(2025, 1, 21, 2, 3, 4, 567890000, time.UTC)
	reviewAfter := time.Date(2025, 6, 1, 0, 0, 0, 678901000, time.UTC)
	validFrom := time.Date(2023, 12, 1, 0, 0, 0, 789012000, time.UTC)
	validUntil := time.Date(2034, 12, 1, 0, 0, 0, 890123000, time.UTC)
	supersedesID := anchors[0].ID
	supersededBy := anchors[1].ID

	row := &Memory{
		Project:                  "restore-raw-before",
		Content:                  "complete snapshot content",
		Tags:                     models.JSONStringArray{"snapshot", "complete"},
		SourceAgent:              "snapshot-agent",
		EditedBy:                 "snapshot-editor",
		Status:                   "superseded",
		Tier:                     "semantic",
		EpistemicType:            "decision",
		Defeasibility:            "fast",
		PromotionTarget:          "procedural",
		PrivacyScope:             "private",
		SourceWorkstationID:      "workstation-before",
		SourceSessions:           pq.StringArray{"session-before-a", "session-before-b"},
		OwnerPrincipal:           "owner-before",
		OwnerPrincipalKind:       "human",
		AgentVisibility:          "private",
		Domain:                   "architecture",
		CreatedAt:                createdAt,
		UpdatedAt:                updatedAt,
		DeletedAt:                &deletedAt,
		LastRetrievedAt:          &lastRetrievedAt,
		LastConfirmed:            &lastConfirmed,
		ReviewAfter:              &reviewAfter,
		ValidFrom:                &validFrom,
		ValidUntil:               &validUntil,
		SupersedesID:             &supersedesID,
		SupersededBy:             &supersededBy,
		ImportanceBase:           0.81,
		TsAlpha:                  2.25,
		TsBeta:                   3.5,
		Confidence:               0.91,
		Stability:                45.5,
		Retrievability:           0.63,
		Version:                  7,
		CitationCount:            11,
		InjectionCount:           12,
		AccessCount:              13,
		RecurrenceCount:          14,
		ConsecutiveCitationCount: 15,
	}
	require.NoError(t, db.Create(row).Error)
	t.Cleanup(func() {
		_ = db.Unscoped().Delete(&Memory{}, "id IN ?", []int64{row.ID, anchors[0].ID, anchors[1].ID}).Error
	})

	var capturedRow Memory
	require.NoError(t, db.Unscoped().Where("id = ?", row.ID).First(&capturedRow).Error)
	encodedBefore, err := json.Marshal(&capturedRow)
	require.NoError(t, err)
	var before models.Memory
	require.NoError(t, json.Unmarshal(encodedBefore, &before))

	mutatedAt := time.Date(2026, 3, 4, 5, 6, 7, 901234000, time.UTC)
	mutatedSupersedesID := anchors[1].ID
	mutatedSupersededBy := anchors[0].ID
	const mutatedVersion = 19
	require.NoError(t, db.Unscoped().Model(&Memory{}).Where("id = ?", row.ID).Updates(map[string]any{
		"project":                    "restore-raw-mutated",
		"content":                    "mutated content",
		"tags":                       models.JSONStringArray{"mutated"},
		"source_agent":               "mutated-agent",
		"edited_by":                  "mutated-editor",
		"status":                     "active",
		"tier":                       "episodic",
		"epistemic_type":             "observation",
		"defeasibility":              "slow",
		"promotion_target":           "none",
		"privacy_scope":              "global",
		"source_workstation_id":      "workstation-mutated",
		"source_sessions":            pq.StringArray{"session-mutated"},
		"owner_principal":            "owner-mutated",
		"owner_principal_kind":       "service",
		"agent_visibility":           "shared",
		"domain":                     "mutated-domain",
		"created_at":                 mutatedAt,
		"updated_at":                 mutatedAt,
		"deleted_at":                 nil,
		"last_retrieved_at":          mutatedAt,
		"last_confirmed":             mutatedAt,
		"review_after":               mutatedAt,
		"valid_from":                 mutatedAt,
		"valid_until":                mutatedAt.Add(24 * time.Hour),
		"supersedes_id":              mutatedSupersedesID,
		"superseded_by":              mutatedSupersededBy,
		"importance_base":            0.11,
		"ts_alpha":                   8.25,
		"ts_beta":                    9.5,
		"confidence":                 0.21,
		"stability":                  5.5,
		"retrievability":             0.13,
		"version":                    mutatedVersion,
		"citation_count":             101,
		"injection_count":            102,
		"access_count":               103,
		"recurrence_count":           104,
		"consecutive_citation_count": 105,
	}).Error)

	require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		return store.RestoreRawTx(ctx, tx, &before)
	}))

	var restoredRow Memory
	require.NoError(t, db.Unscoped().Where("id = ?", row.ID).First(&restoredRow).Error)
	encodedRestored, err := json.Marshal(&restoredRow)
	require.NoError(t, err)
	var restored models.Memory
	require.NoError(t, json.Unmarshal(encodedRestored, &restored))

	before.Version = mutatedVersion
	require.Equal(t, before, restored,
		"rollback must restore the complete persisted snapshot; version alone stays monotonic to prevent ABA")
}
