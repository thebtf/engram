package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

const (
	uciCodeIntelCompatibilityQuery       = "SharedTarget"
	uciCodeIntelCompatibilityPathPrefix  = "internal/"
	uciCodeIntelCompatibilityProject     = "legacy-engram"
	uciCodeIntelCompatibilityConflict    = "legacy-conflicting-source"
	uciCodeIntelCompatibilityBodyA       = "saved-body-A"
	uciCodeIntelCompatibilityBodyB       = "saved-body-B"
	uciCodeIntelCompatibilityExposureA   = "uci-exp_a"
	uciCodeIntelCompatibilityExposureB   = "uci-exp_b"
	uciCodeIntelCompatibilityDigestA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uciCodeIntelCompatibilityDigestB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	uciCodeIntelCompatibilityOtherSource = "20000000-0000-4000-8000-000000000002"
)

type uciCodeIntelCompatibilityFixture struct {
	*uciCodebaseContextFixture
	application   *uciCodeIntelCompatibilityApplication
	exposureStore *uciCodebaseExposureStoreFake
}

type uciCodeIntelCompatibilityAliasCall struct {
	ref     uci.ContextRef
	project string
}

type uciCodeIntelCompatibilitySearchCall struct {
	ref   uci.ContextRef
	input CodebaseSearchInput
}

// uciCodeIntelCompatibilityApplication composes T021's existing UCI context
// fixture with the smallest T027 query/status compatibility port. Its maps are
// keyed only by an AuthorizedContext-derived checkout, never raw project text.
type uciCodeIntelCompatibilityApplication struct {
	*uciCodebaseContextApplicationFake

	aliases         map[string]uci.AliasTarget
	queryResponses  map[string]uci.QueryResponse
	statusSnapshots map[string]CodebaseStatusSnapshot

	aliasCalls           []uciCodeIntelCompatibilityAliasCall
	searchCalls          []uciCodeIntelCompatibilitySearchCall
	statusCalls          []uci.ContextRef
	afterAliasResolution func()
	afterStatus          func()
}

var (
	_ CodebaseContextApplication      = (*uciCodeIntelCompatibilityApplication)(nil)
	_ CodebaseIntelligenceApplication = (*uciCodeIntelCompatibilityApplication)(nil)
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
		statusSnapshots: map[string]CodebaseStatusSnapshot{
			contextFixture.refA.CheckoutID: {
				TotalChunks:    17,
				EmbeddedChunks: 11,
				Embedding:      uci.EmbeddingStatus{Coverage: uci.IndexCoverageUnavailable},
				EvidenceRecorder: CodebaseEvidenceRecorderHealth{
					State:           "healthy",
					LastFailureCode: "NONE",
				},
			},
			contextFixture.refB.CheckoutID: {
				TotalChunks:    31,
				EmbeddedChunks: 19,
				Embedding:      uci.EmbeddingStatus{Coverage: uci.IndexCoverageUnavailable},
				EvidenceRecorder: CodebaseEvidenceRecorderHealth{
					State:           "degraded",
					LastFailureCode: "COMPLETION_EVIDENCE_UNAVAILABLE",
				},
			},
		},
	}

	// The scoped application owns context authority; the recorder is an
	// independent shared boundary that receives only released query metadata.
	contextFixture.server.SetCodebaseContextApplication(application)
	recorder, exposureStore := newUCICodebaseExposureRecorder()
	contextFixture.server.SetUCIExposureRecorder(recorder)
	return &uciCodeIntelCompatibilityFixture{
		uciCodebaseContextFixture: contextFixture,
		application:               application,
		exposureStore:             exposureStore,
	}
}

func (application *uciCodeIntelCompatibilityApplication) ResolveLegacyProject(_ context.Context, authorized uci.AuthorizedContext, project string) (uci.AliasTarget, error) {
	ref := authorized.Ref()
	application.aliasCalls = append(application.aliasCalls, uciCodeIntelCompatibilityAliasCall{ref: ref, project: project})
	target, found := application.aliases[project]
	if !found {
		return uci.AliasTarget{}, errors.New("compatibility project fixture is not mapped")
	}
	if application.afterAliasResolution != nil {
		application.afterAliasResolution()
	}
	return target, nil
}

func (application *uciCodeIntelCompatibilityApplication) SearchCodebase(_ context.Context, authorized uci.AuthorizedContext, input CodebaseSearchInput) (uci.QueryResponse, error) {
	ref := authorized.Ref()
	application.searchCalls = append(application.searchCalls, uciCodeIntelCompatibilitySearchCall{ref: ref, input: input})
	response, found := application.queryResponses[ref.CheckoutID]
	if !found {
		return uci.QueryResponse{}, errors.New("query fixture is not mapped to the authorized checkout")
	}
	return response, nil
}

func (application *uciCodeIntelCompatibilityApplication) CodebaseStatus(_ context.Context, authorized uci.AuthorizedContext) (CodebaseStatusSnapshot, error) {
	ref := authorized.Ref()
	application.statusCalls = append(application.statusCalls, ref)
	snapshot, found := application.statusSnapshots[ref.CheckoutID]
	if !found {
		return CodebaseStatusSnapshot{}, errors.New("status fixture is not mapped to the authorized checkout")
	}
	if application.afterStatus != nil {
		application.afterStatus()
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

	expectedA := fixture.application.queryResponses[fixture.refA.CheckoutID]
	expectedA.Exposure = responseA.Exposure
	expectedAJSON, err := json.Marshal(expectedA)
	require.NoError(t, err)
	expectedB := fixture.application.queryResponses[fixture.refB.CheckoutID]
	expectedB.Exposure = responseB.Exposure
	expectedBJSON, err := json.Marshal(expectedB)
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedAJSON), textA, "codebase_search must append one recorder-owned receipt for A only")
	assert.JSONEq(t, string(expectedBJSON), textB, "codebase_search must append one recorder-owned receipt for B only")
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
	}), fixture.refB, 31, 19, "healthy", "NONE")
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

func TestUCICodeIntelStatusSerializesClosedEmbeddingStatus(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	profileID := "55555555-5555-4555-8555-555555555555"
	jobState := uci.IndexStatusJobRetryScheduled
	errorCode := uci.EmbeddingFailureProviderUnavailable
	retryAfter := time.Date(2026, time.September, 6, 2, 3, 4, 0, time.UTC)
	snapshot := fixture.application.statusSnapshots[fixture.refA.CheckoutID]
	snapshot.Embedding = uci.EmbeddingStatus{
		EmbeddingProfileID: &profileID,
		Coverage:           uci.IndexCoveragePartial,
		TotalCandidates:    17,
		ReadyCandidates:    5,
		PendingJobs:        1,
		JobState:           &jobState,
		ErrorCode:          &errorCode,
		RetryAfter:         &retryAfter,
	}
	fixture.application.statusSnapshots[fixture.refA.CheckoutID] = snapshot

	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{"context_handle": handle})
	text := uciCodeIntelToolText(t, response)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	embeddingStatus, ok := payload["embedding"].(map[string]any)
	require.True(t, ok)
	require.Len(t, embeddingStatus, 8)
	assert.Equal(t, profileID, embeddingStatus["embedding_profile_id"])
	assert.Equal(t, string(uci.IndexCoveragePartial), embeddingStatus["coverage"])
	assert.Equal(t, float64(17), embeddingStatus["total_candidates"])
	assert.Equal(t, float64(5), embeddingStatus["ready_candidates"])
	assert.Equal(t, float64(1), embeddingStatus["pending_jobs"])
	assert.Equal(t, string(uci.IndexStatusJobRetryScheduled), embeddingStatus["job_state"])
	assert.Equal(t, string(uci.EmbeddingFailureProviderUnavailable), embeddingStatus["error_code"])
	assert.Equal(t, retryAfter.Format(time.RFC3339Nano), embeddingStatus["retry_after"])
	assert.NotContains(t, text, "https://")
	assert.NotContains(t, text, "api-key")
	assert.Zero(t, fixture.exposureStore.exposureCount())
	require.Len(t, fixture.application.statusCalls, 1)
}

func TestUCICodeIntelStatusReauthorizesBeforeEmbeddingSerialization(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	profileID := "55555555-5555-4555-8555-555555555555"
	jobState := uci.IndexStatusJobQueued
	snapshot := fixture.application.statusSnapshots[fixture.refA.CheckoutID]
	snapshot.Embedding = uci.EmbeddingStatus{
		EmbeddingProfileID: &profileID,
		Coverage:           uci.IndexCoveragePartial,
		TotalCandidates:    1,
		PendingJobs:        1,
		JobState:           &jobState,
	}
	fixture.application.statusSnapshots[fixture.refA.CheckoutID] = snapshot
	fixture.application.afterStatus = func() {
		fixture.server.codebaseContextMu.Lock()
		if client := fixture.server.codebaseContextHandles["mcp-client-a"]; client != nil {
			fixture.server.codebaseContextRemoveEntry(client, handle)
		}
		fixture.server.codebaseContextMu.Unlock()
	}

	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{"context_handle": handle})
	requireUCICodeIntelSafeToolError(t, response, "CONTEXT_MISMATCH", fixture)
	require.Len(t, fixture.application.statusCalls, 1)
	assert.Zero(t, fixture.exposureStore.exposureCount())
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "embedding")
}

func TestUCICodeIntelStatusRejectsContinuousCheckoutViewDrift(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	binding := uciCodeIntelCheckoutBinding(fixture.refA, &fixture.refA)
	fixture.catalog.bindings[fixture.refA.CheckoutID] = binding
	handle := fixture.selectCheckout(t, fixture.clientA, binding)

	current := fixture.refA
	views := []string{
		"40000000-0000-4000-8000-000000000005",
		"40000000-0000-4000-8000-000000000006",
	}
	advance := 0
	fixture.application.afterStatus = func() {
		if advance == len(views) {
			return
		}
		current.ViewID = views[advance]
		current.Generation++
		advance++
		fixture.catalog.records[current.CheckoutID] = uci.ContextRecord{Ref: current, AuthRealm: uciCodebaseContextTestRealm}
		movedBinding := binding.Clone()
		movedBinding.Context = &current
		fixture.catalog.bindings[current.CheckoutID] = movedBinding
	}

	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{"context_handle": handle})
	requireUCICodeIntelSafeToolError(t, response, "CONTEXT_MISMATCH", fixture)
	require.Len(t, fixture.application.statusCalls, codebaseStatusStabilizationAttempts)
	assert.Equal(t, fixture.refA, fixture.application.statusCalls[0])
	moved := fixture.refA
	moved.ViewID = views[0]
	moved.Generation++
	assert.Equal(t, moved, fixture.application.statusCalls[1])
	assert.Equal(t, len(views), advance)
	assert.Zero(t, fixture.exposureStore.exposureCount())
}

func TestUCICodeIntelExplicitHandlesAuthorizeWithoutSelectingDefaults(t *testing.T) {
	t.Run("pinned handle preserves checkout default", func(t *testing.T) {
		fixture := newUCICodeIntelCompatibilityFixture(t)
		pinnedHandle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		binding := uciCodeIntelCheckoutBinding(fixture.refA, &fixture.refA)
		fixture.catalog.bindings[fixture.refA.CheckoutID] = binding
		fixture.selectCheckout(t, fixture.clientA, binding)

		resolvesBefore := len(fixture.application.resolveInputs)
		authorizesBefore := len(fixture.application.authorizeInputs)
		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(pinnedHandle, uciCodeIntelCompatibilityProject, 10))
		requireUCICodeIntelQueryResponse(t, response, fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
		require.Len(t, fixture.application.resolveInputs, resolvesBefore, "explicit pinned handles must not select")
		require.Len(t, fixture.application.authorizeInputs, authorizesBefore+2, "initial lookup and release must both non-selectingly authorize the pinned View")
		for _, input := range fixture.application.authorizeInputs[authorizesBefore:] {
			require.NotNil(t, input.Ref)
			assert.Equal(t, fixture.refA, *input.Ref)
			assert.Empty(t, input.Candidates)
		}
		selector, found := fixture.application.BoundSelector("mcp-client-a")
		require.True(t, found)
		_, following := selector.Checkout()
		assert.True(t, following, "pinned-handle authorization must not replace a checkout default")
	})

	t.Run("checkout handle reloads current view without selecting", func(t *testing.T) {
		fixture := newUCICodeIntelCompatibilityFixture(t)
		binding := uciCodeIntelCheckoutBinding(fixture.refA, &fixture.refA)
		fixture.catalog.bindings[fixture.refA.CheckoutID] = binding
		checkoutHandle := fixture.selectCheckout(t, fixture.clientA, binding)

		resolvesBefore := len(fixture.application.resolveInputs)
		authorizesBefore := len(fixture.application.authorizeInputs)
		indexAuthorizesBefore := len(fixture.application.authorizeIndexInputs)
		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(checkoutHandle, uciCodeIntelCompatibilityProject, 10))
		requireUCICodeIntelQueryResponse(t, response, fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
		require.Len(t, fixture.application.resolveInputs, resolvesBefore, "explicit checkout handles must not select")
		require.Len(t, fixture.application.authorizeInputs, authorizesBefore+2)
		require.Len(t, fixture.application.authorizeIndexInputs, indexAuthorizesBefore+2, "checkout binding must be reauthorized for lookup and release")
		for _, input := range fixture.application.authorizeIndexInputs[indexAuthorizesBefore:] {
			require.NotNil(t, input.Selector)
			checkout, ok := input.Selector.Checkout()
			require.True(t, ok)
			assert.Equal(t, binding.Scope, checkout.Scope)
			assert.Equal(t, binding.ProfileID, checkout.ProfileID)
		}
		selector, found := fixture.application.BoundSelector("mcp-client-a")
		require.True(t, found)
		checkout, following := selector.Checkout()
		require.True(t, following)
		assert.Equal(t, binding.Scope, checkout.Scope)
	})

	t.Run("unpublished checkout suppresses search without exposure", func(t *testing.T) {
		fixture := newUCICodeIntelCompatibilityFixture(t)
		binding := uciCodeIntelCheckoutBinding(fixture.refA, nil)
		fixture.catalog.bindings[fixture.refA.CheckoutID] = binding
		checkoutHandle := fixture.selectCheckout(t, fixture.clientA, binding)

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(checkoutHandle, uciCodeIntelCompatibilityProject, 10))
		requireUCICodeIntelSuppressedQueryError(t, response, "CONTEXT_REQUIRED", fixture)
		assert.Empty(t, fixture.application.searchCalls)
		assert.Zero(t, fixture.exposureStore.exposureCount())
	})
}

func TestUCICodeIntelEvictedHandleCannotReleaseQueryResults(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	fixture.application.afterAliasResolution = func() {
		fixture.server.codebaseContextMu.Lock()
		if client := fixture.server.codebaseContextHandles["mcp-client-a"]; client != nil {
			fixture.server.codebaseContextRemoveEntry(client, handle)
		}
		fixture.server.codebaseContextMu.Unlock()
	}

	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handle, uciCodeIntelCompatibilityProject, 10))
	requireUCICodeIntelSuppressedQueryError(t, response, "CONTEXT_MISMATCH", fixture)
	require.Len(t, fixture.application.searchCalls, 1, "a handle evicted after lookup may not release its application result")
	assert.Zero(t, fixture.exposureStore.exposureCount())
}

func TestUCICodeIntelCheckoutViewDriftCannotReleaseQueryResults(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	binding := uciCodeIntelCheckoutBinding(fixture.refA, &fixture.refA)
	fixture.catalog.bindings[fixture.refA.CheckoutID] = binding
	handle := fixture.selectCheckout(t, fixture.clientA, binding)
	fixture.application.afterAliasResolution = func() {
		moved := fixture.refA
		moved.ViewID = "40000000-0000-4000-8000-000000000003"
		moved.Generation++
		fixture.catalog.records[moved.CheckoutID] = uci.ContextRecord{Ref: moved, AuthRealm: uciCodebaseContextTestRealm}
		movedBinding := binding.Clone()
		movedBinding.Context = &moved
		fixture.catalog.bindings[moved.CheckoutID] = movedBinding
	}

	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handle, uciCodeIntelCompatibilityProject, 10))
	requireUCICodeIntelSuppressedQueryError(t, response, "CONTEXT_MISMATCH", fixture)
	require.Len(t, fixture.application.searchCalls, 1)
	assert.Zero(t, fixture.exposureStore.exposureCount())
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

func TestUCICodeIntelCompatibilityNormalizesAndRejectsPathPrefixBeforeApplication(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)

	valid := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", map[string]any{
		"context_handle": handle,
		"query":          uciCodeIntelCompatibilityQuery,
		"path_prefix":    "./internal//",
		"limit":          10,
	})
	requireUCICodeIntelQueryResponse(t, valid, fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	require.Len(t, fixture.application.searchCalls, 1)
	require.Equal(t, CodebaseSearchInput{
		Query:      uciCodeIntelCompatibilityQuery,
		PathPrefix: "internal",
		Limit:      10,
	}, fixture.application.searchCalls[0].input)

	resolvesBefore := len(fixture.application.resolveInputs)
	aliasesBefore := len(fixture.application.aliasCalls)
	queriesBefore := len(fixture.application.searchCalls)
	for _, tc := range []struct {
		name       string
		pathPrefix any
	}{
		{name: "null", pathPrefix: nil},
		{name: "absolute", pathPrefix: "/workspace/internal"},
		{name: "traversal", pathPrefix: "internal/../outside"},
		{name: "windows", pathPrefix: `C:\workspace\internal`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", map[string]any{
				"context_handle": handle,
				"query":          uciCodeIntelCompatibilityQuery,
				"path_prefix":    tc.pathPrefix,
			})
			require.NotNil(t, response.Error)
			require.Nil(t, response.Result)
			assert.Contains(t, strings.ToLower(response.Error.Message), "path prefix")
			require.Len(t, fixture.application.resolveInputs, resolvesBefore, "invalid path prefix must fail before context resolution")
			require.Len(t, fixture.application.aliasCalls, aliasesBefore)
			require.Len(t, fixture.application.searchCalls, queriesBefore, "invalid path prefix must not reach the application")
		})
	}

	rawInvalidUTF8 := append([]byte(`{"query":"`+uciCodeIntelCompatibilityQuery+`","path_prefix":"`), byte(0xff))
	rawInvalidUTF8 = append(rawInvalidUTF8, []byte(`"}`)...)
	_, err := decodeCodebaseSearchArgs(rawInvalidUTF8)
	require.Error(t, err, "invalid UTF-8 path prefix must not be normalized through JSON replacement")
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

func TestUCINoMixedQueryPath(t *testing.T) {
	t.Run("current default and explicit handles use UCI without a legacy chunk store", func(t *testing.T) {
		fixture := newUCICodeIntelCompatibilityFixture(t)
		require.Nil(t, fixture.server.legacyUnscopedCodeChunkStore, "the UCI fixture must not wire the raw-project reader")
		handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)

		defaultSearch, _ := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", map[string]any{
			"query":       uciCodeIntelCompatibilityQuery,
			"path_prefix": uciCodeIntelCompatibilityPathPrefix,
			"project":     uciCodeIntelCompatibilityProject,
		}), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
		explicitSearch, _ := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handleA, uciCodeIntelCompatibilityProject, 10)), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
		requireUCICodeIntelStatus(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
			"project": uciCodeIntelCompatibilityProject,
		}), fixture.refA, 17, 11, "healthy", "NONE")
		requireUCICodeIntelStatus(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
			"context_handle": handleA,
			"project":        uciCodeIntelCompatibilityProject,
		}), fixture.refA, 17, 11, "healthy", "NONE")

		assert.NotContains(t, defaultSearch, "legacy_unscoped")
		assert.NotContains(t, explicitSearch, "legacy_unscoped")
		require.Len(t, fixture.application.aliasCalls, 4)
		require.Len(t, fixture.application.searchCalls, 2)
		assert.Equal(t, fixture.refA, fixture.application.searchCalls[0].ref)
		assert.Equal(t, fixture.refA, fixture.application.searchCalls[1].ref)
		require.Len(t, fixture.application.statusCalls, 2)
		assert.Equal(t, fixture.refA, fixture.application.statusCalls[0])
		assert.Equal(t, fixture.refA, fixture.application.statusCalls[1])
	})

	t.Run("project compatibility evidence cannot select another source or view", func(t *testing.T) {
		fixture := newUCICodeIntelCompatibilityFixture(t)
		handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
		fixture.selectContext(t, fixture.clientB, fixture.refB)

		_, current := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", map[string]any{
			"query":       uciCodeIntelCompatibilityQuery,
			"path_prefix": uciCodeIntelCompatibilityPathPrefix,
			"project":     uciCodeIntelCompatibilityProject,
		}), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
		require.NotNil(t, current.Contexts)
		assert.Equal(t, fixture.refA.ViewID, (*current.Contexts)[0].ViewID, "the same legacy project must not select B's current view")
		require.Len(t, fixture.application.aliasCalls, 1)
		assert.Equal(t, fixture.refA, fixture.application.aliasCalls[0].ref)
		require.Len(t, fixture.application.searchCalls, 1)
		assert.Equal(t, fixture.refA, fixture.application.searchCalls[0].ref)

		conflictingSearch := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handleA, uciCodeIntelCompatibilityConflict, 10))
		requireUCICodeIntelSuppressedQueryError(t, conflictingSearch, "CONTEXT_MISMATCH", fixture)
		conflictingStatus := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
			"context_handle": handleA,
			"project":        uciCodeIntelCompatibilityConflict,
		})
		requireUCICodeIntelSafeToolError(t, conflictingStatus, "CONTEXT_MISMATCH", fixture)
		require.Len(t, fixture.application.aliasCalls, 3)
		require.Len(t, fixture.application.searchCalls, 1, "conflicting compatibility evidence must not select a raw-project result")
		require.Empty(t, fixture.application.statusCalls, "conflicting compatibility evidence must not select a raw-project status")
	})

	t.Run("closed context, epoch, and application failures never fall back", func(t *testing.T) {
		t.Run("missing UCI application", func(t *testing.T) {
			fixture := newUCICodeIntelCompatibilityFixture(t)
			fixture.server.SetCodebaseContextApplication(nil)
			require.Nil(t, fixture.server.legacyUnscopedCodeChunkStore)

			search := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", map[string]any{
				"query":       uciCodeIntelCompatibilityQuery,
				"path_prefix": uciCodeIntelCompatibilityPathPrefix,
				"project":     uciCodeIntelCompatibilityProject,
			})
			requireUCICodeIntelSuppressedQueryError(t, search, "CONTEXT_REQUIRED", fixture)
			status := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
				"project": uciCodeIntelCompatibilityProject,
			})
			requireUCICodeIntelSafeToolError(t, status, "CONTEXT_REQUIRED", fixture)
			require.Empty(t, fixture.application.aliasCalls)
			require.Empty(t, fixture.application.searchCalls)
			require.Empty(t, fixture.application.statusCalls)
		})

		t.Run("unbound current context", func(t *testing.T) {
			fixture := newUCICodeIntelCompatibilityFixture(t)
			require.Nil(t, fixture.server.legacyUnscopedCodeChunkStore)

			search := callUCICodeIntel(t, fixture.server, fixture.clientC, "codebase_search", map[string]any{
				"query":       uciCodeIntelCompatibilityQuery,
				"path_prefix": uciCodeIntelCompatibilityPathPrefix,
				"project":     uciCodeIntelCompatibilityProject,
			})
			requireUCICodeIntelSuppressedQueryError(t, search, "CONTEXT_REQUIRED", fixture)
			status := callUCICodeIntel(t, fixture.server, fixture.clientC, "codebase_status", map[string]any{
				"project": uciCodeIntelCompatibilityProject,
			})
			requireUCICodeIntelSafeToolError(t, status, "CONTEXT_REQUIRED", fixture)
			require.Len(t, fixture.application.resolveInputs, 2)
			require.Empty(t, fixture.application.aliasCalls)
			require.Empty(t, fixture.application.searchCalls)
			require.Empty(t, fixture.application.statusCalls)
		})

		t.Run("selected context without an intelligence application", func(t *testing.T) {
			fixture := newUCICodeIntelCompatibilityFixture(t)
			fixture.selectContext(t, fixture.clientA, fixture.refA)
			resolvesBefore := len(fixture.application.resolveInputs)
			fixture.server.SetCodebaseContextApplication(fixture.uciCodebaseContextFixture.application)
			require.Nil(t, fixture.server.legacyUnscopedCodeChunkStore)

			search := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", map[string]any{
				"query":       uciCodeIntelCompatibilityQuery,
				"path_prefix": uciCodeIntelCompatibilityPathPrefix,
				"project":     uciCodeIntelCompatibilityProject,
			})
			requireUCICodeIntelSuppressedQueryError(t, search, "CONTEXT_REQUIRED", fixture)
			status := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
				"project": uciCodeIntelCompatibilityProject,
			})
			requireUCICodeIntelSafeToolError(t, status, "CONTEXT_REQUIRED", fixture)
			require.Len(t, fixture.application.resolveInputs, resolvesBefore+2)
			require.Empty(t, fixture.application.aliasCalls)
			require.Empty(t, fixture.application.searchCalls)
			require.Empty(t, fixture.application.statusCalls)
		})

		t.Run("epoch changes after compatibility evidence", func(t *testing.T) {
			t.Run("search", func(t *testing.T) {
				fixture := newUCICodeIntelCompatibilityFixture(t)
				handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
				fixture.application.afterAliasResolution = func() {
					fixture.server.SetCodebaseContextApplication(fixture.application)
				}

				response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handleA, uciCodeIntelCompatibilityProject, 10))
				requireUCICodeIntelSuppressedQueryError(t, response, "CONTEXT_MISMATCH", fixture)
				require.Len(t, fixture.application.aliasCalls, 1)
				require.Empty(t, fixture.application.searchCalls)
				require.Empty(t, fixture.application.statusCalls)
			})

			t.Run("status", func(t *testing.T) {
				fixture := newUCICodeIntelCompatibilityFixture(t)
				handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
				fixture.application.afterAliasResolution = func() {
					fixture.server.SetCodebaseContextApplication(fixture.application)
				}

				response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
					"context_handle": handleA,
					"project":        uciCodeIntelCompatibilityProject,
				})
				requireUCICodeIntelSafeToolError(t, response, "CONTEXT_MISMATCH", fixture)
				require.Len(t, fixture.application.aliasCalls, 1)
				require.Empty(t, fixture.application.searchCalls)
				require.Empty(t, fixture.application.statusCalls)
			})
		})
	})

	t.Run("legacy handlers remain explicitly unscoped but are not public tools", func(t *testing.T) {
		t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
		server := NewServer(ServerOptions{Version: "uci-legacy-unscoped"})
		server.SetLegacyUnscopedCodeChunkStore(newUCICodeIntelLegacyUnscopedStore(t))

		searchArguments := map[string]any{
			"query":   "needle",
			"project": "legacy-project",
			"limit":   1,
		}
		searchRaw, err := json.Marshal(searchArguments)
		require.NoError(t, err)
		searchText, err := server.handleLegacyUnscopedCodebaseSearch(context.Background(), searchRaw)
		require.NoError(t, err)
		requireUCICodeIntelLegacyUnscopedResponse(t, searchText)

		statusArguments := map[string]any{"project": "legacy-project"}
		statusRaw, err := json.Marshal(statusArguments)
		require.NoError(t, err)
		statusText, err := server.handleLegacyUnscopedCodebaseStatus(context.Background(), statusRaw)
		require.NoError(t, err)
		requireUCICodeIntelLegacyUnscopedResponse(t, statusText)

		requireUCICodeIntelLegacyToolNotPublic(t, server, "codebase_search_legacy_unscoped", searchArguments)
		requireUCICodeIntelLegacyToolNotPublic(t, server, "codebase_status_legacy_unscoped", statusArguments)
	})
}

func (fixture *uciCodeIntelCompatibilityFixture) selectContext(t *testing.T, client context.Context, ref uci.ContextRef) string {
	t.Helper()
	payload := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, client, uciCodebaseContextSelectArgs(ref)))
	return requireUCICodebaseContextPayload(t, fixture.uciCodebaseContextFixture, payload, map[bool]string{true: "agent-a-linked", false: "agent-b-linked"}[ref.CheckoutID == fixture.refA.CheckoutID], map[bool]string{true: "saved-a", false: "saved-b"}[ref.CheckoutID == fixture.refA.CheckoutID], ref.Generation)
}

func uciCodeIntelCheckoutBinding(ref uci.ContextRef, current *uci.ContextRef) uci.IndexBinding {
	binding := uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      ref.SourceID,
			CheckoutID:    ref.CheckoutID,
			IncarnationID: uciCodebaseContextTestIncarnationA,
		},
		ProfileID:     ref.AnalysisProfileID,
		LocalRootID:   uciCodebaseContextPrivateLocatorA,
		WorkstationID: "keycard-a",
	}
	if current != nil {
		contextRef := cloneCodebaseContextRef(*current)
		binding.Context = &contextRef
	}
	return binding
}

func (fixture *uciCodeIntelCompatibilityFixture) selectCheckout(t *testing.T, client context.Context, binding uci.IndexBinding) string {
	t.Helper()
	payload := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, client, map[string]any{
		"action": "select",
		"checkout": map[string]any{
			"source_id":           binding.Scope.SourceID,
			"checkout_id":         binding.Scope.CheckoutID,
			"incarnation_id":      binding.Scope.IncarnationID,
			"analysis_profile_id": binding.ProfileID,
		},
	}))
	handle, ok := payload["context_handle"].(string)
	require.True(t, ok)
	require.NotEmpty(t, handle)
	return handle
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
	assert.NotContains(t, text, "legacy_unscoped", "current UCI query responses must not claim legacy retrieval semantics")
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

	embedding, ok := payload["embedding"].(map[string]any)
	require.True(t, ok, "status must retain typed embedding state")
	require.Len(t, embedding, 8)
	assert.Nil(t, embedding["embedding_profile_id"])
	assert.Equal(t, string(uci.IndexCoverageUnavailable), embedding["coverage"])
	assert.Equal(t, float64(0), embedding["total_candidates"])
	assert.Equal(t, float64(0), embedding["ready_candidates"])
	assert.Equal(t, float64(0), embedding["pending_jobs"])
	assert.Nil(t, embedding["job_state"])
	assert.Nil(t, embedding["error_code"])
	assert.Nil(t, embedding["retry_after"])

	recorder, ok := payload["evidence_recorder"].(map[string]any)
	require.True(t, ok, "authorized status must retain secret-free recorder health")
	require.Len(t, recorder, 2, "recorder health must expose only its public state and failure code")
	assert.Equal(t, wantState, recorder["state"])
	assert.Equal(t, wantFailureCode, recorder["last_failure_code"])
	assert.NotContains(t, text, `"project"`, "status must not echo legacy project text as authority")
	assert.NotContains(t, text, "private_locator")
	assert.NotContains(t, text, "legacy_unscoped", "current UCI status responses must not claim legacy retrieval semantics")
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

func requireUCICodeIntelLegacyUnscopedResponse(t *testing.T, text string) {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	assert.Equal(t, "legacy_unscoped", payload["retrieval_mode"])

	var current uci.QueryResponse
	assert.Error(t, json.Unmarshal([]byte(text), &current), "legacy responses must not decode as the strict current UCI View response schema")
	assert.Nil(t, current.Contexts)
}

func requireUCICodeIntelLegacyToolNotPublic(t *testing.T, server *Server, name string, arguments map[string]any) {
	t.Helper()
	tools := server.ListTools()
	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Name)
	}
	assert.NotContains(t, toolNames, name, "%s must not have a public tool schema", name)

	response := callUCICodeIntel(t, server, context.Background(), name, arguments)
	require.NotNil(t, response.Error)
	require.Nil(t, response.Result)
	assert.Contains(t, response.Error.Message, "unknown tool")
	assert.Contains(t, response.Error.Message, name)
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
		"embedding",
		"edges",
		"uci-exp_",
		"private_locator",
	} {
		assert.NotContains(t, raw, forbidden, "closed failures must not disclose %q", forbidden)
	}
}

func newUCICodeIntelLegacyUnscopedStore(t *testing.T) *gormdb.CodeChunkStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	_, err = sqlDB.Exec(`
		CREATE TABLE code_chunks (
			id INTEGER PRIMARY KEY,
			project_id TEXT NOT NULL,
			file_path TEXT NOT NULL,
			byte_start INTEGER NOT NULL,
			byte_end INTEGER NOT NULL,
			language TEXT NOT NULL,
			chunk_type TEXT NOT NULL,
			content TEXT NOT NULL,
			content_sha256 TEXT NOT NULL,
			index_session_id TEXT NOT NULL,
			embedding BLOB,
			created_at DATETIME,
			updated_at DATETIME
		)
	`)
	require.NoError(t, err)
	return gormdb.NewCodeChunkStore(db)
}

func uciCodeIntelCompatibilityQueryResponse(t *testing.T, ref uci.ContextRef, excerpt, _ string, digest string) uci.QueryResponse {
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
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}
	require.NoError(t, response.ValidatePreExposure(), "fixture must begin as a valid recordable UCI QueryResponse")
	return response
}
