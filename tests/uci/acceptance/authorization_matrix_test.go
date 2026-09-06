package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciAuthorizationMatrixSpaceID    = "71000000-0000-4000-8000-000000000001"
	uciAuthorizationMatrixSourceA    = "72000000-0000-4000-8000-000000000001"
	uciAuthorizationMatrixSourceB    = "72000000-0000-4000-8000-000000000002"
	uciAuthorizationMatrixCheckoutA  = "73000000-0000-4000-8000-000000000001"
	uciAuthorizationMatrixCheckoutB  = "73000000-0000-4000-8000-000000000002"
	uciAuthorizationMatrixViewA      = "74000000-0000-4000-8000-000000000001"
	uciAuthorizationMatrixViewB      = "74000000-0000-4000-8000-000000000002"
	uciAuthorizationMatrixProfileID  = "75000000-0000-4000-8000-000000000001"
	uciAuthorizationMatrixRawQuery   = "authorization-query-must-never-be-recorded"
	uciAuthorizationMatrixRawSource  = "authorization-source-body-must-never-be-recorded"
	uciAuthorizationMatrixRawPath    = "sensitive/authorization-path-must-never-be-recorded.go"
	uciAuthorizationMatrixLocator    = `D:\private\authorization-locator-must-never-be-recorded`
	uciAuthorizationMatrixSearchKey  = "fixture.AuthorizationSearch"
	uciAuthorizationMatrixReadKey    = "fixture.AuthorizationRead"
	uciAuthorizationMatrixGraphKey   = "fixture.AuthorizationGraph"
	uciAuthorizationMatrixBarrierKey = "authorization-barrier"
)

func TestUCIAuthorizationMatrix(t *testing.T) {
	t.Run("authorized search graph and read append one scoped non-content exposure", func(t *testing.T) {
		for _, test := range []struct {
			name      string
			operation string
			mode      uci.QueryRetrievalMode
		}{
			{name: "search", operation: "search", mode: uci.QueryRetrievalLexical},
			{name: "graph", operation: "graph", mode: uci.QueryRetrievalGraph},
			{name: "read", operation: "read", mode: uci.QueryRetrievalExact},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				fixture := newUCIAuthorizationMatrixFixture(t)
				handle := fixture.selectContext(t, fixture.clientA, fixture.refA)

				payload := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "authorized-"+test.operation, test.operation, handle))
				if payload.Status != uci.QueryStatusOK {
					t.Fatalf("%s status = %q, want ok", test.operation, payload.Status)
				}
				fixture.requireBoundContext(t, payload, fixture.refA)
				if payload.Retrieval == nil || payload.Retrieval.Mode != test.mode {
					t.Fatalf("%s retrieval = %#v, want %q", test.operation, payload.Retrieval, test.mode)
				}
				fixture.requireUnknownExposure(t, payload)
				fixture.requireScopedExposure(t, fixture.refA, payload)
				fixture.requireCallerIdentity(t, "principal-a")
				fixture.requireOperationCalls(t, test.operation, 1)
				if got := fixture.store.completionCount(); got != 0 {
					t.Fatalf("completion rows = %d, want 0 without a verified callback", got)
				}

				if test.operation == "graph" {
					fixture.requireAllGraphRefsBound(t, payload, fixture.refA)
				}
			})
		}
	})

	t.Run("authorized unavailable retains its closed contextual result and records once", func(t *testing.T) {
		fixture := newUCIAuthorizationMatrixFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		fixture.application.setResponse("search", uciAuthorizationUnavailableResponse(t, fixture.refA))

		payload := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "authorized-unavailable", "search", handle))
		if payload.Status != uci.QueryStatusUnavailable {
			t.Fatalf("status = %q, want unavailable", payload.Status)
		}
		if payload.Error == nil || payload.Error.Code != uci.QueryErrorCheckoutOffline {
			t.Fatalf("unavailable error = %#v, want CHECKOUT_OFFLINE", payload.Error)
		}
		fixture.requireBoundContext(t, payload, fixture.refA)
		if payload.Items == nil || len(*payload.Items) != 0 {
			t.Fatalf("authorized unavailable items = %#v, want explicit empty items", payload.Items)
		}
		fixture.requireUnknownExposure(t, payload)
		fixture.requireScopedExposure(t, fixture.refA, payload)
		fixture.requireRecorderHealth(t, fixture.clientA, handle, uci.ExposureHealthHealthy, uci.ExposureHealthFailureNone)
	})

	t.Run("denial resolution and epoch checks prevent candidate read graph and exposure work", func(t *testing.T) {
		t.Run("revoked authorization blocks every operation", func(t *testing.T) {
			for _, operation := range []string{"search", "graph", "read"} {
				operation := operation
				t.Run(operation, func(t *testing.T) {
					fixture := newUCIAuthorizationMatrixFixture(t)
					handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
					fixture.authorizer.setAllowed("principal-a", fixture.refA.CheckoutID, false)

					response := fixture.invoke(t, fixture.clientA, "revoked-"+operation, operation, handle)
					fixture.requireSuppressedQuery(t, response, uci.QueryStatusForbidden, uci.QueryErrorPermissionDenied)
					fixture.requireOperationCalls(t, operation, 0)
					fixture.requireNoExposureAttempts(t)
				})
			}
		})

		t.Run("private checkout is refused before any tool operation", func(t *testing.T) {
			fixture := newUCIAuthorizationMatrixFixture(t)
			response := fixture.call(t, fixture.clientA, "private-select", "codebase_context", fixture.contextSelectionArguments(fixture.refB))
			fixture.requireToolError(t, response, string(uci.PermissionDenied))
			fixture.requireAllOperationCalls(t, 0)
			fixture.requireNoExposureAttempts(t)
		})

		t.Run("cross source or view selectors are refused before graph and read", func(t *testing.T) {
			for _, operation := range []string{"graph", "read"} {
				operation := operation
				t.Run(operation, func(t *testing.T) {
					fixture := newUCIAuthorizationMatrixFixture(t)
					handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
					response := fixture.call(t, fixture.clientA, "cross-"+operation, "codebase_"+operation, fixture.operationArguments(operation, handle, fixture.refB, nil))
					fixture.requireSuppressedQuery(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
					fixture.requireOperationCalls(t, operation, 0)
					fixture.requireNoExposureAttempts(t)
				})
			}
		})

		t.Run("a different client cannot reuse an opaque handle", func(t *testing.T) {
			fixture := newUCIAuthorizationMatrixFixture(t)
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			response := fixture.invoke(t, fixture.clientB, "cross-client-handle", "search", handle)
			fixture.requireSuppressedQuery(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
			fixture.requireOperationCalls(t, "search", 0)
			fixture.requireNoExposureAttempts(t)
		})

		t.Run("ambiguous selection never borrows a candidate", func(t *testing.T) {
			fixture := newUCIAuthorizationMatrixFixture(t)
			response := fixture.call(t, fixture.clientC, "ambiguous-search", "codebase_search", fixture.operationArguments("search", "", fixture.refA, nil))
			fixture.requireSuppressedQuery(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextRequired)
			fixture.requireOperationCalls(t, "search", 0)
			fixture.requireNoExposureAttempts(t)
		})

		t.Run("epoch change after authorization blocks every operation before release", func(t *testing.T) {
			for _, operation := range []string{"search", "graph", "read"} {
				operation := operation
				t.Run(operation, func(t *testing.T) {
					fixture := newUCIAuthorizationMatrixFixture(t)
					handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
					fixture.application.setAfterResolve(func() {
						fixture.server.SetCodebaseContextApplication(fixture.application)
					})

					response := fixture.invoke(t, fixture.clientA, "epoch-"+operation, operation, handle)
					fixture.requireSuppressedQuery(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch)
					fixture.requireOperationCalls(t, operation, 0)
					fixture.requireNoExposureAttempts(t)
				})
			}
		})

		t.Run("revocation after operation suppresses release and exposure", func(t *testing.T) {
			for _, operation := range []string{"search", "graph", "read"} {
				operation := operation
				t.Run(operation, func(t *testing.T) {
					fixture := newUCIAuthorizationMatrixFixture(t)
					handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
					fixture.application.setAfterOperation(func() {
						fixture.authorizer.setAllowed("principal-a", fixture.refA.CheckoutID, false)
					})

					response := fixture.invoke(t, fixture.clientA, "post-operation-revocation-"+operation, operation, handle)
					fixture.requireSuppressedQuery(t, response, uci.QueryStatusForbidden, uci.QueryErrorPermissionDenied)
					fixture.requireOperationCalls(t, operation, 1)
					fixture.requireNoExposureAttempts(t)
				})
			}
		})
	})

	t.Run("historical pagination cache retry and every graph hop remain scoped", func(t *testing.T) {
		t.Run("pinned historical result is independently authorized and recorded", func(t *testing.T) {
			fixture := newUCIAuthorizationMatrixFixture(t)
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			fixture.application.setResponse("search", uciAuthorizationSearchResponse(t, fixture.refA, true))

			payload := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "historical", "search", handle))
			if payload.Freshness == nil || payload.Freshness.State != uci.QueryFreshnessHistorical || payload.Freshness.Method != uci.QueryFreshnessPinnedHistory {
				t.Fatalf("historical freshness = %#v, want pinned historical freshness", payload.Freshness)
			}
			fixture.requireScopedExposure(t, fixture.refA, payload)
		})

		t.Run("pagination reauthorizes each graph page and records each distinct request", func(t *testing.T) {
			fixture := newUCIAuthorizationMatrixFixture(t)
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			continuation := "authorization-next-page"
			fixture.application.setGraphResponder(func(_ context.Context, _ uci.AuthorizedContext, input mcp.CodebaseGraphInput) (uci.QueryResponse, error) {
				if input.Continuation == nil {
					return uciAuthorizationGraphResponse(t, fixture.refA, true, &continuation), nil
				}
				return uciAuthorizationGraphResponse(t, fixture.refA, false, nil), nil
			})

			first := fixture.requireQueryResponse(t, fixture.call(t, fixture.clientA, "page-one", "codebase_graph", fixture.operationArguments("graph", handle, fixture.refA, nil)))
			if first.Truncated == nil || !*first.Truncated || first.Continuation == nil || first.Continuation.Value == nil {
				t.Fatalf("first graph page = %#v, want a continuation", first)
			}
			fixture.requireUnknownExposure(t, first)
			fixture.requireAllGraphRefsBound(t, first, fixture.refA)

			second := fixture.requireQueryResponse(t, fixture.call(t, fixture.clientA, "page-two", "codebase_graph", fixture.operationArguments("graph", handle, fixture.refA, &continuation)))
			if second.Truncated == nil || *second.Truncated {
				t.Fatalf("second graph page = %#v, want terminal page", second)
			}
			fixture.requireUnknownExposure(t, second)
			fixture.requireAllGraphRefsBound(t, second, fixture.refA)
			if got := fixture.store.exposureCount(); got != 2 {
				t.Fatalf("pagination exposure rows = %d, want 2", got)
			}
			fixture.requireEachExposureScoped(t, fixture.refA)
		})

		t.Run("exact cache-like retry reuses only its original receipt", func(t *testing.T) {
			fixture := newUCIAuthorizationMatrixFixture(t)
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)

			first := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "cache-retry", "search", handle))
			second := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "cache-retry", "search", handle))
			fixture.requireUnknownExposure(t, first)
			fixture.requireUnknownExposure(t, second)
			if first.Exposure.ExposureRef != second.Exposure.ExposureRef {
				t.Fatalf("exact retry exposure = %q, want original %q", second.Exposure.ExposureRef, first.Exposure.ExposureRef)
			}
			if got := fixture.store.exposureCount(); got != 1 {
				t.Fatalf("exact retry exposure rows = %d, want 1", got)
			}
			if got := fixture.store.exposureAttemptsCount(); got != 2 {
				t.Fatalf("exact retry append attempts = %d, want 2", got)
			}
		})

		t.Run("all graph node edge and evidence refs stay in the authorized view", func(t *testing.T) {
			fixture := newUCIAuthorizationMatrixFixture(t)
			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			payload := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "all-graph-hops", "graph", handle))
			fixture.requireAllGraphRefsBound(t, payload, fixture.refA)
			if payload.Graph == nil || len(payload.Graph.Edges) != len(uciAuthorizationRelations) {
				t.Fatalf("graph edges = %#v, want one edge for every declared relation", payload.Graph)
			}
			fixture.requireScopedExposure(t, fixture.refA, payload)
		})
	})

	t.Run("initial append failure suppresses every operation result", func(t *testing.T) {
		for _, operation := range []string{"search", "graph", "read"} {
			operation := operation
			t.Run(operation, func(t *testing.T) {
				fixture := newUCIAuthorizationMatrixFixture(t)
				handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
				fixture.store.setFailExposure(true)

				response := fixture.invoke(t, fixture.clientA, "append-failure-"+operation, operation, handle)
				fixture.requireSuppressedQuery(t, response, uci.QueryStatusUnavailable, uci.QueryErrorExposureUnavailable)
				fixture.requireOperationCalls(t, operation, 1)
				if got := fixture.store.exposureCount(); got != 0 {
					t.Fatalf("failed append exposure rows = %d, want 0", got)
				}
				fixture.requireRecorderHealth(t, fixture.clientA, handle, uci.ExposureHealthUnavailable, uci.ExposureHealthFailureExposureUnavailable)
			})
		}
	})

	t.Run("exposure idempotency mismatch never releases a stale receipt", func(t *testing.T) {
		fixture := newUCIAuthorizationMatrixFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)

		first := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "same-request", "search", handle))
		fixture.requireUnknownExposure(t, first)
		fixture.requireScopedExposure(t, fixture.refA, first)
		fixture.application.setResponse("search", uciAuthorizationUnavailableResponse(t, fixture.refA))

		mismatch := fixture.invoke(t, fixture.clientA, "same-request", "search", handle)
		fixture.requireSuppressedQuery(t, mismatch, uci.QueryStatusUnavailable, uci.QueryErrorIdempotencyMismatch)
		if got := fixture.store.exposureCount(); got != 1 {
			t.Fatalf("mismatch exposure rows = %d, want original row only", got)
		}
		if got := fixture.store.exposureAttemptsCount(); got != 2 {
			t.Fatalf("mismatch append attempts = %d, want 2", got)
		}
		fixture.requireRecorderHealth(t, fixture.clientA, handle, uci.ExposureHealthHealthy, uci.ExposureHealthFailureNone)
	})

	t.Run("completion remains unknown without a callback and only a verified partial callback appends", func(t *testing.T) {
		fixture := newUCIAuthorizationMatrixFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		payload := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "completion-parent", "search", handle))
		fixture.requireUnknownExposure(t, payload)
		fixture.requireScopedExposure(t, fixture.refA, payload)

		callback := uciAuthorizationCallback(payload.Exposure.ExposureRef, "completion-exact")
		if err := fixture.server.RecordUCICompletion(fixture.clientA, callback); err != nil {
			t.Fatalf("verified partial callback: %v", err)
		}
		if err := fixture.server.RecordUCICompletion(fixture.clientA, callback); err != nil {
			t.Fatalf("exact completion retry: %v", err)
		}
		fixture.requireOneCompletion(t, payload.Exposure.ExposureRef, "partial")
		if got := fixture.store.completionAttemptsCount(); got != 2 {
			t.Fatalf("completion append attempts = %d, want 2", got)
		}
		if payload.Exposure.CompletionState != uci.QueryCompletionUnknown {
			t.Fatalf("original query completion state = %q, want unknown", payload.Exposure.CompletionState)
		}

		before := fixture.health.Snapshot()
		mismatch := callback
		mismatch.Outcome = "failed"
		err := fixture.server.RecordUCICompletion(fixture.clientA, mismatch)
		if !errors.Is(err, uci.ErrIdempotencyMismatch) {
			t.Fatalf("completion mismatch error = %v, want IDEMPOTENCY_MISMATCH", err)
		}
		if got := fixture.store.completionCount(); got != 1 {
			t.Fatalf("completion mismatch rows = %d, want 1", got)
		}
		if after := fixture.health.Snapshot(); after != before {
			t.Fatalf("completion mismatch health = %#v, want unchanged %#v", after, before)
		}
		fixture.requireClosedNoDisclosure(t, err.Error())
	})

	t.Run("completion append failure changes only callback health and preserves parent", func(t *testing.T) {
		fixture := newUCIAuthorizationMatrixFixture(t)
		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		payload := fixture.requireQueryResponse(t, fixture.invoke(t, fixture.clientA, "completion-failure-parent", "search", handle))
		parentBefore := fixture.store.exposureJSON(t, 0)
		fixture.store.setFailCompletion(true)

		err := fixture.server.RecordUCICompletion(fixture.clientA, uciAuthorizationCallback(payload.Exposure.ExposureRef, "completion-failure"))
		if err == nil || !strings.Contains(err.Error(), "COMPLETION_EVIDENCE_UNAVAILABLE") {
			t.Fatalf("completion append failure = %v, want COMPLETION_EVIDENCE_UNAVAILABLE", err)
		}
		fixture.requireClosedNoDisclosure(t, err.Error())
		if got := fixture.store.completionCount(); got != 0 {
			t.Fatalf("failed completion rows = %d, want 0", got)
		}
		if got := fixture.store.exposureJSON(t, 0); got != parentBefore {
			t.Fatalf("completion failure altered the parent exposure")
		}
		fixture.requireRecorderHealth(t, fixture.clientA, handle, uci.ExposureHealthDegraded, uci.ExposureHealthFailureCompletionEvidenceUnavailable)
	})

	t.Run("bounded after barrier timeout and cancellation reach neither application nor recorder", func(t *testing.T) {
		for _, test := range []struct {
			name      string
			operation string
			cancel    bool
			want      string
		}{
			{name: "search timeout", operation: "search", want: context.DeadlineExceeded.Error()},
			{name: "graph timeout", operation: "graph", want: context.DeadlineExceeded.Error()},
			{name: "search cancellation", operation: "search", cancel: true, want: context.Canceled.Error()},
			{name: "graph cancellation", operation: "graph", cancel: true, want: context.Canceled.Error()},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				fixture := newUCIAuthorizationMatrixFixture(t)
				handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
				fixture.application.setBlockBarrier(true)
				ctx := fixture.clientA
				if test.cancel {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				arguments := fixture.operationArguments(test.operation, handle, fixture.refA, nil)
				arguments["after_barrier"] = map[string]any{"token": uciAuthorizationMatrixBarrierKey, "wait_ms": int64(1)}

				response := fixture.call(t, ctx, "barrier-"+strings.ReplaceAll(test.name, " ", "-"), "codebase_"+test.operation, arguments)
				if response.Error == nil || response.Result != nil || !strings.Contains(response.Error.Message, test.want) {
					t.Fatalf("after_barrier response = %#v, want tool error containing %q", response, test.want)
				}
				fixture.requireClosedNoDisclosure(t, response.Error.Message)
				fixture.requireOperationCalls(t, test.operation, 0)
				fixture.requireNoExposureAttempts(t)
			})
		}
	})
}

type uciAuthorizationMatrixFixture struct {
	server      *mcp.Server
	application *uciAuthorizationMatrixApplication
	store       *uciAuthorizationExposureStore
	health      *uci.ExposureHealthController
	authorizer  *uciAuthorizationMatrixAuthorizer
	refA        uci.ContextRef
	refB        uci.ContextRef
	clientA     context.Context
	clientB     context.Context
	clientC     context.Context
}

func newUCIAuthorizationMatrixFixture(t *testing.T) *uciAuthorizationMatrixFixture {
	t.Helper()
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	refA := uciAuthorizationContextRef(uciAuthorizationMatrixSourceA, uciAuthorizationMatrixCheckoutA, uciAuthorizationMatrixViewA, 7)
	refB := uciAuthorizationContextRef(uciAuthorizationMatrixSourceB, uciAuthorizationMatrixCheckoutB, uciAuthorizationMatrixViewB, 11)
	catalog := &uciAuthorizationMatrixCatalog{records: map[string]uci.ContextRecord{
		refA.CheckoutID: {Ref: refA, AuthRealm: string(auth.SourceClient)},
		refB.CheckoutID: {Ref: refB, AuthRealm: string(auth.SourceClient)},
	}}
	authorizer := &uciAuthorizationMatrixAuthorizer{allowed: map[string]map[string]bool{
		"principal-a": {refA.CheckoutID: true},
		"principal-b": {refB.CheckoutID: true},
	}}
	health := uci.NewExposureHealthController(true)
	store := &uciAuthorizationExposureStore{}
	application := &uciAuthorizationMatrixApplication{
		catalog:    catalog,
		authorizer: authorizer,
		candidates: map[string][]uci.ContextRef{
			"authorization-client-a": {refA, refB},
			"authorization-client-b": {refA, refB},
			"authorization-client-c": {refA, refB},
		},
		metadata: map[string]map[string]string{
			refA.CheckoutID: {
				"source":          "fixture-source-a",
				"checkout":        "fixture-checkout-a",
				"view":            "fixture-view-a",
				"private_locator": uciAuthorizationMatrixLocator,
			},
			refB.CheckoutID: {
				"source":          "fixture-source-b",
				"checkout":        "fixture-checkout-b",
				"view":            "fixture-view-b",
				"private_locator": uciAuthorizationMatrixLocator,
			},
		},
		responses: map[string]uci.QueryResponse{
			"search": uciAuthorizationSearchResponse(t, refA, false),
			"graph":  uciAuthorizationGraphResponse(t, refA, false, nil),
			"read":   uciAuthorizationReadResponse(t, refA),
		},
		health: health,
	}
	application.resolver = uci.NewContextResolver(catalog, authorizer, nil)

	server := mcp.NewServer(mcp.ServerOptions{Version: "uci-authorization-matrix"})
	server.SetCodebaseContextApplication(application)
	server.SetUCIExposureRecorder(uci.NewExposureRecorder(store, health))

	return &uciAuthorizationMatrixFixture{
		server:      server,
		application: application,
		store:       store,
		health:      health,
		authorizer:  authorizer,
		refA:        refA,
		refB:        refB,
		clientA:     uciAuthorizationClient("authorization-client-a", "authorization-keycard-a", "principal-a"),
		clientB:     uciAuthorizationClient("authorization-client-b", "authorization-keycard-b", "principal-b"),
		clientC:     uciAuthorizationClient("authorization-client-c", "authorization-keycard-c", "principal-c"),
	}
}

func uciAuthorizationContextRef(sourceID, checkoutID, viewID string, generation int64) uci.ContextRef {
	spaceID := uciAuthorizationMatrixSpaceID
	return uci.ContextRef{
		SpaceID:           &spaceID,
		SourceID:          sourceID,
		CheckoutID:        checkoutID,
		ViewID:            viewID,
		AnalysisProfileID: uciAuthorizationMatrixProfileID,
		Generation:        generation,
	}
}

func uciAuthorizationClient(sessionID, keycardID, principal string) context.Context {
	identity := auth.ClientWithPrincipal("read-write", keycardID, principal, auth.PrincipalKindHuman)
	return mcp.ContextWithSession(auth.WithIdentity(context.Background(), identity), sessionID)
}

func (fixture *uciAuthorizationMatrixFixture) selectContext(t *testing.T, ctx context.Context, ref uci.ContextRef) string {
	t.Helper()
	response := fixture.call(t, ctx, "select-"+ref.CheckoutID, "codebase_context", fixture.contextSelectionArguments(ref))
	text := fixture.toolText(t, response)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode context selection: %v", err)
	}
	handle, ok := payload["context_handle"].(string)
	if !ok || handle == "" {
		t.Fatalf("context selection payload = %#v, want opaque context handle", payload)
	}
	fixture.requireOpaqueContextHandle(t, handle)
	return handle
}

func (fixture *uciAuthorizationMatrixFixture) contextSelectionArguments(ref uci.ContextRef) map[string]any {
	arguments := map[string]any{
		"action":              "select",
		"source_id":           ref.SourceID,
		"checkout_id":         ref.CheckoutID,
		"view_id":             ref.ViewID,
		"analysis_profile_id": ref.AnalysisProfileID,
		"generation":          ref.Generation,
	}
	if ref.SpaceID != nil {
		arguments["space_id"] = *ref.SpaceID
	}
	return arguments
}

func (fixture *uciAuthorizationMatrixFixture) invoke(t *testing.T, ctx context.Context, requestID, operation, handle string) *mcp.Response {
	t.Helper()
	return fixture.call(t, ctx, requestID, "codebase_"+operation, fixture.operationArguments(operation, handle, fixture.refA, nil))
}

func (fixture *uciAuthorizationMatrixFixture) operationArguments(operation, handle string, ref uci.ContextRef, continuation *string) map[string]any {
	switch operation {
	case "search":
		arguments := map[string]any{
			"query":       uciAuthorizationMatrixRawQuery,
			"path_prefix": uciAuthorizationMatrixRawPath,
			"limit":       5,
		}
		if handle != "" {
			arguments["context_handle"] = handle
		}
		return arguments
	case "graph":
		arguments := map[string]any{
			"action": "explain",
			"target": map[string]any{
				"source_id":  ref.SourceID,
				"view_id":    ref.ViewID,
				"entity_key": uciAuthorizationMatrixGraphKey,
			},
		}
		if handle != "" {
			arguments["context_handle"] = handle
		}
		if continuation != nil {
			arguments["continuation"] = *continuation
		}
		return arguments
	case "read":
		item := uciAuthorizationReadItem(ref)
		arguments := map[string]any{
			"ref": map[string]any{
				"source_id":  item.Ref.SourceID,
				"view_id":    item.Ref.ViewID,
				"entity_key": item.Ref.EntityKey,
			},
			"span": map[string]any{
				"byte_start": item.Span.ByteStart,
				"byte_end":   item.Span.ByteEnd,
				"line_start": item.Span.LineStart,
				"line_end":   item.Span.LineEnd,
			},
			"content_digest": string(item.ContentDigest),
		}
		if handle != "" {
			arguments["context_handle"] = handle
		}
		return arguments
	default:
		panic("unsupported authorization matrix operation: " + operation)
	}
}

func (fixture *uciAuthorizationMatrixFixture) call(t *testing.T, ctx context.Context, requestID, tool string, arguments map[string]any) *mcp.Response {
	t.Helper()
	argumentsJSON, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("marshal %s arguments: %v", tool, err)
	}
	params, err := json.Marshal(mcp.ToolCallParams{Name: tool, Arguments: argumentsJSON})
	if err != nil {
		t.Fatalf("marshal %s request: %v", tool, err)
	}
	response := fixture.server.HandleRequest(ctx, &mcp.Request{
		JSONRPC: "2.0",
		ID:      requestID,
		Method:  "tools/call",
		Params:  params,
	})
	if response == nil {
		t.Fatalf("%s returned no JSON-RPC response", tool)
	}
	return response
}

func (fixture *uciAuthorizationMatrixFixture) toolText(t *testing.T, response *mcp.Response) string {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("tool response error = %#v", response.Error)
	}
	result, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("tool result = %#v, want MCP content envelope", response.Result)
	}
	content, ok := result["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("tool content = %#v, want one text value", result["content"])
	}
	text, ok := content[0]["text"].(string)
	if !ok {
		t.Fatalf("tool text = %#v, want JSON string", content[0]["text"])
	}
	return text
}

func (fixture *uciAuthorizationMatrixFixture) requireQueryResponse(t *testing.T, response *mcp.Response) uci.QueryResponse {
	t.Helper()
	text := fixture.toolText(t, response)
	var payload uci.QueryResponse
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode query response: %v", err)
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("query response did not close after MCP release: %v; payload=%s", err, text)
	}
	return payload
}

func (fixture *uciAuthorizationMatrixFixture) requireBoundContext(t *testing.T, payload uci.QueryResponse, want uci.ContextRef) {
	t.Helper()
	if payload.Contexts == nil || len(*payload.Contexts) != 1 {
		t.Fatalf("contexts = %#v, want exactly one authorized context", payload.Contexts)
	}
	got := (*payload.Contexts)[0]
	if got.SourceID != want.SourceID || got.CheckoutID != want.CheckoutID || got.ViewID != want.ViewID || got.ProfileID != want.AnalysisProfileID || got.Generation != want.Generation {
		t.Fatalf("context = %#v, want bound context %#v", got, want)
	}
	if want.SpaceID != nil && (got.SpaceID == nil || *got.SpaceID != *want.SpaceID) {
		t.Fatalf("context space = %#v, want %q", got.SpaceID, *want.SpaceID)
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireUnknownExposure(t *testing.T, payload uci.QueryResponse) {
	t.Helper()
	if payload.Exposure == nil || payload.Exposure.ExposureRef == "" || payload.Exposure.CompletionState != uci.QueryCompletionUnknown {
		t.Fatalf("exposure = %#v, want opaque unknown receipt", payload.Exposure)
	}
	if !uci.ValidExposureRef(payload.Exposure.ExposureRef) {
		t.Fatalf("exposure reference = %q, want opaque canonical receipt", payload.Exposure.ExposureRef)
	}
	fixture.requireNoSensitiveValues(t, payload.Exposure.ExposureRef)
}

func (fixture *uciAuthorizationMatrixFixture) requireScopedExposure(t *testing.T, ref uci.ContextRef, payload uci.QueryResponse) {
	t.Helper()
	if payload.Retrieval == nil || payload.Coverage == nil || payload.Exposure == nil {
		t.Fatalf("response missing contextual evidence inputs: %#v", payload)
	}
	rows := fixture.store.exposuresSnapshot()
	if len(rows) != 1 {
		t.Fatalf("exposure rows = %d, want 1", len(rows))
	}
	fixture.requireExposureRecord(t, rows[0], ref, payload)
}

func (fixture *uciAuthorizationMatrixFixture) requireEachExposureScoped(t *testing.T, ref uci.ContextRef) {
	t.Helper()
	for _, row := range fixture.store.exposuresSnapshot() {
		fixture.requireExposureRecord(t, row, ref, uci.QueryResponse{})
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireExposureRecord(t *testing.T, record uci.ExposureRecord, ref uci.ContextRef, payload uci.QueryResponse) {
	t.Helper()
	if err := record.Validate(); err != nil {
		t.Fatalf("stored exposure record validation: %v", err)
	}
	if !uci.ValidExposureRef(record.ExposureRef) {
		t.Fatalf("exposure reference = %q, want opaque canonical receipt", record.ExposureRef)
	}
	if record.AuthRealm != string(auth.SourceClient) {
		t.Fatalf("exposure auth realm = %q, want caller source realm %q", record.AuthRealm, auth.SourceClient)
	}
	if record.SourceID != ref.SourceID || record.CheckoutID != ref.CheckoutID || record.ViewID != ref.ViewID {
		t.Fatalf("exposure scope = %#v, want %s/%s/%s", record, ref.SourceID, ref.CheckoutID, ref.ViewID)
	}
	if record.Operation == "" || record.Evidence == "" || record.Certainty == "" {
		t.Fatalf("exposure metadata is incomplete: %#v", record)
	}
	if payload.Status != "" {
		if record.ExposureRef != payload.Exposure.ExposureRef {
			t.Fatalf("stored receipt = %q, want released receipt %q", record.ExposureRef, payload.Exposure.ExposureRef)
		}
		if record.Result != uci.ExposureResultState(payload.Status) {
			t.Fatalf("exposure result = %q, want %q", record.Result, payload.Status)
		}
		if record.Retrieval != uci.ExposureRetrievalMode(payload.Retrieval.Mode) {
			t.Fatalf("exposure retrieval = %q, want %q", record.Retrieval, payload.Retrieval.Mode)
		}
		if record.Coverage != uci.ExposureCoverageState(payload.Coverage.Structural) {
			t.Fatalf("exposure coverage = %q, want %q", record.Coverage, payload.Coverage.Structural)
		}
	}
	if !uciAuthorizationHexDigest(record.BindingDigest) {
		t.Fatalf("exposure binding digest = %q, want opaque SHA-256 digest", record.BindingDigest)
	}
	for _, opaque := range []string{record.ClientRef, record.ClientSessionRef, record.RequestRef, record.IdempotencyKey, record.BindingDigest, record.ExposureRef} {
		fixture.requireNoSensitiveValues(t, opaque)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal stored exposure: %v", err)
	}
	fixture.requireNoSensitiveValues(t, string(raw))
}

func (fixture *uciAuthorizationMatrixFixture) requireSuppressedQuery(t *testing.T, response *mcp.Response, wantStatus uci.QueryResponseStatus, wantCode uci.QueryErrorCode) {
	t.Helper()
	text := fixture.toolText(t, response)
	var payload uci.QueryResponse
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode suppressed response: %v", err)
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("suppressed response validation: %v; payload=%s", err, text)
	}
	if payload.Status != wantStatus || payload.Error == nil || payload.Error.Code != wantCode {
		t.Fatalf("suppressed response = %#v, want %q/%q", payload, wantStatus, wantCode)
	}
	if payload.Exposure != nil || payload.Contexts != nil || payload.Freshness != nil || payload.Retrieval != nil || payload.Coverage != nil || payload.Items != nil || payload.Graph != nil || payload.Truncated != nil || payload.Warnings != nil || payload.Continuation != nil {
		t.Fatalf("suppressed response disclosed contextual data: %#v", payload)
	}
	fixture.requireClosedNoDisclosure(t, text)
}

func (fixture *uciAuthorizationMatrixFixture) requireToolError(t *testing.T, response *mcp.Response, wantCode string) {
	t.Helper()
	if response.Error == nil || response.Result != nil || fmt.Sprint(response.Error.Data) != wantCode {
		t.Fatalf("tool error = %#v, want closed %q", response, wantCode)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal tool error: %v", err)
	}
	fixture.requireClosedNoDisclosure(t, string(raw))
}

func (fixture *uciAuthorizationMatrixFixture) requireNoExposureAttempts(t *testing.T) {
	t.Helper()
	if got := fixture.store.exposureAttemptsCount(); got != 0 {
		t.Fatalf("exposure append attempts = %d, want 0", got)
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireOperationCalls(t *testing.T, operation string, want int) {
	t.Helper()
	if got := fixture.application.operationCallCount(operation); got != want {
		t.Fatalf("%s application calls = %d, want %d", operation, got, want)
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireAllOperationCalls(t *testing.T, want int) {
	t.Helper()
	for _, operation := range []string{"search", "graph", "read"} {
		fixture.requireOperationCalls(t, operation, want)
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireCallerIdentity(t *testing.T, principal string) {
	t.Helper()
	inputs := fixture.application.resolveInputsSnapshot()
	if len(inputs) == 0 {
		t.Fatal("resolver received no caller input")
	}
	for _, input := range inputs {
		if input.AuthRealm != string(auth.SourceClient) || input.Principal != principal {
			t.Fatalf("caller input = %#v, want source-derived realm and principal %q", input, principal)
		}
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireAllGraphRefsBound(t *testing.T, payload uci.QueryResponse, ref uci.ContextRef) {
	t.Helper()
	if payload.Graph == nil {
		t.Fatal("graph response is missing graph body")
	}
	for _, node := range payload.Graph.Nodes {
		fixture.requireGraphRefBound(t, node, ref)
	}
	for _, edge := range payload.Graph.Edges {
		fixture.requireGraphRefBound(t, edge.From, ref)
		fixture.requireGraphRefBound(t, edge.To, ref)
		for _, evidence := range edge.EvidenceRefs {
			fixture.requireGraphRefBound(t, evidence, ref)
		}
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireGraphRefBound(t *testing.T, value uci.QueryEntityRef, ref uci.ContextRef) {
	t.Helper()
	if value.SourceID != ref.SourceID || value.ViewID != ref.ViewID {
		t.Fatalf("graph ref = %#v, want source/view bound to %#v", value, ref)
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireOneCompletion(t *testing.T, exposureRef, wantOutcome string) {
	t.Helper()
	rows := fixture.store.completionsSnapshot()
	if len(rows) != 1 {
		t.Fatalf("completion rows = %d, want 1", len(rows))
	}
	record := rows[0]
	if err := record.Validate(); err != nil {
		t.Fatalf("stored completion validation: %v", err)
	}
	if record.ExposureRef != exposureRef || record.Outcome != uci.CompletionOutcome(wantOutcome) {
		t.Fatalf("completion record = %#v, want parent %q and outcome %q", record, exposureRef, wantOutcome)
	}
	if record.SupportedHostRef == "" || record.CallbackRef == "" || record.IdempotencyKey == "" || !uciAuthorizationHexDigest(record.BindingDigest) {
		t.Fatalf("completion record is incomplete or has non-opaque binding: %#v", record)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal stored completion: %v", err)
	}
	fixture.requireNoSensitiveValues(t, string(raw))
}

func (fixture *uciAuthorizationMatrixFixture) requireRecorderHealth(t *testing.T, ctx context.Context, handle string, wantState uci.ExposureHealthState, wantCode uci.ExposureHealthFailureCode) {
	t.Helper()
	response := fixture.call(t, ctx, "recorder-health-"+string(wantState), "codebase_status", map[string]any{"context_handle": handle})
	text := fixture.toolText(t, response)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode recorder health: %v", err)
	}
	recorder, ok := payload["evidence_recorder"].(map[string]any)
	if !ok || len(recorder) != 2 {
		t.Fatalf("recorder health = %#v, want secret-free state and failure code", payload["evidence_recorder"])
	}
	if recorder["state"] != string(wantState) || recorder["last_failure_code"] != string(wantCode) {
		t.Fatalf("recorder health = %#v, want %q/%q", recorder, wantState, wantCode)
	}
	fixture.requireNoSensitiveValues(t, text)
}

func (fixture *uciAuthorizationMatrixFixture) requireOpaqueContextHandle(t *testing.T, handle string) {
	t.Helper()
	fixture.requireNoSensitiveValues(t, handle)
	for _, ref := range []string{fixture.refA.SourceID, fixture.refA.CheckoutID, fixture.refA.ViewID, fixture.refB.SourceID, fixture.refB.CheckoutID, fixture.refB.ViewID} {
		if strings.Contains(handle, ref) {
			t.Fatalf("opaque context handle disclosed scoped identity %q", ref)
		}
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireNoSensitiveValues(t *testing.T, raw string) {
	t.Helper()
	for _, forbidden := range []string{
		uciAuthorizationMatrixRawQuery,
		uciAuthorizationMatrixRawSource,
		uciAuthorizationMatrixRawPath,
		uciAuthorizationMatrixLocator,
		uciAuthorizationMatrixSearchKey,
		uciAuthorizationMatrixReadKey,
		uciAuthorizationMatrixGraphKey,
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("non-content value leaked %q in %q", forbidden, raw)
		}
	}
}

func (fixture *uciAuthorizationMatrixFixture) requireClosedNoDisclosure(t *testing.T, raw string) {
	t.Helper()
	fixture.requireNoSensitiveValues(t, raw)
	for _, forbidden := range []string{
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
		fixture.refB.SourceID,
		fixture.refB.CheckoutID,
		fixture.refB.ViewID,
		"uci-exp_",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("closed value leaked %q in %q", forbidden, raw)
		}
	}
}

func uciAuthorizationHexDigest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, prefix)
	_, err := hex.DecodeString(encoded)
	return err == nil && strings.ToLower(encoded) == encoded
}

type uciAuthorizationMatrixApplication struct {
	mu sync.Mutex

	resolver   *uci.ContextResolver
	catalog    *uciAuthorizationMatrixCatalog
	authorizer *uciAuthorizationMatrixAuthorizer
	candidates map[string][]uci.ContextRef
	metadata   map[string]map[string]string
	responses  map[string]uci.QueryResponse
	health     *uci.ExposureHealthController

	graphResponder func(context.Context, uci.AuthorizedContext, mcp.CodebaseGraphInput) (uci.QueryResponse, error)
	afterResolve   func()
	afterOperation func()
	blockBarrier   bool
	resolveInputs  []uci.ResolveContextInput
	searchCalls    []mcp.CodebaseSearchInput
	graphCalls     []mcp.CodebaseGraphInput
	readCalls      []mcp.CodebaseReadInput
}

var (
	_ mcp.CodebaseContextApplication      = (*uciAuthorizationMatrixApplication)(nil)
	_ mcp.CodebaseIntelligenceApplication = (*uciAuthorizationMatrixApplication)(nil)
	_ mcp.CodebaseFreshnessApplication    = (*uciAuthorizationMatrixApplication)(nil)
	_ mcp.CodebaseReadApplication         = (*uciAuthorizationMatrixApplication)(nil)
	_ mcp.CodebaseGraphApplication        = (*uciAuthorizationMatrixApplication)(nil)
)

func (application *uciAuthorizationMatrixApplication) Resolve(ctx context.Context, input uci.ResolveContextInput) (uci.AuthorizedContext, error) {
	application.mu.Lock()
	application.resolveInputs = append(application.resolveInputs, input)
	resolver := application.resolver
	application.mu.Unlock()

	resolved, err := resolver.Resolve(ctx, input)
	if err != nil || input.Ref != nil || len(input.Candidates) != 0 {
		return application.afterSuccessfulResolve(resolved, err)
	}
	var contextErr *uci.ContextError
	if !errors.As(err, &contextErr) || contextErr.Code() != uci.ContextRequired {
		return application.afterSuccessfulResolve(resolved, err)
	}

	application.mu.Lock()
	candidates := append([]uci.ContextRef(nil), application.candidates[input.ClientSessionID]...)
	application.mu.Unlock()
	input.Candidates = candidates
	resolved, err = resolver.Resolve(ctx, input)
	return application.afterSuccessfulResolve(resolved, err)
}

func (application *uciAuthorizationMatrixApplication) Authorize(ctx context.Context, input uci.ResolveContextInput) (uci.AuthorizedContext, error) {
	application.mu.Lock()
	resolver := application.resolver
	application.mu.Unlock()
	return application.afterSuccessfulResolve(resolver.Authorize(ctx, input))
}

func (application *uciAuthorizationMatrixApplication) afterSuccessfulResolve(resolved uci.AuthorizedContext, err error) (uci.AuthorizedContext, error) {
	if err != nil {
		return resolved, err
	}
	application.mu.Lock()
	afterResolve := application.afterResolve
	application.afterResolve = nil
	application.mu.Unlock()
	if afterResolve != nil {
		afterResolve()
	}
	return resolved, nil
}

func (application *uciAuthorizationMatrixApplication) List(ctx context.Context, input uci.ResolveContextInput) ([]uci.ContextRef, error) {
	application.mu.Lock()
	candidates := append([]uci.ContextRef(nil), application.candidates[input.ClientSessionID]...)
	application.mu.Unlock()

	visible := make([]uci.ContextRef, 0, len(candidates))
	for _, candidate := range candidates {
		record, err := application.catalog.LoadContext(ctx, candidate)
		if err != nil || record.AuthRealm != input.AuthRealm {
			continue
		}
		if err := application.authorizer.AuthorizeContext(ctx, uci.ContextAccess{
			AuthRealm:  input.AuthRealm,
			Principal:  input.Principal,
			SourceID:   record.Ref.SourceID,
			CheckoutID: record.Ref.CheckoutID,
		}); err == nil {
			visible = append(visible, record.Ref)
		}
	}
	return visible, nil
}

func (application *uciAuthorizationMatrixApplication) Project(_ context.Context, ref uci.ContextRef) (map[string]string, error) {
	application.mu.Lock()
	metadata, found := application.metadata[ref.CheckoutID]
	application.mu.Unlock()
	if !found {
		return nil, errors.New("fixture context metadata unavailable")
	}
	copy := make(map[string]string, len(metadata))
	for key, value := range metadata {
		copy[key] = value
	}
	return copy, nil
}

func (*uciAuthorizationMatrixApplication) ResolveLegacyProject(context.Context, uci.AuthorizedContext, string) (uci.AliasTarget, error) {
	return uci.AliasTarget{}, errors.New("legacy compatibility is outside this fixture")
}

func (application *uciAuthorizationMatrixApplication) SearchCodebase(_ context.Context, _ uci.AuthorizedContext, input mcp.CodebaseSearchInput) (uci.QueryResponse, error) {
	application.mu.Lock()
	application.searchCalls = append(application.searchCalls, input)
	response, found := application.responses["search"]
	afterOperation := application.afterOperation
	application.afterOperation = nil
	application.mu.Unlock()
	if !found {
		return uci.QueryResponse{}, errors.New("fixture search response unavailable")
	}
	if afterOperation != nil {
		afterOperation()
	}
	return response, nil
}

func (application *uciAuthorizationMatrixApplication) CodebaseStatus(_ context.Context, _ uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error) {
	snapshot := application.health.Snapshot()
	return mcp.CodebaseStatusSnapshot{
		TotalChunks:    1,
		EmbeddedChunks: 1,
		EvidenceRecorder: mcp.CodebaseEvidenceRecorderHealth{
			State:           string(snapshot.State),
			LastFailureCode: string(snapshot.LastFailureCode),
		},
	}, nil
}

func (application *uciAuthorizationMatrixApplication) CodebaseFreshness(ctx context.Context, authorized uci.AuthorizedContext, token string) (uci.QueryFreshness, error) {
	application.mu.Lock()
	block := application.blockBarrier && token == uciAuthorizationMatrixBarrierKey
	application.mu.Unlock()
	if block {
		<-ctx.Done()
		return uci.QueryFreshness{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return uci.QueryFreshness{}, err
	}
	return *uciAuthorizationFreshness(authorized.Ref(), false), nil
}

func (application *uciAuthorizationMatrixApplication) ExploreCodebase(ctx context.Context, authorized uci.AuthorizedContext, input mcp.CodebaseGraphInput) (uci.QueryResponse, error) {
	application.mu.Lock()
	application.graphCalls = append(application.graphCalls, input)
	responder := application.graphResponder
	response, found := application.responses["graph"]
	afterOperation := application.afterOperation
	application.afterOperation = nil
	application.mu.Unlock()
	if afterOperation != nil {
		afterOperation()
	}
	if responder != nil {
		return responder(ctx, authorized, input)
	}
	if !found {
		return uci.QueryResponse{}, errors.New("fixture graph response unavailable")
	}
	return response, nil
}

func (application *uciAuthorizationMatrixApplication) ReadCodebase(_ context.Context, _ uci.AuthorizedContext, input mcp.CodebaseReadInput) (uci.QueryResponse, error) {
	application.mu.Lock()
	application.readCalls = append(application.readCalls, input)
	response, found := application.responses["read"]
	afterOperation := application.afterOperation
	application.afterOperation = nil
	application.mu.Unlock()
	if !found {
		return uci.QueryResponse{}, errors.New("fixture read response unavailable")
	}
	if afterOperation != nil {
		afterOperation()
	}
	return response, nil
}

func (application *uciAuthorizationMatrixApplication) setResponse(operation string, response uci.QueryResponse) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.responses[operation] = response
}

func (application *uciAuthorizationMatrixApplication) setGraphResponder(responder func(context.Context, uci.AuthorizedContext, mcp.CodebaseGraphInput) (uci.QueryResponse, error)) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.graphResponder = responder
}

func (application *uciAuthorizationMatrixApplication) setAfterResolve(afterResolve func()) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.afterResolve = afterResolve
}

func (application *uciAuthorizationMatrixApplication) setAfterOperation(afterOperation func()) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.afterOperation = afterOperation
}

func (application *uciAuthorizationMatrixApplication) setBlockBarrier(block bool) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.blockBarrier = block
}

func (application *uciAuthorizationMatrixApplication) operationCallCount(operation string) int {
	application.mu.Lock()
	defer application.mu.Unlock()
	switch operation {
	case "search":
		return len(application.searchCalls)
	case "graph":
		return len(application.graphCalls)
	case "read":
		return len(application.readCalls)
	default:
		return 0
	}
}

func (application *uciAuthorizationMatrixApplication) resolveInputsSnapshot() []uci.ResolveContextInput {
	application.mu.Lock()
	defer application.mu.Unlock()
	return append([]uci.ResolveContextInput(nil), application.resolveInputs...)
}

type uciAuthorizationMatrixCatalog struct {
	mu      sync.Mutex
	records map[string]uci.ContextRecord
}

func (catalog *uciAuthorizationMatrixCatalog) LoadContext(_ context.Context, ref uci.ContextRef) (uci.ContextRecord, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	record, found := catalog.records[ref.CheckoutID]
	if !found {
		return uci.ContextRecord{}, errors.New("fixture context not found")
	}
	return record, nil
}

type uciAuthorizationMatrixAuthorizer struct {
	mu      sync.Mutex
	allowed map[string]map[string]bool
}

func (authorizer *uciAuthorizationMatrixAuthorizer) AuthorizeContext(_ context.Context, access uci.ContextAccess) error {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	if authorizer.allowed[access.Principal][access.CheckoutID] {
		return nil
	}
	return errors.New("permission denied")
}

func (authorizer *uciAuthorizationMatrixAuthorizer) setAllowed(principal, checkoutID string, allowed bool) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	if authorizer.allowed[principal] == nil {
		authorizer.allowed[principal] = make(map[string]bool)
	}
	authorizer.allowed[principal][checkoutID] = allowed
}

type uciAuthorizationExposureStore struct {
	mu sync.Mutex

	failExposure   bool
	failCompletion bool

	exposureAttempts   int
	completionAttempts int
	exposures          []uci.ExposureRecord
	completions        []uci.CompletionEvidence
}

var _ uci.ExposureStore = (*uciAuthorizationExposureStore)(nil)

func (store *uciAuthorizationExposureStore) AppendExposure(ctx context.Context, record uci.ExposureRecord) (uci.ExposureRecord, error) {
	if err := ctx.Err(); err != nil {
		return uci.ExposureRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.exposureAttempts++
	if store.failExposure {
		return uci.ExposureRecord{}, errors.New("fixture exposure append unavailable")
	}

	key, digest, err := uciAuthorizationExposureIdentity(record)
	if err != nil {
		return uci.ExposureRecord{}, err
	}
	for _, existing := range store.exposures {
		existingKey, existingDigest, err := uciAuthorizationExposureIdentity(existing)
		if err != nil {
			return uci.ExposureRecord{}, err
		}
		if existingKey != key {
			continue
		}
		if existingDigest == digest {
			return existing, nil
		}
		return uci.ExposureRecord{}, uci.ErrIdempotencyMismatch
	}
	record.ExposureRef = uci.NewExposureRef()
	store.exposures = append(store.exposures, record)
	return record, nil
}

func (store *uciAuthorizationExposureStore) AppendCompletion(ctx context.Context, record uci.CompletionEvidence) (uci.CompletionEvidence, error) {
	if err := ctx.Err(); err != nil {
		return uci.CompletionEvidence{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.completionAttempts++
	if store.failCompletion {
		return uci.CompletionEvidence{}, errors.New("fixture completion append unavailable")
	}

	key, digest, err := uciAuthorizationCompletionIdentity(record)
	if err != nil {
		return uci.CompletionEvidence{}, err
	}
	for _, existing := range store.completions {
		existingKey, existingDigest, err := uciAuthorizationCompletionIdentity(existing)
		if err != nil {
			return uci.CompletionEvidence{}, err
		}
		if existingKey != key {
			continue
		}
		if existingDigest == digest {
			return existing, nil
		}
		return uci.CompletionEvidence{}, uci.ErrIdempotencyMismatch
	}
	store.completions = append(store.completions, record)
	return record, nil
}

func (store *uciAuthorizationExposureStore) setFailExposure(fail bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failExposure = fail
}

func (store *uciAuthorizationExposureStore) setFailCompletion(fail bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failCompletion = fail
}

func (store *uciAuthorizationExposureStore) exposuresSnapshot() []uci.ExposureRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]uci.ExposureRecord(nil), store.exposures...)
}

func (store *uciAuthorizationExposureStore) completionsSnapshot() []uci.CompletionEvidence {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]uci.CompletionEvidence(nil), store.completions...)
}

func (store *uciAuthorizationExposureStore) exposureCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.exposures)
}

func (store *uciAuthorizationExposureStore) completionCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.completions)
}

func (store *uciAuthorizationExposureStore) exposureAttemptsCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.exposureAttempts
}

func (store *uciAuthorizationExposureStore) completionAttemptsCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.completionAttempts
}

func (store *uciAuthorizationExposureStore) exposureJSON(t *testing.T, index int) string {
	t.Helper()
	rows := store.exposuresSnapshot()
	if index < 0 || index >= len(rows) {
		t.Fatalf("exposure index %d outside %d rows", index, len(rows))
	}
	raw, err := json.Marshal(rows[index])
	if err != nil {
		t.Fatalf("marshal exposure row: %v", err)
	}
	return string(raw)
}

func uciAuthorizationExposureIdentity(record uci.ExposureRecord) (string, string, error) {
	if record.AuthRealm == "" || record.ClientSessionRef == "" || record.IdempotencyKey == "" || record.BindingDigest == "" {
		return "", "", errors.New("fixture exposure record misses canonical identity")
	}
	return strings.Join([]string{record.AuthRealm, record.ClientSessionRef, record.IdempotencyKey}, "\x00"), record.BindingDigest, nil
}

func uciAuthorizationCompletionIdentity(record uci.CompletionEvidence) (string, string, error) {
	if record.ExposureRef == "" || record.SupportedHostRef == "" || record.IdempotencyKey == "" || record.BindingDigest == "" {
		return "", "", errors.New("fixture completion record misses canonical identity")
	}
	return strings.Join([]string{record.ExposureRef, record.SupportedHostRef, record.IdempotencyKey}, "\x00"), record.BindingDigest, nil
}

func uciAuthorizationSearchResponse(t *testing.T, ref uci.ContextRef, historical bool) uci.QueryResponse {
	t.Helper()
	response := uciAuthorizationBaseResponse(ref, uci.QueryStatusOK, uci.QueryRetrievalLexical, uci.IndexCoverageComplete, historical)
	items := uci.QueryItems{uciAuthorizationSearchItem(ref)}
	response.Items = &items
	uciAuthorizationRequirePreExposure(t, response)
	return response
}

func uciAuthorizationReadResponse(t *testing.T, ref uci.ContextRef) uci.QueryResponse {
	t.Helper()
	response := uciAuthorizationBaseResponse(ref, uci.QueryStatusOK, uci.QueryRetrievalExact, uci.IndexCoverageComplete, false)
	items := uci.QueryItems{uciAuthorizationReadItem(ref)}
	response.Items = &items
	uciAuthorizationRequirePreExposure(t, response)
	return response
}

func uciAuthorizationUnavailableResponse(t *testing.T, ref uci.ContextRef) uci.QueryResponse {
	t.Helper()
	response := uciAuthorizationBaseResponse(ref, uci.QueryStatusUnavailable, uci.QueryRetrievalUnavailable, uci.IndexCoverageUnavailable, true)
	response.Error = &uci.QueryError{Code: uci.QueryErrorCheckoutOffline}
	uciAuthorizationRequirePreExposure(t, response)
	return response
}

func uciAuthorizationGraphResponse(t *testing.T, ref uci.ContextRef, truncated bool, continuation *string) uci.QueryResponse {
	t.Helper()
	status := uci.QueryStatusOK
	stop := uci.QueryGraphComplete
	if truncated {
		status = uci.QueryStatusPartial
		stop = uci.QueryGraphNodeCap
	}
	response := uciAuthorizationBaseResponse(ref, status, uci.QueryRetrievalGraph, uci.IndexCoverageComplete, false)
	nodes, edges := uciAuthorizationGraphHops(ref)
	response.Graph = &uci.QueryGraph{Nodes: nodes, Edges: edges, StopReason: stop}
	response.Truncated = &truncated
	response.Continuation = &uci.QueryContinuation{Value: continuation}
	uciAuthorizationRequirePreExposure(t, response)
	return response
}

func uciAuthorizationBaseResponse(ref uci.ContextRef, status uci.QueryResponseStatus, mode uci.QueryRetrievalMode, coverage uci.IndexCoverageState, historical bool) uci.QueryResponse {
	contexts := uci.QueryContexts{{
		SpaceID:    ref.SpaceID,
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
		ViewID:     ref.ViewID,
		Generation: ref.Generation,
		ProfileID:  ref.AnalysisProfileID,
	}}
	items := uci.QueryItems{}
	warnings := uci.QueryWarnings{}
	truncated := false
	continuation := uci.QueryContinuation{}
	queryCoverage := uci.QueryCoverage{Structural: coverage}
	if coverage != uci.IndexCoverageUnavailable {
		zero := int64(0)
		queryCoverage.UnresolvedSites = &zero
		queryCoverage.UnsupportedFiles = &zero
	}
	return uci.QueryResponse{
		Schema:    uci.QueryResponseSchema,
		Status:    status,
		Contexts:  &contexts,
		Freshness: uciAuthorizationFreshness(ref, historical),
		Retrieval: &uci.QueryRetrieval{
			Mode:               mode,
			DegradationReasons: []string{},
		},
		Coverage:     &queryCoverage,
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}
}

func uciAuthorizationFreshness(ref uci.ContextRef, historical bool) *uci.QueryFreshness {
	zero := int64(0)
	freshness := &uci.QueryFreshness{
		State:          uci.QueryFreshnessObservedCurrent,
		Method:         uci.QueryFreshnessWatchWatermark,
		PendingChanges: &zero,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: ref.Generation,
			State:    uci.QueryEnrichmentCurrent,
		},
	}
	if historical {
		freshness.State = uci.QueryFreshnessHistorical
		freshness.Method = uci.QueryFreshnessPinnedHistory
		freshness.PendingChanges = nil
	}
	return freshness
}

func uciAuthorizationSearchItem(ref uci.ContextRef) uci.QueryItem {
	return uciAuthorizationItem(ref, uciAuthorizationMatrixSearchKey)
}

func uciAuthorizationReadItem(ref uci.ContextRef) uci.QueryItem {
	item := uciAuthorizationItem(ref, uciAuthorizationMatrixReadKey)
	item.MatchSources = []uci.QueryMatchSource{uci.QueryMatchExact}
	return item
}

func uciAuthorizationItem(ref uci.ContextRef, entityKey string) uci.QueryItem {
	return uci.QueryItem{
		Ref: uci.QueryEntityRef{
			SourceID:  ref.SourceID,
			ViewID:    ref.ViewID,
			EntityKey: entityKey,
		},
		Path: uciAuthorizationMatrixRawPath,
		Span: uci.QuerySpan{
			ByteStart: 0,
			ByteEnd:   int64(len(uciAuthorizationMatrixRawSource)),
			LineStart: 1,
			LineEnd:   1,
		},
		ContentDigest: uciAuthorizationContentDigest(uciAuthorizationMatrixRawSource),
		Kind:          uci.QueryItemCode,
		Language:      "go",
		Excerpt:       uciAuthorizationMatrixRawSource,
		MatchSources:  []uci.QueryMatchSource{uci.QueryMatchFTS},
	}
}

func uciAuthorizationContentDigest(value string) uci.QueryContentDigest {
	sum := sha256.Sum256([]byte(value))
	return uci.QueryContentDigest(hex.EncodeToString(sum[:]))
}

var uciAuthorizationRelations = []uci.IndexRelation{
	"contains",
	"imports",
	"exports",
	"references",
	"calls",
	"may_call",
	"inherits",
	"implements",
	"documents",
	"mentions",
	"configures",
	"schema_references",
	"tests",
	"depends_on",
}

func uciAuthorizationGraphHops(ref uci.ContextRef) ([]uci.QueryEntityRef, []uci.QueryGraphEdge) {
	entry := uci.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: uciAuthorizationMatrixGraphKey}
	nodes := []uci.QueryEntityRef{entry}
	edges := make([]uci.QueryGraphEdge, 0, len(uciAuthorizationRelations))
	for index, relation := range uciAuthorizationRelations {
		hop := uci.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: fmt.Sprintf("fixture.AuthorizationHop.%02d", index)}
		evidence := uci.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: fmt.Sprintf("fixture.AuthorizationEvidence.%02d", index)}
		nodes = append(nodes, hop, evidence)
		edges = append(edges, uci.QueryGraphEdge{
			From:         entry,
			To:           hop,
			Relation:     relation,
			EvidenceKind: uci.QueryEvidenceResolved,
			EvidenceRefs: []uci.QueryEntityRef{evidence},
		})
	}
	return nodes, edges
}

func uciAuthorizationRequirePreExposure(t *testing.T, response uci.QueryResponse) {
	t.Helper()
	if response.Exposure != nil {
		t.Fatalf("application response unexpectedly carries a released exposure: %#v", response.Exposure)
	}
	if err := response.ValidatePreExposure(); err != nil {
		t.Fatalf("application response is not a valid pre-exposure result: %v", err)
	}
}

func uciAuthorizationCallback(exposureRef, idempotencyKey string) uci.VerifiedSupportedHostCallback {
	return uci.VerifiedSupportedHostCallback{
		ExposureRef:      exposureRef,
		SupportedHostRef: "supported-host-opaque-reference",
		CallbackRef:      "callback-opaque-reference",
		Outcome:          "partial",
		IdempotencyKey:   idempotencyKey,
		OccurredAt:       time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC),
	}
}
