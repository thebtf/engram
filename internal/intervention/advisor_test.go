package intervention

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/taskmemory"
)

func TestRuntimeAdvisorPreparesOnceThenCommitsOnlyNoCandidates(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	input := runtimeAdviseInput(t, "no candidates")
	prepared := runtimePreparedTaskMemory(t, 0)
	order := []string{}
	preparer := &runtimeRecordingPreparer{prepared: prepared, order: &order}
	store := &runtimeReceiptStore{order: &order}
	advisor := newRuntimeAdvisorForTest(t, preparer, store, fixtureKeyEpoch(t), now)

	decision, err := advisor.Advise(ctx, input)
	if err != nil {
		t.Fatalf("Advise() error = %v", err)
	}
	receipt, reason, ok := decision.Abstain()
	if !ok || reason != AbstentionNoCandidates || !receipt.valid() {
		t.Fatalf("Advise() = %#v", decision)
	}
	if preparer.calls != 1 || store.lookupCalls != 1 || store.commitCalls != 1 {
		t.Fatalf("prepare=%d lookup=%d commit=%d", preparer.calls, store.lookupCalls, store.commitCalls)
	}
	if got, want := order, []string{"prepare", "lookup", "commit"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if record := store.committed.PersistenceRecord(); record.Outcome != ReceiptOutcomeAbstain || record.ClosedReason == nil || *record.ClosedReason != AbstentionNoCandidates || record.EvaluatedCount != 0 || record.EligibleCount != 0 || string(record.SnapshotRefsJSON) != "[]" || record.Selection != nil {
		t.Fatalf("committed no-candidates record = %#v", record)
	}
	if !store.committed.CanCommit() {
		t.Fatal("advisor committed a receipt that was not authorable")
	}
}

func TestRuntimeAdvisorMapsExactReceiptReplays(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	epoch := fixtureKeyEpoch(t)
	input := runtimeAdviseInput(t, "same replay")
	prepared := runtimePreparedTaskMemory(t, 0)
	axis := runtimeAxis(t, epoch, input, prepared)

	for _, testCase := range []struct {
		name    string
		receipt Receipt
		assert  func(t *testing.T, decision Decision)
	}{
		{
			name:    "abstain",
			receipt: mustUnverifiedReceipt(t, mustNoCandidatesReceipt(t, ctx, epoch, axis, "30000000-0000-4000-8000-000000000001", "30000000-0000-4000-8000-000000000002")),
			assert: func(t *testing.T, decision Decision) {
				t.Helper()
				if receipt, reason, ok := decision.Abstain(); !ok || !receipt.valid() || reason != AbstentionNoCandidates {
					t.Fatalf("exact ABSTAIN replay = %#v", decision)
				}
			},
		},
		{
			name:    "emit becomes delivery ambiguous",
			receipt: mustUnverifiedReceipt(t, mustEmittedReceipt(t, ctx, epoch, axis, "30000000-0000-4000-8000-000000000003", "30000000-0000-4000-8000-000000000004")),
			assert: func(t *testing.T, decision Decision) {
				t.Helper()
				if receipt, ok := decision.DeliveryAmbiguous(); !ok || !receipt.valid() {
					t.Fatalf("exact EMIT replay = %#v", decision)
				}
				if _, _, ok := decision.Emit(); ok {
					t.Fatal("exact EMIT replay returned a packet")
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			order := []string{}
			preparer := &runtimeRecordingPreparer{prepared: prepared, order: &order}
			store := &runtimeReceiptStore{lookupReceipt: testCase.receipt, lookupFound: true, order: &order}
			advisor := newRuntimeAdvisorForTest(t, preparer, store, epoch, now)

			decision, err := advisor.Advise(ctx, input)
			if err != nil {
				t.Fatalf("Advise() error = %v", err)
			}
			testCase.assert(t, decision)
			if preparer.calls != 1 || store.lookupCalls != 1 || store.commitCalls != 0 {
				t.Fatalf("prepare=%d lookup=%d commit=%d", preparer.calls, store.lookupCalls, store.commitCalls)
			}
		})
	}
}

func TestRuntimeAdvisorRejectsContentConflictWithoutCommit(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	epoch := fixtureKeyEpoch(t)
	firstInput := runtimeAdviseInput(t, "first content")
	replayInput := runtimeAdviseInput(t, "changed content")
	prepared := runtimePreparedTaskMemory(t, 0)
	firstAxis := runtimeAxis(t, epoch, firstInput, prepared)
	stored := mustUnverifiedReceipt(t, mustNoCandidatesReceipt(t, ctx, epoch, firstAxis, "30000000-0000-4000-8000-000000000005", "30000000-0000-4000-8000-000000000006"))
	order := []string{}
	preparer := &runtimeRecordingPreparer{prepared: prepared, order: &order}
	store := &runtimeReceiptStore{lookupReceipt: stored, lookupFound: true, order: &order}
	advisor := newRuntimeAdvisorForTest(t, preparer, store, epoch, now)

	decision, err := advisor.Advise(ctx, replayInput)
	if err != nil {
		t.Fatalf("Advise() error = %v", err)
	}
	unavailable, ok := decision.Unavailable()
	if !ok || unavailable.Code() != UnavailableReplayConflict {
		t.Fatalf("content conflict = %#v", decision)
	}
	if store.commitCalls != 0 {
		t.Fatalf("content conflict committed %d receipts", store.commitCalls)
	}
}

func TestRuntimeAdvisorLeavesNonemptyCandidatesUnavailable(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	input := runtimeAdviseInput(t, "candidate present")
	prepared := runtimePreparedTaskMemory(t, 1)
	preparer := &runtimeRecordingPreparer{prepared: prepared}
	store := &runtimeReceiptStore{}
	advisor := newRuntimeAdvisorForTest(t, preparer, store, fixtureKeyEpoch(t), now)

	decision, err := advisor.Advise(ctx, input)
	if err != nil {
		t.Fatalf("Advise() error = %v", err)
	}
	unavailable, ok := decision.Unavailable()
	if !ok || unavailable.Code() != UnavailableDependency {
		t.Fatalf("nonempty candidate result = %#v", decision)
	}
	if preparer.calls != 1 || store.lookupCalls != 1 || store.commitCalls != 0 {
		t.Fatalf("prepare=%d lookup=%d commit=%d", preparer.calls, store.lookupCalls, store.commitCalls)
	}
}

func TestRuntimeAdvisorDeadlineBeforeStoreWritesNothing(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(CommitReserve+ResponseReserve-time.Microsecond))
	defer cancel()
	preparer := &runtimeRecordingPreparer{prepared: runtimePreparedTaskMemory(t, 0)}
	store := &runtimeReceiptStore{}
	advisor := newRuntimeAdvisorForTest(t, preparer, store, fixtureKeyEpoch(t), now)

	decision, err := advisor.Advise(ctx, runtimeAdviseInput(t, "late callback"))
	if err != nil {
		t.Fatalf("Advise() error = %v", err)
	}
	unavailable, ok := decision.Unavailable()
	if !ok || unavailable.Code() != UnavailableDeadline {
		t.Fatalf("late callback result = %#v", decision)
	}
	if preparer.calls != 1 || store.lookupCalls != 0 || store.commitCalls != 0 {
		t.Fatalf("prepare=%d lookup=%d commit=%d", preparer.calls, store.lookupCalls, store.commitCalls)
	}
}

func TestRuntimeAdvisorMapsPrepareTimeOuterDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	preparer := &runtimeRecordingPreparer{
		prepare: func(ctx context.Context, _ taskmemory.PrepareRequest) (taskmemory.PreparedTaskMemory, error) {
			<-ctx.Done()
			return taskmemory.PreparedTaskMemory{}, taskmemory.ErrUnavailable
		},
	}
	store := &runtimeReceiptStore{}
	advisor := newRuntimeAdvisorForTest(t, preparer, store, fixtureKeyEpoch(t), interventionTestTime)

	decision, err := advisor.Advise(ctx, runtimeAdviseInput(t, "prepare deadline"))
	if err != nil {
		t.Fatalf("Advise() error = %v", err)
	}
	unavailable, ok := decision.Unavailable()
	if !ok || unavailable.Code() != UnavailableDeadline {
		t.Fatalf("prepare-time deadline result = %#v", decision)
	}
	if preparer.calls != 1 || store.lookupCalls != 0 || store.commitCalls != 0 {
		t.Fatalf("prepare=%d lookup=%d commit=%d", preparer.calls, store.lookupCalls, store.commitCalls)
	}
}

func TestRuntimeAdvisorKeepsLivePreparerDeadlineDependency(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), interventionTestTime.Add(time.Second))
	defer cancel()
	preparer := &runtimeRecordingPreparer{err: context.DeadlineExceeded}
	store := &runtimeReceiptStore{}
	advisor := newRuntimeAdvisorForTest(t, preparer, store, fixtureKeyEpoch(t), interventionTestTime)

	decision, err := advisor.Advise(ctx, runtimeAdviseInput(t, "preparer deadline"))
	if err != nil {
		t.Fatalf("Advise() error = %v", err)
	}
	unavailable, ok := decision.Unavailable()
	if !ok || unavailable.Code() != UnavailableDependency {
		t.Fatalf("live-callback preparer deadline result = %#v", decision)
	}
	if preparer.calls != 1 || store.lookupCalls != 0 || store.commitCalls != 0 {
		t.Fatalf("prepare=%d lookup=%d commit=%d", preparer.calls, store.lookupCalls, store.commitCalls)
	}
}

func TestRuntimeAdvisorObserveRemainsUnavailable(t *testing.T) {
	advisor := newRuntimeAdvisorForTest(t, &runtimeRecordingPreparer{}, &runtimeReceiptStore{}, fixtureKeyEpoch(t), interventionTestTime)
	acknowledgement, err := advisor.Observe(context.Background(), ObserveInput{})
	if err != nil || acknowledgement.State() != ObservationUnavailable || acknowledgement.Reason() != ObservationReasonDependencyUnavailable {
		t.Fatalf("Observe() = (%#v, %v)", acknowledgement, err)
	}
}

type runtimeRecordingPreparer struct {
	prepared taskmemory.PreparedTaskMemory
	err      error
	prepare  func(context.Context, taskmemory.PrepareRequest) (taskmemory.PreparedTaskMemory, error)
	calls    int
	order    *[]string
}

func (p *runtimeRecordingPreparer) Prepare(ctx context.Context, request taskmemory.PrepareRequest) (taskmemory.PreparedTaskMemory, error) {
	p.calls++
	if p.order != nil {
		*p.order = append(*p.order, "prepare")
	}
	if p.prepare != nil {
		return p.prepare(ctx, request)
	}
	return p.prepared, p.err
}

type runtimeReceiptStore struct {
	lookupReceipt  Receipt
	lookupFound    bool
	lookupErr      error
	commitReceipt  Receipt
	commitInserted bool
	commitErr      error
	lookupCalls    int
	commitCalls    int
	committed      Receipt
	order          *[]string
}

func (s *runtimeReceiptStore) Lookup(_ context.Context, _ ReceiptAxis) (Receipt, bool, error) {
	s.lookupCalls++
	if s.order != nil {
		*s.order = append(*s.order, "lookup")
	}
	return s.lookupReceipt, s.lookupFound, s.lookupErr
}

func (s *runtimeReceiptStore) Commit(_ context.Context, receipt Receipt) (Receipt, bool, error) {
	s.commitCalls++
	s.committed = receipt
	if s.order != nil {
		*s.order = append(*s.order, "commit")
	}
	if s.commitErr != nil {
		return Receipt{}, false, s.commitErr
	}
	if s.commitReceipt.valid() {
		return s.commitReceipt, s.commitInserted, nil
	}
	restored, err := RestoreUnverifiedReceipt(receipt.PersistenceRecord())
	if err != nil {
		return Receipt{}, false, err
	}
	return restored, true, nil
}

type runtimeAuthorityResolver struct {
	authority taskmemory.AuthorizedTaskContext
}

func (r runtimeAuthorityResolver) ResolveTaskAuthority(_ context.Context, _ taskmemory.ProjectEvidenceV3) (taskmemory.AuthorizedTaskContext, error) {
	return r.authority, nil
}

type runtimeCandidateProvider struct {
	snapshot taskmemory.CandidateSnapshot
}

func (p runtimeCandidateProvider) Snapshot(_ context.Context, _ taskmemory.AuthorizedCandidateQuery) (taskmemory.CandidateSnapshot, error) {
	return p.snapshot, nil
}

func runtimePreparedTaskMemory(t *testing.T, candidateCount int) taskmemory.PreparedTaskMemory {
	t.Helper()
	var candidates []taskmemory.AuthorizedCandidateRef
	mode := taskmemory.RetrievalEmpty
	for index := range candidateCount {
		candidate, err := taskmemory.NewAuthorizedCandidateRef(int64(index+1), 1, taskmemory.CandidateExact)
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, candidate)
		mode = taskmemory.RetrievalExact
	}
	snapshot, err := taskmemory.NewCandidateSnapshot(mode, candidates)
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := taskmemory.NewPreparer(taskmemory.PreparerConfig{
		Authority:  runtimeAuthorityResolver{authority: receiptTestAuthority(t)},
		Candidates: runtimeCandidateProvider{snapshot: snapshot},
	})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := taskmemory.NewTaskFacts("prepared task")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := preparer.Prepare(context.Background(), taskmemory.PrepareRequest{Task: facts})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func runtimeAdviseInput(t *testing.T, query string) AdviseInput {
	t.Helper()
	occurrence := fixtureOccurrence(t, query, nil)
	input, err := NewAdviseInput(fixtureBindingFacts(), taskmemory.ProjectEvidenceV3{}, occurrence)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func runtimeAxis(t *testing.T, epoch KeyEpoch, input AdviseInput, prepared taskmemory.PreparedTaskMemory) ReceiptAxis {
	t.Helper()
	axis, err := NewReceiptAxis(epoch, input.Binding(), prepared.Context(), input.Occurrence())
	if err != nil {
		t.Fatal(err)
	}
	return axis
}

func newRuntimeAdvisorForTest(t *testing.T, preparer taskmemory.Preparer, store ReceiptStore, epoch KeyEpoch, now time.Time) *RuntimeAdvisor {
	t.Helper()
	sequence := 0
	advisor, err := NewRuntimeAdvisor(RuntimeAdvisorConfig{
		Preparer:     preparer,
		ReceiptStore: store,
		KeyProvider:  NewStaticKeyProvider(epoch),
		Clock:        func() time.Time { return now },
		NewUUID: func() (string, error) {
			sequence++
			return fmt.Sprintf("40000000-0000-4000-8000-%012d", sequence), nil
		},
	})
	if err != nil {
		t.Fatalf("NewRuntimeAdvisor() error = %v", err)
	}
	return advisor
}

func mustUnverifiedReceipt(t *testing.T, receipt Receipt) Receipt {
	t.Helper()
	restored, err := RestoreUnverifiedReceipt(receipt.PersistenceRecord())
	if err != nil {
		t.Fatalf("RestoreUnverifiedReceipt() error = %v", err)
	}
	return restored
}
