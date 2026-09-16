package uci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	indexIntentMaxRequestRef   = 256
	indexIntentMaxOwner        = 256
	indexIntentMaxProcessNonce = 256
	indexIntentMaxOperationRef = 256
)

var (
	ErrIndexIntentNotFound          = errors.New("INDEX_INTENT_NOT_FOUND")
	ErrIndexIntentBindingMismatch   = errors.New("INDEX_INTENT_BINDING_MISMATCH")
	ErrIndexIntentInvalidTransition = errors.New("INDEX_INTENT_INVALID_TRANSITION")
	ErrIndexIntentOwnerLost         = errors.New("INDEX_INTENT_OWNER_LOST")
	ErrIndexIntentLeaseExpired      = errors.New("INDEX_INTENT_LEASE_EXPIRED")
	ErrIndexIntentRetryUnauthorized = errors.New("INDEX_INTENT_RETRY_UNAUTHORIZED")
	ErrIndexIntentResultInvalid     = errors.New("INDEX_INTENT_RESULT_INVALID")
	ErrIndexIntentResultUnreadable  = errors.New("INDEX_INTENT_RESULT_UNREADABLE")
)

// IndexIntentKind is the closed durable intent vocabulary.
type IndexIntentKind string

const (
	IndexIntentReindex   IndexIntentKind = "reindex"
	IndexIntentReconcile IndexIntentKind = "reconcile"
)

func (kind IndexIntentKind) valid() bool {
	return kind == IndexIntentReindex || kind == IndexIntentReconcile
}

// ValidIndexIntentRequestRef reports whether an opaque request reference is
// valid at the durable intent boundary.
func ValidIndexIntentRequestRef(value string) bool {
	return validIndexIntentText(value, indexIntentMaxRequestRef)
}

// IndexIntentState is the durable lifecycle for a requested index operation.
type IndexIntentState string

const (
	IndexIntentSubmitted    IndexIntentState = "submitted"
	IndexIntentQueued       IndexIntentState = "queued"
	IndexIntentAcknowledged IndexIntentState = "acknowledged"
	IndexIntentRunning      IndexIntentState = "running"
	IndexIntentCompleted    IndexIntentState = "completed"
	IndexIntentUnavailable  IndexIntentState = "unavailable"
	IndexIntentFailed       IndexIntentState = "failed"
)

func (state IndexIntentState) valid() bool {
	switch state {
	case IndexIntentSubmitted,
		IndexIntentQueued,
		IndexIntentAcknowledged,
		IndexIntentRunning,
		IndexIntentCompleted,
		IndexIntentUnavailable,
		IndexIntentFailed:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether the lifecycle permits a direct state transition.
// A matching owner’s post-completion fail report remains completed as a durable
// replay receipt; it is intentionally not a second lifecycle transition.
func (state IndexIntentState) CanTransitionTo(next IndexIntentState) bool {
	switch state {
	case IndexIntentSubmitted:
		return next == IndexIntentQueued || next == IndexIntentUnavailable
	case IndexIntentQueued:
		return next == IndexIntentAcknowledged || next == IndexIntentUnavailable
	case IndexIntentAcknowledged:
		return next == IndexIntentRunning || next == IndexIntentFailed
	case IndexIntentRunning:
		return next == IndexIntentCompleted || next == IndexIntentFailed
	case IndexIntentUnavailable:
		return next == IndexIntentQueued
	default:
		return false
	}
}

// Clone returns an independent ContextRef value for an IndexIntent boundary.
func (ref ContextRef) Clone() ContextRef {
	return ref.clone()
}

// IndexIntentInput is the safe, immutable binding for one opaque request reference.
type IndexIntentInput struct {
	RequestRef   string
	Kind         IndexIntentKind
	Scope        IndexScope
	ProfileID    string
	PreviousView *ContextRef
}

// Clone returns an independent input value.
func (input IndexIntentInput) Clone() IndexIntentInput {
	copy := input
	copy.PreviousView = cloneIndexIntentContext(input.PreviousView)
	return copy
}

// Validate checks that an intent contains only a complete safe identity tuple.
func (input IndexIntentInput) Validate() error {
	if !ValidIndexIntentRequestRef(input.RequestRef) {
		return fmt.Errorf("uci index intent: invalid request reference")
	}
	if !input.Kind.valid() || !validIndexScope(input.Scope) || !canonicalContextUUID(input.ProfileID) {
		return fmt.Errorf("uci index intent: invalid binding")
	}
	if input.PreviousView == nil {
		return nil
	}
	if !input.PreviousView.valid() ||
		input.PreviousView.SourceID != input.Scope.SourceID ||
		input.PreviousView.CheckoutID != input.Scope.CheckoutID ||
		input.PreviousView.AnalysisProfileID != input.ProfileID {
		return fmt.Errorf("uci index intent: invalid previous view")
	}
	return nil
}

// IndexIntentClaim is the owner-fenced acknowledgement required to start, fail,
// renew, link, or complete an intent.
type IndexIntentClaim struct {
	IntentID       string
	Owner          string
	Epoch          int64
	AcknowledgedAt time.Time
	LeaseExpiresAt time.Time
}

// NewIndexIntentClaim retains the original constructor for local callers while
// using the publication lease policy for the claim lifetime.
func NewIndexIntentClaim(intentID, owner string, epoch int64, acknowledgedAt time.Time) (IndexIntentClaim, error) {
	return NewLeasedIndexIntentClaim(intentID, owner, epoch, acknowledgedAt, acknowledgedAt.Add(DefaultIndexPublicationLimits().LeaseTTL))
}

// NewLeasedIndexIntentClaim validates exact persisted claim metadata.
func NewLeasedIndexIntentClaim(intentID, owner string, epoch int64, acknowledgedAt, leaseExpiresAt time.Time) (IndexIntentClaim, error) {
	claim := IndexIntentClaim{
		IntentID: intentID, Owner: owner, Epoch: epoch,
		AcknowledgedAt: acknowledgedAt.UTC(), LeaseExpiresAt: leaseExpiresAt.UTC(),
	}
	if err := claim.Validate(); err != nil {
		return IndexIntentClaim{}, err
	}
	return claim, nil
}

// Validate checks a complete owner claim.
func (claim IndexIntentClaim) Validate() error {
	if !canonicalContextUUID(claim.IntentID) || !validIndexIntentText(claim.Owner, indexIntentMaxOwner) || claim.Epoch < 1 ||
		claim.AcknowledgedAt.IsZero() || !claim.LeaseExpiresAt.After(claim.AcknowledgedAt) {
		return fmt.Errorf("uci index intent: invalid owner claim")
	}
	return nil
}

// IndexIntent is the safe durable record. It contains no host, source, or
// provider details; Views are opaque UCI identities.
type IndexIntent struct {
	ID                 string
	RequestRef         string
	Kind               IndexIntentKind
	Scope              IndexScope
	ProfileID          string
	PreviousView       *ContextRef
	State              IndexIntentState
	Attempt            int
	Acknowledgement    *IndexIntentClaim
	PublicationBuildID string
	ResultView         *ContextRef
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Clone returns an independent durable record.
func (intent IndexIntent) Clone() IndexIntent {
	copy := intent
	copy.PreviousView = cloneIndexIntentContext(intent.PreviousView)
	copy.ResultView = cloneIndexIntentContext(intent.ResultView)
	if intent.Acknowledgement != nil {
		claim := *intent.Acknowledgement
		copy.Acknowledgement = &claim
	}
	return copy
}

// Validate checks the lifecycle metadata and every identity visible at this boundary.
func (intent IndexIntent) Validate() error {
	if !canonicalContextUUID(intent.ID) || intent.Attempt < 0 || !intent.State.valid() {
		return fmt.Errorf("uci index intent: invalid durable record")
	}
	if err := (IndexIntentInput{
		RequestRef:   intent.RequestRef,
		Kind:         intent.Kind,
		Scope:        intent.Scope,
		ProfileID:    intent.ProfileID,
		PreviousView: intent.PreviousView,
	}).Validate(); err != nil {
		return err
	}

	requiresClaim := intent.State == IndexIntentAcknowledged || intent.State == IndexIntentRunning || intent.State == IndexIntentCompleted || intent.State == IndexIntentFailed
	if requiresClaim {
		if intent.Acknowledgement == nil || intent.Acknowledgement.IntentID != intent.ID || intent.Attempt < 1 {
			return fmt.Errorf("uci index intent: acknowledged state lacks an owner claim")
		}
		if err := intent.Acknowledgement.Validate(); err != nil {
			return err
		}
	} else if intent.Acknowledgement != nil || intent.Attempt != 0 {
		return fmt.Errorf("uci index intent: unacknowledged state carries owner metadata")
	}

	if intent.State == IndexIntentCompleted {
		if intent.ResultView == nil {
			return ErrIndexIntentResultInvalid
		}
		return ValidateIndexIntentResult(intent, *intent.ResultView)
	}
	if intent.ResultView != nil {
		return fmt.Errorf("uci index intent: non-completed state has a result")
	}
	return nil
}

// ValidateIndexIntentResult checks that a result is in the intent's exact
// source, checkout, profile, and space lineage. A publisher-proven semantic
// no-op may return the exact previous View; otherwise the result must advance.
func ValidateIndexIntentResult(intent IndexIntent, result ContextRef) error {
	if !result.valid() || result.SourceID != intent.Scope.SourceID || result.CheckoutID != intent.Scope.CheckoutID || result.AnalysisProfileID != intent.ProfileID {
		return ErrIndexIntentResultInvalid
	}
	previous := intent.PreviousView
	if previous == nil {
		return nil
	}
	if !indexIntentSameSpace(result.SpaceID, previous.SpaceID) {
		return ErrIndexIntentResultInvalid
	}
	if result.ViewID == previous.ViewID && result.Generation == previous.Generation {
		return nil
	}
	if result.ViewID == previous.ViewID || result.Generation <= previous.Generation {
		return ErrIndexIntentResultInvalid
	}
	return nil
}

// IndexIntentReadableView proves that a newly published result is presently
// readable through the private UCI read boundary.
type IndexIntentReadableView interface {
	ReadableIndexIntentView(context.Context, ContextRef) error
}

// IndexIntentRetryAuthorizer authorizes a retry without persisting caller or
// transport details on the intent.
type IndexIntentRetryAuthorizer interface {
	AuthorizeIndexIntentRetry(context.Context, IndexIntent) error
}

// IndexIntentStore owns durable idempotency and state transitions. It does not
// schedule, execute, or publish indexing work.
type IndexIntentStore interface {
	SubmitIndexIntent(context.Context, IndexIntentInput) (IndexIntent, error)
	GetIndexIntent(context.Context, string) (IndexIntent, error)
	QueueIndexIntent(context.Context, string) (IndexIntent, error)
	AcknowledgeIndexIntent(context.Context, string, string) (IndexIntentClaim, error)
	StartIndexIntent(context.Context, IndexIntentClaim) (IndexIntent, error)
	CompleteIndexIntent(context.Context, IndexIntentClaim, ContextRef, IndexIntentReadableView) (IndexIntent, error)
	MarkIndexIntentUnavailable(context.Context, string) (IndexIntent, error)
	RetryIndexIntent(context.Context, string, IndexIntentRetryAuthorizer) (IndexIntent, error)
	FailIndexIntent(context.Context, IndexIntentClaim) (IndexIntent, error)
}

// IndexIntentOperation is the closed daemon transition vocabulary.
type IndexIntentOperation string

const (
	IndexIntentAcknowledge IndexIntentOperation = "acknowledge"
	IndexIntentStart       IndexIntentOperation = "start"
	IndexIntentRenew       IndexIntentOperation = "renew"
	IndexIntentFail        IndexIntentOperation = "fail"
)

func (operation IndexIntentOperation) Valid() bool {
	switch operation {
	case IndexIntentAcknowledge, IndexIntentStart, IndexIntentRenew, IndexIntentFail:
		return true
	default:
		return false
	}
}

// IndexIntentOwnerBinding is the authenticated executor incarnation. OwnerKey
// returns a non-reversible durable fence; none of these transport facts are
// persisted individually on the intent.
type IndexIntentOwnerBinding struct {
	AuthRealm        string
	Principal        string
	WorkstationID    string
	ClientSessionID  string
	ClientInstanceID string
	ProcessNonce     string
}

func (binding IndexIntentOwnerBinding) OwnerKey() (string, error) {
	values := []string{binding.AuthRealm, binding.Principal, binding.WorkstationID, binding.ClientSessionID, binding.ClientInstanceID, binding.ProcessNonce}
	for _, value := range values {
		if !validIndexIntentText(value, indexIntentMaxOwner) {
			return "", fmt.Errorf("uci index intent: invalid owner binding")
		}
	}
	if !validIndexIntentText(binding.ProcessNonce, indexIntentMaxProcessNonce) {
		return "", fmt.Errorf("uci index intent: invalid process nonce")
	}
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// IndexIntentUpdate is one replay-keyed daemon transition.
type IndexIntentUpdate struct {
	OperationRef string
	Operation    IndexIntentOperation
	ClaimEpoch   int64
}

func (update IndexIntentUpdate) Validate() error {
	if !validIndexIntentText(update.OperationRef, indexIntentMaxOperationRef) || !update.Operation.Valid() || update.ClaimEpoch < 0 {
		return fmt.Errorf("uci index intent: invalid update")
	}
	if update.Operation == IndexIntentAcknowledge && update.ClaimEpoch != 0 {
		return fmt.Errorf("uci index intent: acknowledge must not carry an epoch")
	}
	if update.Operation != IndexIntentAcknowledge && update.ClaimEpoch < 1 {
		return fmt.Errorf("uci index intent: claimed update requires an epoch")
	}
	return nil
}

// IndexIntentExecutionClaim is the daemon-held token attached to publication
// messages. ProcessNonce is never accepted as authority without the current
// authenticated transport caller.
type IndexIntentExecutionClaim struct {
	IntentID       string
	Epoch          int64
	ProcessNonce   string
	LeaseExpiresAt time.Time
}

func (claim IndexIntentExecutionClaim) Validate() error {
	if !canonicalContextUUID(claim.IntentID) || claim.Epoch < 1 || !validIndexIntentText(claim.ProcessNonce, indexIntentMaxProcessNonce) || claim.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("uci index intent: invalid execution claim")
	}
	return nil
}

// IndexIntentUpdateResult is the exact durable receipt replayed for one
// operation reference.
type IndexIntentUpdateResult struct {
	IntentID       string
	State          IndexIntentState
	Attempt        int
	ClaimEpoch     int64
	LeaseExpiresAt time.Time
}

func (result IndexIntentUpdateResult) Validate() error {
	if !canonicalContextUUID(result.IntentID) || !result.State.valid() || result.Attempt < 0 || result.ClaimEpoch < 0 {
		return fmt.Errorf("uci index intent: invalid update result")
	}
	if result.ClaimEpoch == 0 {
		if !result.LeaseExpiresAt.IsZero() {
			return fmt.Errorf("uci index intent: unclaimed result has a lease")
		}
		return nil
	}
	if result.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("uci index intent: claimed result lacks a lease")
	}
	return nil
}

func cloneIndexIntentContext(value *ContextRef) *ContextRef {
	if value == nil {
		return nil
	}
	copy := value.Clone()
	return &copy
}

func indexIntentSameSpace(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func validIndexIntentText(value string, maximum int) bool {
	return len(value) <= maximum && validIndexText(value)
}
