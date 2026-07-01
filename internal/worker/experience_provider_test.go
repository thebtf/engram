package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gormstore "github.com/thebtf/engram/internal/db/gorm"
	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

func TestMemoryExperienceProviderProjectsMemoryRows(t *testing.T) {
	store := &fakeExperienceMemoryStore{memories: []*models.Memory{{
		ID:             42,
		Project:        "engram",
		Content:        "OAuth retry once before cooldown prevented transient login failure.",
		Status:         "active",
		Domain:         "auth",
		SourceSessions: []string{"session-42"},
		CreatedAt:      time.Date(2026, time.July, 1, 5, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, time.July, 1, 5, 5, 0, 0, time.UTC),
	}}}
	provider := newMemoryExperienceProvider(store)

	results, err := provider.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Domain:         "auth",
		Principal:      "agent/omp",
		Query:          "OAuth retry cooldown",
		CurrentContext: "transient login failure",
		Limit:          5,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "engram", store.project)
	require.Equal(t, "auth", store.opts.Domain)
	require.Equal(t, "agent/omp", store.opts.OwnerPrincipal)
	require.Equal(t, experienceProjectionFetchLimit, store.opts.Limit)
	require.Equal(t, cognitive.ExperienceSourceProjection, results[0].StorageOrigin)
	require.Equal(t, "OAuth retry once before cooldown prevented transient login failure.", results[0].Lesson)
	require.Equal(t, "memory", results[0].SourceAttribution[0].Kind)
	require.Equal(t, "42", results[0].SourceAttribution[0].ID)
	require.Equal(t, "session-42", results[0].SourceAttribution[0].SessionID)
}

func TestMemoryExperienceProviderDetailUsesProjectedMemoryRows(t *testing.T) {
	store := &fakeExperienceMemoryStore{memories: []*models.Memory{{
		ID:             7,
		Project:        "engram",
		Content:        "Stored memory detail should surface through the provider seam.",
		Status:         "active",
		SourceSessions: []string{"detail-session"},
		CreatedAt:      time.Date(2026, time.July, 1, 6, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, time.July, 1, 6, 5, 0, 0, time.UTC),
	}}}
	provider := newMemoryExperienceProvider(store)

	detail, evidence, found, err := provider.QueryExperienceDetail(context.Background(), experiencehistory.HistoryDetailRequest{
		Project:      "engram",
		ExperienceID: "memory:7",
	})

	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, evidence)
	require.Equal(t, "memory:7", detail.SourceAttribution[0].Kind+":"+detail.SourceAttribution[0].ID)
}

type fakeExperienceMemoryStore struct {
	memories []*models.Memory
	project  string
	opts     gormstore.ListOptions
}

func (f *fakeExperienceMemoryStore) ListPrincipalMemory(_ context.Context, project string, opts gormstore.ListOptions) ([]*models.Memory, error) {
	f.project = project
	f.opts = opts
	return f.memories, nil
}
