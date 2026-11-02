package retrieval

import (
	"context"

	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// SearchMemoryStore applies caller policy in each candidate query, before its
// ordering/limit, and rechecks version, exact body digest and ACLs during hydration.
// Public search must fail closed when the underlying reader lacks this contract.
type SearchMemoryStore interface {
	ListForSearch(context.Context, string, int, scope.MemoryReadAccess) ([]*models.Memory, error)
	// The ID set marks OR-only recovery evidence, not strict or full literal hits.
	SearchFTSForSearch(context.Context, string, string, int, scope.MemoryReadAccess) ([]*models.Memory, map[int64]bool, error)
	GetByRefsForSearch(context.Context, string, []scope.MemoryRef, scope.MemoryReadAccess) ([]*models.Memory, error)
}

// SearchEmbeddingStore returns authorized version-and-digest-pinned candidates.
type SearchEmbeddingStore interface {
	FindSimilarForSearch(context.Context, string, []float32, int, float64, scope.MemoryReadAccess) ([]embedding.SimilarResult, error)
}

// SearchGraphStore bounds the authorized union of one-hop memory neighbors,
// excluding the existing fused pool before the shared twenty-neighbor cap.
type SearchGraphStore interface {
	NeighborsForSearch(context.Context, string, []int64, []int64, int, scope.MemoryReadAccess) ([]scope.MemoryRef, error)
}

func searchMemoryMatches(memory *models.Memory, project string, ref scope.MemoryRef, access scope.MemoryReadAccess) bool {
	return ref.Matches(memory) && memory.Project == project &&
		scope.ResolveMemory(access.Caller, memory, access.Visibility)
}

func revalidateSearchResults(ctx context.Context, project string, scored []ScoredMemory, store SearchMemoryStore, access scope.MemoryReadAccess) ([]ScoredMemory, error) {
	if len(scored) == 0 {
		return scored, nil
	}
	refs := make([]scope.MemoryRef, len(scored))
	for i, candidate := range scored {
		refs[i] = scope.MemoryReference(candidate.Memory)
	}
	rows, err := store.GetByRefsForSearch(ctx, project, refs, access)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*models.Memory, len(rows))
	for _, memory := range rows {
		byID[memory.ID] = memory
	}
	visible := scored[:0]
	for i, candidate := range scored {
		memory := byID[candidate.Memory.ID]
		if searchMemoryMatches(memory, project, refs[i], access) {
			candidate.Memory = memory
			visible = append(visible, candidate)
		}
	}
	return visible, nil
}

// reconcileSearchLegs keeps evidence attached to its exact selected identity.
// Only conflicting IDs require an extra current-policy read. A current FTS or
// vector reference survives on its own; neither receives the other's rank credit.
func reconcileSearchLegs(
	ctx context.Context, project string, store SearchMemoryStore, access scope.MemoryReadAccess,
	ftsIDs, vecIDs []int64, ftsRefs, vecRefs map[int64]scope.MemoryRef, vecSims map[int64]float64,
) ([]int64, []int64, error) {
	var conflicts []scope.MemoryRef
	for _, id := range ftsIDs {
		ftsRef := ftsRefs[id]
		if vecRef, ok := vecRefs[id]; ok && ftsRef != vecRef {
			if conflicts == nil {
				conflicts = make([]scope.MemoryRef, 0, 2*min(len(ftsIDs), len(vecIDs)))
			}
			conflicts = append(conflicts, ftsRef, vecRef)
		}
	}
	if len(conflicts) == 0 {
		return ftsIDs, vecIDs, nil
	}
	rows, err := store.GetByRefsForSearch(ctx, project, conflicts, access)
	if err != nil {
		return nil, nil, err
	}
	current := make(map[int64]scope.MemoryRef, len(rows))
	for _, memory := range rows {
		if memory == nil || memory.Project != project || !scope.ResolveMemory(access.Caller, memory, access.Visibility) {
			continue
		}
		ref := scope.MemoryReference(memory)
		if ref == ftsRefs[memory.ID] || ref == vecRefs[memory.ID] {
			current[memory.ID] = ref
		}
	}
	for index := 0; index < len(conflicts); index += 2 {
		ftsRef, vecRef := conflicts[index], conflicts[index+1]
		ref, ok := current[ftsRef.ID]
		if !ok || ref != ftsRef {
			delete(ftsRefs, ftsRef.ID)
		}
		if !ok || ref != vecRef {
			delete(vecRefs, vecRef.ID)
			delete(vecSims, vecRef.ID)
		}
	}
	return retainSearchEvidenceIDs(ftsIDs, ftsRefs), retainSearchEvidenceIDs(vecIDs, vecRefs), nil
}

func retainSearchEvidenceIDs(ids []int64, refs map[int64]scope.MemoryRef) []int64 {
	retained := ids[:0]
	for _, id := range ids {
		if _, ok := refs[id]; ok {
			retained = append(retained, id)
		}
	}
	return retained
}
