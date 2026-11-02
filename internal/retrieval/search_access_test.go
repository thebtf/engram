package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

type searchAccessTestStore struct {
	mockMemStore
	current    map[int64]*models.Memory
	loads      int
	beforeLoad func(int)
	partialFTS map[int64]bool
}

func (s *searchAccessTestStore) ListForSearch(context.Context, string, int, scope.MemoryReadAccess) ([]*models.Memory, error) {
	return s.listResults, nil
}

func (s *searchAccessTestStore) SearchFTSForSearch(context.Context, string, string, int, scope.MemoryReadAccess) ([]*models.Memory, map[int64]bool, error) {
	return s.ftsResults, s.partialFTS, nil
}

func (s *searchAccessTestStore) GetByRefsForSearch(_ context.Context, project string, refs []scope.MemoryRef, access scope.MemoryReadAccess) ([]*models.Memory, error) {
	s.loads++
	if s.beforeLoad != nil {
		s.beforeLoad(s.loads)
	}
	var rows []*models.Memory
	for _, ref := range refs {
		memory := s.current[ref.ID]
		if ref.Matches(memory) && memory.Project == project && scope.ResolveMemory(access.Caller, memory, access.Visibility) {
			rows = append(rows, memory)
		}
	}
	return rows, nil
}

func TestHybridSearch_MaterializationRevalidatesBodyAndACL(t *testing.T) {
	for _, mutation := range []string{"same_version_body_replacement", "principal_acl_revocation"} {
		for _, phase := range []string{"before_rerank", "during_rerank"} {
			t.Run(mutation+"/"+phase, func(t *testing.T) {
				access := scope.MemoryReadAccess{
					Caller:     scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"},
					Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true},
				}
				var selected []*models.Memory
				for index, body := range []string{"selected body A", "visible control north", "visible control south"} {
					selected = append(selected, &models.Memory{
						ID: int64(index + 1), Version: 3, Project: "project", Content: body,
						PrivacyScope: "project", OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "agent", AgentVisibility: "private",
						CreatedAt: time.Now().UTC(), ImportanceBase: 0.5,
					})
				}
				store := &searchAccessTestStore{
					mockMemStore: mockMemStore{ftsResults: selected},
					current:      make(map[int64]*models.Memory),
				}
				for _, memory := range selected {
					store.current[memory.ID] = memory
				}
				mutate := func() {
					replacement := *selected[0]
					if mutation == "same_version_body_replacement" {
						replacement.Content = "replacement body B"
					} else {
						replacement.OwnerPrincipal = "agent/bob"
					}
					require.Equal(t, selected[0].Version, replacement.Version)
					store.current[replacement.ID] = &replacement
				}
				if phase == "before_rerank" {
					store.beforeLoad = func(load int) {
						if load == 2 {
							mutate()
						}
					}
				}
				var observed []string
				encoder := &stubCrossEncoder{rankByIndex: func(passages []string) []RerankResult {
					observed = append([]string(nil), passages...)
					if phase == "during_rerank" {
						mutate()
					}
					result := make([]RerankResult, len(passages))
					for index := range passages {
						result[index] = RerankResult{Index: index, RelevanceScore: 1}
					}
					return result
				}}
				results, explanations, err := HybridSearch(t.Context(), "project", "search query", 3, store, nil, nil, HybridOptions{
					Access: &access, SkipTier0: true, TierFilter: []string{"tier1_fts"}, Reranker: encoder, Explain: true,
				})
				require.NoError(t, err)
				require.True(t, encoder.called, "authorized controls must keep actual reranking active")
				require.Contains(t, observed, "visible control north")
				require.Contains(t, observed, "visible control south")
				require.NotContains(t, observed, "replacement body B", "a reused version cannot substitute a new provider body")
				if phase == "before_rerank" {
					require.NotContains(t, observed, "selected body A", "changed or revoked candidates must disappear before provider dispatch")
				} else {
					require.Contains(t, observed, "selected body A", "the selected authorized body was still current at provider dispatch")
				}
				require.Len(t, results, 2)
				require.Len(t, explanations, 2)
				for _, result := range results {
					require.NotEqual(t, int64(1), result.Memory.ID, "changed or revoked candidates cannot survive final materialization")
				}
				for _, explanation := range explanations {
					require.NotEqual(t, int64(1), explanation.MemoryID)
				}
			})
		}
	}
}

type searchAccessTestEmbeddingStore struct {
	mockEmbStore
}

func (s *searchAccessTestEmbeddingStore) FindSimilarForSearch(context.Context, string, []float32, int, float64, scope.MemoryReadAccess) ([]embedding.SimilarResult, error) {
	return s.results, nil
}

func TestHybridSearch_PartialFTSPreservesSemanticMatch(t *testing.T) {
	for _, test := range []struct {
		name            string
		partial, vector bool
		firstID         int64
	}{
		{"or_recovery_with_vector", true, true, 1},
		{"strict_fts_keeps_existing_strength", false, true, 3},
		{"or_recovery_without_vector_still_returns_matches", true, false, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			semantic := &models.Memory{ID: 1, Version: 3, Project: "project", Content: "authorized semantic match", PrivacyScope: "project", CreatedAt: now, ImportanceBase: 0.5}
			lexical := &models.Memory{ID: 3, Version: 1, Project: "project", Content: "authorized lexical match", PrivacyScope: "project", CreatedAt: now, ImportanceBase: 0.5}
			store := &searchAccessTestStore{
				mockMemStore: mockMemStore{ftsResults: []*models.Memory{lexical, semantic}},
				current:      map[int64]*models.Memory{1: semantic, 3: lexical},
			}
			if test.partial {
				store.partialFTS = map[int64]bool{1: true, 3: true}
			}
			var vectors EmbeddingStoreInterface
			if test.vector {
				vectors = &searchAccessTestEmbeddingStore{mockEmbStore{results: []embedding.SimilarResult{{
					MemoryID: semantic.ID, Version: semantic.Version, ContentDigest: scope.MemoryContentDigest(semantic.Content), Similarity: 0.8,
				}}}}
			}
			access := scope.MemoryReadAccess{Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}}
			results, _, err := HybridSearch(t.Context(), "project", "multi term query", 2, store, vectors, nil, HybridOptions{
				Access: &access, SkipTier0: true, QueryVec: []float32{1},
			})
			require.NoError(t, err)
			require.Len(t, results, 2, "partial lexical recovery must remain active")
			require.Equal(t, test.firstID, results[0].Memory.ID)
		})
	}
}

func TestHybridSearch_ConflictingLegsKeepOnlyCurrentIdentityEvidence(t *testing.T) {
	for _, test := range []struct {
		name, current string
		vectorVersion int
		firstID       int64
		matches       int
		tier          string
	}{
		{"same_version_current_fts", "fts", 3, 2, 2, "tier1_fts"},
		{"same_version_current_vector", "vector", 3, 1, 2, "tier1_vector"},
		{"different_version_current_vector", "vector", 4, 1, 2, "tier1_vector"},
		{"neither_selected_identity_is_current", "neither", 3, 2, 1, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			fts := &models.Memory{ID: 1, Version: 3, Project: "project", Content: "selected lexical body A", PrivacyScope: "project", CreatedAt: now, ImportanceBase: 0.5}
			vector := *fts
			vector.Version, vector.Content = test.vectorVersion, "selected vector body B"
			control := &models.Memory{ID: 2, Version: 1, Project: "project", Content: "current vector control", PrivacyScope: "project", CreatedAt: now, ImportanceBase: 0.5}
			current := fts
			switch test.current {
			case "vector":
				current = &vector
			case "neither":
				replacement := *fts
				replacement.Content = "unselected current body C"
				current = &replacement
			}
			store := &searchAccessTestStore{
				mockMemStore: mockMemStore{ftsResults: []*models.Memory{fts}},
				current:      map[int64]*models.Memory{1: current, 2: control},
			}
			vectors := &searchAccessTestEmbeddingStore{mockEmbStore{results: []embedding.SimilarResult{
				{MemoryID: vector.ID, Version: vector.Version, ContentDigest: scope.MemoryContentDigest(vector.Content), Similarity: 1.0},
				{MemoryID: control.ID, Version: control.Version, ContentDigest: scope.MemoryContentDigest(control.Content), Similarity: 0.95},
			}}}
			access := scope.MemoryReadAccess{Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}}
			results, explanations, err := HybridSearch(t.Context(), "project", "query", 2, store, vectors, nil, HybridOptions{
				Access: &access, SkipTier0: true, QueryVec: []float32{1}, Explain: true,
			})
			require.NoError(t, err)
			require.Len(t, results, test.matches)
			require.Equal(t, test.firstID, results[0].Memory.ID)
			for _, result := range results {
				if result.Memory.ID == 1 {
					require.Equal(t, current.Content, result.Memory.Content, "retain the supported current identity, not a fixed leg preference")
					if test.current == "fts" {
						require.Less(t, result.Relevance, 0.95, "body A must not inherit body B's vector similarity")
					}
				}
			}
			for _, explanation := range explanations {
				if explanation.MemoryID == 1 {
					require.Equal(t, test.tier, explanation.SourceTier, "only the current identity's leg may supply evidence")
				}
			}
		})
	}
}

func TestHybridSearch_ConflictingLegsCannotStarveCurrentCandidate(t *testing.T) {
	now := time.Now().UTC()
	control := &models.Memory{ID: 100, Version: 1, Project: "project", Content: "supported current control", PrivacyScope: "project", CreatedAt: now, ImportanceBase: 0.5}
	store := &searchAccessTestStore{current: map[int64]*models.Memory{control.ID: control}}
	vectorRows := []embedding.SimilarResult{{MemoryID: control.ID, Version: control.Version, ContentDigest: scope.MemoryContentDigest(control.Content), Similarity: 1.0}}
	// Six mismatched both-leg IDs would crowd this one-leg control out of the
	// limit-one pipeline's five-candidate hydration cap if fused before reconciliation.
	for id := int64(1); id <= 6; id++ {
		fts := &models.Memory{ID: id, Version: 3, Project: "project", Content: "selected lexical body", PrivacyScope: "project", CreatedAt: now, ImportanceBase: 0.5}
		store.ftsResults = append(store.ftsResults, fts)
		vectorRows = append(vectorRows, embedding.SimilarResult{MemoryID: id, Version: 3, ContentDigest: scope.MemoryContentDigest("selected vector body"), Similarity: 0.99})
		replacement := *fts
		replacement.Content = "unselected current body"
		store.current[id] = &replacement
	}
	vectors := &searchAccessTestEmbeddingStore{mockEmbStore{results: vectorRows}}
	access := scope.MemoryReadAccess{Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}}
	results, _, err := HybridSearch(t.Context(), "project", "query", 1, store, vectors, nil, HybridOptions{
		Access: &access, SkipTier0: true, QueryVec: []float32{1},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, control.ID, results[0].Memory.ID, "mismatched evidence cannot consume the pre-hydration cap")
}
