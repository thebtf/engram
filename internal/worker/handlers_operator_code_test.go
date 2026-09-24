package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
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
			name:   "grant catalog",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleContexts,
			assertResponse: func(t *testing.T, body string) {
				t.Helper()
				require.Contains(t, body, `"contexts":[{`)
				require.Contains(t, body, `"repository":"Engram"`)
				require.Contains(t, body, `"working_copy":"Studio workstation · release candidate"`)
				require.Contains(t, body, `"selection_ref":"`)
				for _, forbidden := range []string{operatorCodeHTTPTestSourceID, operatorCodeHTTPTestCheckoutID, operatorCodeHTTPTestViewID, "proof-current", "grant_ref", "nonce", "digest"} {
					require.NotContains(t, body, forbidden)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			if testCase.configure != nil {
				testCase.configure(fixture.app, fixture.ref)
			}
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

func TestOperatorCodeHTTPAdapter_CatalogKeepsWorktreesExplicitAndNoViewUnselected(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	second := fixture.ref.Clone()
	second.CheckoutID = "30000000-0000-4000-8000-000000000002"
	second.ViewID = "40000000-0000-4000-8000-000000000002"
	noViewCheckout := "30000000-0000-4000-8000-000000000003"
	otherSource := second.Clone()
	otherSource.SourceID = "20000000-0000-4000-8000-000000000002"
	otherSource.CheckoutID = "30000000-0000-4000-8000-000000000004"
	otherSource.ViewID = "40000000-0000-4000-8000-000000000004"
	fixture.contexts.entries = append(fixture.contexts.entries,
		gormdb.BrowserCodeContextCatalogEntry{SourceID: second.SourceID, SourceLabel: "Engram", CheckoutID: second.CheckoutID, CheckoutLabel: "Studio workstation · release candidate", Context: &second, ViewLabel: "feature/api"},
		gormdb.BrowserCodeContextCatalogEntry{SourceID: fixture.ref.SourceID, SourceLabel: "Engram", CheckoutID: noViewCheckout, CheckoutLabel: "Studio workstation · release candidate", IndexIntentAvailable: true},
		gormdb.BrowserCodeContextCatalogEntry{SourceID: otherSource.SourceID, SourceLabel: "Engram", CheckoutID: otherSource.CheckoutID, CheckoutLabel: "Studio workstation · release candidate", Context: &otherSource, ViewLabel: "feature/api"},
	)
	recorder := httptest.NewRecorder()
	adapter.HandleContexts(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current"}`, fixture.identity))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"working_copy":"Studio workstation · release candidate"`)
	require.Contains(t, recorder.Body.String(), `"indexed_snapshot":{"label":"feature/api"}`)
	require.Contains(t, recorder.Body.String(), `"index_intent_available":true,"index_intent_selection_ref":"`)
	var catalog operatorCodeContextsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &catalog))
	require.Len(t, catalog.Contexts, 4)
	require.Equal(t, catalog.Contexts[0].SourceRef, catalog.Contexts[1].SourceRef)
	require.NotEqual(t, catalog.Contexts[0].SourceRef, catalog.Contexts[3].SourceRef)
	for i := 0; i < len(catalog.Contexts); i++ {
		require.NotEmpty(t, catalog.Contexts[i].CheckoutRef)
		for j := i + 1; j < len(catalog.Contexts); j++ {
			require.NotEqual(t, catalog.Contexts[i].CheckoutRef, catalog.Contexts[j].CheckoutRef)
		}
	}
	for _, forbidden := range []string{second.CheckoutID, second.ViewID, noViewCheckout, otherSource.SourceID, otherSource.CheckoutID, otherSource.ViewID, operatorCodeHTTPTestProfileID, "grant_ref"} {
		require.NotContains(t, recorder.Body.String(), forbidden)
	}
}

func TestOperatorCodeHTTPAdapter_SearchContinuationStaysServerOwnedAndExactlyBound(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	internalCursor := "usc1.00000000-0000-4000-8000-000000000001"
	firstResponse := operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
	truncated := true
	firstResponse.Truncated = &truncated
	firstResponse.Continuation = &uci.QueryContinuation{Value: &internalCursor}
	require.NoError(t, firstResponse.ValidatePreExposure())
	fixture.app.searchResponses = []uci.QueryResponse{firstResponse, operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)}
	fixture.contexts.createCursorRef = "80000000-0000-4000-8000-000000000001"

	first := httptest.NewRecorder()
	adapter.HandleSearch(first, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","query":"Fixture","path_prefix":"internal","languages":["Go","go"],"limit":1}`, fixture.identity))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Contains(t, first.Body.String(), fixture.contexts.createCursorRef)
	require.NotContains(t, first.Body.String(), internalCursor)
	require.Len(t, fixture.contexts.createdBindings, 1)
	created := fixture.contexts.createdBindings[0]
	require.Equal(t, fixture.grants.current.GrantRef, created.GrantRef)
	require.Equal(t, fixture.grants.current.IssuedAt, created.GrantIssuedAt)
	require.Equal(t, fixture.ref, created.Context)
	require.Equal(t, operatorCodeHTTPTestBindingID, created.TabBindingID)
	require.Equal(t, 1, created.PageSize)
	require.Equal(t, uci.QueryFilter{PathPrefix: "internal", Languages: []string{"go"}}, fixture.app.searchSpecs[0].Filter)

	second := httptest.NewRecorder()
	adapter.HandleSearch(second, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","query":"Fixture","path_prefix":"internal","languages":["go"],"limit":1,"continuation":"`+fixture.contexts.createCursorRef+`"}`, fixture.identity))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, []string{fixture.contexts.createCursorRef}, fixture.contexts.loadedRefs)
	require.Equal(t, []string{fixture.contexts.createCursorRef}, fixture.contexts.advancedRefs)
	require.Equal(t, []gormdb.BrowserCodeContinuationBinding{created}, fixture.contexts.loadedBindings)
	require.Equal(t, []gormdb.BrowserCodeContinuationBinding{created}, fixture.contexts.advancedBindings)
	require.NotNil(t, fixture.app.searchSpecs[1].Continuation)
	require.Equal(t, internalCursor, *fixture.app.searchSpecs[1].Continuation)
	require.NotContains(t, second.Body.String(), internalCursor)

	fixture.contexts.loadErr = gormdb.ErrBrowserCodeContinuationDenied
	denied := httptest.NewRecorder()
	adapter.HandleSearch(denied, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","query":"Fixture","path_prefix":"internal","languages":["go"],"limit":1,"continuation":"80000000-0000-4000-8000-000000000099"}`, fixture.identity))
	require.Equal(t, http.StatusForbidden, denied.Code)
	require.Empty(t, denied.Body.String())
	require.Equal(t, 2, fixture.app.searchCalls)
}

func TestOperatorCodeHTTPAdapter_GraphNavigationPublishesOnlyStoredSourceDescriptors(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	available := uci.QueryEntityRef{SourceID: fixture.ref.SourceID, ViewID: fixture.ref.ViewID, EntityKey: "Fixture.Available"}
	unavailable := uci.QueryEntityRef{SourceID: fixture.ref.SourceID, ViewID: fixture.ref.ViewID, EntityKey: "Fixture.Unavailable"}
	response := operatorCodeHTTPTestGraphResponse(t, fixture.ref)
	response.Graph.Nodes = []uci.QueryEntityRef{available, unavailable}
	response.Graph.Edges = []uci.QueryGraphEdge{{
		From:         available,
		To:           unavailable,
		Relation:     uci.IndexRelation("calls"),
		EvidenceKind: uci.QueryEvidenceResolved,
		EvidenceRefs: []uci.QueryEntityRef{available},
	}}
	require.NoError(t, response.ValidatePreExposure())
	fixture.app.graph = response
	descriptor := uci.VersionedReadSpec{
		Entity:        available,
		Span:          uci.QuerySpan{ByteStart: 0, ByteEnd: 12, LineStart: 1, LineEnd: 1},
		ContentDigest: uci.QueryContentDigest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		MaxBytes:      12,
	}
	graphSources := &operatorCodeHTTPTestGraphSources{descriptors: map[uci.QueryEntityRef]uci.VersionedReadSpec{available: descriptor}}
	adapter.graphSources = graphSources
	recorder := httptest.NewRecorder()
	adapter.HandleGraph(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","action":"neighbors","target":{"entity_key":"Fixture.Available"}}`, fixture.identity))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"source_state":"available","source_read":{"entity_key":"Fixture.Available"`)
	require.Contains(t, recorder.Body.String(), `"source_state":"unavailable"`)
	require.Contains(t, recorder.Body.String(), `"relation":"calls"`)
	require.NotContains(t, recorder.Body.String(), `"excerpt"`)
	require.Equal(t, []uci.QueryEntityRef{available, unavailable}, graphSources.calls)
}

func TestOperatorCodeHTTPAdapter_GraphNavigationReleasesExactRelationSiteOnly(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	caller := uci.QueryEntityRef{SourceID: fixture.ref.SourceID, ViewID: fixture.ref.ViewID, EntityKey: "Fixture.Caller"}
	first := uci.QueryEntityRef{SourceID: fixture.ref.SourceID, ViewID: fixture.ref.ViewID, EntityKey: "Fixture.First"}
	second := uci.QueryEntityRef{SourceID: fixture.ref.SourceID, ViewID: fixture.ref.ViewID, EntityKey: "Fixture.Second"}
	site := uuid.NewString()
	response := operatorCodeHTTPTestGraphResponse(t, fixture.ref)
	response.Graph.Nodes = []uci.QueryEntityRef{caller, first, second}
	response.Graph.Edges = []uci.QueryGraphEdge{
		{From: caller, To: first, Relation: "calls", EvidenceKind: uci.QueryEvidenceResolved, EvidenceRefs: []uci.QueryEntityRef{caller}, Evidence: []uci.QueryRelationEvidence{{Ref: caller, Precision: uci.QueryEvidencePrecisionReferenceSite, ReferenceSiteID: &site}}},
		{From: caller, To: second, Relation: "may_call", EvidenceKind: uci.QueryEvidenceHeuristic, EvidenceRefs: []uci.QueryEntityRef{caller}, Evidence: []uci.QueryRelationEvidence{{Ref: caller, Precision: uci.QueryEvidencePrecisionUnsupported}}},
	}
	require.NoError(t, response.ValidatePreExposure())
	fixture.app.graph = response
	descriptor := uci.VersionedReadSpec{Entity: caller, Span: uci.QuerySpan{ByteStart: 18, ByteEnd: 25, LineStart: 2, LineEnd: 2}, ContentDigest: uci.QueryContentDigest(strings.Repeat("a", 64)), MaxBytes: 7, ReferenceSiteID: &site}
	adapter.graphSources = &operatorCodeHTTPTestGraphSources{evidenceDescriptors: map[string]uci.VersionedReadSpec{site: descriptor}}
	recorder := httptest.NewRecorder()
	adapter.HandleGraph(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","action":"neighbors","target":{"entity_key":"Fixture.Caller"}}`, fixture.identity))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body struct {
		Navigation operatorCodeGraphNavigation `json:"navigation"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Len(t, body.Navigation.Edges, 2)
	require.Equal(t, &operatorCodeSourceReadDescriptor{EntityKey: caller.EntityKey, Span: descriptor.Span, ContentDigest: descriptor.ContentDigest, ReferenceSiteID: &site}, body.Navigation.Edges[0].Evidence[0].SourceRead)
	require.Equal(t, "available", body.Navigation.Edges[0].Evidence[0].SourceState)
	require.Nil(t, body.Navigation.Edges[1].Evidence[0].SourceRead)
	require.Equal(t, "unavailable", body.Navigation.Edges[1].Evidence[0].SourceState)
	require.Equal(t, []uci.QueryRelationEvidence{response.Graph.Edges[0].Evidence[0]}, adapter.graphSources.(*operatorCodeHTTPTestGraphSources).evidenceCalls)
}

func TestOperatorCodeHTTPAdapter_ReferenceSiteSourceReadUsesExactPinnedKey(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.app.read = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalExact)
	site := uuid.NewString()
	request := `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","entity_key":"Fixture.Symbol","span":{"byte_start":0,"byte_end":12,"line_start":1,"line_end":1},"content_digest":"` + strings.Repeat("a", 64) + `","reference_site_id":"` + site + `"}`
	recorder := httptest.NewRecorder()
	adapter.HandleVersionedRead(recorder, operatorCodeHTTPTestRequest(t, request, fixture.identity))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Len(t, fixture.app.readInputs, 1)
	require.Equal(t, site, *fixture.app.readInputs[0].ReferenceSiteID)
	require.Equal(t, fixture.ref.SourceID, fixture.app.readInputs[0].Ref.SourceID)
	require.Equal(t, fixture.ref.ViewID, fixture.app.readInputs[0].Ref.ViewID)
}

func TestOperatorCodeGraphRequestInputBuildsNormalizedSemantics(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	text := func(value string) *string { return &value }
	integer := func(value int) *int { return &value }
	milliseconds := func(value int64) *int64 { return &value }
	for _, testCase := range []struct {
		name    string
		request operatorCodeGraphRequest
		assert  func(uci.GraphSpec, int64)
	}{
		{
			name:    "defaults",
			request: operatorCodeGraphRequest{Action: " Neighbors ", Target: &operatorCodeGraphTargetRequest{EntityKey: text(" Fixture.Symbol ")}},
			assert: func(input uci.GraphSpec, deadlineMS int64) {
				require.Equal(t, uci.GraphActionNeighbors, input.Action)
				require.Equal(t, uci.GraphTarget{EntityKey: "Fixture.Symbol"}, input.Target)
				require.Nil(t, input.Destination)
				require.Equal(t, uci.GraphFilter{Direction: uci.GraphDirectionBoth}, input.Filter)
				require.Equal(t, uci.GraphBudget{MaxDepth: 4, MaxVisited: 64, MaxNodes: 32, MaxEdges: 64, Deadline: now.Add(30 * time.Second)}, input.Budget)
				require.EqualValues(t, 30_000, deadlineMS)
				require.Nil(t, input.Continuation)
			},
		},
		{
			name: "path normalizes filters and bounds",
			request: operatorCodeGraphRequest{
				Action:        "PATH",
				Target:        &operatorCodeGraphTargetRequest{Name: text(" Source.Name ")},
				Destination:   &operatorCodeGraphTargetRequest{EntityKey: text(" Destination.Key ")},
				Direction:     text(" OUTGOING "),
				Relations:     []string{"calls", " IMPORTS ", "calls"},
				EvidenceKinds: []string{"semantic", " EXTRACTED ", "semantic"},
				MaxDepth:      integer(operatorCodeGraphMaxDepth),
				MaxVisited:    integer(operatorCodeGraphMaxVisited),
				MaxNodes:      integer(operatorCodeGraphMaxNodes),
				MaxEdges:      integer(operatorCodeGraphMaxEdges),
				DeadlineMS:    milliseconds(operatorCodeGraphMaxWaitMS),
				Continuation:  text("cursor-1"),
			},
			assert: func(input uci.GraphSpec, deadlineMS int64) {
				require.Equal(t, uci.GraphActionPath, input.Action)
				require.Equal(t, uci.GraphTarget{Name: "Source.Name"}, input.Target)
				require.Equal(t, &uci.GraphTarget{EntityKey: "Destination.Key"}, input.Destination)
				require.Equal(t, uci.GraphFilter{Direction: uci.GraphDirectionOutgoing, Relations: []uci.IndexRelation{"calls", "imports"}, EvidenceKinds: []uci.QueryEvidenceKind{uci.QueryEvidenceExtracted, uci.QueryEvidenceSemantic}}, input.Filter)
				require.Equal(t, uci.GraphBudget{MaxDepth: operatorCodeGraphMaxDepth, MaxVisited: operatorCodeGraphMaxVisited, MaxNodes: operatorCodeGraphMaxNodes, MaxEdges: operatorCodeGraphMaxEdges, Deadline: now.Add(time.Duration(operatorCodeGraphMaxWaitMS) * time.Millisecond)}, input.Budget)
				require.Equal(t, operatorCodeGraphMaxWaitMS, deadlineMS)
				require.Equal(t, "cursor-1", *input.Continuation)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input, deadlineMS, valid := testCase.request.input(now)
			require.True(t, valid)
			testCase.assert(input, deadlineMS)
		})
	}
}

func TestOperatorCodeGraphRequestInputRejectsInvalidComponents(t *testing.T) {
	text := func(value string) *string { return &value }
	integer := func(value int) *int { return &value }
	validTarget := &operatorCodeGraphTargetRequest{EntityKey: text("Fixture.Symbol")}
	for _, testCase := range []struct {
		name    string
		request operatorCodeGraphRequest
	}{
		{name: "unknown action", request: operatorCodeGraphRequest{Action: "walk", Target: validTarget}},
		{name: "missing target", request: operatorCodeGraphRequest{Action: "neighbors"}},
		{name: "dual target", request: operatorCodeGraphRequest{Action: "neighbors", Target: &operatorCodeGraphTargetRequest{EntityKey: text("key"), Name: text("name")}}},
		{name: "blank target text", request: operatorCodeGraphRequest{Action: "neighbors", Target: &operatorCodeGraphTargetRequest{Name: text(" ")}}},
		{name: "invalid target text", request: operatorCodeGraphRequest{Action: "neighbors", Target: &operatorCodeGraphTargetRequest{EntityKey: text(string([]byte{0xff}))}}},
		{name: "destination without path", request: operatorCodeGraphRequest{Action: "neighbors", Target: validTarget, Destination: &operatorCodeGraphTargetRequest{Name: text("destination")}}},
		{name: "path without destination", request: operatorCodeGraphRequest{Action: "path", Target: validTarget}},
		{name: "dual destination", request: operatorCodeGraphRequest{Action: "path", Target: validTarget, Destination: &operatorCodeGraphTargetRequest{EntityKey: text("key"), Name: text("name")}}},
		{name: "invalid filter", request: operatorCodeGraphRequest{Action: "neighbors", Target: validTarget, Direction: text("sideways")}},
		{name: "invalid budget", request: operatorCodeGraphRequest{Action: "neighbors", Target: validTarget, MaxDepth: integer(-1)}},
		{name: "invalid continuation", request: operatorCodeGraphRequest{Action: "neighbors", Target: validTarget, Continuation: text("")}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, valid := testCase.request.input(time.Time{})
			require.False(t, valid)
		})
	}
}

func TestOperatorCodeGraphFilterNormalizesAndRejectsValues(t *testing.T) {
	text := func(value string) *string { return &value }
	for _, testCase := range []struct {
		name      string
		request   operatorCodeGraphRequest
		want      uci.GraphFilter
		wantValid bool
	}{
		{name: "defaults", want: uci.GraphFilter{Direction: uci.GraphDirectionBoth}, wantValid: true},
		{name: "normalized", request: operatorCodeGraphRequest{Direction: text(" incoming "), Relations: []string{"calls", "imports", "calls"}, EvidenceKinds: []string{"resolved", "heuristic", "resolved"}}, want: uci.GraphFilter{Direction: uci.GraphDirectionIncoming, Relations: []uci.IndexRelation{"calls", "imports"}, EvidenceKinds: []uci.QueryEvidenceKind{uci.QueryEvidenceHeuristic, uci.QueryEvidenceResolved}}, wantValid: true},
		{name: "invalid direction", request: operatorCodeGraphRequest{Direction: text("sideways")}},
		{name: "invalid relation", request: operatorCodeGraphRequest{Relations: []string{"unknown"}}},
		{name: "too many relations", request: operatorCodeGraphRequest{Relations: make([]string, 33)}},
		{name: "invalid evidence kind", request: operatorCodeGraphRequest{EvidenceKinds: []string{"unknown"}}},
		{name: "too many evidence kinds", request: operatorCodeGraphRequest{EvidenceKinds: make([]string, 5)}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			filter, valid := operatorCodeGraphFilter(testCase.request)
			require.Equal(t, testCase.wantValid, valid)
			if valid {
				require.Equal(t, testCase.want, filter)
			}
		})
	}
}

func TestOperatorCodeGraphBudgetAndContinuationEnforceBounds(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	integer := func(value int) *int { return &value }
	milliseconds := func(value int64) *int64 { return &value }
	text := func(value string) *string { return &value }
	for _, testCase := range []struct {
		name    string
		request operatorCodeGraphRequest
	}{
		{name: "negative depth", request: operatorCodeGraphRequest{MaxDepth: integer(-1)}},
		{name: "visited below minimum", request: operatorCodeGraphRequest{MaxVisited: integer(0)}},
		{name: "nodes above maximum", request: operatorCodeGraphRequest{MaxNodes: integer(operatorCodeGraphMaxNodes + 1)}},
		{name: "edges above maximum", request: operatorCodeGraphRequest{MaxEdges: integer(operatorCodeGraphMaxEdges + 1)}},
		{name: "deadline below minimum", request: operatorCodeGraphRequest{DeadlineMS: milliseconds(0)}},
		{name: "deadline above maximum", request: operatorCodeGraphRequest{DeadlineMS: milliseconds(operatorCodeGraphMaxWaitMS + 1)}},
		{name: "empty continuation", request: operatorCodeGraphRequest{Continuation: text("")}},
		{name: "whitespace continuation", request: operatorCodeGraphRequest{Continuation: text(" cursor")}},
		{name: "control continuation", request: operatorCodeGraphRequest{Continuation: text("cursor\n")}},
		{name: "oversized continuation", request: operatorCodeGraphRequest{Continuation: text(string(bytes.Repeat([]byte("x"), 2_049)))}},
		{name: "invalid continuation text", request: operatorCodeGraphRequest{Continuation: text(string([]byte{0xff}))}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.request.Continuation != nil {
				_, valid := operatorCodeGraphContinuation(testCase.request)
				require.False(t, valid)
				return
			}
			_, _, valid := operatorCodeGraphBudget(testCase.request, now)
			require.False(t, valid)
		})
	}
}

func TestOperatorCodeHTTPAdapter_BindsReleasedReadsToBrowserSession(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		body      string
		invoke    func(*OperatorCodeHTTPAdapter, http.ResponseWriter, *http.Request)
		configure func(*operatorCodeHTTPTestApplication, uci.ContextRef)
	}{
		{
			name:   "search",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleSearch,
			configure: func(app *operatorCodeHTTPTestApplication, ref uci.ContextRef) {
				app.search = operatorCodeHTTPTestQueryResponse(t, ref, uci.QueryRetrievalLexical)
			},
		},
		{
			name:   "graph",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","action":"neighbors","target":{"entity_key":"Fixture.Symbol"}}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleGraph,
			configure: func(app *operatorCodeHTTPTestApplication, ref uci.ContextRef) {
				app.graph = operatorCodeHTTPTestGraphResponse(t, ref)
			},
		},
		{
			name:   "versioned read",
			body:   `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","entity_key":"Fixture.Symbol","span":{"byte_start":0,"byte_end":12,"line_start":1,"line_end":1},"content_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			invoke: (*OperatorCodeHTTPAdapter).HandleVersionedRead,
			configure: func(app *operatorCodeHTTPTestApplication, ref uci.ContextRef) {
				app.read = operatorCodeHTTPTestQueryResponse(t, ref, uci.QueryRetrievalExact)
			},
		},
	} {
		for _, inheritedSession := range []string{"", "forged-client-session"} {
			t.Run(testCase.name+"/"+map[bool]string{true: "forged", false: "absent"}[inheritedSession != ""], func(t *testing.T) {
				adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
				testCase.configure(fixture.app, fixture.ref)
				request := operatorCodeHTTPTestRequest(t, testCase.body, fixture.identity)
				request = request.WithContext(auditcontext.WithSourceSession(request.Context(), inheritedSession))
				recorder := httptest.NewRecorder()

				testCase.invoke(adapter, recorder, request)

				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				require.Equal(t, []string{"browser-session-41"}, fixture.app.sourceSessions)
				require.Equal(t, []string{"browser-session-41"}, fixture.recorder.sourceSessions)
			})
		}
	}
}

func TestOperatorCodeHTTPAdapter_UsesAuthenticatedSessionContext(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.binding.expectedSessionID = authentikBrowserSessionID(41)
	fixture.app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
	request := operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","query":"Fixture"}`, fixture.identity)
	request = request.WithContext(withAuthenticatedBrowserSession(request.Context(), authentikBrowserSessionID(41)))
	recorder := httptest.NewRecorder()

	adapter.HandleSearch(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, []string{authentikBrowserSessionID(41)}, fixture.app.sourceSessions)
}

func TestOperatorCodeHTTPAdapter_UsesGrantedSourceRealmForRelease(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	identity := operatorCodeRequestIdentity{identity: fixture.identity, sessionID: "browser-session-41"}
	caller, failure := adapter.authorize(context.Background(), identity, BrowserBindingProof{TabBindingID: operatorCodeHTTPTestBindingID, DocumentProof: "proof-current"})
	require.Equal(t, uci.ReleaseFailureNone, failure)
	require.Equal(t, fixture.grants.current.AuthRealm, adapter.releaseRequest(identity, caller, uci.ReleaseCategoryCodeSearch, nil, nil).AuthRealm)
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
			name:     "different browser subject",
			identity: auth.SessionForBrowserUser("other", 42),
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
			want: http.StatusForbidden,
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

func TestOperatorCodeHTTPAdapter_IndexIntentAcknowledgementIsNotCompletion(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	body := `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","request_ref":"lost-response","kind":"reindex"}`

	first := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(first, operatorCodeHTTPTestRequest(t, body, fixture.identity))
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	firstResponse := operatorCodeHTTPTestIndexIntentResponse(t, first.Body.String())
	require.Equal(t, string(uci.IndexIntentSubmitted), firstResponse["state"])
	require.NotContains(t, first.Body.String(), `"result"`)
	operatorCodeHTTPTestRequireSafeIndexIntentResponse(t, first.Body.String(), false, fixture)

	// The client lost the first response; repeating the same request gets the
	// same durable intent rather than a second execution.
	replay := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(replay, operatorCodeHTTPTestRequest(t, body, fixture.identity))
	require.Equal(t, http.StatusAccepted, replay.Code, replay.Body.String())
	replayResponse := operatorCodeHTTPTestIndexIntentResponse(t, replay.Body.String())
	require.Equal(t, firstResponse["intent_ref"], replayResponse["intent_ref"])
	require.Len(t, fixture.app.indexIntents, 1)

	lateCompleted := operatorCodeHTTPTestIndexIntent(fixture.ref, "late-completed", uci.IndexIntentReindex, uci.IndexIntentCompleted)
	fixture.app.indexIntents = map[string]uci.IndexIntent{"late-completed": lateCompleted}
	lateReplay := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(lateReplay, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"late-completed","kind":"reindex"}`, fixture.identity))
	require.Equal(t, http.StatusAccepted, lateReplay.Code, lateReplay.Body.String())
	lateResponse := operatorCodeHTTPTestIndexIntentResponse(t, lateReplay.Body.String())
	require.Equal(t, lateCompleted.ID, lateResponse["intent_ref"])
	require.Equal(t, string(uci.IndexIntentSubmitted), lateResponse["state"])
	operatorCodeHTTPTestRequireSafeIndexIntentResponse(t, lateReplay.Body.String(), false, fixture)
	require.Equal(t, []string{"browser-session-41", "browser-session-41", "browser-session-41"}, fixture.app.indexSubmitSessions)

	completed := operatorCodeHTTPTestIndexIntent(fixture.ref, "completed-status", uci.IndexIntentReindex, uci.IndexIntentCompleted)
	fixture.app.indexIntents = map[string]uci.IndexIntent{completed.ID: completed}
	status := httptest.NewRecorder()
	adapter.HandleIndexIntentStatus(status, operatorCodeHTTPTestIndexIntentStatusRequest(t, completed.ID, "", fixture.identity))
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	statusResponse := operatorCodeHTTPTestIndexIntentResponse(t, status.Body.String())
	require.Equal(t, string(uci.IndexIntentCompleted), statusResponse["state"])
	result, ok := statusResponse["result"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, completed.ResultView.ViewID, result["view_ref"])
	require.Equal(t, float64(completed.ResultView.Generation), result["generation"])
	operatorCodeHTTPTestRequireSafeIndexIntentResponse(t, status.Body.String(), true, fixture)
	require.Empty(t, fixture.recorder.inputs, "code_index_result is non-content release only")
	require.Equal(t, []string{"browser-session-41"}, fixture.app.indexGetSessions)
}

func TestOperatorCodeHTTPAdapter_NoViewIndexIntentReauthorizesWithoutPin(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.binding.pinned = nil
	fixture.contexts.entries[0].Context = nil
	target := `{"selection_ref":"` + operatorCodeIndexSelectionRef(operatorCodeHTTPTestSourceID, operatorCodeHTTPTestCheckoutID, operatorCodeHTTPTestProfileID) + `"}`
	body := `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","request_ref":"first-index","kind":"reindex","target":` + target + `}`

	first := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(first, operatorCodeHTTPTestRequest(t, body, fixture.identity))
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	response := operatorCodeHTTPTestIndexIntentResponse(t, first.Body.String())
	intentRef, ok := response["intent_ref"].(string)
	require.True(t, ok)
	stored := fixture.app.indexIntents["first-index"]
	require.Nil(t, stored.PreviousView)
	require.Len(t, fixture.contexts.initialTargets, 1)
	require.Equal(t, operatorCodeHTTPTestProfileID, fixture.contexts.initialTargets[0].ProfileID, "the server must derive the daemon-advertised profile")
	require.Nil(t, fixture.binding.pinned, "first-index admission must not pin a View")
	adapter.indexTargets = operatorCodeHTTPTestExpiredIndexTargets{}

	replay := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(replay, operatorCodeHTTPTestRequest(t, body, fixture.identity))
	require.Equal(t, http.StatusAccepted, replay.Code, replay.Body.String())
	require.Equal(t, intentRef, operatorCodeHTTPTestIndexIntentResponse(t, replay.Body.String())["intent_ref"])
	require.Len(t, fixture.app.indexIntents, 1)
	require.Len(t, fixture.contexts.reauthTargets, 1, "replay must reauthorize the durable target")

	newAfterAdvertisementExpiry := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(newAfterAdvertisementExpiry, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"new-after-advertisement-expiry","kind":"reindex","target":`+target+`}`, fixture.identity))
	require.Equal(t, http.StatusForbidden, newAfterAdvertisementExpiry.Code, newAfterAdvertisementExpiry.Body.String())
	require.Len(t, fixture.contexts.initialTargets, 1, "a new request still requires a fresh daemon advertisement")

	fixture.contexts.reauthTargetErr = gormdb.ErrBrowserCodeContextDenied
	revokedReplay := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(revokedReplay, operatorCodeHTTPTestRequest(t, body, fixture.identity))
	require.Equal(t, http.StatusForbidden, revokedReplay.Code, revokedReplay.Body.String())
	fixture.contexts.reauthTargetErr = nil

	changed := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(changed, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"first-index","kind":"reconcile","target":`+target+`}`, fixture.identity))
	require.Equal(t, http.StatusConflict, changed.Code, changed.Body.String())

	stored.State = uci.IndexIntentUnavailable
	fixture.app.indexIntents["first-index"] = stored
	status := httptest.NewRecorder()
	adapter.HandleIndexIntentStatus(status, operatorCodeHTTPTestIndexIntentStatusRequest(t, intentRef, "", fixture.identity))
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	require.True(t, operatorCodeHTTPTestIndexIntentResponse(t, status.Body.String())["retryable"].(bool))

	retry := httptest.NewRecorder()
	adapter.HandleIndexIntentRetry(retry, operatorCodeHTTPTestIndexIntentRetryRequest(t, intentRef, fixture.identity))
	require.Equal(t, http.StatusAccepted, retry.Code, retry.Body.String())
	require.Equal(t, string(uci.IndexIntentQueued), operatorCodeHTTPTestIndexIntentResponse(t, retry.Body.String())["state"])

	completed := operatorCodeHTTPTestNoViewIndexIntent(stored.Scope, stored.ProfileID, stored.RequestRef, stored.Kind, uci.IndexIntentCompleted)
	completed.ID = stored.ID
	claim, err := uci.NewIndexIntentClaim(completed.ID, "private-index-owner", 1, completed.CreatedAt)
	require.NoError(t, err)
	completed.Acknowledgement = &claim
	fixture.app.indexIntents["first-index"] = completed
	completedStatus := httptest.NewRecorder()
	adapter.HandleIndexIntentStatus(completedStatus, operatorCodeHTTPTestIndexIntentStatusRequest(t, intentRef, "", fixture.identity))
	require.Equal(t, http.StatusOK, completedStatus.Code, completedStatus.Body.String())
	result, ok := operatorCodeHTTPTestIndexIntentResponse(t, completedStatus.Body.String())["result"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, completed.ResultView.ViewID, result["view_ref"])
	require.Nil(t, fixture.binding.pinned, "completed first-index status must not select its result")

	fixture.contexts.reauthTargetErr = gormdb.ErrBrowserCodeContextDenied
	revokedStatus := httptest.NewRecorder()
	adapter.HandleIndexIntentStatus(revokedStatus, operatorCodeHTTPTestIndexIntentStatusRequest(t, intentRef, "", fixture.identity))
	require.Equal(t, http.StatusForbidden, revokedStatus.Code, revokedStatus.Body.String())
	fixture.contexts.reauthTargetErr = nil

	foreign := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(foreign, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"foreign","kind":"reindex","target":{"selection_ref":"`+operatorCodeIndexSelectionRef(uuid.NewString(), operatorCodeHTTPTestCheckoutID, operatorCodeHTTPTestProfileID)+`"}}`, fixture.identity))
	require.Equal(t, http.StatusForbidden, foreign.Code, foreign.Body.String())

	fixture.contexts.initialTargetErr = gormdb.ErrBrowserCodeContextDenied
	revoked := httptest.NewRecorder()
	adapter.HandleIndexIntentSubmit(revoked, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"revoked","kind":"reindex","target":`+target+`}`, fixture.identity))
	require.Equal(t, http.StatusForbidden, revoked.Code, revoked.Body.String())
}

func TestOperatorCodeHTTPAdapter_IndexIntentIdempotencyAndRetry(t *testing.T) {
	t.Run("changed binding is rejected without a second intent", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		first := httptest.NewRecorder()
		adapter.HandleIndexIntentSubmit(first, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"same-client-request","kind":"reindex"}`, fixture.identity))
		require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())

		changed := httptest.NewRecorder()
		adapter.HandleIndexIntentSubmit(changed, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"same-client-request","kind":"reconcile"}`, fixture.identity))
		require.Equal(t, http.StatusConflict, changed.Code, changed.Body.String())
		require.Empty(t, changed.Body.String())
		require.Len(t, fixture.app.indexIntents, 1)
	})

	t.Run("unavailable intent retries once into queued", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		unavailable := operatorCodeHTTPTestIndexIntent(fixture.ref, "offline-request", uci.IndexIntentReindex, uci.IndexIntentUnavailable)
		fixture.app.indexIntents = map[string]uci.IndexIntent{unavailable.ID: unavailable}

		before := httptest.NewRecorder()
		adapter.HandleIndexIntentStatus(before, operatorCodeHTTPTestIndexIntentStatusRequest(t, unavailable.ID, "", fixture.identity))
		require.Equal(t, http.StatusOK, before.Code, before.Body.String())
		require.Equal(t, true, operatorCodeHTTPTestIndexIntentResponse(t, before.Body.String())["retryable"])

		retry := httptest.NewRecorder()
		adapter.HandleIndexIntentRetry(retry, operatorCodeHTTPTestIndexIntentRetryRequest(t, unavailable.ID, fixture.identity))
		require.Equal(t, http.StatusAccepted, retry.Code, retry.Body.String())
		retryResponse := operatorCodeHTTPTestIndexIntentResponse(t, retry.Body.String())
		require.Equal(t, string(uci.IndexIntentQueued), retryResponse["state"])
		require.NotContains(t, retry.Body.String(), `"result"`)
		require.Equal(t, 1, fixture.app.indexRetryExecutions)

		replayedRetry := httptest.NewRecorder()
		adapter.HandleIndexIntentRetry(replayedRetry, operatorCodeHTTPTestIndexIntentRetryRequest(t, unavailable.ID, fixture.identity))
		require.Equal(t, http.StatusConflict, replayedRetry.Code, replayedRetry.Body.String())
		require.Empty(t, replayedRetry.Body.String())
		require.Equal(t, 1, fixture.app.indexRetryExecutions)
		require.Equal(t, []string{"browser-session-41", "browser-session-41"}, fixture.app.indexRetrySessions)
	})

	t.Run("completed retry return is a contract failure", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		unavailable := operatorCodeHTTPTestIndexIntent(fixture.ref, "completed-retry", uci.IndexIntentReindex, uci.IndexIntentUnavailable)
		completed := operatorCodeHTTPTestIndexIntent(fixture.ref, "completed-retry", uci.IndexIntentReindex, uci.IndexIntentCompleted)
		fixture.app.indexIntents = map[string]uci.IndexIntent{unavailable.ID: unavailable}
		fixture.app.indexRetryResult = &completed
		recorder := httptest.NewRecorder()
		adapter.HandleIndexIntentRetry(recorder, operatorCodeHTTPTestIndexIntentRetryRequest(t, unavailable.ID, fixture.identity))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		require.Empty(t, recorder.Body.String())
		require.Equal(t, []string{"browser-session-41"}, fixture.app.indexRetrySessions)
	})
}

func TestOperatorCodeHTTPAdapter_IndexIntentStatusValidatesEveryState(t *testing.T) {
	for _, state := range []uci.IndexIntentState{
		uci.IndexIntentSubmitted,
		uci.IndexIntentQueued,
		uci.IndexIntentAcknowledged,
		uci.IndexIntentRunning,
		uci.IndexIntentCompleted,
		uci.IndexIntentUnavailable,
		uci.IndexIntentFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			intent := operatorCodeHTTPTestIndexIntent(fixture.ref, "state-"+string(state), uci.IndexIntentReindex, state)
			fixture.app.indexIntents = map[string]uci.IndexIntent{intent.ID: intent}
			recorder := httptest.NewRecorder()
			adapter.HandleIndexIntentStatus(recorder, operatorCodeHTTPTestIndexIntentStatusRequest(t, intent.ID, "", fixture.identity))

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			response := operatorCodeHTTPTestIndexIntentResponse(t, recorder.Body.String())
			require.Equal(t, string(state), response["state"])
			require.Equal(t, float64(intent.Attempt), response["attempt"])
			require.Equal(t, state == uci.IndexIntentUnavailable, response["retryable"])
			operatorCodeHTTPTestRequireSafeIndexIntentResponse(t, recorder.Body.String(), state == uci.IndexIntentCompleted, fixture)
			require.Empty(t, fixture.recorder.inputs)
		})
	}
}

func TestOperatorCodeHTTPAdapter_IndexIntentFailsClosedAndConcealsReleasedResults(t *testing.T) {
	t.Run("missing capability", func(t *testing.T) {
		_, fixture := newOperatorCodeHTTPTestAdapter(t)
		adapter := NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, operatorCodeHTTPTestApplicationWithoutIndexIntent{base: fixture.app}, fixture.recorder)
		recorder := httptest.NewRecorder()
		adapter.HandleIndexIntentSubmit(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"missing-capability","kind":"reindex"}`, fixture.identity))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		require.Empty(t, recorder.Body.String())
	})

	t.Run("revoked grant", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		fixture.grants.allowed = false
		recorder := httptest.NewRecorder()
		adapter.HandleIndexIntentSubmit(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"revoked","kind":"reindex"}`, fixture.identity))
		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		require.Empty(t, recorder.Body.String())
	})

	t.Run("wrong returned context", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		intent := operatorCodeHTTPTestIndexIntent(fixture.ref, "wrong-context", uci.IndexIntentReindex, uci.IndexIntentSubmitted)
		intent.PreviousView.ViewID = uuid.NewString()
		fixture.app.indexIntents = map[string]uci.IndexIntent{intent.ID: intent}
		recorder := httptest.NewRecorder()
		adapter.HandleIndexIntentStatus(recorder, operatorCodeHTTPTestIndexIntentStatusRequest(t, intent.ID, "", fixture.identity))
		require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
		require.Empty(t, recorder.Body.String())
	})

	t.Run("invalid returned shape", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		intent := operatorCodeHTTPTestIndexIntent(fixture.ref, "invalid-shape", uci.IndexIntentReindex, uci.IndexIntentSubmitted)
		intent.CreatedAt = time.Time{}
		fixture.app.indexIntents = map[string]uci.IndexIntent{intent.ID: intent}
		recorder := httptest.NewRecorder()
		adapter.HandleIndexIntentStatus(recorder, operatorCodeHTTPTestIndexIntentStatusRequest(t, intent.ID, "", fixture.identity))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		require.Empty(t, recorder.Body.String())
	})

	t.Run("ordinary application failure", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		fixture.app.indexGetErr = errors.New("index service offline")
		recorder := httptest.NewRecorder()
		adapter.HandleIndexIntentStatus(recorder, operatorCodeHTTPTestIndexIntentStatusRequest(t, uuid.NewString(), "", fixture.identity))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		require.Empty(t, recorder.Body.String())
	})

	for _, testCase := range []struct {
		name      string
		configure func(*operatorCodeHTTPTestFixture)
	}{
		{
			name: "binding release failure",
			configure: func(fixture *operatorCodeHTTPTestFixture) {
				fixture.binding.failOnCall = 2
			},
		},
		{
			name: "grant revocation during release",
			configure: func(fixture *operatorCodeHTTPTestFixture) {
				fixture.grants.revokeOnSecondCheck = true
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			intent := operatorCodeHTTPTestIndexIntent(fixture.ref, "concealed-"+testCase.name, uci.IndexIntentReindex, uci.IndexIntentCompleted)
			fixture.app.indexIntents = map[string]uci.IndexIntent{intent.ID: intent}
			testCase.configure(fixture)
			recorder := httptest.NewRecorder()
			adapter.HandleIndexIntentStatus(recorder, operatorCodeHTTPTestIndexIntentStatusRequest(t, intent.ID, "", fixture.identity))

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.NotContains(t, recorder.Body.String(), `"result"`)
			operatorCodeHTTPTestRequireSafeIndexIntentResponse(t, recorder.Body.String(), false, fixture)
			require.Empty(t, fixture.recorder.inputs)
		})
	}
}

func TestOperatorCodeHTTPAdapter_IndexIntentStatusRequiresHeaderProof(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		body      string
		configure func(*http.Request)
		want      int
	}{
		{name: "header proof", want: http.StatusOK},
		{name: "query proof", configure: func(request *http.Request) { request.URL.RawQuery = "document_proof=proof-current" }, want: http.StatusBadRequest},
		{name: "unknown query", configure: func(request *http.Request) { request.URL.RawQuery = "unexpected=value" }, want: http.StatusBadRequest},
		{name: "body proof", body: `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current"}`, want: http.StatusBadRequest},
		{name: "missing proof header", configure: func(request *http.Request) { request.Header.Del("X-Engram-Document-Proof") }, want: http.StatusBadRequest},
		{name: "missing binding header", configure: func(request *http.Request) { request.Header.Del("X-Engram-Tab-Binding-ID") }, want: http.StatusBadRequest},
		{name: "duplicate proof header", configure: func(request *http.Request) { request.Header.Add("X-Engram-Document-Proof", "proof-second") }, want: http.StatusBadRequest},
		{name: "oversized proof header", configure: func(request *http.Request) {
			request.Header.Set("X-Engram-Document-Proof", string(bytes.Repeat([]byte("x"), 257)))
		}, want: http.StatusBadRequest},
		{name: "wrong method", configure: func(request *http.Request) { request.Method = http.MethodPost }, want: http.StatusBadRequest},
		{name: "invalid path reference", configure: func(request *http.Request) {
			routeContext := chi.RouteContext(request.Context())
			routeContext.URLParams.Values[0] = "not-an-intent"
		}, want: http.StatusBadRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
			intent := operatorCodeHTTPTestIndexIntent(fixture.ref, "header-rules", uci.IndexIntentReindex, uci.IndexIntentSubmitted)
			fixture.app.indexIntents = map[string]uci.IndexIntent{intent.ID: intent}
			request := operatorCodeHTTPTestIndexIntentStatusRequest(t, intent.ID, testCase.body, fixture.identity)
			if testCase.configure != nil {
				testCase.configure(request)
			}
			recorder := httptest.NewRecorder()
			adapter.HandleIndexIntentStatus(recorder, request)

			require.Equal(t, testCase.want, recorder.Code, recorder.Body.String())
			if testCase.want == http.StatusOK {
				operatorCodeHTTPTestRequireSafeIndexIntentResponse(t, recorder.Body.String(), false, fixture)
			} else {
				require.Empty(t, recorder.Body.String())
				require.Zero(t, fixture.app.indexGetCalls)
			}
		})
	}
}

func TestOperatorCodeHTTPAdapter_IndexIntentDigestIsCanonical(t *testing.T) {
	proof := BrowserBindingProof{TabBindingID: operatorCodeHTTPTestBindingID, DocumentProof: "proof-current"}
	first := operatorCodeIndexIntentDigest("operator-code-index-intent-submit", proof, "", "request-1", uci.IndexIntentReindex)
	second := operatorCodeIndexIntentDigest("operator-code-index-intent-submit", proof, "", "request-1", uci.IndexIntentReindex)
	require.Equal(t, first, second)
	require.NotEqual(t, first, operatorCodeIndexIntentDigest("operator-code-index-intent-submit", proof, "", "request-2", uci.IndexIntentReindex))
}

func TestOperatorCodeHTTPAdapter_GrantEmptyEnvelopeIsCanonical(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	for _, testCase := range []struct {
		endpoint string
		method   string
	}{
		{endpoint: "operator-code-grant-choices", method: http.MethodGet},
		{endpoint: "operator-code-grant-revoke", method: http.MethodPost},
	} {
		t.Run(testCase.endpoint, func(t *testing.T) {
			request := operatorCodeHTTPTestRequest(t, "", fixture.identity)
			request.Method = testCase.method
			request.URL.Path = "/api/code/grants"
			recorder := httptest.NewRecorder()
			identity, guarded, ok := adapter.decodeEmptyEnvelope(recorder, request, testCase.endpoint, testCase.method)
			require.True(t, ok, recorder.Body.String())
			require.Equal(t, operatorCodeRequestDigest(testCase.endpoint, nil), identity.digest)
			require.Equal(t, "browser-session-41", auditcontext.SourceSession(guarded.Context()))

			invalid := operatorCodeHTTPTestRequest(t, `{}`, fixture.identity)
			invalid.Method = testCase.method
			invalid.URL.Path = "/api/code/grants"
			rejected := httptest.NewRecorder()
			_, _, ok = adapter.decodeEmptyEnvelope(rejected, invalid, testCase.endpoint, testCase.method)
			require.False(t, ok)
			require.Equal(t, http.StatusBadRequest, rejected.Code)
		})
	}
}

func TestOperatorCodeHTTPAdapter_GrantHandlersCarryAuditBreadcrumb(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	onboarding := &operatorCodeGrantBreadcrumbApplication{}
	adapter.onboarding = onboarding

	choices := operatorCodeHTTPTestRequest(t, "", fixture.identity)
	choices.Method = http.MethodGet
	choicesRecorder := httptest.NewRecorder()
	adapter.HandleGrantChoices(choicesRecorder, choices)
	require.Equal(t, http.StatusOK, choicesRecorder.Code, choicesRecorder.Body.String())

	revoke := operatorCodeHTTPTestRequest(t, "", fixture.identity)
	grantRef := uuid.NewString()
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("grant_ref", grantRef)
	revoke = revoke.WithContext(context.WithValue(revoke.Context(), chi.RouteCtxKey, routeContext))
	revokeRecorder := httptest.NewRecorder()
	adapter.HandleGrantRevoke(revokeRecorder, revoke)
	require.Equal(t, http.StatusOK, revokeRecorder.Code, revokeRecorder.Body.String())
	require.Equal(t, []string{"browser-session-41", "browser-session-41"}, onboarding.sessions)
}

type operatorCodeGrantBreadcrumbApplication struct {
	sessions []string
}

func (application *operatorCodeGrantBreadcrumbApplication) ListOwnerChoices(ctx context.Context, _ auth.Identity) ([]gormdb.BrowserReadGrantOwnerChoice, error) {
	application.sessions = append(application.sessions, auditcontext.SourceSession(ctx))
	return nil, nil
}

func (*operatorCodeGrantBreadcrumbApplication) ListTargetChoices(context.Context, auth.Identity) ([]gormdb.BrowserReadGrantTargetChoice, error) {
	return nil, nil
}

func (*operatorCodeGrantBreadcrumbApplication) IssueOnboarding(context.Context, auth.Identity, IssueOnboardingCodeGrantInput) (gormdb.BrowserReadGrant, error) {
	return gormdb.BrowserReadGrant{}, nil
}

func (*operatorCodeGrantBreadcrumbApplication) SetWorkingCopyLabel(context.Context, auth.Identity, string, string) (gormdb.BrowserReadGrantOwnerChoice, error) {
	return gormdb.BrowserReadGrantOwnerChoice{}, nil
}

func (application *operatorCodeGrantBreadcrumbApplication) Revoke(ctx context.Context, _ auth.Identity, _ string) (gormdb.BrowserReadGrant, error) {
	application.sessions = append(application.sessions, auditcontext.SourceSession(ctx))
	return gormdb.BrowserReadGrant{}, nil
}

func TestOperatorCodeHTTPAdapter_CatalogAndPinFailuresStayPrivate(t *testing.T) {
	pathRequest := func(body string, identity auth.Identity) *http.Request {
		request := operatorCodeHTTPTestRequest(t, body, identity)
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("tab_binding_id", operatorCodeHTTPTestBindingID)
		return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	}

	t.Run("catalog failure withholds context labels", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		fixture.contexts.listErr = errors.New("catalog unavailable")
		recorder := httptest.NewRecorder()

		adapter.HandleContexts(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current"}`, fixture.identity))

		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Empty(t, recorder.Body.String())
	})

	t.Run("unlisted context cannot become the selected view", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		foreign := fixture.ref.Clone()
		foreign.ViewID = uuid.NewString()
		recorder := httptest.NewRecorder()

		adapter.HandlePin(recorder, pathRequest(`{"document_proof":"proof-current","selection_ref":"`+operatorCodeContextSelectionRef(foreign)+`"}`, fixture.identity))

		require.Equal(t, http.StatusForbidden, recorder.Code)
		require.Empty(t, recorder.Body.String())
	})

	t.Run("pin persistence failure leaves the current selection unchanged", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		fixture.binding.pinned = nil
		fixture.contexts.pinErr = errors.New("pin store unavailable")
		recorder := httptest.NewRecorder()

		adapter.HandlePin(recorder, pathRequest(`{"document_proof":"proof-current","selection_ref":"`+operatorCodeContextSelectionRef(fixture.ref)+`"}`, fixture.identity))

		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Empty(t, recorder.Body.String())
		followUp := httptest.NewRecorder()
		adapter.HandleSearch(followUp, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","query":"Fixture"}`, fixture.identity))
		require.Equal(t, http.StatusForbidden, followUp.Code)
		require.Empty(t, followUp.Body.String())
	})
}

func TestOperatorCodeHTTPAdapter_SearchCursorWriteFailureWithholdsResult(t *testing.T) {
	body := `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture"}`

	t.Run("initial page", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		fixture.app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
		fixture.contexts.createErr = errors.New("cursor store unavailable")
		recorder := httptest.NewRecorder()

		adapter.HandleSearch(recorder, operatorCodeHTTPTestRequest(t, body, fixture.identity))

		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Empty(t, recorder.Body.String())
		require.Empty(t, fixture.recorder.inputs)
	})

	t.Run("continued page", func(t *testing.T) {
		adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
		serviceCursor := "service-cursor"
		firstResponse := operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
		firstResponse.Continuation = &uci.QueryContinuation{Value: &serviceCursor}
		truncated := true
		firstResponse.Truncated = &truncated
		fixture.app.searchResponses = []uci.QueryResponse{firstResponse, operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)}
		fixture.contexts.createCursorRef = "80000000-0000-4000-8000-000000000012"

		first := httptest.NewRecorder()
		adapter.HandleSearch(first, operatorCodeHTTPTestRequest(t, body, fixture.identity))
		require.Equal(t, http.StatusOK, first.Code, first.Body.String())

		fixture.contexts.advanceErr = errors.New("cursor store unavailable")
		second := httptest.NewRecorder()
		adapter.HandleSearch(second, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","query":"Fixture","continuation":"`+fixture.contexts.createCursorRef+`"}`, fixture.identity))

		require.Equal(t, http.StatusServiceUnavailable, second.Code)
		require.Empty(t, second.Body.String())
		require.Len(t, fixture.recorder.inputs, 1)
	})
}

func TestOperatorCodeHTTPAdapter_GraphRevocationWithholdsContent(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.app.graph = operatorCodeHTTPTestGraphResponse(t, fixture.ref)
	fixture.grants.revokeOnSecondCheck = true
	recorder := httptest.NewRecorder()

	adapter.HandleGraph(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","action":"neighbors","target":{"entity_key":"Fixture.Symbol"}}`, fixture.identity))

	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "package demo")
	require.NotContains(t, recorder.Body.String(), fixture.ref.SourceID)
	require.NotContains(t, recorder.Body.String(), fixture.ref.ViewID)
	require.Empty(t, fixture.recorder.inputs)
}

func TestOperatorCodeHTTPAdapter_NoViewIntentBindingMismatchIsBodyless(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.binding.pinned = nil
	fixture.contexts.entries[0].Context = nil
	fixture.app.indexSubmitErr = uci.ErrIndexIntentBindingMismatch
	recorder := httptest.NewRecorder()
	target := `{"selection_ref":"` + operatorCodeIndexSelectionRef(operatorCodeHTTPTestSourceID, operatorCodeHTTPTestCheckoutID, operatorCodeHTTPTestProfileID) + `"}`

	adapter.HandleIndexIntentSubmit(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","request_ref":"first-index-mismatch","kind":"reindex","target":`+target+`}`, fixture.identity))

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Empty(t, recorder.Body.String())
}

func TestOperatorCodeHTTPAdapter_SearchPreservesSuppressedResponse(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.app.search = uci.QueryResponse{
		Schema: uci.QueryResponseSchema,
		Status: uci.QueryStatusContextRequired,
		Error:  &uci.QueryError{Code: uci.QueryErrorContextRequired},
	}
	require.NoError(t, fixture.app.search.Validate())
	recorder := httptest.NewRecorder()

	adapter.HandleSearch(recorder, operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current","query":"Fixture"}`, fixture.identity))

	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"status":"context_required"`)
	require.NotContains(t, recorder.Body.String(), `"continuation"`)
	require.NotContains(t, recorder.Body.String(), "package demo")
	require.Empty(t, fixture.recorder.inputs)
}

const (
	operatorCodeHTTPTestSourceID       = "20000000-0000-4000-8000-000000000001"
	operatorCodeHTTPTestCheckoutID     = "30000000-0000-4000-8000-000000000001"
	operatorCodeHTTPTestViewID         = "40000000-0000-4000-8000-000000000001"
	operatorCodeHTTPTestProfileID      = "50000000-0000-4000-8000-000000000001"
	operatorCodeHTTPTestBindingID      = "60000000-0000-4000-8000-000000000041"
	operatorCodeHTTPTestIncarnationID  = "70000000-0000-4000-8000-000000000001"
	operatorCodeHTTPTestContextRefJSON = `{"source_id":"20000000-0000-4000-8000-000000000001","checkout_id":"30000000-0000-4000-8000-000000000001","view_id":"40000000-0000-4000-8000-000000000001","analysis_profile_id":"50000000-0000-4000-8000-000000000001","generation":7}`
)

type operatorCodeHTTPTestFixture struct {
	ref       uci.ContextRef
	identity  auth.Identity
	grants    *operatorCodeHTTPTestGrants
	binding   *operatorCodeHTTPTestBinding
	authority *operatorCodeHTTPTestAuthority
	app       *operatorCodeHTTPTestApplication
	contexts  *operatorCodeHTTPTestContextStore
	recorder  *operatorCodeHTTPTestRecorder
}

func newOperatorCodeHTTPTestAdapter(t *testing.T) (*OperatorCodeHTTPAdapter, *operatorCodeHTTPTestFixture) {
	t.Helper()
	ref := uci.ContextRef{
		SourceID:          operatorCodeHTTPTestSourceID,
		CheckoutID:        operatorCodeHTTPTestCheckoutID,
		ViewID:            operatorCodeHTTPTestViewID,
		AnalysisProfileID: operatorCodeHTTPTestProfileID,
		Generation:        7,
	}
	identity := auth.SessionForBrowserUser("viewer", 41)
	fixture := &operatorCodeHTTPTestFixture{
		ref:      ref,
		identity: identity,
		grants: &operatorCodeHTTPTestGrants{
			allowed:   true,
			currentOK: true,
			current: gormdb.BrowserReadGrant{
				GrantRef:      uuid.NewString(),
				AuthRealm:     "browser",
				SubjectUserID: 41,
				SourceID:      ref.SourceID,
				CheckoutID:    ref.CheckoutID,
				IssuedAt:      time.Now().UTC().Add(-time.Minute),
			},
		},
		binding: &operatorCodeHTTPTestBinding{pinned: &BrowserBindingContext{
			SourceID:          ref.SourceID,
			CheckoutID:        ref.CheckoutID,
			ViewID:            ref.ViewID,
			AnalysisProfileID: ref.AnalysisProfileID,
			Generation:        ref.Generation,
		}},
		app: &operatorCodeHTTPTestApplication{},
		contexts: &operatorCodeHTTPTestContextStore{entries: []gormdb.BrowserCodeContextCatalogEntry{{
			SourceID:      ref.SourceID,
			SourceLabel:   "Engram",
			CheckoutID:    ref.CheckoutID,
			CheckoutLabel: "Studio workstation · release candidate",
			Context:       &ref,
			ViewLabel:     "release candidate",
		}}},
		recorder: &operatorCodeHTTPTestRecorder{},
	}
	fixture.authority = newOperatorCodeHTTPTestAuthority(t, ref)
	fixture.contexts.binding = fixture.binding
	adapter := NewOperatorCodeHTTPAdapter(fixture.grants, fixture.binding, fixture.authority, fixture.app, fixture.recorder)
	adapter.contexts = fixture.contexts
	adapter.indexTargets = operatorCodeHTTPTestIndexTargets{}
	return adapter, fixture
}

type operatorCodeHTTPTestIndexTargets struct{}

func (operatorCodeHTTPTestIndexTargets) Resolve(sourceID, checkoutID string) (uci.IndexBinding, bool) {
	if sourceID != operatorCodeHTTPTestSourceID || !operatorCodeUUID(checkoutID) {
		return uci.IndexBinding{}, false
	}
	return uci.IndexBinding{
		Scope:         uci.IndexScope{SourceID: sourceID, CheckoutID: checkoutID, IncarnationID: operatorCodeHTTPTestIncarnationID},
		ProfileID:     operatorCodeHTTPTestProfileID,
		LocalRootID:   "daemon-root",
		WorkstationID: "daemon-workstation",
	}, true
}

type operatorCodeHTTPTestExpiredIndexTargets struct{}

func (operatorCodeHTTPTestExpiredIndexTargets) Resolve(string, string) (uci.IndexBinding, bool) {
	return uci.IndexBinding{}, false
}

type operatorCodeHTTPTestGrants struct {
	allowed             bool
	checks              int
	revokeOnSecondCheck bool
	current             gormdb.BrowserReadGrant
	currentOK           bool
	currentErr          error
}

func (grants *operatorCodeHTTPTestGrants) Active(_ context.Context, caller auth.Identity, sourceID, checkoutID string) (gormdb.BrowserReadGrant, bool, error) {
	if _, ok := caller.SessionBrowserSubject(); !ok || sourceID != grants.current.SourceID || checkoutID != grants.current.CheckoutID {
		return gormdb.BrowserReadGrant{}, false, nil
	}
	grants.checks++
	if grants.revokeOnSecondCheck && grants.checks == 2 {
		grants.allowed = false
	}
	if !grants.allowed {
		return gormdb.BrowserReadGrant{}, false, grants.currentErr
	}
	return grants.current, grants.currentOK, grants.currentErr
}

type operatorCodeHTTPTestContextStore struct {
	entries          []gormdb.BrowserCodeContextCatalogEntry
	listErr          error
	pins             []gormdb.BrowserCodeContextPin
	cursor           string
	createCursorRef  string
	advanceCursorRef string
	loadErr          error
	createErr        error
	advanceErr       error
	createdBindings  []gormdb.BrowserCodeContinuationBinding
	loadedBindings   []gormdb.BrowserCodeContinuationBinding
	advancedBindings []gormdb.BrowserCodeContinuationBinding
	loadedRefs       []string
	advancedRefs     []string
	binding          *operatorCodeHTTPTestBinding
	pinErr           error
	initialTargets   []gormdb.BrowserCodeIndexIntentTarget
	reauthTargets    []gormdb.BrowserCodeIndexIntentTarget
	initialTargetErr error
	reauthTargetErr  error
	noViewBinding    gormdb.BrowserCodeIndexIntentBinding
}

func (store *operatorCodeHTTPTestContextStore) ListCatalog(_ context.Context, _ int64) ([]gormdb.BrowserCodeContextCatalogEntry, error) {
	if store.listErr != nil {
		return nil, store.listErr
	}
	return append([]gormdb.BrowserCodeContextCatalogEntry(nil), store.entries...), nil
}

func (store *operatorCodeHTTPTestContextStore) Pin(_ context.Context, pin gormdb.BrowserCodeContextPin) error {
	if store.pinErr != nil {
		return store.pinErr
	}
	if pin.Caller.SubjectUserID != 41 || pin.Caller.SessionID != "browser-session-41" || pin.TabBindingID != operatorCodeHTTPTestBindingID {
		return gormdb.ErrBrowserCodeContextDenied
	}
	authorized := false
	for _, entry := range store.entries {
		if entry.Context != nil && operatorCodeHTTPTestRefsEqual(pin.Context, *entry.Context) {
			authorized = true
			break
		}
	}
	if !authorized {
		return gormdb.ErrBrowserCodeContextDenied
	}
	store.pins = append(store.pins, pin)
	if store.binding != nil {
		pinned := browserBindingContext(pin.Context)
		store.binding.pinnedTo = append(store.binding.pinnedTo, pinned)
		store.binding.pinned = &pinned
	}
	return nil
}

func (store *operatorCodeHTTPTestContextStore) AuthorizeInitialIndexIntent(_ context.Context, target gormdb.BrowserCodeIndexIntentTarget) (gormdb.BrowserCodeIndexIntentBinding, error) {
	store.initialTargets = append(store.initialTargets, target)
	if store.initialTargetErr != nil {
		return gormdb.BrowserCodeIndexIntentBinding{}, store.initialTargetErr
	}
	return store.noViewIndexIntentBinding(target, true)
}

func (store *operatorCodeHTTPTestContextStore) ReauthorizeIndexIntent(_ context.Context, target gormdb.BrowserCodeIndexIntentTarget) (gormdb.BrowserCodeIndexIntentBinding, error) {
	store.reauthTargets = append(store.reauthTargets, target)
	if store.reauthTargetErr != nil {
		return gormdb.BrowserCodeIndexIntentBinding{}, store.reauthTargetErr
	}
	return store.noViewIndexIntentBinding(target, false)
}

func (store *operatorCodeHTTPTestContextStore) noViewIndexIntentBinding(target gormdb.BrowserCodeIndexIntentTarget, requireNoView bool) (gormdb.BrowserCodeIndexIntentBinding, error) {
	if target.Caller.SubjectUserID != 41 || target.Caller.SessionID != "browser-session-41" || target.TabBindingID != operatorCodeHTTPTestBindingID || target.SourceID != operatorCodeHTTPTestSourceID || target.CheckoutID != operatorCodeHTTPTestCheckoutID || target.ProfileID != operatorCodeHTTPTestProfileID {
		return gormdb.BrowserCodeIndexIntentBinding{}, gormdb.ErrBrowserCodeContextDenied
	}
	if requireNoView {
		found := false
		for _, entry := range store.entries {
			if entry.SourceID != target.SourceID || entry.CheckoutID != target.CheckoutID {
				continue
			}
			found = true
			if entry.Context != nil {
				return gormdb.BrowserCodeIndexIntentBinding{}, gormdb.ErrBrowserCodeContextDenied
			}
		}
		if !found {
			return gormdb.BrowserCodeIndexIntentBinding{}, gormdb.ErrBrowserCodeContextDenied
		}
	}
	binding := store.noViewBinding
	if binding.Scope == (uci.IndexScope{}) {
		binding.Scope = uci.IndexScope{SourceID: target.SourceID, CheckoutID: target.CheckoutID, IncarnationID: operatorCodeHTTPTestIncarnationID}
		binding.ProfileID = target.ProfileID
		binding.AuthRealm = "browser"
	}
	return binding, nil
}

func (store *operatorCodeHTTPTestContextStore) LoadContinuation(_ context.Context, cursorRef string, binding gormdb.BrowserCodeContinuationBinding) (string, error) {
	store.loadedRefs = append(store.loadedRefs, cursorRef)
	store.loadedBindings = append(store.loadedBindings, binding)
	if store.loadErr != nil {
		return "", store.loadErr
	}
	if store.cursor == "" {
		return "", gormdb.ErrBrowserCodeContinuationDenied
	}
	return store.cursor, nil
}

func (store *operatorCodeHTTPTestContextStore) CreateContinuation(_ context.Context, binding gormdb.BrowserCodeContinuationBinding, cursor string) (string, error) {
	store.createdBindings = append(store.createdBindings, binding)
	if store.createErr != nil {
		return "", store.createErr
	}
	store.cursor = cursor
	if cursor == "" {
		return "", nil
	}
	if store.createCursorRef != "" {
		return store.createCursorRef, nil
	}
	return uuid.NewString(), nil
}

func (store *operatorCodeHTTPTestContextStore) AdvanceContinuation(_ context.Context, cursorRef string, binding gormdb.BrowserCodeContinuationBinding, cursor string) (string, error) {
	store.advancedRefs = append(store.advancedRefs, cursorRef)
	store.advancedBindings = append(store.advancedBindings, binding)
	if store.advanceErr != nil {
		return "", store.advanceErr
	}
	store.cursor = cursor
	if cursor == "" {
		return "", nil
	}
	if store.advanceCursorRef != "" {
		return store.advanceCursorRef, nil
	}
	return uuid.NewString(), nil
}

type operatorCodeHTTPTestGraphSources struct {
	descriptors         map[uci.QueryEntityRef]uci.VersionedReadSpec
	calls               []uci.QueryEntityRef
	evidenceDescriptors map[string]uci.VersionedReadSpec
	evidenceCalls       []uci.QueryRelationEvidence
}

func (sources *operatorCodeHTTPTestGraphSources) DescribeGraphSource(_ context.Context, _ uci.AuthorizedContext, entity uci.QueryEntityRef) (uci.VersionedReadSpec, bool, error) {
	sources.calls = append(sources.calls, entity)
	descriptor, available := sources.descriptors[entity]
	return descriptor, available, nil
}

func (sources *operatorCodeHTTPTestGraphSources) DescribeGraphEvidence(_ context.Context, authorized uci.AuthorizedContext, evidence uci.QueryRelationEvidence) (uci.VersionedReadSpec, bool, error) {
	sources.evidenceCalls = append(sources.evidenceCalls, evidence)
	if evidence.ReferenceSiteID == nil || evidence.Ref.SourceID != authorized.Ref().SourceID || evidence.Ref.ViewID != authorized.Ref().ViewID {
		return uci.VersionedReadSpec{}, false, nil
	}
	descriptor, available := sources.evidenceDescriptors[*evidence.ReferenceSiteID]
	return descriptor, available, nil
}

type operatorCodeHTTPTestBinding struct {
	pinned            *BrowserBindingContext
	err               error
	calls             int
	failOnCall        int
	guardedID         string
	handshake         BrowserBindingTransition
	resume            BrowserBindingTransition
	renewed           []BrowserBindingProof
	closed            []BrowserBindingProof
	pinnedTo          []BrowserBindingContext
	expectedSessionID string
	expectedUserID    int64
}

func (binding *operatorCodeHTTPTestBinding) Guard(_ context.Context, identity auth.Identity, sessionID string, proof BrowserBindingProof) (BrowserBindingGuarded, error) {
	binding.calls++
	if binding.err != nil || (binding.failOnCall > 0 && binding.calls >= binding.failOnCall) {
		return BrowserBindingGuarded{}, errors.New("proof denied")
	}
	subject, ok := identity.SessionBrowserSubject()
	expectedSessionID := binding.expectedSessionID
	if expectedSessionID == "" {
		expectedSessionID = "browser-session-41"
	}
	expectedUserID := binding.expectedUserID
	if expectedUserID == 0 {
		expectedUserID = 41
	}
	if !ok || subject.UserID != expectedUserID || sessionID != expectedSessionID || proof.TabBindingID != operatorCodeHTTPTestBindingID || proof.DocumentProof != "proof-current" {
		return BrowserBindingGuarded{}, errors.New("proof denied")
	}
	guarded := BrowserBindingGuarded{TabBindingID: proof.TabBindingID}
	if binding.guardedID != "" {
		guarded.TabBindingID = binding.guardedID
	}
	if binding.pinned != nil {
		pinned := *binding.pinned
		guarded.Pinned = &pinned
	}
	return guarded, nil
}

func (binding *operatorCodeHTTPTestBinding) Handshake(_ context.Context, _ auth.Identity, _ string, _ BrowserBindingHandshakeInput) (BrowserBindingTransition, error) {
	if binding.err != nil {
		return BrowserBindingTransition{}, binding.err
	}
	if binding.handshake.State != "" {
		return binding.handshake, nil
	}
	return BrowserBindingTransition{State: BrowserBindingReady, TabBindingID: operatorCodeHTTPTestBindingID, DocumentProof: "proof-handshake", ResumeNonce: "resume-handshake", ReloadToken: "reload-handshake"}, nil
}

func (binding *operatorCodeHTTPTestBinding) Resume(_ context.Context, _ auth.Identity, _ string, _ BrowserBindingResumeInput) (BrowserBindingTransition, error) {
	if binding.err != nil {
		return BrowserBindingTransition{}, binding.err
	}
	if binding.resume.State != "" {
		return binding.resume, nil
	}
	return BrowserBindingTransition{State: BrowserBindingReady, TabBindingID: operatorCodeHTTPTestBindingID, DocumentProof: "proof-resumed", ResumeNonce: "resume-current", ReloadToken: "reload-rotated"}, nil
}

func (binding *operatorCodeHTTPTestBinding) Renew(_ context.Context, _ auth.Identity, _ string, proof BrowserBindingProof) error {
	binding.renewed = append(binding.renewed, proof)
	return binding.err
}

func (binding *operatorCodeHTTPTestBinding) Close(_ context.Context, _ auth.Identity, _ string, proof BrowserBindingProof) error {
	binding.closed = append(binding.closed, proof)
	return binding.err
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
	ref := caller.Context
	if ref.ViewID == "" {
		ref = authority.ref
	}
	resolver := uci.NewContextResolver(operatorCodeHTTPTestCatalog{ref: ref}, operatorCodeHTTPTestAuthorizer{ref: ref}, nil)
	return resolver.Authorize(ctx, uci.ResolveContextInput{
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
	search               uci.QueryResponse
	searchResponses      []uci.QueryResponse
	searchSpecs          []uci.QuerySpec
	structure            uci.QueryResponse
	structureSpecs       []uci.QuerySpec
	graph                uci.QueryResponse
	read                 uci.QueryResponse
	status               mcp.CodebaseStatusSnapshot
	sourceSessions       []string
	searchCalls          int
	structureCalls       int
	graphCalls           int
	readCalls            int
	readInputs           []mcp.CodebaseReadInput
	indexIntents         map[string]uci.IndexIntent
	indexSubmitErr       error
	indexGetErr          error
	indexRetryErr        error
	indexSubmitCalls     int
	indexGetCalls        int
	indexRetryCalls      int
	indexRetryExecutions int
	indexSubmitSessions  []string
	indexGetSessions     []string
	indexRetrySessions   []string
	indexRetryResult     *uci.IndexIntent
}

func (app *operatorCodeHTTPTestApplication) SearchOperatorCodebase(ctx context.Context, _ uci.AuthorizedContext, spec uci.QuerySpec) (uci.QueryResponse, error) {
	app.searchCalls++
	app.searchSpecs = append(app.searchSpecs, spec)
	app.sourceSessions = append(app.sourceSessions, auditcontext.SourceSession(ctx))
	if len(app.searchResponses) > 0 {
		response := app.searchResponses[0]
		app.searchResponses = app.searchResponses[1:]
		return response, nil
	}
	return app.search, nil
}

func (app *operatorCodeHTTPTestApplication) StructureOperatorCodebase(ctx context.Context, _ uci.AuthorizedContext, spec uci.QuerySpec) (uci.QueryResponse, error) {
	app.structureCalls++
	app.structureSpecs = append(app.structureSpecs, spec)
	app.sourceSessions = append(app.sourceSessions, auditcontext.SourceSession(ctx))
	return app.structure, nil
}

func (app *operatorCodeHTTPTestApplication) ExploreOperatorCodebase(ctx context.Context, _ uci.AuthorizedContext, _ uci.GraphSpec) (uci.QueryResponse, error) {
	app.graphCalls++
	app.sourceSessions = append(app.sourceSessions, auditcontext.SourceSession(ctx))
	return app.graph, nil
}

func (app *operatorCodeHTTPTestApplication) ReadCodebase(ctx context.Context, _ uci.AuthorizedContext, input mcp.CodebaseReadInput) (uci.QueryResponse, error) {
	app.readCalls++
	app.readInputs = append(app.readInputs, input)
	app.sourceSessions = append(app.sourceSessions, auditcontext.SourceSession(ctx))
	return app.read, nil
}

func (app *operatorCodeHTTPTestApplication) CodebaseStatus(_ context.Context, _ uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error) {
	return app.status, nil
}

func (app *operatorCodeHTTPTestApplication) SubmitIndexIntent(ctx context.Context, authorized uci.AuthorizedContext, requestRef string, kind uci.IndexIntentKind) (uci.IndexIntent, error) {
	app.indexSubmitCalls++
	app.indexSubmitSessions = append(app.indexSubmitSessions, auditcontext.SourceSession(ctx))
	if app.indexSubmitErr != nil {
		return uci.IndexIntent{}, app.indexSubmitErr
	}
	if app.indexIntents == nil {
		app.indexIntents = make(map[string]uci.IndexIntent)
	}
	if existing, found := app.indexIntents[requestRef]; found {
		if existing.Kind != kind || existing.PreviousView == nil || !operatorCodeHTTPTestRefsEqual(*existing.PreviousView, authorized.Ref()) {
			return uci.IndexIntent{}, uci.ErrIndexIntentBindingMismatch
		}
		return existing.Clone(), nil
	}
	intent := operatorCodeHTTPTestIndexIntent(authorized.Ref(), requestRef, kind, uci.IndexIntentSubmitted)
	app.indexIntents[requestRef] = intent
	return intent.Clone(), nil
}

func (app *operatorCodeHTTPTestApplication) GetIndexIntent(ctx context.Context, _ uci.AuthorizedContext, intentRef string) (uci.IndexIntent, error) {
	app.indexGetCalls++
	app.indexGetSessions = append(app.indexGetSessions, auditcontext.SourceSession(ctx))
	if app.indexGetErr != nil {
		return uci.IndexIntent{}, app.indexGetErr
	}
	_, intent, found := app.indexIntent(intentRef)
	if !found {
		return uci.IndexIntent{}, uci.ErrIndexIntentNotFound
	}
	return intent.Clone(), nil
}

func (app *operatorCodeHTTPTestApplication) RetryIndexIntent(ctx context.Context, authorized uci.AuthorizedContext, intentRef string) (uci.IndexIntent, error) {
	app.indexRetryCalls++
	app.indexRetrySessions = append(app.indexRetrySessions, auditcontext.SourceSession(ctx))
	if app.indexRetryErr != nil {
		return uci.IndexIntent{}, app.indexRetryErr
	}
	key, intent, found := app.indexIntent(intentRef)
	if !found {
		return uci.IndexIntent{}, uci.ErrIndexIntentNotFound
	}
	if intent.PreviousView == nil || !operatorCodeHTTPTestRefsEqual(*intent.PreviousView, authorized.Ref()) {
		return uci.IndexIntent{}, uci.ErrIndexIntentRetryUnauthorized
	}
	if intent.State != uci.IndexIntentUnavailable {
		return uci.IndexIntent{}, uci.ErrIndexIntentInvalidTransition
	}
	if app.indexRetryResult != nil {
		return app.indexRetryResult.Clone(), nil
	}
	intent.State = uci.IndexIntentQueued
	intent.UpdatedAt = intent.UpdatedAt.Add(time.Second)
	app.indexIntents[key] = intent
	app.indexRetryExecutions++
	return intent.Clone(), nil
}

func (app *operatorCodeHTTPTestApplication) SubmitNoViewIndexIntent(ctx context.Context, scope uci.IndexScope, profileID, requestRef string, kind uci.IndexIntentKind) (uci.IndexIntent, error) {
	app.indexSubmitCalls++
	app.indexSubmitSessions = append(app.indexSubmitSessions, auditcontext.SourceSession(ctx))
	if app.indexSubmitErr != nil {
		return uci.IndexIntent{}, app.indexSubmitErr
	}
	if app.indexIntents == nil {
		app.indexIntents = make(map[string]uci.IndexIntent)
	}
	if existing, found := app.indexIntents[requestRef]; found {
		if existing.Kind != kind || existing.PreviousView != nil || existing.Scope != scope || existing.ProfileID != profileID {
			return uci.IndexIntent{}, uci.ErrIndexIntentBindingMismatch
		}
		return existing.Clone(), nil
	}
	intent := operatorCodeHTTPTestNoViewIndexIntent(scope, profileID, requestRef, kind, uci.IndexIntentSubmitted)
	app.indexIntents[requestRef] = intent
	return intent.Clone(), nil
}

func (app *operatorCodeHTTPTestApplication) NoViewIndexIntentByRequestRef(_ context.Context, requestRef string) (uci.IndexIntent, error) {
	intent, found := app.indexIntents[requestRef]
	if !found {
		return uci.IndexIntent{}, uci.ErrIndexIntentNotFound
	}
	if intent.PreviousView != nil {
		return uci.IndexIntent{}, uci.ErrIndexIntentBindingMismatch
	}
	return intent.Clone(), nil
}

func (app *operatorCodeHTTPTestApplication) ReplayNoViewIndexIntent(ctx context.Context, scope uci.IndexScope, profileID, requestRef string, kind uci.IndexIntentKind) (uci.IndexIntent, error) {
	intent, err := app.NoViewIndexIntentByRequestRef(ctx, requestRef)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	if intent.Scope != scope || intent.ProfileID != profileID || intent.Kind != kind {
		return uci.IndexIntent{}, uci.ErrIndexIntentBindingMismatch
	}
	return intent, nil
}

func (app *operatorCodeHTTPTestApplication) NoViewIndexIntentTarget(_ context.Context, intentRef string) (uci.IndexScope, string, error) {
	_, intent, found := app.indexIntent(intentRef)
	if !found || intent.PreviousView != nil {
		return uci.IndexScope{}, "", uci.ErrIndexIntentBindingMismatch
	}
	return intent.Scope, intent.ProfileID, nil
}

func (app *operatorCodeHTTPTestApplication) GetNoViewIndexIntent(ctx context.Context, scope uci.IndexScope, profileID, intentRef string) (uci.IndexIntent, error) {
	app.indexGetCalls++
	app.indexGetSessions = append(app.indexGetSessions, auditcontext.SourceSession(ctx))
	if app.indexGetErr != nil {
		return uci.IndexIntent{}, app.indexGetErr
	}
	_, intent, found := app.indexIntent(intentRef)
	if !found || intent.PreviousView != nil || intent.Scope != scope || intent.ProfileID != profileID {
		return uci.IndexIntent{}, uci.ErrIndexIntentBindingMismatch
	}
	return intent.Clone(), nil
}

func (app *operatorCodeHTTPTestApplication) RetryNoViewIndexIntent(ctx context.Context, scope uci.IndexScope, profileID, intentRef string) (uci.IndexIntent, error) {
	app.indexRetryCalls++
	app.indexRetrySessions = append(app.indexRetrySessions, auditcontext.SourceSession(ctx))
	if app.indexRetryErr != nil {
		return uci.IndexIntent{}, app.indexRetryErr
	}
	key, intent, found := app.indexIntent(intentRef)
	if !found || intent.PreviousView != nil || intent.Scope != scope || intent.ProfileID != profileID {
		return uci.IndexIntent{}, uci.ErrIndexIntentRetryUnauthorized
	}
	if intent.State != uci.IndexIntentUnavailable {
		return uci.IndexIntent{}, uci.ErrIndexIntentInvalidTransition
	}
	intent.State = uci.IndexIntentQueued
	intent.UpdatedAt = intent.UpdatedAt.Add(time.Second)
	app.indexIntents[key] = intent
	app.indexRetryExecutions++
	return intent.Clone(), nil
}

func (app *operatorCodeHTTPTestApplication) indexIntent(intentRef string) (string, uci.IndexIntent, bool) {
	if intent, found := app.indexIntents[intentRef]; found && intent.ID == intentRef {
		return intentRef, intent, true
	}
	for key, intent := range app.indexIntents {
		if intent.ID == intentRef {
			return key, intent, true
		}
	}
	return "", uci.IndexIntent{}, false
}

type operatorCodeHTTPTestApplicationWithoutIndexIntent struct {
	base *operatorCodeHTTPTestApplication
}

func (app operatorCodeHTTPTestApplicationWithoutIndexIntent) SearchOperatorCodebase(ctx context.Context, authorized uci.AuthorizedContext, spec uci.QuerySpec) (uci.QueryResponse, error) {
	return app.base.SearchOperatorCodebase(ctx, authorized, spec)
}

func (app operatorCodeHTTPTestApplicationWithoutIndexIntent) StructureOperatorCodebase(ctx context.Context, authorized uci.AuthorizedContext, spec uci.QuerySpec) (uci.QueryResponse, error) {
	return app.base.StructureOperatorCodebase(ctx, authorized, spec)
}

func (app operatorCodeHTTPTestApplicationWithoutIndexIntent) ExploreOperatorCodebase(ctx context.Context, authorized uci.AuthorizedContext, input uci.GraphSpec) (uci.QueryResponse, error) {
	return app.base.ExploreOperatorCodebase(ctx, authorized, input)
}

func (app operatorCodeHTTPTestApplicationWithoutIndexIntent) ReadCodebase(ctx context.Context, authorized uci.AuthorizedContext, input mcp.CodebaseReadInput) (uci.QueryResponse, error) {
	return app.base.ReadCodebase(ctx, authorized, input)
}

func (app operatorCodeHTTPTestApplicationWithoutIndexIntent) CodebaseStatus(ctx context.Context, authorized uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error) {
	return app.base.CodebaseStatus(ctx, authorized)
}

type operatorCodeHTTPTestRecorder struct {
	inputs         []uci.ExposureInput
	sourceSessions []string
	err            error
}

func (recorder *operatorCodeHTTPTestRecorder) Record(ctx context.Context, _ uci.AuthorizedContext, input uci.ExposureInput) (uci.QueryExposure, error) {
	recorder.inputs = append(recorder.inputs, input)
	recorder.sourceSessions = append(recorder.sourceSessions, auditcontext.SourceSession(ctx))
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

func operatorCodeHTTPTestIndexIntentStatusRequest(t *testing.T, intentRef, body string, identity auth.Identity) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/operator/code/intents/"+intentRef, bytes.NewBufferString(body))
	request.Header.Set("X-Engram-Request-ID", "operator-request-1")
	request.Header.Set("X-Engram-Tab-Binding-ID", operatorCodeHTTPTestBindingID)
	request.Header.Set("X-Engram-Document-Proof", "proof-current")
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "browser-session-41"})
	request = request.WithContext(auth.WithIdentity(request.Context(), identity))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("intent_ref", intentRef)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func operatorCodeHTTPTestIndexIntentRetryRequest(t *testing.T, intentRef string, identity auth.Identity) *http.Request {
	t.Helper()
	request := operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+operatorCodeHTTPTestBindingID+`","document_proof":"proof-current"}`, identity)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("intent_ref", intentRef)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func operatorCodeHTTPTestIndexIntent(tRef uci.ContextRef, requestRef string, kind uci.IndexIntentKind, state uci.IndexIntentState) uci.IndexIntent {
	createdAt := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	previous := tRef.Clone()
	intent := uci.IndexIntent{
		ID:         uuid.NewString(),
		RequestRef: requestRef,
		Kind:       kind,
		Scope: uci.IndexScope{
			SourceID:      tRef.SourceID,
			CheckoutID:    tRef.CheckoutID,
			IncarnationID: operatorCodeHTTPTestIncarnationID,
		},
		ProfileID:    tRef.AnalysisProfileID,
		PreviousView: &previous,
		State:        state,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
	if state == uci.IndexIntentAcknowledged || state == uci.IndexIntentRunning || state == uci.IndexIntentCompleted || state == uci.IndexIntentFailed {
		claim, _ := uci.NewIndexIntentClaim(intent.ID, "private-index-owner", 1, createdAt)
		intent.Attempt = 1
		intent.Acknowledgement = &claim
	}
	if state == uci.IndexIntentCompleted {
		result := tRef.Clone()
		result.ViewID = uuid.NewString()
		result.Generation++
		intent.ResultView = &result
	}
	return intent
}

func operatorCodeHTTPTestNoViewIndexIntent(scope uci.IndexScope, profileID, requestRef string, kind uci.IndexIntentKind, state uci.IndexIntentState) uci.IndexIntent {
	intent := operatorCodeHTTPTestIndexIntent(uci.ContextRef{SourceID: scope.SourceID, CheckoutID: scope.CheckoutID, AnalysisProfileID: profileID}, requestRef, kind, state)
	intent.Scope = scope
	intent.ProfileID = profileID
	intent.PreviousView = nil
	if state == uci.IndexIntentCompleted {
		result := uci.ContextRef{SourceID: scope.SourceID, CheckoutID: scope.CheckoutID, ViewID: uuid.NewString(), AnalysisProfileID: profileID, Generation: 1}
		intent.ResultView = &result
	}
	return intent
}

func operatorCodeHTTPTestIndexIntentResponse(t *testing.T, body string) map[string]any {
	t.Helper()
	response := make(map[string]any)
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response
}

func operatorCodeHTTPTestRequireSafeIndexIntentResponse(t *testing.T, body string, resultAllowed bool, fixture *operatorCodeHTTPTestFixture) {
	t.Helper()
	for _, privateField := range []string{
		`"request_ref"`, `"kind"`, `"source_id"`, `"checkout_id"`, `"incarnation_id"`, `"profile_id"`, `"previous_view"`, `"acknowledgement"`, `"owner"`, `"tab_binding_id"`, `"document_proof"`, `"lease"`, `"host"`, `"path"`, `"provider"`,
	} {
		require.NotContains(t, body, privateField)
	}
	for _, privateValue := range []string{
		fixture.ref.SourceID, fixture.ref.CheckoutID, fixture.ref.AnalysisProfileID, operatorCodeHTTPTestBindingID, "proof-current", "private-index-owner",
	} {
		require.NotContains(t, body, privateValue)
	}
	if !resultAllowed {
		require.NotContains(t, body, `"result"`)
	}
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
