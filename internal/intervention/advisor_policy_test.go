package intervention

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/taskmemory"
)

func TestRuntimeAdvisorReducesCurrentPolicyStates(t *testing.T) {
	now := interventionTestTime
	for _, testCase := range []runtimePolicyReductionCase{
		{name: "valid wins mixed set", states: []CandidatePolicyState{CandidatePolicyMissing, CandidatePolicyValid}, want: AbstentionPolicyObserving},
		{name: "missing and insufficient", states: []CandidatePolicyState{CandidatePolicyInsufficient, CandidatePolicyMissing}, want: AbstentionEvidenceInsufficient},
		{name: "all stale", states: []CandidatePolicyState{CandidatePolicySourceStale, CandidatePolicySourceStale}, want: AbstentionEvidenceState},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertRuntimePolicyReduction(t, now, testCase)
		})
	}
}

type runtimePolicyReductionCase struct {
	name   string
	states []CandidatePolicyState
	want   AbstentionReason
}

func assertRuntimePolicyReduction(t *testing.T, now time.Time, testCase runtimePolicyReductionCase) {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	prepared := runtimePreparedTaskMemory(t, len(testCase.states))
	order := []string{}
	preparer := &runtimeRecordingPreparer{prepared: prepared, order: &order}
	store := &runtimeReceiptStore{order: &order}
	epoch := fixtureKeyEpoch(t)
	reader := &runtimePolicyReader{policies: runtimeCandidatePolicies(t, epoch, prepared.Candidates(), testCase.states), order: &order}
	advisor := newRuntimeAdvisorWithPolicyReader(t, preparer, store, epoch, now, reader)
	decision, err := advisor.Advise(ctx, runtimeAdviseInput(t, "policy read"))
	if err != nil {
		t.Fatal(err)
	}
	_, reason, ok := decision.Abstain()
	if !ok || reason != testCase.want {
		t.Fatalf("Advise() = %#v, want %v", decision, testCase.want)
	}
	if reader.calls != 1 || store.commitCalls != 1 {
		t.Fatalf("reader=%d commits=%d", reader.calls, store.commitCalls)
	}
	if got, want := order, []string{"prepare", "lookup", "policy", "commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	record := store.committed.PersistenceRecord()
	if record.ClosedReason == nil || *record.ClosedReason != testCase.want || record.EvaluatedCount != len(testCase.states) ||
		record.EligibleCount != 0 || string(record.SnapshotRefsJSON) != "[]" || record.Selection != nil {
		t.Fatalf("policy abstention record = %#v", record)
	}
}

func TestRuntimeAdvisorReplaysBeforePolicyReader(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	epoch := fixtureKeyEpoch(t)
	input := runtimeAdviseInput(t, "replay before policy")
	prepared := runtimePreparedTaskMemory(t, 1)
	axis := runtimeAxis(t, epoch, input, prepared)
	stored := mustUnverifiedReceipt(t, mustNoCandidatesReceipt(t, ctx, epoch, axis, "50000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000002"))
	order := []string{}
	preparer := &runtimeRecordingPreparer{prepared: prepared, order: &order}
	store := &runtimeReceiptStore{lookupReceipt: stored, lookupFound: true, order: &order}
	reader := &runtimePolicyReader{policies: runtimeCandidatePolicies(t, epoch, prepared.Candidates(), []CandidatePolicyState{CandidatePolicyValid}), order: &order}
	advisor := newRuntimeAdvisorWithPolicyReader(t, preparer, store, epoch, now, reader)

	decision, err := advisor.Advise(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	_, reason, ok := decision.Abstain()
	if !ok || reason != AbstentionNoCandidates {
		t.Fatalf("replay decision = %#v", decision)
	}
	if reader.calls != 0 || store.commitCalls != 0 {
		t.Fatalf("reader=%d commits=%d", reader.calls, store.commitCalls)
	}
	if got, want := order, []string{"prepare", "lookup"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
}

func TestRuntimeAdvisorMapsPolicyReaderErrorToDependency(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	prepared := runtimePreparedTaskMemory(t, 1)
	store := &runtimeReceiptStore{}
	reader := &runtimePolicyReader{err: errors.New("reader unavailable")}
	advisor := newRuntimeAdvisorWithPolicyReader(t, &runtimeRecordingPreparer{prepared: prepared}, store, fixtureKeyEpoch(t), now, reader)

	decision, err := advisor.Advise(ctx, runtimeAdviseInput(t, "reader error"))
	if err != nil {
		t.Fatal(err)
	}
	unavailable, ok := decision.Unavailable()
	if !ok || unavailable.Code() != UnavailableDependency || store.commitCalls != 0 || reader.calls != 1 {
		t.Fatalf("reader error decision = %#v, commits=%d, calls=%d", decision, store.commitCalls, reader.calls)
	}
}

func TestRuntimeAdvisorDoesNotReadPoliciesWithoutReserve(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(CommitReserve+ResponseReserve-time.Microsecond))
	defer cancel()
	prepared := runtimePreparedTaskMemory(t, 1)
	epoch := fixtureKeyEpoch(t)
	reader := &runtimePolicyReader{policies: runtimeCandidatePolicies(t, epoch, prepared.Candidates(), []CandidatePolicyState{CandidatePolicyValid})}
	store := &runtimeReceiptStore{}
	advisor := newRuntimeAdvisorWithPolicyReader(t, &runtimeRecordingPreparer{prepared: prepared}, store, epoch, now, reader)

	decision, err := advisor.Advise(ctx, runtimeAdviseInput(t, "no reserve"))
	if err != nil {
		t.Fatal(err)
	}
	unavailable, ok := decision.Unavailable()
	if !ok || unavailable.Code() != UnavailableDeadline || reader.calls != 0 || store.lookupCalls != 0 || store.commitCalls != 0 {
		t.Fatalf("reserve result = %#v, reader=%d lookup=%d commit=%d", decision, reader.calls, store.lookupCalls, store.commitCalls)
	}
}

func TestRuntimeAdvisorTreatsPolicyScopeDriftAsEvidenceState(t *testing.T) {
	now := interventionTestTime
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	prepared := runtimePreparedTaskMemory(t, 1)
	epoch := fixtureKeyEpoch(t)
	currentScope := mustPolicySource(t, policySourceInput("current scope", []string{"scope"})).Scope()
	differentInput := policySourceInput("different scope", []string{"scope"})
	differentInput.Domain = "different-domain"
	differentScope := mustPolicySource(t, differentInput).Scope()
	staleCommitment, err := epoch.DerivePolicyScope(differentScope)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewCandidatePolicy(
		prepared.Candidates()[0],
		CandidatePolicyValid,
		Digest(testBytes(91)),
		staleCommitment,
		Digest(testBytes(92)),
		currentScope,
	)
	if err != nil {
		t.Fatal(err)
	}
	reader := &runtimePolicyReader{policies: []CandidatePolicy{policy}}
	store := &runtimeReceiptStore{}
	advisor := newRuntimeAdvisorWithPolicyReader(t, &runtimeRecordingPreparer{prepared: prepared}, store, epoch, now, reader)
	decision, err := advisor.Advise(ctx, runtimeAdviseInput(t, "scope drift"))
	if err != nil {
		t.Fatal(err)
	}
	_, reason, ok := decision.Abstain()
	if !ok || reason != AbstentionEvidenceState {
		t.Fatalf("scope drift decision = %#v", decision)
	}
}

type runtimePolicyReader struct {
	policies []CandidatePolicy
	err      error
	calls    int
	order    *[]string
	refs     []taskmemory.AuthorizedCandidateRef
	versions PolicySemanticVersions
}

func (r *runtimePolicyReader) ReadCandidatePolicies(_ context.Context, _ taskmemory.AuthorizedTaskContext, refs []taskmemory.AuthorizedCandidateRef, versions PolicySemanticVersions) ([]CandidatePolicy, error) {
	r.calls++
	if r.order != nil {
		*r.order = append(*r.order, "policy")
	}
	r.refs = append([]taskmemory.AuthorizedCandidateRef(nil), refs...)
	r.versions = versions
	if r.err != nil {
		return nil, r.err
	}
	return append([]CandidatePolicy(nil), r.policies...), nil
}

func runtimeCandidatePolicies(t *testing.T, epoch KeyEpoch, refs []taskmemory.AuthorizedCandidateRef, states []CandidatePolicyState) []CandidatePolicy {
	t.Helper()
	if len(refs) != len(states) {
		t.Fatalf("refs=%d states=%d", len(refs), len(states))
	}
	currentScope := mustPolicySource(t, policySourceInput("runtime policy scope", []string{"runtime"})).Scope()
	currentScopeCommitment, err := epoch.DerivePolicyScope(currentScope)
	if err != nil {
		t.Fatal(err)
	}
	policies := make([]CandidatePolicy, 0, len(refs))
	for index, ref := range refs {
		var policyVersion, scopeCommitment, descriptorCommitment Digest
		var scope SourceMemoryScope
		switch states[index] {
		case CandidatePolicyValid, CandidatePolicyInsufficient:
			policyVersion = Digest(testBytes(byte(80 + index*3)))
			scopeCommitment = currentScopeCommitment
			descriptorCommitment = Digest(testBytes(byte(82 + index*3)))
			scope = currentScope
		}
		policy, err := NewCandidatePolicy(ref, states[index], policyVersion, scopeCommitment, descriptorCommitment, scope)
		if err != nil {
			t.Fatal(err)
		}
		policies = append(policies, policy)
	}
	return policies
}

func newRuntimeAdvisorWithPolicyReader(t *testing.T, preparer taskmemory.Preparer, store ReceiptStore, epoch KeyEpoch, now time.Time, reader PolicyReader) *RuntimeAdvisor {
	t.Helper()
	sequence := 0
	compiler := mustPolicyCompiler(t, nil)
	advisor, err := NewRuntimeAdvisor(RuntimeAdvisorConfig{
		Preparer:       preparer,
		ReceiptStore:   store,
		KeyProvider:    NewStaticKeyProvider(epoch),
		PolicyReader:   reader,
		PolicyVersions: compiler.Versions(),
		Clock:          func() time.Time { return now },
		NewUUID: func() (string, error) {
			sequence++
			return fmt.Sprintf("60000000-0000-4000-8000-%012d", sequence), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return advisor
}
