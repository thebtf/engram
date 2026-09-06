package uci

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// IndexStatusErrorCode is the closed outcome vocabulary for status lookups.
type IndexStatusErrorCode string

const (
	IndexStatusNotFound           IndexStatusErrorCode = "NOT_FOUND"
	IndexStatusUnavailable        IndexStatusErrorCode = "UNAVAILABLE"
	IndexStatusBarrierUnavailable IndexStatusErrorCode = "BARRIER_UNAVAILABLE"
)

var (
	ErrIndexStatusNotFound           = errors.New(string(IndexStatusNotFound))
	ErrIndexStatusUnavailable        = errors.New(string(IndexStatusUnavailable))
	ErrIndexStatusBarrierUnavailable = errors.New(string(IndexStatusBarrierUnavailable))
)

// IndexStatusError keeps internal diagnostics while exposing only a closed
// status outcome to callers.
type IndexStatusError struct {
	code  IndexStatusErrorCode
	cause error
}

// NewIndexStatusError creates one closed status error. An invalid code is
// conservatively represented as unavailable.
func NewIndexStatusError(code IndexStatusErrorCode, cause error) *IndexStatusError {
	if !code.valid() {
		code = IndexStatusUnavailable
		cause = fmt.Errorf("invalid index status error code")
	}
	return &IndexStatusError{code: code, cause: cause}
}

// Code returns the closed status outcome.
func (err *IndexStatusError) Code() IndexStatusErrorCode {
	if err == nil || !err.code.valid() {
		return ""
	}
	return err.code
}

// Error never discloses the wrapped diagnostic.
func (err *IndexStatusError) Error() string {
	return string(err.Code())
}

// Unwrap preserves the internal diagnostic for trusted callers.
func (err *IndexStatusError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// Is supports classification without exposing the underlying diagnostic.
func (err *IndexStatusError) Is(target error) bool {
	switch target {
	case ErrIndexStatusNotFound:
		return err.Code() == IndexStatusNotFound
	case ErrIndexStatusUnavailable:
		return err.Code() == IndexStatusUnavailable
	case ErrIndexStatusBarrierUnavailable:
		return err.Code() == IndexStatusBarrierUnavailable
	default:
		return false
	}
}

func (code IndexStatusErrorCode) valid() bool {
	switch code {
	case IndexStatusNotFound, IndexStatusUnavailable, IndexStatusBarrierUnavailable:
		return true
	default:
		return false
	}
}

// IndexStatusSourceState is the closed status vocabulary for a source that can
// still serve an already-authorized ContextRef.
type IndexStatusSourceState string

const (
	IndexStatusSourceActive  IndexStatusSourceState = "active"
	IndexStatusSourceOffline IndexStatusSourceState = "offline"
)

func (state IndexStatusSourceState) valid() bool {
	switch state {
	case IndexStatusSourceActive, IndexStatusSourceOffline:
		return true
	default:
		return false
	}
}

// IndexStatusCheckoutState is the closed status vocabulary for the selected
// checkout. Configured and unregistered checkouts do not serve status snapshots.
type IndexStatusCheckoutState string

const (
	IndexStatusCheckoutRegistered IndexStatusCheckoutState = "registered"
	IndexStatusCheckoutWatching   IndexStatusCheckoutState = "watching"
	IndexStatusCheckoutCatchingUp IndexStatusCheckoutState = "catching_up"
	IndexStatusCheckoutOffline    IndexStatusCheckoutState = "offline"
)

func (state IndexStatusCheckoutState) valid() bool {
	switch state {
	case IndexStatusCheckoutRegistered, IndexStatusCheckoutWatching, IndexStatusCheckoutCatchingUp, IndexStatusCheckoutOffline:
		return true
	default:
		return false
	}
}

// IndexStatusViewState is the closed status vocabulary for a View that remains
// queryable through an already-authorized ContextRef.
type IndexStatusViewState string

const (
	IndexStatusViewPublished  IndexStatusViewState = "published"
	IndexStatusViewSuperseded IndexStatusViewState = "superseded"
)

func (state IndexStatusViewState) valid() bool {
	switch state {
	case IndexStatusViewPublished, IndexStatusViewSuperseded:
		return true
	default:
		return false
	}
}

// IndexStatusCurrentViewRelation describes the selected View's exact relation
// to its checkout's current pointer without substituting another View.
type IndexStatusCurrentViewRelation string

const (
	IndexStatusCurrentViewSelected  IndexStatusCurrentViewRelation = "selected"
	IndexStatusCurrentViewDifferent IndexStatusCurrentViewRelation = "different"
	IndexStatusCurrentViewNone      IndexStatusCurrentViewRelation = "none"
)

func (relation IndexStatusCurrentViewRelation) valid() bool {
	switch relation {
	case IndexStatusCurrentViewSelected, IndexStatusCurrentViewDifferent, IndexStatusCurrentViewNone:
		return true
	default:
		return false
	}
}

// IndexStatusJobState is the closed durable-job vocabulary exposed by a status
// snapshot. It mirrors only the persisted state, not worker-local progress.
type IndexStatusJobState string

const (
	IndexStatusJobQueued         IndexStatusJobState = "queued"
	IndexStatusJobRunning        IndexStatusJobState = "running"
	IndexStatusJobRetryScheduled IndexStatusJobState = "retry_scheduled"
	IndexStatusJobSucceeded      IndexStatusJobState = "succeeded"
	IndexStatusJobFailedTerminal IndexStatusJobState = "failed_terminal"
	IndexStatusJobCancelled      IndexStatusJobState = "cancelled"
	IndexStatusJobObsolete       IndexStatusJobState = "obsolete"
)

func (state IndexStatusJobState) valid() bool {
	switch state {
	case IndexStatusJobQueued, IndexStatusJobRunning, IndexStatusJobRetryScheduled, IndexStatusJobSucceeded, IndexStatusJobFailedTerminal, IndexStatusJobCancelled, IndexStatusJobObsolete:
		return true
	default:
		return false
	}
}

func (state IndexStatusJobState) pending() bool {
	switch state {
	case IndexStatusJobQueued, IndexStatusJobRunning, IndexStatusJobRetryScheduled:
		return true
	default:
		return false
	}
}

// IndexStatusJob is the selected View's relevant durable job state.
type IndexStatusJob struct {
	State            IndexStatusJobState
	TargetGeneration *int64
}

func (job IndexStatusJob) clone() IndexStatusJob {
	copy := job
	if job.TargetGeneration != nil {
		targetGeneration := *job.TargetGeneration
		copy.TargetGeneration = &targetGeneration
	}
	return copy
}

// Validate checks that a status job uses the closed persisted vocabulary.
func (job IndexStatusJob) Validate() error {
	if !job.State.valid() {
		return fmt.Errorf("uci index status: invalid job state %q", job.State)
	}
	if job.TargetGeneration != nil && *job.TargetGeneration < 1 {
		return fmt.Errorf("uci index status: job target generation must be positive")
	}
	return nil
}

// EmbeddingStatus reports the exact configured-profile enrichment state for
// one selected View. Its candidate denominator is independent of query filters
// and publication-owned coverage.
type EmbeddingStatus struct {
	EmbeddingProfileID *string
	Coverage           IndexCoverageState
	TotalCandidates    uint64
	ReadyCandidates    uint64
	PendingJobs        uint64
	JobState           *IndexStatusJobState
	ErrorCode          *EmbeddingFailureCode
	RetryAfter         *time.Time
}

func (status EmbeddingStatus) clone() EmbeddingStatus {
	copy := status
	if status.EmbeddingProfileID != nil {
		profileID := *status.EmbeddingProfileID
		copy.EmbeddingProfileID = &profileID
	}
	if status.JobState != nil {
		jobState := *status.JobState
		copy.JobState = &jobState
	}
	if status.ErrorCode != nil {
		errorCode := *status.ErrorCode
		copy.ErrorCode = &errorCode
	}
	if status.RetryAfter != nil {
		retryAfter := status.RetryAfter.UTC()
		copy.RetryAfter = &retryAfter
	}
	return copy
}

func (status EmbeddingStatus) Validate() error {
	if !isIndexCoverageState(status.Coverage) || status.ReadyCandidates > status.TotalCandidates {
		return fmt.Errorf("uci embedding status: invalid coverage counts")
	}
	if status.EmbeddingProfileID == nil {
		if status.Coverage != IndexCoverageUnavailable || status.TotalCandidates != 0 || status.ReadyCandidates != 0 || status.PendingJobs != 0 || status.JobState != nil || status.ErrorCode != nil || status.RetryAfter != nil {
			return fmt.Errorf("uci embedding status: unavailable profile has state")
		}
		return nil
	}
	if !canonicalContextUUID(*status.EmbeddingProfileID) {
		return fmt.Errorf("uci embedding status: invalid profile")
	}
	if status.JobState != nil && !status.JobState.valid() {
		return fmt.Errorf("uci embedding status: invalid job state")
	}
	if status.ErrorCode != nil && !status.ErrorCode.valid() {
		return fmt.Errorf("uci embedding status: invalid error code")
	}
	if status.RetryAfter != nil && status.RetryAfter.IsZero() {
		return fmt.Errorf("uci embedding status: invalid retry time")
	}
	if status.PendingJobs > 0 && (status.JobState == nil || !status.JobState.pending()) {
		return fmt.Errorf("uci embedding status: pending jobs lack a pending state")
	}
	if status.Coverage == IndexCoverageComplete {
		if status.TotalCandidates == 0 || status.ReadyCandidates != status.TotalCandidates || status.PendingJobs != 0 || status.JobState == nil || *status.JobState != IndexStatusJobSucceeded || status.ErrorCode != nil || status.RetryAfter != nil {
			return fmt.Errorf("uci embedding status: complete coverage lacks exact completion proof")
		}
	}
	if status.TotalCandidates == 0 && status.ReadyCandidates != 0 {
		return fmt.Errorf("uci embedding status: empty denominator has ready candidates")
	}
	if status.ErrorCode != nil && *status.ErrorCode == EmbeddingFailureNoCandidates && (status.TotalCandidates != 0 || status.ReadyCandidates != 0 || status.Coverage != IndexCoverageUnavailable || status.JobState == nil || *status.JobState != IndexStatusJobSucceeded) {
		return fmt.Errorf("uci embedding status: no-candidates state is invalid")
	}
	return nil
}

// IndexStatusSnapshot is a storage-agnostic, exact-View status read. All
// counts are scoped through Context before they are calculated.
type IndexStatusSnapshot struct {
	Context                    ContextRef
	SourceState                IndexStatusSourceState
	CheckoutState              IndexStatusCheckoutState
	ViewState                  IndexStatusViewState
	CurrentViewRelation        IndexStatusCurrentViewRelation
	Dirty                      bool
	ObservedFSSeq              int64
	Coverage                   IndexCoverage
	PublishedAt                time.Time
	ScanStartedAt              time.Time
	ScanCompletedAt            time.Time
	ChunkCount                 uint64
	ReadyEmbeddingCount        uint64
	PendingPublicationJobCount uint64
	PublicationJob             *IndexStatusJob
	Embedding                  EmbeddingStatus
	Freshness                  QueryFreshness
}

// Clone returns a defensive copy of the status snapshot.
func (snapshot IndexStatusSnapshot) Clone() IndexStatusSnapshot {
	copy := snapshot
	copy.Context = snapshot.Context.clone()
	if snapshot.PublicationJob != nil {
		job := snapshot.PublicationJob.clone()
		copy.PublicationJob = &job
	}
	copy.Embedding = snapshot.Embedding.clone()
	return copy
}

// Validate checks closed status vocabularies and the exact freshness evidence
// derived from the selected View's durable state.
func (snapshot IndexStatusSnapshot) Validate() error {
	if !snapshot.Context.valid() {
		return fmt.Errorf("uci index status: invalid context")
	}
	if !snapshot.SourceState.valid() {
		return fmt.Errorf("uci index status: invalid source state %q", snapshot.SourceState)
	}
	if !snapshot.CheckoutState.valid() {
		return fmt.Errorf("uci index status: invalid checkout state %q", snapshot.CheckoutState)
	}
	if !snapshot.ViewState.valid() {
		return fmt.Errorf("uci index status: invalid view state %q", snapshot.ViewState)
	}
	if !snapshot.CurrentViewRelation.valid() {
		return fmt.Errorf("uci index status: invalid current view relation %q", snapshot.CurrentViewRelation)
	}
	if snapshot.ObservedFSSeq < 0 {
		return fmt.Errorf("uci index status: observed filesystem sequence must be non-negative")
	}
	if snapshot.PublishedAt.IsZero() || snapshot.ScanStartedAt.IsZero() || snapshot.ScanCompletedAt.IsZero() || snapshot.ScanCompletedAt.Before(snapshot.ScanStartedAt) {
		return fmt.Errorf("uci index status: invalid publication or scan timestamps")
	}
	if !isIndexCoverageState(snapshot.Coverage.Structural) || !isIndexCoverageState(snapshot.Coverage.Lexical) || !isIndexCoverageState(snapshot.Coverage.Vector) {
		return fmt.Errorf("uci index status: invalid coverage")
	}
	if snapshot.PublicationJob != nil {
		if err := snapshot.PublicationJob.Validate(); err != nil {
			return err
		}
	}
	if snapshot.PendingPublicationJobCount > 0 {
		if snapshot.PublicationJob == nil || !snapshot.PublicationJob.State.pending() {
			return fmt.Errorf("uci index status: pending publication jobs lack a pending publication state")
		}
	}
	if err := snapshot.Embedding.Validate(); err != nil {
		return err
	}
	if err := snapshot.Freshness.Validate(); err != nil {
		return fmt.Errorf("uci index status: invalid freshness: %w", err)
	}
	disposition, err := ClassifyQueryFreshness(snapshot.Freshness)
	if err != nil {
		return fmt.Errorf("uci index status: classify freshness: %w", err)
	}

	switch {
	case snapshot.SourceState == IndexStatusSourceOffline || snapshot.CheckoutState == IndexStatusCheckoutOffline:
		return snapshot.validateFreshness(QueryFreshnessOffline, QueryFreshnessNone, QueryEnrichmentUnavailable, nil, QueryFreshnessDispositionOffline, disposition)
	case snapshot.ViewState == IndexStatusViewSuperseded:
		return snapshot.validateFreshness(QueryFreshnessHistorical, QueryFreshnessPinnedHistory, QueryEnrichmentCurrent, nil, QueryFreshnessDispositionCurrent, disposition)
	case snapshot.ViewState == IndexStatusViewPublished:
		if snapshot.CurrentViewRelation != IndexStatusCurrentViewSelected {
			return fmt.Errorf("uci index status: published view is not the checkout current pointer")
		}
		if snapshot.CheckoutState == IndexStatusCheckoutCatchingUp || snapshot.PendingPublicationJobCount > 0 {
			return snapshot.validateFreshness(QueryFreshnessCatchingUp, QueryFreshnessWatchWatermark, QueryEnrichmentPending, nil, QueryFreshnessDispositionStale, disposition)
		}
		zero := int64(0)
		return snapshot.validateFreshness(QueryFreshnessObservedCurrent, QueryFreshnessWatchWatermark, QueryEnrichmentCurrent, &zero, QueryFreshnessDispositionCurrent, disposition)
	default:
		return fmt.Errorf("uci index status: unsupported status combination")
	}
}

func (snapshot IndexStatusSnapshot) validateFreshness(
	state QueryFreshnessState,
	method QueryFreshnessMethod,
	watermarkState QueryEnrichmentState,
	pending *int64,
	disposition QueryFreshnessDisposition,
	actualDisposition QueryFreshnessDisposition,
) error {
	freshness := snapshot.Freshness
	if freshness.State != state || freshness.Method != method || freshness.EnrichmentWatermark.Sequence != snapshot.ObservedFSSeq || freshness.EnrichmentWatermark.State != watermarkState || freshness.Barrier != nil || actualDisposition != disposition {
		return fmt.Errorf("uci index status: freshness does not match durable status")
	}
	if (freshness.PendingChanges == nil) != (pending == nil) || (pending != nil && *freshness.PendingChanges != *pending) {
		return fmt.Errorf("uci index status: freshness pending changes do not match durable status")
	}
	return nil
}

// IndexStatusStore loads a snapshot from the already-authorized exact View.
// It must not resolve a new context or substitute a checkout current View.
type IndexStatusStore interface {
	LoadIndexStatus(context.Context, AuthorizedContext, *VectorProfile) (IndexStatusSnapshot, error)
}

// IndexStatusService validates storage results and owns the explicit
// read-your-save capability boundary.
type IndexStatusService struct {
	store   IndexStatusStore
	profile *VectorProfile
}

// NewIndexStatusService creates a service for one injected exact-View store
// and an optional configured vector profile.
func NewIndexStatusService(store IndexStatusStore, profile *VectorProfile) *IndexStatusService {
	service := &IndexStatusService{store: store}
	if profile != nil {
		profileCopy := *profile
		service.profile = &profileCopy
	}
	return service
}

// Status returns real durable status for an empty barrier token. A nonempty
// token is intentionally refused until a genuine watcher/index token authority
// exists; PostgreSQL status alone cannot satisfy it.
func (service *IndexStatusService) Status(ctx context.Context, authorized AuthorizedContext, barrierToken string) (IndexStatusSnapshot, error) {
	if service == nil || service.store == nil {
		return IndexStatusSnapshot{}, fmt.Errorf("uci index status: store is not configured")
	}
	if ctx == nil {
		return IndexStatusSnapshot{}, fmt.Errorf("uci index status: context is required")
	}
	if err := ctx.Err(); err != nil {
		return IndexStatusSnapshot{}, err
	}
	ref := authorized.Ref()
	if !ref.valid() {
		return IndexStatusSnapshot{}, fmt.Errorf("uci index status: authorized context is invalid")
	}
	if barrierToken != "" {
		return IndexStatusSnapshot{}, NewIndexStatusError(IndexStatusBarrierUnavailable, ErrIndexStatusBarrierUnavailable)
	}

	snapshot, err := service.store.LoadIndexStatus(ctx, authorized, service.profile)
	if err != nil {
		return IndexStatusSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return IndexStatusSnapshot{}, err
	}
	if !indexStatusContextEqual(snapshot.Context, ref) {
		return IndexStatusSnapshot{}, fmt.Errorf("uci index status: store returned a different context")
	}
	if err := snapshot.Validate(); err != nil {
		return IndexStatusSnapshot{}, err
	}
	return snapshot.Clone(), nil
}

func indexStatusContextEqual(left, right ContextRef) bool {
	if left.SourceID != right.SourceID || left.CheckoutID != right.CheckoutID || left.ViewID != right.ViewID || left.AnalysisProfileID != right.AnalysisProfileID || left.Generation != right.Generation {
		return false
	}
	if left.SpaceID == nil || right.SpaceID == nil {
		return left.SpaceID == nil && right.SpaceID == nil
	}
	return *left.SpaceID == *right.SpaceID
}
