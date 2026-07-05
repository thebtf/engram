package s6

import (
	"context"
	"fmt"

	"github.com/thebtf/engram/pkg/models"
)

// injectionCandidateStore is the concrete bounded-read seam S6 reuses from the
// existing memory store. It stays local so proposer tests remain decoupled from
// database implementation details.
type injectionCandidateStore interface {
	ListForInjection(ctx context.Context, project string, limit int) ([]*models.Memory, error)
}

type memoryStoreAdapter struct {
	store injectionCandidateStore
}

func NewMemoryStoreAdapter(store injectionCandidateStore) OutcomeStore {
	return &memoryStoreAdapter{store: store}
}

func (a *memoryStoreAdapter) ListOutcomeCandidates(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	if a == nil || a.store == nil {
		return nil, fmt.Errorf("outcome memory store is not configured")
	}
	return a.store.ListForInjection(ctx, project, limit)
}
