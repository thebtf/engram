package intervention

import (
	"context"
	"errors"
	"time"
)

const (
	// HardDeadlineCap bounds every H03 server callback independently of host input.
	HardDeadlineCap = 2 * time.Second
	// MinimumEntryBudget is required before Advise begins any downstream work.
	MinimumEntryBudget = 300 * time.Millisecond
	// CommitReserve is held for receipt commit and revalidation.
	CommitReserve = 250 * time.Millisecond
	// ResponseReserve is held for final response mapping.
	ResponseReserve = 50 * time.Millisecond
)

var ErrDeadlineElapsed = errors.New("intervention deadline elapsed")

// EffectiveDeadline is the immutable earliest legal H03 callback end.
type EffectiveDeadline struct {
	deadline time.Time
}

// NewEffectiveDeadline computes the minimum of a caller deadline when present,
// handler entry plus the accepted binding callback duration, binding expiry, and
// the fixed server hard cap. It reads no clock; entry is an explicit input.
func NewEffectiveDeadline(ctx context.Context, enteredAt time.Time, binding BindingFacts) (EffectiveDeadline, error) {
	if ctx == nil || enteredAt.IsZero() || !binding.valid() {
		return EffectiveDeadline{}, ErrInvalidInput
	}
	enteredAt = enteredAt.UTC()
	deadline := minTime(
		enteredAt.Add(binding.callbackDuration),
		binding.expiresAt,
		enteredAt.Add(HardDeadlineCap),
	)
	if contextDeadline, hasDeadline := ctx.Deadline(); hasDeadline {
		deadline = minTime(deadline, contextDeadline.UTC())
	}
	if !deadline.After(enteredAt) {
		return EffectiveDeadline{}, ErrDeadlineElapsed
	}
	return EffectiveDeadline{deadline: deadline.UTC()}, nil
}

// Deadline returns the absolute UTC callback deadline.
func (d EffectiveDeadline) Deadline() time.Time {
	return d.deadline
}

// WorkDeadline returns the latest time ordinary preparation may begin while
// retaining the required commit and response reserves.
func (d EffectiveDeadline) WorkDeadline() time.Time {
	if !d.valid() {
		return time.Time{}
	}
	return d.deadline.Add(-(CommitReserve + ResponseReserve))
}

// RemainingAt returns the signed remaining duration against an injected instant.
func (d EffectiveDeadline) RemainingAt(at time.Time) time.Duration {
	if !d.valid() || at.IsZero() {
		return 0
	}
	return d.deadline.Sub(at.UTC())
}

// HasReserve reports whether the deadline leaves at least reserve at the
// injected instant. Negative reserves are never legal.
func (d EffectiveDeadline) HasReserve(at time.Time, reserve time.Duration) bool {
	return reserve >= 0 && d.valid() && !at.IsZero() && d.RemainingAt(at) >= reserve
}

// AllowsEntry enforces the mandatory 300 ms initial callback budget.
func (d EffectiveDeadline) AllowsEntry(at time.Time) bool {
	return d.HasReserve(at, MinimumEntryBudget)
}

// AllowsWork reports whether preparation can start without consuming commit or
// response reserves.
func (d EffectiveDeadline) AllowsWork(at time.Time) bool {
	return d.HasReserve(at, CommitReserve+ResponseReserve)
}

// AllowsCommit reports whether a receipt commit can start while leaving response mapping time.
func (d EffectiveDeadline) AllowsCommit(at time.Time) bool {
	return d.HasReserve(at, CommitReserve+ResponseReserve)
}

// AllowsResponse reports whether final response mapping still has its reserve.
func (d EffectiveDeadline) AllowsResponse(at time.Time) bool {
	return d.HasReserve(at, ResponseReserve)
}

func (d EffectiveDeadline) valid() bool {
	return !d.deadline.IsZero()
}

func minTime(values ...time.Time) time.Time {
	minimum := values[0]
	for _, value := range values[1:] {
		if value.Before(minimum) {
			minimum = value
		}
	}
	return minimum
}
