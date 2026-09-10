package worker

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

func TestOperatorCodeHTTPAdapter_ReleasesFiveBoundEndpoints(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		body           string
		invoke         func(*OperatorCodeHTTPAdapter, http.ResponseWriter, *http.Request)
		configure      func(*operatorCodeHTTPTestApplication, uci.ContextRef)
		wantRecorder   int
		assertResponse func(*testing.T, string)
	}{
		{
			name:   "status",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleStatus,
			configure: func(app *operatorCodeHTTPTestApplication, _ uci.ContextRef) {
				app.status = mcp.CodebaseStatusSnapshot{TotalChunks: 12, EmbeddedChunks: 7, Embedding: uci.EmbeddingStatus{Coverage: uci.IndexCoverageUnavailable}}
			},
			assertResponse: func(t *testing.T, body string) {
				t.Helper()
				require.JSONEq(t, `{"total_chunks":12,"embedded_chunks":7,"embedding":{"EmbeddingProfileID":null,"Coverage":"unavailable","TotalCandidates":0,"ReadyCandidates":0,"PendingJobs":0,"JobState":null,"ErrorCode":null,"RetryAfter":null}}`, body)
			},
		},
		{
			name:   "search",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleSearch,
			configure: func(app *operatorCodeHTTPTestApplication, ref uci.ContextRef) {
				app.search = operatorCodeHTTPTestQueryResponse(t, ref, uci.QueryRetrievalLexical)
			},
			wantRecorder: 1,
			assertResponse: func(t *testing.T, body string) {
				t.Helper()
				require.Contains(t, body, `"excerpt":"package demo"`)
				require.Contains(t, body, `"exposure_ref":"uci-exp_operator1"`)
			},
		},
		{
			name:   "graph",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","action":"neighbors","target":{"entity_key":"Fixture.Symbol"}}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleGraph,
			configure: func(app *operatorCodeHTTPTestApplication, ref uci.ContextRef) {
				app.graph = operatorCodeHTTPTestGraphResponse(t, ref)
			},
			wantRecorder: 1,
			assertResponse: func(t *testing.T, body string) {
				t.Helper()
				require.Contains(t, body, `"stop_reason":"complete"`)
				require.Contains(t, body, `"exposure_ref":"uci-exp_operator1"`)
			},
		},
		{
			name:   "versioned read",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","entity_key":"Fixture.Symbol","span":{"byte_start":0,"byte_end":12,"line_start":1,"line_end":1},"content_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleVersionedRead,
			configure: func(app *operatorCodeHTTPTestApplication, ref uci.ContextRef) {
				app.read = operatorCodeHTTPTestQueryResponse(t, ref, uci.QueryRetrievalExact)
			},
			wantRecorder: 1,
			assertResponse: func(t *testing.T, body string) {
				t.Helper()
				require.Contains(t, body, `"excerpt":"package demo"`)
				require.Contains(t, body, `"exposure_ref":"uci-exp_operator1"`)
			},
		},
		{
			name:   "current grant metadata",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleContexts,
			configure: func(app *operatorCodeHTTPTestApplication, _ uci.ContextRef) {
				app.metadata = map[string]string{"source": "engram source", "checkout": "working tree", "view": "release candidate"}
			},
			assertResponse: func(t *testing.T, body string) {
				t.Helper()
				require.JSONEq(t, `{"context":{"source":"engram source","checkout":"working tree","view":"release candidate"}}`, body)
				require.NotContains(t, body, "proof-current")
				require.NotContains(t, body, operatorCodeHTTPTestBindingID)
				require.NotContains(t, body, "operator-request")
				require.NotContains(t, body, "grant_ref")
				require.NotContains(t, body, "nonce")
				require.NotContains(t, body, "digest")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			testCase.configure(fixture.app, fixture.ref)
			recorder := httptest.NewRecorder()
			testCase.invoke(adapter, recorder, operatorCodeHTTPTestRequest(t, testCase.body, fixture.identity))

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Len(t, fixture.recorder.inputs, testCase.wantRecorder)
			if testCase.wantRecorder != 0 {
				input := fixture.recorder.inputs[0]
				require.Empty(t, input.ClientKeycard)
				require.Equal(t, fixture.identity.BrowserSubject, input.BrowserSubject)
				require.Equal(t, operatorCodeHTTPTestBindingID, input.BrowserDocumentBinding)
				require.Equal(t, "browser-session-41", input.ClientSession)
			}
			testCase.assertResponse(t, recorder.Body.String())
		})
	}
}

func TestOperatorCodeHTTPAdapter_RejectsClientSelectorsAndBoundsWithoutBody(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		body   string
		invoke func(*OperatorCodeHTTPAdapter, http.ResponseWriter, *http.Request)
	}{
		{
			name:   "search path selector",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture","path":"internal/secret.go"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleSearch,
		},
		{
			name:   "search project label and Space selectors",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture","project":"operator-selected","label":"release","space_id":"` + uuid.NewString() + `"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleSearch,
		},
		{
			name:   "search admin selector",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture","admin":true}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleSearch,
		},
		{
			name:   "search before cursor",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture","before":"cursor-from-another-context"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleSearch,
		},
		{
			name:   "search limit bound",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture","limit":51}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleSearch,
		},
		{
			name:   "graph bound",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","action":"neighbors","target":{"entity_key":"Fixture.Symbol"},"max_nodes":201}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleGraph,
		},
		{
			name:   "read context selector",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","entity_key":"Fixture.Symbol","source_id":"` + uuid.NewString() + `","span":{"byte_start":0,"byte_end":12,"line_start":1,"line_end":1},"content_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleVersionedRead,
		},
		{
			name:   "read byte limit",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","entity_key":"Fixture.Symbol","span":{"byte_start":0,"byte_end":12,"line_start":1,"line_end":1},"content_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","max_bytes":8193}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleVersionedRead,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			fixture.app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
			recorder := httptest.NewRecorder()
			testCase.invoke(adapter, recorder, operatorCodeHTTPTestRequest(t, testCase.body, fixture.identity))

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, recorder.Body.String())
			require.Zero(t, fixture.app.searchCalls)
			require.Zero(t, fixture.app.graphCalls)
			require.Zero(t, fixture.app.readCalls)
			require.Empty(t, fixture.recorder.inputs)
		})
	}
}

func TestOperatorCodeHTTPAdapter_RejectsNonBrowserWrongProofAndWrongContext(t *testing.T) {
	body := `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture"}`
	for _, testCase := range []struct {
		name      string
		body      string
		configure func(*operatorCodeHTTPTestFixture)
		identity  auth.Identity
		want      int
	}{
		{
			name:     "keycard caller",
			identity: auth.Client("read-write", uuid.NewString()),
			want:     http.StatusForbidden,
		},
		{
			name: "replayed proof from another document",
			body: `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-replayed","query":"Fixture"}`,
			want: http.StatusForbidden,
		},
		{
			name: "wrong pinned context",
			configure: func(fixture *operatorCodeHTTPTestFixture) {
				fixture.binding.pinned.SourceID = uuid.NewString()
			},
			want: http.StatusConflict,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			fixture.app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
			if testCase.configure != nil {
				testCase.configure(fixture)
			}
			identity := fixture.identity
			if testCase.identity.Source != "" {
				identity = testCase.identity
			}
			recorder := httptest.NewRecorder()
			requestBody := body
			if testCase.body != "" {
				requestBody = testCase.body
			}
			adapter.HandleSearch(recorder, operatorCodeHTTPTestRequest(t, requestBody, identity))

			require.Equal(t, testCase.want, recorder.Code)
			require.Empty(t, recorder.Body.String())
			require.Zero(t, fixture.app.searchCalls)
			require.Empty(t, fixture.recorder.inputs)
		})
	}
}

func TestOperatorCodeHTTPAdapter_RevocationAndRecorderFailureSuppressResultAndReceipt(t *testing.T) {
	body := `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture"}`
	for _, testCase := range []struct {
		name      string
		configure func(*operatorCodeHTTPTestFixture)
		want      int
		bodyless  bool
	}{
		{
			name: "revoked after application response",
			configure: func(fixture *operatorCodeHTTPTestFixture) {
				fixture.grants.revokeOnSecondCheck = true
			},
			want: http.StatusForbidden,
		},
		{
			name: "recorder failure",
			configure: func(fixture *operatorCodeHTTPTestFixture) {
				fixture.recorder.err = errors.New("database unavailable")
			},
			want: http.StatusServiceUnavailable,
		},
		{
			name: "invalid application output",
			configure: func(fixture *operatorCodeHTTPTestFixture) {
				fixture.app.search = uci.QueryResponse{}
			},
			want:     http.StatusServiceUnavailable,
			bodyless: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			fixture.app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
			testCase.configure(fixture)
			recorder := httptest.NewRecorder()
			adapter.HandleSearch(recorder, operatorCodeHTTPTestRequest(t, body, fixture.identity))

			require.Equal(t, testCase.want, recorder.Code)
			require.NotContains(t, recorder.Body.String(), "package demo")
			require.NotContains(t, recorder.Body.String(), "uci-exp_operator1")
			require.NotContains(t, recorder.Body.String(), `"items"`)
			require.NotContains(t, recorder.Body.String(), `"contexts"`)
			require.NotContains(t, recorder.Body.String(), `"exposure_ref"`)
			if testCase.bodyless {
				require.Empty(t, recorder.Body.String())
			}
		})
	}
}

const operatorCodeHTTPTestBindingID = "60000000-0000-4000-8000-000000000041"

type operatorCodeHTTPTestFixture struct {
	ref       uci.ContextRef
	identity  auth.Identity
	grants    *operatorCodeHTTPTestGrants
	binding   *operatorCodeHTTPTestBinding
	authority *operatorCodeHTTPTestAuthority
	app       *operatorCodeHTTPTestApplication
	recorder  *operatorCodeHTTPTestRecorder
}

func newOperatorCodeHTTPTestAdapter(t *testing.T) (*OperatorCodeHTTPAdapter, *operatorCodeHTTPTestFixture) {
	t.Helper()
	ref := uci.ContextRef{
		SourceID:          "20000000-0000-4000-8000-000000000001",
		CheckoutID:        "30000000-0000-4000-8000-000000000001",
		ViewID:            "40000000-0000-4000-8000-000000000001",
		AnalysisProfileID: "50000000-0000-4000-8000-000000000001",
		Generation:        7,
	}
	identity := auth.SessionForBrowserUser("viewer", 41)
	fixture := &operatorCodeHTTPTestFixture{
		ref:      ref,
		identity: identity,
		grants:   &operatorCodeHTTPTestGrants{allowed: true},
		binding: &operatorCodeHTTPTestBinding{pinned: BrowserBindingContext{
			SourceID:          ref.SourceID,
			CheckoutID:        ref.CheckoutID,
			ViewID:            ref.ViewID,
			AnalysisProfileID: ref.AnalysisProfileID,
			Generation:        ref.Generation,
		}},
		app:      &operatorCodeHTTPTestApplication{},
		recorder: &operatorCodeHTTPTestRecorder{},
	}
	fixture.authority = newOperatorCodeHTTPTestAuthority(t, ref)
	return NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, fixture.app, fixture.recorder), fixture
}

type operatorCodeHTTPTestGrants struct {
	allowed             bool
	checks              int
	revokeOnSecondCheck bool
}

func (grants *operatorCodeHTTPTestGrants) CanRead(_ context.Context, caller auth.Identity, sourceID, checkoutID string) (bool, error) {
	if _, ok := caller.SessionBrowserSubject(); !ok || sourceID == "" || checkoutID == "" {
		return false, nil
	}
	grants.checks++
	if grants.revokeOnSecondCheck && grants.checks == 2 {
		grants.allowed = false
	}
	return grants.allowed, nil
}

type operatorCodeHTTPTestBinding struct {
	pinned BrowserBindingContext
	err    error
	calls  int
}

func (binding *operatorCodeHTTPTestBinding) Guard(_ context.Context, identity auth.Identity, sessionID string, proof BrowserBindingProof) (BrowserBindingGuarded, error) {
	binding.calls++
	if binding.err != nil {
		return BrowserBindingGuarded{}, binding.err
	}
	if _, ok := identity.SessionBrowserSubject(); !ok || sessionID != "browser-session-41" || proof.TabBindingID != operatorCodeHTTPTestBindingID || proof.DocumentProof != "proof-current" {
		return BrowserBindingGuarded{}, errors.New("proof denied")
	}
	pinned := binding.pinned
	return BrowserBindingGuarded{TabBindingID: proof.TabBindingID, Pinned: &pinned}, nil
}

type operatorCodeHTTPTestAuthority struct {
	resolver *uci.ContextResolver
	ref      uci.ContextRef
}

func newOperatorCodeHTTPTestAuthority(t *testing.T, ref uci.ContextRef) *operatorCodeHTTPTestAuthority {
	t.Helper()
	catalog := operatorCodeHTTPTestCatalog{ref: ref}
	resolver := uci.NewContextResolver(catalog, operatorCodeHTTPTestAuthorizer{ref: ref}, nil)
	return &operatorCodeHTTPTestAuthority{resolver: resolver, ref: ref}
}

func (authority *operatorCodeHTTPTestAuthority) AuthorizeOperatorCode(ctx context.Context, caller operatorCodeVerifiedCaller) (uci.AuthorizedContext, error) {
	ref := authority.ref
	return authority.resolver.Authorize(ctx, uci.ResolveContextInput{
		ClientSessionID: "server/" + caller.SessionID,
		AuthRealm:       "browser",
		Principal:       "browser-user/99",
		Ref:             &ref,
	})
}

type operatorCodeHTTPTestCatalog struct {
	ref uci.ContextRef
}

func (catalog operatorCodeHTTPTestCatalog) LoadContext(_ context.Context, ref uci.ContextRef) (uci.ContextRecord, error) {
	if !operatorCodeHTTPTestRefsEqual(ref, catalog.ref) {
		return uci.ContextRecord{}, errors.New("unknown context")
	}
	return uci.ContextRecord{Ref: catalog.ref, AuthRealm: "browser"}, nil
}

type operatorCodeHTTPTestAuthorizer struct {
	ref uci.ContextRef
}

func (authorizer operatorCodeHTTPTestAuthorizer) AuthorizeContext(_ context.Context, access uci.ContextAccess) error {
	if access.AuthRealm != "browser" || access.Principal != "browser-user/99" || access.SourceID != authorizer.ref.SourceID || access.CheckoutID != authorizer.ref.CheckoutID {
		return errors.New("owner mismatch")
	}
	return nil
}

type operatorCodeHTTPTestApplication struct {
	search      uci.QueryResponse
	graph       uci.QueryResponse
	read        uci.QueryResponse
	status      mcp.CodebaseStatusSnapshot
	metadata    map[string]string
	searchCalls int
	graphCalls  int
	readCalls   int
}

func (app *operatorCodeHTTPTestApplication) SearchCodebase(_ context.Context, _ uci.AuthorizedContext, _ mcp.CodebaseSearchInput) (uci.QueryResponse, error) {
	app.searchCalls++
	return app.search, nil
}

func (app *operatorCodeHTTPTestApplication) ExploreCodebase(_ context.Context, _ uci.AuthorizedContext, _ mcp.CodebaseGraphInput) (uci.QueryResponse, error) {
	app.graphCalls++
	return app.graph, nil
}

func (app *operatorCodeHTTPTestApplication) ReadCodebase(_ context.Context, _ uci.AuthorizedContext, _ mcp.CodebaseReadInput) (uci.QueryResponse, error) {
	app.readCalls++
	return app.read, nil
}

func (app *operatorCodeHTTPTestApplication) CodebaseStatus(_ context.Context, _ uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error) {
	return app.status, nil
}

func (app *operatorCodeHTTPTestApplication) Project(_ context.Context, _ uci.ContextRef) (map[string]string, error) {
	return app.metadata, nil
}

type operatorCodeHTTPTestRecorder struct {
	inputs []uci.ExposureInput
	err    error
}

func (recorder *operatorCodeHTTPTestRecorder) Record(_ context.Context, _ uci.AuthorizedContext, input uci.ExposureInput) (uci.QueryExposure, error) {
	recorder.inputs = append(recorder.inputs, input)
	if recorder.err != nil {
		return uci.QueryExposure{}, recorder.err
	}
	return uci.QueryExposure{ExposureRef: "uci-exp_operator1", CompletionState: uci.QueryCompletionUnknown}, nil
}

func operatorCodeHTTPTestRequest(t *testing.T, body string, identity auth.Identity) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/operator/code", bytes.NewBufferString(body))
	request.Header.Set("X-Engram-Request-ID", "operator-request-1")
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "browser-session-41"})
	return request.WithContext(auth.WithIdentity(request.Context(), identity))
}

func operatorCodeHTTPTestQueryResponse(t *testing.T, ref uci.ContextRef, mode uci.QueryRetrievalMode) uci.QueryResponse {
	t.Helper()
	zero := int64(0)
	truncated := false
	contexts := uci.QueryContexts{{SourceID: ref.SourceID, CheckoutID: ref.CheckoutID, ViewID: ref.ViewID, Generation: ref.Generation, ProfileID: ref.AnalysisProfileID}}
	items := uci.QueryItems{{
		Ref:           uci.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: "Fixture.Symbol"},
		Path:          "internal/fixture.go",
		Span:          uci.QuerySpan{ByteStart: 0, ByteEnd: 12, LineStart: 1, LineEnd: 1},
		ContentDigest: uci.QueryContentDigest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Kind:          uci.QueryItemCode,
		Language:      "go",
		Excerpt:       "package demo",
		MatchSources:  []uci.QueryMatchSource{uci.QueryMatchFTS},
	}}
	warnings := uci.QueryWarnings{}
	continuation := uci.QueryContinuation{}
	response := uci.QueryResponse{
		Schema:   uci.QueryResponseSchema,
		Status:   uci.QueryStatusOK,
		Contexts: &contexts,
		Freshness: &uci.QueryFreshness{
			State:               uci.QueryFreshnessObservedCurrent,
			Method:              uci.QueryFreshnessWatchWatermark,
			PendingChanges:      &zero,
			EnrichmentWatermark: uci.QueryEnrichmentWatermark{Sequence: ref.Generation, State: uci.QueryEnrichmentCurrent},
		},
		Retrieval:    &uci.QueryRetrieval{Mode: mode, DegradationReasons: []string{}},
		Coverage:     &uci.QueryCoverage{Structural: uci.IndexCoverageComplete, UnresolvedSites: &zero, UnsupportedFiles: &zero},
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}
	require.NoError(t, response.ValidatePreExposure())
	return response
}

func operatorCodeHTTPTestGraphResponse(t *testing.T, ref uci.ContextRef) uci.QueryResponse {
	t.Helper()
	zero := int64(0)
	truncated := false
	contexts := uci.QueryContexts{{SourceID: ref.SourceID, CheckoutID: ref.CheckoutID, ViewID: ref.ViewID, Generation: ref.Generation, ProfileID: ref.AnalysisProfileID}}
	items := uci.QueryItems{}
	warnings := uci.QueryWarnings{}
	continuation := uci.QueryContinuation{}
	response := uci.QueryResponse{
		Schema:   uci.QueryResponseSchema,
		Status:   uci.QueryStatusOK,
		Contexts: &contexts,
		Freshness: &uci.QueryFreshness{
			State:               uci.QueryFreshnessObservedCurrent,
			Method:              uci.QueryFreshnessWatchWatermark,
			PendingChanges:      &zero,
			EnrichmentWatermark: uci.QueryEnrichmentWatermark{Sequence: ref.Generation, State: uci.QueryEnrichmentCurrent},
		},
		Retrieval:    &uci.QueryRetrieval{Mode: uci.QueryRetrievalGraph, DegradationReasons: []string{}},
		Coverage:     &uci.QueryCoverage{Structural: uci.IndexCoverageComplete, UnresolvedSites: &zero, UnsupportedFiles: &zero},
		Items:        &items,
		Graph:        &uci.QueryGraph{Nodes: []uci.QueryEntityRef{}, Edges: []uci.QueryGraphEdge{}, StopReason: uci.QueryGraphComplete},
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}
	require.NoError(t, response.ValidatePreExposure())
	return response
}

func operatorCodeHTTPTestRefsEqual(left, right uci.ContextRef) bool {
	return left.SpaceID == nil && right.SpaceID == nil && left.SourceID == right.SourceID && left.CheckoutID == right.CheckoutID && left.ViewID == right.ViewID && left.AnalysisProfileID == right.AnalysisProfileID && left.Generation == right.Generation
}
