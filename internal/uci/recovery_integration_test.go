package uci

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

var (
	_ RecoveryPublisher         = (*ReconcileService)(nil)
	_ RecoveryEmbeddingEnsurer  = (*SemanticService)(nil)
	_ RecoveryCurrentByteSource = (*recoveryTestCurrentByteSource)(nil)
	_ RecoveryLocalStatePort    = (*recoveryTestLocalState)(nil)
	_ RecoveryPublisher         = (*recoveryTestPublisher)(nil)
	_ RecoveryEmbeddingEnsurer  = (*recoveryTestEmbeddingEnsurer)(nil)
)

var (
	errRecoveryTestLostACK = errors.New("recovery test: durable publisher acknowledgement lost")
	errRecoveryTestScan    = errors.New("recovery test: current-byte scan failed")
)

func TestUCIRestartAndOfflineRecoveryRescansCurrentBytesWithoutReembedding(t *testing.T) {
	fixture := newRecoveryIntegrationFixture(t)
	initialA := fixture.reconcile.artifact(901, "symbol:recovery-initial-a", "RecoveryInitialA")
	initialB := fixture.reconcile.artifact(902, "symbol:recovery-initial-b", "RecoveryInitialB")
	offlineA := fixture.reconcile.artifact(903, "symbol:recovery-offline-a", "RecoveryOfflineA")

	fixture.local.set(fixture.reconcile.scopeA, 1, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", initialA, recoveryTestObservation("main", true)))
	publishedA := fixture.recover(t, fixture.request(fixture.reconcile.scopeA, fixture.reconcile.profileID, fixture.rootA, "recovery-initial-a", nil))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, publishedA.View, "a.go", initialA, 1)

	fixture.local.set(fixture.reconcile.scopeB, 1, true)
	fixture.source.queue(fixture.rootB, fixture.currentBytes("b.go", initialB, recoveryTestObservation("main", true)))
	publishedB := fixture.recover(t, fixture.request(fixture.reconcile.scopeB, fixture.reconcile.profileID, fixture.rootB, "recovery-initial-b", nil))
	fixture.requireCurrent(t, fixture.reconcile.scopeB, publishedB.View, "b.go", initialB, 1)
	fixture.requireVectorCalls(t, 2)

	// Restart leaves no trustworthy event replay. The persisted high-water is
	// already acknowledged, so this only succeeds if recovery still scans the
	// current bytes and reuses the exact semantic input.
	fixture.local.set(fixture.reconcile.scopeA, 1, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", initialA, recoveryTestObservation("main", true)))
	restartedA := fixture.recover(t, fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-daemon-restart-a",
		&publishedA.View.Context,
	))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, restartedA.View, "a.go", initialA, 1)
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	fixture.requireVectorCalls(t, 2)

	// The outage retains only a bounded dirty high-water, not intermediate
	// source bodies. Reconnect must scan the final current bytes once and use
	// that high-water as the durable acknowledgement boundary.
	fixture.local.set(fixture.reconcile.scopeA, 5, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", offlineA, recoveryTestObservation("main", true)))
	reconnectedA := fixture.recover(t, fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-offline-reconnect-a",
		&restartedA.View.Context,
	))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, reconnectedA.View, "a.go", offlineA, 5)
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	fixture.requireVectorCalls(t, 3)
	fixture.requireSourceCalls(t, 4)
}

func TestUCIRecoveryHandlesBranchDetachedUnbornAndFailedScans(t *testing.T) {
	fixture := newRecoveryIntegrationFixture(t)
	mainA := fixture.reconcile.artifact(911, "symbol:recovery-main-a", "RecoveryMainA")
	branchA := fixture.reconcile.artifact(912, "symbol:recovery-branch-a", "RecoveryBranchA")
	detachedA := fixture.reconcile.artifact(913, "symbol:recovery-detached-a", "RecoveryDetachedA")
	unbornA := fixture.reconcile.artifact(914, "symbol:recovery-unborn-a", "RecoveryUnbornA")
	initialB := fixture.reconcile.artifact(915, "symbol:recovery-stable-b", "RecoveryStableB")

	fixture.local.set(fixture.reconcile.scopeA, 1, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", mainA, recoveryTestObservation("main", true)))
	publishedA := fixture.recover(t, fixture.request(fixture.reconcile.scopeA, fixture.reconcile.profileID, fixture.rootA, "recovery-main-a", nil))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, publishedA.View, "a.go", mainA, 1)

	fixture.local.set(fixture.reconcile.scopeB, 1, true)
	fixture.source.queue(fixture.rootB, fixture.currentBytes("b.go", initialB, recoveryTestObservation("main", true)))
	publishedB := fixture.recover(t, fixture.request(fixture.reconcile.scopeB, fixture.reconcile.profileID, fixture.rootB, "recovery-stable-b", nil))
	fixture.requireCurrent(t, fixture.reconcile.scopeB, publishedB.View, "b.go", initialB, 1)

	fixture.local.set(fixture.reconcile.scopeA, 2, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", branchA, recoveryTestObservation("feature/recovery", true)))
	branchView := fixture.recover(t, fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-branch-switch-a",
		&publishedA.View.Context,
	))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, branchView.View, "a.go", branchA, 2)
	fixture.requireUnchangedB(t, publishedB.View, initialB)

	// Detached HEAD has an observed object ID but no branch label. It is still
	// an admissible complete current-byte census.
	fixture.local.set(fixture.reconcile.scopeA, 3, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", detachedA, recoveryTestObservation("", true)))
	detachedView := fixture.recover(t, fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-detached-head-a",
		&branchView.View.Context,
	))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, detachedView.View, "a.go", detachedA, 3)
	fixture.requireUnchangedB(t, publishedB.View, initialB)

	// An unborn HEAD has no object ID, but its ref label remains an observed
	// fact. Recovery must not turn that state into a failed or empty scan.
	fixture.local.set(fixture.reconcile.scopeA, 4, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", unbornA, recoveryTestObservation("main", false)))
	unbornView := fixture.recover(t, fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-unborn-head-a",
		&detachedView.View.Context,
	))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, unbornView.View, "a.go", unbornA, 4)
	fixture.requireUnchangedB(t, publishedB.View, initialB)

	fixture.local.set(fixture.reconcile.scopeA, 5, true)
	fixture.source.queue(fixture.rootA, RecoveryCurrentBytes{Scan: fixture.reconcile.incompleteScan(0, IndexScanIncomplete)})
	fixture.requireRecoveryError(t, fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-incomplete-scan-a",
		&unbornView.View.Context,
	))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, unbornView.View, "a.go", unbornA, 4)
	fixture.requireUnchangedB(t, publishedB.View, initialB)

	fixture.local.set(fixture.reconcile.scopeA, 6, true)
	fixture.source.fail(fixture.rootA, errRecoveryTestScan)
	fixture.requireRecoveryError(t, fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-failed-scan-a",
		&unbornView.View.Context,
	))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, unbornView.View, "a.go", unbornA, 4)
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	fixture.requireVectorCalls(t, 5)
	fixture.requireSourceCalls(t, 7)
}

func TestUCIRecoveryDefersLocalAcknowledgementUntilCrashAndLostAckReplay(t *testing.T) {
	fixture := newRecoveryIntegrationFixture(t)
	initialA := fixture.reconcile.artifact(921, "symbol:recovery-crash-initial-a", "RecoveryCrashInitialA")
	crashedA := fixture.reconcile.artifact(922, "symbol:recovery-crash-next-a", "RecoveryCrashNextA")
	lostAckA := fixture.reconcile.artifact(923, "symbol:recovery-lost-ack-a", "RecoveryLostACKA")
	initialB := fixture.reconcile.artifact(924, "symbol:recovery-crash-stable-b", "RecoveryCrashStableB")

	fixture.local.set(fixture.reconcile.scopeA, 1, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", initialA, recoveryTestObservation("main", true)))
	publishedA := fixture.recover(t, fixture.request(fixture.reconcile.scopeA, fixture.reconcile.profileID, fixture.rootA, "recovery-crash-initial-a", nil))

	fixture.local.set(fixture.reconcile.scopeB, 1, true)
	fixture.source.queue(fixture.rootB, fixture.currentBytes("b.go", initialB, recoveryTestObservation("main", true)))
	publishedB := fixture.recover(t, fixture.request(fixture.reconcile.scopeB, fixture.reconcile.profileID, fixture.rootB, "recovery-crash-initial-b", nil))

	fixture.local.set(fixture.reconcile.scopeA, 2, true)
	crashPayload := fixture.currentBytes("a.go", crashedA, recoveryTestObservation("main", true))
	crashRequest := fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-crash-before-finalize-a",
		&publishedA.View.Context,
	)
	fixture.armFinalizeCrash(t, crashRequest, crashPayload, 2)
	fixture.source.queue(fixture.rootA, crashPayload)
	fixture.requireRecoveryError(t, crashRequest)
	fixture.requireCurrent(t, fixture.reconcile.scopeA, publishedA.View, "a.go", initialA, 1)
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	fixture.requireVectorCalls(t, 2)

	fixture.source.queue(fixture.rootA, crashPayload)
	crashRetried := fixture.recover(t, crashRequest)
	fixture.requireCurrent(t, fixture.reconcile.scopeA, crashRetried.View, "a.go", crashedA, 2)
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	fixture.requireVectorCalls(t, 3)

	fixture.local.set(fixture.reconcile.scopeA, 3, true)
	lostAckPayload := fixture.currentBytes("a.go", lostAckA, recoveryTestObservation("main", true))
	lostAckRequest := fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		fixture.rootA,
		"recovery-lost-ack-a",
		&crashRetried.View.Context,
	)
	fixture.publisher.loseNextAcknowledgement()
	fixture.source.queue(fixture.rootA, lostAckPayload)
	fixture.requireRecoveryError(t, lostAckRequest)
	durableAfterLostAck := fixture.currentPublished(t, fixture.reconcile.scopeA.CheckoutID)
	fixture.reconcile.requireCurrent(t, fixture.reconcile.scopeA.CheckoutID, durableAfterLostAck.Context)
	fixture.reconcile.requirePath(t, durableAfterLostAck.Context, "a.go", lostAckA)
	if got := durableAfterLostAck.AcceptedFSSeq; got != 3 {
		t.Fatalf("durable lost-ACK sequence = %d, want 3", got)
	}
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	fixture.requireLocalSequence(t, fixture.reconcile.scopeA.CheckoutID, 2)
	fixture.requireVectorCalls(t, 3)
	viewCount := fixture.reconcile.store.viewCount(fixture.reconcile.scopeA.CheckoutID)

	fixture.source.queue(fixture.rootA, lostAckPayload)
	replayed := fixture.recover(t, lostAckRequest)
	if !reconcileTestPublishedEqual(replayed.View, durableAfterLostAck) {
		t.Fatalf("lost-ACK replay = %#v, want durable result %#v", replayed.View, durableAfterLostAck)
	}
	if got := fixture.reconcile.store.viewCount(fixture.reconcile.scopeA.CheckoutID); got != viewCount {
		t.Fatalf("exact lost-ACK replay created %d Views, want %d", got, viewCount)
	}
	fixture.requireCurrent(t, fixture.reconcile.scopeA, replayed.View, "a.go", lostAckA, 3)
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	fixture.requireVectorCalls(t, 4)
	fixture.requireSourceCalls(t, 6)
}

func TestUCIIncarnationMoveContinuityAndRecreatedCheckoutLeaveOldViewHistorical(t *testing.T) {
	fixture := newRecoveryIntegrationFixture(t)
	initialA := fixture.reconcile.artifact(931, "symbol:recovery-move-a", "RecoveryMoveA")
	recreatedA := fixture.reconcile.artifact(932, "symbol:recovery-recreated-a", "RecoveryRecreatedA")
	initialB := fixture.reconcile.artifact(933, "symbol:recovery-move-stable-b", "RecoveryMoveStableB")
	movedRoot := `D:\fixture\moved-checkout-a`

	fixture.local.set(fixture.reconcile.scopeA, 1, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", initialA, recoveryTestObservation("main", true)))
	publishedA := fixture.recover(t, fixture.request(fixture.reconcile.scopeA, fixture.reconcile.profileID, fixture.rootA, "recovery-move-initial-a", nil))

	fixture.local.set(fixture.reconcile.scopeB, 1, true)
	fixture.source.queue(fixture.rootB, fixture.currentBytes("b.go", initialB, recoveryTestObservation("main", true)))
	publishedB := fixture.recover(t, fixture.request(fixture.reconcile.scopeB, fixture.reconcile.profileID, fixture.rootB, "recovery-move-initial-b", nil))

	// A move with verified continuity retains the server-authorized scope and
	// changes only the authorized root used for the safe current-byte scan.
	fixture.local.set(fixture.reconcile.scopeA, 2, true)
	fixture.source.queue(movedRoot, fixture.currentBytes("a.go", initialA, recoveryTestObservation("main", true)))
	movedA := fixture.recover(t, fixture.request(
		fixture.reconcile.scopeA,
		fixture.reconcile.profileID,
		movedRoot,
		"recovery-verified-move-a",
		&publishedA.View.Context,
	))
	fixture.requireCurrent(t, fixture.reconcile.scopeA, movedA.View, "a.go", initialA, 2)
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	fixture.requireVectorCalls(t, 2)

	// Re-registration after delete/recreate is a server-authorized incarnation
	// transition. The local state merely triggers recovery; it cannot select the
	// new scope or retain the old current View as its parent.
	recreatedScope := fixture.reconcile.scopeA
	recreatedScope.IncarnationID = "73000000-0000-4000-8000-000000000999"
	recreatedProfileID := "74000000-0000-4000-8000-000000000999"
	fixture.replaceAuthoritativeCheckout(recreatedScope)
	fixture.local.replace(recreatedScope, 1, true)
	fixture.source.queue(fixture.rootA, fixture.currentBytes("a.go", recreatedA, recoveryTestObservation("main", true)))
	recreated := fixture.recover(t, fixture.request(
		recreatedScope,
		recreatedProfileID,
		fixture.rootA,
		"recovery-recreated-checkout-a",
		nil,
	))
	fixture.requireCurrent(t, recreatedScope, recreated.View, "a.go", recreatedA, 1)
	fixture.requireUnchangedB(t, publishedB.View, initialB)
	if recreated.View.BuildID == movedA.View.BuildID {
		t.Fatal("recreated checkout reused the prior incarnation's durable View")
	}
	if recreated.View.Context.AnalysisProfileID != recreatedProfileID {
		t.Fatalf("recreated View profile = %q, want %q", recreated.View.Context.AnalysisProfileID, recreatedProfileID)
	}
	fixture.requireHistorical(t, movedA.View.Context)
	fixture.requireVectorCalls(t, 3)
	fixture.requireSourceCalls(t, 4)
}

type recoveryIntegrationFixture struct {
	reconcile  *reconcileTestFixture
	source     *recoveryTestCurrentByteSource
	publisher  *recoveryTestPublisher
	local      *recoveryTestLocalState
	embeddings *recoveryTestEmbeddingEnsurer
	service    *RecoveryService
	rootA      string
	rootB      string
}

func newRecoveryIntegrationFixture(t *testing.T) *recoveryIntegrationFixture {
	t.Helper()
	reconcile := newReconcileTestFixture(t)
	source := newRecoveryTestCurrentByteSource()
	publisher := &recoveryTestPublisher{service: reconcile.service}
	local := newRecoveryTestLocalState()
	embeddings := newRecoveryTestEmbeddingEnsurer(semanticTestProfile("recovery-integration-model"))
	return &recoveryIntegrationFixture{
		reconcile:  reconcile,
		source:     source,
		publisher:  publisher,
		local:      local,
		embeddings: embeddings,
		service:    NewRecoveryService(source, publisher, local, embeddings),
		rootA:      `C:\fixture\checkout-a`,
		rootB:      `C:\fixture\checkout-b`,
	}
}

func (fixture *recoveryIntegrationFixture) request(scope IndexScope, profileID, root, buildKey string, parent *ContextRef) RecoveryRequest {
	return RecoveryRequest{
		Caller:         fixture.reconcile.caller,
		Root:           AuthorizedRootEvidence{RootPath: root},
		Scope:          scope,
		ProfileID:      profileID,
		BuildKey:       buildKey,
		ExpectedParent: parent,
	}
}

func (fixture *recoveryIntegrationFixture) currentBytes(path string, artifact reconcileTestArtifact, observation IndexObservation) RecoveryCurrentBytes {
	scan := fixture.reconcile.completeScan(0, reconcileTestSnapshotFile(path, artifact))
	scan.Observation.HeadOID = observation.HeadOID
	scan.Observation.ObjectFormat = observation.ObjectFormat
	scan.Observation.RefLabel = observation.RefLabel
	return RecoveryCurrentBytes{
		Scan: scan,
		Parts: []IndexPart{reconcileTestPart(
			[]reconcileTestArtifact{artifact},
			[]IndexMembership{reconcileTestMembership(path, artifact)},
			[]IndexEdgeReplacement{{SourcePath: path}},
		)},
		EmbeddingCandidates: []QueryCandidate{artifact.candidate(ContextRef{}, path)},
	}
}

func recoveryTestObservation(refLabel string, hasHead bool) IndexObservation {
	objectFormat := "sha1"
	observation := IndexObservation{ObjectFormat: &objectFormat}
	if refLabel != "" {
		observation.RefLabel = &refLabel
	}
	if hasHead {
		head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		observation.HeadOID = &head
	}
	return observation
}

func (fixture *recoveryIntegrationFixture) recover(t *testing.T, request RecoveryRequest) RecoveryResult {
	t.Helper()
	result, err := fixture.service.Recover(context.Background(), request)
	if err != nil {
		t.Fatalf("Recover(%q) error = %v", request.BuildKey, err)
	}
	return result
}

func (fixture *recoveryIntegrationFixture) requireRecoveryError(t *testing.T, request RecoveryRequest) {
	t.Helper()
	if _, err := fixture.service.Recover(context.Background(), request); err == nil {
		t.Fatalf("Recover(%q) succeeded after an incomplete or failed recovery input", request.BuildKey)
	}
}

func (fixture *recoveryIntegrationFixture) requireCurrent(t *testing.T, scope IndexScope, view IndexPublishedView, path string, artifact reconcileTestArtifact, sequence int64) {
	t.Helper()
	fixture.reconcile.requireCurrent(t, scope.CheckoutID, view.Context)
	fixture.reconcile.requirePath(t, view.Context, path, artifact)
	if got := view.AcceptedFSSeq; got != sequence {
		t.Fatalf("published sequence for checkout %q = %d, want %d", scope.CheckoutID, got, sequence)
	}
	fixture.requireLocalSequence(t, scope.CheckoutID, sequence)
}

func (fixture *recoveryIntegrationFixture) requireUnchangedB(t *testing.T, view IndexPublishedView, artifact reconcileTestArtifact) {
	t.Helper()
	fixture.reconcile.requireCurrent(t, fixture.reconcile.scopeB.CheckoutID, view.Context)
	fixture.reconcile.requirePath(t, view.Context, "b.go", artifact)
}

func (fixture *recoveryIntegrationFixture) requireLocalSequence(t *testing.T, checkoutID string, want int64) {
	t.Helper()
	if got := fixture.local.sequence(checkoutID); got != want {
		t.Fatalf("local acknowledgement for checkout %q = %d, want %d", checkoutID, got, want)
	}
}

func (fixture *recoveryIntegrationFixture) requireVectorCalls(t *testing.T, want int) {
	t.Helper()
	if got := fixture.embeddings.providerCalls(); got != want {
		t.Fatalf("embedding provider calls = %d, want %d", got, want)
	}
}

func (fixture *recoveryIntegrationFixture) requireSourceCalls(t *testing.T, want int) {
	t.Helper()
	if got := fixture.source.callCount(); got != want {
		t.Fatalf("current-byte scans = %d, want %d", got, want)
	}
}

func (fixture *recoveryIntegrationFixture) armFinalizeCrash(t *testing.T, request RecoveryRequest, current RecoveryCurrentBytes, sequence int64) {
	t.Helper()
	scan := current.Scan
	scan.Observation.ObservedFSSeq = sequence
	publication := ReconcileRequest{
		BuildKey:       request.BuildKey,
		Scope:          request.Scope,
		ProfileID:      request.ProfileID,
		ExpectedParent: request.ExpectedParent,
		Scan:           scan,
		Parts:          current.Parts,
	}
	fixture.reconcile.store.failNextFinalize(reconcileTestPublicationKey(t, request.Caller, publication))
}

func (fixture *recoveryIntegrationFixture) currentPublished(t *testing.T, checkoutID string) IndexPublishedView {
	t.Helper()
	fixture.reconcile.store.mu.Lock()
	defer fixture.reconcile.store.mu.Unlock()
	view, found := fixture.reconcile.store.current[checkoutID]
	if !found {
		t.Fatalf("checkout %q has no current durable View", checkoutID)
	}
	return view.published
}

func (fixture *recoveryIntegrationFixture) replaceAuthoritativeCheckout(scope IndexScope) {
	fixture.reconcile.store.mu.Lock()
	defer fixture.reconcile.store.mu.Unlock()
	fixture.reconcile.store.checkouts[scope.CheckoutID] = scope
	delete(fixture.reconcile.store.current, scope.CheckoutID)
}

func (fixture *recoveryIntegrationFixture) requireHistorical(t *testing.T, old ContextRef) {
	t.Helper()
	fixture.reconcile.store.mu.Lock()
	_, current := fixture.reconcile.store.currentForContextLocked(old)
	fixture.reconcile.store.mu.Unlock()
	if current {
		t.Fatalf("historical View %#v remained current after checkout recreation", old)
	}
}

type recoveryTestCurrentByteSource struct {
	mu    sync.Mutex
	plans []recoveryTestSourcePlan
	calls int
}

type recoveryTestSourcePlan struct {
	root    string
	current RecoveryCurrentBytes
	err     error
}

func newRecoveryTestCurrentByteSource() *recoveryTestCurrentByteSource {
	return &recoveryTestCurrentByteSource{}
}

func (source *recoveryTestCurrentByteSource) queue(root string, current RecoveryCurrentBytes) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.plans = append(source.plans, recoveryTestSourcePlan{root: root, current: current})
}

func (source *recoveryTestCurrentByteSource) fail(root string, err error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.plans = append(source.plans, recoveryTestSourcePlan{root: root, err: err})
}

func (source *recoveryTestCurrentByteSource) ScanCurrent(ctx context.Context, root AuthorizedRootEvidence) (RecoveryCurrentBytes, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryCurrentBytes{}, err
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.plans) == 0 {
		return RecoveryCurrentBytes{}, errors.New("recovery test: unexpected current-byte scan")
	}
	plan := source.plans[0]
	source.plans = source.plans[1:]
	source.calls++
	if root.RootPath != plan.root {
		return RecoveryCurrentBytes{}, fmt.Errorf("recovery test: scanned root %q, want %q", root.RootPath, plan.root)
	}
	if plan.err != nil {
		return RecoveryCurrentBytes{}, plan.err
	}
	return plan.current, nil
}

func (source *recoveryTestCurrentByteSource) callCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

type recoveryTestPublisher struct {
	service *ReconcileService

	mu      sync.Mutex
	loseAck bool
}

func (publisher *recoveryTestPublisher) Reconcile(ctx context.Context, caller IndexCaller, request ReconcileRequest) (ReconcileResult, error) {
	result, err := publisher.service.Reconcile(ctx, caller, request)
	if err != nil {
		return ReconcileResult{}, err
	}
	publisher.mu.Lock()
	lost := publisher.loseAck
	publisher.loseAck = false
	publisher.mu.Unlock()
	if lost {
		return ReconcileResult{}, errRecoveryTestLostACK
	}
	return result, nil
}

func (publisher *recoveryTestPublisher) loseNextAcknowledgement() {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.loseAck = true
}

type recoveryTestLocalState struct {
	mu      sync.Mutex
	records map[string]RecoveryLocalState
}

func newRecoveryTestLocalState() *recoveryTestLocalState {
	return &recoveryTestLocalState{records: make(map[string]RecoveryLocalState)}
}

func (state *recoveryTestLocalState) set(scope IndexScope, dirtySequence int64, rescanRequired bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	record := state.records[scope.CheckoutID]
	record.CheckoutID = scope.CheckoutID
	record.IncarnationID = scope.IncarnationID
	record.DirtySequence = dirtySequence
	record.RescanRequired = rescanRequired
	state.records[scope.CheckoutID] = record
}

func (state *recoveryTestLocalState) replace(scope IndexScope, dirtySequence int64, rescanRequired bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.records[scope.CheckoutID] = RecoveryLocalState{
		CheckoutID:     scope.CheckoutID,
		IncarnationID:  scope.IncarnationID,
		DirtySequence:  dirtySequence,
		RescanRequired: rescanRequired,
	}
}

func (state *recoveryTestLocalState) Snapshot(ctx context.Context, checkoutID string) (RecoveryLocalState, bool, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryLocalState{}, false, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	record, found := state.records[checkoutID]
	return record, found, nil
}

func (state *recoveryTestLocalState) MarkReconciled(ctx context.Context, checkoutID string, sequence int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	record, found := state.records[checkoutID]
	if !found {
		return fmt.Errorf("recovery test: checkout %q is not locally registered", checkoutID)
	}
	if sequence > record.DirtySequence {
		return fmt.Errorf("recovery test: acknowledgement %d exceeds dirty sequence %d", sequence, record.DirtySequence)
	}
	if sequence > record.LastReconciledSequence {
		record.LastReconciledSequence = sequence
	}
	if sequence == record.DirtySequence {
		record.RescanRequired = false
	}
	state.records[checkoutID] = record
	return nil
}

func (state *recoveryTestLocalState) sequence(checkoutID string) int64 {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.records[checkoutID].LastReconciledSequence
}

type recoveryTestEmbeddingKey struct {
	profile VectorProfile
	input   IndexDigest
}

type recoveryTestEmbeddingEnsurer struct {
	profile VectorProfile

	mu        sync.Mutex
	cached    map[recoveryTestEmbeddingKey]struct{}
	providers int
}

func newRecoveryTestEmbeddingEnsurer(profile VectorProfile) *recoveryTestEmbeddingEnsurer {
	return &recoveryTestEmbeddingEnsurer{
		profile: profile,
		cached:  make(map[recoveryTestEmbeddingKey]struct{}),
	}
}

func (ensurer *recoveryTestEmbeddingEnsurer) EnsureCandidateEmbedding(ctx context.Context, authorized AuthorizedContext, candidate QueryCandidate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !reconcileTestContextsEqual(authorized.Ref(), candidate.Context) {
		return errors.New("recovery test: embedding candidate is outside the published View")
	}
	_, input, err := SemanticEmbeddingInput(ensurer.profile, candidate)
	if err != nil {
		return err
	}
	key := recoveryTestEmbeddingKey{profile: ensurer.profile, input: input}
	ensurer.mu.Lock()
	defer ensurer.mu.Unlock()
	if _, found := ensurer.cached[key]; found {
		return nil
	}
	ensurer.cached[key] = struct{}{}
	ensurer.providers++
	return nil
}

func (ensurer *recoveryTestEmbeddingEnsurer) providerCalls() int {
	ensurer.mu.Lock()
	defer ensurer.mu.Unlock()
	return ensurer.providers
}
