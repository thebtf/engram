package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
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
