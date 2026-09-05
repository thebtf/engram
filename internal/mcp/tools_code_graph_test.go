package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciCodeGraphTargetKey       = "fixture.GraphEntry"
	uciCodeGraphTargetName      = "fixture.GraphEntryName"
	uciCodeGraphDestinationKey  = "fixture.GraphExit"
	uciCodeGraphDestinationName = "fixture.GraphExitName"
	uciCodeGraphEvidenceKey     = "fixture.GraphEvidence"
	uciCodeGraphContinuation    = "graph-page-token-1"
	uciCodeGraphBarrier         = "uci-graph-barrier"
)

type uciCodeGraphCall struct {
	ref         uci.ContextRef
	input       codebaseGraphInput
	deadline    time.Time
	hasDeadline bool
}

type uciCodeGraphFreshnessCall struct {
	ref         uci.ContextRef
	token       string
	deadline    time.Time
	hasDeadline bool
}

type uciCodeGraphFreshnessPlan struct {
	freshness          uci.QueryFreshness
	waitForContextDone bool
	returnContextError bool
	started            chan<- struct{}
	afterStart         func()
}

// uciCodeGraphApplicationFake adds the optional graph port to the established
// context/query fixture. It has no filesystem, CWD, locator, store, or
// traversal behavior: each result is an already-closed application-owned DTO
// keyed only by the AuthorizedContext received from MCP.
type uciCodeGraphApplicationFake struct {
	*uciCodeIntelCompatibilityApplication

	mu             sync.Mutex
	response       func(context.Context, uci.AuthorizedContext, codebaseGraphInput) (uci.QueryResponse, error)
	calls          []uciCodeGraphCall
	freshnessPlans map[string]uciCodeGraphFreshnessPlan
	freshnessCalls []uciCodeGraphFreshnessCall
}

var (
	_ codebaseContextApplication      = (*uciCodeGraphApplicationFake)(nil)
	_ codebaseIntelligenceApplication = (*uciCodeGraphApplicationFake)(nil)
	_ codebaseFreshnessApplication    = (*uciCodeGraphApplicationFake)(nil)
	_ codebaseGraphApplication        = (*uciCodeGraphApplicationFake)(nil)
)

func (application *uciCodeGraphApplicationFake) ExploreCodebase(ctx context.Context, authorized uci.AuthorizedContext, input codebaseGraphInput) (uci.QueryResponse, error) {
	deadline, hasDeadline := ctx.Deadline()

	application.mu.Lock()
	response := application.response
	application.calls = append(application.calls, uciCodeGraphCall{
		ref:         authorized.Ref(),
		input:       input,
		deadline:    deadline,
		hasDeadline: hasDeadline,
	})
	application.mu.Unlock()

	if response == nil {
		return uci.QueryResponse{}, errors.New("graph test application has no response")
	}
	return response(ctx, authorized, input)
}

func (application *uciCodeGraphApplicationFake) CodebaseFreshness(ctx context.Context, authorized uci.AuthorizedContext, token string) (uci.QueryFreshness, error) {
	ref := authorized.Ref()
	deadline, hasDeadline := ctx.Deadline()
	key := uciCodeGraphFreshnessKey(ref, token)

	application.mu.Lock()
	plan, found := application.freshnessPlans[key]
	application.freshnessCalls = append(application.freshnessCalls, uciCodeGraphFreshnessCall{
		ref:         ref,
		token:       token,
		deadline:    deadline,
		hasDeadline: hasDeadline,
	})
	application.mu.Unlock()

	if !found {
		if token == "" {
			return uciFreshnessObservedCurrent(ref.Generation), nil
		}
		return uci.QueryFreshness{}, errors.New("graph test freshness plan is not mapped to the authorized context")
	}
	if plan.started != nil {
		select {
		case plan.started <- struct{}{}:
		default:
		}
	}
	if plan.afterStart != nil {
		plan.afterStart()
	}
	if plan.waitForContextDone {
		<-ctx.Done()
		if plan.returnContextError {
			return uci.QueryFreshness{}, ctx.Err()
		}
	}
	return plan.freshness, nil
}

func (application *uciCodeGraphApplicationFake) setFreshnessPlan(ref uci.ContextRef, token string, plan uciCodeGraphFreshnessPlan) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.freshnessPlans[uciCodeGraphFreshnessKey(ref, token)] = plan
}

func (application *uciCodeGraphApplicationFake) graphCalls() []uciCodeGraphCall {
	application.mu.Lock()
	defer application.mu.Unlock()
	return append([]uciCodeGraphCall(nil), application.calls...)
}

func (application *uciCodeGraphApplicationFake) recordedFreshnessCalls() []uciCodeGraphFreshnessCall {
	application.mu.Lock()
	defer application.mu.Unlock()
	return append([]uciCodeGraphFreshnessCall(nil), application.freshnessCalls...)
}

func uciCodeGraphFreshnessKey(ref uci.ContextRef, token string) string {
	spaceID := ""
	if ref.SpaceID != nil {
		spaceID = *ref.SpaceID
	}
	return strings.Join([]string{
		spaceID,
		ref.SourceID,
		ref.CheckoutID,
		ref.ViewID,
		ref.AnalysisProfileID,
		token,
	}, "\x00")
}

type uciCodeGraphFixture struct {
	*uciCodeIntelCompatibilityFixture
	application *uciCodeGraphApplicationFake
}

func newUCICodeGraphFixture(t *testing.T) *uciCodeGraphFixture {
	t.Helper()

	compatibility := newUCICodeIntelCompatibilityFixture(t)
	application := &uciCodeGraphApplicationFake{
		uciCodeIntelCompatibilityApplication: compatibility.application,
		freshnessPlans:                       make(map[string]uciCodeGraphFreshnessPlan),
	}
	// The existing context setter remains the only handle owner. The graph
	// adapter is an optional capability of that one application instance.
	compatibility.server.SetCodebaseContextApplication(application)
	return &uciCodeGraphFixture{
		uciCodeIntelCompatibilityFixture: compatibility,
		application:                      application,
	}
}

func TestUCICodebaseGraphToolDefinitionIsClosedAndBounded(t *testing.T) {
	fixture := newUCICodeGraphFixture(t)
	tool := uciCodeGraphTool(t, fixture.server.ListTools())

	assert.Equal(t, "codebase_graph", tool.Name)
	assert.Equal(t, "object", tool.InputSchema["type"])
	assert.Equal(t, false, tool.InputSchema["additionalProperties"])

	required, ok := tool.InputSchema["required"].([]string)
	require.True(t, ok, "codebase_graph must declare its graph action and target")
	assert.ElementsMatch(t, []string{"action", "target"}, required)

	properties, ok := tool.InputSchema["properties"].(map[string]any)
	require.True(t, ok, "codebase_graph must expose an object property schema")
	for _, property := range []string{
		"action",
		"context_handle",
		"project",
		"target",
		"destination",
		"direction",
		"relations",
		"evidence_kinds",
		"max_depth",
		"max_visited",
		"max_nodes",
		"max_edges",
		"deadline_ms",
		"continuation",
		"after_barrier",
	} {
		require.Contains(t, properties, property, "codebase_graph must expose %q", property)
	}

	action, ok := properties["action"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", action["type"])
	actions, ok := action["enum"].([]string)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"explain", "neighbors", "path", "impact", "flow", "cycles"}, actions)

	for _, property := range []string{"target", "destination"} {
		target, ok := properties[property].(map[string]any)
		require.True(t, ok, "%s must be an object", property)
		assert.Equal(t, "object", target["type"])
		assert.Equal(t, false, target["additionalProperties"])
		targetRequired, ok := target["required"].([]string)
		require.True(t, ok)
		assert.ElementsMatch(t, []string{"source_id", "view_id"}, targetRequired)
		targetProperties, ok := target["properties"].(map[string]any)
		require.True(t, ok)
		for _, field := range []string{"source_id", "view_id", "entity_key", "name"} {
			require.Contains(t, targetProperties, field, "%s must carry explicit %s evidence", property, field)
		}
		for _, forbidden := range []string{"path", "absolute_path", "locator", "cwd", "root"} {
			assert.NotContains(t, targetProperties, forbidden, "%s must not accept a private %s", property, forbidden)
		}
	}

	project, ok := properties["project"].(map[string]any)
	require.True(t, ok)
	projectDescription, ok := project["description"].(string)
	require.True(t, ok)
	assert.Contains(t, strings.ToLower(projectDescription), "compatibility")
	assert.NotContains(t, strings.ToLower(projectDescription), "select")

	for property, bounds := range map[string][2]int{
		"max_depth":   {0, 8},
		"max_visited": {1, 5_000},
		"max_nodes":   {1, 200},
		"max_edges":   {1, 400},
		"deadline_ms": {1, 30_000},
	} {
		schema, ok := properties[property].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "integer", schema["type"])
		assert.Equal(t, bounds[0], schema["minimum"])
		assert.Equal(t, bounds[1], schema["maximum"])
	}

	afterBarrier, ok := properties["after_barrier"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, afterBarrier["additionalProperties"])
	assert.ElementsMatch(t, []string{"token", "wait_ms"}, afterBarrier["required"])

	for _, forbidden := range []string{"source_id", "view_id", "absolute_path", "path", "file", "locator", "cwd", "root", "edit", "write", "content", "body"} {
		assert.NotContains(t, properties, forbidden, "codebase_graph must not advertise root-level %q authority", forbidden)
	}
}

func TestUCICodebaseGraphAllActionsForwardOnlyAuthorizedTargets(t *testing.T) {
	for _, test := range []struct {
		name       string
		action     uci.GraphAction
		targetName bool
		path       bool
	}{
		{name: "explain", action: uci.GraphActionExplain},
		{name: "neighbors", action: uci.GraphActionNeighbors, targetName: true},
		{name: "path", action: uci.GraphActionPath, path: true},
		{name: "impact", action: uci.GraphActionImpact},
		{name: "flow", action: uci.GraphActionFlow},
		{name: "cycles", action: uci.GraphActionCycles},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCICodeGraphFixture(t)
			expected := uciCodeGraphResponse(t, fixture.refA, uciCodeGraphResponseOptions{})
			fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
				return expected, nil
			}

			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeGraphArguments(handle, test.action, fixture.refA)
			wantTarget := uci.GraphTarget{EntityKey: uciCodeGraphTargetKey}
			if test.targetName {
				target := arguments["target"].(map[string]any)
				delete(target, "entity_key")
				target["name"] = uciCodeGraphTargetName
				wantTarget = uci.GraphTarget{Name: uciCodeGraphTargetName}
			}
			var wantDestination *uci.GraphTarget
			if test.path {
				arguments["destination"] = uciCodeGraphTargetArguments(fixture.refA, "", uciCodeGraphDestinationName)
				wantDestination = &uci.GraphTarget{Name: uciCodeGraphDestinationName}
			}
			arguments["project"] = uciCodeIntelCompatibilityProject

			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
			text, _ := requireUCICodeGraphResponse(t, response, fixture, fixture.refA, uci.QueryStatusOK)
			expectedJSON, err := json.Marshal(expected)
			require.NoError(t, err)
			assert.JSONEq(t, string(expectedJSON), text, "the adapter must release the application-owned graph response unchanged")

			calls := fixture.application.graphCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, fixture.refA, calls[0].ref)
			assert.Equal(t, test.action, calls[0].input.Action)
			assert.Equal(t, wantTarget, calls[0].input.Target)
			if wantDestination == nil {
				assert.Nil(t, calls[0].input.Destination)
			} else {
				require.NotNil(t, calls[0].input.Destination)
				assert.Equal(t, *wantDestination, *calls[0].input.Destination)
			}
			require.Len(t, fixture.application.aliasCalls, 1)
			assert.Equal(t, fixture.refA, fixture.application.aliasCalls[0].ref)
			assert.Equal(t, uciCodeIntelCompatibilityProject, fixture.application.aliasCalls[0].project)
		})
	}
}

func TestUCICodebaseGraphUsesOnlyTheCallersCurrentBindingWhenHandleIsOmitted(t *testing.T) {
	fixture := newUCICodeGraphFixture(t)
	expected := uciCodeGraphResponse(t, fixture.refA, uciCodeGraphResponseOptions{})
	fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
		return expected, nil
	}

	fixture.selectContext(t, fixture.clientA, fixture.refA)
	arguments := uciCodeGraphArguments("", uci.GraphActionExplain, fixture.refA)
	delete(arguments, "context_handle")

	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
	requireUCICodeGraphResponse(t, response, fixture, fixture.refA, uci.QueryStatusOK)
	calls := fixture.application.graphCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, fixture.refA, calls[0].ref)
}

func TestUCICodebaseGraphBindsTargetAndPathDestinationToTheSelectedSourceAndView(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(map[string]any, *uciCodeGraphFixture)
		refusal bool
	}{
		{
			name: "target foreign source",
			mutate: func(arguments map[string]any, _ *uciCodeGraphFixture) {
				arguments["target"].(map[string]any)["source_id"] = uciCodeIntelCompatibilityOtherSource
			},
			refusal: true,
		},
		{
			name: "target foreign view",
			mutate: func(arguments map[string]any, fixture *uciCodeGraphFixture) {
				arguments["target"].(map[string]any)["view_id"] = fixture.refB.ViewID
			},
			refusal: true,
		},
		{
			name: "path destination foreign source",
			mutate: func(arguments map[string]any, fixture *uciCodeGraphFixture) {
				arguments["action"] = string(uci.GraphActionPath)
				destination := uciCodeGraphTargetArguments(fixture.refA, uciCodeGraphDestinationKey, "")
				destination["source_id"] = uciCodeIntelCompatibilityOtherSource
				arguments["destination"] = destination
			},
			refusal: true,
		},
		{
			name: "path destination foreign view",
			mutate: func(arguments map[string]any, fixture *uciCodeGraphFixture) {
				arguments["action"] = string(uci.GraphActionPath)
				destination := uciCodeGraphTargetArguments(fixture.refA, uciCodeGraphDestinationKey, "")
				destination["view_id"] = fixture.refB.ViewID
				arguments["destination"] = destination
			},
			refusal: true,
		},
		{
			name: "target carries both entity key and name",
			mutate: func(arguments map[string]any, _ *uciCodeGraphFixture) {
				arguments["target"].(map[string]any)["name"] = uciCodeGraphTargetName
			},
		},
		{
			name: "target carries neither entity key nor name",
			mutate: func(arguments map[string]any, _ *uciCodeGraphFixture) {
				target := arguments["target"].(map[string]any)
				delete(target, "entity_key")
			},
		},
		{
			name: "target lacks source identity",
			mutate: func(arguments map[string]any, _ *uciCodeGraphFixture) {
				delete(arguments["target"].(map[string]any), "source_id")
			},
		},
		{
			name: "target lacks view identity",
			mutate: func(arguments map[string]any, _ *uciCodeGraphFixture) {
				delete(arguments["target"].(map[string]any), "view_id")
			},
		},
		{
			name: "path omits destination",
			mutate: func(arguments map[string]any, _ *uciCodeGraphFixture) {
				arguments["action"] = string(uci.GraphActionPath)
			},
		},
		{
			name: "destination is rejected outside path",
			mutate: func(arguments map[string]any, fixture *uciCodeGraphFixture) {
				arguments["destination"] = uciCodeGraphTargetArguments(fixture.refA, uciCodeGraphDestinationKey, "")
			},
		},
		{
			name: "path destination lacks source identity",
			mutate: func(arguments map[string]any, fixture *uciCodeGraphFixture) {
				arguments["action"] = string(uci.GraphActionPath)
				destination := uciCodeGraphTargetArguments(fixture.refA, uciCodeGraphDestinationKey, "")
				delete(destination, "source_id")
				arguments["destination"] = destination
			},
		},
		{
			name: "path destination lacks view identity",
			mutate: func(arguments map[string]any, fixture *uciCodeGraphFixture) {
				arguments["action"] = string(uci.GraphActionPath)
				destination := uciCodeGraphTargetArguments(fixture.refA, uciCodeGraphDestinationKey, "")
				delete(destination, "view_id")
				arguments["destination"] = destination
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCICodeGraphFixture(t)
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeGraphArguments(handle, uci.GraphActionNeighbors, fixture.refA)
			test.mutate(arguments, fixture)

			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
			if test.refusal {
				requireUCICodeGraphSuppressed(t, response, fixture, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
			} else {
				requireUCICodeGraphSafeToolError(t, response, fixture)
			}
			assert.Empty(t, fixture.application.graphCalls(), "an invalid or foreign graph selector must not reach the application")
		})
	}
}

func TestUCICodebaseGraphForwardsClosedFiltersAndEveryTraversalBudget(t *testing.T) {
	fixture := newUCICodeGraphFixture(t)
	expected := uciCodeGraphResponse(t, fixture.refA, uciCodeGraphResponseOptions{})
	fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
		return expected, nil
	}

	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	arguments := uciCodeGraphArguments(handle, uci.GraphActionPath, fixture.refA)
	arguments["destination"] = uciCodeGraphTargetArguments(fixture.refA, uciCodeGraphDestinationKey, "")
	arguments["direction"] = string(uci.GraphDirectionOutgoing)
	arguments["relations"] = []string{"imports", "calls"}
	arguments["evidence_kinds"] = []string{"RESOLVED", "HEURISTIC"}
	arguments["max_depth"] = 8
	arguments["max_visited"] = 5_000
	arguments["max_nodes"] = 200
	arguments["max_edges"] = 400
	arguments["deadline_ms"] = 30_000
	arguments["continuation"] = uciCodeGraphContinuation

	parent, cancel := context.WithTimeout(fixture.clientA, time.Second)
	defer cancel()
	parentDeadline, hasParentDeadline := parent.Deadline()
	require.True(t, hasParentDeadline)

	response := callUCICodeIntel(t, fixture.server, parent, "codebase_graph", arguments)
	requireUCICodeGraphResponse(t, response, fixture, fixture.refA, uci.QueryStatusOK)

	calls := fixture.application.graphCalls()
	require.Len(t, calls, 1)
	call := calls[0]
	assert.Equal(t, fixture.refA, call.ref)
	assert.Equal(t, uci.GraphActionPath, call.input.Action)
	assert.Equal(t, uci.GraphTarget{EntityKey: uciCodeGraphTargetKey}, call.input.Target)
	require.NotNil(t, call.input.Destination)
	assert.Equal(t, uci.GraphTarget{EntityKey: uciCodeGraphDestinationKey}, *call.input.Destination)
	assert.Equal(t, uci.GraphDirectionOutgoing, call.input.Filter.Direction)
	assert.ElementsMatch(t, []uci.IndexRelation{uci.IndexRelation("calls"), uci.IndexRelation("imports")}, call.input.Filter.Relations)
	assert.ElementsMatch(t, []uci.QueryEvidenceKind{uci.QueryEvidenceHeuristic, uci.QueryEvidenceResolved}, call.input.Filter.EvidenceKinds)
	assert.Equal(t, 8, call.input.Budget.MaxDepth)
	assert.Equal(t, 5_000, call.input.Budget.MaxVisited)
	assert.Equal(t, 200, call.input.Budget.MaxNodes)
	assert.Equal(t, 400, call.input.Budget.MaxEdges)
	assert.False(t, call.input.Budget.Deadline.IsZero(), "deadline_ms must become a typed graph deadline")
	assert.False(t, call.input.Budget.Deadline.After(parentDeadline), "the graph deadline must not outlive the caller")
	require.NotNil(t, call.input.Continuation)
	assert.Equal(t, uciCodeGraphContinuation, *call.input.Continuation)
}

func TestUCICodebaseGraphRejectsOpenFiltersAndUnboundedTraversalBeforeApplication(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(map[string]any)
		forbidden string
	}{
		{
			name: "unknown direction",
			mutate: func(arguments map[string]any) {
				arguments["direction"] = "sideways"
			},
		},
		{
			name: "unknown relation label",
			mutate: func(arguments map[string]any) {
				arguments["relations"] = []string{"runtime_execution"}
			},
			forbidden: "runtime_execution",
		},
		{
			name: "unknown evidence label",
			mutate: func(arguments map[string]any) {
				arguments["evidence_kinds"] = []string{"UNVERIFIED"}
			},
			forbidden: "UNVERIFIED",
		},
		{
			name: "too many relation labels",
			mutate: func(arguments map[string]any) {
				relations := make([]string, 33)
				for index := range relations {
					relations[index] = "calls"
				}
				arguments["relations"] = relations
			},
		},
		{
			name: "too many evidence labels",
			mutate: func(arguments map[string]any) {
				evidenceKinds := make([]string, 5)
				for index := range evidenceKinds {
					evidenceKinds[index] = "RESOLVED"
				}
				arguments["evidence_kinds"] = evidenceKinds
			},
		},
		{
			name: "depth cap exceeds public maximum",
			mutate: func(arguments map[string]any) {
				arguments["max_depth"] = 9
			},
		},
		{
			name: "visited cap exceeds public maximum",
			mutate: func(arguments map[string]any) {
				arguments["max_visited"] = 5_001
			},
		},
		{
			name: "node cap exceeds public maximum",
			mutate: func(arguments map[string]any) {
				arguments["max_nodes"] = 201
			},
		},
		{
			name: "edge cap exceeds public maximum",
			mutate: func(arguments map[string]any) {
				arguments["max_edges"] = 401
			},
		},
		{
			name: "deadline exceeds public maximum",
			mutate: func(arguments map[string]any) {
				arguments["deadline_ms"] = 30_001
			},
		},
		{
			name: "empty continuation is not opaque",
			mutate: func(arguments map[string]any) {
				arguments["continuation"] = ""
			},
		},
		{
			name: "oversized continuation",
			mutate: func(arguments map[string]any) {
				arguments["continuation"] = strings.Repeat("x", 2_049)
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCICodeGraphFixture(t)
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA)
			test.mutate(arguments)

			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
			requireUCICodeGraphSafeToolError(t, response, fixture)
			assert.Empty(t, fixture.application.graphCalls(), "unbounded graph requests must fail before application traversal")
			if test.forbidden != "" {
				raw, err := json.Marshal(response)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), test.forbidden)
			}
		})
	}
}

func TestUCICodebaseGraphAppliesFreshnessBarrierBeforeTraversal(t *testing.T) {
	fixture := newUCICodeGraphFixture(t)
	expected := uciCodeGraphResponse(t, fixture.refA, uciCodeGraphResponseOptions{})
	fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
		return expected, nil
	}
	freshness := uciFreshnessBarrierSatisfied(fixture.refA.Generation, 25)
	fixture.application.setFreshnessPlan(fixture.refA, uciCodeGraphBarrier, uciCodeGraphFreshnessPlan{freshness: freshness})

	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	arguments := uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA)
	arguments["after_barrier"] = uciFreshnessBarrierArguments(uciCodeGraphBarrier, 25)

	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
	_, payload := requireUCICodeGraphResponse(t, response, fixture, fixture.refA, uci.QueryStatusOK)
	requireUCIFreshnessValue(t, payload.Freshness, freshness)

	freshnessCalls := fixture.application.recordedFreshnessCalls()
	require.Len(t, freshnessCalls, 1)
	assert.Equal(t, fixture.refA, freshnessCalls[0].ref)
	assert.Equal(t, uciCodeGraphBarrier, freshnessCalls[0].token)
	assert.True(t, freshnessCalls[0].hasDeadline, "after_barrier must bound the freshness wait")
	require.Len(t, fixture.application.graphCalls(), 1)
}

func TestUCICodebaseGraphPreservesHistoricalContextWithoutCurrentLocatorFallback(t *testing.T) {
	fixture := newUCICodeGraphFixture(t)
	expected := uciCodeGraphResponse(t, fixture.refA, uciCodeGraphResponseOptions{
		Historical:  true,
		ExposureRef: "uci-exp_graph-history",
	})
	fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
		return expected, nil
	}
	fixture.application.setFreshnessPlan(fixture.refA, "", uciCodeGraphFreshnessPlan{freshness: uciFreshnessHistorical(fixture.refA.Generation)})

	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", uciCodeGraphArguments(handle, uci.GraphActionFlow, fixture.refA))
	text, payload := requireUCICodeGraphResponse(t, response, fixture, fixture.refA, uci.QueryStatusOK)
	require.NotNil(t, payload.Freshness)
	assert.Equal(t, uci.QueryFreshnessHistorical, payload.Freshness.State)
	assert.Equal(t, uci.QueryFreshnessPinnedHistory, payload.Freshness.Method)
	assert.NotContains(t, text, uciCodebaseContextPrivateLocatorA)
	assert.NotContains(t, text, uciCodebaseContextPrivateLocatorB)
}

func TestUCICodebaseGraphReleasesAmbiguousPartialAndCappedApplicationOutcomes(t *testing.T) {
	continuation := "application-owned-next-page"
	for _, test := range []struct {
		name      string
		options   uciCodeGraphResponseOptions
		stop      uci.QueryGraphStopReason
		truncated bool
	}{
		{
			name: "ambiguous target has no fabricated traversal",
			options: uciCodeGraphResponseOptions{
				Status: uci.QueryStatusPartial,
				Nodes:  []uci.QueryEntityRef{},
				Edges:  []uci.QueryGraphEdge{},
				Stop:   uci.QueryGraphComplete,
			},
			stop: uci.QueryGraphComplete,
		},
		{
			name: "partial coverage remains non-conclusive",
			options: uciCodeGraphResponseOptions{
				Status:   uci.QueryStatusPartial,
				Coverage: uci.IndexCoveragePartial,
				Nodes:    []uci.QueryEntityRef{},
				Edges:    []uci.QueryGraphEdge{},
				Stop:     uci.QueryGraphCoverageGap,
			},
			stop: uci.QueryGraphCoverageGap,
		},
		{
			name: "capped page retains its application continuation",
			options: uciCodeGraphResponseOptions{
				Status:       uci.QueryStatusPartial,
				Nodes:        []uci.QueryEntityRef{},
				Edges:        []uci.QueryGraphEdge{},
				Stop:         uci.QueryGraphNodeCap,
				Truncated:    true,
				Continuation: &continuation,
			},
			stop:      uci.QueryGraphNodeCap,
			truncated: true,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCICodeGraphFixture(t)
			expected := uciCodeGraphResponse(t, fixture.refA, test.options)
			fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
				return expected, nil
			}

			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeGraphArguments(handle, uci.GraphActionPath, fixture.refA)
			arguments["destination"] = uciCodeGraphTargetArguments(fixture.refA, uciCodeGraphDestinationKey, "")
			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
			_, payload := requireUCICodeGraphResponse(t, response, fixture, fixture.refA, uci.QueryStatusPartial)
			require.NotNil(t, payload.Graph)
			assert.Equal(t, test.stop, payload.Graph.StopReason)
			require.NotNil(t, payload.Truncated)
			assert.Equal(t, test.truncated, *payload.Truncated)
			if test.truncated {
				require.NotNil(t, payload.Continuation)
				require.NotNil(t, payload.Continuation.Value)
				assert.Equal(t, continuation, *payload.Continuation.Value)
			} else {
				require.NotNil(t, payload.Continuation)
				assert.Nil(t, payload.Continuation.Value)
			}
		})
	}
}

func TestUCICodebaseGraphDeniesForeignRevokedPrivateAndRetiredContextsBeforeApplication(t *testing.T) {
	t.Run("foreign opaque handle", func(t *testing.T) {
		fixture := newUCICodeGraphFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)

		response := callUCICodeIntel(t, fixture.server, fixture.clientC, "codebase_graph", uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA))
		requireUCICodeGraphSuppressed(t, response, fixture, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
		assert.Empty(t, fixture.application.graphCalls())
	})

	t.Run("revoked selected context", func(t *testing.T) {
		fixture := newUCICodeGraphFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		fixture.authorizer.allowed["agent/a"][fixture.refA.CheckoutID] = false

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA))
		requireUCICodeGraphSuppressed(t, response, fixture, uci.QueryStatusForbidden, uci.QueryErrorPermissionDenied)
		assert.Empty(t, fixture.application.graphCalls())
	})

	t.Run("private context from another client", func(t *testing.T) {
		fixture := newUCICodeGraphFixture(t)
		handle := fixture.selectContext(t, fixture.clientB, fixture.refB)

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refB))
		requireUCICodeGraphSuppressed(t, response, fixture, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
		assert.Empty(t, fixture.application.graphCalls())
	})

	t.Run("recreated checkout retires its old handle", func(t *testing.T) {
		fixture := newUCICodeGraphFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		recreated := fixture.refA
		recreated.ViewID = "40000000-0000-4000-8000-000000000003"
		recreated.AnalysisProfileID = "50000000-0000-4000-8000-000000000003"
		recreated.Generation++
		fixture.catalog.records[fixture.refA.CheckoutID] = uci.ContextRecord{Ref: recreated, AuthRealm: uciCodebaseContextTestRealm}

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA))
		requireUCICodeGraphSuppressed(t, response, fixture, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
		assert.Empty(t, fixture.application.graphCalls())
	})

	t.Run("conflicting compatibility project", func(t *testing.T) {
		fixture := newUCICodeGraphFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		arguments := uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA)
		arguments["project"] = uciCodeIntelCompatibilityConflict

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
		requireUCICodeGraphSuppressed(t, response, fixture, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
		require.Len(t, fixture.application.aliasCalls, 1)
		assert.Empty(t, fixture.application.graphCalls(), "project evidence cannot select or widen a graph context")
	})
}

func TestUCICodebaseGraphRejectsUnknownLocatorAndEditArgumentsBeforeApplication(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(map[string]any)
		forbidden string
	}{
		{
			name: "absolute locator",
			mutate: func(arguments map[string]any) {
				arguments["absolute_path"] = `C:\private\source.go`
			},
			forbidden: `C:\\private\\source.go`,
		},
		{
			name: "caller working directory",
			mutate: func(arguments map[string]any) {
				arguments["cwd"] = `D:\private\worktree`
			},
			forbidden: `D:\\private\\worktree`,
		},
		{
			name: "nested private locator",
			mutate: func(arguments map[string]any) {
				arguments["target"].(map[string]any)["locator"] = `C:\private\source.go`
			},
			forbidden: `C:\\private\\source.go`,
		},
		{
			name: "edit attempt",
			mutate: func(arguments map[string]any) {
				arguments["edit"] = map[string]any{"replace": "forbidden graph rewrite"}
			},
			forbidden: "forbidden graph rewrite",
		},
		{
			name: "client supplied barrier sequence",
			mutate: func(arguments map[string]any) {
				arguments["after_barrier"] = map[string]any{
					"token":    uciCodeGraphBarrier,
					"wait_ms":  25,
					"sequence": 9_007_199_254_740_991,
				}
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCICodeGraphFixture(t)
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA)
			test.mutate(arguments)

			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
			requireUCICodeGraphSafeToolError(t, response, fixture)
			assert.Empty(t, fixture.application.graphCalls(), "unknown path or mutation fields must not reach graph traversal")
			if test.forbidden != "" {
				raw, err := json.Marshal(response)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), test.forbidden)
			}
		})
	}
}

func TestUCICodebaseGraphSuppressesApplicationResultsAfterEpochChange(t *testing.T) {
	fixture := newUCICodeGraphFixture(t)
	expected := uciCodeGraphResponse(t, fixture.refA, uciCodeGraphResponseOptions{})
	fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
		fixture.server.SetCodebaseContextApplication(fixture.application)
		return expected, nil
	}

	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA))
	requireUCICodeGraphSuppressed(t, response, fixture, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
	require.Len(t, fixture.application.graphCalls(), 1)
}

func TestUCICodebaseGraphPreservesCallerCancellation(t *testing.T) {
	fixture := newUCICodeGraphFixture(t)
	started := make(chan struct{}, 1)
	fixture.application.response = func(ctx context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return uci.QueryResponse{}, ctx.Err()
	}

	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	arguments := uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA)
	params, err := json.Marshal(map[string]any{
		"name":      "codebase_graph",
		"arguments": arguments,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(fixture.clientA)
	defer cancel()
	responses := make(chan *Response, 1)
	go func() {
		responses <- fixture.server.HandleRequest(ctx, &Request{
			JSONRPC: "2.0",
			ID:      float64(1),
			Method:  "tools/call",
			Params:  params,
		})
	}()

	awaitUCICodeGraphSignal(t, started, "graph application start")
	cancel()
	select {
	case response := <-responses:
		require.NotNil(t, response)
		require.NotNil(t, response.Error, "caller cancellation must not become a graph result")
		assert.Contains(t, strings.ToLower(response.Error.Message), "cancel")
	case <-time.After(time.Second):
		t.Fatal("cancelled graph request did not return")
	}
}

func TestUCICodebaseGraphReleasesOnlyExactClosedBoundedApplicationResponses(t *testing.T) {
	t.Run("exact graph response retains identity bounds labels and application exposure", func(t *testing.T) {
		fixture := newUCICodeGraphFixture(t)
		expected := uciCodeGraphResponse(t, fixture.refA, uciCodeGraphResponseOptions{})
		fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
			return expected, nil
		}

		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		arguments := uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA)
		arguments["max_nodes"] = 2
		arguments["max_edges"] = 1
		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
		text, payload := requireUCICodeGraphResponse(t, response, fixture, fixture.refA, uci.QueryStatusOK)
		expectedJSON, err := json.Marshal(expected)
		require.NoError(t, err)
		assert.JSONEq(t, string(expectedJSON), text)
		require.NotNil(t, payload.Exposure)
		assert.Equal(t, "uci-exp_graph", payload.Exposure.ExposureRef)
		require.NotNil(t, payload.Graph)
		require.Len(t, payload.Graph.Nodes, 2)
		require.Len(t, payload.Graph.Edges, 1)
		assert.Equal(t, uci.IndexRelation("calls"), payload.Graph.Edges[0].Relation)
		assert.Equal(t, uci.QueryEvidenceResolved, payload.Graph.Edges[0].EvidenceKind)
		assertUCICodeGraphScope(t, *payload.Graph, fixture.refA)
	})

	t.Run("different application response context is denied", func(t *testing.T) {
		fixture := newUCICodeGraphFixture(t)
		foreign := uciCodeGraphResponse(t, fixture.refB, uciCodeGraphResponseOptions{ExposureRef: "uci-exp_graph-foreign"})
		fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
			return foreign, nil
		}

		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA))
		requireUCICodeGraphSuppressed(t, response, fixture, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
	})

	for _, test := range []struct {
		name      string
		configure func(*testing.T, *uci.QueryResponse, map[string]any, *uciCodeGraphFixture)
	}{
		{
			name: "missing application exposure",
			configure: func(_ *testing.T, response *uci.QueryResponse, _ map[string]any, _ *uciCodeGraphFixture) {
				response.Exposure = nil
			},
		},
		{
			name: "non graph retrieval mode",
			configure: func(_ *testing.T, response *uci.QueryResponse, _ map[string]any, _ *uciCodeGraphFixture) {
				response.Retrieval.Mode = uci.QueryRetrievalLexical
			},
		},
		{
			name: "missing graph body",
			configure: func(_ *testing.T, response *uci.QueryResponse, _ map[string]any, _ *uciCodeGraphFixture) {
				response.Graph = nil
			},
		},
		{
			name: "cross view graph node",
			configure: func(_ *testing.T, response *uci.QueryResponse, _ map[string]any, fixture *uciCodeGraphFixture) {
				foreign := (*response.Graph).Nodes[1]
				foreign.ViewID = fixture.refB.ViewID
				(*response.Graph).Nodes[1] = foreign
				(*response.Graph).Edges[0].To = foreign
			},
		},
		{
			name: "cross view graph evidence",
			configure: func(_ *testing.T, response *uci.QueryResponse, _ map[string]any, fixture *uciCodeGraphFixture) {
				foreign := (*response.Graph).Edges[0].EvidenceRefs[0]
				foreign.ViewID = fixture.refB.ViewID
				(*response.Graph).Edges[0].EvidenceRefs[0] = foreign
			},
		},
		{
			name: "graph exceeds requested node cap",
			configure: func(_ *testing.T, _ *uci.QueryResponse, arguments map[string]any, _ *uciCodeGraphFixture) {
				arguments["max_nodes"] = 1
			},
		},
		{
			name: "graph exceeds requested edge cap",
			configure: func(t *testing.T, response *uci.QueryResponse, arguments map[string]any, fixture *uciCodeGraphFixture) {
				third := uciCodeGraphEntity(fixture.refA, "fixture.GraphThird")
				(*response.Graph).Nodes = append((*response.Graph).Nodes, third)
				(*response.Graph).Edges = append((*response.Graph).Edges, uci.QueryGraphEdge{
					From:         (*response.Graph).Nodes[1],
					To:           third,
					Relation:     uci.IndexRelation("calls"),
					EvidenceKind: uci.QueryEvidenceResolved,
					EvidenceRefs: []uci.QueryEntityRef{uciCodeGraphEntity(fixture.refA, uciCodeGraphEvidenceKey)},
				})
				arguments["max_nodes"] = 3
				arguments["max_edges"] = 1
				require.NoError(t, response.Validate(), "the application response remains closed; MCP owns the caller cap")
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCICodeGraphFixture(t)
			applicationResponse := uciCodeGraphResponse(t, fixture.refA, uciCodeGraphResponseOptions{})
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeGraphArguments(handle, uci.GraphActionExplain, fixture.refA)
			test.configure(t, &applicationResponse, arguments, fixture)
			fixture.application.response = func(_ context.Context, _ uci.AuthorizedContext, _ codebaseGraphInput) (uci.QueryResponse, error) {
				return applicationResponse, nil
			}

			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_graph", arguments)
			requireUCICodeGraphSafeToolError(t, response, fixture)
			require.Len(t, fixture.application.graphCalls(), 1)
		})
	}
}

func uciCodeGraphArguments(handle string, action uci.GraphAction, ref uci.ContextRef) map[string]any {
	arguments := map[string]any{
		"action": string(action),
		"target": uciCodeGraphTargetArguments(ref, uciCodeGraphTargetKey, ""),
	}
	if handle != "" {
		arguments["context_handle"] = handle
	}
	return arguments
}

func uciCodeGraphTargetArguments(ref uci.ContextRef, entityKey, name string) map[string]any {
	target := map[string]any{
		"source_id": ref.SourceID,
		"view_id":   ref.ViewID,
	}
	if entityKey != "" {
		target["entity_key"] = entityKey
	}
	if name != "" {
		target["name"] = name
	}
	return target
}

type uciCodeGraphResponseOptions struct {
	Status       uci.QueryResponseStatus
	Coverage     uci.IndexCoverageState
	Nodes        []uci.QueryEntityRef
	Edges        []uci.QueryGraphEdge
	Stop         uci.QueryGraphStopReason
	Truncated    bool
	Continuation *string
	Historical   bool
	ExposureRef  string
}

func uciCodeGraphResponse(t *testing.T, ref uci.ContextRef, options uciCodeGraphResponseOptions) uci.QueryResponse {
	t.Helper()

	status := options.Status
	if status == "" {
		status = uci.QueryStatusOK
	}
	coverage := options.Coverage
	if coverage == "" {
		coverage = uci.IndexCoverageComplete
	}
	stop := options.Stop
	if stop == "" {
		stop = uci.QueryGraphComplete
	}
	exposureRef := options.ExposureRef
	if exposureRef == "" {
		exposureRef = "uci-exp_graph"
	}

	nodes, edges := options.Nodes, options.Edges
	if nodes == nil && edges == nil {
		nodes, edges = uciCodeGraphDefaultGraph(ref)
	} else {
		if nodes == nil {
			nodes = []uci.QueryEntityRef{}
		}
		if edges == nil {
			edges = []uci.QueryGraphEdge{}
		}
	}

	zero := int64(0)
	freshness := uci.QueryFreshness{
		State:          uci.QueryFreshnessObservedCurrent,
		Method:         uci.QueryFreshnessWatchWatermark,
		PendingChanges: &zero,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: ref.Generation,
			State:    uci.QueryEnrichmentCurrent,
		},
	}
	if options.Historical {
		freshness.State = uci.QueryFreshnessHistorical
		freshness.Method = uci.QueryFreshnessPinnedHistory
		freshness.PendingChanges = nil
	}

	contexts := uci.QueryContexts{codebaseQueryContextRef(ref)}
	items := uci.QueryItems{}
	warnings := uci.QueryWarnings{}
	response := uci.QueryResponse{
		Schema:    uci.QueryResponseSchema,
		Status:    status,
		Contexts:  &contexts,
		Freshness: &freshness,
		Retrieval: &uci.QueryRetrieval{
			Mode:               uci.QueryRetrievalGraph,
			DegradationReasons: []string{},
		},
		Coverage: &uci.QueryCoverage{
			Structural:       coverage,
			UnresolvedSites:  &zero,
			UnsupportedFiles: &zero,
		},
		Exposure: &uci.QueryExposure{
			ExposureRef:     exposureRef,
			CompletionState: uci.QueryCompletionUnknown,
		},
		Items: &items,
		Graph: &uci.QueryGraph{
			Nodes:      nodes,
			Edges:      edges,
			StopReason: stop,
		},
		Truncated:    &options.Truncated,
		Warnings:     &warnings,
		Continuation: &uci.QueryContinuation{Value: options.Continuation},
	}
	require.NoError(t, response.Validate(), "graph test application must begin with a closed UCI response")
	return response
}

func uciCodeGraphDefaultGraph(ref uci.ContextRef) ([]uci.QueryEntityRef, []uci.QueryGraphEdge) {
	entry := uciCodeGraphEntity(ref, uciCodeGraphTargetKey)
	exit := uciCodeGraphEntity(ref, uciCodeGraphDestinationKey)
	evidence := uciCodeGraphEntity(ref, uciCodeGraphEvidenceKey)
	return []uci.QueryEntityRef{entry, exit}, []uci.QueryGraphEdge{{
		From:         entry,
		To:           exit,
		Relation:     uci.IndexRelation("calls"),
		EvidenceKind: uci.QueryEvidenceResolved,
		EvidenceRefs: []uci.QueryEntityRef{evidence},
	}}
}

func uciCodeGraphEntity(ref uci.ContextRef, entityKey string) uci.QueryEntityRef {
	return uci.QueryEntityRef{
		SourceID:  ref.SourceID,
		ViewID:    ref.ViewID,
		EntityKey: entityKey,
	}
}

func uciCodeGraphTool(t *testing.T, tools []Tool) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == "codebase_graph" {
			return tool
		}
	}
	t.Fatalf("codebase_graph was not advertised")
	return Tool{}
}

func requireUCICodeGraphResponse(t *testing.T, response *Response, fixture *uciCodeGraphFixture, wantRef uci.ContextRef, wantStatus uci.QueryResponseStatus) (string, uci.QueryResponse) {
	t.Helper()

	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate(), "codebase_graph must release a closed UCI QueryResponse")
	assert.Equal(t, wantStatus, payload.Status)
	require.NotNil(t, payload.Contexts)
	require.Len(t, *payload.Contexts, 1)
	assertUCICodeGraphContext(t, (*payload.Contexts)[0], wantRef)
	require.NotNil(t, payload.Retrieval)
	assert.Equal(t, uci.QueryRetrievalGraph, payload.Retrieval.Mode)
	require.NotNil(t, payload.Exposure)
	require.NotNil(t, payload.Graph)
	assertUCICodeGraphScope(t, *payload.Graph, wantRef)
	assert.NotContains(t, text, `"project"`, "legacy project evidence must never be emitted as graph authority")
	assert.NotContains(t, text, uciCodebaseContextPrivateLocatorA)
	assert.NotContains(t, text, uciCodebaseContextPrivateLocatorB)
	return text, payload
}

func assertUCICodeGraphContext(t *testing.T, actual uci.QueryContextRef, want uci.ContextRef) {
	t.Helper()
	assert.Equal(t, want.SpaceID, actual.SpaceID)
	assert.Equal(t, want.SourceID, actual.SourceID)
	assert.Equal(t, want.CheckoutID, actual.CheckoutID)
	assert.Equal(t, want.ViewID, actual.ViewID)
	assert.Equal(t, want.Generation, actual.Generation)
	assert.Equal(t, want.AnalysisProfileID, actual.ProfileID)
}

func assertUCICodeGraphScope(t *testing.T, graph uci.QueryGraph, want uci.ContextRef) {
	t.Helper()
	for _, node := range graph.Nodes {
		assert.Equal(t, want.SourceID, node.SourceID, "graph node must remain in the authorized Source")
		assert.Equal(t, want.ViewID, node.ViewID, "graph node must remain in the authorized View")
	}
	for _, edge := range graph.Edges {
		for _, ref := range append([]uci.QueryEntityRef{edge.From, edge.To}, edge.EvidenceRefs...) {
			assert.Equal(t, want.SourceID, ref.SourceID, "graph edge/evidence must remain in the authorized Source")
			assert.Equal(t, want.ViewID, ref.ViewID, "graph edge/evidence must remain in the authorized View")
		}
	}
}

func requireUCICodeGraphSuppressed(t *testing.T, response *Response, fixture *uciCodeGraphFixture, wantStatus uci.QueryResponseStatus, wantCode uci.QueryErrorCode) {
	t.Helper()

	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate(), "graph denial must remain a closed UCI response")
	assert.Equal(t, wantStatus, payload.Status)
	require.NotNil(t, payload.Error)
	assert.Equal(t, wantCode, payload.Error.Code)
	assert.Nil(t, payload.Exposure)
	assert.Nil(t, payload.Contexts)
	assert.Nil(t, payload.Freshness)
	assert.Nil(t, payload.Retrieval)
	assert.Nil(t, payload.Coverage)
	assert.Nil(t, payload.Items)
	assert.Nil(t, payload.Graph)
	assert.Nil(t, payload.Truncated)
	assert.Nil(t, payload.Warnings)
	assert.Nil(t, payload.Continuation)
	assertUCICodeGraphNoLeaks(t, text, fixture)
}

func requireUCICodeGraphSafeToolError(t *testing.T, response *Response, fixture *uciCodeGraphFixture) {
	t.Helper()

	require.NotNil(t, response.Error)
	require.Nil(t, response.Result)
	assert.Equal(t, -32000, response.Error.Code)
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	assertUCICodeGraphNoLeaks(t, string(raw), fixture)
}

func assertUCICodeGraphNoLeaks(t *testing.T, raw string, fixture *uciCodeGraphFixture) {
	t.Helper()
	for _, forbidden := range []string{
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
		fixture.refB.SourceID,
		fixture.refB.CheckoutID,
		fixture.refB.ViewID,
		uciCodeGraphTargetKey,
		uciCodeGraphDestinationKey,
		uciCodeGraphEvidenceKey,
		"uci-exp_graph",
		uciCodebaseContextPrivateLocatorA,
		uciCodebaseContextPrivateLocatorB,
		`"project"`,
		`"graph"`,
		`"contexts"`,
	} {
		assert.NotContains(t, raw, forbidden, "suppressed graph result must not disclose %q", forbidden)
	}
}

func awaitUCICodeGraphSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
