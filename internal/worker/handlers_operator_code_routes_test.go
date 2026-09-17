package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/worker/sse"
)

func TestOperatorCodeRoutesDelegateFiveEndpoints(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		path         string
		body         string
		configure    func(*operatorCodeRouteTestApplication, uci.ContextRef)
		wantCalls    operatorCodeRouteTestCalls
		wantRecorder int
	}{
		{
			name: "status",
			path: "/api/code/status",
			body: `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current"}`,
			configure: func(app *operatorCodeRouteTestApplication, _ uci.ContextRef) {
				app.status = mcp.CodebaseStatusSnapshot{TotalChunks: 12, EmbeddedChunks: 7, Embedding: uci.EmbeddingStatus{Coverage: uci.IndexCoverageUnavailable}}
			},
			wantCalls: operatorCodeRouteTestCalls{status: 1},
		},
		{
			name: "search",
			path: "/api/code/search",
			body: `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture"}`,
			configure: func(app *operatorCodeRouteTestApplication, ref uci.ContextRef) {
				app.search = operatorCodeHTTPTestQueryResponse(t, ref, uci.QueryRetrievalLexical)
			},
			wantCalls:    operatorCodeRouteTestCalls{search: 1},
			wantRecorder: 1,
		},
		{
			name: "graph",
			path: "/api/code/graph",
			body: `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","action":"neighbors","target":{"entity_key":"Fixture.Symbol"}}`,
			configure: func(app *operatorCodeRouteTestApplication, ref uci.ContextRef) {
				app.graph = operatorCodeHTTPTestGraphResponse(t, ref)
			},
			wantCalls:    operatorCodeRouteTestCalls{graph: 1},
			wantRecorder: 1,
		},
		{
			name: "versioned read",
			path: "/api/code/source",
			body: `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","entity_key":"Fixture.Symbol","span":{"byte_start":0,"byte_end":12,"line_start":1,"line_end":1},"content_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			configure: func(app *operatorCodeRouteTestApplication, ref uci.ContextRef) {
				app.read = operatorCodeHTTPTestQueryResponse(t, ref, uci.QueryRetrievalExact)
			},
			wantCalls:    operatorCodeRouteTestCalls{read: 1},
			wantRecorder: 1,
		},
		{
			name:      "contexts",
			path:      "/api/code/contexts",
			body:      `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current"}`,
			configure: func(app *operatorCodeRouteTestApplication, _ uci.ContextRef) {},
			wantCalls: operatorCodeRouteTestCalls{},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			app := &operatorCodeRouteTestApplication{operatorCodeHTTPTestApplication: fixture.app}
			adapter = NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, app, fixture.recorder)
			adapter.contexts = fixture.contexts
			testCase.configure(app, fixture.ref)

			service := newOperatorCodeRouteTestService(adapter)
			recorder := httptest.NewRecorder()
			request := operatorCodeHTTPTestRequest(t, testCase.body, fixture.identity)
			request.URL.Path = testCase.path
			request.RequestURI = testCase.path
			service.router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, testCase.wantCalls.status, app.statusCalls)
			require.Equal(t, testCase.wantCalls.search, app.searchCalls)
			require.Equal(t, testCase.wantCalls.graph, app.graphCalls)
			require.Equal(t, testCase.wantCalls.read, app.readCalls)
			require.Len(t, fixture.recorder.inputs, testCase.wantRecorder)
		})
	}
}

func TestOperatorCodeRoutesDelegateIndexIntentEndpoints(t *testing.T) {
	t.Run("submit", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		app := &operatorCodeRouteTestApplication{operatorCodeHTTPTestApplication: fixture.app}
		adapter = NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, app, fixture.recorder)
		service := newOperatorCodeRouteTestService(adapter)
		recorder := httptest.NewRecorder()
		request := operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"route-submit","kind":"reindex"}`, fixture.identity)
		request.URL.Path = "/api/code/index-intents"
		request.RequestURI = "/api/code/index-intents"
		service.router.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
		require.Equal(t, 1, app.indexSubmitCalls)
		require.Len(t, app.indexIntents, 1)
		require.Empty(t, fixture.recorder.inputs)
	})

	t.Run("status releases completed metadata in the handler", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		app := &operatorCodeRouteTestApplication{operatorCodeHTTPTestApplication: fixture.app}
		adapter = NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, app, fixture.recorder)
		intent := operatorCodeHTTPTestIndexIntent(fixture.ref, "route-status", uci.IndexIntentReindex, uci.IndexIntentCompleted)
		app.indexIntents = map[string]uci.IndexIntent{intent.ID: intent}
		service := newOperatorCodeRouteTestService(adapter)
		recorder := httptest.NewRecorder()
		request := operatorCodeHTTPTestIndexIntentStatusRequest(t, intent.ID, "", fixture.identity)
		request.URL.Path = "/api/code/index-intents/" + intent.ID
		request.RequestURI = request.URL.Path
		service.router.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Equal(t, 1, app.indexGetCalls)
		require.Contains(t, recorder.Body.String(), `"result"`)
		require.Empty(t, fixture.recorder.inputs)
	})

	t.Run("retry", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		app := &operatorCodeRouteTestApplication{operatorCodeHTTPTestApplication: fixture.app}
		adapter = NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, app, fixture.recorder)
		intent := operatorCodeHTTPTestIndexIntent(fixture.ref, "route-retry", uci.IndexIntentReindex, uci.IndexIntentUnavailable)
		app.indexIntents = map[string]uci.IndexIntent{intent.ID: intent}
		service := newOperatorCodeRouteTestService(adapter)
		recorder := httptest.NewRecorder()
		request := operatorCodeHTTPTestIndexIntentRetryRequest(t, intent.ID, fixture.identity)
		request.URL.Path = "/api/code/index-intents/" + intent.ID + "/retry"
		request.RequestURI = request.URL.Path
		service.router.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
		require.Equal(t, 1, app.indexRetryCalls)
		require.Equal(t, 1, app.indexRetryExecutions)
	})
}

func TestOperatorCodeIndexIntentRoutesFailClosedWithoutComposition(t *testing.T) {
	service := newOperatorCodeRouteTestService(nil)
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/code/index-intents"},
		{method: http.MethodGet, path: "/api/code/index-intents/60000000-0000-4000-8000-000000000042"},
		{method: http.MethodPost, path: "/api/code/index-intents/60000000-0000-4000-8000-000000000042/retry"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, nil)
		service.router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, route.method+" "+route.path)
	}
}

func TestOperatorCodeIndexIntentRouteRejectsBrowserExecutionSelectors(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	app := &operatorCodeRouteTestApplication{operatorCodeHTTPTestApplication: fixture.app}
	adapter = NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, app, fixture.recorder)
	service := newOperatorCodeRouteTestService(adapter)
	recorder := httptest.NewRecorder()
	request := operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"rejected-browser-selector","kind":"reindex","path":"internal/private.go","credential":"secret","daemon":"local","publish":true}`, fixture.identity)
	request.URL.Path = "/api/code/index-intents"
	request.RequestURI = "/api/code/index-intents"
	service.router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Zero(t, app.indexSubmitCalls)
	require.Empty(t, app.indexIntents)
}

func TestOperatorCodeIndexIntentCompositionUsesDurableCurrentBinding(t *testing.T) {
	store := openWorkerUCIContextCompositionStore(t)
	server := mcp.NewServer(mcp.ServerOptions{Version: "operator-code-index-intent"})
	composition, err := composeUCIContext(true, store.GetDB(), server, workerUCISemanticConfig())
	require.NoError(t, err)
	fixture := newWorkerUCIApplicationFixture(t, composition)

	missingContextStore := *composition
	missingContextStore.contextStore = nil
	_, err = composeOperatorCodeHTTPAdapter(store.GetDB(), &missingContextStore)
	require.Error(t, err)

	adapter, err := composeOperatorCodeHTTPAdapter(store.GetDB(), composition)
	require.NoError(t, err)
	application, ok := adapter.app.(*operatorCodeIndexIntentComposition)
	require.True(t, ok)
	require.IsType(t, &CodeGrantApplication{}, adapter.onboarding)

	ctx := context.Background()
	currentRef := fixture.current.Context
	current, err := composition.resolver.Authorize(ctx, uci.ResolveContextInput{
		ClientSessionID: "operator-code-index-intent",
		AuthRealm:       fixture.source.AuthRealm,
		Principal:       fixture.principal,
		Ref:             &currentRef,
	})
	require.NoError(t, err)

	queued, err := application.SubmitIndexIntent(ctx, current, "durable-index-intent", uci.IndexIntentReindex)
	require.NoError(t, err)
	require.Equal(t, uci.IndexIntentQueued, queued.State)
	require.Zero(t, queued.Attempt)
	require.Nil(t, queued.Acknowledgement)
	stored, err := application.indexIntentStore.GetIndexIntent(ctx, queued.ID)
	require.NoError(t, err)
	require.WithinDuration(t, queued.CreatedAt, stored.CreatedAt, time.Microsecond)
	require.WithinDuration(t, queued.UpdatedAt, stored.UpdatedAt, time.Microsecond)
	stored.CreatedAt = queued.CreatedAt
	stored.UpdatedAt = queued.UpdatedAt
	require.Equal(t, queued, stored)

	replay, err := application.SubmitIndexIntent(ctx, current, "durable-index-intent", uci.IndexIntentReindex)
	require.NoError(t, err)
	require.Equal(t, queued.ID, replay.ID)
	require.Equal(t, uci.IndexIntentQueued, replay.State)
	_, err = application.SubmitIndexIntent(ctx, current, "durable-index-intent", uci.IndexIntentReconcile)
	require.ErrorIs(t, err, uci.ErrIndexIntentBindingMismatch)

	loaded, err := application.GetIndexIntent(ctx, current, queued.ID)
	require.NoError(t, err)
	require.Equal(t, queued.ID, loaded.ID)
	unavailable, err := application.indexIntentStore.MarkIndexIntentUnavailable(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, uci.IndexIntentUnavailable, unavailable.State)
	retried, err := application.RetryIndexIntent(ctx, current, queued.ID)
	require.NoError(t, err)
	require.Equal(t, queued.ID, retried.ID)
	require.Equal(t, uci.IndexIntentQueued, retried.State)

	historicalRef := fixture.historical.Context
	historical, err := composition.resolver.Authorize(ctx, uci.ResolveContextInput{
		ClientSessionID: "operator-code-index-intent-historical",
		AuthRealm:       fixture.source.AuthRealm,
		Principal:       fixture.principal,
		Ref:             &historicalRef,
	})
	require.NoError(t, err)
	_, err = application.GetIndexIntent(ctx, historical, queued.ID)
	require.ErrorIs(t, err, uci.ErrIndexIntentBindingMismatch)
}

func TestOperatorCodeRoutes_ExposeLifecycleAndPrePinContexts(t *testing.T) {
	t.Run("handshake", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		service := newOperatorCodeRouteTestService(adapter)
		recorder := httptest.NewRecorder()
		request := operatorCodeHTTPTestRequest(t, `{"document_nonce":"document-nonce-1"}`, fixture.identity)
		request.URL.Path = "/api/code/tabs/handshake"
		request.RequestURI = "/api/code/tabs/handshake"
		service.router.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.JSONEq(t, `{"state":"TAB_BINDING_READY","tab_binding_id":"60000000-0000-4000-8000-000000000041","document_proof":"proof-handshake","resume_nonce":"resume-handshake","reload_token":"reload-handshake"}`, recorder.Body.String())
	})

	t.Run("catalog before pin", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		fixture.binding.pinned = nil
		service := newOperatorCodeRouteTestService(adapter)
		recorder := httptest.NewRecorder()
		request := operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current"}`, fixture.identity)
		request.URL.Path = "/api/code/contexts"
		request.RequestURI = "/api/code/contexts"
		service.router.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Contains(t, recorder.Body.String(), `"contexts":[{`)
		require.Contains(t, recorder.Body.String(), `"repository":"Engram"`)
		require.Contains(t, recorder.Body.String(), `"selection_ref":"`)
		for _, forbidden := range []string{fixture.ref.SourceID, fixture.ref.CheckoutID, fixture.ref.ViewID} {
			require.NotContains(t, recorder.Body.String(), forbidden)
		}
	})
}

func TestOperatorCodeRoutes_BindingLifecyclePinsExactCatalogContext(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.binding.pinned = nil
	fixture.binding.handshake = BrowserBindingTransition{
		State:         BrowserBindingReady,
		TabBindingID:  operatorCodeHTTPTestBindingID,
		DocumentProof: "proof-current",
		ResumeNonce:   "resume-current",
		ReloadToken:   "reload-current",
	}
	fixture.binding.resume = BrowserBindingTransition{
		State:         BrowserBindingReady,
		TabBindingID:  operatorCodeHTTPTestBindingID,
		DocumentProof: "proof-resumed",
		ResumeNonce:   "resume-current",
		ReloadToken:   "reload-rotated",
	}
	fixture.app.read = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalExact)
	service := newOperatorCodeRouteTestService(adapter)

	call := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := operatorCodeHTTPTestRequest(t, body, fixture.identity)
		request.Method = method
		request.URL.Path = path
		request.RequestURI = path
		service.router.ServeHTTP(recorder, request)
		return recorder
	}

	handshake := call(http.MethodPost, "/api/code/tabs/handshake", `{"document_nonce":"fresh-document"}`)
	require.Equal(t, http.StatusOK, handshake.Code, handshake.Body.String())
	require.JSONEq(t, `{"state":"TAB_BINDING_READY","tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","resume_nonce":"resume-current","reload_token":"reload-current"}`, handshake.Body.String())

	contexts := call(http.MethodPost, "/api/code/contexts", `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current"}`)
	require.Equal(t, http.StatusOK, contexts.Code, contexts.Body.String())
	require.Contains(t, contexts.Body.String(), `"contexts":[{`)
	require.Contains(t, contexts.Body.String(), `"selection_ref":"`)
	for _, forbidden := range []string{fixture.ref.SourceID, fixture.ref.CheckoutID, fixture.ref.ViewID, "grant_ref", "digest"} {
		require.NotContains(t, contexts.Body.String(), forbidden)
	}

	pinned := call(http.MethodPut, "/api/code/tabs/"+operatorCodeHTTPTestBindingID+"/context", `{"document_proof":"proof-current","selection_ref":"`+operatorCodeContextSelectionRef(fixture.ref)+`"}`)
	require.Equal(t, http.StatusNoContent, pinned.Code, pinned.Body.String())
	require.Empty(t, pinned.Body.String())
	require.Equal(t, []BrowserBindingContext{browserBindingContext(fixture.ref)}, fixture.binding.pinnedTo)

	read := call(http.MethodPost, "/api/code/source", `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","entity_key":"Fixture.Symbol","span":{"byte_start":0,"byte_end":12,"line_start":1,"line_end":1},"content_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	require.Contains(t, read.Body.String(), `"excerpt":"package demo"`)

	renewed := call(http.MethodPut, "/api/code/tabs/"+operatorCodeHTTPTestBindingID+"/lease", `{"document_proof":"proof-current"}`)
	require.Equal(t, http.StatusNoContent, renewed.Code, renewed.Body.String())
	closed := call(http.MethodDelete, "/api/code/tabs/"+operatorCodeHTTPTestBindingID, `{"document_proof":"proof-current"}`)
	require.Equal(t, http.StatusNoContent, closed.Code, closed.Body.String())
	require.Equal(t, []BrowserBindingProof{{TabBindingID: operatorCodeHTTPTestBindingID, DocumentProof: "proof-current"}}, fixture.binding.renewed)
	require.Equal(t, []BrowserBindingProof{{TabBindingID: operatorCodeHTTPTestBindingID, DocumentProof: "proof-current"}}, fixture.binding.closed)

	resumed := call(http.MethodPost, "/api/code/tabs/resume", `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","resume_nonce":"resume-current","reload_token":"reload-current","document_nonce":"reload-document"}`)
	require.Equal(t, http.StatusOK, resumed.Code, resumed.Body.String())
	require.JSONEq(t, `{"state":"TAB_BINDING_READY","tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-resumed","resume_nonce":"resume-current","reload_token":"reload-rotated"}`, resumed.Body.String())
}

func TestOperatorCodeRoutes_CloseDenialsDoNotSelectOrLeak(t *testing.T) {
	selection := operatorCodeContextSelectionRef(uci.ContextRef{
		SourceID: operatorCodeHTTPTestSourceID, CheckoutID: operatorCodeHTTPTestCheckoutID, ViewID: operatorCodeHTTPTestViewID, AnalysisProfileID: operatorCodeHTTPTestProfileID, Generation: 7,
	})
	for _, testCase := range []struct {
		name      string
		path      string
		method    string
		body      string
		configure func(*operatorCodeHTTPTestFixture)
	}{
		{name: "replayed document proof", path: "/api/code/contexts", method: http.MethodPost, body: `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-replayed"}`},
		{name: "path binding mismatch", path: "/api/code/tabs/60000000-0000-4000-8000-000000000042/context", method: http.MethodPut, body: `{"document_proof":"proof-current","selection_ref":"` + selection + `"}`},
		{name: "revoked grant cannot pin", path: "/api/code/tabs/" + operatorCodeHTTPTestBindingID + "/context", method: http.MethodPut, body: `{"document_proof":"proof-current","selection_ref":"` + selection + `"}`, configure: func(fixture *operatorCodeHTTPTestFixture) {
			fixture.contexts.pinErr = gormstore.ErrBrowserCodeContextDenied
		}},
		{name: "expired grant cannot pin", path: "/api/code/tabs/" + operatorCodeHTTPTestBindingID + "/context", method: http.MethodPut, body: `{"document_proof":"proof-current","selection_ref":"` + selection + `"}`, configure: func(fixture *operatorCodeHTTPTestFixture) {
			fixture.contexts.pinErr = gormstore.ErrBrowserCodeContextDenied
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			fixture.binding.pinned = nil
			if testCase.configure != nil {
				testCase.configure(fixture)
			}
			service := newOperatorCodeRouteTestService(adapter)
			recorder := httptest.NewRecorder()
			request := operatorCodeHTTPTestRequest(t, testCase.body, fixture.identity)
			request.Method = testCase.method
			request.URL.Path = testCase.path
			request.RequestURI = testCase.path
			service.router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
			require.Empty(t, recorder.Body.String())
			require.Empty(t, fixture.binding.pinnedTo)
			for _, forbidden := range []string{fixture.ref.SourceID, fixture.ref.CheckoutID, fixture.ref.ViewID, "grant_ref", "digest", "proof-current"} {
				require.NotContains(t, recorder.Body.String(), forbidden)
			}
		})
	}
}

func TestOperatorCodeRoutes_CollisionDoesNotChangeOriginalPin(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	original := *fixture.binding.pinned
	fixture.binding.handshake = BrowserBindingTransition{
		State:         BrowserBindingCollision,
		TabBindingID:  "60000000-0000-4000-8000-000000000042",
		DocumentProof: "proof-copy",
		ResumeNonce:   "resume-copy",
		ReloadToken:   "reload-copy",
	}
	service := newOperatorCodeRouteTestService(adapter)
	recorder := httptest.NewRecorder()
	request := operatorCodeHTTPTestRequest(t, `{"document_nonce":"copy-document","copied_tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","copied_resume_nonce":"resume-current"}`, fixture.identity)
	request.URL.Path = "/api/code/tabs/handshake"
	request.RequestURI = "/api/code/tabs/handshake"
	service.router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"state":"TAB_BINDING_COLLISION","tab_binding_id":"60000000-0000-4000-8000-000000000042","document_proof":"proof-copy","resume_nonce":"resume-copy","reload_token":"reload-copy"}`, recorder.Body.String())
	require.Equal(t, &original, fixture.binding.pinned)
	require.Empty(t, fixture.binding.pinnedTo)
}

func TestOperatorCodeRoutesRejectForbiddenSelectorsBeforeDelegation(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	app := &operatorCodeRouteTestApplication{operatorCodeHTTPTestApplication: fixture.app}
	adapter = NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, app, fixture.recorder)
	app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
	service := newOperatorCodeRouteTestService(adapter)

	recorder := httptest.NewRecorder()
	request := operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","query":"Fixture","project":"forged-project","space_id":"60000000-0000-4000-8000-000000000099","admin":true,"path":"internal/secret.go"}`, fixture.identity)
	request.URL.Path = "/api/code/search"
	request.RequestURI = "/api/code/search"
	service.router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, app.statusCalls)
	require.Zero(t, app.searchCalls)
	require.Zero(t, app.graphCalls)
	require.Zero(t, app.readCalls)
	require.Empty(t, fixture.recorder.inputs)
}

func TestOperatorCodeServerAuthorizerDerivesSourceOwnerScope(t *testing.T) {
	ref := uci.ContextRef{
		SourceID:          "20000000-0000-4000-8000-000000000001",
		CheckoutID:        "30000000-0000-4000-8000-000000000001",
		ViewID:            "40000000-0000-4000-8000-000000000001",
		AnalysisProfileID: "50000000-0000-4000-8000-000000000001",
		Generation:        7,
	}
	contexts := &operatorCodeRouteTestContextStore{
		source:   &gormstore.UCISource{SourceID: ref.SourceID, AuthRealm: "browser-realm"},
		checkout: &gormstore.UCICheckout{CheckoutID: ref.CheckoutID, SourceID: ref.SourceID, OwnerPrincipal: "browser-user/99"},
	}
	resolver := &operatorCodeRouteTestResolver{}
	authority := newOperatorCodeServerAuthorizer(contexts, resolver)

	_, err := authority.AuthorizeOperatorCode(context.Background(), operatorCodeVerifiedCaller{
		Subject:   auth.BrowserSubjectForUser(41),
		SessionID: "browser-session-41",
		BindingID: operatorCodeHTTPTestBindingID,
		Context:   ref,
	})

	require.NoError(t, err)
	require.Equal(t, []string{ref.SourceID}, contexts.sourceCalls)
	require.Equal(t, []string{ref.CheckoutID}, contexts.checkoutCalls)
	require.Equal(t, 1, resolver.calls)
	require.NotNil(t, resolver.input.Ref)
	require.Equal(t, ref, *resolver.input.Ref)
	require.Equal(t, "browser-realm", resolver.input.AuthRealm)
	require.Equal(t, "browser-user/99", resolver.input.Principal)
	require.Equal(t, "operator-code/"+operatorCodeHTTPTestBindingID, resolver.input.ClientSessionID)
}

func TestOperatorCodeServerAuthorizer_FailsClosedBeforeResolverOnUnavailableContext(t *testing.T) {
	ref := uci.ContextRef{
		SourceID: "20000000-0000-4000-8000-000000000001", CheckoutID: "30000000-0000-4000-8000-000000000001", ViewID: "40000000-0000-4000-8000-000000000001", AnalysisProfileID: "50000000-0000-4000-8000-000000000001",
	}
	caller := operatorCodeVerifiedCaller{Subject: auth.BrowserSubjectForUser(41), BindingID: operatorCodeHTTPTestBindingID, Context: ref}

	t.Run("source unavailable", func(t *testing.T) {
		contexts := &operatorCodeRouteTestContextStore{}
		resolver := &operatorCodeRouteTestResolver{}
		_, err := newOperatorCodeServerAuthorizer(contexts, resolver).AuthorizeOperatorCode(context.Background(), caller)
		require.ErrorContains(t, err, "source scope is unavailable")
		require.Zero(t, resolver.calls)
		require.Empty(t, contexts.checkoutCalls)
	})

	t.Run("checkout belongs to a different source", func(t *testing.T) {
		contexts := &operatorCodeRouteTestContextStore{
			source:   &gormstore.UCISource{SourceID: ref.SourceID},
			checkout: &gormstore.UCICheckout{CheckoutID: ref.CheckoutID, SourceID: "other-source"},
		}
		resolver := &operatorCodeRouteTestResolver{}
		_, err := newOperatorCodeServerAuthorizer(contexts, resolver).AuthorizeOperatorCode(context.Background(), caller)
		require.ErrorContains(t, err, "checkout scope is unavailable")
		require.Zero(t, resolver.calls, "a mismatched persisted checkout must never reach UCI authorization")
	})
}

func TestOperatorCodeRoutesExposeWorkspaceJourneyBoundary(t *testing.T) {
	service := newOperatorCodeRouteTestService(nil)
	routes := make(map[string]map[string]bool)
	require.NoError(t, chi.Walk(service.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if routes[route] == nil {
			routes[route] = make(map[string]bool)
		}
		routes[route][method] = true
		return nil
	}))

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/code/tabs/handshake"},
		{http.MethodPost, "/api/code/tabs/resume"},
		{http.MethodPost, "/api/code/contexts"},
		{http.MethodPost, "/api/code/structure"},
		{http.MethodPost, "/api/code/search"},
		{http.MethodPost, "/api/code/graph"},
		{http.MethodPost, "/api/code/source"},
		{http.MethodGet, "/api/code/grants/choices"},
		{http.MethodPatch, "/api/code/grants/choices/{choice_ref}"},
		{http.MethodPost, "/api/code/grants"},
		{http.MethodPost, "/api/code/grants/{grant_ref}/revoke"},
	} {
		require.Truef(t, routes[route.path][route.method], "missing %s %s", route.method, route.path)
	}
}

func TestOperatorCodeRoutesDelegateStructureAndOwnerOnboarding(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	structure := operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalStructure)
	(*structure.Items)[0].MatchSources = []uci.QueryMatchSource{uci.QueryMatchStructure}
	require.NoError(t, structure.ValidatePreExposure())
	fixture.app.structure = structure
	grants := &recordingCodeGrantStore{ownerChoices: []gormstore.BrowserReadGrantOwnerChoice{{
		ChoiceRef:        fixture.ref.CheckoutID,
		RepositoryLabel:  "Engram",
		WorkingCopyLabel: "Studio workstation · release candidate",
	}}}
	adapter.onboarding = &CodeGrantApplication{grants: grants}
	service := newOperatorCodeRouteTestService(adapter)

	call := func(method, path, body string, identity auth.Identity) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := operatorCodeHTTPTestRequest(t, body, identity)
		request.Method = method
		request.URL.Path = path
		request.RequestURI = path
		service.router.ServeHTTP(recorder, request)
		return recorder
	}

	structureResult := call(http.MethodPost, "/api/code/structure", `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","path_prefix":"internal","limit":1}`, fixture.identity)
	require.Equal(t, http.StatusOK, structureResult.Code, structureResult.Body.String())
	require.Equal(t, 1, fixture.app.structureCalls)
	require.Equal(t, uci.QueryModeStructure, fixture.app.structureSpecs[0].Mode)
	require.Equal(t, "internal", fixture.app.structureSpecs[0].Filter.PathPrefix)
	require.Len(t, fixture.recorder.inputs, 1)

	choices := httptest.NewRecorder()
	choiceRequest := httptest.NewRequest(http.MethodGet, "/api/code/grants/choices", nil)
	choiceRequest.Header.Set(operatorCodeRequestIDHeader, "operator-request-choices")
	choiceRequest.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "browser-session-41"})
	choiceRequest = choiceRequest.WithContext(auth.WithIdentity(choiceRequest.Context(), fixture.identity))
	service.router.ServeHTTP(choices, choiceRequest)
	require.Equal(t, http.StatusOK, choices.Code, choices.Body.String())
	choiceRef := operatorCodeOpaqueRef("grant-choice", fixture.ref.CheckoutID)
	require.Contains(t, choices.Body.String(), `"choice_ref":"`+choiceRef+`"`)
	require.NotContains(t, choices.Body.String(), fixture.ref.CheckoutID)

	issued := call(http.MethodPost, "/api/code/grants", `{"choice_ref":"`+choiceRef+`"}`, fixture.identity)
	require.Equal(t, http.StatusOK, issued.Code, issued.Body.String())
	require.Len(t, grants.ownerIssues, 1)
	require.Equal(t, fixture.ref.CheckoutID, grants.ownerIssues[0].ChoiceRef)
	require.Equal(t, int64(41), grants.ownerIssues[0].TargetUserID)

	labeled := call(http.MethodPatch, "/api/code/grants/choices/"+choiceRef, `{"working_copy":"Desk · release candidate"}`, fixture.identity)
	require.Equal(t, http.StatusOK, labeled.Code, labeled.Body.String())
	require.Len(t, grants.ownerLabels, 1)
	require.Len(t, grants.ownerIssues, 1, "labels are display metadata, not grant issuance")

	grantRef := "60000000-0000-4000-8000-000000000099"
	revoked := call(http.MethodPost, "/api/code/grants/"+grantRef+"/revoke", "", fixture.identity)
	require.Equal(t, http.StatusOK, revoked.Code, revoked.Body.String())
	require.Len(t, grants.revokes, 1)
	require.Equal(t, grantRef, grants.revokes[0].grantRef)

	nonOwner := httptest.NewRecorder()
	nonOwnerRequest := httptest.NewRequest(http.MethodGet, "/api/code/grants/choices", nil)
	nonOwnerRequest.Header.Set(operatorCodeRequestIDHeader, "operator-request-non-owner")
	nonOwnerRequest.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "browser-session-41"})
	nonOwnerRequest = nonOwnerRequest.WithContext(auth.WithIdentity(nonOwnerRequest.Context(), auth.Session("admin")))
	service.router.ServeHTTP(nonOwner, nonOwnerRequest)
	require.Equal(t, http.StatusForbidden, nonOwner.Code, nonOwner.Body.String())
}

type operatorCodeRouteTestCalls struct {
	status int
	search int
	graph  int
	read   int
}

type operatorCodeRouteTestApplication struct {
	*operatorCodeHTTPTestApplication
	statusCalls int
}

func (app *operatorCodeRouteTestApplication) CodebaseStatus(ctx context.Context, authorized uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error) {
	app.statusCalls++
	return app.operatorCodeHTTPTestApplication.CodebaseStatus(ctx, authorized)
}

func newOperatorCodeRouteTestService(adapter *OperatorCodeHTTPAdapter) *Service {
	service := &Service{
		router:              chi.NewRouter(),
		mcpHealth:           mcp.NewMCPHealth(),
		sseBroadcaster:      sse.NewBroadcaster(),
		operatorCodeAdapter: adapter,
	}
	service.ready.Store(true)
	service.setupRoutes()
	return service
}

type operatorCodeRouteTestContextStore struct {
	source        *gormstore.UCISource
	checkout      *gormstore.UCICheckout
	sourceCalls   []string
	checkoutCalls []string
}

func (store *operatorCodeRouteTestContextStore) GetSource(_ context.Context, sourceID string) (*gormstore.UCISource, error) {
	store.sourceCalls = append(store.sourceCalls, sourceID)
	if store.source == nil || store.source.SourceID != sourceID {
		return nil, errors.New("source not found")
	}
	return store.source, nil
}

func (store *operatorCodeRouteTestContextStore) GetCheckout(_ context.Context, checkoutID string) (*gormstore.UCICheckout, error) {
	store.checkoutCalls = append(store.checkoutCalls, checkoutID)
	if store.checkout == nil || store.checkout.CheckoutID != checkoutID {
		return nil, errors.New("checkout not found")
	}
	return store.checkout, nil
}

type operatorCodeRouteTestResolver struct {
	input uci.ResolveContextInput
	calls int
}

func (resolver *operatorCodeRouteTestResolver) Authorize(_ context.Context, input uci.ResolveContextInput) (uci.AuthorizedContext, error) {
	resolver.calls++
	resolver.input = input
	if input.Ref != nil {
		ref := *input.Ref
		resolver.input.Ref = &ref
	}
	return uci.AuthorizedContext{}, nil
}
