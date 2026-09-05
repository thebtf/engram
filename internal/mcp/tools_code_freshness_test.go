package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciFreshnessBarrierA = "uci-barrier-a"
)

// T062's narrow optional MCP application port is intentionally structural:
//
//	CodebaseFreshness(context.Context, uci.AuthorizedContext, string) (uci.QueryFreshness, error)
//
// The string is an opaque, server-issued barrier token. The transport owns the
// caller-selected wait budget and passes the clamped deadline through context;
// the application receives only an already-authorized ContextRef and never a
// client-supplied filesystem sequence, path, or checkout selector.
type uciFreshnessTestApplication struct {
	*uciCodeIntelCompatibilityApplication

	mu    sync.Mutex
	plans map[string]uciFreshnessTestPlan
	calls []uciFreshnessTestCall
}

type uciFreshnessTestPlan struct {
	freshness          uci.QueryFreshness
	waitForContextDone bool
	returnContextError bool
	started            chan<- struct{}
	afterStart         func()
}

type uciFreshnessTestCall struct {
	ref         uci.ContextRef
	token       string
	deadline    time.Time
	hasDeadline bool
}

type uciFreshnessFixture struct {
	*uciCodeIntelCompatibilityFixture
	application *uciFreshnessTestApplication
}

type uciFreshnessStatusPayload struct {
	Context          uci.QueryContextRef            `json:"context"`
	TotalChunks      int64                          `json:"total_chunks"`
	EmbeddedChunks   int64                          `json:"embedded_chunks"`
	EvidenceRecorder CodebaseEvidenceRecorderHealth `json:"evidence_recorder"`
	Freshness        uci.QueryFreshness             `json:"freshness"`
}

func newUCIFreshnessFixture(t *testing.T) *uciFreshnessFixture {
	t.Helper()

	compatibility := newUCICodeIntelCompatibilityFixture(t)
	application := &uciFreshnessTestApplication{
		uciCodeIntelCompatibilityApplication: compatibility.application,
		plans:                                make(map[string]uciFreshnessTestPlan),
	}
	compatibility.server.SetCodebaseContextApplication(application)
	return &uciFreshnessFixture{
		uciCodeIntelCompatibilityFixture: compatibility,
		application:                      application,
	}
}

func (application *uciFreshnessTestApplication) CodebaseFreshness(ctx context.Context, authorized uci.AuthorizedContext, token string) (uci.QueryFreshness, error) {
	ref := authorized.Ref()
	key := uciFreshnessPlanKey(ref, token)

	application.mu.Lock()
	plan, found := application.plans[key]
	deadline, hasDeadline := ctx.Deadline()
	application.calls = append(application.calls, uciFreshnessTestCall{
		ref:         ref,
		token:       token,
		deadline:    deadline,
		hasDeadline: hasDeadline,
	})
	application.mu.Unlock()

	if !found {
		return uci.QueryFreshness{}, errors.New("freshness test fixture has no authorized context/token plan")
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

func (application *uciFreshnessTestApplication) setPlan(ref uci.ContextRef, token string, plan uciFreshnessTestPlan) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.plans[uciFreshnessPlanKey(ref, token)] = plan
}

func (application *uciFreshnessTestApplication) freshnessCalls() []uciFreshnessTestCall {
	application.mu.Lock()
	defer application.mu.Unlock()
	return append([]uciFreshnessTestCall(nil), application.calls...)
}

func uciFreshnessPlanKey(ref uci.ContextRef, token string) string {
	spaceID := ""
	if ref.SpaceID != nil {
		spaceID = *ref.SpaceID
	}
	return fmt.Sprintf("%s/%s/%s/%s/%d/%s/%s", spaceID, ref.SourceID, ref.CheckoutID, ref.ViewID, ref.Generation, ref.AnalysisProfileID, token)
}

func TestUCIFreshnessPublicStatesAreScopedAndClosed(t *testing.T) {
	tests := []struct {
		name             string
		freshness        uci.QueryFreshness
		queryStatus      uci.QueryResponseStatus
		queryError       uci.QueryErrorCode
		hasQueryError    bool
		wantSearchCalls  int
		hasPending       bool
		wantPending      int64
		wantWatermarkSeq int64
		historicalView   bool
	}{
		{
			name:             "observed current",
			freshness:        uciFreshnessObservedCurrent(7),
			queryStatus:      uci.QueryStatusOK,
			wantSearchCalls:  1,
			hasPending:       true,
			wantPending:      0,
			wantWatermarkSeq: 7,
		},
		{
			name:             "catching up retains pending watermark",
			freshness:        uciFreshnessCatchingUp(3, 6),
			queryStatus:      uci.QueryStatusStale,
			wantSearchCalls:  1,
			hasPending:       true,
			wantPending:      3,
			wantWatermarkSeq: 6,
		},
		{
			name:             "offline is an authorized unavailable outcome",
			freshness:        uciFreshnessOffline(7),
			queryStatus:      uci.QueryStatusUnavailable,
			hasQueryError:    true,
			queryError:       uci.QueryErrorCheckoutOffline,
			wantSearchCalls:  1,
			wantWatermarkSeq: 7,
		},
		{
			name:             "pinned view remains historical",
			freshness:        uciFreshnessHistorical(6),
			queryStatus:      uci.QueryStatusOK,
			historicalView:   true,
			wantSearchCalls:  1,
			wantWatermarkSeq: 6,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCIFreshnessFixture(t)
			target := fixture.refA
			if test.historicalView {
				target.ViewID = "40000000-0000-4000-8000-000000000004"
				target.Generation = 6
				fixture.catalog.records[target.CheckoutID] = uci.ContextRecord{Ref: target, AuthRealm: uciCodebaseContextTestRealm}
				fixture.application.queryResponses[target.CheckoutID] = uciCodeIntelCompatibilityQueryResponse(t, target, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityExposureA, uciCodeIntelCompatibilityDigestA)
			}
			handle := fixture.selectContext(t, fixture.clientA, target)
			fixture.application.setPlan(target, "", uciFreshnessTestPlan{freshness: test.freshness})
			if test.queryStatus == uci.QueryStatusUnavailable {
				response := fixture.application.queryResponses[target.CheckoutID]
				emptyItems := uci.QueryItems{}
				response.Status = uci.QueryStatusUnavailable
				response.Error = &uci.QueryError{Code: uci.QueryErrorCheckoutOffline}
				response.Items = &emptyItems
				fixture.application.queryResponses[target.CheckoutID] = response
			}

			status := requireUCIFreshnessStatus(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
				"context_handle": handle,
			}), target, test.freshness)
			assert.Equal(t, int64(17), status.TotalChunks)
			assert.Equal(t, int64(11), status.EmbeddedChunks)
			assert.Equal(t, CodebaseEvidenceRecorderHealth{State: "healthy", LastFailureCode: "NONE"}, status.EvidenceRecorder)

			response := requireUCIFreshnessQuery(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciFreshnessSearchArguments(handle, nil)), target, test.freshness, test.queryStatus, test.hasQueryError, test.queryError)
			if test.queryStatus == uci.QueryStatusUnavailable {
				require.NotNil(t, response.Items)
				assert.Empty(t, *response.Items, "offline requests must not expose an old View as current")
			}

			requireUCIFreshnessValue(t, response.Freshness, test.freshness)
			if !test.hasPending {
				require.Nil(t, response.Freshness.PendingChanges)
			} else {
				require.NotNil(t, response.Freshness.PendingChanges)
				assert.Equal(t, test.wantPending, *response.Freshness.PendingChanges)
			}
			assert.Equal(t, test.wantWatermarkSeq, response.Freshness.EnrichmentWatermark.Sequence)

			freshnessCalls := fixture.application.freshnessCalls()
			require.Len(t, freshnessCalls, 2, "status and search must each ask the authorized freshness application")
			for _, call := range freshnessCalls {
				assert.Equal(t, target, call.ref)
				assert.Empty(t, call.token)
				assert.False(t, call.hasDeadline, "a non-barrier request must not create a wait")
			}
			require.Len(t, fixture.application.statusCalls, 1)
			assert.Equal(t, target, fixture.application.statusCalls[0])
			require.Len(t, fixture.application.searchCalls, test.wantSearchCalls)
			for _, call := range fixture.application.searchCalls {
				assert.Equal(t, target, call.ref, "the query store may only receive the authorized View")
			}
		})
	}
}

func TestUCIFreshnessAfterBarrierStaysOnAuthorizedCheckout(t *testing.T) {
	fixture := newUCIFreshnessFixture(t)
	handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
	fixture.selectContext(t, fixture.clientB, fixture.refB)

	freshA := uciFreshnessBarrierSatisfied(8, 25)
	freshB := uciFreshnessCatchingUp(4, 11)
	fixture.application.setPlan(fixture.refA, uciFreshnessBarrierA, uciFreshnessTestPlan{freshness: freshA})
	fixture.application.setPlan(fixture.refB, uciFreshnessBarrierA, uciFreshnessTestPlan{freshness: freshB})

	started := time.Now()
	status := requireUCIFreshnessStatus(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", uciFreshnessStatusArguments(handleA, uciFreshnessBarrierA, 25)), fixture.refA, freshA)
	assert.Equal(t, int64(17), status.TotalChunks)

	search := requireUCIFreshnessQuery(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciFreshnessSearchArguments(handleA, uciFreshnessBarrierArguments(uciFreshnessBarrierA, 25))), fixture.refA, freshA, uci.QueryStatusOK, false, "")
	require.Len(t, *search.Items, 1)
	assert.NotContains(t, (*search.Items)[0].Excerpt, uciCodeIntelCompatibilityBodyB)

	freshnessCalls := fixture.application.freshnessCalls()
	require.Len(t, freshnessCalls, 2)
	for _, call := range freshnessCalls {
		assert.Equal(t, fixture.refA, call.ref, "a barrier for A must never wait on B or a latest checkout")
		assert.Equal(t, uciFreshnessBarrierA, call.token)
		require.True(t, call.hasDeadline, "after_barrier must be deadline-bounded")
		assert.False(t, call.deadline.After(started.Add(150*time.Millisecond)), "caller-selected barrier wait must bound the application context")
	}
	require.Len(t, fixture.application.statusCalls, 1)
	assert.Equal(t, fixture.refA, fixture.application.statusCalls[0])
	require.Len(t, fixture.application.searchCalls, 1)
	for _, call := range fixture.application.searchCalls {
		assert.Equal(t, fixture.refA, call.ref)
	}
}

func TestUCIFreshnessAfterBarrierTimeoutUsesBoundedStaleResult(t *testing.T) {
	fixture := newUCIFreshnessFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	timedOut := uciFreshnessBarrierTimedOut(2, 6, 50)
	fixture.application.setPlan(fixture.refA, uciFreshnessBarrierA, uciFreshnessTestPlan{
		freshness:          timedOut,
		waitForContextDone: true,
	})

	parent, cancel := context.WithTimeout(fixture.clientA, 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	response := requireUCIFreshnessQuery(t, callUCICodeIntel(t, fixture.server, parent, "codebase_search", uciFreshnessSearchArguments(handle, uciFreshnessBarrierArguments(uciFreshnessBarrierA, 50))), fixture.refA, timedOut, uci.QueryStatusStale, false, "")
	assert.Less(t, time.Since(started), 250*time.Millisecond, "the requested barrier deadline must win over a longer caller deadline")
	requireUCIFreshnessValue(t, response.Freshness, timedOut)

	freshnessCalls := fixture.application.freshnessCalls()
	require.Len(t, freshnessCalls, 1)
	assert.Equal(t, fixture.refA, freshnessCalls[0].ref)
	assert.Equal(t, uciFreshnessBarrierA, freshnessCalls[0].token)
	require.True(t, freshnessCalls[0].hasDeadline)
	assert.False(t, freshnessCalls[0].deadline.After(started.Add(150*time.Millisecond)))
}

func TestUCIFreshnessBarrierHonorsCallerCancellationAndDeadline(t *testing.T) {
	t.Run("caller cancellation stops the authorized wait", func(t *testing.T) {
		fixture := newUCIFreshnessFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		started := make(chan struct{}, 1)
		fixture.application.setPlan(fixture.refA, uciFreshnessBarrierA, uciFreshnessTestPlan{
			waitForContextDone: true,
			returnContextError: true,
			started:            started,
		})

		ctx, cancel := context.WithCancel(fixture.clientA)
		defer cancel()
		params := uciFreshnessToolCallParams(t, "codebase_status", uciFreshnessStatusArguments(handle, uciFreshnessBarrierA, 250))
		responses := make(chan *Response, 1)
		go func() {
			responses <- fixture.server.HandleRequest(ctx, &Request{
				JSONRPC: "2.0",
				ID:      float64(1),
				Method:  "tools/call",
				Params:  params,
			})
		}()

		awaitUCIFreshnessSignal(t, started, "barrier wait start")
		cancel()
		select {
		case response := <-responses:
			require.NotNil(t, response)
			require.NotNil(t, response.Error, "caller cancellation must not become a successful freshness result")
			assert.Contains(t, strings.ToLower(response.Error.Message), "cancel")
		case <-time.After(time.Second):
			t.Fatal("cancelled after_barrier did not return")
		}
	})

	t.Run("earlier caller deadline clamps the requested maximum", func(t *testing.T) {
		fixture := newUCIFreshnessFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		fixture.application.setPlan(fixture.refA, uciFreshnessBarrierA, uciFreshnessTestPlan{
			freshness: uciFreshnessBarrierSatisfied(8, 100),
		})

		parent, cancel := context.WithTimeout(fixture.clientA, 100*time.Millisecond)
		defer cancel()
		parentDeadline, ok := parent.Deadline()
		require.True(t, ok)
		requireUCIFreshnessStatus(t, callUCICodeIntel(t, fixture.server, parent, "codebase_status", uciFreshnessStatusArguments(handle, uciFreshnessBarrierA, 60_000)), fixture.refA, uciFreshnessBarrierSatisfied(8, 100))

		freshnessCalls := fixture.application.freshnessCalls()
		require.Len(t, freshnessCalls, 1)
		require.True(t, freshnessCalls[0].hasDeadline)
		assert.False(t, freshnessCalls[0].deadline.After(parentDeadline), "the application must never outlive the caller deadline")
	})

	t.Run("oversized and client-sequence barrier input are rejected before any wait", func(t *testing.T) {
		fixture := newUCIFreshnessFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)

		oversized := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", uciFreshnessStatusArguments(handle, uciFreshnessBarrierA, 60_001))
		require.NotNil(t, oversized.Error)
		require.Nil(t, oversized.Result)

		clientSequence := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
			"context_handle": handle,
			"after_barrier": map[string]any{
				"token":    uciFreshnessBarrierA,
				"wait_ms":  25,
				"sequence": 9_007_199_254_740_991,
			},
		})
		require.NotNil(t, clientSequence.Error)
		require.Nil(t, clientSequence.Result)
		assert.Empty(t, fixture.application.freshnessCalls(), "client sequence values cannot reach the freshness application")
		assert.Empty(t, fixture.application.statusCalls)
	})
}

func TestUCIFreshnessDeniesForeignStaleAndEpochChangedBarriersBeforeStoreAccess(t *testing.T) {
	t.Run("foreign client handle is denied before a barrier wait", func(t *testing.T) {
		fixture := newUCIFreshnessFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		fixture.application.setPlan(fixture.refA, uciFreshnessBarrierA, uciFreshnessTestPlan{freshness: uciFreshnessBarrierSatisfied(8, 25)})

		response := callUCICodeIntel(t, fixture.server, fixture.clientC, "codebase_status", uciFreshnessStatusArguments(handle, uciFreshnessBarrierA, 25))
		requireUCICodeIntelSafeToolError(t, response, "CONTEXT_MISMATCH", fixture.uciCodeIntelCompatibilityFixture)
		assert.Empty(t, fixture.application.freshnessCalls(), "foreign handles must fail before waiting")
		assert.Empty(t, fixture.application.statusCalls, "foreign handles must fail before scoped status storage")
	})

	t.Run("recreated checkout retires the old handle before a barrier wait", func(t *testing.T) {
		fixture := newUCIFreshnessFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		recreated := fixture.refA
		recreated.ViewID = "40000000-0000-4000-8000-000000000003"
		recreated.AnalysisProfileID = "50000000-0000-4000-8000-000000000003"
		recreated.Generation++
		fixture.catalog.records[fixture.refA.CheckoutID] = uci.ContextRecord{Ref: recreated, AuthRealm: uciCodebaseContextTestRealm}
		fixture.application.setPlan(fixture.refA, uciFreshnessBarrierA, uciFreshnessTestPlan{freshness: uciFreshnessBarrierSatisfied(8, 25)})

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", uciFreshnessStatusArguments(handle, uciFreshnessBarrierA, 25))
		requireUCICodeIntelSafeToolError(t, response, "CONTEXT_MISMATCH", fixture.uciCodeIntelCompatibilityFixture)
		assert.Empty(t, fixture.application.freshnessCalls(), "a retired View must not wait against a recreated incarnation")
		assert.Empty(t, fixture.application.statusCalls)
	})

	t.Run("adapter epoch change during a barrier suppresses the old view", func(t *testing.T) {
		fixture := newUCIFreshnessFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		fixture.application.setPlan(fixture.refA, uciFreshnessBarrierA, uciFreshnessTestPlan{
			freshness: uciFreshnessBarrierSatisfied(8, 25),
			afterStart: func() {
				fixture.server.SetCodebaseContextApplication(fixture.application)
			},
		})

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciFreshnessSearchArguments(handle, uciFreshnessBarrierArguments(uciFreshnessBarrierA, 25)))
		requireUCICodeIntelSuppressedQueryError(t, response, "CONTEXT_MISMATCH", fixture.uciCodeIntelCompatibilityFixture)
		require.Len(t, fixture.application.freshnessCalls(), 1)
		assert.Empty(t, fixture.application.searchCalls, "a changed application epoch must suppress the old-view query")
	})
}

func uciFreshnessSearchArguments(handle string, afterBarrier map[string]any) map[string]any {
	arguments := map[string]any{
		"context_handle": handle,
		"query":          uciCodeIntelCompatibilityQuery,
		"path_prefix":    uciCodeIntelCompatibilityPathPrefix,
		"limit":          10,
	}
	if afterBarrier != nil {
		arguments["after_barrier"] = afterBarrier
	}
	return arguments
}

func uciFreshnessStatusArguments(handle, token string, waitMS int64) map[string]any {
	return map[string]any{
		"context_handle": handle,
		"after_barrier":  uciFreshnessBarrierArguments(token, waitMS),
	}
}

func uciFreshnessBarrierArguments(token string, waitMS int64) map[string]any {
	return map[string]any{
		"token":   token,
		"wait_ms": waitMS,
	}
}

func requireUCIFreshnessStatus(t *testing.T, response *Response, wantRef uci.ContextRef, want uci.QueryFreshness) uciFreshnessStatusPayload {
	t.Helper()
	text := uciCodeIntelToolText(t, response)
	var payload uciFreshnessStatusPayload
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	assert.Equal(t, wantRef.SourceID, payload.Context.SourceID)
	assert.Equal(t, wantRef.CheckoutID, payload.Context.CheckoutID)
	assert.Equal(t, wantRef.ViewID, payload.Context.ViewID)
	assert.Equal(t, wantRef.Generation, payload.Context.Generation)
	assert.Equal(t, wantRef.AnalysisProfileID, payload.Context.ProfileID)
	requireUCIFreshnessValue(t, &payload.Freshness, want)
	assert.NotContains(t, text, `"project"`, "status must not echo raw project authority")
	assert.NotContains(t, text, "legacy_unscoped")
	return payload
}

func requireUCIFreshnessQuery(t *testing.T, response *Response, wantRef uci.ContextRef, wantFreshness uci.QueryFreshness, wantStatus uci.QueryResponseStatus, hasWantError bool, wantError uci.QueryErrorCode) uci.QueryResponse {
	t.Helper()
	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate(), "search must retain the closed UCI QueryResponse contract")
	assert.Equal(t, wantStatus, payload.Status)
	require.NotNil(t, payload.Contexts)
	require.Len(t, *payload.Contexts, 1)
	contextRef := (*payload.Contexts)[0]
	assert.Equal(t, wantRef.SourceID, contextRef.SourceID)
	assert.Equal(t, wantRef.CheckoutID, contextRef.CheckoutID)
	assert.Equal(t, wantRef.ViewID, contextRef.ViewID)
	assert.Equal(t, wantRef.Generation, contextRef.Generation)
	assert.Equal(t, wantRef.AnalysisProfileID, contextRef.ProfileID)
	requireUCIFreshnessValue(t, payload.Freshness, wantFreshness)
	if !hasWantError {
		assert.Nil(t, payload.Error)
	} else {
		require.NotNil(t, payload.Error)
		assert.Equal(t, wantError, payload.Error.Code)
	}
	assert.NotContains(t, text, `"project"`, "search must not echo raw project authority")
	assert.NotContains(t, text, "legacy_unscoped")
	return payload
}

func requireUCIFreshnessValue(t *testing.T, got *uci.QueryFreshness, want uci.QueryFreshness) {
	t.Helper()
	require.NotNil(t, got)
	require.NoError(t, got.Validate(), "freshness must use the existing closed UCI DTO vocabulary")
	assert.Equal(t, want.State, got.State)
	assert.Equal(t, want.Method, got.Method)
	assert.Equal(t, want.EnrichmentWatermark, got.EnrichmentWatermark)
	if want.PendingChanges == nil {
		assert.Nil(t, got.PendingChanges)
	} else {
		require.NotNil(t, got.PendingChanges)
		assert.Equal(t, *want.PendingChanges, *got.PendingChanges)
	}
	if want.Barrier == nil {
		assert.Nil(t, got.Barrier)
	} else {
		require.NotNil(t, got.Barrier)
		assert.Equal(t, *want.Barrier, *got.Barrier)
	}
}

func uciFreshnessObservedCurrent(sequence int64) uci.QueryFreshness {
	return uci.QueryFreshness{
		State:          uci.QueryFreshnessObservedCurrent,
		Method:         uci.QueryFreshnessWatchWatermark,
		PendingChanges: new(int64),
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: sequence,
			State:    uci.QueryEnrichmentCurrent,
		},
	}
}

func uciFreshnessCatchingUp(pending, sequence int64) uci.QueryFreshness {
	pendingChanges := new(int64)
	*pendingChanges = pending
	return uci.QueryFreshness{
		State:          uci.QueryFreshnessCatchingUp,
		Method:         uci.QueryFreshnessWatchWatermark,
		PendingChanges: pendingChanges,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: sequence,
			State:    uci.QueryEnrichmentPending,
		},
	}
}

func uciFreshnessOffline(sequence int64) uci.QueryFreshness {
	return uci.QueryFreshness{
		State:          uci.QueryFreshnessOffline,
		Method:         uci.QueryFreshnessNone,
		PendingChanges: nil,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: sequence,
			State:    uci.QueryEnrichmentUnavailable,
		},
	}
}

func uciFreshnessHistorical(sequence int64) uci.QueryFreshness {
	return uci.QueryFreshness{
		State:          uci.QueryFreshnessHistorical,
		Method:         uci.QueryFreshnessPinnedHistory,
		PendingChanges: nil,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: sequence,
			State:    uci.QueryEnrichmentCurrent,
		},
	}
}

func uciFreshnessBarrierSatisfied(sequence, deadlineMS int64) uci.QueryFreshness {
	freshness := uciFreshnessObservedCurrent(sequence)
	freshness.Method = uci.QueryFreshnessPathHashBarrier
	freshness.Barrier = &uci.QueryBarrier{
		Scope:      uci.QueryBarrierScope{Kind: uci.QueryBarrierPathsWithHashes, PathCount: 1},
		DeadlineMS: deadlineMS,
		State:      uci.QueryBarrierSatisfied,
	}
	return freshness
}

func uciFreshnessBarrierTimedOut(pending, sequence, deadlineMS int64) uci.QueryFreshness {
	freshness := uciFreshnessCatchingUp(pending, sequence)
	freshness.Method = uci.QueryFreshnessPathHashBarrier
	freshness.Barrier = &uci.QueryBarrier{
		Scope:      uci.QueryBarrierScope{Kind: uci.QueryBarrierPathsWithHashes, PathCount: 1},
		DeadlineMS: deadlineMS,
		State:      uci.QueryBarrierTimedOut,
	}
	return freshness
}

func uciFreshnessToolCallParams(t *testing.T, name string, arguments map[string]any) json.RawMessage {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	require.NoError(t, err)
	return params
}

func awaitUCIFreshnessSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
