package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

func TestMemoryExperienceProviderUsesPrincipalPolicyQuery(t *testing.T) {
	store := &fakeExperienceMemoryStore{memories: []*models.Memory{
		{
			ID:                 42,
			Project:            "engram",
			Content:            "OAuth retry once before cooldown prevented transient login failure.",
			Status:             "active",
			Domain:             "auth",
			OwnerPrincipal:     "agent/omp",
			OwnerPrincipalKind: "agent",
			AgentVisibility:    models.AgentVisibilityPrivate,
			SourceSessions:     []string{"session-42"},
			CreatedAt:          time.Date(2026, time.July, 1, 5, 0, 0, 0, time.UTC),
			UpdatedAt:          time.Date(2026, time.July, 1, 5, 5, 0, 0, time.UTC),
		},
		{
			ID:                 99,
			Project:            "engram",
			Content:            "Another principal private memory must stay hidden.",
			Status:             "active",
			Domain:             "auth",
			OwnerPrincipal:     "agent/other",
			OwnerPrincipalKind: "agent",
			AgentVisibility:    models.AgentVisibilityPrivate,
			SourceSessions:     []string{"session-99"},
			CreatedAt:          time.Date(2026, time.July, 1, 5, 10, 0, 0, time.UTC),
			UpdatedAt:          time.Date(2026, time.July, 1, 5, 15, 0, 0, time.UTC),
		},
	}}
	provider := newMemoryExperienceProvider(principalmemory.NewPrincipalMemoryQueryService(store, nil))
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-1", "agent/omp", auth.PrincipalKindAgent))

	results, err := provider.QueryExperience(ctx, cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Domain:         "auth",
		Query:          "OAuth retry cooldown",
		CurrentContext: "transient login failure",
		Limit:          5,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "engram", store.project)
	require.Equal(t, "auth", store.opts.Domain)
	require.Equal(t, experienceProjectionFetchLimit+50, store.opts.Limit)
	require.Empty(t, store.opts.ContentContains)
	require.Equal(t, cognitive.ExperienceSourceProjection, results[0].StorageOrigin)
	require.Equal(t, "OAuth retry once before cooldown prevented transient login failure.", results[0].Lesson)
	require.Equal(t, "memory", results[0].SourceAttribution[0].Kind)
	require.Equal(t, "42", results[0].SourceAttribution[0].ID)
	require.Equal(t, "session-42", results[0].SourceAttribution[0].SessionID)
}

func TestMemoryExperienceProviderDetailUsesPrincipalQueryResult(t *testing.T) {
	store := &fakeExperienceMemoryStore{memories: []*models.Memory{{
		ID:                 7,
		Project:            "engram",
		Content:            "Stored memory detail should surface through the provider seam.",
		Status:             "active",
		OwnerPrincipal:     "agent/omp",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityPrivate,
		SourceSessions:     []string{"detail-session"},
		CreatedAt:          time.Date(2026, time.July, 1, 6, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, time.July, 1, 6, 5, 0, 0, time.UTC),
	}}}
	provider := newMemoryExperienceProvider(principalmemory.NewPrincipalMemoryQueryService(store, nil))
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-1", "agent/omp", auth.PrincipalKindAgent))

	detail, evidence, found, err := provider.QueryExperienceDetail(ctx, experiencehistory.HistoryDetailRequest{
		Project:      "engram",
		ExperienceID: "memory:7",
	})

	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, evidence)
	require.Equal(t, "engram", store.project)
	require.Empty(t, store.opts.ContentContains)
	require.Equal(t, "memory:7", detail.SourceAttribution[0].Kind+":"+detail.SourceAttribution[0].ID)
	require.Equal(t, "detail-session", detail.SourceAttribution[0].SessionID)
}

type fakeExperienceMemoryStore struct {
	memories []*models.Memory
	project  string
	opts     gormstore.ListOptions
}

func (f *fakeExperienceMemoryStore) ListPrincipalMemory(_ context.Context, project string, opts gormstore.ListOptions) ([]*models.Memory, error) {
	f.project = project
	f.opts = opts
	if opts.ContentContains == "" {
		return f.memories, nil
	}
	filtered := make([]*models.Memory, 0, len(f.memories))
	for _, memory := range f.memories {
		if memory != nil && strings.Contains(strings.ToLower(memory.Content), strings.ToLower(opts.ContentContains)) {
			filtered = append(filtered, memory)
		}
	}
	return filtered, nil
}
