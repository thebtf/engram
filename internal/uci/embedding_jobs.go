package uci

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"
)

const embeddingWorkerClientSessionPrefix = "embedding-worker"

var (
	ErrEmbeddingJobLeaseLost  = errors.New("uci embedding job: lease lost")
	ErrEmbeddingJobObsolete   = errors.New("uci embedding job: obsolete")
	ErrEmbeddingJobCancelled  = errors.New("uci embedding job: cancelled")
	ErrEmbeddingInputCapacity = errors.New("uci embedding job: input capacity")
	errEmbeddingProviderReply = errors.New("uci embedding job: malformed provider response")
)

// EmbeddingFailureCode is the closed durable outcome vocabulary for one corpus
// embedding obligation. It deliberately contains no provider diagnostic.
type EmbeddingFailureCode string

const (
	EmbeddingFailureProviderUnavailable EmbeddingFailureCode = "provider_unavailable"
	EmbeddingFailureProviderQuota       EmbeddingFailureCode = "provider_quota"
	EmbeddingFailureProviderAuth        EmbeddingFailureCode = "provider_auth_unavailable"
	EmbeddingFailureProviderContract    EmbeddingFailureCode = "provider_contract"
	EmbeddingFailureMalformedResponse   EmbeddingFailureCode = "provider_malformed_response"
	EmbeddingFailureInputCapacity       EmbeddingFailureCode = "input_capacity"
	EmbeddingFailureProfileInvalid      EmbeddingFailureCode = "profile_invalid"
	EmbeddingFailureDatabaseUnavailable EmbeddingFailureCode = "database_unavailable"
	EmbeddingFailureTargetObsolete      EmbeddingFailureCode = "target_obsolete"
	EmbeddingFailureAuthorityLost       EmbeddingFailureCode = "authority_lost"
	EmbeddingFailureLeaseLost           EmbeddingFailureCode = "lease_lost"
	EmbeddingFailureNoCandidates        EmbeddingFailureCode = "no_candidates"
	EmbeddingFailureLegacyUnbound       EmbeddingFailureCode = "legacy_unbound"
)

func (code EmbeddingFailureCode) valid() bool {
	switch code {
	case EmbeddingFailureProviderUnavailable,
		EmbeddingFailureProviderQuota,
		EmbeddingFailureProviderAuth,
		EmbeddingFailureProviderContract,
		EmbeddingFailureMalformedResponse,
		EmbeddingFailureInputCapacity,
		EmbeddingFailureProfileInvalid,
		EmbeddingFailureDatabaseUnavailable,
		EmbeddingFailureTargetObsolete,
		EmbeddingFailureAuthorityLost,
		EmbeddingFailureLeaseLost,
		EmbeddingFailureNoCandidates,
		EmbeddingFailureLegacyUnbound:
		return true
	default:
		return false
	}
}

// ValidForEmbeddingJob reports whether the code is in the closed durable vocabulary.
func (code EmbeddingFailureCode) ValidForEmbeddingJob() bool {
	return code.valid()
}

// EmbeddingFailureDisposition controls the durable state transition for a
// provider or authority outcome.
type EmbeddingFailureDisposition string

const (
	EmbeddingFailureRetry     EmbeddingFailureDisposition = "retry"
	EmbeddingFailureTerminal  EmbeddingFailureDisposition = "terminal"
	EmbeddingFailureObsolete  EmbeddingFailureDisposition = "obsolete"
	EmbeddingFailureCancelled EmbeddingFailureDisposition = "cancelled"
)

func (disposition EmbeddingFailureDisposition) valid() bool {
	switch disposition {
	case EmbeddingFailureRetry, EmbeddingFailureTerminal, EmbeddingFailureObsolete, EmbeddingFailureCancelled:
		return true
	default:
		return false
	}
}

// EmbeddingFailure contains only a closed state-transition decision. Store
// implementations must not persist the original provider error text.
type EmbeddingFailure struct {
	Code        EmbeddingFailureCode
	Disposition EmbeddingFailureDisposition
}

func (failure EmbeddingFailure) valid() bool {
	if !failure.Code.valid() || !failure.Disposition.valid() {
		return false
	}
	switch failure.Code {
	case EmbeddingFailureTargetObsolete:
		return failure.Disposition == EmbeddingFailureObsolete
	case EmbeddingFailureAuthorityLost:
		return failure.Disposition == EmbeddingFailureCancelled
	case EmbeddingFailureLeaseLost:
		return failure.Disposition == EmbeddingFailureCancelled
	case EmbeddingFailureNoCandidates, EmbeddingFailureLegacyUnbound:
		return false
	default:
		return true
	}
}

// ValidForEmbeddingJob reports whether a failure is a closed durable transition.
func (failure EmbeddingFailure) ValidForEmbeddingJob() bool {
	return failure.valid()
}

// EmbeddingJobRef is the lease-fenced durable identity for one exact View and
// vector profile. It is never a checkout-following selection.
type EmbeddingJobRef struct {
	JobID              string
	Scope              IndexScope
	Context            ContextRef
	EmbeddingProfileID string
	InputFingerprint   IndexDigest
	LeaseOwner         string
	LeaseEpoch         int64
}

func (ref EmbeddingJobRef) valid() bool {
	return canonicalContextUUID(ref.JobID) && validIndexScope(ref.Scope) && ref.Context.valid() &&
		ref.Scope.SourceID == ref.Context.SourceID && ref.Scope.CheckoutID == ref.Context.CheckoutID &&
		canonicalContextUUID(ref.EmbeddingProfileID) && isIndexDigest(ref.InputFingerprint) &&
		validEmbeddingOwner(ref.LeaseOwner) && ref.LeaseEpoch > 0
}

// ValidForEmbeddingJob reports whether a reference carries a complete lease fence.
func (ref EmbeddingJobRef) ValidForEmbeddingJob() bool {
	return ref.valid()
}

// ValidForEmbeddingJob reports whether an exact context can anchor embedding work.
func (ref ContextRef) ValidForEmbeddingJob() bool {
	return ref.valid()
}

func embeddingJobRefsEqual(left, right EmbeddingJobRef) bool {
	return left.JobID == right.JobID && left.Scope == right.Scope && contextRefsEqual(left.Context, right.Context) &&
		left.EmbeddingProfileID == right.EmbeddingProfileID && left.InputFingerprint == right.InputFingerprint &&
		left.LeaseOwner == right.LeaseOwner && left.LeaseEpoch == right.LeaseEpoch
}

// EmbeddingJobClaim is a job lease plus the frozen catalog authority needed to
// authorize the exact target before every producer operation.
type EmbeddingJobClaim struct {
	Ref            EmbeddingJobRef
	Access         ContextAccess
	Profile        VectorProfile
	Attempt        int
	LeaseExpiresAt time.Time
}

func (claim EmbeddingJobClaim) valid() bool {
	return claim.Ref.valid() && claim.Access.AuthRealm != "" && claim.Access.Principal != "" &&
		claim.Access.SourceID == claim.Ref.Scope.SourceID && claim.Access.CheckoutID == claim.Ref.Scope.CheckoutID &&
		validIndexText(claim.Access.AuthRealm) && validIndexText(claim.Access.Principal) &&
		validateSemanticProfile(claim.Profile) == nil && claim.Attempt > 0 && !claim.LeaseExpiresAt.IsZero()
}

// ValidForEmbeddingJob reports whether a claim carries a valid durable lease and authority tuple.
func (claim EmbeddingJobClaim) ValidForEmbeddingJob() bool {
	return claim.valid()
}

// EmbeddingCandidateKey is the stable keyset position in the unfiltered exact
// View candidate denominator.
type EmbeddingCandidateKey struct {
	MembershipID string
	ChunkID      string
}

func (key EmbeddingCandidateKey) valid() bool {
	return canonicalContextUUID(key.MembershipID) && canonicalContextUUID(key.ChunkID)
}

func embeddingCandidateKeyEqual(left, right EmbeddingCandidateKey) bool {
	return left.MembershipID == right.MembershipID && left.ChunkID == right.ChunkID
}

// EmbeddingCandidate is one admitted current candidate whose exact canonical
// input may be reused only in its source and protection domain.
type EmbeddingCandidate struct {
	Key              EmbeddingCandidateKey
	Candidate        QueryCandidate
	ProtectionDomain string
	Input            string
	InputDigest      IndexDigest
}

func (candidate EmbeddingCandidate) validFor(ref ContextRef, profile VectorProfile) bool {
	if !candidate.Key.valid() || !contextRefsEqual(candidate.Candidate.Context, ref) || !validQueryCandidate(candidate.Candidate) ||
		!validIndexText(candidate.ProtectionDomain) || !isIndexDigest(candidate.InputDigest) {
		return false
	}
	input, digest, err := SemanticEmbeddingInput(profile, candidate.Candidate)
	return err == nil && input == candidate.Input && digest == candidate.InputDigest
}

// EmbeddingBatch is an exact contiguous candidate page. Vectors supplied to
// CommitEmbeddingBatch correspond, in order, to MissingInputIndexes.
type EmbeddingBatch struct {
	Job                 EmbeddingJobRef
	BatchDigest         IndexDigest
	StartAfter          *EmbeddingCandidateKey
	NextAfter           *EmbeddingCandidateKey
	Candidates          []EmbeddingCandidate
	MissingInputIndexes []int
	Exhausted           bool
}

func (batch EmbeddingBatch) validFor(claim EmbeddingJobClaim) bool {
	if !embeddingJobRefsEqual(batch.Job, claim.Ref) || !isIndexDigest(batch.BatchDigest) {
		return false
	}
	if !embeddingBatchCursorsValid(batch) {
		return false
	}
	if len(batch.Candidates) == 0 {
		return len(batch.MissingInputIndexes) == 0 && (batch.Exhausted || batch.NextAfter == nil)
	}
	return embeddingBatchCandidatesValid(batch, claim) && embeddingMissingIndexesValid(batch)
}

func embeddingBatchCursorsValid(batch EmbeddingBatch) bool {
	return (batch.StartAfter == nil || batch.StartAfter.valid()) && (batch.NextAfter == nil || batch.NextAfter.valid())
}

func embeddingBatchCandidatesValid(batch EmbeddingBatch, claim EmbeddingJobClaim) bool {
	if batch.NextAfter == nil || !embeddingCandidateKeyEqual(*batch.NextAfter, batch.Candidates[len(batch.Candidates)-1].Key) {
		return false
	}
	last := EmbeddingCandidateKey{}
	for index, candidate := range batch.Candidates {
		if !candidate.validFor(claim.Ref.Context, claim.Profile) || (index > 0 && !embeddingCandidateKeyLess(last, candidate.Key)) {
			return false
		}
		last = candidate.Key
	}
	return true
}

func embeddingMissingIndexesValid(batch EmbeddingBatch) bool {
	previous := -1
	for _, index := range batch.MissingInputIndexes {
		if index < 0 || index >= len(batch.Candidates) || index <= previous {
			return false
		}
		previous = index
	}
	return true
}

// ValidForEmbeddingJob reports whether a batch is consistent with its lease claim.
func (batch EmbeddingBatch) ValidForEmbeddingJob(claim EmbeddingJobClaim) bool {
	return batch.validFor(claim)
}

func embeddingCandidateKeyLess(left, right EmbeddingCandidateKey) bool {
	if left.MembershipID != right.MembershipID {
		return left.MembershipID < right.MembershipID
	}
	return left.ChunkID < right.ChunkID
}

// EmbeddingWorkerLimits bounds page, transport, lease, and polling work. It
// never truncates the target View denominator.
type EmbeddingWorkerLimits struct {
	CandidatePageSize     int
	ProviderBatchSize     int
	ProviderConcurrency   int
	MaxProviderBatchBytes int
	LeaseTTL              time.Duration
	RenewInterval         time.Duration
	ProviderCallTimeout   time.Duration
	PollInterval          time.Duration
}

// DefaultEmbeddingWorkerLimits returns production bounds for one process-local
// worker that persists all durable progress in PostgreSQL.
func DefaultEmbeddingWorkerLimits() EmbeddingWorkerLimits {
	return EmbeddingWorkerLimits{
		CandidatePageSize:     512,
		ProviderBatchSize:     128,
		ProviderConcurrency:   4,
		MaxProviderBatchBytes: 524288,
		LeaseTTL:              2 * time.Minute,
		RenewInterval:         30 * time.Second,
		ProviderCallTimeout:   100 * time.Second,
		PollInterval:          100 * time.Millisecond,
	}
}

func (limits EmbeddingWorkerLimits) valid() bool {
	return limits.CandidatePageSize > 0 && limits.ProviderBatchSize > 0 && limits.ProviderBatchSize <= limits.CandidatePageSize &&
		limits.ProviderConcurrency > 0 && limits.ProviderConcurrency <= limits.CandidatePageSize &&
		limits.MaxProviderBatchBytes > 0 && limits.LeaseTTL > 0 && limits.RenewInterval > 0 && limits.RenewInterval < limits.LeaseTTL &&
		limits.ProviderCallTimeout > 0 && limits.PollInterval > 0
}

// EmbeddingJobStore owns the durable, transaction-fenced embedding workflow.
// Provider calls deliberately remain outside this boundary.
type EmbeddingJobStore interface {
	EnsureCurrentEmbeddingJobs(ctx context.Context, profile VectorProfile, afterCheckoutID string, limit int) (nextCheckoutID string, exhausted bool, err error)
	ClaimEmbeddingJob(ctx context.Context, profile VectorProfile, owner string, leaseTTL time.Duration) (EmbeddingJobClaim, bool, error)
	RenewEmbeddingJob(ctx context.Context, ref EmbeddingJobRef, leaseTTL time.Duration) error
	PrepareEmbeddingBatch(ctx context.Context, claim EmbeddingJobClaim, authorized AuthorizedContext, limit int) (EmbeddingBatch, error)
	CommitEmbeddingBatch(ctx context.Context, claim EmbeddingJobClaim, authorized AuthorizedContext, batch EmbeddingBatch, vectors [][]float32) error
	CompleteEmbeddingJob(ctx context.Context, claim EmbeddingJobClaim, authorized AuthorizedContext) error
	FailEmbeddingJob(ctx context.Context, ref EmbeddingJobRef, failure EmbeddingFailure) error
}

// EmbeddingWorker claims durable obligations, performs provider calls outside
// database transactions, and commits only current lease-fenced results.
type EmbeddingWorker struct {
	profile        VectorProfile
	embedder       SemanticEmbedder
	store          EmbeddingJobStore
	resolver       *ContextResolver
	limits         EmbeddingWorkerLimits
	timingObserver embeddingJobTimingObserver
}

// NewEmbeddingWorker constructs one configured-profile corpus worker.
func NewEmbeddingWorker(profile VectorProfile, embedder SemanticEmbedder, store EmbeddingJobStore, resolver *ContextResolver, limits EmbeddingWorkerLimits) (*EmbeddingWorker, error) {
	if err := validateSemanticProfile(profile); err != nil {
		return nil, err
	}
	if semanticNil(embedder) || semanticNil(store) || resolver == nil || !limits.valid() {
		return nil, fmt.Errorf("uci embedding worker: configuration is invalid")
	}
	if embedder.Model() != profile.Model {
		return nil, fmt.Errorf("uci embedding worker: provider model does not match profile")
	}
	return &EmbeddingWorker{profile: profile, embedder: embedder, store: store, resolver: resolver, limits: limits, timingObserver: newEmbeddingJobTimingObserverFromEnvironment()}, nil
}

// Run adopts existing current Views once, then processes durable work until the
// service root context is cancelled.
func (worker *EmbeddingWorker) Run(ctx context.Context, owner string) error {
	if worker == nil || ctx == nil || !validEmbeddingOwner(owner) {
		return fmt.Errorf("uci embedding worker: run configuration is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil
	}
	if err := worker.adopt(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		claimed, err := worker.claimOnce(ctx, owner)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if claimed {
			timer.Reset(0)
			continue
		}
		timer.Reset(worker.limits.PollInterval)
	}
}

func (worker *EmbeddingWorker) claimOnce(ctx context.Context, owner string) (bool, error) {
	var claimStarted time.Time
	if worker.timingObserver != nil {
		claimStarted = time.Now()
	}
	claim, claimed, err := worker.store.ClaimEmbeddingJob(ctx, worker.profile, owner, worker.limits.LeaseTTL)
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, nil
	}
	timing := newEmbeddingJobTimingScope(worker.timingObserver, claim, claimStarted)
	if timing != nil {
		timing.observe(embeddingJobTimingStageClaim, claimStarted, time.Now(), nil, "ok")
	}
	if err := worker.runClaim(ctx, claim, timing); err != nil {
		return true, err
	}
	return true, nil
}

func (worker *EmbeddingWorker) adopt(ctx context.Context) error {
	after := ""
	for {
		next, exhausted, err := worker.store.EnsureCurrentEmbeddingJobs(ctx, worker.profile, after, worker.limits.CandidatePageSize)
		if err != nil {
			return err
		}
		if exhausted {
			return nil
		}
		if next == "" || next <= after {
			return fmt.Errorf("uci embedding worker: adoption cursor did not advance")
		}
		after = next
	}
}

func (worker *EmbeddingWorker) runClaim(root context.Context, claim EmbeddingJobClaim, timing *embeddingJobTimingScope) error {
	if !claim.valid() {
		return fmt.Errorf("uci embedding worker: claimed job is invalid")
	}
	authorized, err := worker.authorizeWithTiming(root, claim, timing)
	if err != nil {
		if root.Err() != nil {
			return nil
		}
		return worker.transitionFailure(root, claim.Ref, EmbeddingFailure{Code: EmbeddingFailureAuthorityLost, Disposition: EmbeddingFailureCancelled})
	}

	jobContext, cancel := context.WithCancel(root)
	defer cancel()
	renewalDone := make(chan struct{})
	renewalErrors := make(chan error, 1)
	go worker.renew(jobContext, cancel, claim.Ref, renewalErrors, renewalDone)
	defer func() {
		cancel()
		<-renewalDone
	}()

	for {
		complete, err := worker.executeClaimBatch(root, jobContext, claim, authorized, renewalErrors, timing)
		if err != nil {
			return err
		}
		if complete {
			return nil
		}
	}
}

func (worker *EmbeddingWorker) authorizeWithTiming(ctx context.Context, claim EmbeddingJobClaim, timing *embeddingJobTimingScope) (AuthorizedContext, error) {
	if timing == nil {
		return worker.authorize(ctx, claim)
	}
	started := time.Now()
	authorized, err := worker.authorize(ctx, claim)
	timing.observe(embeddingJobTimingStageAuthorize, started, time.Now(), nil, embeddingJobTimingAuthorizationResultCode(err))
	return authorized, err
}

func (worker *EmbeddingWorker) executeClaimBatch(root, jobContext context.Context, claim EmbeddingJobClaim, authorized AuthorizedContext, renewalErrors <-chan error, timing *embeddingJobTimingScope) (bool, error) {
	batch, err := worker.prepareEmbeddingBatch(jobContext, claim, authorized, timing)
	if err != nil {
		return true, worker.finishRunningClaim(root, jobContext, renewalErrors, claim.Ref, err)
	}
	if !batch.validFor(claim) {
		return true, worker.transitionFailure(root, claim.Ref, EmbeddingFailure{Code: EmbeddingFailureProviderContract, Disposition: EmbeddingFailureTerminal})
	}
	if batch.Exhausted {
		err = worker.completeEmbeddingJob(jobContext, claim, authorized, batch, timing)
		if err != nil {
			return true, worker.finishRunningClaim(root, jobContext, renewalErrors, claim.Ref, err)
		}
		return true, nil
	}
	if len(batch.MissingInputIndexes) == 0 {
		return false, nil
	}
	vectors, err := worker.embedMissingWithTiming(jobContext, claim, batch, timing)
	if err != nil {
		return true, worker.finishRunningClaim(root, jobContext, renewalErrors, claim.Ref, err)
	}
	if err := worker.commitEmbeddingBatch(jobContext, claim, authorized, batch, vectors, timing); err != nil {
		return true, worker.finishRunningClaim(root, jobContext, renewalErrors, claim.Ref, err)
	}
	return false, nil
}

func (worker *EmbeddingWorker) prepareEmbeddingBatch(ctx context.Context, claim EmbeddingJobClaim, authorized AuthorizedContext, timing *embeddingJobTimingScope) (EmbeddingBatch, error) {
	if timing == nil {
		return worker.store.PrepareEmbeddingBatch(ctx, claim, authorized, worker.limits.CandidatePageSize)
	}
	started := time.Now()
	batch, err := worker.store.PrepareEmbeddingBatch(ctx, claim, authorized, worker.limits.CandidatePageSize)
	timing.observe(embeddingJobTimingStagePrepare, started, time.Now(), &batch, embeddingJobTimingResultCode(err))
	return batch, err
}

func (worker *EmbeddingWorker) completeEmbeddingJob(ctx context.Context, claim EmbeddingJobClaim, authorized AuthorizedContext, batch EmbeddingBatch, timing *embeddingJobTimingScope) error {
	if timing == nil {
		return worker.store.CompleteEmbeddingJob(ctx, claim, authorized)
	}
	started := time.Now()
	err := worker.store.CompleteEmbeddingJob(ctx, claim, authorized)
	timing.observe(embeddingJobTimingStageComplete, started, time.Now(), &batch, embeddingJobTimingResultCode(err))
	return err
}

func (worker *EmbeddingWorker) commitEmbeddingBatch(ctx context.Context, claim EmbeddingJobClaim, authorized AuthorizedContext, batch EmbeddingBatch, vectors [][]float32, timing *embeddingJobTimingScope) error {
	if timing == nil {
		return worker.store.CommitEmbeddingBatch(ctx, claim, authorized, batch, vectors)
	}
	started := time.Now()
	err := worker.store.CommitEmbeddingBatch(ctx, claim, authorized, batch, vectors)
	timing.observe(embeddingJobTimingStageCommit, started, time.Now(), &batch, embeddingJobTimingResultCode(err))
	return err
}

func (worker *EmbeddingWorker) finishRunningClaim(root, jobContext context.Context, renewalErrors <-chan error, ref EmbeddingJobRef, err error) error {
	if worker.leaseLost(root, jobContext, renewalErrors, err) {
		return nil
	}
	return worker.transitionFailure(root, ref, classifyEmbeddingFailure(err))
}

func (worker *EmbeddingWorker) authorize(ctx context.Context, claim EmbeddingJobClaim) (AuthorizedContext, error) {
	ref := claim.Ref.Context
	return worker.resolver.Authorize(ctx, ResolveContextInput{
		ClientSessionID: embeddingWorkerClientSessionID(claim.Ref),
		AuthRealm:       claim.Access.AuthRealm,
		Principal:       claim.Access.Principal,
		Ref:             &ref,
	})
}

func embeddingWorkerClientSessionID(ref EmbeddingJobRef) string {
	return embeddingWorkerClientSessionPrefix + ":" + ref.LeaseOwner + ":" + ref.JobID
}

func (worker *EmbeddingWorker) renew(ctx context.Context, cancel context.CancelFunc, ref EmbeddingJobRef, errors chan<- error, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(worker.limits.RenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := worker.store.RenewEmbeddingJob(ctx, ref, worker.limits.LeaseTTL); err != nil {
				select {
				case errors <- err:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func (worker *EmbeddingWorker) leaseLost(root, job context.Context, renewalErrors <-chan error, err error) bool {
	if root.Err() != nil {
		return true
	}
	if errors.Is(err, ErrEmbeddingJobLeaseLost) || errors.Is(err, ErrEmbeddingJobObsolete) || errors.Is(err, ErrEmbeddingJobCancelled) {
		return true
	}
	if job.Err() == nil {
		return false
	}
	select {
	case <-renewalErrors:
		return true
	default:
		return errors.Is(job.Err(), context.Canceled)
	}
}

func (worker *EmbeddingWorker) transitionFailure(ctx context.Context, ref EmbeddingJobRef, failure EmbeddingFailure) error {
	if ctx.Err() != nil {
		return nil
	}
	if !failure.valid() {
		return fmt.Errorf("uci embedding worker: invalid failure transition")
	}
	if err := worker.store.FailEmbeddingJob(ctx, ref, failure); err != nil && !errors.Is(err, ErrEmbeddingJobLeaseLost) && !errors.Is(err, ErrEmbeddingJobObsolete) && !errors.Is(err, ErrEmbeddingJobCancelled) {
		return err
	}
	return nil
}

type embeddingProviderInput struct {
	input   string
	indexes []int
}

type embeddingProviderBatch struct {
	start int
	texts []string
}

func (worker *EmbeddingWorker) embedMissing(ctx context.Context, claim EmbeddingJobClaim, batch EmbeddingBatch) ([][]float32, error) {
	return worker.embedMissingWithTiming(ctx, claim, batch, nil)
}

func (worker *EmbeddingWorker) embedMissingWithTiming(ctx context.Context, claim EmbeddingJobClaim, batch EmbeddingBatch, timing *embeddingJobTimingScope) ([][]float32, error) {
	if !batch.validFor(claim) {
		return nil, errEmbeddingProviderReply
	}
	inputs, vectors, err := embeddingProviderInputs(batch)
	if err != nil {
		return nil, err
	}
	providerBatches, err := worker.embeddingProviderBatches(inputs)
	if err != nil {
		return nil, err
	}
	if err := worker.embedProviderBatches(ctx, claim, batch, timing, inputs, providerBatches, vectors); err != nil {
		return nil, err
	}
	if !embeddingVectorsValid(vectors, claim.Profile.Dimension) {
		return nil, errEmbeddingProviderReply
	}
	return vectors, nil
}

func embeddingProviderInputs(batch EmbeddingBatch) ([]embeddingProviderInput, [][]float32, error) {
	positions := make(map[int]int, len(batch.MissingInputIndexes))
	for position, index := range batch.MissingInputIndexes {
		positions[index] = position
	}
	vectors := make([][]float32, len(batch.MissingInputIndexes))
	grouped := make(map[string]*embeddingProviderInput, len(batch.MissingInputIndexes))
	keys := make([]string, 0, len(batch.MissingInputIndexes))
	for _, index := range batch.MissingInputIndexes {
		candidate := batch.Candidates[index]
		key := candidate.ProtectionDomain + "\x00" + string(candidate.InputDigest)
		group, found := grouped[key]
		if !found {
			group = &embeddingProviderInput{input: candidate.Input}
			grouped[key] = group
			keys = append(keys, key)
		} else if group.input != candidate.Input {
			return nil, nil, errEmbeddingProviderReply
		}
		group.indexes = append(group.indexes, positions[index])
	}
	sort.Strings(keys)
	inputs := make([]embeddingProviderInput, 0, len(keys))
	for _, key := range keys {
		inputs = append(inputs, *grouped[key])
	}
	return inputs, vectors, nil
}

func (worker *EmbeddingWorker) embeddingProviderBatches(inputs []embeddingProviderInput) ([]embeddingProviderBatch, error) {
	batches := make([]embeddingProviderBatch, 0, (len(inputs)+worker.limits.ProviderBatchSize-1)/worker.limits.ProviderBatchSize)
	for start := 0; start < len(inputs); {
		batch, end, err := worker.embeddingProviderBatchAt(inputs, start)
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
		start = end
	}
	return batches, nil
}

func (worker *EmbeddingWorker) embeddingProviderBatchAt(inputs []embeddingProviderInput, start int) (embeddingProviderBatch, int, error) {
	end := start
	bytes := 0
	for end < len(inputs) && end-start < worker.limits.ProviderBatchSize {
		inputBytes := len(inputs[end].input)
		if inputBytes > worker.limits.MaxProviderBatchBytes {
			return embeddingProviderBatch{}, 0, ErrEmbeddingInputCapacity
		}
		if end > start && bytes+inputBytes > worker.limits.MaxProviderBatchBytes {
			break
		}
		bytes += inputBytes
		end++
	}
	if end == start {
		return embeddingProviderBatch{}, 0, ErrEmbeddingInputCapacity
	}
	texts := make([]string, end-start)
	for index := start; index < end; index++ {
		texts[index-start] = inputs[index].input
	}
	return embeddingProviderBatch{start: start, texts: texts}, end, nil
}

func (worker *EmbeddingWorker) embedProviderBatches(ctx context.Context, claim EmbeddingJobClaim, batch EmbeddingBatch, timing *embeddingJobTimingScope, inputs []embeddingProviderInput, providerBatches []embeddingProviderBatch, vectors [][]float32) error {
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(worker.limits.ProviderConcurrency)
	for _, providerBatch := range providerBatches {
		providerBatch := providerBatch
		group.Go(func() error {
			return worker.embedProviderBatch(groupContext, claim, batch, timing, inputs, providerBatch, vectors)
		})
	}
	return group.Wait()
}

func (worker *EmbeddingWorker) embedProviderBatch(ctx context.Context, claim EmbeddingJobClaim, batch EmbeddingBatch, timing *embeddingJobTimingScope, inputs []embeddingProviderInput, providerBatch embeddingProviderBatch, vectors [][]float32) error {
	callContext, cancel := context.WithTimeout(ctx, worker.limits.ProviderCallTimeout)
	var started, returned time.Time
	if timing != nil {
		started = time.Now()
	}
	returnedVectors, err := worker.embedder.Embed(callContext, providerBatch.texts)
	if timing != nil {
		returned = time.Now()
	}
	cancel()
	if timing != nil {
		timing.observe(embeddingJobTimingStageEmbed, started, returned, &batch, embeddingJobTimingResultCode(err))
	}
	if err != nil {
		return err
	}
	if len(returnedVectors) != len(providerBatch.texts) {
		return errEmbeddingProviderReply
	}
	for index, vector := range returnedVectors {
		if err := validateSemanticVector(vector, claim.Profile.Dimension); err != nil {
			return errEmbeddingProviderReply
		}
		for _, position := range inputs[providerBatch.start+index].indexes {
			vectors[position] = append([]float32(nil), vector...)
		}
	}
	return nil
}

func embeddingVectorsValid(vectors [][]float32, dimension int) bool {
	for _, vector := range vectors {
		if err := validateSemanticVector(vector, dimension); err != nil {
			return false
		}
	}
	return true
}

func classifyEmbeddingFailure(err error) EmbeddingFailure {
	switch {
	case errors.Is(err, ErrEmbeddingJobObsolete):
		return EmbeddingFailure{Code: EmbeddingFailureTargetObsolete, Disposition: EmbeddingFailureObsolete}
	case errors.Is(err, ErrEmbeddingJobCancelled):
		return EmbeddingFailure{Code: EmbeddingFailureAuthorityLost, Disposition: EmbeddingFailureCancelled}
	case errors.Is(err, ErrEmbeddingJobLeaseLost):
		return EmbeddingFailure{Code: EmbeddingFailureLeaseLost, Disposition: EmbeddingFailureCancelled}
	case errors.Is(err, ErrEmbeddingInputCapacity):
		return EmbeddingFailure{Code: EmbeddingFailureInputCapacity, Disposition: EmbeddingFailureTerminal}
	case errors.Is(err, errEmbeddingProviderReply):
		return EmbeddingFailure{Code: EmbeddingFailureMalformedResponse, Disposition: EmbeddingFailureTerminal}
	case errors.Is(err, context.DeadlineExceeded):
		return EmbeddingFailure{Code: EmbeddingFailureProviderUnavailable, Disposition: EmbeddingFailureRetry}
	}
	var status interface{ StatusCode() int }
	if errors.As(err, &status) {
		switch code := status.StatusCode(); {
		case code == 401 || code == 403:
			return EmbeddingFailure{Code: EmbeddingFailureProviderAuth, Disposition: EmbeddingFailureRetry}
		case code == 429:
			return EmbeddingFailure{Code: EmbeddingFailureProviderQuota, Disposition: EmbeddingFailureRetry}
		case code >= 500:
			return EmbeddingFailure{Code: EmbeddingFailureProviderUnavailable, Disposition: EmbeddingFailureRetry}
		case code >= 400:
			return EmbeddingFailure{Code: EmbeddingFailureProviderContract, Disposition: EmbeddingFailureTerminal}
		}
	}
	return EmbeddingFailure{Code: EmbeddingFailureDatabaseUnavailable, Disposition: EmbeddingFailureRetry}
}

func validEmbeddingOwner(value string) bool {
	return len(value) <= queryMaxClientSessionID && validIndexText(value)
}
