package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciCodebaseContextTestRealm   = "client"
	uciCodebaseContextTestSpaceID = "10000000-0000-4000-8000-000000000001"
	uciCodebaseContextTestSource  = "20000000-0000-4000-8000-000000000001"
	uciCodebaseContextTestProfile = "50000000-0000-4000-8000-000000000001"

	uciCodebaseContextTestCheckoutA    = "30000000-0000-4000-8000-000000000001"
	uciCodebaseContextTestCheckoutB    = "30000000-0000-4000-8000-000000000002"
	uciCodebaseContextTestViewA        = "40000000-0000-4000-8000-000000000001"
	uciCodebaseContextTestViewB        = "40000000-0000-4000-8000-000000000002"
	uciCodebaseContextTestIncarnationA = "60000000-0000-4000-8000-000000000001"

	uciCodebaseContextPrivateLocatorA = `D:\private\agent-a\engram`
	uciCodebaseContextPrivateLocatorB = `D:\private\agent-b\engram`
)

type uciCodebaseContextFixture struct {
	server      *Server
	application *uciCodebaseContextApplicationFake
	discovery   *uciCodebaseContextDiscoveryFake
	catalog     *uciCodebaseContextCatalogFake
	authorizer  *uciCodebaseContextAuthorizerFake
	projection  *uciCodebaseContextProjectionFake
	refA        uci.ContextRef
	refB        uci.ContextRef
	clientA     context.Context
	clientB     context.Context
	clientC     context.Context
}

func newUCICodebaseContextFixture(t *testing.T) *uciCodebaseContextFixture {
	t.Helper()
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	refA := uciCodebaseContextRef(uciCodebaseContextTestCheckoutA, uciCodebaseContextTestViewA, 7)
	refB := uciCodebaseContextRef(uciCodebaseContextTestCheckoutB, uciCodebaseContextTestViewB, 11)
	catalog := &uciCodebaseContextCatalogFake{
		records: map[string]uci.ContextRecord{
			refA.CheckoutID: {Ref: refA, AuthRealm: uciCodebaseContextTestRealm},
			refB.CheckoutID: {Ref: refB, AuthRealm: uciCodebaseContextTestRealm},
		},
		bindings: make(map[string]uci.IndexBinding),
	}
	authorizer := &uciCodebaseContextAuthorizerFake{allowed: map[string]map[string]bool{
		"agent/a": {refA.CheckoutID: true},
		"agent/b": {refB.CheckoutID: true},
	}}
	discovery := &uciCodebaseContextDiscoveryFake{candidates: map[string][]uci.ContextRef{
		"mcp-client-a": {refA, refB},
		"mcp-client-b": {refA, refB},
		"mcp-client-c": {refA, refB},
	}}
	projection := &uciCodebaseContextProjectionFake{metadata: map[string]map[string]string{
		refA.CheckoutID: {
			"source":          "engram",
			"checkout":        "agent-a-linked",
			"view":            "saved-a",
			"private_locator": uciCodebaseContextPrivateLocatorA,
		},
		refB.CheckoutID: {
			"source":          "engram",
			"checkout":        "agent-b-linked",
			"view":            "saved-b",
			"private_locator": uciCodebaseContextPrivateLocatorB,
		},
	}}
	application := &uciCodebaseContextApplicationFake{
		realm:      uciCodebaseContextTestRealm,
		catalog:    catalog,
		authorizer: authorizer,
		discovery:  discovery,
		projection: projection,
	}
	application.resolver = uci.NewContextResolver(catalog, authorizer, catalog)

	server := NewServer(ServerOptions{Version: "uci-context-red"})
	// T021's only Server injection seam. The fake's Resolve/List/Project method
	// set is deliberately the narrow application port over internal/uci.
	server.SetCodebaseContextApplication(application)

	return &uciCodebaseContextFixture{
		server:      server,
		application: application,
		discovery:   discovery,
		catalog:     catalog,
		authorizer:  authorizer,
		projection:  projection,
		refA:        refA,
		refB:        refB,
		clientA:     uciCodebaseContextClient("mcp-client-a", "keycard-a", "agent/a"),
		clientB:     uciCodebaseContextClient("mcp-client-b", "keycard-b", "agent/b"),
		clientC:     uciCodebaseContextClient("mcp-client-c", "keycard-c", "agent/c"),
	}
}

func TestUCICodebaseContextToolDefinition(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)

	tool := uciCodebaseContextTool(t, fixture.server.ListTools())
	properties, ok := tool.InputSchema["properties"].(map[string]any)
	require.True(t, ok, "codebase_context must expose an object property schema")
	require.Contains(t, tool.InputSchema["required"], "action")

	action, ok := properties["action"].(map[string]any)
	require.True(t, ok, "codebase_context.action must be declared")
	require.Equal(t, "string", action["type"])
	require.ElementsMatch(t, []string{"resolve", "list", "select"}, action["enum"])
	for _, name := range []string{
		"context_handle",
		"source_id",
		"checkout_id",
		"view_id",
		"analysis_profile_id",
		"generation",
	} {
		require.Contains(t, properties, name, "codebase_context must accept typed %s selection evidence", name)
	}
	assert.NotContains(t, properties, "project", "legacy project text is compatibility evidence, never context authority")
	assert.NotContains(t, properties, "absolute_path", "the public contract must not accept or advertise a private locator")
}

func TestUCICodebaseContextResolveAmbiguityIsClosedBeforeAuthorizationOrProjection(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)

	// No cwd and two discovered candidates must not borrow main/latest or either
	// agent's checkout. The resolver owns the closed CONTEXT_REQUIRED outcome.
	response := callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "resolve"})

	requireUCICodebaseContextError(t, fixture, response, "CONTEXT_REQUIRED")
	require.Len(t, fixture.application.resolveInputs, 1)
	require.Equal(t, "mcp-client-a", fixture.application.resolveInputs[0].ClientSessionID)
	require.Equal(t, "agent/a", fixture.application.resolveInputs[0].Principal)
	assert.Equal(t, uciCodebaseContextTestRealm, fixture.application.resolveInputs[0].AuthRealm)
	require.Len(t, fixture.discovery.calls, 1, "candidate discovery is separate from authorization")
	require.Empty(t, fixture.catalog.calls, "ambiguity must refuse before canonical source lookup")
	require.Empty(t, fixture.authorizer.calls, "ambiguity must refuse before ContextAuthorizer")
	require.Empty(t, fixture.projection.calls, "ambiguity must refuse before metadata or query projection")
}

func TestUCICodebaseContextListAndSelectKeepClientDefaultsIsolated(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)

	selectedA := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, uciCodebaseContextSelectArgs(fixture.refA)))
	handleA := requireUCICodebaseContextPayload(t, fixture, selectedA, "agent-a-linked", "saved-a", fixture.refA.Generation)
	selectedB := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientB, uciCodebaseContextSelectArgs(fixture.refB)))
	handleB := requireUCICodebaseContextPayload(t, fixture, selectedB, "agent-b-linked", "saved-b", fixture.refB.Generation)
	assert.NotEqual(t, handleA, handleB, "each client receives an opaque handle for its own selected context")

	listedA := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "list"}))
	contextsA := requireUCICodebaseContextList(t, listedA, 1)
	requireUCICodebaseContextPayload(t, fixture, contextsA[0], "agent-a-linked", "saved-a", fixture.refA.Generation)

	listedB := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientB, map[string]any{"action": "list"}))
	contextsB := requireUCICodebaseContextList(t, listedB, 1)
	requireUCICodebaseContextPayload(t, fixture, contextsB[0], "agent-b-linked", "saved-b", fixture.refB.Generation)

	listedC := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientC, map[string]any{"action": "list"}))
	require.Empty(t, requireUCICodebaseContextList(t, listedC, 0), "list must contain only caller-authorized contexts")

	projectionsBeforeDeniedSelect := len(fixture.projection.calls)
	deniedSelect := callUCICodebaseContext(t, fixture.server, fixture.clientC, uciCodebaseContextSelectArgs(fixture.refB))
	requireUCICodebaseContextError(t, fixture, deniedSelect, "PERMISSION_DENIED")
	require.Len(t, fixture.projection.calls, projectionsBeforeDeniedSelect, "a denied client cannot project or replace another client's context")

	// Omitted selection reuses only the caller's existing UCI ContextResolver binding.
	reusedA := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "resolve"}))
	requireUCICodebaseContextPayload(t, fixture, reusedA, "agent-a-linked", "saved-a", fixture.refA.Generation)
	reusedB := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientB, map[string]any{"action": "resolve"}))
	requireUCICodebaseContextPayload(t, fixture, reusedB, "agent-b-linked", "saved-b", fixture.refB.Generation)
}

func TestUCICodebaseContextRejectsCrossClientHandleAndUnboundDefaultReuse(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)

	selectedA := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, uciCodebaseContextSelectArgs(fixture.refA)))
	handleA := requireUCICodebaseContextPayload(t, fixture, selectedA, "agent-a-linked", "saved-a", fixture.refA.Generation)
	selectedB := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientB, uciCodebaseContextSelectArgs(fixture.refB)))
	requireUCICodebaseContextPayload(t, fixture, selectedB, "agent-b-linked", "saved-b", fixture.refB.Generation)

	resolveCallsBeforeHandleReuse := len(fixture.application.resolveInputs)
	projectionsBeforeHandleReuse := len(fixture.projection.calls)
	crossClientHandle := callUCICodebaseContext(t, fixture.server, fixture.clientB, map[string]any{
		"action":         "select",
		"context_handle": handleA,
	})
	requireUCICodebaseContextError(t, fixture, crossClientHandle, "CONTEXT_MISMATCH")
	require.Len(t, fixture.application.resolveInputs, resolveCallsBeforeHandleReuse, "a foreign opaque handle must fail before application resolution")
	require.Len(t, fixture.projection.calls, projectionsBeforeHandleReuse, "a foreign opaque handle must fail before projection")

	projectionsBeforeUnboundReuse := len(fixture.projection.calls)
	unboundDefault := callUCICodebaseContext(t, fixture.server, fixture.clientC, map[string]any{"action": "resolve"})
	requireUCICodebaseContextError(t, fixture, unboundDefault, "CONTEXT_REQUIRED")
	require.Len(t, fixture.projection.calls, projectionsBeforeUnboundReuse, "an unbound client cannot reuse another client's default")

	// The failed B/C attempts must leave the two established bindings untouched.
	reusedA := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "resolve"}))
	requireUCICodebaseContextPayload(t, fixture, reusedA, "agent-a-linked", "saved-a", fixture.refA.Generation)
	reusedB := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientB, map[string]any{"action": "resolve"}))
	requireUCICodebaseContextPayload(t, fixture, reusedB, "agent-b-linked", "saved-b", fixture.refB.Generation)
}

func TestUCICodebaseContextSelectsRegisteredCheckoutWithoutView(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)
	bootstrap := uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      fixture.refA.SourceID,
			CheckoutID:    fixture.refA.CheckoutID,
			IncarnationID: uciCodebaseContextTestIncarnationA,
		},
		ProfileID:     fixture.refA.AnalysisProfileID,
		LocalRootID:   uciCodebaseContextPrivateLocatorA,
		WorkstationID: "keycard-a",
	}
	require.NoError(t, bootstrap.Validate())
	fixture.catalog.bindings[fixture.refA.CheckoutID] = bootstrap
	arguments := map[string]any{
		"action": "select",
		"checkout": map[string]any{
			"source_id":           bootstrap.Scope.SourceID,
			"checkout_id":         bootstrap.Scope.CheckoutID,
			"incarnation_id":      bootstrap.Scope.IncarnationID,
			"analysis_profile_id": bootstrap.ProfileID,
		},
	}
	selected := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, arguments))
	require.Equal(t, "checkout", selected["binding_kind"])
	require.Nil(t, selected["context"], "bootstrap output must explicitly state that no View exists")
	require.Equal(t, bootstrap.Scope.SourceID, selected["source_id"])
	require.Equal(t, bootstrap.Scope.CheckoutID, selected["checkout_id"])
	require.Equal(t, bootstrap.Scope.IncarnationID, selected["incarnation_id"])
	require.Equal(t, bootstrap.ProfileID, selected["analysis_profile_id"])
	handle, ok := selected["context_handle"].(string)
	require.True(t, ok)
	require.NotEmpty(t, handle)
	raw, err := json.Marshal(selected)
	require.NoError(t, err)
	require.NotContains(t, string(raw), bootstrap.LocalRootID)
	require.NotContains(t, string(raw), bootstrap.WorkstationID)

	mixed := callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{
		"action":    "select",
		"source_id": fixture.refA.SourceID,
		"checkout":  arguments["checkout"],
	})
	requireUCICodebaseContextError(t, fixture, mixed, "CONTEXT_MISMATCH")

	published := bootstrap.Clone()
	published.Context = &fixture.refA
	fixture.catalog.bindings[fixture.refA.CheckoutID] = published
	followed := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{
		"action":         "select",
		"context_handle": handle,
	}))
	require.Equal(t, handle, followed["context_handle"])
	require.Equal(t, "checkout", followed["binding_kind"])
	contextPayload, ok := followed["context"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, fixture.refA.ViewID, contextPayload["view_id"])
}

func uciCodebaseContextClient(sessionID, keycardID, principal string) context.Context {
	return auth.WithIdentity(
		ContextWithSession(context.Background(), sessionID),
		auth.ClientWithPrincipal("read-write", keycardID, principal, auth.PrincipalKindAgent),
	)
}

func uciCodebaseContextRef(checkoutID, viewID string, generation int64) uci.ContextRef {
	spaceID := uciCodebaseContextTestSpaceID
	return uci.ContextRef{
		SpaceID:           &spaceID,
		SourceID:          uciCodebaseContextTestSource,
		CheckoutID:        checkoutID,
		ViewID:            viewID,
		AnalysisProfileID: uciCodebaseContextTestProfile,
		Generation:        generation,
	}
}

func uciCodebaseContextSelectArgs(ref uci.ContextRef) map[string]any {
	args := map[string]any{
		"action":              "select",
		"source_id":           ref.SourceID,
		"checkout_id":         ref.CheckoutID,
		"view_id":             ref.ViewID,
		"analysis_profile_id": ref.AnalysisProfileID,
		"generation":          ref.Generation,
	}
	if ref.SpaceID != nil {
		args["space_id"] = *ref.SpaceID
	}
	return args
}

func uciCodebaseContextTool(t *testing.T, tools []Tool) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == "codebase_context" {
			return tool
		}
	}
	t.Fatalf("codebase_context was not advertised")
	return Tool{}
}

func callUCICodebaseContext(t *testing.T, server *Server, ctx context.Context, arguments map[string]any) *Response {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"name":      "codebase_context",
		"arguments": arguments,
	})
	require.NoError(t, err)
	response := server.HandleRequest(ctx, &Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})
	require.NotNil(t, response)
	return response
}

func decodeUCICodebaseContextResponse(t *testing.T, response *Response) map[string]any {
	t.Helper()
	require.Nil(t, response.Error)
	result, ok := response.Result.(map[string]any)
	require.True(t, ok, "tools/call result must use the MCP content envelope")
	content, ok := result["content"].([]map[string]any)
	require.True(t, ok, "tools/call result must carry text content")
	require.Len(t, content, 1)
	text, ok := content[0]["text"].(string)
	require.True(t, ok, "codebase_context result must be JSON text")
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	return payload
}

func requireUCICodebaseContextList(t *testing.T, payload map[string]any, wantLen int) []map[string]any {
	t.Helper()
	values, ok := payload["contexts"].([]any)
	require.True(t, ok, "list must return contexts")
	require.Len(t, values, wantLen)
	contexts := make([]map[string]any, len(values))
	for index, value := range values {
		contextPayload, ok := value.(map[string]any)
		require.True(t, ok, "contexts[%d] must be a context metadata object", index)
		contexts[index] = contextPayload
	}
	return contexts
}

func requireUCICodebaseContextPayload(t *testing.T, fixture *uciCodebaseContextFixture, payload map[string]any, checkout, view string, generation int64) string {
	t.Helper()
	handle, ok := payload["context_handle"].(string)
	require.True(t, ok, "authorized context response must return an opaque context_handle")
	require.NotEmpty(t, handle)
	assert.NotContains(t, handle, fixture.refA.SourceID)
	assert.NotContains(t, handle, fixture.refA.CheckoutID)
	assert.NotContains(t, handle, fixture.refA.ViewID)
	assert.NotContains(t, handle, fixture.refB.CheckoutID)
	assert.NotContains(t, handle, fixture.refB.ViewID)
	assert.NotContains(t, handle, uciCodebaseContextPrivateLocatorA)
	assert.NotContains(t, handle, uciCodebaseContextPrivateLocatorB)

	require.Equal(t, "engram", payload["source"])
	require.Equal(t, checkout, payload["checkout"])
	require.Equal(t, view, payload["view"])
	require.Equal(t, float64(generation), payload["generation"])

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), uciCodebaseContextPrivateLocatorA)
	assert.NotContains(t, string(raw), uciCodebaseContextPrivateLocatorB)
	assert.NotContains(t, string(raw), "private_locator")
	return handle
}

func requireUCICodebaseContextError(t *testing.T, fixture *uciCodebaseContextFixture, response *Response, wantCode string) {
	t.Helper()
	require.NotNil(t, response.Error)
	require.Nil(t, response.Result)
	require.Equal(t, -32000, response.Error.Code)
	require.Equal(t, wantCode, response.Error.Data)
	require.Contains(t, response.Error.Message, wantCode)

	raw, err := json.Marshal(response)
	require.NoError(t, err)
	for _, forbidden := range []string{
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
		fixture.refB.CheckoutID,
		fixture.refB.ViewID,
		"agent-a-linked",
		"agent-b-linked",
		"saved-a",
		"saved-b",
		uciCodebaseContextPrivateLocatorA,
		uciCodebaseContextPrivateLocatorB,
		"\"source\"",
		"\"checkout\"",
		"\"view\"",
		"\"count\"",
	} {
		assert.NotContains(t, string(raw), forbidden, "closed context failures must not disclose %q", forbidden)
	}
}

// uciCodebaseContextApplicationFake is the T021 application port requested by
// Server.SetCodebaseContextApplication. It intentionally uses the existing
// UCI ContextRef, ContextResolver, closed ContextError codes, and
// ContextAuthorizer boundary instead of defining a parallel context model.
type uciCodebaseContextApplicationFake struct {
	realm      string
	resolver   *uci.ContextResolver
	discovery  *uciCodebaseContextDiscoveryFake
	catalog    *uciCodebaseContextCatalogFake
	authorizer *uciCodebaseContextAuthorizerFake
	projection *uciCodebaseContextProjectionFake

	resolveInputs []uci.ResolveContextInput
	listInputs    []uci.ResolveContextInput
}

func (application *uciCodebaseContextApplicationFake) Resolve(ctx context.Context, input uci.ResolveContextInput) (uci.AuthorizedContext, error) {
	application.resolveInputs = append(application.resolveInputs, input)
	input = application.withRealm(input)

	resolved, err := application.resolver.Resolve(ctx, input)
	if err == nil || input.Ref != nil || len(input.Candidates) != 0 || !uciCodebaseContextRequired(err) {
		return resolved, err
	}

	// An unbound client first receives the resolver's closed no-selection result.
	// Only then does local candidate discovery run; a second resolver pass keeps
	// discovery separate from canonical lookup and authorization.
	candidates, err := application.discovery.Discover(ctx, input)
	if err != nil {
		return uci.AuthorizedContext{}, err
	}
	input.Candidates = candidates
	return application.resolver.Resolve(ctx, input)
}

func (application *uciCodebaseContextApplicationFake) ResolveIndexBinding(ctx context.Context, input uci.ResolveIndexBindingInput) (uci.AuthorizedIndexBinding, error) {
	if input.AuthRealm == "" {
		input.AuthRealm = application.realm
	}
	return application.resolver.ResolveIndexBinding(ctx, input)
}

func (application *uciCodebaseContextApplicationFake) AuthorizeIndexBinding(ctx context.Context, input uci.ResolveIndexBindingInput) (uci.AuthorizedIndexBinding, error) {
	if input.AuthRealm == "" {
		input.AuthRealm = application.realm
	}
	return application.resolver.AuthorizeIndexBinding(ctx, input)
}

func (application *uciCodebaseContextApplicationFake) BoundSelector(clientSessionID string) (uci.IndexBindingSelector, bool) {
	return application.resolver.BoundSelector(clientSessionID)
}

func (application *uciCodebaseContextApplicationFake) ForgetClient(clientSessionID string) {
	application.resolver.ForgetClient(clientSessionID)
}

func (application *uciCodebaseContextApplicationFake) List(ctx context.Context, input uci.ResolveContextInput) ([]uci.ContextRef, error) {
	application.listInputs = append(application.listInputs, input)
	input = application.withRealm(input)
	candidates, err := application.discovery.Discover(ctx, input)
	if err != nil {
		return nil, err
	}

	visible := make([]uci.ContextRef, 0, len(candidates))
	for _, candidate := range candidates {
		record, err := application.catalog.LoadContext(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if record.AuthRealm != input.AuthRealm {
			continue
		}
		if err := application.authorizer.AuthorizeContext(ctx, uci.ContextAccess{
			AuthRealm:  input.AuthRealm,
			Principal:  input.Principal,
			SourceID:   record.Ref.SourceID,
			CheckoutID: record.Ref.CheckoutID,
		}); err != nil {
			continue
		}
		visible = append(visible, record.Ref)
	}
	return visible, nil
}

func (application *uciCodebaseContextApplicationFake) Project(ctx context.Context, ref uci.ContextRef) (map[string]string, error) {
	return application.projection.Project(ctx, ref)
}

func (application *uciCodebaseContextApplicationFake) withRealm(input uci.ResolveContextInput) uci.ResolveContextInput {
	if input.AuthRealm == "" {
		input.AuthRealm = application.realm
	}
	return input
}

func uciCodebaseContextRequired(err error) bool {
	var contextErr *uci.ContextError
	return errors.As(err, &contextErr) && contextErr.Code() == uci.ContextRequired
}

type uciCodebaseContextDiscoveryFake struct {
	candidates map[string][]uci.ContextRef
	calls      []uci.ResolveContextInput
}

func (catalog *uciCodebaseContextCatalogFake) LoadIndexBinding(_ context.Context, selector uci.IndexBindingSelector) (uci.IndexBinding, error) {
	selector = selector.Clone()
	catalog.bindingCalls = append(catalog.bindingCalls, selector)
	if err := selector.Validate(); err != nil {
		return uci.IndexBinding{}, err
	}
	if ref, pinned := selector.Context(); pinned {
		binding, found := catalog.bindings[ref.CheckoutID]
		if !found || binding.Context == nil || !codebaseContextRefsEqual(*binding.Context, ref) {
			return uci.IndexBinding{}, errors.New("fixture index binding not found")
		}
		return binding.Clone(), nil
	}
	checkout, following := selector.Checkout()
	if !following {
		return uci.IndexBinding{}, errors.New("fixture invalid selector")
	}
	binding, found := catalog.bindings[checkout.Scope.CheckoutID]
	if !found || binding.Scope != checkout.Scope || binding.ProfileID != checkout.ProfileID {
		return uci.IndexBinding{}, errors.New("fixture index binding not found")
	}
	return binding.Clone(), nil
}

func (discovery *uciCodebaseContextDiscoveryFake) Discover(_ context.Context, input uci.ResolveContextInput) ([]uci.ContextRef, error) {
	discovery.calls = append(discovery.calls, input)
	return append([]uci.ContextRef(nil), discovery.candidates[input.ClientSessionID]...), nil
}

type uciCodebaseContextCatalogFake struct {
	records      map[string]uci.ContextRecord
	bindings     map[string]uci.IndexBinding
	calls        []uci.ContextRef
	bindingCalls []uci.IndexBindingSelector
}

func (catalog *uciCodebaseContextCatalogFake) LoadContext(_ context.Context, ref uci.ContextRef) (uci.ContextRecord, error) {
	catalog.calls = append(catalog.calls, ref)
	record, found := catalog.records[ref.CheckoutID]
	if !found {
		return uci.ContextRecord{}, errors.New("fixture context record not found")
	}
	return record, nil
}

type uciCodebaseContextAuthorizerFake struct {
	allowed map[string]map[string]bool
	calls   []uci.ContextAccess
}

var (
	_ codebaseContextIndexApplication = (*uciCodebaseContextApplicationFake)(nil)
	_ uci.IndexBindingCatalog         = (*uciCodebaseContextCatalogFake)(nil)
)

func (authorizer *uciCodebaseContextAuthorizerFake) AuthorizeContext(_ context.Context, access uci.ContextAccess) error {
	authorizer.calls = append(authorizer.calls, access)
	if authorizer.allowed[access.Principal][access.CheckoutID] {
		return nil
	}
	return errors.New("fixture private checkout denied")
}

type uciCodebaseContextProjectionFake struct {
	metadata map[string]map[string]string
	calls    []uci.ContextRef
}

func (projection *uciCodebaseContextProjectionFake) Project(_ context.Context, ref uci.ContextRef) (map[string]string, error) {
	projection.calls = append(projection.calls, ref)
	metadata, found := projection.metadata[ref.CheckoutID]
	if !found {
		return nil, errors.New("fixture context metadata not found")
	}
	copy := make(map[string]string, len(metadata))
	for key, value := range metadata {
		copy[key] = value
	}
	return copy, nil
}
