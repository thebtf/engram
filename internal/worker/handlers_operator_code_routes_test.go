package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
			name: "contexts",
			path: "/api/code/contexts",
			body: `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current"}`,
			configure: func(app *operatorCodeRouteTestApplication, _ uci.ContextRef) {
				app.metadata = map[string]string{"source": "engram source", "checkout": "working tree", "view": "release candidate"}
			},
			wantCalls: operatorCodeRouteTestCalls{project: 1},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			app := &operatorCodeRouteTestApplication{operatorCodeHTTPTestApplication: fixture.app}
			adapter = NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, app, fixture.recorder)
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
			require.Equal(t, testCase.wantCalls.project, app.projectCalls)
			require.Len(t, fixture.recorder.inputs, testCase.wantRecorder)
		})
	}
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
	require.Zero(t, app.projectCalls)
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

type operatorCodeRouteTestCalls struct {
	status  int
	search  int
	graph   int
	read    int
	project int
}

type operatorCodeRouteTestApplication struct {
	*operatorCodeHTTPTestApplication
	statusCalls  int
	projectCalls int
}

func (app *operatorCodeRouteTestApplication) CodebaseStatus(ctx context.Context, authorized uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error) {
	app.statusCalls++
	return app.operatorCodeHTTPTestApplication.CodebaseStatus(ctx, authorized)
}

func (app *operatorCodeRouteTestApplication) Project(ctx context.Context, ref uci.ContextRef) (map[string]string, error) {
	app.projectCalls++
	return app.operatorCodeHTTPTestApplication.Project(ctx, ref)
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
