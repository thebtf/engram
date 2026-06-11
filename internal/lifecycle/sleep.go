package lifecycle

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/pkg/models"
)

// SleepResult contains the outcome of a sleep cycle run.
type SleepResult struct {
	MemoriesProcessed int
	Promotions        int
	Demotions         int
	Expirations       int
	ReviewFlagged     int
	Duration          time.Duration
}

// MemoryUpdater abstracts the store operations needed by the sleep cycle.
type MemoryUpdater interface {
	ListAllActive(ctx context.Context, batchSize int, offset int) ([]*models.Memory, error)
	UpdateLifecycleFields(ctx context.Context, id int64, fields map[string]any) error
}

// PromotionLogger abstracts audit logging for tier changes.
type PromotionLogger interface {
	LogPromotion(ctx context.Context, memoryID int64, fromTier, toTier, reason string) error
}

// AuditLogger abstracts audit_log writes for the sleep cycle (T004).
// Accepts just the fields needed for promote/demote events. When nil,
// no audit write occurs — nil-guard is applied at call sites.
type AuditLogger interface {
	LogAudit(ctx context.Context, memoryID int64, action, actor string) error
}

// RunSleepCycle executes a full decay + promotion + demotion + expiration cycle.
// auditLog may be nil (e.g. when ENGRAM_VNEXT_ENABLED is off); all call sites
// nil-guard before invoking.
func RunSleepCycle(ctx context.Context, store MemoryUpdater, promotionLog PromotionLogger, auditLog AuditLogger) SleepResult {
	start := time.Now()
	result := SleepResult{}

	offset := 0
	const batchSize = 500
	now := time.Now().UTC()

	for {
		select {
		case <-ctx.Done():
			result.Duration = time.Since(start)
			return result
		default:
		}

		memories, err := store.ListAllActive(ctx, batchSize, offset)
		if err != nil {
			log.Error().Err(err).Msg("sleep cycle: list active memories failed")
			break
		}
		if len(memories) == 0 {
			break
		}

		for _, m := range memories {
			fields := make(map[string]any)

			elapsed := now.Sub(m.CreatedAt).Hours() / 24
			if m.LastRetrievedAt != nil {
				elapsed = now.Sub(*m.LastRetrievedAt).Hours() / 24
			}

			stability := ComputeStability(m.Stability, m.Tier, m.EpistemicType, m.CitationCount)
			retrievability := ComputeRetrievability(stability, elapsed)

			if stability != m.Stability {
				fields["stability"] = stability
			}
			if absDiff(retrievability, m.Retrievability) > 0.01 {
				fields["retrievability"] = retrievability
			}

			confidence := ComputeConfidence(ConfidenceInputs{
				RecurrenceCount: m.RecurrenceCount,
				CitationCount:   m.CitationCount,
				InjectionCount:  m.InjectionCount,
				LastConfirmed:   m.LastConfirmed,
			})
			if absDiff(confidence, m.Confidence) > 0.01 {
				fields["confidence"] = confidence
			}

			m.Retrievability = retrievability
			m.Confidence = confidence
			m.Stability = stability

			// Finding 5: capture promotion/demotion intent but defer audit until
			// Findings 4-5 (second review): both promotionLog and auditLog must fire
			// only after UpdateLifecycleFields succeeds.  Capture pending callbacks
			// here; execute them in the success branch below.
			var pendingAuditAction string
			var pendingPromoLog func()
			if promo := EvaluatePromotion(m); promo.Changed {
				fields["tier"] = promo.NewTier
				result.Promotions++
				pendingPromoLog = func() {
					if promotionLog != nil {
						_ = promotionLog.LogPromotion(ctx, m.ID, m.Tier, promo.NewTier, promo.Reason)
					}
				}
				pendingAuditAction = "promote"
			} else if demo := EvaluateDemotion(m); demo.Changed {
				fields["tier"] = demo.NewTier
				result.Demotions++
				pendingPromoLog = func() {
					if promotionLog != nil {
						_ = promotionLog.LogPromotion(ctx, m.ID, m.Tier, demo.NewTier, demo.Reason)
					}
				}
				pendingAuditAction = "demote"
			}

			if m.Defeasibility == DefeasibilityRapid && m.ValidUntil != nil && now.After(*m.ValidUntil) {
				fields["status"] = "expired"
				result.Expirations++
			}

			if retrievability < 0.1 && m.ReviewAfter == nil {
				reviewAt := now.Add(7 * 24 * time.Hour)
				fields["review_after"] = reviewAt
				result.ReviewFlagged++
			}

			if len(fields) > 0 {
				if err := store.UpdateLifecycleFields(ctx, m.ID, fields); err != nil {
					log.Error().Err(err).Int64("memory_id", m.ID).Msg("sleep cycle: update failed")
				} else {
					result.MemoriesProcessed++
					// Findings 4-5: execute promotion/demotion log ONLY after
					// the DB update succeeds, preventing phantom entries on rollback.
					if pendingPromoLog != nil {
						pendingPromoLog()
					}
					// Audit tier changes only after successful persist.
					if pendingAuditAction != "" && auditLog != nil {
						if auditErr := auditLog.LogAudit(ctx, m.ID, pendingAuditAction, "system"); auditErr != nil {
							log.Error().Err(auditErr).Int64("memory_id", m.ID).Str("action", pendingAuditAction).Msg("sleep cycle: audit log failed")
						}
					}
				}
			}
		}

		offset += len(memories)
		if len(memories) < batchSize {
			break
		}
	}

	result.Duration = time.Since(start)
	log.Info().
		Int("processed", result.MemoriesProcessed).
		Int("promotions", result.Promotions).
		Int("demotions", result.Demotions).
		Int("expirations", result.Expirations).
		Int("review_flagged", result.ReviewFlagged).
		Dur("duration", result.Duration).
		Msg("sleep cycle: complete")

	return result
}

func absDiff(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

// --- Milestone-F TG4: crystallization candidate decay (T028) ---

// DecayBatchSize is the number of expired candidates processed per RunCandidateDecayCycle call.
const DecayBatchSize = 100

// DecayRecurrenceThreshold is the recurrence_count below which a candidate that has
// passed its review_after window is eligible for decay.  Candidates that have
// recurred DecayRecurrenceThreshold or more times are left pending so that an
// operator can manually review high-signal candidates.
const DecayRecurrenceThreshold = 3

// CandidateDecayResult holds the outcome of a single candidate decay cycle.
type CandidateDecayResult struct {
	Decayed  int
	Errors   int
	Duration time.Duration
}

// CandidateDecayer abstracts the candidate store operations needed by the decay cycle.
// Satisfied by *gorm.CandidateStore.
type CandidateDecayer interface {
	ListExpiredPending(ctx context.Context, threshold int, batchSize int) ([]*models.CrystallizationCandidate, error)
	TransitionToDecayed(ctx context.Context, id int64) (*models.CrystallizationCandidate, error)
}

// RunCandidateDecayCycle processes one batch of expired pending candidates.
// A candidate is eligible for decay when:
//   - status = 'pending'
//   - review_after < now
//   - recurrence_count < DecayRecurrenceThreshold
//
// Each eligible candidate is transitioned to 'decayed' via the state machine
// (SELECT...FOR UPDATE per EC-F10; audit_log entry written per §FR-F5).
// This function is a no-op when ENGRAM_VNEXT_F_ENABLED is false — callers
// are expected to gate via crystallization.VnextFEnabled() before invoking.
func RunCandidateDecayCycle(ctx context.Context, decayer CandidateDecayer) CandidateDecayResult {
	start := time.Now()
	result := CandidateDecayResult{}

	candidates, err := decayer.ListExpiredPending(ctx, DecayRecurrenceThreshold, DecayBatchSize)
	if err != nil {
		log.Error().Err(err).Msg("candidate decay: list expired pending failed")
		result.Errors++
		result.Duration = time.Since(start)
		return result
	}

	for _, c := range candidates {
		select {
		case <-ctx.Done():
			result.Duration = time.Since(start)
			return result
		default:
		}

		if _, transErr := decayer.TransitionToDecayed(ctx, c.ID); transErr != nil {
			log.Warn().Err(transErr).Int64("candidate_id", c.ID).Msg("candidate decay: transition failed")
			result.Errors++
			continue
		}
		result.Decayed++
	}

	result.Duration = time.Since(start)
	log.Info().
		Int("decayed", result.Decayed).
		Int("errors", result.Errors).
		Dur("duration", result.Duration).
		Msg("candidate decay cycle: complete")
	return result
}
