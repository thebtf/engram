package memorysearch

import (
	"context"

	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/pkg/models"
)

// LiteralStore supplies existing content-substring and tag matches to the text
// candidate leg. The shared hybrid engine still owns ranking and final filters.
type LiteralStore interface {
	SearchLiteralWithOffset(ctx context.Context, project, query string, limit, offset int) ([]*models.Memory, error)
}

type textCandidates struct {
	retrieval.MemoryStoreInterface
	literal LiteralStore
	filter  *memoryFilter
	// The engine joins its FTS goroutine before Search reads this request-local error.
	literalErr error
}

func (s *textCandidates) SearchFTS(ctx context.Context, project, query string, limit int) ([]*models.Memory, error) {
	fts, ftsErr := s.MemoryStoreInterface.SearchFTS(ctx, project, query, limit)
	literal, literalErr := s.literalCandidates(ctx, project, query, limit)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.literalErr = literalErr
	if literalErr != nil {
		return nil, literalErr
	}
	visibleFTS := fts[:0]
	for _, memory := range fts {
		if s.filter.matches(memory) {
			visibleFTS = append(visibleFTS, memory)
		}
	}
	fts = visibleFTS
	if ftsErr != nil {
		return literal, literalErr
	}
	if len(literal) == 0 {
		return fts, nil
	}
	if len(fts) == 0 {
		return literal, nil
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
	return result, nil
}

func (s *textCandidates) literalCandidates(ctx context.Context, project, query string, limit int) ([]*models.Memory, error) {
	const batchSize = 200
	var visible []*models.Memory
	for offset := 0; len(visible) < limit; {
		batch, err := s.literal.SearchLiteralWithOffset(ctx, project, query, batchSize, offset)
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
