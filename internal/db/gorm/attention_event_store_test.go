package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/cognitive"
)

func TestAttentionEventStoreCreateGetListByProject(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	project := fmt.Sprintf("test-attention-event-%d", time.Now().UnixNano())
	otherProject := project + "-other"
	defer db.Exec(`DELETE FROM attention_events WHERE project IN (?, ?)`, project, otherProject)

	store := NewAttentionEventStore(db)
	ctx := context.Background()

	first, err := store.Create(ctx, cognitive.AttentionEventRecord{
		Project:        project,
		SessionID:      "session-a",
		SourceTurnHash: "sha256:first",
		DerivedIntent:  "keep release notes short",
		AgentConfirmed: true,
		Horizon:        "project",
		PrivacyClass:   "internal",
	})
	require.NoError(t, err)
	require.NotZero(t, first.ID)

	second, err := store.Create(ctx, cognitive.AttentionEventRecord{
		Project:        project,
		SessionID:      "session-b",
		SourceTurnHash: "sha256:second",
		DerivedIntent:  "never include raw creator turns",
		AgentConfirmed: true,
		Horizon:        "permanent",
		PrivacyClass:   "secret",
	})
	require.NoError(t, err)

	_, err = store.Create(ctx, cognitive.AttentionEventRecord{
		Project:        otherProject,
		SessionID:      "session-c",
		SourceTurnHash: "sha256:third",
		DerivedIntent:  "other project directive",
		AgentConfirmed: true,
		Horizon:        "session",
		PrivacyClass:   "public",
	})
	require.NoError(t, err)

	older := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	require.NoError(t, db.Model(&attentionEventRow{}).Where("id = ?", first.ID).Updates(map[string]any{"created_at": older, "updated_at": older}).Error)
	require.NoError(t, db.Model(&attentionEventRow{}).Where("id = ?", second.ID).Updates(map[string]any{"created_at": newer, "updated_at": newer}).Error)

	got, err := store.Get(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, project, got.Project)
	require.Equal(t, "session-b", got.SessionID)
	require.Equal(t, "sha256:second", got.SourceTurnHash)
	require.Equal(t, "never include raw creator turns", got.DerivedIntent)
	require.True(t, got.AgentConfirmed)
	require.Equal(t, "permanent", got.Horizon)
	require.Equal(t, "secret", got.PrivacyClass)

	listed, err := store.ListByProject(ctx, project, 10)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, second.ID, listed[0].ID, "newest project row must sort first")
	require.Equal(t, first.ID, listed[1].ID)
	for _, row := range listed {
		require.Equal(t, project, row.Project, "ListByProject must not leak rows from another project")
	}

	limited, err := store.ListByProject(ctx, project, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, second.ID, limited[0].ID)
}

func TestAttentionEventStoreRejectsInvalidRowsBeforeInsert(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	project := fmt.Sprintf("test-attention-event-invalid-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM attention_events WHERE project = ?`, project)
	store := NewAttentionEventStore(db)

	tests := []struct {
		name   string
		record cognitive.AttentionEventRecord
	}{
		{
			name: "missing project",
			record: cognitive.AttentionEventRecord{
				SessionID:      "session-a",
				SourceTurnHash: "sha256:first",
				DerivedIntent:  "keep release notes short",
				AgentConfirmed: true,
				Horizon:        "project",
				PrivacyClass:   "internal",
			},
		},
		{
			name: "missing source hash",
			record: cognitive.AttentionEventRecord{
				Project:        project,
				SessionID:      "session-a",
				DerivedIntent:  "keep release notes short",
				AgentConfirmed: true,
				Horizon:        "project",
				PrivacyClass:   "internal",
			},
		},
		{
			name: "invalid horizon",
			record: cognitive.AttentionEventRecord{
				Project:        project,
				SessionID:      "session-a",
				SourceTurnHash: "sha256:first",
				DerivedIntent:  "keep release notes short",
				AgentConfirmed: true,
				Horizon:        "forever",
				PrivacyClass:   "internal",
			},
		},
		{
			name: "invalid privacy class",
			record: cognitive.AttentionEventRecord{
				Project:        project,
				SessionID:      "session-a",
				SourceTurnHash: "sha256:first",
				DerivedIntent:  "keep release notes short",
				AgentConfirmed: true,
				Horizon:        "project",
				PrivacyClass:   "private-ish",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.Create(context.Background(), tt.record)
			require.Error(t, err)
		})
	}

	var count int64
	require.NoError(t, db.Model(&attentionEventRow{}).Where("project = ?", project).Count(&count).Error)
	require.Zero(t, count, "invalid rows must not create partial attention_events records")
}
