package intervention

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/thebtf/engram/internal/taskmemory"
)

// UUIDSource allocates one canonical UUID for receipt or correlation identity.
type UUIDSource func() (string, error)

// RuntimeAdvisorConfig supplies the bounded T02/T03/M1 runtime dependencies.
type RuntimeAdvisorConfig struct {
	Preparer       taskmemory.Preparer
	Materializer   taskmemory.Materializer
	ReceiptStore   ReceiptStore
	KeyProvider    KeyProvider
	PolicyReader   PolicyReader
	PolicyVersions PolicySemanticVersions
	Clock          func() time.Time
	NewUUID        UUIDSource
}

// RuntimeAdvisor performs receipt replay, M1 exact materialization, T02
// no-candidate abstention, and T03 policy-state abstention. It has no
// request-time compiler or Observe persistence path.
type RuntimeAdvisor struct {
	preparer       taskmemory.Preparer
	materializer   taskmemory.Materializer
	receipts       ReceiptStore
	keyProvider    KeyProvider
	policyReader   PolicyReader
	policyVersions PolicySemanticVersions
	clock          func() time.Time
	newUUID        UUIDSource
}

var _ Advisor = (*RuntimeAdvisor)(nil)

// NewRuntimeAdvisor constructs the concrete advisor only when every required
// dependency is explicit. The policy reader remains optional to preserve T02.
func NewRuntimeAdvisor(config RuntimeAdvisorConfig) (*RuntimeAdvisor, error) {
	if config.Preparer == nil || config.ReceiptStore == nil || config.KeyProvider == nil || config.Clock == nil || config.NewUUID == nil ||
		(config.PolicyReader != nil && !config.PolicyVersions.Valid()) {
		return nil, ErrInvalidInput
	}
	return &RuntimeAdvisor{
		preparer:       config.Preparer,
		materializer:   config.Materializer,
		receipts:       config.ReceiptStore,
		keyProvider:    config.KeyProvider,
		policyReader:   config.PolicyReader,
		policyVersions: config.PolicyVersions,
		clock:          config.Clock,
		newUUID:        config.NewUUID,
	}, nil
}

// Advise prepares exactly once, resolves immutable receipt replay first, then
// materializes ranked context references before falling back to accepted T02/T03
// abstention shapes.
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
	if len(candidates) > taskmemory.MaxPreparedCandidates {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	if len(candidates) == 0 {
		if !a.hasCommitAndResponseReserve(ctx) {
			return a.unavailable(ctx, input, UnavailableDeadline)
		}
		return a.commitAbstention(ctx, input, epoch, axis, AbstentionNoCandidates, 0)
	}
	if a.materializer != nil {
		for index, candidate := range candidates {
			if !a.hasCommitAndResponseReserve(ctx) {
				return a.unavailable(ctx, input, UnavailableDeadline)
			}
			materialized, found, err := a.materializer.Materialize(ctx, prepared.Context(), candidate)
			if err != nil {
				return a.unavailable(ctx, input, unavailableCodeForContext(ctx, UnavailableDependency))
			}
			if !found || !materialized.Valid() {
				continue
			}
			reference := materialized.Reference()
			if reference.ID() != candidate.ID() || reference.Version() != candidate.Version() || reference.SourceTier() != candidate.SourceTier() {
				continue
			}
			return a.commitContextReference(ctx, input, epoch, axis, materialized, index+1)
		}
		if !a.hasCommitAndResponseReserve(ctx) {
			return a.unavailable(ctx, input, UnavailableDeadline)
		}
		return a.commitAbstention(ctx, input, epoch, axis, AbstentionEvidenceInsufficient, len(candidates))
	}
	if a.policyReader == nil {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	if !a.hasCommitAndResponseReserve(ctx) {
		return a.unavailable(ctx, input, UnavailableDeadline)
	}
	policies, err := a.policyReader.ReadCandidatePolicies(ctx, prepared.Context(), candidates, a.policyVersions)
	if err != nil {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	reason, ok := reduceCandidatePolicyStates(epoch, candidates, policies)
	if !ok {
		return a.unavailable(ctx, input, UnavailableDependency)
	}
	if !a.hasCommitAndResponseReserve(ctx) {
		return a.unavailable(ctx, input, UnavailableDeadline)
	}
	return a.commitAbstention(ctx, input, epoch, axis, reason, len(candidates))
}

func (a *RuntimeAdvisor) commitAbstention(ctx context.Context, input AdviseInput, epoch KeyEpoch, axis ReceiptAxis, reason AbstentionReason, evaluatedCount int) (Decision, error) {
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

	var receipt Receipt
	if reason == AbstentionNoCandidates {
		receipt, err = NewNoCandidatesReceipt(ctx, epoch, axis, receiptID, operationID, createdAt)
	} else {
		receipt, err = NewPolicyAbstentionReceipt(ctx, epoch, axis, receiptID, operationID, createdAt, reason, evaluatedCount)
	}
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

func (a *RuntimeAdvisor) commitContextReference(ctx context.Context, input AdviseInput, epoch KeyEpoch, axis ReceiptAxis, materialized taskmemory.MaterializedCandidate, evaluatedCount int) (Decision, error) {
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
	receipt, err := NewContextReferenceReceipt(ctx, epoch, axis, receiptID, operationID, createdAt, materialized, evaluatedCount)
	if err != nil {
		return a.unavailable(ctx, input, unavailableCodeForContext(ctx, UnavailableDependency))
	}

	winner, inserted, err := a.receipts.Commit(ctx, receipt)
	if err != nil {
		return a.unavailable(ctx, input, unavailableCodeForContext(ctx, UnavailableReceipt))
	}
	if !inserted {
		return a.replay(ctx, input, epoch, axis, winner)
	}
	verified, code, ok := verifiedReceiptForReplay(epoch, axis, winner)
	if !ok || !sameReceiptIdentity(verified, receipt) {
		if !ok {
			return a.unavailable(ctx, input, code)
		}
		return a.unavailable(ctx, input, UnavailableReceipt)
	}
	return emitContextReference(verified, materialized)
}

func emitContextReference(receipt Receipt, materialized taskmemory.MaterializedCandidate) (Decision, error) {
	if !materialized.Valid() {
		return Decision{}, ErrInvalidInput
	}
	identity := receipt.Identity()
	record := receipt.PersistenceRecord()
	if !identity.valid() || record.Outcome != ReceiptOutcomeEmit || record.DecisionMode != ReceiptDecisionModeContextReference || record.Selection == nil {
		return Decision{}, ErrInvalidInput
	}
	memoryID, memoryVersion, sourceProject, sourceTier, textDigest, ok := record.Selection.ContextReference()
	if !ok {
		return Decision{}, ErrInvalidInput
	}
	reference := materialized.Reference()
	tier, ok := candidateTierFromTaskMemory(reference.SourceTier())
	if !ok || memoryID != reference.ID() || memoryVersion != reference.Version() || sourceProject != materialized.SourceProject() || sourceTier != tier || textDigest != Digest(materialized.TextDigest()) {
		return Decision{}, ErrInvalidInput
	}
	knowledge, err := NewKnowledgeReference(memoryID, uint32(memoryVersion), sourceProject, sourceTier, [32]byte(textDigest))
	if err != nil {
		return Decision{}, err
	}
	presentation, err := NewUntrustedReferencePresentation(materialized.Excerpt())
	if err != nil {
		return Decision{}, err
	}
	packet, err := NewPacket(identity, record.ExpiresAt, knowledge, presentation)
	if err != nil {
		return Decision{}, err
	}
	return NewEmitDecision(identity, packet)
}

func reduceCandidatePolicyStates(epoch KeyEpoch, candidates []taskmemory.AuthorizedCandidateRef, policies []CandidatePolicy) (AbstentionReason, bool) {
	if !epoch.valid() || len(candidates) == 0 || len(candidates) > taskmemory.MaxPreparedCandidates || len(candidates) != len(policies) {
		return 0, false
	}
	anyValid := false
	anyInsufficientOrMissing := false
	for index, candidate := range candidates {
		policy := policies[index]
		ref := policy.Ref()
		if ref.ID() != candidate.ID() || ref.Version() != candidate.Version() || ref.SourceTier() != candidate.SourceTier() {
			return 0, false
		}
		state := policy.State()
		if state == CandidatePolicyValid || state == CandidatePolicyInsufficient {
			scope, present := policy.CurrentScope()
			currentCommitment, err := epoch.DerivePolicyScope(scope)
			if !present || err != nil || !sameDigestConstantTime(currentCommitment, policy.ScopeCommitment()) {
				state = CandidatePolicySourceStale
			}
		}
		switch state {
		case CandidatePolicyValid:
			anyValid = true
		case CandidatePolicyInsufficient, CandidatePolicyMissing:
			anyInsufficientOrMissing = true
		case CandidatePolicySourceStale:
		default:
			return 0, false
		}
	}
	if anyValid {
		return AbstentionPolicyObserving, true
	}
	if anyInsufficientOrMissing {
		return AbstentionEvidenceInsufficient, true
	}
	return AbstentionEvidenceState, true
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
