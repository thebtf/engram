package uci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	errReconcileTestWrongIncarnation = errors.New("reconcile test store: checkout incarnation is stale")
	errReconcileTestLeaseStale       = errors.New("reconcile test store: lease epoch is stale")
	errReconcileTestCrash            = errors.New("reconcile test store: crash before durable finalization")
	errReconcileTestRejected         = errors.New("reconcile test store: publication rejected")
)

func TestUCIReconcileChangedCalleeReResolvesUnchangedCallerAtomically(t *testing.T) {
	fixture := newReconcileTestFixture(t)
	caller := fixture.artifact(101, "symbol:caller", "Caller")
	calleeV1 := fixture.artifact(102, "symbol:callee-v1", "CalleeV1")
	calleeV2 := fixture.artifact(103, "symbol:callee-v2", "CalleeV2")
	otherCaller := fixture.artifact(104, "symbol:other-caller", "OtherCaller")
	otherCallee := fixture.artifact(105, "symbol:other-callee", "OtherCallee")

	initialA := fixture.mustReconcile(t, fixture.request(
		"changed-callee-a-v1",
		fixture.scopeA,
		nil,
		fixture.completeScan(10,
			reconcileTestSnapshotFile("caller.go", caller),
			reconcileTestSnapshotFile("callee.go", calleeV1),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{caller, calleeV1},
			[]IndexMembership{
				reconcileTestMembership("caller.go", caller),
				reconcileTestMembership("callee.go", calleeV1),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "callee.go", calleeV1)}},
				{SourcePath: "callee.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(initialA.View)

	initialB := fixture.mustReconcile(t, fixture.request(
		"changed-callee-b-v1",
		fixture.scopeB,
		nil,
		fixture.completeScan(20,
			reconcileTestSnapshotFile("other_caller.go", otherCaller),
			reconcileTestSnapshotFile("other_callee.go", otherCallee),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{otherCaller, otherCallee},
			[]IndexMembership{
				reconcileTestMembership("other_caller.go", otherCaller),
				reconcileTestMembership("other_callee.go", otherCallee),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "other_caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("other_caller.go", otherCaller, "other_callee.go", otherCallee)}},
				{SourcePath: "other_callee.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(initialB.View)

	fixture.store.setBeforePublish(func() {
		fixture.requirePath(t, initialA.View.Context, "caller.go", caller)
		fixture.requirePath(t, initialA.View.Context, "callee.go", calleeV1)
		fixture.requireGraphEdge(t, initialA.View.Context, caller.entityKey, calleeV1.entityKey)
		fixture.requirePath(t, initialB.View.Context, "other_caller.go", otherCaller)
		fixture.requireGraphEdge(t, initialB.View.Context, otherCaller.entityKey, otherCallee.entityKey)
	})

	updatedA := fixture.mustReconcile(t, fixture.request(
		"changed-callee-a-v2",
		fixture.scopeA,
		reconcileTestParent(initialA.View),
		fixture.completeScan(30,
			reconcileTestSnapshotFile("caller.go", caller),
			reconcileTestSnapshotFile("callee.go", calleeV2),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{caller, calleeV2},
			[]IndexMembership{
				reconcileTestMembership("caller.go", caller),
				reconcileTestMembership("callee.go", calleeV2),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "callee.go", calleeV2)}},
				{SourcePath: "callee.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(updatedA.View)

	if updatedA.View.Context.ViewID == initialA.View.Context.ViewID {
		t.Fatalf("changed callee retained current View %q", updatedA.View.Context.ViewID)
	}
	fixture.requirePath(t, updatedA.View.Context, "caller.go", caller)
	fixture.requirePath(t, updatedA.View.Context, "callee.go", calleeV2)
	fixture.requireGraphEdge(t, updatedA.View.Context, caller.entityKey, calleeV2.entityKey)
	fixture.requireNoGraphEndpoint(t, updatedA.View.Context, caller.entityKey, calleeV1.entityKey)
	fixture.requireCurrent(t, fixture.scopeA.CheckoutID, updatedA.View.Context)

	fixture.requirePath(t, initialB.View.Context, "other_caller.go", otherCaller)
	fixture.requireGraphEdge(t, initialB.View.Context, otherCaller.entityKey, otherCallee.entityKey)
	fixture.requireCurrent(t, fixture.scopeB.CheckoutID, initialB.View.Context)
}

func TestUCIReconcileRenameAndDeleteCurrentMembership(t *testing.T) {
	fixture := newReconcileTestFixture(t)
	caller := fixture.artifact(201, "symbol:rename-caller", "RenameCaller")
	target := fixture.artifact(202, "symbol:rename-target", "RenameTarget")
	otherCaller := fixture.artifact(203, "symbol:rename-other-caller", "RenameOtherCaller")
	otherTarget := fixture.artifact(204, "symbol:rename-other-target", "RenameOtherTarget")

	initialA := fixture.mustReconcile(t, fixture.request(
		"rename-delete-a-v1",
		fixture.scopeA,
		nil,
		fixture.completeScan(10,
			reconcileTestSnapshotFile("caller.go", caller),
			reconcileTestSnapshotFile("target.go", target),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{caller, target},
			[]IndexMembership{
				reconcileTestMembership("caller.go", caller),
				reconcileTestMembership("target.go", target),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", target)}},
				{SourcePath: "target.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(initialA.View)

	initialB := fixture.mustReconcile(t, fixture.request(
		"rename-delete-b-v1",
		fixture.scopeB,
		nil,
		fixture.completeScan(20,
			reconcileTestSnapshotFile("other_caller.go", otherCaller),
			reconcileTestSnapshotFile("other_target.go", otherTarget),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{otherCaller, otherTarget},
			[]IndexMembership{
				reconcileTestMembership("other_caller.go", otherCaller),
				reconcileTestMembership("other_target.go", otherTarget),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "other_caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("other_caller.go", otherCaller, "other_target.go", otherTarget)}},
				{SourcePath: "other_target.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(initialB.View)

	renamedA := fixture.mustReconcile(t, fixture.request(
		"rename-delete-a-renamed",
		fixture.scopeA,
		reconcileTestParent(initialA.View),
		fixture.completeScan(30,
			reconcileTestSnapshotFile("caller.go", caller),
			reconcileTestSnapshotFile("renamed.go", target),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{caller, target},
			[]IndexMembership{
				reconcileTestMembership("caller.go", caller),
				reconcileTestMembership("renamed.go", target),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "renamed.go", target)}},
				{SourcePath: "renamed.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(renamedA.View)

	fixture.requireNoPath(t, renamedA.View.Context, "target.go")
	fixture.requirePath(t, renamedA.View.Context, "renamed.go", target)
	fixture.requireGraphEdge(t, renamedA.View.Context, caller.entityKey, target.entityKey)
	fixture.requireCurrent(t, fixture.scopeA.CheckoutID, renamedA.View.Context)
	fixture.requirePath(t, initialB.View.Context, "other_target.go", otherTarget)
	fixture.requireGraphEdge(t, initialB.View.Context, otherCaller.entityKey, otherTarget.entityKey)
	fixture.requireCurrent(t, fixture.scopeB.CheckoutID, initialB.View.Context)

	deletedA := fixture.mustReconcile(t, fixture.request(
		"rename-delete-a-deleted",
		fixture.scopeA,
		reconcileTestParent(renamedA.View),
		fixture.completeScan(40, reconcileTestSnapshotFile("caller.go", caller)),
		reconcileTestPart(
			[]reconcileTestArtifact{caller},
			[]IndexMembership{reconcileTestMembership("caller.go", caller)},
			[]IndexEdgeReplacement{{
				SourcePath: "caller.go",
				Edges:      []IndexEdge{reconcileTestUnresolvedEdge("caller.go", caller, "deleted-target")},
			}},
		),
	))
	fixture.checkpoint.acknowledge(deletedA.View)

	fixture.requireNoPath(t, deletedA.View.Context, "renamed.go")
	fixture.requireUnresolved(t, deletedA.View.Context, caller.entityKey, "calls")
	fixture.requireCurrent(t, fixture.scopeA.CheckoutID, deletedA.View.Context)
	fixture.requirePath(t, initialB.View.Context, "other_target.go", otherTarget)
	fixture.requireGraphEdge(t, initialB.View.Context, otherCaller.entityKey, otherTarget.entityKey)
	fixture.requireCurrent(t, fixture.scopeB.CheckoutID, initialB.View.Context)
}

func TestUCIReconcileUnresolvedAndAmbiguousTargetsRemainHonest(t *testing.T) {
	t.Run("new unresolved target keeps a cited unknown instead of an invented edge", func(t *testing.T) {
		fixture := newReconcileTestFixture(t)
		caller := fixture.artifact(301, "symbol:unresolved-caller", "UnresolvedCaller")
		target := fixture.artifact(302, "symbol:unresolved-target", "UnresolvedTarget")

		initial := fixture.mustReconcile(t, fixture.request(
			"unresolved-v1",
			fixture.scopeA,
			nil,
			fixture.completeScan(10,
				reconcileTestSnapshotFile("caller.go", caller),
				reconcileTestSnapshotFile("target.go", target),
			),
			reconcileTestPart(
				[]reconcileTestArtifact{caller, target},
				[]IndexMembership{
					reconcileTestMembership("caller.go", caller),
					reconcileTestMembership("target.go", target),
				},
				[]IndexEdgeReplacement{
					{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", target)}},
					{SourcePath: "target.go"},
				},
			),
		))
		fixture.checkpoint.acknowledge(initial.View)

		unresolved := fixture.mustReconcile(t, fixture.request(
			"unresolved-v2",
			fixture.scopeA,
			reconcileTestParent(initial.View),
			fixture.completeScan(20, reconcileTestSnapshotFile("caller.go", caller)),
			reconcileTestPart(
				[]reconcileTestArtifact{caller},
				[]IndexMembership{reconcileTestMembership("caller.go", caller)},
				[]IndexEdgeReplacement{{
					SourcePath: "caller.go",
					Edges:      []IndexEdge{reconcileTestUnresolvedEdge("caller.go", caller, "new-missing-target")},
				}},
			),
		))
		fixture.checkpoint.acknowledge(unresolved.View)

		fixture.requireNoPath(t, unresolved.View.Context, "target.go")
		fixture.requireUnresolved(t, unresolved.View.Context, caller.entityKey, "calls")
	})

	t.Run("new ambiguous target returns candidates without a fabricated graph", func(t *testing.T) {
		fixture := newReconcileTestFixture(t)
		caller := fixture.artifact(311, "symbol:ambiguous-caller", "AmbiguousCaller")
		oldTarget := fixture.artifact(312, "symbol:ambiguous-old-target", "AmbiguousOldTarget")
		handlerA := fixture.artifact(313, "symbol:handler-a", "Handler")
		handlerB := fixture.artifact(314, "symbol:handler-b", "Handler")

		initial := fixture.mustReconcile(t, fixture.request(
			"ambiguous-v1",
			fixture.scopeA,
			nil,
			fixture.completeScan(10,
				reconcileTestSnapshotFile("caller.go", caller),
				reconcileTestSnapshotFile("target.go", oldTarget),
			),
			reconcileTestPart(
				[]reconcileTestArtifact{caller, oldTarget},
				[]IndexMembership{
					reconcileTestMembership("caller.go", caller),
					reconcileTestMembership("target.go", oldTarget),
				},
				[]IndexEdgeReplacement{
					{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", oldTarget)}},
					{SourcePath: "target.go"},
				},
			),
		))
		fixture.checkpoint.acknowledge(initial.View)

		ambiguous := fixture.mustReconcile(t, fixture.request(
			"ambiguous-v2",
			fixture.scopeA,
			reconcileTestParent(initial.View),
			fixture.completeScan(20,
				reconcileTestSnapshotFile("caller.go", caller),
				reconcileTestSnapshotFile("handler_a.go", handlerA),
				reconcileTestSnapshotFile("handler_b.go", handlerB),
			),
			reconcileTestPart(
				[]reconcileTestArtifact{caller, handlerA, handlerB},
				[]IndexMembership{
					reconcileTestMembership("caller.go", caller),
					reconcileTestMembership("handler_a.go", handlerA),
					reconcileTestMembership("handler_b.go", handlerB),
				},
				[]IndexEdgeReplacement{
					{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestAmbiguousEdge("caller.go", caller, "Handler")}},
					{SourcePath: "handler_a.go"},
					{SourcePath: "handler_b.go"},
				},
			),
		))
		fixture.checkpoint.acknowledge(ambiguous.View)

		fixture.requireUnresolved(t, ambiguous.View.Context, caller.entityKey, "calls")
		result := fixture.graph(t, ambiguous.View.Context, GraphTarget{Name: "Handler"}, GraphFilter{Direction: GraphDirectionBoth})
		if result.Outcome != GraphOutcomeAmbiguous {
			t.Fatalf("ambiguous target outcome = %q, want %q", result.Outcome, GraphOutcomeAmbiguous)
		}
		if len(result.Graph.Nodes) != 0 || len(result.Graph.Edges) != 0 {
			t.Fatalf("ambiguous target fabricated graph %#v", result.Graph)
		}
		fixture.requireGraphBound(t, result, ambiguous.View.Context)
		if got := reconcileTestEntityKeys(result.Candidates); !reconcileTestStringsEqual(got, []string{handlerA.entityKey, handlerB.entityKey}) {
			t.Fatalf("ambiguous candidates = %#v, want %#v", got, []string{handlerA.entityKey, handlerB.entityKey})
		}
	})
}

func TestUCIReconcileIncompleteAndFailedScansPreserveCurrentWhileCompleteEmptyDeletesAll(t *testing.T) {
	fixture := newReconcileTestFixture(t)
	caller := fixture.artifact(401, "symbol:delete-all-caller", "DeleteAllCaller")
	target := fixture.artifact(402, "symbol:delete-all-target", "DeleteAllTarget")
	otherCaller := fixture.artifact(403, "symbol:delete-all-other-caller", "DeleteAllOtherCaller")
	otherTarget := fixture.artifact(404, "symbol:delete-all-other-target", "DeleteAllOtherTarget")

	initialA := fixture.mustReconcile(t, fixture.request(
		"delete-all-a-v1",
		fixture.scopeA,
		nil,
		fixture.completeScan(10,
			reconcileTestSnapshotFile("caller.go", caller),
			reconcileTestSnapshotFile("target.go", target),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{caller, target},
			[]IndexMembership{
				reconcileTestMembership("caller.go", caller),
				reconcileTestMembership("target.go", target),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", target)}},
				{SourcePath: "target.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(initialA.View)

	initialB := fixture.mustReconcile(t, fixture.request(
		"delete-all-b-v1",
		fixture.scopeB,
		nil,
		fixture.completeScan(20,
			reconcileTestSnapshotFile("other_caller.go", otherCaller),
			reconcileTestSnapshotFile("other_target.go", otherTarget),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{otherCaller, otherTarget},
			[]IndexMembership{
				reconcileTestMembership("other_caller.go", otherCaller),
				reconcileTestMembership("other_target.go", otherTarget),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "other_caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("other_caller.go", otherCaller, "other_target.go", otherTarget)}},
				{SourcePath: "other_target.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(initialB.View)

	initialFootprint := fixture.store.viewCount(fixture.scopeA.CheckoutID)
	for _, outcome := range []IndexScanOutcome{IndexScanIncomplete, IndexScanFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			operations := fixture.store.operationCounts()
			_, err := fixture.reconcile(fixture.request(
				"delete-all-"+string(outcome),
				fixture.scopeA,
				reconcileTestParent(initialA.View),
				fixture.incompleteScan(30, outcome),
			))
			if err == nil {
				t.Fatalf("%s scan reconciled as current", outcome)
			}
			if got := fixture.store.operationCounts(); got != operations {
				t.Fatalf("%s scan invoked IndexStore operations %#v, want no Begin/Stage/Finalize", outcome, got)
			}
			if got := fixture.checkpoint.sequence(fixture.scopeA.CheckoutID); got != initialA.View.AcceptedFSSeq {
				t.Fatalf("%s scan advanced local acknowledgement to %d, want %d", outcome, got, initialA.View.AcceptedFSSeq)
			}
			if got := fixture.store.viewCount(fixture.scopeA.CheckoutID); got != initialFootprint {
				t.Fatalf("%s scan created %d A Views, want %d", outcome, got, initialFootprint)
			}
			fixture.requireCurrent(t, fixture.scopeA.CheckoutID, initialA.View.Context)
			fixture.requirePath(t, initialA.View.Context, "target.go", target)
			fixture.requireGraphEdge(t, initialA.View.Context, caller.entityKey, target.entityKey)
			fixture.requireCurrent(t, fixture.scopeB.CheckoutID, initialB.View.Context)
			fixture.requirePath(t, initialB.View.Context, "other_target.go", otherTarget)
			fixture.requireGraphEdge(t, initialB.View.Context, otherCaller.entityKey, otherTarget.entityKey)
		})
	}

	deleted := fixture.mustReconcile(t, fixture.request(
		"delete-all-complete-empty",
		fixture.scopeA,
		reconcileTestParent(initialA.View),
		fixture.completeScan(40),
	))
	fixture.checkpoint.acknowledge(deleted.View)

	if deleted.View.Context.Generation != initialA.View.Context.Generation+1 {
		t.Fatalf("complete empty scan generation = %d, want %d", deleted.View.Context.Generation, initialA.View.Context.Generation+1)
	}
	fixture.requireNoPath(t, deleted.View.Context, "caller.go")
	fixture.requireNoPath(t, deleted.View.Context, "target.go")
	emptyGraph := fixture.graph(t, deleted.View.Context, GraphTarget{EntityKey: caller.entityKey}, GraphFilter{Direction: GraphDirectionOutgoing})
	if len(emptyGraph.Graph.Edges) != 0 || len(emptyGraph.Unresolved) != 0 {
		t.Fatalf("complete delete-all retained graph evidence %#v", emptyGraph)
	}
	fixture.requireCurrent(t, fixture.scopeA.CheckoutID, deleted.View.Context)
	fixture.requireCurrent(t, fixture.scopeB.CheckoutID, initialB.View.Context)
	fixture.requirePath(t, initialB.View.Context, "other_target.go", otherTarget)
	fixture.requireGraphEdge(t, initialB.View.Context, otherCaller.entityKey, otherTarget.entityKey)
}

func TestUCIReconcileRejectsWrongIncarnationAndStaleLeaseFinalization(t *testing.T) {
	fixture := newReconcileTestFixture(t)
	caller := fixture.artifact(501, "symbol:fence-caller", "FenceCaller")
	target := fixture.artifact(502, "symbol:fence-target", "FenceTarget")
	updatedTarget := fixture.artifact(503, "symbol:fence-updated-target", "FenceUpdatedTarget")
	otherCaller := fixture.artifact(504, "symbol:fence-other-caller", "FenceOtherCaller")
	otherTarget := fixture.artifact(505, "symbol:fence-other-target", "FenceOtherTarget")

	initialA := fixture.mustReconcile(t, fixture.request(
		"fence-a-v1",
		fixture.scopeA,
		nil,
		fixture.completeScan(10,
			reconcileTestSnapshotFile("caller.go", caller),
			reconcileTestSnapshotFile("target.go", target),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{caller, target},
			[]IndexMembership{
				reconcileTestMembership("caller.go", caller),
				reconcileTestMembership("target.go", target),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", target)}},
				{SourcePath: "target.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(initialA.View)

	initialB := fixture.mustReconcile(t, fixture.request(
		"fence-b-v1",
		fixture.scopeB,
		nil,
		fixture.completeScan(20,
			reconcileTestSnapshotFile("other_caller.go", otherCaller),
			reconcileTestSnapshotFile("other_target.go", otherTarget),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{otherCaller, otherTarget},
			[]IndexMembership{
				reconcileTestMembership("other_caller.go", otherCaller),
				reconcileTestMembership("other_target.go", otherTarget),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "other_caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("other_caller.go", otherCaller, "other_target.go", otherTarget)}},
				{SourcePath: "other_target.go"},
			},
		),
	))
	fixture.checkpoint.acknowledge(initialB.View)

	baselineViews := fixture.store.viewCount(fixture.scopeA.CheckoutID)
	wrongScope := fixture.scopeA
	wrongScope.IncarnationID = "81000000-0000-4000-8000-000000000999"
	_, err := fixture.reconcile(fixture.request(
		"fence-wrong-incarnation",
		wrongScope,
		reconcileTestParent(initialA.View),
		fixture.completeScan(30,
			reconcileTestSnapshotFile("caller.go", caller),
			reconcileTestSnapshotFile("target.go", updatedTarget),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{caller, updatedTarget},
			[]IndexMembership{
				reconcileTestMembership("caller.go", caller),
				reconcileTestMembership("target.go", updatedTarget),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", updatedTarget)}},
				{SourcePath: "target.go"},
			},
		),
	))
	if err == nil {
		t.Fatal("wrong incarnation finalized a View")
	}
	fixture.requireCurrent(t, fixture.scopeA.CheckoutID, initialA.View.Context)
	fixture.requireCurrent(t, fixture.scopeB.CheckoutID, initialB.View.Context)
	if got := fixture.store.viewCount(fixture.scopeA.CheckoutID); got != baselineViews {
		t.Fatalf("wrong incarnation created %d A Views, want %d", got, baselineViews)
	}
	if got := fixture.checkpoint.sequence(fixture.scopeA.CheckoutID); got != initialA.View.AcceptedFSSeq {
		t.Fatalf("wrong incarnation advanced acknowledgement to %d, want %d", got, initialA.View.AcceptedFSSeq)
	}

	staleRequest := fixture.request(
		"fence-stale-lease",
		fixture.scopeA,
		reconcileTestParent(initialA.View),
		fixture.completeScan(40,
			reconcileTestSnapshotFile("caller.go", caller),
			reconcileTestSnapshotFile("target.go", updatedTarget),
		),
		reconcileTestPart(
			[]reconcileTestArtifact{caller, updatedTarget},
			[]IndexMembership{
				reconcileTestMembership("caller.go", caller),
				reconcileTestMembership("target.go", updatedTarget),
			},
			[]IndexEdgeReplacement{
				{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", updatedTarget)}},
				{SourcePath: "target.go"},
			},
		),
	)
	fixture.store.staleAfterStage(reconcileTestPublicationKey(t, fixture.caller, staleRequest))
	_, err = fixture.reconcile(staleRequest)
	if err == nil {
		t.Fatal("stale lease finalized a View")
	}
	fixture.requireCurrent(t, fixture.scopeA.CheckoutID, initialA.View.Context)
	fixture.requirePath(t, initialA.View.Context, "target.go", target)
	fixture.requireGraphEdge(t, initialA.View.Context, caller.entityKey, target.entityKey)
	fixture.requireCurrent(t, fixture.scopeB.CheckoutID, initialB.View.Context)
	fixture.requirePath(t, initialB.View.Context, "other_target.go", otherTarget)
	fixture.requireGraphEdge(t, initialB.View.Context, otherCaller.entityKey, otherTarget.entityKey)
	if got := fixture.store.viewCount(fixture.scopeA.CheckoutID); got != baselineViews {
		t.Fatalf("stale lease created %d A Views, want %d", got, baselineViews)
	}
	if got := fixture.checkpoint.sequence(fixture.scopeA.CheckoutID); got != initialA.View.AcceptedFSSeq {
		t.Fatalf("stale lease advanced acknowledgement to %d, want %d", got, initialA.View.AcceptedFSSeq)
	}
}

func TestUCIReplayAfterCrashAndLostACKIsDurableAndIdempotent(t *testing.T) {
	t.Run("crash before durable finalization retains dirty acknowledgement until retry", func(t *testing.T) {
		fixture := newReconcileTestFixture(t)
		caller := fixture.artifact(601, "symbol:crash-caller", "CrashCaller")
		targetV1 := fixture.artifact(602, "symbol:crash-target-v1", "CrashTargetV1")
		targetV2 := fixture.artifact(603, "symbol:crash-target-v2", "CrashTargetV2")

		initial := fixture.mustReconcile(t, fixture.request(
			"crash-v1",
			fixture.scopeA,
			nil,
			fixture.completeScan(10,
				reconcileTestSnapshotFile("caller.go", caller),
				reconcileTestSnapshotFile("target.go", targetV1),
			),
			reconcileTestPart(
				[]reconcileTestArtifact{caller, targetV1},
				[]IndexMembership{
					reconcileTestMembership("caller.go", caller),
					reconcileTestMembership("target.go", targetV1),
				},
				[]IndexEdgeReplacement{
					{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", targetV1)}},
					{SourcePath: "target.go"},
				},
			),
		))
		fixture.checkpoint.acknowledge(initial.View)

		request := fixture.request(
			"crash-v2",
			fixture.scopeA,
			reconcileTestParent(initial.View),
			fixture.completeScan(20,
				reconcileTestSnapshotFile("caller.go", caller),
				reconcileTestSnapshotFile("target.go", targetV2),
			),
			reconcileTestPart(
				[]reconcileTestArtifact{caller, targetV2},
				[]IndexMembership{
					reconcileTestMembership("caller.go", caller),
					reconcileTestMembership("target.go", targetV2),
				},
				[]IndexEdgeReplacement{
					{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", targetV2)}},
					{SourcePath: "target.go"},
				},
			),
		)
		fixture.store.failNextFinalize(reconcileTestPublicationKey(t, fixture.caller, request))
		if _, err := fixture.reconcile(request); err == nil {
			t.Fatal("crash before durable finalization returned a published result")
		}
		fixture.requireCurrent(t, fixture.scopeA.CheckoutID, initial.View.Context)
		fixture.requirePath(t, initial.View.Context, "target.go", targetV1)
		fixture.requireGraphEdge(t, initial.View.Context, caller.entityKey, targetV1.entityKey)
		if got := fixture.checkpoint.sequence(fixture.scopeA.CheckoutID); got != initial.View.AcceptedFSSeq {
			t.Fatalf("crash advanced acknowledgement to %d, want %d", got, initial.View.AcceptedFSSeq)
		}
		if got := fixture.store.viewCount(fixture.scopeA.CheckoutID); got != 1 {
			t.Fatalf("crash created %d durable Views, want 1", got)
		}

		retried := fixture.mustReconcile(t, request)
		fixture.checkpoint.acknowledge(retried.View)
		fixture.requireCurrent(t, fixture.scopeA.CheckoutID, retried.View.Context)
		fixture.requirePath(t, retried.View.Context, "target.go", targetV2)
		fixture.requireGraphEdge(t, retried.View.Context, caller.entityKey, targetV2.entityKey)
		if got := fixture.checkpoint.sequence(fixture.scopeA.CheckoutID); got != retried.View.AcceptedFSSeq {
			t.Fatalf("retry acknowledgement = %d, want %d", got, retried.View.AcceptedFSSeq)
		}
	})

	t.Run("lost acknowledgement after durable finalization replays one immutable result", func(t *testing.T) {
		fixture := newReconcileTestFixture(t)
		caller := fixture.artifact(611, "symbol:lost-ack-caller", "LostACKCaller")
		targetV1 := fixture.artifact(612, "symbol:lost-ack-target-v1", "LostACKTargetV1")
		targetV2 := fixture.artifact(613, "symbol:lost-ack-target-v2", "LostACKTargetV2")

		initial := fixture.mustReconcile(t, fixture.request(
			"lost-ack-v1",
			fixture.scopeA,
			nil,
			fixture.completeScan(10,
				reconcileTestSnapshotFile("caller.go", caller),
				reconcileTestSnapshotFile("target.go", targetV1),
			),
			reconcileTestPart(
				[]reconcileTestArtifact{caller, targetV1},
				[]IndexMembership{
					reconcileTestMembership("caller.go", caller),
					reconcileTestMembership("target.go", targetV1),
				},
				[]IndexEdgeReplacement{
					{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", targetV1)}},
					{SourcePath: "target.go"},
				},
			),
		))
		fixture.checkpoint.acknowledge(initial.View)

		request := fixture.request(
			"lost-ack-v2",
			fixture.scopeA,
			reconcileTestParent(initial.View),
			fixture.completeScan(20,
				reconcileTestSnapshotFile("caller.go", caller),
				reconcileTestSnapshotFile("target.go", targetV2),
			),
			reconcileTestPart(
				[]reconcileTestArtifact{caller, targetV2},
				[]IndexMembership{
					reconcileTestMembership("caller.go", caller),
					reconcileTestMembership("target.go", targetV2),
				},
				[]IndexEdgeReplacement{
					{SourcePath: "caller.go", Edges: []IndexEdge{reconcileTestResolvedEdge("caller.go", caller, "target.go", targetV2)}},
					{SourcePath: "target.go"},
				},
			),
		)
		durable := fixture.mustReconcile(t, request)
		fixture.requireCurrent(t, fixture.scopeA.CheckoutID, durable.View.Context)
		fixture.requirePath(t, durable.View.Context, "target.go", targetV2)
		fixture.requireGraphEdge(t, durable.View.Context, caller.entityKey, targetV2.entityKey)
		if got := fixture.checkpoint.sequence(fixture.scopeA.CheckoutID); got != initial.View.AcceptedFSSeq {
			t.Fatalf("lost acknowledgement advanced local checkpoint to %d, want %d", got, initial.View.AcceptedFSSeq)
		}
		viewCount := fixture.store.viewCount(fixture.scopeA.CheckoutID)

		replayed := fixture.mustReconcile(t, request)
		if !reconcileTestPublishedEqual(replayed.View, durable.View) {
			t.Fatalf("lost-ACK replay = %#v, want durable %#v", replayed.View, durable.View)
		}
		if got := fixture.store.viewCount(fixture.scopeA.CheckoutID); got != viewCount {
			t.Fatalf("exact replay created %d durable Views, want %d", got, viewCount)
		}
		fixture.checkpoint.acknowledge(replayed.View)
		if got := fixture.checkpoint.sequence(fixture.scopeA.CheckoutID); got != replayed.View.AcceptedFSSeq {
			t.Fatalf("replayed acknowledgement = %d, want %d", got, replayed.View.AcceptedFSSeq)
		}
	})

	t.Run("same caller key with changed payload is not an exact replay", func(t *testing.T) {
		fixture := newReconcileTestFixture(t)
		firstArtifact := fixture.artifact(621, "symbol:first-payload", "FirstPayload")
		changedArtifact := fixture.artifact(622, "symbol:changed-payload", "ChangedPayload")

		firstRequest := fixture.request(
			"reused-caller-key",
			fixture.scopeA,
			nil,
			fixture.completeScan(10, reconcileTestSnapshotFile("value.go", firstArtifact)),
			reconcileTestPart(
				[]reconcileTestArtifact{firstArtifact},
				[]IndexMembership{reconcileTestMembership("value.go", firstArtifact)},
				[]IndexEdgeReplacement{{SourcePath: "value.go"}},
			),
		)
		published := fixture.mustReconcile(t, firstRequest)
		viewCount := fixture.store.viewCount(fixture.scopeA.CheckoutID)

		changedRequest := fixture.request(
			"reused-caller-key",
			fixture.scopeA,
			nil,
			fixture.completeScan(11, reconcileTestSnapshotFile("value.go", changedArtifact)),
			reconcileTestPart(
				[]reconcileTestArtifact{changedArtifact},
				[]IndexMembership{reconcileTestMembership("value.go", changedArtifact)},
				[]IndexEdgeReplacement{{SourcePath: "value.go"}},
			),
		)
		if _, err := fixture.reconcile(changedRequest); err == nil {
			t.Fatal("changed payload under one caller key replayed the prior durable result")
		}
		fixture.requireCurrent(t, fixture.scopeA.CheckoutID, published.View.Context)
		fixture.requirePath(t, published.View.Context, "value.go", firstArtifact)
		if got := fixture.store.viewCount(fixture.scopeA.CheckoutID); got != viewCount {
			t.Fatalf("changed replay created %d Views, want %d", got, viewCount)
		}
	})
}

type reconcileTestFixture struct {
	service    *ReconcileService
	store      *reconcileTestStore
	checkpoint *reconcileTestCheckpoint
	caller     IndexCaller
	scopeA     IndexScope
	scopeB     IndexScope
	profileID  string
}

func newReconcileTestFixture(t *testing.T) *reconcileTestFixture {
	t.Helper()
	fixture := &reconcileTestFixture{
		store:      newReconcileTestStore(),
		checkpoint: newReconcileTestCheckpoint(),
		caller: IndexCaller{
			AuthRealm:     "reconcile-test-realm",
			Principal:     "reconcile-test-principal",
			OwnerInstance: "reconcile-test-owner",
		},
		scopeA: IndexScope{
			SourceID:      "71000000-0000-4000-8000-000000000001",
			CheckoutID:    "72000000-0000-4000-8000-000000000001",
			IncarnationID: "73000000-0000-4000-8000-000000000001",
		},
		scopeB: IndexScope{
			SourceID:      "71000000-0000-4000-8000-000000000001",
			CheckoutID:    "72000000-0000-4000-8000-000000000002",
			IncarnationID: "73000000-0000-4000-8000-000000000002",
		},
		profileID: "74000000-0000-4000-8000-000000000001",
	}
	fixture.store.registerCheckout(fixture.scopeA)
	fixture.store.registerCheckout(fixture.scopeB)
	fixture.service = NewReconcileService(fixture.store)
	return fixture
}

func (fixture *reconcileTestFixture) artifact(serial int, entityKey, localName string) reconcileTestArtifact {
	text := "func " + localName + "() {}\n"
	content := sha256.Sum256([]byte(text))
	facts := sha256.Sum256([]byte("facts\x00" + entityKey))
	artifact := reconcileTestArtifact{
		proof: IndexArtifactProof{
			ArtifactID:         fmt.Sprintf("75000000-0000-4000-8000-%012d", serial),
			ContentDigest:      IndexDigest("sha256:" + hex.EncodeToString(content[:])),
			FactsDigest:        IndexDigest("sha256:" + hex.EncodeToString(facts[:])),
			DefinitionCount:    1,
			ReferenceSiteCount: 1,
			ChunkCount:         1,
		},
		entityKey:       entityKey,
		localName:       localName,
		qualifiedSymbol: "reconcile." + localName,
		text:            text,
	}
	fixture.store.registerArtifacts(artifact)
	return artifact
}

func (fixture *reconcileTestFixture) request(key string, scope IndexScope, parent *ContextRef, scan ScannerResult, parts ...IndexPart) ReconcileRequest {
	return ReconcileRequest{
		BuildKey:       key,
		Scope:          scope,
		ProfileID:      fixture.profileID,
		ExpectedParent: parent,
		Scan:           scan,
		Parts:          append([]IndexPart(nil), parts...),
	}
}

func (fixture *reconcileTestFixture) completeScan(sequence int64, files ...ScannerFile) ScannerResult {
	started := time.Unix(sequence, 0).UTC()
	return ScannerResult{
		Files: files,
		Census: ScannerCensus{
			Outcome:      IndexScanComplete,
			Complete:     true,
			CanDeleteAll: true,
		},
		Observation: IndexObservation{
			ObservedFSSeq: sequence,
			ScanStart:     started,
			ScanEnd:       started.Add(time.Second),
		},
		Coverage: IndexCoverage{
			Structural: IndexCoverageComplete,
			Lexical:    IndexCoverageComplete,
			Vector:     IndexCoverageUnavailable,
		},
	}
}

func (fixture *reconcileTestFixture) incompleteScan(sequence int64, outcome IndexScanOutcome) ScannerResult {
	started := time.Unix(sequence, 0).UTC()
	return ScannerResult{
		Census: ScannerCensus{
			Outcome:      outcome,
			Complete:     false,
			CanDeleteAll: false,
		},
		Observation: IndexObservation{
			ObservedFSSeq: sequence,
			ScanStart:     started,
			ScanEnd:       started.Add(time.Second),
		},
		Coverage: IndexCoverage{
			Structural: IndexCoverageUnavailable,
			Lexical:    IndexCoverageUnavailable,
			Vector:     IndexCoverageUnavailable,
		},
	}
}

func (fixture *reconcileTestFixture) reconcile(request ReconcileRequest) (ReconcileResult, error) {
	return fixture.service.Reconcile(context.Background(), fixture.caller, request)
}

func (fixture *reconcileTestFixture) mustReconcile(t *testing.T, request ReconcileRequest) ReconcileResult {
	t.Helper()
	result, err := fixture.reconcile(request)
	if err != nil {
		t.Fatalf("Reconcile(%q) error = %v", request.BuildKey, err)
	}
	return result
}

func reconcileTestPublicationKey(t *testing.T, caller IndexCaller, request ReconcileRequest) string {
	t.Helper()
	plan, err := prepareReconcilePlan(caller, request)
	if err != nil {
		t.Fatalf("prepare reconcile plan: %v", err)
	}
	return plan.begin.BuildKey
}

func (fixture *reconcileTestFixture) requirePath(t *testing.T, ref ContextRef, path string, artifact reconcileTestArtifact) {
	t.Helper()
	result := fixture.query(t, ref, path)
	if result.Response.Contexts == nil || result.Response.Items == nil {
		t.Fatalf("query %q omitted its current contextual envelope: %#v", path, result.Response)
	}
	items := *result.Response.Items
	if len(items) != 1 {
		t.Fatalf("query %q items = %#v, want one current item", path, items)
	}
	item := items[0]
	if item.Path != path || item.Ref.ViewID != ref.ViewID || item.Ref.SourceID != ref.SourceID || item.Ref.EntityKey != artifact.entityKey {
		t.Fatalf("query %q item = %#v, want current %q/%q/%q", path, item, ref.ViewID, ref.SourceID, artifact.entityKey)
	}
	wantDigest, ok := queryBareContentDigest(artifact.proof.ContentDigest)
	if !ok {
		t.Fatalf("artifact %q has invalid content digest %q", artifact.proof.ArtifactID, artifact.proof.ContentDigest)
	}
	if item.ContentDigest != wantDigest {
		t.Fatalf("query %q digest = %q, want %q", path, item.ContentDigest, wantDigest)
	}
}

func (fixture *reconcileTestFixture) requireNoPath(t *testing.T, ref ContextRef, path string) {
	t.Helper()
	result := fixture.query(t, ref, path)
	if result.Response.Contexts == nil || result.Response.Items == nil {
		t.Fatalf("query %q omitted its current contextual envelope: %#v", path, result.Response)
	}
	if len(*result.Response.Items) != 0 {
		t.Fatalf("query %q retained stale items %#v", path, result.Response.Items)
	}
}

func (fixture *reconcileTestFixture) query(t *testing.T, ref ContextRef, path string) QueryResult {
	t.Helper()
	result, err := NewQueryService(fixture.store).Query(context.Background(), newAuthorizedContext(ref), QuerySpec{
		ClientSessionID: "reconcile-query-client",
		Mode:            QueryModeExactRelativePath,
		Text:            path,
		Filter:          QueryFilter{Languages: []string{"go"}},
		Order:           QueryOrderPath,
		Limit:           8,
	})
	if err != nil {
		t.Fatalf("Query(%q) error = %v", path, err)
	}
	return result
}

func (fixture *reconcileTestFixture) requireGraphEdge(t *testing.T, ref ContextRef, fromKey, toKey string) {
	t.Helper()
	result := fixture.graph(t, ref, GraphTarget{EntityKey: fromKey}, GraphFilter{
		Direction: GraphDirectionOutgoing,
		Relations: []IndexRelation{"calls"},
	})
	if result.Outcome != GraphOutcomeComplete {
		t.Fatalf("graph %q outcome = %q, want %q", fromKey, result.Outcome, GraphOutcomeComplete)
	}
	fixture.requireGraphBound(t, result, ref)
	for _, edge := range result.Graph.Edges {
		if edge.From.EntityKey == fromKey && edge.To.EntityKey == toKey {
			return
		}
	}
	t.Fatalf("graph %q has no current edge to %q: %#v", fromKey, toKey, result.Graph.Edges)
}

func (fixture *reconcileTestFixture) requireNoGraphEndpoint(t *testing.T, ref ContextRef, fromKey, forbiddenTo string) {
	t.Helper()
	result := fixture.graph(t, ref, GraphTarget{EntityKey: fromKey}, GraphFilter{Direction: GraphDirectionOutgoing})
	fixture.requireGraphBound(t, result, ref)
	for _, edge := range result.Graph.Edges {
		if edge.To.EntityKey == forbiddenTo {
			t.Fatalf("graph %q retained stale endpoint %q in %#v", fromKey, forbiddenTo, result.Graph.Edges)
		}
	}
}

func (fixture *reconcileTestFixture) requireUnresolved(t *testing.T, ref ContextRef, fromKey, relation string) {
	t.Helper()
	result := fixture.graph(t, ref, GraphTarget{EntityKey: fromKey}, GraphFilter{Direction: GraphDirectionOutgoing})
	if result.Outcome != GraphOutcomeUnknownOrTruncated {
		t.Fatalf("unresolved graph %q outcome = %q, want %q", fromKey, result.Outcome, GraphOutcomeUnknownOrTruncated)
	}
	fixture.requireGraphBound(t, result, ref)
	if len(result.Graph.Edges) != 0 {
		t.Fatalf("unresolved graph %q fabricated resolved edges %#v", fromKey, result.Graph.Edges)
	}
	for _, unresolved := range result.Unresolved {
		if unresolved.From.EntityKey == fromKey && unresolved.Relation == IndexRelation(relation) {
			return
		}
	}
	t.Fatalf("graph %q missing unresolved %q evidence %#v", fromKey, relation, result.Unresolved)
}

func (fixture *reconcileTestFixture) graph(t *testing.T, ref ContextRef, target GraphTarget, filter GraphFilter) GraphResult {
	t.Helper()
	result, err := NewGraphService(fixture.store).Explore(context.Background(), newAuthorizedContext(ref), GraphSpec{
		ClientSessionID: "reconcile-graph-client",
		Action:          GraphActionExplain,
		Target:          target,
		Filter:          filter,
		Budget: GraphBudget{
			MaxDepth:   2,
			MaxVisited: 16,
			MaxNodes:   16,
			MaxEdges:   16,
		},
	})
	if err != nil {
		t.Fatalf("Explore(%#v) error = %v", target, err)
	}
	return result
}

func (fixture *reconcileTestFixture) requireGraphBound(t *testing.T, result GraphResult, ref ContextRef) {
	t.Helper()
	for _, candidate := range result.Candidates {
		reconcileTestRequireRefBound(t, candidate, ref, "ambiguous candidate")
	}
	for _, node := range result.Graph.Nodes {
		reconcileTestRequireRefBound(t, node, ref, "graph node")
	}
	for _, edge := range result.Graph.Edges {
		reconcileTestRequireRefBound(t, edge.From, ref, "edge source")
		reconcileTestRequireRefBound(t, edge.To, ref, "edge target")
		for _, evidence := range edge.EvidenceRefs {
			reconcileTestRequireRefBound(t, evidence, ref, "edge evidence")
		}
	}
	for _, unresolved := range result.Unresolved {
		reconcileTestRequireRefBound(t, unresolved.From, ref, "unresolved source")
		for _, evidence := range unresolved.EvidenceRefs {
			reconcileTestRequireRefBound(t, evidence, ref, "unresolved evidence")
		}
	}
}

func (fixture *reconcileTestFixture) requireCurrent(t *testing.T, checkoutID string, want ContextRef) {
	t.Helper()
	got, found := fixture.store.currentContext(checkoutID)
	if !found || !reconcileTestContextsEqual(got, want) {
		t.Fatalf("checkout %q current context = %#v (found=%t), want %#v", checkoutID, got, found, want)
	}
}

func reconcileTestParent(view IndexPublishedView) *ContextRef {
	parent := view.Context
	return &parent
}

func reconcileTestSnapshotFile(path string, artifact reconcileTestArtifact) ScannerFile {
	return ScannerFile{
		Path:  path,
		Body:  append([]byte(nil), []byte(artifact.text)...),
		State: IndexFilePresent,
	}
}

func reconcileTestPart(artifacts []reconcileTestArtifact, memberships []IndexMembership, replacements []IndexEdgeReplacement) IndexPart {
	proofs := make([]IndexArtifactProof, 0, len(artifacts))
	for _, artifact := range artifacts {
		proofs = append(proofs, artifact.proof)
	}
	return IndexPart{
		Artifacts:        proofs,
		Memberships:      append([]IndexMembership(nil), memberships...),
		EdgeReplacements: append([]IndexEdgeReplacement(nil), replacements...),
	}
}

func reconcileTestMembership(path string, artifact reconcileTestArtifact) IndexMembership {
	artifactID := artifact.proof.ArtifactID
	return IndexMembership{
		PathKey:     path,
		DisplayPath: path,
		Mode:        "100644",
		State:       IndexFilePresent,
		ArtifactID:  &artifactID,
	}
}

func reconcileTestResolvedEdge(sourcePath string, source reconcileTestArtifact, targetPath string, target reconcileTestArtifact) IndexEdge {
	sourceSymbol := source.entityKey
	targetSymbol := target.entityKey
	return IndexEdge{
		EdgeKey:          sourcePath + "->" + targetPath,
		SourceArtifactID: source.proof.ArtifactID,
		SourceSymbolKey:  &sourceSymbol,
		Target: &IndexEdgeTarget{
			PathKey:    targetPath,
			ArtifactID: target.proof.ArtifactID,
			SymbolKey:  &targetSymbol,
		},
		Relation:         IndexRelation("calls"),
		EvidenceKind:     IndexEvidenceKind("resolved"),
		ResolutionState:  IndexResolutionState("resolved"),
		ResolverRevision: "reconcile-test-resolver-v1",
		Evidence: IndexEdgeEvidence{
			Span:        IndexSpan{ByteStart: 0, ByteEnd: 1, LineStart: 1, LineEnd: 1},
			RuleKey:     "reconcile-test-call",
			Explanation: "static call resolved from immutable extraction facts",
		},
	}
}

func reconcileTestUnresolvedEdge(sourcePath string, source reconcileTestArtifact, key string) IndexEdge {
	sourceSymbol := source.entityKey
	return IndexEdge{
		EdgeKey:          sourcePath + "->" + key,
		SourceArtifactID: source.proof.ArtifactID,
		SourceSymbolKey:  &sourceSymbol,
		Relation:         IndexRelation("calls"),
		EvidenceKind:     IndexEvidenceKind("unresolved"),
		ResolutionState:  IndexResolutionState("unresolved"),
		ResolverRevision: "reconcile-test-resolver-v1",
		Evidence: IndexEdgeEvidence{
			Span:        IndexSpan{ByteStart: 0, ByteEnd: 1, LineStart: 1, LineEnd: 1},
			RuleKey:     "reconcile-test-call",
			Explanation: "static call has no current target",
		},
	}
}

func reconcileTestAmbiguousEdge(sourcePath string, source reconcileTestArtifact, name string) IndexEdge {
	edge := reconcileTestUnresolvedEdge(sourcePath, source, "ambiguous-"+name)
	edge.ResolutionState = IndexResolutionState("ambiguous")
	edge.Evidence.Explanation = "static call has multiple current targets"
	return edge
}

type reconcileTestArtifact struct {
	proof           IndexArtifactProof
	entityKey       string
	localName       string
	qualifiedSymbol string
	text            string
}

func (artifact reconcileTestArtifact) candidate(ref ContextRef, path string) QueryCandidate {
	return QueryCandidate{
		Context:         ref.clone(),
		Proof:           artifact.proof,
		EntityKey:       artifact.entityKey,
		LocalName:       artifact.localName,
		QualifiedSymbol: artifact.qualifiedSymbol,
		RelativePath:    path,
		Span: IndexSpan{
			ByteStart: 0,
			ByteEnd:   int64(len(artifact.text)),
			LineStart: 1,
			LineEnd:   1,
		},
		Text:     artifact.text,
		Kind:     QueryItemCode,
		Language: "go",
		Score:    1,
	}
}

type reconcileTestCheckpoint struct {
	sequences map[string]int64
}

func newReconcileTestCheckpoint() *reconcileTestCheckpoint {
	return &reconcileTestCheckpoint{sequences: make(map[string]int64)}
}

func (checkpoint *reconcileTestCheckpoint) acknowledge(view IndexPublishedView) {
	if existing := checkpoint.sequences[view.Context.CheckoutID]; view.AcceptedFSSeq < existing {
		return
	}
	checkpoint.sequences[view.Context.CheckoutID] = view.AcceptedFSSeq
}

func (checkpoint *reconcileTestCheckpoint) sequence(checkoutID string) int64 {
	return checkpoint.sequences[checkoutID]
}

type reconcileTestStore struct {
	mu sync.Mutex

	artifacts     map[string]reconcileTestArtifact
	checkouts     map[string]IndexScope
	leaseEpochs   map[string]int64
	builds        map[string]*reconcileTestBuild
	nextBuildID   int
	current       map[string]reconcileTestView
	history       map[string][]reconcileTestView
	failFinalize  map[string]int
	staleStage    map[string]bool
	attempts      reconcileTestStoreOperations
	beforePublish func()
}

type reconcileTestBuild struct {
	input     IndexBeginInput
	caller    IndexCaller
	ref       IndexBuildRef
	staged    map[uint32]reconcileTestStagedPart
	published *IndexPublishedView
}

type reconcileTestStagedPart struct {
	digest IndexDigest
	part   IndexPart
}

type reconcileTestView struct {
	published   IndexPublishedView
	memberships map[string]IndexMembership
	candidates  []QueryCandidate
	edges       []QueryGraphEdge
	unresolved  []GraphUnresolvedSite
	coverage    IndexCoverageState
}

type reconcileTestStoreOperations struct {
	Begins    int
	Stages    int
	Finalizes int
}

var (
	_ IndexStore = (*reconcileTestStore)(nil)
	_ QueryStore = (*reconcileTestStore)(nil)
	_ GraphStore = (*reconcileTestStore)(nil)
)

func newReconcileTestStore() *reconcileTestStore {
	return &reconcileTestStore{
		artifacts:    make(map[string]reconcileTestArtifact),
		checkouts:    make(map[string]IndexScope),
		leaseEpochs:  make(map[string]int64),
		builds:       make(map[string]*reconcileTestBuild),
		current:      make(map[string]reconcileTestView),
		history:      make(map[string][]reconcileTestView),
		failFinalize: make(map[string]int),
		staleStage:   make(map[string]bool),
	}
}

func (store *reconcileTestStore) registerCheckout(scope IndexScope) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.checkouts[scope.CheckoutID] = scope
}

func (store *reconcileTestStore) registerArtifacts(artifacts ...reconcileTestArtifact) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, artifact := range artifacts {
		store.artifacts[artifact.proof.ArtifactID] = artifact
	}
}

func (store *reconcileTestStore) setBeforePublish(hook func()) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.beforePublish = hook
}

func (store *reconcileTestStore) failNextFinalize(buildKey string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failFinalize[buildKey]++
}

func (store *reconcileTestStore) staleAfterStage(buildKey string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.staleStage[buildKey] = true
}

func (store *reconcileTestStore) Begin(ctx context.Context, caller IndexCaller, input IndexBeginInput) (IndexBeginResult, error) {
	if err := ctx.Err(); err != nil {
		return IndexBeginResult{}, err
	}
	store.mu.Lock()
	store.attempts.Begins++
	defer store.mu.Unlock()

	registered, found := store.checkouts[input.Scope.CheckoutID]
	if !found || registered != input.Scope {
		return IndexBeginResult{}, errReconcileTestWrongIncarnation
	}
	if input.Mode != IndexManifestFull || input.JobKind != IndexJobReconcile {
		return IndexBeginResult{}, errReconcileTestRejected
	}
	if build, found := store.builds[input.BuildKey]; found {
		if build.caller != caller || !reconcileTestBeginInputsEqual(build.input, input) {
			return IndexBeginResult{}, errReconcileTestRejected
		}
		result := IndexBeginResult{Build: build.ref, LeaseExpiresAt: time.Unix(1, 0).UTC()}
		if build.published != nil {
			published := *build.published
			result.Published = &published
		}
		return result, nil
	}
	if !reconcileTestCurrentMatchesParent(store.current[input.Scope.CheckoutID], store.currentHas(input.Scope.CheckoutID), input.ExpectedParent) {
		return IndexBeginResult{}, errReconcileTestRejected
	}

	store.leaseEpochs[input.Scope.CheckoutID]++
	store.nextBuildID++
	buildID := fmt.Sprintf("78000000-0000-4000-8000-%012d", store.nextBuildID)
	build := &reconcileTestBuild{
		input:  input,
		caller: caller,
		ref: IndexBuildRef{
			BuildID:       buildID,
			Scope:         input.Scope,
			OwnerInstance: caller.OwnerInstance,
			LeaseEpoch:    store.leaseEpochs[input.Scope.CheckoutID],
		},
		staged: make(map[uint32]reconcileTestStagedPart),
	}
	store.builds[input.BuildKey] = build
	return IndexBeginResult{Build: build.ref, LeaseExpiresAt: time.Unix(1, 0).UTC()}, nil
}

func (store *reconcileTestStore) Stage(ctx context.Context, caller IndexCaller, input IndexStageInput) (IndexPartAck, error) {
	if err := ctx.Err(); err != nil {
		return IndexPartAck{}, err
	}
	store.mu.Lock()
	store.attempts.Stages++
	defer store.mu.Unlock()

	build, found := store.buildByRefLocked(input.Build)
	if !found || build.caller != caller {
		return IndexPartAck{}, errReconcileTestRejected
	}
	if store.leaseEpochs[input.Build.Scope.CheckoutID] != input.Build.LeaseEpoch {
		return IndexPartAck{}, errReconcileTestLeaseStale
	}
	digest, err := DigestIndexPart(input.Part)
	if err != nil || digest != input.Digest {
		return IndexPartAck{}, errReconcileTestRejected
	}
	if existing, found := build.staged[input.Sequence]; found {
		if existing.digest != input.Digest {
			return IndexPartAck{}, errReconcileTestRejected
		}
		return IndexPartAck{BuildID: input.Build.BuildID, Sequence: input.Sequence, Digest: existing.digest}, nil
	}
	if input.Sequence != uint32(len(build.staged)) {
		return IndexPartAck{}, errReconcileTestRejected
	}
	build.staged[input.Sequence] = reconcileTestStagedPart{digest: input.Digest, part: input.Part}
	if store.staleStage[build.input.BuildKey] {
		delete(store.staleStage, build.input.BuildKey)
		store.leaseEpochs[input.Build.Scope.CheckoutID]++
	}
	return IndexPartAck{BuildID: input.Build.BuildID, Sequence: input.Sequence, Digest: input.Digest}, nil
}

func (store *reconcileTestStore) Finalize(ctx context.Context, caller IndexCaller, input IndexFinalizeInput) (IndexPublishedView, error) {
	if err := ctx.Err(); err != nil {
		return IndexPublishedView{}, err
	}
	store.mu.Lock()
	store.attempts.Finalizes++
	build, found := store.buildByRefLocked(input.Build)
	if !found || build.caller != caller {
		store.mu.Unlock()
		return IndexPublishedView{}, errReconcileTestRejected
	}
	if build.published != nil {
		published := *build.published
		store.mu.Unlock()
		return published, nil
	}
	if store.leaseEpochs[input.Build.Scope.CheckoutID] != input.Build.LeaseEpoch {
		store.mu.Unlock()
		return IndexPublishedView{}, errReconcileTestLeaseStale
	}
	if !reconcileTestContextsEqualOrNil(input.ExpectedParent, build.input.ExpectedParent) ||
		!reconcileTestCurrentMatchesParent(store.current[input.Build.Scope.CheckoutID], store.currentHas(input.Build.Scope.CheckoutID), input.ExpectedParent) {
		store.mu.Unlock()
		return IndexPublishedView{}, errReconcileTestRejected
	}
	if store.failFinalize[build.input.BuildKey] > 0 {
		store.failFinalize[build.input.BuildKey]--
		store.mu.Unlock()
		return IndexPublishedView{}, errReconcileTestCrash
	}
	view, err := store.buildViewLocked(build, input.Manifest)
	if err != nil {
		store.mu.Unlock()
		return IndexPublishedView{}, err
	}
	hook := store.beforePublish
	store.mu.Unlock()

	if hook != nil {
		hook()
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	build, found = store.buildByRefLocked(input.Build)
	if !found || build.caller != caller || build.published != nil ||
		store.leaseEpochs[input.Build.Scope.CheckoutID] != input.Build.LeaseEpoch ||
		!reconcileTestCurrentMatchesParent(store.current[input.Build.Scope.CheckoutID], store.currentHas(input.Build.Scope.CheckoutID), input.ExpectedParent) {
		return IndexPublishedView{}, errReconcileTestRejected
	}
	store.current[input.Build.Scope.CheckoutID] = view
	store.history[input.Build.Scope.CheckoutID] = append(store.history[input.Build.Scope.CheckoutID], view)
	published := view.published
	build.published = &published
	return published, nil
}

func (store *reconcileTestStore) SelectCandidates(ctx context.Context, authorized AuthorizedContext, spec QuerySpec) (QueryStoreResult, error) {
	if err := ctx.Err(); err != nil {
		return QueryStoreResult{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	view, found := store.currentForContextLocked(authorized.Ref())
	if !found {
		return QueryStoreResult{Coverage: IndexCoverageUnavailable}, nil
	}
	result := QueryStoreResult{Coverage: view.coverage}
	for _, candidate := range view.candidates {
		if reconcileTestQueryMatches(candidate, spec) {
			result.Candidates = append(result.Candidates, candidate)
		}
	}
	sort.Slice(result.Candidates, func(left, right int) bool {
		if result.Candidates[left].RelativePath != result.Candidates[right].RelativePath {
			return result.Candidates[left].RelativePath < result.Candidates[right].RelativePath
		}
		return result.Candidates[left].EntityKey < result.Candidates[right].EntityKey
	})
	return result, nil
}

func (store *reconcileTestStore) ResolveGraphTargets(ctx context.Context, authorized AuthorizedContext, target GraphTarget) (GraphTargetResolution, error) {
	if err := ctx.Err(); err != nil {
		return GraphTargetResolution{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	view, found := store.currentForContextLocked(authorized.Ref())
	if !found {
		return GraphTargetResolution{Coverage: IndexCoverageUnavailable}, nil
	}
	result := GraphTargetResolution{Coverage: view.coverage}
	for _, candidate := range view.candidates {
		if target.EntityKey == candidate.EntityKey || (target.EntityKey == "" && (target.Name == candidate.LocalName || target.Name == candidate.QualifiedSymbol)) {
			result.Candidates = append(result.Candidates, reconcileTestEntityRef(candidate))
		}
	}
	sort.Slice(result.Candidates, func(left, right int) bool {
		return result.Candidates[left].EntityKey < result.Candidates[right].EntityKey
	})
	return result, nil
}

func (store *reconcileTestStore) SelectGraphEdges(ctx context.Context, authorized AuthorizedContext, query GraphEdgeQuery) (GraphStoreResult, error) {
	if err := ctx.Err(); err != nil {
		return GraphStoreResult{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	view, found := store.currentForContextLocked(authorized.Ref())
	if !found {
		return GraphStoreResult{Coverage: IndexCoverageUnavailable}, nil
	}
	result := GraphStoreResult{Coverage: view.coverage}
	for _, edge := range view.edges {
		if reconcileTestGraphEdgeMatches(edge, query) {
			result.Edges = append(result.Edges, edge)
		}
	}
	for _, unresolved := range view.unresolved {
		if reconcileTestUnresolvedMatches(unresolved, query) {
			result.Unresolved = append(result.Unresolved, unresolved)
		}
	}
	return result, nil
}

func (store *reconcileTestStore) buildByRefLocked(ref IndexBuildRef) (*reconcileTestBuild, bool) {
	for _, build := range store.builds {
		if build.ref == ref {
			return build, true
		}
	}
	return nil, false
}

func (store *reconcileTestStore) buildViewLocked(build *reconcileTestBuild, manifest IndexManifestCompletion) (reconcileTestView, error) {
	parts, acknowledgements, err := reconcileTestStagedParts(build)
	if err != nil {
		return reconcileTestView{}, err
	}
	partsDigest, err := DigestIndexParts(acknowledgements)
	if err != nil {
		return reconcileTestView{}, err
	}
	memberships, replacements := reconcileTestPublicationValues(parts)
	manifestDigest, err := DigestIndexManifest(memberships)
	if err != nil {
		return reconcileTestView{}, err
	}
	edgesDigest, err := DigestIndexEdges(replacements)
	if err != nil {
		return reconcileTestView{}, err
	}
	if manifest.ScanOutcome != IndexScanComplete || !manifest.CensusComplete ||
		manifest.PartCount != uint32(len(parts)) || manifest.PartsDigest != partsDigest ||
		manifest.EntryCount != uint64(len(memberships)) || manifest.ManifestDigest != manifestDigest ||
		manifest.EdgeCount != reconcileTestEdgeCount(replacements) || manifest.EdgesDigest != edgesDigest {
		return reconcileTestView{}, errReconcileTestRejected
	}

	current, hasCurrent := store.current[build.ref.Scope.CheckoutID]
	generation := int64(1)
	if hasCurrent {
		generation = current.published.Context.Generation + 1
	}
	contextRef := ContextRef{
		SourceID:          build.ref.Scope.SourceID,
		CheckoutID:        build.ref.Scope.CheckoutID,
		ViewID:            reconcileTestViewID(build.ref.Scope.CheckoutID, generation),
		AnalysisProfileID: build.input.ProfileID,
		Generation:        generation,
	}
	view := reconcileTestView{
		published: IndexPublishedView{
			BuildID:        build.ref.BuildID,
			Context:        contextRef,
			ManifestDigest: manifest.ManifestDigest,
			AcceptedFSSeq:  manifest.Observation.ObservedFSSeq,
			PublishedAt:    time.Unix(generation, 0).UTC(),
		},
		memberships: make(map[string]IndexMembership),
		coverage:    manifest.Coverage.Structural,
	}
	if view.coverage == "" {
		view.coverage = IndexCoverageComplete
	}
	candidatesByPath := make(map[string]QueryCandidate)
	stagedArtifacts := reconcileTestStagedArtifactIDs(parts)
	for _, membership := range memberships {
		if membership.State != IndexFilePresent {
			continue
		}
		if membership.ArtifactID == nil {
			return reconcileTestView{}, errReconcileTestRejected
		}
		if _, found := stagedArtifacts[*membership.ArtifactID]; !found {
			return reconcileTestView{}, errReconcileTestRejected
		}
		artifact, found := store.artifacts[*membership.ArtifactID]
		if !found {
			return reconcileTestView{}, errReconcileTestRejected
		}
		candidate := artifact.candidate(contextRef, membership.DisplayPath)
		view.memberships[membership.PathKey] = membership
		view.candidates = append(view.candidates, candidate)
		candidatesByPath[membership.PathKey] = candidate
	}
	sort.Slice(view.candidates, func(left, right int) bool {
		return view.candidates[left].RelativePath < view.candidates[right].RelativePath
	})

	replacementByPath := make(map[string]IndexEdgeReplacement, len(replacements))
	for _, replacement := range replacements {
		if _, duplicate := replacementByPath[replacement.SourcePath]; duplicate {
			return reconcileTestView{}, errReconcileTestRejected
		}
		replacementByPath[replacement.SourcePath] = replacement
	}
	for path := range candidatesByPath {
		if _, found := replacementByPath[path]; !found {
			return reconcileTestView{}, errReconcileTestRejected
		}
	}
	for sourcePath, replacement := range replacementByPath {
		source, found := candidatesByPath[sourcePath]
		if !found {
			if len(replacement.Edges) == 0 {
				continue
			}
			return reconcileTestView{}, errReconcileTestRejected
		}
		for _, edge := range replacement.Edges {
			if err := validateIndexEdge(edge); err != nil || edge.SourceArtifactID != source.Proof.ArtifactID {
				return reconcileTestView{}, errReconcileTestRejected
			}
			if edge.ResolutionState == IndexResolutionState("resolved") {
				target, found := candidatesByPath[edge.Target.PathKey]
				if !found || target.Proof.ArtifactID != edge.Target.ArtifactID {
					return reconcileTestView{}, errReconcileTestRejected
				}
				view.edges = append(view.edges, QueryGraphEdge{
					From:         reconcileTestEntityRef(source),
					To:           reconcileTestEntityRef(target),
					Relation:     edge.Relation,
					EvidenceKind: reconcileTestEvidenceKind(edge.EvidenceKind),
					EvidenceRefs: []QueryEntityRef{reconcileTestEntityRef(source)},
				})
				continue
			}
			view.unresolved = append(view.unresolved, GraphUnresolvedSite{
				From:         reconcileTestEntityRef(source),
				Relation:     edge.Relation,
				EvidenceRefs: []QueryEntityRef{reconcileTestEntityRef(source)},
			})
		}
	}
	return view, nil
}

func (store *reconcileTestStore) currentHas(checkoutID string) bool {
	_, found := store.current[checkoutID]
	return found
}

func (store *reconcileTestStore) currentForContextLocked(ref ContextRef) (reconcileTestView, bool) {
	view, found := store.current[ref.CheckoutID]
	if !found || !reconcileTestContextsEqual(view.published.Context, ref) {
		return reconcileTestView{}, false
	}
	return view, true
}

func (store *reconcileTestStore) currentContext(checkoutID string) (ContextRef, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	view, found := store.current[checkoutID]
	if !found {
		return ContextRef{}, false
	}
	return view.published.Context.clone(), true
}

func (store *reconcileTestStore) viewCount(checkoutID string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.history[checkoutID])
}

func (store *reconcileTestStore) operationCounts() reconcileTestStoreOperations {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.attempts
}

func reconcileTestStagedParts(build *reconcileTestBuild) ([]IndexPart, []IndexPartAck, error) {
	parts := make([]IndexPart, 0, len(build.staged))
	acks := make([]IndexPartAck, 0, len(build.staged))
	for sequence := range len(build.staged) {
		staged, found := build.staged[uint32(sequence)]
		if !found {
			return nil, nil, errReconcileTestRejected
		}
		parts = append(parts, staged.part)
		acks = append(acks, IndexPartAck{BuildID: build.ref.BuildID, Sequence: uint32(sequence), Digest: staged.digest})
	}
	return parts, acks, nil
}

func reconcileTestPublicationValues(parts []IndexPart) ([]IndexMembership, []IndexEdgeReplacement) {
	var memberships []IndexMembership
	var replacements []IndexEdgeReplacement
	for _, part := range parts {
		memberships = append(memberships, part.Memberships...)
		replacements = append(replacements, part.EdgeReplacements...)
	}
	return memberships, replacements
}

func reconcileTestStagedArtifactIDs(parts []IndexPart) map[string]struct{} {
	artifacts := make(map[string]struct{})
	for _, part := range parts {
		for _, artifact := range part.Artifacts {
			artifacts[artifact.ArtifactID] = struct{}{}
		}
	}
	return artifacts
}

func reconcileTestEdgeCount(replacements []IndexEdgeReplacement) uint64 {
	var count uint64
	for _, replacement := range replacements {
		count += uint64(len(replacement.Edges))
	}
	return count
}

func reconcileTestCurrentMatchesParent(current reconcileTestView, hasCurrent bool, parent *ContextRef) bool {
	if !hasCurrent {
		return parent == nil
	}
	return parent != nil && reconcileTestContextsEqual(current.published.Context, *parent)
}

func reconcileTestBeginInputsEqual(left, right IndexBeginInput) bool {
	return left.BuildKey == right.BuildKey && left.Scope == right.Scope && left.ProfileID == right.ProfileID &&
		left.Mode == right.Mode && left.JobKind == right.JobKind && reconcileTestContextsEqualOrNil(left.ExpectedParent, right.ExpectedParent)
}

func reconcileTestContextsEqualOrNil(left, right *ContextRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return reconcileTestContextsEqual(*left, *right)
}

func reconcileTestContextsEqual(left, right ContextRef) bool {
	if left.SourceID != right.SourceID || left.CheckoutID != right.CheckoutID || left.ViewID != right.ViewID ||
		left.AnalysisProfileID != right.AnalysisProfileID || left.Generation != right.Generation {
		return false
	}
	if left.SpaceID == nil || right.SpaceID == nil {
		return left.SpaceID == nil && right.SpaceID == nil
	}
	return *left.SpaceID == *right.SpaceID
}

func reconcileTestPublishedEqual(left, right IndexPublishedView) bool {
	return left.BuildID == right.BuildID && reconcileTestContextsEqual(left.Context, right.Context) &&
		left.ManifestDigest == right.ManifestDigest && left.AcceptedFSSeq == right.AcceptedFSSeq && left.PublishedAt.Equal(right.PublishedAt)
}

func reconcileTestViewID(checkoutID string, generation int64) string {
	prefix := "76000000"
	if strings.HasSuffix(checkoutID, "2") {
		prefix = "77000000"
	}
	return fmt.Sprintf("%s-0000-4000-8000-%012d", prefix, generation)
}

func reconcileTestQueryMatches(candidate QueryCandidate, spec QuerySpec) bool {
	if len(spec.Filter.Languages) != 0 {
		matchedLanguage := false
		for _, language := range spec.Filter.Languages {
			if candidate.Language == language {
				matchedLanguage = true
				break
			}
		}
		if !matchedLanguage {
			return false
		}
	}
	switch spec.Mode {
	case QueryModeExactRelativePath:
		return candidate.RelativePath == spec.Text
	case QueryModeExactLocalName:
		return candidate.LocalName == spec.Text
	case QueryModeExactQualifiedSymbol:
		return candidate.QualifiedSymbol == spec.Text
	case QueryModeFTS:
		return strings.Contains(strings.ToLower(candidate.Text), strings.ToLower(spec.Text))
	default:
		return false
	}
}

func reconcileTestGraphEdgeMatches(edge QueryGraphEdge, query GraphEdgeQuery) bool {
	if !reconcileTestGraphEdgeTouches(edge, query.Nodes, query.Filter.Direction) {
		return false
	}
	if len(query.Filter.Relations) != 0 && !reconcileTestContainsRelation(query.Filter.Relations, edge.Relation) {
		return false
	}
	return len(query.Filter.EvidenceKinds) == 0 || reconcileTestContainsEvidenceKind(query.Filter.EvidenceKinds, edge.EvidenceKind)
}

func reconcileTestGraphEdgeTouches(edge QueryGraphEdge, nodes []QueryEntityRef, direction GraphDirection) bool {
	for _, node := range nodes {
		switch direction {
		case GraphDirectionIncoming:
			if reconcileTestEntityRefsEqual(edge.To, node) {
				return true
			}
		case GraphDirectionOutgoing:
			if reconcileTestEntityRefsEqual(edge.From, node) {
				return true
			}
		case GraphDirectionBoth:
			if reconcileTestEntityRefsEqual(edge.From, node) || reconcileTestEntityRefsEqual(edge.To, node) {
				return true
			}
		}
	}
	return false
}

func reconcileTestUnresolvedMatches(site GraphUnresolvedSite, query GraphEdgeQuery) bool {
	if query.Filter.Direction == GraphDirectionIncoming {
		return false
	}
	if !reconcileTestContainsRef(query.Nodes, site.From) {
		return false
	}
	return len(query.Filter.Relations) == 0 || reconcileTestContainsRelation(query.Filter.Relations, site.Relation)
}

func reconcileTestContainsRelation(relations []IndexRelation, want IndexRelation) bool {
	for _, relation := range relations {
		if relation == want {
			return true
		}
	}
	return false
}

func reconcileTestContainsEvidenceKind(kinds []QueryEvidenceKind, want QueryEvidenceKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func reconcileTestContainsRef(refs []QueryEntityRef, want QueryEntityRef) bool {
	for _, ref := range refs {
		if reconcileTestEntityRefsEqual(ref, want) {
			return true
		}
	}
	return false
}

func reconcileTestEntityRefsEqual(left, right QueryEntityRef) bool {
	return left.SourceID == right.SourceID && left.ViewID == right.ViewID && left.EntityKey == right.EntityKey
}

func reconcileTestEntityRef(candidate QueryCandidate) QueryEntityRef {
	return QueryEntityRef{SourceID: candidate.Context.SourceID, ViewID: candidate.Context.ViewID, EntityKey: candidate.EntityKey}
}

func reconcileTestEvidenceKind(kind IndexEvidenceKind) QueryEvidenceKind {
	switch kind {
	case IndexEvidenceKind("extracted"):
		return QueryEvidenceExtracted
	case IndexEvidenceKind("heuristic"):
		return QueryEvidenceHeuristic
	case IndexEvidenceKind("semantic"):
		return QueryEvidenceSemantic
	default:
		return QueryEvidenceResolved
	}
}

func reconcileTestRequireRefBound(t *testing.T, ref QueryEntityRef, want ContextRef, kind string) {
	t.Helper()
	if ref.SourceID != want.SourceID || ref.ViewID != want.ViewID {
		t.Fatalf("%s %#v is not bound to current %q/%q", kind, ref, want.SourceID, want.ViewID)
	}
}

func reconcileTestEntityKeys(refs []QueryEntityRef) []string {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, ref.EntityKey)
	}
	return keys
}

func reconcileTestStringsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
