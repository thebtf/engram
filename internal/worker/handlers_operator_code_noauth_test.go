package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

type noAuthHTTPAuthority struct{ ref uci.ContextRef }

func (a noAuthHTTPAuthority) AuthorizeOperatorCode(ctx context.Context, caller operatorCodeVerifiedCaller) (uci.AuthorizedContext, error) {
	if !caller.NoAuth || caller.Context != a.ref {
		return uci.AuthorizedContext{}, errors.New("scope denied")
	}
	resolver := uci.NewContextResolver(noAuthHTTPCatalog{a.ref}, noAuthHTTPAccess{a.ref}, nil)
	return resolver.Authorize(ctx, uci.ResolveContextInput{ClientSessionID: "local-code/" + caller.BindingID, AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal, Ref: &a.ref})
}

type noAuthHTTPCatalog struct{ ref uci.ContextRef }

func (c noAuthHTTPCatalog) LoadContext(_ context.Context, ref uci.ContextRef) (uci.ContextRecord, error) {
	if ref != c.ref {
		return uci.ContextRecord{}, errors.New("view denied")
	}
	return uci.ContextRecord{Ref: ref, AuthRealm: uci.NoAuthCodeRealm}, nil
}

type noAuthHTTPAccess struct{ ref uci.ContextRef }

func (c noAuthHTTPAccess) AuthorizeContext(_ context.Context, access uci.ContextAccess) error {
	if access.AuthRealm != uci.NoAuthCodeRealm || access.Principal != uci.NoAuthCodePrincipal || access.SourceID != c.ref.SourceID || access.CheckoutID != c.ref.CheckoutID {
		return errors.New("owner denied")
	}
	return nil
}

func TestNoAuthOperatorCodeFirstUseAndAuthEnabledCannotForge(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	adapter.authority = noAuthHTTPAuthority{fixture.ref}
	noauth := auth.AuthDisabled()
	invoke := func(body string, id auth.Identity, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		request := operatorCodeHTTPTestRequest(t, body, id)
		request.Header.Set("X-Engram-Auth-Disabled", "true")
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder
	}
	handshake := invoke(`{"document_nonce":"first-use"}`, noauth, adapter.HandleHandshake)
	require.Equal(t, http.StatusOK, handshake.Code, handshake.Body.String())
	var transition operatorCodeTransitionResponse
	require.NoError(t, json.Unmarshal(handshake.Body.Bytes(), &transition))
	proof := `{"tab_binding_id":"` + transition.TabBindingID + `","document_proof":"` + transition.DocumentProof + `"}`
	catalog := invoke(proof, noauth, adapter.HandleContexts)
	require.Equal(t, http.StatusOK, catalog.Code, catalog.Body.String())
	var contexts operatorCodeContextsResponse
	require.NoError(t, json.Unmarshal(catalog.Body.Bytes(), &contexts))
	require.Len(t, contexts.Contexts, 1)
	fixture.contexts.listErr = errors.New("catalog exceeds 128 active checkouts")
	require.Equal(t, http.StatusServiceUnavailable, invoke(proof, noauth, adapter.HandleContexts).Code)
	fixture.contexts.listErr = nil
	require.NotEmpty(t, contexts.Contexts[0].SelectionRef)
	pinRequest := operatorCodeHTTPTestRequest(t, `{"document_proof":"`+transition.DocumentProof+`","selection_ref":"`+contexts.Contexts[0].SelectionRef+`"}`, noauth)
	route := chi.NewRouteContext()
	route.URLParams.Add("tab_binding_id", transition.TabBindingID)
	pinRequest = pinRequest.WithContext(context.WithValue(pinRequest.Context(), chi.RouteCtxKey, route))
	pin := httptest.NewRecorder()
	adapter.HandlePin(pin, pinRequest)
	require.Equal(t, http.StatusNoContent, pin.Code, pin.Body.String())
	fixture.app.status = mcp.CodebaseStatusSnapshot{TotalChunks: 1, Embedding: uci.EmbeddingStatus{Coverage: uci.IndexCoverageUnavailable}}
	status := invoke(proof, noauth, adapter.HandleStatus)
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	fixture.app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
	search := invoke(`{"tab_binding_id":"`+transition.TabBindingID+`","document_proof":"`+transition.DocumentProof+`","query":"Fixture"}`, noauth, adapter.HandleSearch)
	require.Equal(t, http.StatusOK, search.Code, search.Body.String())
	require.Contains(t, search.Body.String(), `"excerpt":"package demo"`)
	fixture.app.graph = operatorCodeHTTPTestGraphResponse(t, fixture.ref)
	graph := invoke(`{"tab_binding_id":"`+transition.TabBindingID+`","document_proof":"`+transition.DocumentProof+`","action":"neighbors","target":{"entity_key":"Fixture.Symbol"}}`, noauth, adapter.HandleGraph)
	require.Equal(t, http.StatusOK, graph.Code, graph.Body.String())
	require.Contains(t, graph.Body.String(), `"navigation"`)
	fixture.app.read = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalExact)
	read := invoke(`{"tab_binding_id":"`+transition.TabBindingID+`","document_proof":"`+transition.DocumentProof+`","entity_key":"Fixture.Symbol","span":{"byte_start":0,"byte_end":12,"line_start":1,"line_end":1},"content_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, noauth, adapter.HandleVersionedRead)
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	require.Contains(t, read.Body.String(), `"excerpt":"package demo"`)
	missingCookie := operatorCodeHTTPTestRequest(t, proof, auth.SessionForBrowserUser("viewer", 41))
	missingCookie.Header.Set("X-Engram-Auth-Disabled", "true")
	missingCookie.Header.Del("Cookie")
	denied := httptest.NewRecorder()
	adapter.HandleStatus(denied, missingCookie)
	require.Equal(t, http.StatusForbidden, denied.Code)
	forged := invoke(proof, auth.SessionForBrowserUser("viewer", 42), adapter.HandleStatus)
	require.NotEqual(t, http.StatusOK, forged.Code)
}

func TestNoAuthOperatorCodePagehideCloseReloadResumesPinnedTab(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	adapter.authority = noAuthHTTPAuthority{fixture.ref}
	noauth := auth.AuthDisabled()
	call := func(body string, id auth.Identity, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		request := operatorCodeHTTPTestRequest(t, body, id)
		request.Header.Set("X-Engram-Auth-Disabled", "true")
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}
	first := call(`{"document_nonce":"first-document"}`, noauth, adapter.HandleHandshake)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var original operatorCodeTransitionResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &original))
	firstProof := `{"tab_binding_id":"` + original.TabBindingID + `","document_proof":"` + original.DocumentProof + `"}`
	catalog := call(firstProof, noauth, adapter.HandleContexts)
	require.Equal(t, http.StatusOK, catalog.Code, catalog.Body.String())
	var contexts operatorCodeContextsResponse
	require.NoError(t, json.Unmarshal(catalog.Body.Bytes(), &contexts))
	require.Len(t, contexts.Contexts, 1)
	pathCall := func(id string, body string, identity auth.Identity, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		request := operatorCodeHTTPTestRequest(t, body, identity)
		request.Header.Set("X-Engram-Auth-Disabled", "true")
		route := chi.NewRouteContext()
		route.URLParams.Add("tab_binding_id", id)
		request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}
	pin := pathCall(original.TabBindingID, `{"document_proof":"`+original.DocumentProof+`","selection_ref":"`+contexts.Contexts[0].SelectionRef+`"}`, noauth, adapter.HandlePin)
	require.Equal(t, http.StatusNoContent, pin.Code, pin.Body.String())
	resumeBody := `{"tab_binding_id":"` + original.TabBindingID + `","resume_nonce":"` + original.ResumeNonce + `","reload_token":"` + original.ReloadToken + `","document_nonce":"second-document"}`
	pending := call(resumeBody, noauth, adapter.HandleResume)
	require.Equal(t, http.StatusOK, pending.Code, pending.Body.String())
	require.JSONEq(t, `{"state":"RELOAD_PENDING"}`, pending.Body.String())
	closeResponse := pathCall(original.TabBindingID, `{"document_proof":"`+original.DocumentProof+`"}`, noauth, adapter.HandleClose)
	require.Equal(t, http.StatusNoContent, closeResponse.Code, closeResponse.Body.String())
	require.Equal(t, http.StatusForbidden, call(firstProof, noauth, adapter.HandleStatus).Code)
	require.Equal(t, http.StatusForbidden, pathCall(original.TabBindingID, `{"document_proof":"`+original.DocumentProof+`"}`, noauth, adapter.HandleRenew).Code, "closed document cannot renew")
	resumed := call(resumeBody, noauth, adapter.HandleResume)
	require.Equal(t, http.StatusOK, resumed.Code, resumed.Body.String())
	var second operatorCodeTransitionResponse
	require.NoError(t, json.Unmarshal(resumed.Body.Bytes(), &second))
	require.Equal(t, BrowserBindingReady, second.State)
	require.Equal(t, original.TabBindingID, second.TabBindingID)
	require.Equal(t, original.ResumeNonce, second.ResumeNonce)
	require.NotEqual(t, original.DocumentProof, second.DocumentProof)
	require.NotEqual(t, original.ReloadToken, second.ReloadToken)
	secondProof := `{"tab_binding_id":"` + second.TabBindingID + `","document_proof":"` + second.DocumentProof + `"}`
	fixture.app.status = mcp.CodebaseStatusSnapshot{TotalChunks: 1, Embedding: uci.EmbeddingStatus{Coverage: uci.IndexCoverageUnavailable}}
	require.Equal(t, http.StatusOK, call(secondProof, noauth, adapter.HandleStatus).Code, "pinned selection survives pagehide and reload")
	require.Equal(t, http.StatusForbidden, call(resumeBody, noauth, adapter.HandleResume).Code, "consumed reload token cannot replay")
	other := call(`{"document_nonce":"other-tab","copied_tab_binding_id":"`+original.TabBindingID+`","copied_resume_nonce":"`+original.ResumeNonce+`"}`, noauth, adapter.HandleHandshake)
	require.Equal(t, http.StatusOK, other.Code, other.Body.String())
	var separate operatorCodeTransitionResponse
	require.NoError(t, json.Unmarshal(other.Body.Bytes(), &separate))
	require.NotEqual(t, original.TabBindingID, separate.TabBindingID, "copied tab gets no prior binding")
	foreignPair := `{"tab_binding_id":"` + separate.TabBindingID + `","resume_nonce":"` + original.ResumeNonce + `","reload_token":"` + second.ReloadToken + `","document_nonce":"foreign-document"}`
	require.Equal(t, http.StatusForbidden, call(foreignPair, noauth, adapter.HandleResume).Code, "another tab cannot borrow resume material")
	require.Equal(t, http.StatusOK, call(secondProof, noauth, adapter.HandleStatus).Code, "foreign replay cannot revoke current tab")
	require.Equal(t, http.StatusForbidden, call(secondProof, auth.SessionForBrowserUser("viewer", 42), adapter.HandleStatus).Code, "auth-enabled identity cannot forge noauth via header")
}

func TestNoAuthOperatorCodeRestartRequiresFreshHandshakeAndSelection(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	adapter.authority = noAuthHTTPAuthority{fixture.ref}
	noauth := auth.AuthDisabled()
	call := func(body string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		request := operatorCodeHTTPTestRequest(t, body, noauth)
		request.Header.Set("X-Engram-Auth-Disabled", "true")
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}
	handshake := func(nonce string) operatorCodeTransitionResponse {
		response := call(`{"document_nonce":"`+nonce+`"}`, adapter.HandleHandshake)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var transition operatorCodeTransitionResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &transition))
		return transition
	}
	pinSelection := func(transition operatorCodeTransitionResponse, selectionRef string) {
		request := operatorCodeHTTPTestRequest(t, `{"document_proof":"`+transition.DocumentProof+`","selection_ref":"`+selectionRef+`"}`, noauth)
		request.Header.Set("X-Engram-Auth-Disabled", "true")
		route := chi.NewRouteContext()
		route.URLParams.Add("tab_binding_id", transition.TabBindingID)
		request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
		response := httptest.NewRecorder()
		adapter.HandlePin(response, request)
		require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	}
	first := handshake("before-restart")
	oldProof := `{"tab_binding_id":"` + first.TabBindingID + `","document_proof":"` + first.DocumentProof + `"}`
	firstCatalog := call(oldProof, adapter.HandleContexts)
	require.Equal(t, http.StatusOK, firstCatalog.Code, firstCatalog.Body.String())
	var firstContexts operatorCodeContextsResponse
	require.NoError(t, json.Unmarshal(firstCatalog.Body.Bytes(), &firstContexts))
	require.Len(t, firstContexts.Contexts, 1)
	pinSelection(first, firstContexts.Contexts[0].SelectionRef)
	fixture.app.status = mcp.CodebaseStatusSnapshot{TotalChunks: 1, Embedding: uci.EmbeddingStatus{Coverage: uci.IndexCoverageUnavailable}}
	require.Equal(t, http.StatusOK, call(oldProof, adapter.HandleStatus).Code, "selection is pinned before restart")

	adapter.noAuthBindings = newNoAuthCodeBindings() // Simulate server restart; catalog remains durable.
	resume := `{"tab_binding_id":"` + first.TabBindingID + `","resume_nonce":"` + first.ResumeNonce + `","reload_token":"` + first.ReloadToken + `","document_nonce":"after-restart"}`
	require.Equal(t, http.StatusForbidden, call(resume, adapter.HandleResume).Code)
	require.Equal(t, http.StatusForbidden, call(oldProof, adapter.HandleContexts).Code)

	second := handshake("after-restart")
	require.NotEqual(t, first.TabBindingID, second.TabBindingID)
	newProof := `{"tab_binding_id":"` + second.TabBindingID + `","document_proof":"` + second.DocumentProof + `"}`
	catalog := call(newProof, adapter.HandleContexts)
	require.Equal(t, http.StatusOK, catalog.Code, catalog.Body.String())
	var contexts operatorCodeContextsResponse
	require.NoError(t, json.Unmarshal(catalog.Body.Bytes(), &contexts))
	require.Len(t, contexts.Contexts, 1)
	require.NotEmpty(t, contexts.Contexts[0].SelectionRef)
	require.Equal(t, http.StatusForbidden, call(newProof, adapter.HandleStatus).Code, "new document has no inherited pin")
	pinSelection(second, contexts.Contexts[0].SelectionRef)
	require.Equal(t, http.StatusOK, call(newProof, adapter.HandleStatus).Code, "fresh catalog selection restores access")
}

func TestNoAuthOperatorCodeCapacityRecoversFromInactiveTabs(t *testing.T) {
	adapter, _ := newOperatorCodeHTTPTestAdapter(t)
	identity := auth.AuthDisabled()
	call := func(body string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		request := operatorCodeHTTPTestRequest(t, body, identity)
		request.Header.Set("X-Engram-Auth-Disabled", "true")
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}
	handshake := func(nonce string) operatorCodeTransitionResponse {
		response := call(`{"document_nonce":"`+nonce+`"}`, adapter.HandleHandshake)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var transition operatorCodeTransitionResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &transition))
		return transition
	}
	active := handshake("active-tab")
	closed := handshake("closed-tab")
	expired := handshake("expired-lease-tab")
	for len(adapter.noAuthBindings.tabs) < 1024 {
		_, err := adapter.noAuthBindings.Handshake(context.Background(), identity, "", BrowserBindingHandshakeInput{DocumentNonce: "additional-tab"})
		require.NoError(t, err)
	}
	require.Equal(t, http.StatusForbidden, call(`{"document_nonce":"full-capacity"}`, adapter.HandleHandshake).Code, "live tabs cannot be evicted")
	require.Len(t, adapter.noAuthBindings.tabs, 1024)
	activeProof := `{"tab_binding_id":"` + active.TabBindingID + `","document_proof":"` + active.DocumentProof + `"}`
	require.Equal(t, http.StatusOK, call(activeProof, adapter.HandleContexts).Code, "capacity rejection preserves active proof")

	closeRequest := operatorCodeHTTPTestRequest(t, `{"document_proof":"`+closed.DocumentProof+`"}`, identity)
	closeRequest.Header.Set("X-Engram-Auth-Disabled", "true")
	route := chi.NewRouteContext()
	route.URLParams.Add("tab_binding_id", closed.TabBindingID)
	closeRequest = closeRequest.WithContext(context.WithValue(closeRequest.Context(), chi.RouteCtxKey, route))
	closeResponse := httptest.NewRecorder()
	adapter.HandleClose(closeResponse, closeRequest)
	require.Equal(t, http.StatusNoContent, closeResponse.Code)
	handshake("after-close")
	require.Len(t, adapter.noAuthBindings.tabs, 1024)
	closedResume := `{"tab_binding_id":"` + closed.TabBindingID + `","resume_nonce":"` + closed.ResumeNonce + `","reload_token":"` + closed.ReloadToken + `","document_nonce":"reload-closed"}`
	require.Equal(t, http.StatusForbidden, call(closedResume, adapter.HandleResume).Code, "evicted binding cannot resume")

	tab := adapter.noAuthBindings.tabs[expired.TabBindingID]
	tab.lease = time.Now().Add(-time.Second)
	adapter.noAuthBindings.tabs[expired.TabBindingID] = tab
	handshake("after-lease-expiry")
	require.Len(t, adapter.noAuthBindings.tabs, 1024)
	expiredResume := `{"tab_binding_id":"` + expired.TabBindingID + `","resume_nonce":"` + expired.ResumeNonce + `","reload_token":"` + expired.ReloadToken + `","document_nonce":"reload-expired"}`
	require.Equal(t, http.StatusForbidden, call(expiredResume, adapter.HandleResume).Code, "evicted expired lease cannot resume")
	require.Equal(t, http.StatusOK, call(activeProof, adapter.HandleContexts).Code, "recovery preserves active proof")
	require.Equal(t, http.StatusForbidden, call(`{"document_nonce":"still-full"}`, adapter.HandleHandshake).Code, "all remaining tabs are live")
}

func TestNoAuthOperatorCodeCursorCapacityEvictsOldestAbandonedCursor(t *testing.T) {
	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	adapter.authority = noAuthHTTPAuthority{fixture.ref}
	identity := auth.AuthDisabled()
	ctx := context.Background()
	abandoned, err := adapter.noAuthBindings.Handshake(ctx, identity, "", BrowserBindingHandshakeInput{DocumentNonce: "abandoned-tab"})
	require.NoError(t, err)
	active, err := adapter.noAuthBindings.Handshake(ctx, identity, "", BrowserBindingHandshakeInput{DocumentNonce: "active-tab"})
	require.NoError(t, err)
	activeProof := BrowserBindingProof{TabBindingID: active.TabBindingID, DocumentProof: active.DocumentProof}
	require.NoError(t, adapter.noAuthBindings.Pin(ctx, identity, activeProof, fixture.ref))

	oldBinding := gormdb.BrowserCodeContinuationBinding{AuthRealm: uci.NoAuthCodeRealm, TabBindingID: abandoned.TabBindingID}
	now := time.Now()
	for index := range 2048 {
		deadline := now.Add(8 * time.Minute)
		if index == 0 {
			deadline = now.Add(time.Minute)
		}
		adapter.noAuthBindings.cursors["abandoned-"+strconv.Itoa(index)] = noAuthCodeCursor{binding: oldBinding, service: "service-cursor", expires: deadline}
	}
	adapter.noAuthBindings.cursors["abandoned-2047"] = noAuthCodeCursor{binding: oldBinding, service: "live-cursor", expires: now.Add(9 * time.Minute)}
	serviceCursor := "usc1.00000000-0000-4000-8000-000000000001"
	response := operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)
	truncated := true
	response.Truncated = &truncated
	response.Continuation = &uci.QueryContinuation{Value: &serviceCursor}
	fixture.app.search = response

	request := operatorCodeHTTPTestRequest(t, `{"tab_binding_id":"`+active.TabBindingID+`","document_proof":"`+active.DocumentProof+`","query":"Fixture","limit":1}`, identity)
	request.Header.Set("X-Engram-Auth-Disabled", "true")
	search := httptest.NewRecorder()
	adapter.HandleSearch(search, request)
	require.Equal(t, 1, fixture.app.searchCalls, "request reached cursor admission after tab authorization")
	require.Equal(t, http.StatusOK, search.Code, search.Body.String())
	require.Len(t, adapter.noAuthBindings.cursors, 2048)
	_, err = adapter.noAuthBindings.cursor("abandoned-0", oldBinding)
	require.ErrorIs(t, err, gormdb.ErrBrowserCodeContinuationDenied, "the oldest abandoned continuation is no longer usable")
	value, err := adapter.noAuthBindings.cursor("abandoned-2047", oldBinding)
	require.NoError(t, err)
	require.Equal(t, "live-cursor", value, "recent continuation survives unrelated tab's search")
	_, err = adapter.noAuthBindings.cursor("abandoned-2047", gormdb.BrowserCodeContinuationBinding{AuthRealm: uci.NoAuthCodeRealm, TabBindingID: active.TabBindingID})
	require.ErrorIs(t, err, gormdb.ErrBrowserCodeContinuationDenied, "other tab cannot redeem an existing cursor")
	require.NotContains(t, search.Body.String(), serviceCursor)
	_, err = adapter.noAuthBindings.Guard(ctx, identity, "", activeProof)
	require.NoError(t, err, "unrelated tab proof remains valid")
	tab := adapter.noAuthBindings.tabs[active.TabBindingID]
	tab.expires = now.Add(-time.Second)
	adapter.noAuthBindings.tabs[active.TabBindingID] = tab
	_, err = adapter.noAuthBindings.Guard(ctx, identity, "", activeProof)
	require.ErrorIs(t, err, ErrBrowserBindingDenied, "expired tab proof cannot be extended by cursor eviction")
}
