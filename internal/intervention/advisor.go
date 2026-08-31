package intervention

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/thebtf/engram/internal/taskmemory"
)

// UUIDSource allocates one canonical UUID for receipt or correlation identity.
type UUIDSource func() (string, error)

// RuntimeAdvisorConfig supplies the bounded T02 runtime dependencies.
type RuntimeAdvisorConfig struct {
	Preparer     taskmemory.Preparer
	ReceiptStore ReceiptStore
	KeyProvider  KeyProvider
	Clock        func() time.Time
	NewUUID      UUIDSource
}

// RuntimeAdvisor implements the permanent T02 no-candidate tracer. It has no
// policy, materialization, packet construction, or Observe persistence path.
type RuntimeAdvisor struct {
	preparer    taskmemory.Preparer
	receipts    ReceiptStore
	keyProvider KeyProvider
	clock       func() time.Time
	newUUID     UUIDSource
}

var _ Advisor = (*RuntimeAdvisor)(nil)

// NewRuntimeAdvisor constructs the concrete T02 advisor only when every
// required boundary dependency is explicit.
func NewRuntimeAdvisor(config RuntimeAdvisorConfig) (*RuntimeAdvisor, error) {
	if config.Preparer == nil || config.ReceiptStore == nil || config.KeyProvider == nil || config.Clock == nil || config.NewUUID == nil {
		return nil, ErrInvalidInput
	}
	return &RuntimeAdvisor{
		preparer:    config.Preparer,
		receipts:    config.ReceiptStore,
		keyProvider: config.KeyProvider,
		clock:       config.Clock,
		newUUID:     config.NewUUID,
	}, nil
}

// Advise prepares exactly once, then handles immutable receipt replay before
// the sole T02 authorable no-candidate abstention.
func (a *RuntimeAdvisor) Advise(ctx context.Context, input AdviseInput) (Decision, error) {
	if a == nil || ctx == nil || !input.binding.valid() || !input.binding.AllowsAdvise() || !input.occurrence.valid() {
		return Decision{}, ErrInvalidInput
	}

	prepared, err := a.preparer.Prepare(ctx, taskmemory.PrepareRequest{
		Project: input.ProjectEvidence(),
		Task:    input.Occurrence().Facts().TaskFacts(),
	})
	if err != nil {
		return a.unavailable(ctx, input, unavailableCodeForContext(ctx, UnavailableDependency))
	}

	if !a.hasCommitAndResponseReserve(ctx) {
		return a.unavailable(ctx, input, UnavailableDeadline)
	}
	epoch, current := a.keyProvider.Current()
	if !current {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	axis, err := NewReceiptAxis(epoch, input.binding, prepared.Context(), input.occurrence)
	if err != nil {
		return a.unavailable(ctx, input, UnavailableDependency)
	}

	winner, found, err := a.receipts.Lookup(ctx, axis)
	if err != nil {
		return a.unavailable(ctx, input, unavailableCodeForContext(ctx, UnavailableReceipt))
	}
	if found {
		return a.replay(ctx, input, epoch, axis, winner)
	}

	candidates := prepared.Candidates()
	if len(candidates) > taskmemory.MaxPreparedCandidates || len(candidates) != 0 {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	if !a.hasCommitAndResponseReserve(ctx) {
		return a.unavailable(ctx, input, UnavailableDeadline)
	}

	receiptID, err := a.nextUUID()
	if err != nil {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	operationID, err := a.nextUUID()
	if err != nil {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	createdAt := a.clock().UTC()
	if createdAt.IsZero() {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	receipt, err := NewNoCandidatesReceipt(ctx, epoch, axis, receiptID, operationID, createdAt)
	if err != nil {
		return a.unavailable(ctx, input, UnavailableDeadline)
	}

	winner, inserted, err := a.receipts.Commit(ctx, receipt)
	if err != nil {
		return a.unavailable(ctx, input, unavailableCodeForContext(ctx, UnavailableReceipt))
	}
	if inserted {
		verified, code, ok := verifiedReceiptForReplay(epoch, axis, winner)
		if !ok {
			return a.unavailable(ctx, input, code)
		}
		if !sameReceiptIdentity(verified, receipt) {
			return a.unavailable(ctx, input, UnavailableReceipt)
		}
		return a.mapVerifiedReplay(ctx, input, verified)
	}
	return a.replay(ctx, input, epoch, axis, winner)
}

// Observe remains deliberately unavailable until migration 172 introduces the
// separately bounded observation persistence contract.
func (a *RuntimeAdvisor) Observe(_ context.Context, _ ObserveInput) (ObservationAck, error) {
	return NewUnavailableObservationAck(ObservationReasonDependencyUnavailable)
}

func (a *RuntimeAdvisor) replay(ctx context.Context, input AdviseInput, epoch KeyEpoch, axis ReceiptAxis, receipt Receipt) (Decision, error) {
	verified, code, ok := verifiedReceiptForReplay(epoch, axis, receipt)
	if !ok {
		return a.unavailable(ctx, input, code)
	}
	return a.mapVerifiedReplay(ctx, input, verified)
}

func verifiedReceiptForReplay(epoch KeyEpoch, axis ReceiptAxis, receipt Receipt) (Receipt, UnavailableCode, bool) {
	verified, ok := receipt.VerifyTrusted(epoch)
	if !ok || !sameReceiptOccurrenceAxis(verified.Axis(), axis) {
		return Receipt{}, UnavailableReceipt, false
	}
	if !sameDigestConstantTime(verified.Axis().ContentCommitment(), axis.ContentCommitment()) {
		return Receipt{}, UnavailableReplayConflict, false
	}
	return verified, 0, true
}

func (a *RuntimeAdvisor) mapVerifiedReplay(ctx context.Context, input AdviseInput, receipt Receipt) (Decision, error) {
	identity := receipt.Identity()
	if !identity.valid() {
		return a.unavailable(ctx, input, UnavailableReceipt)
	}
	record := receipt.PersistenceRecord()
	switch record.Outcome {
	case ReceiptOutcomeAbstain:
		if record.ClosedReason == nil {
			return a.unavailable(ctx, input, UnavailableReceipt)
		}
		return NewAbstainDecision(identity, *record.ClosedReason)
	case ReceiptOutcomeEmit:
		return NewDeliveryAmbiguousDecision(identity)
	default:
		return a.unavailable(ctx, input, UnavailableReceipt)
	}
}

func (a *RuntimeAdvisor) unavailable(ctx context.Context, input AdviseInput, code UnavailableCode) (Decision, error) {
	correlationID, err := a.nextUUID()
	if err != nil {
		return Decision{}, err
	}
	expiresAt := input.binding.ExpiresAt()
	if ctx != nil {
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
			expiresAt = deadline.UTC()
		}
	}
	return NewUnavailableDecision(correlationID, expiresAt, code)
}

func (a *RuntimeAdvisor) hasCommitAndResponseReserve(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return false
	}
	now := a.clock().UTC()
	if now.IsZero() {
		return false
	}
	return normalizeReceiptTimestamp(deadline).Sub(now) >= CommitReserve+ResponseReserve
}

func (a *RuntimeAdvisor) nextUUID() (string, error) {
	value, err := a.newUUID()
	if err != nil || !validCanonicalUUID(value) {
		return "", ErrInvalidInput
	}
	return value, nil
}

// unavailableCodeForContext gives callback deadline exhaustion precedence only
// when the outer callback context itself expired. Dependency-local timeouts and
// cancellations keep their dependency-specific fallback.
func unavailableCodeForContext(ctx context.Context, fallback UnavailableCode) UnavailableCode {
	if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		return UnavailableDeadline
	}
	return fallback
}

func sameReceiptIdentity(left, right Receipt) bool {
	leftIdentity := left.Identity()
	rightIdentity := right.Identity()
	return leftIdentity.valid() && rightIdentity.valid() && leftIdentity == rightIdentity
}

func sameReceiptOccurrenceAxis(left, right ReceiptAxis) bool {
	return left.ChannelKey() == right.ChannelKey() &&
		left.HostFamily() == right.HostFamily() &&
		left.CanonicalProject() == right.CanonicalProject() &&
		left.ActorPrincipal() == right.ActorPrincipal() &&
		left.ActorKind() == right.ActorKind() &&
		left.Workstation() == right.Workstation() &&
		left.OccurrenceKey() == right.OccurrenceKey()
}

func sameDigestConstantTime(left, right Digest) bool {
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}
