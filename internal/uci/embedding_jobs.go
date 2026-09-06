package uci

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
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
	if batch.StartAfter != nil && !batch.StartAfter.valid() {
		return false
	}
	if batch.NextAfter != nil && !batch.NextAfter.valid() {
		return false
	}
	if len(batch.Candidates) == 0 {
		return len(batch.MissingInputIndexes) == 0 && (batch.Exhausted || batch.NextAfter == nil)
	}
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
		CandidatePageSize:     128,
		ProviderBatchSize:     32,
		MaxProviderBatchBytes: 524288,
		LeaseTTL:              2 * time.Minute,
		RenewInterval:         30 * time.Second,
		ProviderCallTimeout:   100 * time.Second,
		PollInterval:          time.Second,
	}
}

func (limits EmbeddingWorkerLimits) valid() bool {
	return limits.CandidatePageSize > 0 && limits.ProviderBatchSize > 0 && limits.ProviderBatchSize <= limits.CandidatePageSize &&
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
	profile  VectorProfile
	embedder SemanticEmbedder
	store    EmbeddingJobStore
	resolver *ContextResolver
	limits   EmbeddingWorkerLimits
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
	return &EmbeddingWorker{profile: profile, embedder: embedder, store: store, resolver: resolver, limits: limits}, nil
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

		claim, claimed, err := worker.store.ClaimEmbeddingJob(ctx, worker.profile, owner, worker.limits.LeaseTTL)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if claimed {
			if err := worker.runClaim(ctx, claim); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			timer.Reset(0)
			continue
		}
		timer.Reset(worker.limits.PollInterval)
	}
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

func (worker *EmbeddingWorker) runClaim(root context.Context, claim EmbeddingJobClaim) error {
	if !claim.valid() {
		return fmt.Errorf("uci embedding worker: claimed job is invalid")
	}
	authorized, err := worker.authorize(root, claim)
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
		batch, err := worker.store.PrepareEmbeddingBatch(jobContext, claim, authorized, worker.limits.CandidatePageSize)
		if err != nil {
			if worker.leaseLost(root, jobContext, renewalErrors, err) {
				return nil
			}
			return worker.transitionFailure(root, claim.Ref, classifyEmbeddingFailure(err))
		}
		if !batch.validFor(claim) {
			return worker.transitionFailure(root, claim.Ref, EmbeddingFailure{Code: EmbeddingFailureProviderContract, Disposition: EmbeddingFailureTerminal})
		}
		if batch.Exhausted {
			err = worker.store.CompleteEmbeddingJob(jobContext, claim, authorized)
			if err == nil {
				return nil
			}
			if worker.leaseLost(root, jobContext, renewalErrors, err) {
				return nil
			}
			return worker.transitionFailure(root, claim.Ref, classifyEmbeddingFailure(err))
		}
		if len(batch.MissingInputIndexes) == 0 {
			continue
		}

		vectors, err := worker.embedMissing(jobContext, claim, batch)
		if err != nil {
			if worker.leaseLost(root, jobContext, renewalErrors, err) {
				return nil
			}
			return worker.transitionFailure(root, claim.Ref, classifyEmbeddingFailure(err))
		}
		if err := worker.store.CommitEmbeddingBatch(jobContext, claim, authorized, batch, vectors); err != nil {
			if worker.leaseLost(root, jobContext, renewalErrors, err) {
				return nil
			}
			return worker.transitionFailure(root, claim.Ref, classifyEmbeddingFailure(err))
		}
	}
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

func (worker *EmbeddingWorker) embedMissing(ctx context.Context, claim EmbeddingJobClaim, batch EmbeddingBatch) ([][]float32, error) {
	if !batch.validFor(claim) {
		return nil, errEmbeddingProviderReply
	}
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
			return nil, errEmbeddingProviderReply
		}
		group.indexes = append(group.indexes, positions[index])
	}
	sort.Strings(keys)
	inputs := make([]embeddingProviderInput, 0, len(keys))
	for _, key := range keys {
		inputs = append(inputs, *grouped[key])
	}

	for start := 0; start < len(inputs); {
		end := start
		bytes := 0
		for end < len(inputs) && end-start < worker.limits.ProviderBatchSize {
			inputBytes := len(inputs[end].input)
			if inputBytes > worker.limits.MaxProviderBatchBytes {
				return nil, ErrEmbeddingInputCapacity
			}
			if end > start && bytes+inputBytes > worker.limits.MaxProviderBatchBytes {
				break
			}
			bytes += inputBytes
			end++
		}
		if end == start {
			return nil, ErrEmbeddingInputCapacity
		}
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = inputs[index].input
		}
		callContext, cancel := context.WithTimeout(ctx, worker.limits.ProviderCallTimeout)
		returned, err := worker.embedder.Embed(callContext, texts)
		cancel()
		if err != nil {
			return nil, err
		}
		if len(returned) != len(texts) {
			return nil, errEmbeddingProviderReply
		}
		for index, vector := range returned {
			if err := validateSemanticVector(vector, claim.Profile.Dimension); err != nil {
				return nil, errEmbeddingProviderReply
			}
			for _, position := range inputs[start+index].indexes {
				vectors[position] = append([]float32(nil), vector...)
			}
		}
		start = end
	}
	for _, vector := range vectors {
		if err := validateSemanticVector(vector, claim.Profile.Dimension); err != nil {
			return nil, errEmbeddingProviderReply
		}
	}
	return vectors, nil
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
