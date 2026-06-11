package worker

import (
	"context"
	"os"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/lifecycle"
	"github.com/thebtf/engram/pkg/models"
)

// sleepCycleMemoryCountThreshold is the minimum number of new memories since the
// last cycle required to trigger a sleep cycle (milestone-B T014 AC: >=10).
const sleepCycleMemoryCountThreshold = 10

// sleepCycleInterval is the polling period for the sleep cycle background goroutine.
// The goroutine wakes every interval and re-evaluates both trigger conditions.
const sleepCycleInterval = 4 * time.Hour

// sleepCycleIdleGate is the minimum time since the last HTTP/MCP request before
// the sleep cycle may run (milestone-B T014 AC: ">=4h since last active session").
// Set equal to sleepCycleInterval: we only run when the server has been idle for
// at least as long as the polling period.
const sleepCycleIdleGate = 4 * time.Hour

// startSleepCycle launches a background goroutine that periodically evaluates
// whether the sleep cycle trigger conditions are met and, if so, runs RunSleepCycle.
//
// Trigger conditions (milestone-B T014 AC):
//   - >=10 new active memories created since the last completed cycle
//   - >=4 hours since the last HTTP/MCP request (idle gate)
//
// Idle gate signal: s.lastRequestAt holds the Unix nanosecond timestamp of the
// most recent request stamped by requestActivityMiddleware. Zero (server restart
// with no requests yet) is treated as "idle since epoch" — in that case only the
// count gate determines the first cycle.
//
// Watermark (Finding 1): s.sleepCycleWatermarkID records the max memory ID seen at
// the end of the last successful cycle. CountActiveSince(watermarkID) is used on
// each tick to count only memories created AFTER the watermark. The watermark is
// in-process and resets to 0 on restart — this is documented in the commit body.
//
// The goroutine is tracked by s.wg and respects ctx cancellation.
func (s *Service) startSleepCycle(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		// Delay first check by one full interval so the sleep cycle does not
		// compete with startup and initial backfill work.
		timer := time.NewTimer(sleepCycleInterval)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.maybeSleepCycle(ctx)
				timer.Reset(sleepCycleInterval)
			}
		}
	}()
}

// trackingMemoryStore wraps a *gorm.MemoryStore and records the first error
// encountered during a sleep cycle run. The error is inspected after RunSleepCycle
// returns to decide whether the watermark should be advanced.
type trackingMemoryStore struct {
	ms  *gorm.MemoryStore
	err error
}

func (t *trackingMemoryStore) ListAllActive(ctx context.Context, batchSize int, offset int) ([]*models.Memory, error) {
	res, err := t.ms.ListAllActive(ctx, batchSize, offset)
	if err != nil && t.err == nil {
		t.err = err
	}
	return res, err
}

func (t *trackingMemoryStore) UpdateLifecycleFields(ctx context.Context, id int64, fields map[string]any) error {
	err := t.ms.UpdateLifecycleFields(ctx, id, fields)
	if err != nil && t.err == nil {
		t.err = err
	}
	return err
}

// maybeSleepCycle checks the trigger conditions and runs RunSleepCycle when met.
func (s *Service) maybeSleepCycle(ctx context.Context) {
	s.initMu.RLock()
	ms := s.memoryStore
	ps := s.promotionStore
	as := s.auditStore
	s.initMu.RUnlock()

	if ms == nil {
		log.Debug().Msg("sleep cycle: memory store not ready, skipping")
		return
	}

	// Idle gate (Finding 2): require >=4h since last HTTP/MCP request.
	// This approximates ">=4h since last active session" per T014 AC.
	// Signal source: s.lastRequestAt is stamped by requestActivityMiddleware
	// on every HTTP request (REST + MCP over HTTP). Zero means no request
	// since server start — treated as idle, so the count gate alone decides.
	lastReq := s.lastRequestAt.Load()
	if lastReq != 0 {
		idleSince := time.Since(time.Unix(0, lastReq))
		if idleSince < sleepCycleIdleGate {
			log.Debug().
				Dur("idle_for", idleSince).
				Dur("required", sleepCycleIdleGate).
				Msg("sleep cycle: idle gate not met, skipping")
			return
		}
	}

	// Count gate (Finding 1): query only memories created after the watermark.
	// This prevents the cycle from firing on a DB that already had many memories
	// before this server process started, and prevents the cap-at-11 bug.
	watermark := s.sleepCycleWatermarkID.Load()
	newCount, err := ms.CountActiveSince(ctx, watermark)
	if err != nil {
		log.Warn().Err(err).Msg("sleep cycle: count query failed, skipping")
		return
	}

	if newCount < sleepCycleMemoryCountThreshold {
		log.Debug().
			Int64("new_since_watermark", newCount).
			Int64("watermark_id", watermark).
			Int64("threshold", sleepCycleMemoryCountThreshold).
			Msg("sleep cycle: count threshold not met, skipping")
		return
	}

	log.Info().
		Int64("new_memories", newCount).
		Int64("watermark_id", watermark).
		Msg("sleep cycle: trigger conditions met, starting")

	// Pass auditStore as AuditLogger only when ENGRAM_VNEXT_ENABLED=true (T004).
	// AuditStore satisfies lifecycle.AuditLogger via its LogAudit method.
	var auditLog lifecycle.AuditLogger
	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" && as != nil {
		auditLog = as
	}

	tracker := &trackingMemoryStore{ms: ms}
	result := lifecycle.RunSleepCycle(ctx, tracker, ps, auditLog)

	log.Info().
		Int("processed", result.MemoriesProcessed).
		Int("promotions", result.Promotions).
		Int("demotions", result.Demotions).
		Int("expirations", result.Expirations).
		Int("review_flagged", result.ReviewFlagged).
		Dur("duration", result.Duration).
		Msg("sleep cycle: finished")

	// Milestone-F TG4 T028: run candidate decay batch when flag is on.
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
		s.initMu.RLock()
		cs := s.candidateStore
		s.initMu.RUnlock()
		if cs != nil {
			decayResult := lifecycle.RunCandidateDecayCycle(ctx, cs)
			log.Info().
				Int("candidates_decayed", decayResult.Decayed).
				Int("decay_errors", decayResult.Errors).
				Dur("decay_duration", decayResult.Duration).
				Msg("sleep cycle: candidate decay finished")
		}
	}

	// Advance watermark only when the run completed without errors and the
	// context was not cancelled. Skipping on failure ensures no memories are
	// silently orphaned from future counts.
	if ctx.Err() != nil {
		log.Warn().Msg("sleep cycle: context cancelled or timed out, skipping watermark update")
		return
	}
	if tracker.err != nil {
		log.Warn().Err(tracker.err).Msg("sleep cycle: encountered errors during run, skipping watermark update")
		return
	}

	// Advance watermark to max active ID so the next tick only counts memories
	// created after this cycle completed.
	newWatermark, err := ms.MaxActiveID(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("sleep cycle: could not fetch max id for watermark update, watermark unchanged")
		return
	}
	s.sleepCycleWatermarkID.Store(newWatermark)
	log.Debug().Int64("new_watermark_id", newWatermark).Msg("sleep cycle: watermark advanced")
}
