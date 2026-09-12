package gorm

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

func TestUCIIndexIntentStoreLifecycleIdempotencyAndReadableCompletion(t *testing.T) {
	store := openUCIIndexIntentStore(t)
	ctx := context.Background()
	input, previous, next := newUCIIndexIntentInput(ucidomain.IndexIntentReindex)

	submitted, err := store.SubmitIndexIntent(ctx, input)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentSubmitted, submitted.State)
	require.Equal(t, previous, *submitted.PreviousView)

	loaded, err := store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, submitted.ID, loaded.ID)
	require.Equal(t, submitted.State, loaded.State)
	require.Equal(t, previous, *loaded.PreviousView)
	loaded.PreviousView.ViewID = uuid.NewString()
	loaded, err = store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, previous, *loaded.PreviousView, "GetIndexIntent must return an independent value")
	_, err = store.GetIndexIntent(ctx, "not-an-intent-id")
	require.Error(t, err)

	replayed, err := store.SubmitIndexIntent(ctx, input.Clone())
	require.NoError(t, err)
	require.Equal(t, submitted.ID, replayed.ID, "an identical request reference must replay its durable intent")

	changed := input.Clone()
	changed.Kind = ucidomain.IndexIntentReconcile
	_, err = store.SubmitIndexIntent(ctx, changed)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentBindingMismatch)
	replayed, err = store.SubmitIndexIntent(ctx, input)
	require.NoError(t, err)
	require.Equal(t, submitted.ID, replayed.ID, "a changed binding must not create a second intent")

	queued, err := store.QueueIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentQueued, queued.State)

	claim, err := store.AcknowledgeIndexIntent(ctx, submitted.ID, "index-worker-a")
	require.NoError(t, err)
	acknowledged, err := store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentAcknowledged, acknowledged.State)
	require.Equal(t, 1, acknowledged.Attempt)
	require.NotNil(t, acknowledged.Acknowledgement)
	require.Equal(t, claim.Owner, acknowledged.Acknowledgement.Owner)

	wrongOwner := claim
	wrongOwner.Owner = "index-worker-b"
	_, err = store.StartIndexIntent(ctx, wrongOwner)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentOwnerLost)
	staleClaim := claim
	staleClaim.Epoch++
	_, err = store.StartIndexIntent(ctx, staleClaim)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentOwnerLost)
	_, err = store.QueueIndexIntent(ctx, submitted.ID)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentInvalidTransition)

	running, err := store.StartIndexIntent(ctx, claim)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentRunning, running.State)

	readable := indexIntentReadableViews{next.ViewID: true}
	wrongCompletion := claim
	wrongCompletion.Owner = "index-worker-completion-wrong"
	_, err = store.CompleteIndexIntent(ctx, wrongCompletion, next, readable)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentOwnerLost)
	staleCompletion := claim
	staleCompletion.Epoch++
	_, err = store.CompleteIndexIntent(ctx, staleCompletion, next, readable)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentOwnerLost)
	stored, err := store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentRunning, stored.State)

	_, err = store.CompleteIndexIntent(ctx, claim, ucidomain.ContextRef{}, readable)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentResultInvalid)
	stored, err = store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentRunning, stored.State)

	_, err = store.CompleteIndexIntent(ctx, claim, previous, readable)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentResultInvalid)
	stored, err = store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentRunning, stored.State)

	_, err = store.CompleteIndexIntent(ctx, claim, next, nil)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentResultUnreadable)
	stored, err = store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentRunning, stored.State)

	unreadable := next.Clone()
	unreadable.ViewID = uuid.NewString()
	_, err = store.CompleteIndexIntent(ctx, claim, unreadable, readable)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentResultUnreadable)
	stored, err = store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentRunning, stored.State)

	completed, err := store.CompleteIndexIntent(ctx, claim, next, readable)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentCompleted, completed.State)
	require.Equal(t, next, *completed.ResultView)
}

func TestUCIIndexIntentStoreConcurrentIdenticalSubmissionReturnsOneIntent(t *testing.T) {
	store := openUCIIndexIntentStore(t)
	ctx := context.Background()
	input, _, _ := newUCIIndexIntentInput(ucidomain.IndexIntentReindex)

	const submitters = 8
	intents := make([]ucidomain.IndexIntent, submitters)
	errs := make([]error, submitters)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(submitters)
	done.Add(submitters)
	start := make(chan struct{})
	for index := range intents {
		go func(index int) {
			defer done.Done()
			ready.Done()
			<-start
			intents[index], errs[index] = store.SubmitIndexIntent(ctx, input)
		}(index)
	}
	ready.Wait()
	close(start)
	done.Wait()

	for index := range intents {
		require.NoErrorf(t, errs[index], "submitter %d", index)
		require.Equalf(t, intents[0].ID, intents[index].ID, "submitter %d", index)
	}
	stored, err := store.GetIndexIntent(ctx, intents[0].ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentSubmitted, stored.State)
}

func TestUCIIndexIntentStoreDeliveryUpdatesReplayExactOwnerClaim(t *testing.T) {
	store := openUCIIndexIntentStore(t)
	ctx := context.Background()
	input, previous, _ := newUCIIndexIntentInput(ucidomain.IndexIntentReconcile)
	submitted, err := store.SubmitIndexIntent(ctx, input)
	require.NoError(t, err)
	_, err = store.QueueIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)

	binding := ucidomain.IndexBinding{
		Context:       &previous,
		Scope:         input.Scope,
		ProfileID:     input.ProfileID,
		LocalRootID:   uuid.NewString(),
		WorkstationID: uuid.NewString(),
	}
	owner := ucidomain.IndexIntentOwnerBinding{
		AuthRealm: "runtime-realm", Principal: "agent/runtime", WorkstationID: binding.WorkstationID,
		ClientSessionID: "runtime-session", ClientInstanceID: "runtime-instance", ProcessNonce: "runtime-process",
	}
	offered, err := store.PollIndexIntent(ctx, binding, owner)
	require.NoError(t, err)
	require.NotNil(t, offered)
	require.Equal(t, submitted.ID, offered.ID)
	require.Equal(t, ucidomain.IndexIntentQueued, offered.State)

	acknowledged, err := store.UpdateIndexIntent(ctx, binding, owner, submitted.ID, ucidomain.IndexIntentUpdate{
		OperationRef: "runtime-delivery/ack", Operation: ucidomain.IndexIntentAcknowledge,
	})
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentAcknowledged, acknowledged.State)
	require.Equal(t, 1, acknowledged.Attempt)
	require.EqualValues(t, 1, acknowledged.ClaimEpoch)

	replayed, err := store.UpdateIndexIntent(ctx, binding, owner, submitted.ID, ucidomain.IndexIntentUpdate{
		OperationRef: "runtime-delivery/ack", Operation: ucidomain.IndexIntentAcknowledge,
	})
	require.NoError(t, err)
	require.Equal(t, acknowledged, replayed)

	foreignOwner := owner
	foreignOwner.ProcessNonce = "foreign-process"
	_, err = store.UpdateIndexIntent(ctx, binding, foreignOwner, submitted.ID, ucidomain.IndexIntentUpdate{
		OperationRef: "runtime-delivery/ack", Operation: ucidomain.IndexIntentAcknowledge,
	})
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentBindingMismatch)

	started, err := store.UpdateIndexIntent(ctx, binding, owner, submitted.ID, ucidomain.IndexIntentUpdate{
		OperationRef: "runtime-delivery/start", Operation: ucidomain.IndexIntentStart, ClaimEpoch: acknowledged.ClaimEpoch,
	})
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentRunning, started.State)

	renewed, err := store.UpdateIndexIntent(ctx, binding, owner, submitted.ID, ucidomain.IndexIntentUpdate{
		OperationRef: "runtime-delivery/renew", Operation: ucidomain.IndexIntentRenew, ClaimEpoch: started.ClaimEpoch,
	})
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentRunning, renewed.State)
	require.NotZero(t, renewed.LeaseExpiresAt)
}

func TestUCIIndexIntentStoreUnavailableRetryAndFailurePreservePriorView(t *testing.T) {
	store := openUCIIndexIntentStore(t)
	ctx := context.Background()
	input, previous, _ := newUCIIndexIntentInput(ucidomain.IndexIntentReconcile)

	submitted, err := store.SubmitIndexIntent(ctx, input)
	require.NoError(t, err)
	unavailable, err := store.MarkIndexIntentUnavailable(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentUnavailable, unavailable.State)
	require.Equal(t, previous, *unavailable.PreviousView)
	require.Nil(t, unavailable.ResultView)

	_, err = store.RetryIndexIntent(ctx, submitted.ID, indexIntentRetryAuthorizer{allow: false})
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentRetryUnauthorized)
	stored, err := store.GetIndexIntent(ctx, submitted.ID)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentUnavailable, stored.State)

	queued, err := store.RetryIndexIntent(ctx, submitted.ID, indexIntentRetryAuthorizer{allow: true})
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentQueued, queued.State)
	require.Zero(t, queued.Attempt)
	replay, err := store.SubmitIndexIntent(ctx, input)
	require.NoError(t, err)
	require.Equal(t, submitted.ID, replay.ID, "retry must reuse the original durable intent")

	failedInput, failedPrevious, _ := newUCIIndexIntentInput(ucidomain.IndexIntentReindex)
	failedSubmitted, err := store.SubmitIndexIntent(ctx, failedInput)
	require.NoError(t, err)
	_, err = store.QueueIndexIntent(ctx, failedSubmitted.ID)
	require.NoError(t, err)
	claim, err := store.AcknowledgeIndexIntent(ctx, failedSubmitted.ID, "index-worker-failure")
	require.NoError(t, err)
	failed, err := store.FailIndexIntent(ctx, claim)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentFailed, failed.State)
	require.Equal(t, failedPrevious, *failed.PreviousView)
	require.Nil(t, failed.ResultView)
	failedReplay, err := store.SubmitIndexIntent(ctx, failedInput)
	require.NoError(t, err)
	require.Equal(t, failed.ID, failedReplay.ID)
	require.Equal(t, ucidomain.IndexIntentFailed, failedReplay.State)
}

func openUCIIndexIntentStore(t *testing.T) *UCIIndexIntentStore {
	t.Helper()
	db, _ := openInterventionReceiptMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate(&indexIntentRow{}))
	return NewUCIIndexIntentStore(db)
}

func newUCIIndexIntentInput(kind ucidomain.IndexIntentKind) (ucidomain.IndexIntentInput, ucidomain.ContextRef, ucidomain.ContextRef) {
	scope := ucidomain.IndexScope{
		SourceID:      uuid.NewString(),
		CheckoutID:    uuid.NewString(),
		IncarnationID: uuid.NewString(),
	}
	previous := ucidomain.ContextRef{
		SourceID:          scope.SourceID,
		CheckoutID:        scope.CheckoutID,
		ViewID:            uuid.NewString(),
		AnalysisProfileID: uuid.NewString(),
		Generation:        1,
	}
	next := previous.Clone()
	next.ViewID = uuid.NewString()
	next.Generation++
	return ucidomain.IndexIntentInput{
		RequestRef:   "index-intent-request-" + uuid.NewString(),
		Kind:         kind,
		Scope:        scope,
		ProfileID:    previous.AnalysisProfileID,
		PreviousView: &previous,
	}, previous, next
}

type indexIntentReadableViews map[string]bool

func (views indexIntentReadableViews) ReadableIndexIntentView(_ context.Context, ref ucidomain.ContextRef) error {
	if views[ref.ViewID] {
		return nil
	}
	return errors.New("view is not readable")
}

type indexIntentRetryAuthorizer struct {
	allow bool
}

func (authorizer indexIntentRetryAuthorizer) AuthorizeIndexIntentRetry(_ context.Context, _ ucidomain.IndexIntent) error {
	if authorizer.allow {
		return nil
	}
	return errors.New("retry is not authorized")
}
