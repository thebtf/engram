package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciCodeIntelCompatibilityQuery       = "SharedTarget"
	uciCodeIntelCompatibilityPathPrefix  = "internal/"
	uciCodeIntelCompatibilityProject     = "legacy-engram"
	uciCodeIntelCompatibilityConflict    = "legacy-conflicting-source"
	uciCodeIntelCompatibilityBodyA       = "saved-body-A"
	uciCodeIntelCompatibilityBodyB       = "saved-body-B"
	uciCodeIntelCompatibilityExposureA   = "uci-exp-a"
	uciCodeIntelCompatibilityExposureB   = "uci-exp-b"
	uciCodeIntelCompatibilityDigestA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uciCodeIntelCompatibilityDigestB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	uciCodeIntelCompatibilityOtherSource = "20000000-0000-4000-8000-000000000002"
)

type uciCodeIntelCompatibilityFixture struct {
	*uciCodebaseContextFixture
	application *uciCodeIntelCompatibilityApplication
}

type uciCodeIntelCompatibilityAliasCall struct {
	ref     uci.ContextRef
	project string
}

type uciCodeIntelCompatibilitySearchCall struct {
	ref   uci.ContextRef
	input codebaseSearchInput
}

// uciCodeIntelCompatibilityApplication composes T021's existing UCI context
// fixture with the smallest T027 query/status compatibility port. Its maps are
// keyed only by an AuthorizedContext-derived checkout, never raw project text.
type uciCodeIntelCompatibilityApplication struct {
	*uciCodebaseContextApplicationFake

	aliases         map[string]uci.AliasTarget
	queryResponses  map[string]uci.QueryResponse
	statusSnapshots map[string]codebaseStatusSnapshot

	aliasCalls  []uciCodeIntelCompatibilityAliasCall
	searchCalls []uciCodeIntelCompatibilitySearchCall
	statusCalls []uci.ContextRef
}

var (
	_ codebaseContextApplication      = (*uciCodeIntelCompatibilityApplication)(nil)
	_ codebaseIntelligenceApplication = (*uciCodeIntelCompatibilityApplication)(nil)
)

func newUCICodeIntelCompatibilityFixture(t *testing.T) *uciCodeIntelCompatibilityFixture {
	t.Helper()

	contextFixture := newUCICodebaseContextFixture(t)
	sourceID := uciCodebaseContextTestSource
	spaceID := uciCodebaseContextTestSpaceID
	otherSourceID := uciCodeIntelCompatibilityOtherSource
	application := &uciCodeIntelCompatibilityApplication{
		uciCodebaseContextApplicationFake: contextFixture.application,
		aliases: map[string]uci.AliasTarget{
			uciCodeIntelCompatibilityProject: {
				SpaceID:  &spaceID,
				SourceID: &sourceID,
			},
			uciCodeIntelCompatibilityConflict: {
				SpaceID:  &spaceID,
				SourceID: &otherSourceID,
			},
		},
		queryResponses: map[string]uci.QueryResponse{
			contextFixture.refA.CheckoutID: uciCodeIntelCompatibilityQueryResponse(t, contextFixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityExposureA, uciCodeIntelCompatibilityDigestA),
			contextFixture.refB.CheckoutID: uciCodeIntelCompatibilityQueryResponse(t, contextFixture.refB, uciCodeIntelCompatibilityBodyB, uciCodeIntelCompatibilityExposureB, uciCodeIntelCompatibilityDigestB),
		},
		statusSnapshots: map[string]codebaseStatusSnapshot{
			contextFixture.refA.CheckoutID: {
				TotalChunks:    17,
				EmbeddedChunks: 11,
				EvidenceRecorder: codebaseEvidenceRecorderHealth{
					State:           "healthy",
					LastFailureCode: "NONE",
				},
			},
			contextFixture.refB.CheckoutID: {
				TotalChunks:    31,
				EmbeddedChunks: 19,
				EvidenceRecorder: codebaseEvidenceRecorderHealth{
					State:           "degraded",
					LastFailureCode: "COMPLETION_EVIDENCE_UNAVAILABLE",
				},
			},
		},
	}

	// This is intentionally the only test injection: the existing setter owns
	// the client-scoped opaque handle table, while this fake implements its
	// optional UCI query/status port.
	contextFixture.server.SetCodebaseContextApplication(application)
	return &uciCodeIntelCompatibilityFixture{
		uciCodebaseContextFixture: contextFixture,
		application:               application,
	}
}

func (application *uciCodeIntelCompatibilityApplication) ResolveLegacyProject(_ context.Context, authorized uci.AuthorizedContext, project string) (uci.AliasTarget, error) {
	ref := authorized.Ref()
	application.aliasCalls = append(application.aliasCalls, uciCodeIntelCompatibilityAliasCall{ref: ref, project: project})
	target, found := application.aliases[project]
	if !found {
		return uci.AliasTarget{}, errors.New("compatibility project fixture is not mapped")
	}
	return target, nil
}

func (application *uciCodeIntelCompatibilityApplication) SearchCodebase(_ context.Context, authorized uci.AuthorizedContext, input codebaseSearchInput) (uci.QueryResponse, error) {
	ref := authorized.Ref()
	application.searchCalls = append(application.searchCalls, uciCodeIntelCompatibilitySearchCall{ref: ref, input: input})
	response, found := application.queryResponses[ref.CheckoutID]
	if !found {
		return uci.QueryResponse{}, errors.New("query fixture is not mapped to the authorized checkout")
	}
	return response, nil
}

func (application *uciCodeIntelCompatibilityApplication) CodebaseStatus(_ context.Context, authorized uci.AuthorizedContext) (codebaseStatusSnapshot, error) {
	ref := authorized.Ref()
	application.statusCalls = append(application.statusCalls, ref)
	snapshot, found := application.statusSnapshots[ref.CheckoutID]
	if !found {
		return codebaseStatusSnapshot{}, errors.New("status fixture is not mapped to the authorized checkout")
	}
	return snapshot, nil
}

func TestUCICodeIntelCompatibilitySearchAndStatusKeepClientContextsDistinct(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
	handleB := fixture.selectContext(t, fixture.clientB, fixture.refB)

	argumentsA := uciCodeIntelCompatibilitySearchArguments(handleA, uciCodeIntelCompatibilityProject, 10)
	argumentsB := uciCodeIntelCompatibilitySearchArguments(handleB, uciCodeIntelCompatibilityProject, 10)
	searchA := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", argumentsA)
	searchB := callUCICodeIntel(t, fixture.server, fixture.clientB, "codebase_search", argumentsB)

	textA, responseA := requireUCICodeIntelQueryResponse(t, searchA, fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	textB, responseB := requireUCICodeIntelQueryResponse(t, searchB, fixture.refB, uciCodeIntelCompatibilityBodyB, uciCodeIntelCompatibilityBodyA)
	assert.NotEqual(t, textA, textB, "identical query/path labels must remain bound to distinct selected views")

	expectedA, err := json.Marshal(fixture.application.queryResponses[fixture.refA.CheckoutID])
	require.NoError(t, err)
	expectedB, err := json.Marshal(fixture.application.queryResponses[fixture.refB.CheckoutID])
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedA), textA, "codebase_search must expose the closed UCI response for A only")
	assert.JSONEq(t, string(expectedB), textB, "codebase_search must expose the closed UCI response for B only")
	assert.NotEqual(t, responseA.Contexts, responseB.Contexts)

	require.Len(t, fixture.application.aliasCalls, 2)
	require.Equal(t, uciCodeIntelCompatibilityProject, fixture.application.aliasCalls[0].project)
	require.Equal(t, fixture.refA, fixture.application.aliasCalls[0].ref)
	require.Equal(t, uciCodeIntelCompatibilityProject, fixture.application.aliasCalls[1].project)
	require.Equal(t, fixture.refB, fixture.application.aliasCalls[1].ref)
	require.Len(t, fixture.application.searchCalls, 2)
	assert.Equal(t, fixture.refA, fixture.application.searchCalls[0].ref)
	assert.Equal(t, fixture.refB, fixture.application.searchCalls[1].ref)
	assert.Equal(t, fixture.application.searchCalls[0].input, fixture.application.searchCalls[1].input, "the fixture changes only the selected client context")

	statusA := requireUCICodeIntelStatus(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
		"context_handle": handleA,
		"project":        uciCodeIntelCompatibilityProject,
	}), fixture.refA, 17, 11, "healthy", "NONE")
	statusB := requireUCICodeIntelStatus(t, callUCICodeIntel(t, fixture.server, fixture.clientB, "codebase_status", map[string]any{
		"context_handle": handleB,
		"project":        uciCodeIntelCompatibilityProject,
	}), fixture.refB, 31, 19, "degraded", "COMPLETION_EVIDENCE_UNAVAILABLE")
	assert.NotEqual(t, statusA["total_chunks"], statusB["total_chunks"], "status counts must remain scoped to the authorized context")
	require.Len(t, fixture.application.statusCalls, 2)
	assert.Equal(t, fixture.refA, fixture.application.statusCalls[0])
	assert.Equal(t, fixture.refB, fixture.application.statusCalls[1])

	// Omitted handles must reuse only the exact caller's selected default.
	_, reusedA := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", map[string]any{
		"query":       uciCodeIntelCompatibilityQuery,
		"path_prefix": uciCodeIntelCompatibilityPathPrefix,
	}), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	_, reusedB := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientB, "codebase_search", map[string]any{
		"query":       uciCodeIntelCompatibilityQuery,
		"path_prefix": uciCodeIntelCompatibilityPathPrefix,
	}), fixture.refB, uciCodeIntelCompatibilityBodyB, uciCodeIntelCompatibilityBodyA)
	assert.NotEqual(t, reusedA.Contexts, reusedB.Contexts)
}

func TestUCICodeIntelCompatibilityRefusesUnboundForeignAndConflictingSelectors(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
	fixture.selectContext(t, fixture.clientB, fixture.refB)

	queriesBefore := len(fixture.application.searchCalls)
	statusesBefore := len(fixture.application.statusCalls)
	aliasesBefore := len(fixture.application.aliasCalls)
	resolvesBefore := len(fixture.application.resolveInputs)

	// C has two candidates and no selected default. It must not borrow A/B.
	unbound := callUCICodeIntel(t, fixture.server, fixture.clientC, "codebase_search", map[string]any{
		"query":       uciCodeIntelCompatibilityQuery,
		"path_prefix": uciCodeIntelCompatibilityPathPrefix,
	})
	requireUCICodeIntelSuppressedQueryError(t, unbound, "CONTEXT_REQUIRED", fixture)
	require.Len(t, fixture.application.resolveInputs, resolvesBefore+1)
	require.Len(t, fixture.application.aliasCalls, aliasesBefore)
	require.Len(t, fixture.application.searchCalls, queriesBefore)
	require.Len(t, fixture.application.statusCalls, statusesBefore)

	// An opaque handle is owned by its client. C cannot reach either resolution
	// or the query/status application through an A-owned handle.
	foreignSearch := callUCICodeIntel(t, fixture.server, fixture.clientC, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handleA, uciCodeIntelCompatibilityProject, 10))
	requireUCICodeIntelSuppressedQueryError(t, foreignSearch, "CONTEXT_MISMATCH", fixture)
	require.Len(t, fixture.application.resolveInputs, resolvesBefore+1, "foreign handles fail before application resolution")
	require.Len(t, fixture.application.aliasCalls, aliasesBefore, "foreign handles cannot reach compatibility lookup")
	require.Len(t, fixture.application.searchCalls, queriesBefore, "foreign handles cannot reach query storage")

	foreignStatus := callUCICodeIntel(t, fixture.server, fixture.clientC, "codebase_status", map[string]any{"context_handle": handleA})
	requireUCICodeIntelSafeToolError(t, foreignStatus, "CONTEXT_MISMATCH", fixture)
	require.Len(t, fixture.application.statusCalls, statusesBefore, "foreign handles cannot reach status storage")

	// A legacy selector is evidence only: its typed alias must agree with A's
	// already authorized source before any search/status call is allowed.
	conflictingSearch := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handleA, uciCodeIntelCompatibilityConflict, 10))
	requireUCICodeIntelSuppressedQueryError(t, conflictingSearch, "CONTEXT_MISMATCH", fixture)
	require.Len(t, fixture.application.aliasCalls, aliasesBefore+1)
	require.Len(t, fixture.application.searchCalls, queriesBefore, "conflicting compatibility evidence must fail before query storage")

	conflictingStatus := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
		"context_handle": handleA,
		"project":        uciCodeIntelCompatibilityConflict,
	})
	requireUCICodeIntelSafeToolError(t, conflictingStatus, "CONTEXT_MISMATCH", fixture)
	require.Len(t, fixture.application.statusCalls, statusesBefore, "conflicting compatibility evidence must fail before status storage")

	// All failed C/A attempts leave the established A/B defaults untouched.
	_, reusedA := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", map[string]any{
		"query":       uciCodeIntelCompatibilityQuery,
		"path_prefix": uciCodeIntelCompatibilityPathPrefix,
	}), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	_, reusedB := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientB, "codebase_search", map[string]any{
		"query":       uciCodeIntelCompatibilityQuery,
		"path_prefix": uciCodeIntelCompatibilityPathPrefix,
	}), fixture.refB, uciCodeIntelCompatibilityBodyB, uciCodeIntelCompatibilityBodyA)
	assert.NotEqual(t, reusedA.Contexts, reusedB.Contexts)
}

func TestUCICodeIntelCompatibilityRejectsOversizedLimitBeforeApplication(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)

	resolvesBefore := len(fixture.application.resolveInputs)
	aliasesBefore := len(fixture.application.aliasCalls)
	queriesBefore := len(fixture.application.searchCalls)
	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handleA, uciCodeIntelCompatibilityProject, 51))

	require.NotNil(t, response.Error)
	require.Nil(t, response.Result)
	assert.Equal(t, -32000, response.Error.Code)
	assert.Contains(t, strings.ToLower(response.Error.Message), "limit")
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	requireUCICodeIntelNoLeaks(t, string(raw), fixture)
	require.Len(t, fixture.application.resolveInputs, resolvesBefore, "an oversized limit is invalid before context resolution")
	require.Len(t, fixture.application.aliasCalls, aliasesBefore)
	require.Len(t, fixture.application.searchCalls, queriesBefore, "oversized limits must be rejected, never silently clamped")
}

func TestUCICodeIntelCompatibilityToolSchemasAdvertiseContextHandleWithoutProjectAuthority(t *testing.T) {
	for _, tool := range []Tool{codebaseSearchTool(), codebaseStatusTool()} {
		tool := tool
		t.Run(tool.Name, func(t *testing.T) {
			properties, ok := tool.InputSchema["properties"].(map[string]any)
			require.True(t, ok, "%s must expose an object property schema", tool.Name)

			contextHandle, ok := properties["context_handle"].(map[string]any)
			require.True(t, ok, "%s must accept a caller-owned context_handle", tool.Name)
			require.Equal(t, "string", contextHandle["type"])

			project, ok := properties["project"].(map[string]any)
			require.True(t, ok, "%s must retain project only as a compatibility input", tool.Name)
			projectDescription, ok := project["description"].(string)
			require.True(t, ok, "%s.project must describe its compatibility-only meaning", tool.Name)
			projectDescription = strings.ToLower(projectDescription)
			assert.Contains(t, projectDescription, "compatibility")
			assert.NotContains(t, projectDescription, "project id to search")
			assert.NotContains(t, projectDescription, "defaults to the current session project")
			assert.NotContains(t, strings.ToLower(tool.Description), "project id")
		})
	}
}

func (fixture *uciCodeIntelCompatibilityFixture) selectContext(t *testing.T, client context.Context, ref uci.ContextRef) string {
	t.Helper()
	payload := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, client, uciCodebaseContextSelectArgs(ref)))
	return requireUCICodebaseContextPayload(t, fixture.uciCodebaseContextFixture, payload, map[bool]string{true: "agent-a-linked", false: "agent-b-linked"}[ref.CheckoutID == fixture.refA.CheckoutID], map[bool]string{true: "saved-a", false: "saved-b"}[ref.CheckoutID == fixture.refA.CheckoutID], ref.Generation)
}

func uciCodeIntelCompatibilitySearchArguments(handle, project string, limit int) map[string]any {
	return map[string]any{
		"context_handle": handle,
		"project":        project,
		"query":          uciCodeIntelCompatibilityQuery,
		"path_prefix":    uciCodeIntelCompatibilityPathPrefix,
		"limit":          limit,
	}
}

func callUCICodeIntel(t *testing.T, server *Server, ctx context.Context, name string, arguments map[string]any) *Response {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"name":      name,
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

func requireUCICodeIntelQueryResponse(t *testing.T, response *Response, wantRef uci.ContextRef, wantExcerpt, forbiddenExcerpt string) (string, uci.QueryResponse) {
	t.Helper()
	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate(), "codebase_search must return the closed UCI QueryResponse contract")
	require.Equal(t, uci.QueryStatusOK, payload.Status)
	require.NotNil(t, payload.Contexts)
	require.Len(t, *payload.Contexts, 1)
	contextRef := (*payload.Contexts)[0]
	assert.Equal(t, wantRef.SourceID, contextRef.SourceID)
	assert.Equal(t, wantRef.CheckoutID, contextRef.CheckoutID)
	assert.Equal(t, wantRef.ViewID, contextRef.ViewID)
	assert.Equal(t, wantRef.Generation, contextRef.Generation)
	assert.Equal(t, wantRef.AnalysisProfileID, contextRef.ProfileID)
	if wantRef.SpaceID != nil {
		require.NotNil(t, contextRef.SpaceID)
		assert.Equal(t, *wantRef.SpaceID, *contextRef.SpaceID)
	}
	require.NotNil(t, payload.Items)
	require.Len(t, *payload.Items, 1)
	assert.Equal(t, wantExcerpt, (*payload.Items)[0].Excerpt)
	assert.NotContains(t, text, forbiddenExcerpt)
	assert.NotContains(t, text, `"project"`, "a raw legacy project selector must not become query authority or output")
	return text, payload
}

func requireUCICodeIntelStatus(t *testing.T, response *Response, wantRef uci.ContextRef, wantTotal, wantEmbedded int64, wantState, wantFailureCode string) map[string]any {
	t.Helper()
	text := uciCodeIntelToolText(t, response)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))

	contextValue, ok := payload["context"].(map[string]any)
	require.True(t, ok, "status must name the authorized scoped context")
	assert.Equal(t, wantRef.SourceID, contextValue["source_id"])
	assert.Equal(t, wantRef.CheckoutID, contextValue["checkout_id"])
	assert.Equal(t, wantRef.ViewID, contextValue["view_id"])
	assert.Equal(t, float64(wantRef.Generation), contextValue["generation"])
	assert.Equal(t, wantRef.AnalysisProfileID, contextValue["profile_id"])
	if wantRef.SpaceID != nil {
		assert.Equal(t, *wantRef.SpaceID, contextValue["space_id"])
	}
	assert.Equal(t, float64(wantTotal), payload["total_chunks"])
	assert.Equal(t, float64(wantEmbedded), payload["embedded_chunks"])

	recorder, ok := payload["evidence_recorder"].(map[string]any)
	require.True(t, ok, "authorized status must retain secret-free recorder health")
	require.Len(t, recorder, 2, "recorder health must expose only its public state and failure code")
	assert.Equal(t, wantState, recorder["state"])
	assert.Equal(t, wantFailureCode, recorder["last_failure_code"])
	assert.NotContains(t, text, `"project"`, "status must not echo legacy project text as authority")
	assert.NotContains(t, text, "private_locator")
	return payload
}

func requireUCICodeIntelSuppressedQueryError(t *testing.T, response *Response, wantCode string, fixture *uciCodeIntelCompatibilityFixture) {
	t.Helper()
	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate(), "context failures must use a valid suppressed UCI response")
	assert.Equal(t, uci.QueryStatusContextRequired, payload.Status)
	require.NotNil(t, payload.Error)
	assert.Equal(t, uci.QueryErrorCode(wantCode), payload.Error.Code)
	assert.Nil(t, payload.Exposure, "context failures never disclose an exposure receipt")
	assert.Nil(t, payload.Contexts)
	assert.Nil(t, payload.Freshness)
	assert.Nil(t, payload.Retrieval)
	assert.Nil(t, payload.Coverage)
	assert.Nil(t, payload.Items)
	assert.Nil(t, payload.Graph)
	assert.Nil(t, payload.Truncated)
	assert.Nil(t, payload.Warnings)
	assert.Nil(t, payload.Continuation)
	requireUCICodeIntelNoLeaks(t, text, fixture)
}

func requireUCICodeIntelSafeToolError(t *testing.T, response *Response, wantCode string, fixture *uciCodeIntelCompatibilityFixture) {
	t.Helper()
	require.NotNil(t, response.Error)
	require.Nil(t, response.Result)
	assert.Equal(t, -32000, response.Error.Code)
	assert.Equal(t, wantCode, response.Error.Data)
	assert.Contains(t, response.Error.Message, wantCode)
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	requireUCICodeIntelNoLeaks(t, string(raw), fixture)
}

func uciCodeIntelToolText(t *testing.T, response *Response) string {
	t.Helper()
	require.Nil(t, response.Error)
	result, ok := response.Result.(map[string]any)
	require.True(t, ok, "tools/call must retain the MCP content envelope")
	content, ok := result["content"].([]map[string]any)
	require.True(t, ok, "tools/call must return text content")
	require.Len(t, content, 1)
	text, ok := content[0]["text"].(string)
	require.True(t, ok, "tool response content must be JSON text")
	return text
}

func requireUCICodeIntelNoLeaks(t *testing.T, raw string, fixture *uciCodeIntelCompatibilityFixture) {
	t.Helper()
	for _, forbidden := range []string{
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
		fixture.refB.CheckoutID,
		fixture.refB.ViewID,
		uciCodeIntelCompatibilityBodyA,
		uciCodeIntelCompatibilityBodyB,
		"total_chunks",
		"embedded_chunks",
		"evidence_recorder",
		"edges",
		"uci-exp_",
		"private_locator",
	} {
		assert.NotContains(t, raw, forbidden, "closed failures must not disclose %q", forbidden)
	}
}

func uciCodeIntelCompatibilityQueryResponse(t *testing.T, ref uci.ContextRef, excerpt, exposureRef, digest string) uci.QueryResponse {
	t.Helper()
	zero := int64(0)
	truncated := false
	contexts := uci.QueryContexts{uci.QueryContextRef{
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
		ViewID:     ref.ViewID,
		Generation: ref.Generation,
		ProfileID:  ref.AnalysisProfileID,
		SpaceID:    ref.SpaceID,
	}}
	items := uci.QueryItems{uci.QueryItem{
		Ref: uci.QueryEntityRef{
			SourceID:  ref.SourceID,
			ViewID:    ref.ViewID,
			EntityKey: uciCodeIntelCompatibilityQuery,
		},
		Path: uciCodeIntelCompatibilityPathPrefix + "shared.go",
		Span: uci.QuerySpan{
			ByteStart: 0,
			ByteEnd:   int64(len(excerpt)),
			LineStart: 1,
			LineEnd:   1,
		},
		ContentDigest: uci.QueryContentDigest(digest),
		Kind:          uci.QueryItemCode,
		Language:      "go",
		Excerpt:       excerpt,
		MatchSources:  []uci.QueryMatchSource{uci.QueryMatchFTS},
	}}
	warnings := uci.QueryWarnings{}
	continuation := uci.QueryContinuation{}
	response := uci.QueryResponse{
		Schema:   uci.QueryResponseSchema,
		Status:   uci.QueryStatusOK,
		Contexts: &contexts,
		Freshness: &uci.QueryFreshness{
			State:          uci.QueryFreshnessObservedCurrent,
			Method:         uci.QueryFreshnessWatchWatermark,
			PendingChanges: &zero,
			EnrichmentWatermark: uci.QueryEnrichmentWatermark{
				Sequence: ref.Generation,
				State:    uci.QueryEnrichmentCurrent,
			},
		},
		Retrieval: &uci.QueryRetrieval{
			Mode:               uci.QueryRetrievalLexical,
			DegradationReasons: []string{},
		},
		Coverage: &uci.QueryCoverage{
			Structural:       uci.IndexCoverageComplete,
			UnresolvedSites:  &zero,
			UnsupportedFiles: &zero,
		},
		Exposure: &uci.QueryExposure{
			ExposureRef:     exposureRef,
			CompletionState: uci.QueryCompletionUnknown,
		},
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}
	require.NoError(t, response.Validate(), "fixture must begin as a valid closed UCI QueryResponse")
	return response
}
