package uci

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These tests keep the graph boundary entirely in-memory. The store exposes
// target resolution and bounded adjacency batches; GraphService owns traversal,
// View validation, result ordering, budgets, and opaque continuation binding.
func TestUCIGraphExplainAndNeighborsAreViewPinnedAndEvidenceLabeled(t *testing.T) {
	fixture := newGraphTestFixture()

	t.Run("explain returns only current-view static evidence", func(t *testing.T) {
		store := fixture.store()
		store.injectedEdges = []QueryGraphEdge{
			graphTestEdge(
				fixture.gateway,
				fixture.oldCallee,
				"calls",
				QueryEvidenceResolved,
				fixture.gateway,
			),
		}
		service := NewGraphService(store)

		result, err := service.Explore(context.Background(), newAuthorizedContext(fixture.contextA), GraphSpec{
			ClientSessionID: "graph-client-a",
			Action:          GraphActionExplain,
			Target:          GraphTarget{EntityKey: fixture.gateway.EntityKey},
			Filter: GraphFilter{
				Direction:     GraphDirectionBoth,
				Relations:     []IndexRelation{IndexRelation("calls"), IndexRelation("may_call")},
				EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved, QueryEvidenceHeuristic},
			},
			Budget: graphTestBudget(),
		})
		if err != nil {
			t.Fatalf("Explore(explain) error = %v", err)
		}

		if got := result.Outcome; got != GraphOutcomeComplete {
			t.Fatalf("explain outcome = %q, want %q", got, GraphOutcomeComplete)
		}
		if got := result.Semantics; got != GraphSemanticsStatic {
			t.Fatalf("explain semantics = %q, want static graph semantics", got)
		}
		if got := result.Coverage; got != IndexCoverageComplete {
			t.Fatalf("explain coverage = %q, want %q", got, IndexCoverageComplete)
		}
		graphTestAssertGraphBoundTo(t, result, fixture.contextA)
		graphTestAssertNodeKeys(t, result.Graph.Nodes, []string{
			fixture.dynamic.EntityKey,
			fixture.entry.EntityKey,
			fixture.gateway.EntityKey,
			fixture.worker.EntityKey,
		})
		graphTestAssertEdgeKeys(t, result.Graph.Edges, []string{
			graphTestEdgeKey(fixture.entry, fixture.gateway, "calls", QueryEvidenceResolved),
			graphTestEdgeKey(fixture.gateway, fixture.dynamic, "may_call", QueryEvidenceHeuristic),
			graphTestEdgeKey(fixture.gateway, fixture.worker, "calls", QueryEvidenceResolved),
		})
		graphTestAssertEdge(t, result.Graph.Edges, fixture.entry, fixture.gateway, "calls", QueryEvidenceResolved, fixture.entry)
		graphTestAssertEdge(t, result.Graph.Edges, fixture.gateway, fixture.worker, "calls", QueryEvidenceResolved, fixture.gateway)
		graphTestAssertEdge(t, result.Graph.Edges, fixture.gateway, fixture.dynamic, "may_call", QueryEvidenceHeuristic, fixture.gateway)
		graphTestAssertStoreBoundTo(t, store, fixture.contextA)
	})

	for _, tc := range []struct {
		name   string
		filter GraphFilter
		want   []string
	}{
		{
			name: "incoming resolved calls",
			filter: GraphFilter{
				Direction:     GraphDirectionIncoming,
				Relations:     []IndexRelation{IndexRelation("calls")},
				EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
			},
			want: []string{
				graphTestEdgeKey(fixture.entry, fixture.gateway, "calls", QueryEvidenceResolved),
			},
		},
		{
			name: "outgoing resolved calls",
			filter: GraphFilter{
				Direction:     GraphDirectionOutgoing,
				Relations:     []IndexRelation{IndexRelation("calls")},
				EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
			},
			want: []string{
				graphTestEdgeKey(fixture.gateway, fixture.worker, "calls", QueryEvidenceResolved),
			},
		},
		{
			name: "outgoing heuristic may call",
			filter: GraphFilter{
				Direction:     GraphDirectionOutgoing,
				Relations:     []IndexRelation{IndexRelation("may_call")},
				EvidenceKinds: []QueryEvidenceKind{QueryEvidenceHeuristic},
			},
			want: []string{
				graphTestEdgeKey(fixture.gateway, fixture.dynamic, "may_call", QueryEvidenceHeuristic),
			},
		},
	} {
		t.Run("neighbors "+tc.name, func(t *testing.T) {
			store := fixture.store()
			service := NewGraphService(store)
			spec := GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionNeighbors,
				Target:          GraphTarget{EntityKey: fixture.gateway.EntityKey},
				Filter:          tc.filter,
				Budget:          graphTestBudget(),
			}

			first, err := service.Explore(context.Background(), newAuthorizedContext(fixture.contextA), spec)
			if err != nil {
				t.Fatalf("first Explore(neighbors) error = %v", err)
			}
			retry, err := service.Explore(context.Background(), newAuthorizedContext(fixture.contextA), spec)
			if err != nil {
				t.Fatalf("retry Explore(neighbors) error = %v", err)
			}

			if got := first.Outcome; got != GraphOutcomeComplete {
				t.Fatalf("neighbors outcome = %q, want %q", got, GraphOutcomeComplete)
			}
			if got := first.Semantics; got != GraphSemanticsStatic {
				t.Fatalf("neighbors semantics = %q, want static graph semantics", got)
			}
			if !reflect.DeepEqual(first.Graph, retry.Graph) {
				t.Fatalf("neighbors graph is not deterministic:\nfirst: %#v\nretry: %#v", first.Graph, retry.Graph)
			}
			graphTestAssertGraphBoundTo(t, first, fixture.contextA)
			graphTestAssertEdgeKeys(t, first.Graph.Edges, tc.want)
			graphTestAssertEdgeQueries(t, store, fixture.contextA, tc.filter)
		})
	}
}

func TestUCIGraphPathImpactFlowAndCyclesRemainStaticAndViewPinned(t *testing.T) {
	fixture := newGraphTestFixture()

	for _, tc := range []struct {
		name string
		spec GraphSpec
		want []string
	}{
		{
			name: "path follows directed calls",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionPath,
				Target:          GraphTarget{EntityKey: fixture.entry.EntityKey},
				Destination:     &GraphTarget{EntityKey: fixture.callee.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionOutgoing,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: graphTestBudget(),
			},
			want: []string{
				graphTestEdgeKey(fixture.entry, fixture.gateway, "calls", QueryEvidenceResolved),
				graphTestEdgeKey(fixture.gateway, fixture.worker, "calls", QueryEvidenceResolved),
				graphTestEdgeKey(fixture.worker, fixture.callee, "calls", QueryEvidenceResolved),
			},
		},
		{
			name: "impact traverses static incoming calls",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionImpact,
				Target:          GraphTarget{EntityKey: fixture.callee.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionIncoming,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: graphTestBudget(),
			},
			want: []string{
				graphTestEdgeKey(fixture.entry, fixture.gateway, "calls", QueryEvidenceResolved),
				graphTestEdgeKey(fixture.gateway, fixture.worker, "calls", QueryEvidenceResolved),
				graphTestEdgeKey(fixture.worker, fixture.callee, "calls", QueryEvidenceResolved),
			},
		},
		{
			name: "flow traverses forward static calls",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionFlow,
				Target:          GraphTarget{EntityKey: fixture.entry.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionOutgoing,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: graphTestBudget(),
			},
			want: []string{
				graphTestEdgeKey(fixture.entry, fixture.gateway, "calls", QueryEvidenceResolved),
				graphTestEdgeKey(fixture.gateway, fixture.worker, "calls", QueryEvidenceResolved),
				graphTestEdgeKey(fixture.worker, fixture.callee, "calls", QueryEvidenceResolved),
			},
		},
		{
			name: "cycles preserve directed import SCC evidence",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionCycles,
				Target:          GraphTarget{EntityKey: fixture.cycleA.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionOutgoing,
					Relations:     []IndexRelation{IndexRelation("imports")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceExtracted},
				},
				Budget: graphTestBudget(),
			},
			want: []string{
				graphTestEdgeKey(fixture.cycleA, fixture.cycleB, "imports", QueryEvidenceExtracted),
				graphTestEdgeKey(fixture.cycleB, fixture.cycleC, "imports", QueryEvidenceExtracted),
				graphTestEdgeKey(fixture.cycleC, fixture.cycleA, "imports", QueryEvidenceExtracted),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := fixture.store()
			service := NewGraphService(store)

			result, err := service.Explore(context.Background(), newAuthorizedContext(fixture.contextA), tc.spec)
			if err != nil {
				t.Fatalf("Explore(%s) error = %v", tc.spec.Action, err)
			}
			if got := result.Outcome; got != GraphOutcomeComplete {
				t.Fatalf("%s outcome = %q, want %q", tc.spec.Action, got, GraphOutcomeComplete)
			}
			if got := result.Semantics; got != GraphSemanticsStatic {
				t.Fatalf("%s semantics = %q, want static graph semantics", tc.spec.Action, got)
			}
			graphTestAssertGraphBoundTo(t, result, fixture.contextA)
			graphTestAssertEdgeKeys(t, result.Graph.Edges, tc.want)
			graphTestAssertStoreBoundTo(t, store, fixture.contextA)
		})
	}
}

func TestUCIGraphReportsAmbiguityUnresolvedSitesAndChangedCalleeInvalidation(t *testing.T) {
	fixture := newGraphTestFixture()

	t.Run("ambiguous name returns sorted candidates without a fabricated target", func(t *testing.T) {
		store := fixture.store()
		store.injectedCandidates = []QueryEntityRef{graphTestRef(fixture.oldCallee)}
		service := NewGraphService(store)

		result, err := service.Explore(context.Background(), newAuthorizedContext(fixture.contextA), GraphSpec{
			ClientSessionID: "graph-client-a",
			Action:          GraphActionExplain,
			Target:          GraphTarget{Name: "Handle"},
			Filter:          GraphFilter{Direction: GraphDirectionBoth},
			Budget:          graphTestBudget(),
		})
		if err != nil {
			t.Fatalf("Explore(ambiguous) error = %v", err)
		}
		if got := result.Outcome; got != GraphOutcomeAmbiguous {
			t.Fatalf("ambiguous outcome = %q, want %q", got, GraphOutcomeAmbiguous)
		}
		if len(result.Graph.Nodes) != 0 || len(result.Graph.Edges) != 0 {
			t.Fatalf("ambiguous result fabricated graph %#v", result.Graph)
		}
		if got, want := graphTestRefKeys(result.Candidates), []string{fixture.handlerA.EntityKey, fixture.handlerB.EntityKey}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ambiguous candidates = %#v, want %#v", got, want)
		}
		graphTestAssertGraphBoundTo(t, result, fixture.contextA)
		if len(store.edgeCalls) != 0 {
			t.Fatalf("ambiguous resolution issued %d adjacency calls, want 0", len(store.edgeCalls))
		}
	})

	t.Run("unresolved target remains evidence labeled unknown", func(t *testing.T) {
		store := fixture.store()
		store.unresolvedTargets["missing-call"] = []GraphUnresolvedSite{
			graphTestUnresolved(fixture.worker, "may_call", fixture.worker),
		}
		service := NewGraphService(store)

		result, err := service.Explore(context.Background(), newAuthorizedContext(fixture.contextA), GraphSpec{
			ClientSessionID: "graph-client-a",
			Action:          GraphActionExplain,
			Target:          GraphTarget{Name: "missing-call"},
			Filter:          GraphFilter{Direction: GraphDirectionBoth},
			Budget:          graphTestBudget(),
		})
		if err != nil {
			t.Fatalf("Explore(unresolved target) error = %v", err)
		}
		if got := result.Outcome; got != GraphOutcomeUnknownOrTruncated {
			t.Fatalf("unresolved target outcome = %q, want %q", got, GraphOutcomeUnknownOrTruncated)
		}
		graphTestAssertGraphBoundTo(t, result, fixture.contextA)
		graphTestAssertUnresolved(t, result.Unresolved, fixture.worker, "may_call", fixture.worker)
	})

	t.Run("changed callee invalidates old target instead of retaining stale calls", func(t *testing.T) {
		store := fixture.store()
		store.edges = graphTestWithoutEdge(store.edges, fixture.worker, fixture.callee)
		store.injectedEdges = []QueryGraphEdge{
			graphTestEdge(fixture.worker, fixture.oldCallee, "calls", QueryEvidenceResolved, fixture.worker),
		}
		store.injectedUnresolved = []GraphUnresolvedSite{
			graphTestUnresolved(fixture.worker, "may_call", fixture.worker),
		}
		service := NewGraphService(store)

		result, err := service.Explore(context.Background(), newAuthorizedContext(fixture.contextA), GraphSpec{
			ClientSessionID: "graph-client-a",
			Action:          GraphActionFlow,
			Target:          GraphTarget{EntityKey: fixture.worker.EntityKey},
			Filter: GraphFilter{
				Direction:     GraphDirectionOutgoing,
				Relations:     []IndexRelation{IndexRelation("calls"), IndexRelation("may_call")},
				EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved, QueryEvidenceHeuristic},
			},
			Budget: graphTestBudget(),
		})
		if err != nil {
			t.Fatalf("Explore(changed callee) error = %v", err)
		}
		if got := result.Outcome; got != GraphOutcomeUnknownOrTruncated {
			t.Fatalf("changed callee outcome = %q, want %q", got, GraphOutcomeUnknownOrTruncated)
		}
		if graphTestHasEndpoint(result.Graph.Edges, fixture.oldCallee.EntityKey) {
			t.Fatalf("changed callee result retained stale target %q in %#v", fixture.oldCallee.EntityKey, result.Graph.Edges)
		}
		graphTestAssertGraphBoundTo(t, result, fixture.contextA)
		graphTestAssertUnresolved(t, result.Unresolved, fixture.worker, "may_call", fixture.worker)
	})
}

func TestUCIGraphDistinguishesConclusiveAbsenceFromUnknownOrTruncated(t *testing.T) {
	fixture := newGraphTestFixture()

	for _, tc := range []struct {
		name        string
		prepare     func(*graphTestStore)
		spec        GraphSpec
		wantOutcome GraphOutcome
		wantStop    QueryGraphStopReason
		checkBounds bool
	}{
		{
			name: "complete graph permits no path",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionPath,
				Target:          GraphTarget{EntityKey: fixture.entry.EntityKey},
				Destination:     &GraphTarget{EntityKey: fixture.isolated.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionOutgoing,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: graphTestBudget(),
			},
			wantOutcome: GraphOutcomeNoPath,
			wantStop:    QueryGraphComplete,
		},
		{
			name: "complete graph permits no impact",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionImpact,
				Target:          GraphTarget{EntityKey: fixture.isolated.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionIncoming,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: graphTestBudget(),
			},
			wantOutcome: GraphOutcomeNoImpact,
			wantStop:    QueryGraphComplete,
		},
		{
			name: "partial graph cannot prove no path",
			prepare: func(store *graphTestStore) {
				store.coverage = IndexCoveragePartial
			},
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionPath,
				Target:          GraphTarget{EntityKey: fixture.entry.EntityKey},
				Destination:     &GraphTarget{EntityKey: fixture.isolated.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionOutgoing,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: graphTestBudget(),
			},
			wantOutcome: GraphOutcomeUnknownOrTruncated,
			wantStop:    QueryGraphCoverageGap,
		},
		{
			name: "partial graph cannot prove no impact",
			prepare: func(store *graphTestStore) {
				store.coverage = IndexCoveragePartial
			},
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionImpact,
				Target:          GraphTarget{EntityKey: fixture.isolated.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionIncoming,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: graphTestBudget(),
			},
			wantOutcome: GraphOutcomeUnknownOrTruncated,
			wantStop:    QueryGraphCoverageGap,
		},
		{
			name: "depth cap cannot prove no path",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionPath,
				Target:          GraphTarget{EntityKey: fixture.entry.EntityKey},
				Destination:     &GraphTarget{EntityKey: fixture.callee.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionOutgoing,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: GraphBudget{
					MaxDepth:   1,
					MaxVisited: 64,
					MaxNodes:   32,
					MaxEdges:   64,
				},
			},
			wantOutcome: GraphOutcomeUnknownOrTruncated,
			wantStop:    QueryGraphDepthCap,
		},
		{
			name: "visited cap cannot prove no impact",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionImpact,
				Target:          GraphTarget{EntityKey: fixture.callee.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionIncoming,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: GraphBudget{
					MaxDepth:   4,
					MaxVisited: 1,
					MaxNodes:   32,
					MaxEdges:   64,
				},
			},
			wantOutcome: GraphOutcomeUnknownOrTruncated,
			wantStop:    QueryGraphNodeCap,
		},
		{
			name: "response node and edge caps are explicit truncation",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionFlow,
				Target:          GraphTarget{EntityKey: fixture.entry.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionOutgoing,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: GraphBudget{
					MaxDepth:   4,
					MaxVisited: 64,
					MaxNodes:   2,
					MaxEdges:   1,
				},
			},
			wantOutcome: GraphOutcomeUnknownOrTruncated,
			wantStop:    QueryGraphNodeCap,
			checkBounds: true,
		},
		{
			name: "expired deadline is bounded unknown rather than false absence",
			spec: GraphSpec{
				ClientSessionID: "graph-client-a",
				Action:          GraphActionPath,
				Target:          GraphTarget{EntityKey: fixture.entry.EntityKey},
				Destination:     &GraphTarget{EntityKey: fixture.isolated.EntityKey},
				Filter: GraphFilter{
					Direction:     GraphDirectionOutgoing,
					Relations:     []IndexRelation{IndexRelation("calls")},
					EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
				},
				Budget: GraphBudget{
					MaxDepth:   4,
					MaxVisited: 64,
					MaxNodes:   32,
					MaxEdges:   64,
					Deadline:   time.Unix(1, 0),
				},
			},
			wantOutcome: GraphOutcomeUnknownOrTruncated,
			wantStop:    QueryGraphDeadline,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := fixture.store()
			if tc.prepare != nil {
				tc.prepare(store)
			}
			service := NewGraphService(store)

			result, err := service.Explore(context.Background(), newAuthorizedContext(fixture.contextA), tc.spec)
			if err != nil {
				t.Fatalf("Explore(%s) error = %v", tc.spec.Action, err)
			}
			if got := result.Outcome; got != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q", got, tc.wantOutcome)
			}
			if got := result.Graph.StopReason; got != tc.wantStop {
				t.Fatalf("stop reason = %q, want %q", got, tc.wantStop)
			}
			if tc.checkBounds {
				if got := len(result.Graph.Nodes); got > tc.spec.Budget.MaxNodes {
					t.Fatalf("result nodes = %d, exceeds budget %d", got, tc.spec.Budget.MaxNodes)
				}
				if got := len(result.Graph.Edges); got > tc.spec.Budget.MaxEdges {
					t.Fatalf("result edges = %d, exceeds budget %d", got, tc.spec.Budget.MaxEdges)
				}
			}
			graphTestAssertGraphBoundTo(t, result, fixture.contextA)
		})
	}
}

func TestUCIGraphContinuationBindsClientViewActionTargetFiltersAndBudget(t *testing.T) {
	fixture := newGraphTestFixture()
	store := fixture.store()
	service := NewGraphService(store)
	authorizedA := newAuthorizedContext(fixture.contextA)

	firstSpec := GraphSpec{
		ClientSessionID: "graph-client-a",
		Action:          GraphActionFlow,
		Target:          GraphTarget{EntityKey: fixture.entry.EntityKey},
		Filter: GraphFilter{
			Direction:     GraphDirectionOutgoing,
			Relations:     []IndexRelation{IndexRelation("calls")},
			EvidenceKinds: []QueryEvidenceKind{QueryEvidenceResolved},
		},
		Budget: GraphBudget{
			MaxDepth:   4,
			MaxVisited: 64,
			MaxNodes:   1,
			MaxEdges:   1,
		},
	}
	first, err := service.Explore(context.Background(), authorizedA, firstSpec)
	if err != nil {
		t.Fatalf("first Explore() error = %v", err)
	}
	if first.Continuation == nil || *first.Continuation == "" {
		t.Fatalf("first graph continuation = %#v, want opaque next-page token", first.Continuation)
	}
	if got := first.Outcome; got != GraphOutcomeUnknownOrTruncated {
		t.Fatalf("first outcome = %q, want %q", got, GraphOutcomeUnknownOrTruncated)
	}

	nextSpec := firstSpec
	nextSpec.Continuation = first.Continuation
	second, err := service.Explore(context.Background(), authorizedA, nextSpec)
	if err != nil {
		t.Fatalf("continued Explore() error = %v", err)
	}
	retry, err := service.Explore(context.Background(), authorizedA, nextSpec)
	if err != nil {
		t.Fatalf("retried continuation error = %v", err)
	}
	if !reflect.DeepEqual(second.Graph, retry.Graph) {
		t.Fatalf("continued graph is not deterministic:\nfirst: %#v\nretry: %#v", second.Graph, retry.Graph)
	}

	callCount := len(store.targetCalls) + len(store.edgeCalls)
	for _, tc := range []struct {
		name       string
		authorized AuthorizedContext
		spec       GraphSpec
	}{
		{
			name:       "client",
			authorized: authorizedA,
			spec: func() GraphSpec {
				changed := nextSpec
				changed.ClientSessionID = "graph-client-b"
				return changed
			}(),
		},
		{
			name:       "view",
			authorized: newAuthorizedContext(fixture.contextB),
			spec:       nextSpec,
		},
		{
			name:       "action",
			authorized: authorizedA,
			spec: func() GraphSpec {
				changed := nextSpec
				changed.Action = GraphActionImpact
				return changed
			}(),
		},
		{
			name:       "target",
			authorized: authorizedA,
			spec: func() GraphSpec {
				changed := nextSpec
				changed.Target = GraphTarget{EntityKey: fixture.gateway.EntityKey}
				return changed
			}(),
		},
		{
			name:       "direction",
			authorized: authorizedA,
			spec: func() GraphSpec {
				changed := nextSpec
				changed.Filter.Direction = GraphDirectionIncoming
				return changed
			}(),
		},
		{
			name:       "relation",
			authorized: authorizedA,
			spec: func() GraphSpec {
				changed := nextSpec
				changed.Filter.Relations = []IndexRelation{IndexRelation("may_call")}
				return changed
			}(),
		},
		{
			name:       "evidence kind",
			authorized: authorizedA,
			spec: func() GraphSpec {
				changed := nextSpec
				changed.Filter.EvidenceKinds = []QueryEvidenceKind{QueryEvidenceHeuristic}
				return changed
			}(),
		},
		{
			name:       "budget",
			authorized: authorizedA,
			spec: func() GraphSpec {
				changed := nextSpec
				changed.Budget.MaxDepth = 3
				return changed
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.Explore(context.Background(), tc.authorized, tc.spec); err == nil {
				t.Fatal("Explore() error = nil, want continuation binding rejection")
			}
		})
	}
	if got := len(store.targetCalls) + len(store.edgeCalls); got != callCount {
		t.Fatalf("store calls after rejected continuations = %d, want %d", got, callCount)
	}
}

type graphTestTargetCall struct {
	Context AuthorizedContext
	Target  GraphTarget
}

type graphTestEdgeCall struct {
	Context AuthorizedContext
	Query   GraphEdgeQuery
}

type graphTestStore struct {
	entities           []QueryCandidate
	edges              []QueryGraphEdge
	coverage           IndexCoverageState
	targetCoverage     IndexCoverageState
	unresolvedTargets  map[string][]GraphUnresolvedSite
	unresolved         []GraphUnresolvedSite
	injectedCandidates []QueryEntityRef
	injectedEdges      []QueryGraphEdge
	injectedUnresolved []GraphUnresolvedSite
	targetCalls        []graphTestTargetCall
	edgeCalls          []graphTestEdgeCall
}

var _ GraphStore = (*graphTestStore)(nil)

func (store *graphTestStore) ResolveGraphTargets(_ context.Context, authorized AuthorizedContext, target GraphTarget) (GraphTargetResolution, error) {
	store.targetCalls = append(store.targetCalls, graphTestTargetCall{Context: authorized, Target: target})

	result := GraphTargetResolution{Coverage: graphTestEffectiveCoverage(store.targetCoverage, store.coverage)}
	selected := authorized.Ref()
	for _, candidate := range store.entities {
		if !graphTestContextRefsEqual(candidate.Context, selected) || !graphTestTargetMatches(candidate, target) {
			continue
		}
		result.Candidates = append(result.Candidates, graphTestRef(candidate))
	}
	result.Candidates = append(result.Candidates, store.injectedCandidates...)
	result.Unresolved = append(result.Unresolved, store.unresolvedTargets[graphTestTargetKey(target)]...)
	return result, nil
}

func (store *graphTestStore) SelectGraphEdges(_ context.Context, authorized AuthorizedContext, query GraphEdgeQuery) (GraphStoreResult, error) {
	store.edgeCalls = append(store.edgeCalls, graphTestEdgeCall{Context: authorized, Query: query})

	result := GraphStoreResult{Coverage: graphTestEffectiveCoverage("", store.coverage)}
	for _, edge := range store.edges {
		if graphTestEdgeMatches(edge, query) {
			result.Edges = append(result.Edges, edge)
		}
	}
	for _, unresolved := range store.unresolved {
		if graphTestUnresolvedMatches(unresolved, query) {
			result.Unresolved = append(result.Unresolved, unresolved)
		}
	}
	result.Edges = append(result.Edges, store.injectedEdges...)
	result.Unresolved = append(result.Unresolved, store.injectedUnresolved...)
	return result, nil
}

type graphTestFixture struct {
	contextA ContextRef
	contextB ContextRef

	entry     QueryCandidate
	gateway   QueryCandidate
	worker    QueryCandidate
	callee    QueryCandidate
	dynamic   QueryCandidate
	isolated  QueryCandidate
	handlerA  QueryCandidate
	handlerB  QueryCandidate
	cycleA    QueryCandidate
	cycleB    QueryCandidate
	cycleC    QueryCandidate
	oldCallee QueryCandidate
	entities  []QueryCandidate
	edges     []QueryGraphEdge
}

func newGraphTestFixture() graphTestFixture {
	contextA := graphTestContextRef(
		"21000000-0000-4000-8000-000000000033",
		"31000000-0000-4000-8000-000000000033",
		"41000000-0000-4000-8000-000000000033",
		"51000000-0000-4000-8000-000000000033",
		33,
	)
	contextB := contextA
	contextB.CheckoutID = "31000000-0000-4000-8000-000000000034"
	contextB.ViewID = "41000000-0000-4000-8000-000000000034"
	contextB.Generation = 34

	fixture := graphTestFixture{
		contextA: contextA,
		contextB: contextB,
		entry: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000031",
			"a",
			"symbol:entry",
			"Entry",
			"flow.Entry",
			"cmd/entry.go",
		),
		gateway: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000032",
			"b",
			"symbol:gateway",
			"Gateway",
			"flow.Gateway",
			"internal/gateway.go",
		),
		worker: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000033",
			"c",
			"symbol:worker",
			"Worker",
			"flow.Worker",
			"internal/worker.go",
		),
		callee: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000034",
			"d",
			"symbol:callee",
			"Callee",
			"flow.Callee",
			"internal/callee.go",
		),
		dynamic: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000035",
			"e",
			"symbol:dynamic",
			"Dynamic",
			"flow.Dynamic",
			"internal/dynamic.go",
		),
		isolated: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000036",
			"f",
			"symbol:isolated",
			"Isolated",
			"flow.Isolated",
			"internal/isolated.go",
		),
		handlerA: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000037",
			"1",
			"symbol:handle:a",
			"Handle",
			"http.Handle",
			"http/handle.go",
		),
		handlerB: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000038",
			"2",
			"symbol:handle:b",
			"Handle",
			"queue.Handle",
			"queue/handle.go",
		),
		cycleA: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000039",
			"3",
			"symbol:cycle:a",
			"CycleA",
			"cycle.A",
			"cycle/a.go",
		),
		cycleB: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000040",
			"4",
			"symbol:cycle:b",
			"CycleB",
			"cycle.B",
			"cycle/b.go",
		),
		cycleC: graphTestCandidate(
			contextA,
			"71000000-0000-4000-8000-000000000041",
			"5",
			"symbol:cycle:c",
			"CycleC",
			"cycle.C",
			"cycle/c.go",
		),
		oldCallee: graphTestCandidate(
			contextB,
			"71000000-0000-4000-8000-000000000042",
			"6",
			"symbol:old-callee",
			"Callee",
			"flow.Callee",
			"internal/callee.go",
		),
	}
	// Deliberately non-canonical input order makes result ordering observable.
	fixture.entities = []QueryCandidate{
		fixture.handlerB,
		fixture.gateway,
		fixture.cycleC,
		fixture.entry,
		fixture.callee,
		fixture.handlerA,
		fixture.dynamic,
		fixture.worker,
		fixture.cycleA,
		fixture.isolated,
		fixture.cycleB,
	}
	fixture.edges = []QueryGraphEdge{
		graphTestEdge(fixture.worker, fixture.callee, "calls", QueryEvidenceResolved, fixture.worker),
		graphTestEdge(fixture.cycleB, fixture.cycleC, "imports", QueryEvidenceExtracted, fixture.cycleB),
		graphTestEdge(fixture.gateway, fixture.dynamic, "may_call", QueryEvidenceHeuristic, fixture.gateway),
		graphTestEdge(fixture.entry, fixture.gateway, "calls", QueryEvidenceResolved, fixture.entry),
		graphTestEdge(fixture.cycleC, fixture.cycleA, "imports", QueryEvidenceExtracted, fixture.cycleC),
		graphTestEdge(fixture.gateway, fixture.worker, "calls", QueryEvidenceResolved, fixture.gateway),
		graphTestEdge(fixture.cycleA, fixture.cycleB, "imports", QueryEvidenceExtracted, fixture.cycleA),
	}
	return fixture
}

func (fixture graphTestFixture) store() *graphTestStore {
	return &graphTestStore{
		entities:          append([]QueryCandidate(nil), fixture.entities...),
		edges:             append([]QueryGraphEdge(nil), fixture.edges...),
		unresolvedTargets: make(map[string][]GraphUnresolvedSite),
	}
}

func graphTestContextRef(sourceID, checkoutID, viewID, profileID string, generation int64) ContextRef {
	spaceID := "11000000-0000-4000-8000-000000000033"
	return ContextRef{
		SpaceID:           &spaceID,
		SourceID:          sourceID,
		CheckoutID:        checkoutID,
		ViewID:            viewID,
		AnalysisProfileID: profileID,
		Generation:        generation,
	}
}

func graphTestCandidate(contextRef ContextRef, artifactID, digestCharacter, entityKey, localName, qualifiedSymbol, relativePath string) QueryCandidate {
	text := "func " + localName + "() {}"
	return QueryCandidate{
		Context: contextRef,
		Proof: IndexArtifactProof{
			ArtifactID:         artifactID,
			ContentDigest:      IndexDigest(strings.Repeat(digestCharacter, 64)),
			FactsDigest:        IndexDigest(strings.Repeat("f", 64)),
			DefinitionCount:    1,
			ReferenceSiteCount: 0,
			ChunkCount:         1,
		},
		EntityKey:       entityKey,
		LocalName:       localName,
		QualifiedSymbol: qualifiedSymbol,
		RelativePath:    relativePath,
		Span: IndexSpan{
			ByteStart: 0,
			ByteEnd:   int64(len(text)),
			LineStart: 1,
			LineEnd:   1,
		},
		Text:     text,
		Kind:     QueryItemCode,
		Language: "go",
		Score:    1,
	}
}

func graphTestRef(candidate QueryCandidate) QueryEntityRef {
	return QueryEntityRef{
		SourceID:  candidate.Context.SourceID,
		ViewID:    candidate.Context.ViewID,
		EntityKey: candidate.EntityKey,
	}
}

func graphTestEdge(from, to QueryCandidate, relation string, evidenceKind QueryEvidenceKind, citation QueryCandidate) QueryGraphEdge {
	return QueryGraphEdge{
		From:         graphTestRef(from),
		To:           graphTestRef(to),
		Relation:     IndexRelation(relation),
		EvidenceKind: evidenceKind,
		EvidenceRefs: []QueryEntityRef{graphTestRef(citation)},
	}
}

func graphTestUnresolved(from QueryCandidate, relation string, citation QueryCandidate) GraphUnresolvedSite {
	return GraphUnresolvedSite{
		From:         graphTestRef(from),
		Relation:     IndexRelation(relation),
		EvidenceRefs: []QueryEntityRef{graphTestRef(citation)},
	}
}

func graphTestBudget() GraphBudget {
	return GraphBudget{
		MaxDepth:   4,
		MaxVisited: 64,
		MaxNodes:   32,
		MaxEdges:   64,
	}
}

func graphTestEffectiveCoverage(preferred, fallback IndexCoverageState) IndexCoverageState {
	if preferred != "" {
		return preferred
	}
	if fallback != "" {
		return fallback
	}
	return IndexCoverageComplete
}

func graphTestTargetKey(target GraphTarget) string {
	if target.EntityKey != "" {
		return "entity:" + target.EntityKey
	}
	return "name:" + target.Name
}

func graphTestTargetMatches(candidate QueryCandidate, target GraphTarget) bool {
	if target.EntityKey != "" {
		return candidate.EntityKey == target.EntityKey
	}
	return candidate.LocalName == target.Name || candidate.QualifiedSymbol == target.Name
}

func graphTestEdgeMatches(edge QueryGraphEdge, query GraphEdgeQuery) bool {
	if !graphTestEdgeTouchesAny(edge, query.Nodes, query.Filter.Direction) {
		return false
	}
	if len(query.Filter.Relations) != 0 && !graphTestContainsRelation(query.Filter.Relations, edge.Relation) {
		return false
	}
	return len(query.Filter.EvidenceKinds) == 0 || graphTestContainsEvidenceKind(query.Filter.EvidenceKinds, edge.EvidenceKind)
}

func graphTestEdgeTouchesAny(edge QueryGraphEdge, nodes []QueryEntityRef, direction GraphDirection) bool {
	for _, node := range nodes {
		switch direction {
		case GraphDirectionIncoming:
			if graphTestRefsEqual(edge.To, node) {
				return true
			}
		case GraphDirectionOutgoing:
			if graphTestRefsEqual(edge.From, node) {
				return true
			}
		case GraphDirectionBoth:
			if graphTestRefsEqual(edge.From, node) || graphTestRefsEqual(edge.To, node) {
				return true
			}
		}
	}
	return false
}

func graphTestUnresolvedMatches(unresolved GraphUnresolvedSite, query GraphEdgeQuery) bool {
	if query.Filter.Direction == GraphDirectionIncoming {
		return false
	}
	if !graphTestContainsRef(query.Nodes, unresolved.From) {
		return false
	}
	return len(query.Filter.Relations) == 0 || graphTestContainsRelation(query.Filter.Relations, unresolved.Relation)
}

func graphTestContainsRelation(relations []IndexRelation, want IndexRelation) bool {
	for _, relation := range relations {
		if relation == want {
			return true
		}
	}
	return false
}

func graphTestContainsEvidenceKind(kinds []QueryEvidenceKind, want QueryEvidenceKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func graphTestContainsRef(refs []QueryEntityRef, want QueryEntityRef) bool {
	for _, ref := range refs {
		if graphTestRefsEqual(ref, want) {
			return true
		}
	}
	return false
}

func graphTestRefsEqual(left, right QueryEntityRef) bool {
	return left.SourceID == right.SourceID && left.ViewID == right.ViewID && left.EntityKey == right.EntityKey
}

func graphTestContextRefsEqual(left, right ContextRef) bool {
	if left.SourceID != right.SourceID ||
		left.CheckoutID != right.CheckoutID ||
		left.ViewID != right.ViewID ||
		left.AnalysisProfileID != right.AnalysisProfileID ||
		left.Generation != right.Generation {
		return false
	}
	if left.SpaceID == nil || right.SpaceID == nil {
		return left.SpaceID == nil && right.SpaceID == nil
	}
	return *left.SpaceID == *right.SpaceID
}

func graphTestWithoutEdge(edges []QueryGraphEdge, from, to QueryCandidate) []QueryGraphEdge {
	kept := make([]QueryGraphEdge, 0, len(edges))
	fromRef, toRef := graphTestRef(from), graphTestRef(to)
	for _, edge := range edges {
		if graphTestRefsEqual(edge.From, fromRef) && graphTestRefsEqual(edge.To, toRef) {
			continue
		}
		kept = append(kept, edge)
	}
	return kept
}

func graphTestAssertStoreBoundTo(t *testing.T, store *graphTestStore, want ContextRef) {
	t.Helper()
	if len(store.targetCalls) == 0 {
		t.Fatal("graph store resolved no targets")
	}
	for index, call := range store.targetCalls {
		if got := call.Context.Ref(); !graphTestContextRefsEqual(got, want) {
			t.Fatalf("target call %d context = %#v, want %#v", index, got, want)
		}
	}
	for index, call := range store.edgeCalls {
		if got := call.Context.Ref(); !graphTestContextRefsEqual(got, want) {
			t.Fatalf("edge call %d context = %#v, want %#v", index, got, want)
		}
		for nodeIndex, node := range call.Query.Nodes {
			graphTestAssertRefBoundTo(t, node, want, "edge call node", nodeIndex)
		}
	}
}

func graphTestAssertEdgeQueries(t *testing.T, store *graphTestStore, want ContextRef, filter GraphFilter) {
	t.Helper()
	if len(store.edgeCalls) == 0 {
		t.Fatal("neighbors issued no graph edge query")
	}
	for index, call := range store.edgeCalls {
		if got := call.Context.Ref(); !graphTestContextRefsEqual(got, want) {
			t.Fatalf("edge call %d context = %#v, want %#v", index, got, want)
		}
		if !reflect.DeepEqual(call.Query.Filter, filter) {
			t.Fatalf("edge call %d filter = %#v, want %#v", index, call.Query.Filter, filter)
		}
	}
}

func graphTestAssertGraphBoundTo(t *testing.T, result GraphResult, want ContextRef) {
	t.Helper()
	for index, node := range result.Graph.Nodes {
		graphTestAssertRefBoundTo(t, node, want, "graph node", index)
	}
	for index, edge := range result.Graph.Edges {
		graphTestAssertRefBoundTo(t, edge.From, want, "graph edge from", index)
		graphTestAssertRefBoundTo(t, edge.To, want, "graph edge to", index)
		if len(edge.EvidenceRefs) == 0 {
			t.Fatalf("graph edge %d has no source-grounded citation", index)
		}
		for evidenceIndex, evidence := range edge.EvidenceRefs {
			graphTestAssertRefBoundTo(t, evidence, want, "graph edge evidence", evidenceIndex)
		}
	}
	for index, candidate := range result.Candidates {
		graphTestAssertRefBoundTo(t, candidate, want, "ambiguous candidate", index)
	}
	for index, unresolved := range result.Unresolved {
		graphTestAssertRefBoundTo(t, unresolved.From, want, "unresolved source", index)
		if len(unresolved.EvidenceRefs) == 0 {
			t.Fatalf("unresolved site %d has no source-grounded citation", index)
		}
		for evidenceIndex, evidence := range unresolved.EvidenceRefs {
			graphTestAssertRefBoundTo(t, evidence, want, "unresolved evidence", evidenceIndex)
		}
	}
}

func graphTestAssertRefBoundTo(t *testing.T, ref QueryEntityRef, want ContextRef, kind string, index int) {
	t.Helper()
	if ref.SourceID != want.SourceID || ref.ViewID != want.ViewID {
		t.Fatalf("%s %d = %#v, want source/view %q/%q", kind, index, ref, want.SourceID, want.ViewID)
	}
}

func graphTestAssertNodeKeys(t *testing.T, nodes []QueryEntityRef, want []string) {
	t.Helper()
	got := graphTestRefKeys(nodes)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("graph node order = %#v, want %#v", got, want)
	}
}

func graphTestAssertEdgeKeys(t *testing.T, edges []QueryGraphEdge, want []string) {
	t.Helper()
	got := make([]string, 0, len(edges))
	for _, edge := range edges {
		got = append(got, edge.From.EntityKey+"->"+edge.To.EntityKey+":"+string(edge.Relation)+":"+string(edge.EvidenceKind))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("graph edge order = %#v, want %#v", got, want)
	}
}

func graphTestAssertEdge(t *testing.T, edges []QueryGraphEdge, from, to QueryCandidate, relation string, evidenceKind QueryEvidenceKind, citation QueryCandidate) {
	t.Helper()
	wantFrom, wantTo, wantCitation := graphTestRef(from), graphTestRef(to), graphTestRef(citation)
	for _, edge := range edges {
		if !graphTestRefsEqual(edge.From, wantFrom) ||
			!graphTestRefsEqual(edge.To, wantTo) ||
			edge.Relation != IndexRelation(relation) ||
			edge.EvidenceKind != evidenceKind {
			continue
		}
		if !reflect.DeepEqual(edge.EvidenceRefs, []QueryEntityRef{wantCitation}) {
			t.Fatalf("edge %s evidence refs = %#v, want %#v", graphTestEdgeKey(from, to, relation, evidenceKind), edge.EvidenceRefs, []QueryEntityRef{wantCitation})
		}
		return
	}
	t.Fatalf("missing evidence-labeled edge %s in %#v", graphTestEdgeKey(from, to, relation, evidenceKind), edges)
}

func graphTestAssertUnresolved(t *testing.T, sites []GraphUnresolvedSite, from QueryCandidate, relation string, citation QueryCandidate) {
	t.Helper()
	wantFrom, wantCitation := graphTestRef(from), graphTestRef(citation)
	for _, site := range sites {
		if !graphTestRefsEqual(site.From, wantFrom) || site.Relation != IndexRelation(relation) {
			continue
		}
		if !reflect.DeepEqual(site.EvidenceRefs, []QueryEntityRef{wantCitation}) {
			t.Fatalf("unresolved %s evidence refs = %#v, want %#v", relation, site.EvidenceRefs, []QueryEntityRef{wantCitation})
		}
		return
	}
	t.Fatalf("missing unresolved %s site from %q in %#v", relation, from.EntityKey, sites)
}

func graphTestHasEndpoint(edges []QueryGraphEdge, entityKey string) bool {
	for _, edge := range edges {
		if edge.From.EntityKey == entityKey || edge.To.EntityKey == entityKey {
			return true
		}
	}
	return false
}

func graphTestRefKeys(refs []QueryEntityRef) []string {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, ref.EntityKey)
	}
	return keys
}

func graphTestEdgeKey(from, to QueryCandidate, relation string, evidenceKind QueryEvidenceKind) string {
	return from.EntityKey + "->" + to.EntityKey + ":" + relation + ":" + string(evidenceKind)
}
