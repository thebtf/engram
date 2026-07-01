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

// TestMemoryExperienceProviderDetailByIDFetchesOlderThanProjection proves a
// memory:<id> detail lookup fetches that exact row by id (additive IN filter)
// rather than scanning the newest-N window, so a target older than the fetch
// limit is still retrievable.
func TestMemoryExperienceProviderDetailByIDFetchesOlderThanProjection(t *testing.T) {
	target := &models.Memory{
		ID:                 3,
		Project:            "engram",
		Content:            "Ancient lesson older than the newest projection window.",
		Status:             "active",
		OwnerPrincipal:     "agent/omp",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityPrivate,
		SourceSessions:     []string{"old-session"},
		CreatedAt:          time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2020, time.January, 1, 0, 5, 0, 0, time.UTC),
	}
	newer := &models.Memory{
		ID:                 500,
		Project:            "engram",
		Content:            "Recent lesson inside the projection window.",
		Status:             "active",
		OwnerPrincipal:     "agent/omp",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityPrivate,
		CreatedAt:          time.Date(2026, time.July, 1, 6, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, time.July, 1, 6, 0, 0, 0, time.UTC),
	}
	store := &fakeExperienceMemoryStore{memories: []*models.Memory{target, newer}}
	provider := newMemoryExperienceProvider(principalmemory.NewPrincipalMemoryQueryService(store, nil))
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-1", "agent/omp", auth.PrincipalKindAgent))

	detail, _, found, err := provider.QueryExperienceDetail(ctx, experiencehistory.HistoryDetailRequest{
		Project:      "engram",
		ExperienceID: "memory:3",
	})

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []int64{3}, store.opts.IDs, "detail-by-id must pass an additive id filter")
	require.Empty(t, store.opts.ContentContains, "detail-by-id must not add a full-phrase prefilter")
	require.Empty(t, store.opts.ContentContainsAny, "detail-by-id must not add term narrowing")
	require.Equal(t, "3", detail.SourceAttribution[0].ID)
	require.Equal(t, "Ancient lesson older than the newest projection window.", detail.Lesson)
}

// TestMemoryExperienceProviderGeneralQueryRecallsByTerm proves the general query
// path narrows by ORed content terms (recall-preserving) instead of a full-phrase
// prefilter: an older memory matching a single query TERM but not the whole
// phrase is still surfaced, and no ContentContains cliff is applied.
func TestMemoryExperienceProviderGeneralQueryRecallsByTerm(t *testing.T) {
	older := &models.Memory{
		ID:                 11,
		Project:            "engram",
		Content:            "Cooldown tuning avoided a thundering-herd reconnect storm.",
		Status:             "active",
		OwnerPrincipal:     "agent/omp",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		CreatedAt:          time.Date(2021, time.March, 3, 0, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2021, time.March, 3, 0, 0, 0, 0, time.UTC),
	}
	store := &fakeExperienceMemoryStore{memories: []*models.Memory{older}}
	provider := newMemoryExperienceProvider(principalmemory.NewPrincipalMemoryQueryService(store, nil))
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-1", "agent/omp", auth.PrincipalKindAgent))

	results, err := provider.QueryExperience(ctx, cognitive.ExperienceQueryRequest{
		Project: "engram",
		Query:   "cooldown backoff strategy",
	})

	require.NoError(t, err)
	require.Len(t, results, 1, "a term match (cooldown) must surface the older lesson")
	require.Empty(t, store.opts.ContentContains, "must not apply the full-phrase prefilter")
	require.Contains(t, store.opts.ContentContainsAny, "cooldown", "query terms must be ORed at the SQL layer")
	require.Equal(t, "Cooldown tuning avoided a thundering-herd reconnect storm.", results[0].Lesson)
}

// TestMemoryExperienceProviderGatingHoldsWithNewFilters proves NFR-1 is intact:
// with the additive id/term filters present, a private memory owned by a
// different principal is still hidden by the access policy on both paths.
func TestMemoryExperienceProviderGatingHoldsWithNewFilters(t *testing.T) {
	foreign := &models.Memory{
		ID:                 21,
		Project:            "engram",
		Content:            "Cooldown secret owned by another agent, private.",
		Status:             "active",
		OwnerPrincipal:     "agent/other",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityPrivate,
		CreatedAt:          time.Date(2026, time.July, 1, 6, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, time.July, 1, 6, 0, 0, 0, time.UTC),
	}
	store := &fakeExperienceMemoryStore{memories: []*models.Memory{foreign}}
	provider := newMemoryExperienceProvider(principalmemory.NewPrincipalMemoryQueryService(store, nil))
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-1", "agent/omp", auth.PrincipalKindAgent))

	results, err := provider.QueryExperience(ctx, cognitive.ExperienceQueryRequest{
		Project: "engram",
		Query:   "cooldown secret",
	})
	require.NoError(t, err)
	require.Empty(t, results, "another principal's private memory must stay hidden on the term path")

	_, _, found, err := provider.QueryExperienceDetail(ctx, experiencehistory.HistoryDetailRequest{
		Project:      "engram",
		ExperienceID: "memory:21",
	})
	require.NoError(t, err)
	require.False(t, found, "detail-by-id must not disclose another principal's private memory")
}

type fakeExperienceMemoryStore struct {
	memories []*models.Memory
	project  string
	opts     gormstore.ListOptions
}

func (f *fakeExperienceMemoryStore) ListPrincipalMemory(_ context.Context, project string, opts gormstore.ListOptions) ([]*models.Memory, error) {
	f.project = project
	f.opts = opts
	filtered := make([]*models.Memory, 0, len(f.memories))
	for _, memory := range f.memories {
		if memory == nil {
			continue
		}
		if len(opts.IDs) > 0 && !containsID(opts.IDs, memory.ID) {
			continue
		}
		if opts.ContentContains != "" &&
			!strings.Contains(strings.ToLower(memory.Content), strings.ToLower(opts.ContentContains)) {
			continue
		}
		if len(opts.ContentContainsAny) > 0 && !matchesAnyTerm(memory.Content, opts.ContentContainsAny) {
			continue
		}
		filtered = append(filtered, memory)
	}
	return filtered, nil
}

func containsID(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func matchesAnyTerm(content string, terms []string) bool {
	lower := strings.ToLower(content)
	for _, term := range terms {
		if t := strings.TrimSpace(strings.ToLower(term)); t != "" && strings.Contains(lower, t) {
			return true
		}
	}
	return false
}
