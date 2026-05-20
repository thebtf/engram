package injection

import (
	"context"

	"github.com/rs/zerolog/log"

	gormstore "github.com/thebtf/engram/internal/db/gorm"
)

// Tracker records injection events and updates memory injection counts.
type Tracker struct {
	injectionLogStore *gormstore.InjectionLogStore
	memoryStore       *gormstore.MemoryStore
}

// NewTracker creates a new injection Tracker.
func NewTracker(injLog *gormstore.InjectionLogStore, memStore *gormstore.MemoryStore) *Tracker {
	return &Tracker{
		injectionLogStore: injLog,
		memoryStore:       memStore,
	}
}

// Track records the injection of selected memories for a session.
// It writes to injection_log and increments each memory's injection_count.
// Errors are logged but do not block the injection response (graceful degradation).
func (t *Tracker) Track(ctx context.Context, sessionID, project string, selected []ScoredMemory) {
	if len(selected) == 0 {
		return
	}

	ids := make([]int64, len(selected))
	for i, s := range selected {
		ids[i] = s.Memory.ID
	}

	// Record to injection_log (append-only)
	if err := t.injectionLogStore.Record(ctx, sessionID, project, ids); err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Msg("failed to record injection log")
		return // if log fails, skip count increment too
	}

	// Increment injection_count on each memory
	// This is best-effort — failure doesn't block injection
	for _, id := range ids {
		if err := t.memoryStore.IncrementInjectionCount(ctx, id); err != nil {
			log.Error().Err(err).Int64("memory_id", id).Msg("failed to increment injection_count")
		}
	}
}
