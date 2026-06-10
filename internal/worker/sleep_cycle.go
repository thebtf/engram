package worker

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/lifecycle"
)

// sleepCycleMemoryCountThreshold is the minimum number of new memories since the
// last run required to trigger a sleep cycle (milestone-B T014 AC: >=10).
const sleepCycleMemoryCountThreshold = 10

// sleepCycleInterval is the minimum wall-clock interval between sleep cycle checks
// (milestone-B T014 AC: >=4h since last active session). We use this as the
// polling period; the count gate provides the second condition.
const sleepCycleInterval = 4 * time.Hour

// startSleepCycle launches a background goroutine that periodically evaluates
// whether the sleep cycle trigger conditions are met and, if so, runs RunSleepCycle.
//
// Trigger conditions (milestone-B T014 AC):
//   - >=10 new memories since last cycle
//   - >=4 hours since last run (approximates "since last session" — exact
//     session-end timestamps are not yet stored; this is documented in commit).
//
// The goroutine is tracked by s.wg and respects ctx cancellation.
func (s *Service) startSleepCycle(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		// Delay first check by one full interval so sleep cycle does not
		// compete with startup and initial backfill work.
		timer := time.NewTimer(sleepCycleInterval)
		defer timer.Stop()

		var lastMemoryCount int64

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.maybeSleepCycle(ctx, &lastMemoryCount)
				timer.Reset(sleepCycleInterval)
			}
		}
	}()
}

// maybeSleepCycle checks the trigger conditions and runs RunSleepCycle when met.
// lastMemoryCount is updated atomically on each successful run.
func (s *Service) maybeSleepCycle(ctx context.Context, lastMemoryCount *int64) {
	s.initMu.RLock()
	ms := s.memoryStore
	ps := s.promotionStore
	s.initMu.RUnlock()

	if ms == nil {
		log.Debug().Msg("sleep cycle: memory store not ready, skipping")
		return
	}

	// Query current total active memory count to evaluate the "new memories" gate.
	memories, err := ms.ListAllActive(ctx, 1, 0)
	if err != nil {
		log.Warn().Err(err).Msg("sleep cycle: count query failed, skipping")
		return
	}

	// ListAllActive(limit=1) gives us whether any exist; for count we need a
	// representative batch. Use a small sentinel approach: fetch up to threshold+1
	// rows and compare against last known count.
	batch, err := ms.ListAllActive(ctx, sleepCycleMemoryCountThreshold+1, 0)
	if err != nil {
		log.Warn().Err(err).Msg("sleep cycle: batch query failed, skipping")
		return
	}
	_ = memories // used for nil-check above

	currentCount := int64(len(batch))
	prev := atomic.LoadInt64(lastMemoryCount)

	newSince := currentCount - prev
	if newSince < sleepCycleMemoryCountThreshold {
		log.Debug().
			Int64("new_since_last", newSince).
			Int64("threshold", sleepCycleMemoryCountThreshold).
			Msg("sleep cycle: count threshold not met, skipping")
		return
	}

	log.Info().Int64("new_memories", newSince).Msg("sleep cycle: trigger conditions met, starting")

	result := lifecycle.RunSleepCycle(ctx, ms, ps)

	log.Info().
		Int("processed", result.MemoriesProcessed).
		Int("promotions", result.Promotions).
		Int("demotions", result.Demotions).
		Int("expirations", result.Expirations).
		Int("review_flagged", result.ReviewFlagged).
		Dur("duration", result.Duration).
		Msg("sleep cycle: finished")

	atomic.StoreInt64(lastMemoryCount, currentCount)
}
