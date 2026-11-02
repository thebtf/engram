package memorysearch

import (
	"context"

	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// LiteralStore supplies existing content-substring and tag matches to the text
// candidate leg. The shared hybrid engine still owns ranking and final filters.
type LiteralStore interface {
	SearchLiteralWithOffset(ctx context.Context, project, query string, limit, offset int) ([]*models.Memory, error)
}

type searchLiteralStore interface {
	SearchLiteralForSearch(context.Context, string, string, int, int, scope.MemoryReadAccess) ([]*models.Memory, error)
}

type textCandidates struct {
	retrieval.MemoryStoreInterface
	retrieval.SearchMemoryStore
	literal searchLiteralStore
	filter  *memoryFilter
	// The engine joins its FTS goroutine before Search reads this request-local error.
	literalErr error
}

func (s *textCandidates) SearchFTS(ctx context.Context, project, query string, limit int) ([]*models.Memory, error) {
	rows, _, err := s.SearchFTSForSearch(ctx, project, query, limit, scope.MemoryReadAccess{Caller: s.filter.caller, Visibility: s.filter.visibility})
	return rows, err
}

func (s *textCandidates) SearchFTSForSearch(ctx context.Context, project, query string, limit int, access scope.MemoryReadAccess) ([]*models.Memory, map[int64]bool, error) {
	fts, partial, ftsErr := s.SearchMemoryStore.SearchFTSForSearch(ctx, project, query, limit, access)
	literal, literalErr := s.literalCandidates(ctx, project, query, limit, access)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.literalErr = literalErr
	if literalErr != nil {
		return nil, nil, literalErr
	}
	visibleFTS := fts[:0]
	for _, memory := range fts {
		if s.filter.matches(memory) {
			visibleFTS = append(visibleFTS, memory)
		}
	}
	fts = visibleFTS
	if ftsErr != nil {
		return literal, nil, literalErr
	}
	if len(literal) == 0 {
		return fts, partial, nil
	}
	if len(fts) == 0 {
		return literal, nil, nil
	}

	// Reuse the existing rank-fusion authority so neither text source crowds out
	// the other before the same hybrid candidate pool is scored and filtered.
	ftsIDs := make([]int64, len(fts))
	literalIDs := make([]int64, len(literal))
	byID := make(map[int64]*models.Memory, len(fts)+len(literal))
	for i, memory := range fts {
		ftsIDs[i] = memory.ID
		byID[memory.ID] = memory
	}
	for i, memory := range literal {
		// A full content/tag literal match is stronger than OR-only recovery.
		delete(partial, memory.ID)
		literalIDs[i] = memory.ID
		byID[memory.ID] = memory
	}
	ids := retrieval.RRF(ftsIDs, literalIDs, 60)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	result := make([]*models.Memory, len(ids))
	for i, id := range ids {
		result[i] = byID[id]
	}
	return result, partial, nil
}

func (s *textCandidates) literalCandidates(ctx context.Context, project, query string, limit int, access scope.MemoryReadAccess) ([]*models.Memory, error) {
	const batchSize = 200
	var visible []*models.Memory
	for offset := 0; len(visible) < limit; {
		batch, err := s.literal.SearchLiteralForSearch(ctx, project, query, batchSize, offset, access)
		if err != nil {
			return nil, err
		}
		for _, memory := range batch {
			if s.filter.matches(memory) {
				visible = append(visible, memory)
				if len(visible) == limit {
					return visible, nil
				}
			}
		}
		if len(batch) < batchSize {
			break
		}
		offset += len(batch)
	}
	return visible, nil
}
