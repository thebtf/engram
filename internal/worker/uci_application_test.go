package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

func TestUCIApplicationMCPUsesExactPostgreSQLViews(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	store := openWorkerUCIContextCompositionStore(t)
	server := mcp.NewServer(mcp.ServerOptions{Version: "uci-application-postgres"})
	composition, err := composeUCIContext(true, store.GetDB(), server, workerUCISemanticConfig())
	require.NoError(t, err)

	fixture := newWorkerUCIApplicationFixture(t, composition)
	identity := auth.ClientWithPrincipal("read-write", fixture.workstationID, fixture.principal, auth.PrincipalKindAgent)
	caller := auth.WithIdentity(
		mcp.ContextWithSession(context.Background(), fixture.clientSessionID),
		identity,
	)
	historicalCaller := auth.WithIdentity(
		mcp.ContextWithSession(context.Background(), fixture.clientSessionID+"-historical"),
		identity,
	)

	currentHandle := workerUCISelectCheckout(t, server, caller, fixture.source.SourceID, fixture.checkout, fixture.profile.ProfileID)
	currentSelection := workerUCIApplicationToolResponse(t, server, caller, "codebase_context", map[string]any{
		"action": "resolve",
	})
	var currentPayload struct {
		Context *struct {
			SourceID string `json:"source_id"`
			ViewID   string `json:"view_id"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(workerUCIApplicationToolText(t, currentSelection)), &currentPayload))
	require.NotNil(t, currentPayload.Context, "checkout selection: %s", workerUCIApplicationToolText(t, currentSelection))
	require.Equal(t, fixture.current.Context.SourceID, currentPayload.Context.SourceID)
	require.Equal(t, fixture.current.Context.ViewID, currentPayload.Context.ViewID)
	historicalHandle := workerUCIApplicationSelectContext(t, server, historicalCaller, fixture.historical.Context)

	beforeExposure := workerUCIApplicationExposureCount(t, store.GetDB())
	search := workerUCIApplicationToolResponse(t, server, caller, "codebase_search", map[string]any{
		"context_handle": currentHandle,
		"query":          "SearchNeedle",
		"path_prefix":    "internal/",
		"limit":          10,
	})
	searchResponse := workerUCIApplicationQueryResponse(t, search)
	require.NoError(t, searchResponse.Validate())
	require.NotNil(t, searchResponse.Retrieval)
	require.Equal(t, uci.QueryRetrievalLexical, searchResponse.Retrieval.Mode)
	require.Empty(t, searchResponse.Retrieval.DegradationReasons)
	require.NotNil(t, searchResponse.Contexts, "search response: %s", workerUCIApplicationToolText(t, search))
	require.Equal(t, fixture.current.Context.ViewID, (*searchResponse.Contexts)[0].ViewID)
	require.NotNil(t, searchResponse.Exposure, "MCP must append evidence before releasing a code search")
	require.Len(t, *searchResponse.Items, 2, "path prefix must exclude the outside-View path without suppressing matching internal files")
	for _, item := range *searchResponse.Items {
		require.Truef(t, strings.HasPrefix(item.Path, "internal/"), "path prefix leaked %q", item.Path)
	}
	currentItem := workerUCIApplicationItemAtPath(t, *searchResponse.Items, "internal/alpha.go")
	require.Contains(t, currentItem.Excerpt, "current-published-body")
	require.Equal(t, beforeExposure+1, workerUCIApplicationExposureCount(t, store.GetDB()))

	// The separate client has a pinned historical default. Omitting the handle
	// must not borrow the checkout-following default from the first client.
	historicalSearch := workerUCIApplicationToolResponse(t, server, historicalCaller, "codebase_search", map[string]any{
		"query":       "SearchNeedle",
		"path_prefix": "internal/",
		"limit":       10,
	})
	historicalResponse := workerUCIApplicationQueryResponse(t, historicalSearch)
	require.NoError(t, historicalResponse.Validate())
	require.Equal(t, fixture.historical.Context.ViewID, (*historicalResponse.Contexts)[0].ViewID)
	require.Len(t, *historicalResponse.Items, 2)
	for _, item := range *historicalResponse.Items {
		require.Truef(t, strings.HasPrefix(item.Path, "internal/"), "path prefix leaked %q", item.Path)
	}
	historicalItem := workerUCIApplicationItemAtPath(t, *historicalResponse.Items, "internal/alpha.go")
	require.Contains(t, historicalItem.Excerpt, "historical-published-body")
	require.NotContains(t, historicalItem.Excerpt, "current-published-body")

	graph := workerUCIApplicationToolResponse(t, server, historicalCaller, "codebase_graph", map[string]any{
		"context_handle": historicalHandle,
		"action":         "neighbors",
		"target": map[string]any{
			"source_id":  historicalItem.Ref.SourceID,
			"view_id":    historicalItem.Ref.ViewID,
			"entity_key": historicalItem.Ref.EntityKey,
		},
		"direction": "outgoing",
		"relations": []string{"calls"},
		"max_depth": 2,
		"max_nodes": 20,
		"max_edges": 20,
	})
	graphResponse := workerUCIApplicationQueryResponse(t, graph)
	require.NoError(t, graphResponse.Validate())
	require.NotNil(t, graphResponse.Graph)
	require.Equal(t, uci.QueryRetrievalGraph, graphResponse.Retrieval.Mode)
	require.Len(t, graphResponse.Graph.Edges, 1)
	require.Equal(t, fixture.historical.Context.SourceID, graphResponse.Graph.Edges[0].From.SourceID)
	require.Equal(t, fixture.historical.Context.ViewID, graphResponse.Graph.Edges[0].From.ViewID)
	require.Equal(t, "calls", string(graphResponse.Graph.Edges[0].Relation))

	read := workerUCIApplicationToolResponse(t, server, historicalCaller, "codebase_read", map[string]any{
		"context_handle": historicalHandle,
		"ref": map[string]any{
			"source_id":  historicalItem.Ref.SourceID,
			"view_id":    historicalItem.Ref.ViewID,
			"entity_key": historicalItem.Ref.EntityKey,
		},
		"span": map[string]any{
			"byte_start": historicalItem.Span.ByteStart,
			"byte_end":   historicalItem.Span.ByteEnd,
			"line_start": historicalItem.Span.LineStart,
			"line_end":   historicalItem.Span.LineEnd,
		},
		"content_digest": historicalItem.ContentDigest,
		"max_bytes":      historicalItem.Span.ByteEnd - historicalItem.Span.ByteStart,
	})
	readResponse := workerUCIApplicationQueryResponse(t, read)
	require.NoError(t, readResponse.Validate())
	require.Len(t, *readResponse.Items, 1)
	require.Contains(t, (*readResponse.Items)[0].Excerpt, "historical-published-body")
	require.NotContains(t, (*readResponse.Items)[0].Excerpt, "current-published-body")

	status := workerUCIApplicationToolResponse(t, server, caller, "codebase_status", map[string]any{
		"context_handle": currentHandle,
	})
	var statusResponse workerUCIApplicationStatusResponse
	require.NoError(t, json.Unmarshal([]byte(workerUCIApplicationToolText(t, status)), &statusResponse))
	require.Equal(t, fixture.current.Context.ViewID, statusResponse.Context.ViewID)
	require.EqualValues(t, 3, statusResponse.TotalChunks)
	require.EqualValues(t, 0, statusResponse.EmbeddedChunks)
	require.Nil(t, statusResponse.Embedding.EmbeddingProfileID)
	require.Equal(t, uci.IndexCoverageUnavailable, statusResponse.Embedding.Coverage)
	require.Zero(t, statusResponse.Embedding.TotalCandidates)
	require.Zero(t, statusResponse.Embedding.ReadyCandidates)
	require.Zero(t, statusResponse.Embedding.PendingJobs)
	require.Nil(t, statusResponse.Embedding.JobState)
	require.Nil(t, statusResponse.Embedding.ErrorCode)
	require.Nil(t, statusResponse.Embedding.RetryAfter)
	require.Equal(t, uci.QueryFreshnessObservedCurrent, statusResponse.Freshness.State)
	require.Equal(t, uci.QueryFreshnessWatchWatermark, statusResponse.Freshness.Method)
	require.Equal(t, "healthy", statusResponse.EvidenceRecorder.State)

	beforeCrossView := workerUCIApplicationExposureCount(t, store.GetDB())
	crossViewRead := workerUCIApplicationToolResponse(t, server, caller, "codebase_read", map[string]any{
		"context_handle": currentHandle,
		"ref": map[string]any{
			"source_id":  historicalItem.Ref.SourceID,
			"view_id":    historicalItem.Ref.ViewID,
			"entity_key": historicalItem.Ref.EntityKey,
		},
		"span": map[string]any{
			"byte_start": historicalItem.Span.ByteStart,
			"byte_end":   historicalItem.Span.ByteEnd,
			"line_start": historicalItem.Span.LineStart,
			"line_end":   historicalItem.Span.LineEnd,
		},
		"content_digest": historicalItem.ContentDigest,
		"max_bytes":      historicalItem.Span.ByteEnd - historicalItem.Span.ByteStart,
	})
	crossViewResponse := workerUCIApplicationQueryResponse(t, crossViewRead)
	require.NoError(t, crossViewResponse.Validate())
	require.Equal(t, uci.QueryStatusContextRequired, crossViewResponse.Status)
	require.Equal(t, uci.QueryErrorContextMismatch, crossViewResponse.Error.Code)
	require.Nil(t, crossViewResponse.Contexts)
	require.Equal(t, beforeCrossView, workerUCIApplicationExposureCount(t, store.GetDB()))

	server.SetUCIExposureRecorder(uci.NewExposureRecorder(workerUCIApplicationFailingExposureStore{}, nil))
	beforeFailure := workerUCIApplicationExposureCount(t, store.GetDB())
	suppressed := workerUCIApplicationToolResponse(t, server, caller, "codebase_search", map[string]any{
		"context_handle": currentHandle,
		"query":          "SearchNeedle",
		"path_prefix":    "internal/",
		"limit":          10,
	})
	suppressedResponse := workerUCIApplicationQueryResponse(t, suppressed)
	require.NoError(t, suppressedResponse.Validate())
	require.Equal(t, uci.QueryStatusUnavailable, suppressedResponse.Status)
	require.Equal(t, uci.QueryErrorExposureUnavailable, suppressedResponse.Error.Code)
	require.Nil(t, suppressedResponse.Contexts)
	require.Nil(t, suppressedResponse.Exposure)
	require.Equal(t, beforeFailure, workerUCIApplicationExposureCount(t, store.GetDB()))
}

func TestUCIApplicationFTSSearchDoesNotWaitForQueuedEmbedding(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	const model = "worker-uci-fts-first"
	providerStarted := make(chan struct{}, 1)
	providerRelease := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case providerStarted <- struct{}{}:
		default:
		}
		<-providerRelease
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": []map[string]any{{"embedding": make([]float32, embedding.EmbeddingDim), "index": 0}}})
	}))
	t.Cleanup(provider.Close)
	t.Setenv("ENGRAM_EMBEDDING_URL", provider.URL)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", model)
	sharedClient, err := embedding.NewClientWithSettings(context.Background(), nil)
	require.NoError(t, err)
	semantic := newUCISemanticConfig(context.Background(), nil, sharedClient, sharedClient)
	store := openWorkerUCIContextCompositionStore(t)
	server := mcp.NewServer(mcp.ServerOptions{Version: "uci-application-fts-first"})
	composition, err := composeUCIContext(true, store.GetDB(), server, semantic)
	require.NoError(t, err)
	fixture := newWorkerUCIApplicationFixture(t, composition)
	identity := auth.ClientWithPrincipal("read-write", fixture.workstationID, fixture.principal, auth.PrincipalKindAgent)
	caller := auth.WithIdentity(mcp.ContextWithSession(context.Background(), fixture.clientSessionID), identity)
	handle := workerUCISelectCheckout(t, server, caller, fixture.source.SourceID, fixture.checkout, fixture.profile.ProfileID)
	parameters, err := json.Marshal(map[string]any{"name": "codebase_search", "arguments": map[string]any{"context_handle": handle, "query": "SearchNeedle", "path_prefix": "internal/", "limit": 10}})
	require.NoError(t, err)
	responses := make(chan *mcp.Response, 1)
	go func() {
		responses <- server.HandleRequest(caller, &mcp.Request{JSONRPC: "2.0", ID: uuid.NewString(), Method: "tools/call", Params: parameters})
	}()
	select {
	case <-providerStarted:
		close(providerRelease)
		<-responses
		t.Fatal("structural FTS search invoked the queued embedding provider")
	case response := <-responses:
		require.NotNil(t, response)
		require.Nil(t, response.Error)
		search := workerUCIApplicationQueryResponse(t, response)
		require.Equal(t, uci.QueryRetrievalLexical, search.Retrieval.Mode)
		select {
		case <-providerStarted:
			t.Fatal("structural FTS search invoked the queued embedding provider")
		default:
		}
	case <-time.After(time.Second):
		t.Fatal("structural FTS search waited for queued embedding")
	}
}

func TestUCIApplicationOperatorSearchUsesSemanticOnlyWhenCoverageComplete(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	const model = "worker-uci-shared-embedding"
	var providerCalls atomic.Int32
	var corpusProviderInputs atomic.Int32
	var queryProviderInputs atomic.Int32
	provider := newWorkerUCIApplicationEmbeddingProvider(t, workerUCIApplicationEmbeddingProviderInput{
		model:                model,
		providerCalls:        &providerCalls,
		corpusProviderInputs: &corpusProviderInputs,
		queryProviderInputs:  &queryProviderInputs,
	})
	t.Setenv("ENGRAM_EMBEDDING_URL", provider.URL)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", model)

	sharedClient, err := embedding.NewClientWithSettings(context.Background(), nil)
	require.NoError(t, err)
	semantic := newUCISemanticConfig(context.Background(), nil, sharedClient, sharedClient)

	store := openWorkerUCIContextCompositionStore(t)
	server := mcp.NewServer(mcp.ServerOptions{Version: "uci-application-shared-embedding"})
	composition, err := composeUCIContext(true, store.GetDB(), server, semantic)
	require.NoError(t, err)
	require.NotNil(t, composition.application.semanticService)
	require.NotNil(t, composition.embeddingProfile)
	require.Equal(t, semantic.profile, *composition.embeddingProfile)
	require.NotNil(t, composition.embeddingWorker)

	fixture := newWorkerUCIApplicationFixture(t, composition)
	identity := auth.ClientWithPrincipal("read-write", fixture.workstationID, fixture.principal, auth.PrincipalKindAgent)
	caller := auth.WithIdentity(mcp.ContextWithSession(context.Background(), fixture.clientSessionID), identity)
	handle := workerUCISelectCheckout(t, server, caller, fixture.source.SourceID, fixture.checkout, fixture.profile.ProfileID)
	search := workerUCIApplicationToolResponse(t, server, caller, "codebase_search", map[string]any{
		"context_handle": handle,
		"query":          "SearchNeedle",
		"path_prefix":    "internal/",
		"limit":          10,
	})
	response := workerUCIApplicationQueryResponse(t, search)
	require.NoError(t, response.Validate())
	require.NotNil(t, response.Retrieval)
	require.Equal(t, uci.QueryRetrievalLexical, response.Retrieval.Mode)
	require.Empty(t, response.Retrieval.DegradationReasons)
	require.NotNil(t, response.Items)
	require.NotEmpty(t, *response.Items)
	require.Zero(t, providerCalls.Load())
	status := workerUCIApplicationToolResponse(t, server, caller, "codebase_status", map[string]any{
		"context_handle": handle,
	})
	statusText := workerUCIApplicationToolText(t, status)
	var statusResponse workerUCIApplicationStatusResponse
	require.NoError(t, json.Unmarshal([]byte(statusText), &statusResponse))
	require.NotNil(t, statusResponse.Embedding.EmbeddingProfileID)
	require.NotEqual(t, provider.URL, *statusResponse.Embedding.EmbeddingProfileID)
	require.Equal(t, uci.IndexCoveragePartial, statusResponse.Embedding.Coverage)
	require.EqualValues(t, 3, statusResponse.Embedding.TotalCandidates)
	require.Zero(t, statusResponse.Embedding.ReadyCandidates)
	require.EqualValues(t, 1, statusResponse.Embedding.PendingJobs)
	require.NotNil(t, statusResponse.Embedding.JobState)
	require.Equal(t, uci.IndexStatusJobQueued, *statusResponse.Embedding.JobState)
	require.Nil(t, statusResponse.Embedding.ErrorCode)
	require.Nil(t, statusResponse.Embedding.RetryAfter)
	require.Equal(t, uci.QueryFreshnessObservedCurrent, statusResponse.Freshness.State)
	require.NotContains(t, statusText, provider.URL)

	ref := fixture.current.Context
	authorized, err := composition.resolver.Authorize(context.Background(), uci.ResolveContextInput{
		ClientSessionID: "worker-uci-embedding-status-" + uuid.NewString(),
		AuthRealm:       string(auth.SourceClient),
		Principal:       fixture.principal,
		Ref:             &ref,
	})
	require.NoError(t, err)
	incompleteSpec := uci.QuerySpec{
		ClientSessionID: "operator-code/" + fixture.clientSessionID,
		Mode:            uci.QueryModeFTS,
		Text:            "SearchNeedle",
		Filter:          uci.QueryFilter{PathPrefix: "internal/"},
		Order:           uci.QueryOrderRelevance,
		Limit:           1,
	}
	incomplete, err := composition.application.SearchOperatorCodebase(context.Background(), authorized, incompleteSpec)
	require.NoError(t, err)
	require.NoError(t, incomplete.ValidatePreExposure())
	require.NotNil(t, incomplete.Retrieval)
	require.Equal(t, uci.QueryRetrievalLexical, incomplete.Retrieval.Mode)
	require.NotNil(t, incomplete.Contexts)
	require.Equal(t, fixture.current.Context.ViewID, (*incomplete.Contexts)[0].ViewID)
	require.Zero(t, providerCalls.Load(), "incomplete coverage must not invoke semantic retrieval")
	workerContext, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- composition.embeddingWorker.Run(workerContext, "worker-uci-embedding-"+uuid.NewString())
	}()
	t.Cleanup(func() {
		stopWorker()
		select {
		case workerErr := <-workerDone:
			require.NoError(t, workerErr)
		case <-time.After(5 * time.Second):
			t.Error("UCI embedding worker did not join after cancellation")
		}
	})

	require.Eventually(t, func() bool {
		snapshot, statusErr := composition.projectionStore.LoadIndexStatus(context.Background(), authorized, composition.embeddingProfile)
		return statusErr == nil && snapshot.Embedding.Coverage == uci.IndexCoverageComplete && snapshot.Embedding.ReadyCandidates == 3
	}, 10*time.Second, 20*time.Millisecond, "runtime worker must populate every current-View corpus vector")

	status = workerUCIApplicationToolResponse(t, server, caller, "codebase_status", map[string]any{"context_handle": handle})
	statusText = workerUCIApplicationToolText(t, status)
	var completedStatus workerUCIApplicationStatusResponse
	require.NoError(t, json.Unmarshal([]byte(statusText), &completedStatus))
	require.Equal(t, uci.IndexCoverageComplete, completedStatus.Embedding.Coverage)

	require.EqualValues(t, 3, completedStatus.Embedding.TotalCandidates)
	require.EqualValues(t, 3, completedStatus.Embedding.ReadyCandidates)
	require.Zero(t, completedStatus.Embedding.PendingJobs)
	require.NotNil(t, completedStatus.Embedding.JobState)
	require.Equal(t, uci.IndexStatusJobSucceeded, *completedStatus.Embedding.JobState)
	require.NotContains(t, statusText, provider.URL)

	semanticSearch := workerUCIApplicationToolResponse(t, server, caller, "codebase_search", map[string]any{
		"context_handle": handle,
		"query":          "NoLexicalMatchToken",
		"path_prefix":    "internal/",
		"limit":          10,
	})
	semanticResponse := workerUCIApplicationQueryResponse(t, semanticSearch)
	require.NoError(t, semanticResponse.Validate())
	require.NotNil(t, semanticResponse.Retrieval)
	require.Equal(t, uci.QueryRetrievalHybrid, semanticResponse.Retrieval.Mode)
	require.NotNil(t, semanticResponse.Retrieval.VectorCoverage)
	require.Equal(t, float64(1), *semanticResponse.Retrieval.VectorCoverage)
	require.Empty(t, semanticResponse.Retrieval.DegradationReasons)
	require.NotNil(t, semanticResponse.Items)
	require.Len(t, *semanticResponse.Items, 2, "semantic retrieval must find in-prefix current chunks without a lexical match")
	for _, item := range *semanticResponse.Items {
		require.True(t, strings.HasPrefix(item.Path, "internal/"))
		require.Contains(t, item.MatchSources, uci.QueryMatchVector)
	}
	semanticSpec := incompleteSpec
	semanticSpec.Text = "NoLexicalMatchToken"
	operatorSemantic, err := composition.application.SearchOperatorCodebase(context.Background(), authorized, semanticSpec)
	require.NoError(t, err)
	require.NoError(t, operatorSemantic.ValidatePreExposure())
	require.NotNil(t, operatorSemantic.Retrieval)
	require.Equal(t, uci.QueryRetrievalHybrid, operatorSemantic.Retrieval.Mode)
	require.NotNil(t, operatorSemantic.Retrieval.VectorCoverage)
	require.Equal(t, float64(1), *operatorSemantic.Retrieval.VectorCoverage)
	require.Empty(t, operatorSemantic.Retrieval.DegradationReasons)
	require.NotNil(t, operatorSemantic.Items)
	require.Len(t, *operatorSemantic.Items, 1)
	require.Contains(t, (*operatorSemantic.Items)[0].MatchSources, uci.QueryMatchVector)
	require.NotNil(t, operatorSemantic.Contexts)
	require.Equal(t, fixture.current.Context.ViewID, (*operatorSemantic.Contexts)[0].ViewID)
	require.NotNil(t, operatorSemantic.Continuation.Value)

	continuationSpec := semanticSpec
	continuationSpec.Continuation = operatorSemantic.Continuation.Value
	continued, err := composition.application.SearchOperatorCodebase(context.Background(), authorized, continuationSpec)
	require.NoError(t, err)
	require.NoError(t, continued.ValidatePreExposure())
	require.NotNil(t, continued.Retrieval)
	require.Equal(t, uci.QueryRetrievalHybrid, continued.Retrieval.Mode)
	require.NotNil(t, continued.Items)
	require.Len(t, *continued.Items, 1)
	require.NotEqual(t, (*operatorSemantic.Items)[0].Ref.EntityKey, (*continued.Items)[0].Ref.EntityKey)
	require.NotNil(t, continued.Contexts)
	require.Equal(t, fixture.current.Context.ViewID, (*continued.Contexts)[0].ViewID)

	for _, invalidSpec := range []uci.QuerySpec{
		{Mode: uci.QueryModeStructure, Order: uci.QueryOrderRelevance},
		{Mode: uci.QueryModeFTS, Order: uci.QueryOrderPath},
	} {
		_, invalidErr := composition.application.SearchOperatorCodebase(context.Background(), authorized, invalidSpec)
		require.ErrorContains(t, invalidErr, "browser query must be lexical relevance")
	}

	configuredStatus := composition.application.indexStatusService
	composition.application.indexStatusService = uci.NewIndexStatusService(nil, composition.embeddingProfile)
	_, statusErr := composition.application.SearchOperatorCodebase(context.Background(), authorized, semanticSpec)
	require.ErrorContains(t, statusErr, "uci index status: store is not configured")
	composition.application.indexStatusService = configuredStatus

	configuredSemantic := composition.application.semanticService
	configuredQuery := composition.application.queryService
	composition.application.semanticService = uci.NewSemanticService(uci.VectorProfile{}, nil, nil, composition.projectionStore, composition.projectionStore)
	composition.application.queryService = uci.NewQueryService(nil)
	_, semanticErr := composition.application.SearchOperatorCodebase(context.Background(), authorized, semanticSpec)
	require.ErrorContains(t, semanticErr, "uci semantic: provider ref is invalid")
	composition.application.semanticService = configuredSemantic
	composition.application.queryService = configuredQuery

	require.Equal(t, int32(3), corpusProviderInputs.Load(), "the runtime producer must embed each current corpus input exactly once")
	require.Equal(t, int32(3), queryProviderInputs.Load(), "only post-coverage semantic operator and MCP queries may call the provider")
	require.Equal(t, int32(4), providerCalls.Load(), "the producer batches corpus inputs once and each semantic query calls the provider")
}

func TestUCIApplicationOperatorSearchSelectsSemanticOnlyForCompleteCoverage(t *testing.T) {
	_, fixture := newOperatorCodeHTTPTestAdapter(t)
	authorized, err := fixture.authority.AuthorizeOperatorCode(context.Background(), operatorCodeVerifiedCaller{
		SessionID: "operator-semantic-selection",
		Context:   fixture.ref,
	})
	require.NoError(t, err)

	profile := uci.VectorProfile{
		ProviderRef:           "worker-uci-operator-semantic",
		Model:                 "worker-uci-operator-semantic-model",
		Dimension:             embedding.EmbeddingDim,
		PreprocessingRevision: "worker-uci-operator-semantic/v1",
		IncludeRelativePath:   true,
	}
	lexical := &workerUCIApplicationQueryStore{}
	semanticFallback := &workerUCIApplicationQueryStore{}
	semanticStore := &workerUCIApplicationSemanticStore{candidates: []uci.SemanticCandidate{
		workerUCIApplicationSemanticCandidate(fixture.ref, "71000000-0000-4000-8000-000000000001", "semantic-one", "internal/semantic_one.go"),
		workerUCIApplicationSemanticCandidate(fixture.ref, "71000000-0000-4000-8000-000000000002", "semantic-two", "internal/semantic_two.go"),
	}}
	statusStore := &workerUCIApplicationStatusStore{snapshot: workerUCIApplicationStatusSnapshot(fixture.ref, uci.IndexCoveragePartial)}
	application := &UCIApplication{
		queryService:       uci.NewQueryService(lexical),
		semanticService:    uci.NewSemanticService(profile, &workerUCIApplicationEmbedder{model: profile.Model}, semanticStore, semanticFallback, semanticStore),
		indexStatusService: uci.NewIndexStatusService(statusStore, &profile),
	}
	spec := uci.QuerySpec{
		ClientSessionID: "operator-code/semantic-selection",
		Mode:            uci.QueryModeFTS,
		Text:            "non lexical concept",
		Filter:          uci.QueryFilter{PathPrefix: "internal/"},
		Order:           uci.QueryOrderRelevance,
		Limit:           1,
	}
	normalizedSpec := spec
	normalizedSpec.Filter.PathPrefix = "internal"

	incomplete, err := application.SearchOperatorCodebase(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.NoError(t, incomplete.ValidatePreExposure())
	require.Equal(t, uci.QueryRetrievalLexical, incomplete.Retrieval.Mode)
	require.Equal(t, []uci.QuerySpec{normalizedSpec}, lexical.calls)
	require.Empty(t, semanticStore.calls)

	statusStore.snapshot = workerUCIApplicationStatusSnapshot(fixture.ref, uci.IndexCoverageComplete)
	semantic, err := application.SearchOperatorCodebase(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.NoError(t, semantic.ValidatePreExposure())
	require.Equal(t, uci.QueryRetrievalHybrid, semantic.Retrieval.Mode)
	require.NotNil(t, semantic.Retrieval.VectorCoverage)
	require.Equal(t, float64(1), *semantic.Retrieval.VectorCoverage)
	require.Empty(t, semantic.Retrieval.DegradationReasons)
	require.Len(t, *semantic.Items, 1)
	require.Equal(t, fixture.ref.ViewID, (*semantic.Contexts)[0].ViewID)
	require.Len(t, lexical.calls, 1, "complete coverage must not call the FTS service")
	require.Empty(t, semanticFallback.calls, "complete semantic retrieval must not degrade to FTS")
	require.Len(t, semanticStore.calls, 1)
	require.Equal(t, normalizedSpec, semanticStore.calls[0])
	require.NotNil(t, semantic.Continuation.Value)

	continuationSpec := spec
	continuationSpec.Continuation = semantic.Continuation.Value
	statusStore.snapshot = workerUCIApplicationStatusSnapshot(fixture.ref, uci.IndexCoveragePartial)
	continued, err := application.SearchOperatorCodebase(context.Background(), authorized, continuationSpec)
	require.NoError(t, err)
	require.NoError(t, continued.ValidatePreExposure())
	require.Equal(t, uci.QueryRetrievalHybrid, continued.Retrieval.Mode)
	require.Len(t, *continued.Items, 1)
	require.NotEqual(t, (*semantic.Items)[0].Ref.EntityKey, (*continued.Items)[0].Ref.EntityKey)
	require.Len(t, semanticStore.calls, 2)
	require.Equal(t, continuationSpec.ClientSessionID, semanticStore.calls[1].ClientSessionID)
	require.Equal(t, continuationSpec.Mode, semanticStore.calls[1].Mode)
	require.Equal(t, continuationSpec.Text, semanticStore.calls[1].Text)
	require.Equal(t, normalizedSpec.Filter, semanticStore.calls[1].Filter)
	require.Equal(t, continuationSpec.Order, semanticStore.calls[1].Order)
	require.Equal(t, continuationSpec.Limit, semanticStore.calls[1].Limit)
	require.Equal(t, continuationSpec.Continuation, semanticStore.calls[1].Continuation)
	require.Equal(t, 1, semanticStore.calls[1].Offset)
	require.Len(t, lexical.calls, 1, "semantic continuation must bypass current incomplete coverage")

	for _, invalid := range []uci.QuerySpec{
		{Mode: uci.QueryModeStructure, Order: uci.QueryOrderRelevance},
		{Mode: uci.QueryModeFTS, Order: uci.QueryOrderPath},
	} {
		_, invalidErr := application.SearchOperatorCodebase(context.Background(), authorized, invalid)
		require.ErrorContains(t, invalidErr, "browser query must be lexical relevance")
	}

	statusFailure := errors.New("status failed")
	statusStore.err = statusFailure
	_, err = application.SearchOperatorCodebase(context.Background(), authorized, spec)
	require.ErrorIs(t, err, statusFailure)
	require.Len(t, lexical.calls, 1, "status failure must not fall back to FTS")
	require.Len(t, semanticStore.calls, 2, "status failure must not invoke semantic retrieval")
	statusStore.err = nil

	semanticFailure := errors.New("semantic failed")
	semanticStore.err = semanticFailure
	application.queryService = uci.NewQueryService(nil)
	_, err = application.SearchOperatorCodebase(context.Background(), authorized, continuationSpec)
	require.ErrorIs(t, err, semanticFailure)
	require.Len(t, lexical.calls, 1, "semantic failure must not fall back to FTS")
}

func TestUCIApplicationOperatorSearchPreservesLexicalContinuation(t *testing.T) {
	_, fixture := newOperatorCodeHTTPTestAdapter(t)
	authorized, err := fixture.authority.AuthorizeOperatorCode(context.Background(), operatorCodeVerifiedCaller{
		SessionID: "operator-lexical-continuation",
		Context:   fixture.ref,
	})
	require.NoError(t, err)

	profile := uci.VectorProfile{
		ProviderRef:           "worker-uci-lexical-continuation",
		Model:                 "worker-uci-lexical-continuation-model",
		Dimension:             embedding.EmbeddingDim,
		PreprocessingRevision: "worker-uci-lexical-continuation/v1",
		IncludeRelativePath:   true,
	}
	lexical := &workerUCIApplicationQueryStore{candidates: []uci.QueryCandidate{
		workerUCIApplicationSemanticCandidate(fixture.ref, "73000000-0000-4000-8000-000000000001", "lexicalOne", "internal/lexical_one.go").Candidate,
		workerUCIApplicationSemanticCandidate(fixture.ref, "73000000-0000-4000-8000-000000000002", "lexicalTwo", "internal/lexical_two.go").Candidate,
	}}
	semanticFallback := &workerUCIApplicationQueryStore{}
	semanticStore := &workerUCIApplicationSemanticStore{}
	statusStore := &workerUCIApplicationStatusStore{snapshot: workerUCIApplicationStatusSnapshot(fixture.ref, uci.IndexCoveragePartial)}
	application := &UCIApplication{
		queryService:       uci.NewQueryService(lexical),
		semanticService:    uci.NewSemanticService(profile, &workerUCIApplicationEmbedder{model: profile.Model}, semanticStore, semanticFallback, semanticStore),
		indexStatusService: uci.NewIndexStatusService(statusStore, &profile),
	}
	spec := uci.QuerySpec{
		ClientSessionID: "operator-code/lexical-continuation",
		Mode:            uci.QueryModeFTS,
		Text:            "lexical continuation",
		Filter:          uci.QueryFilter{PathPrefix: "internal/"},
		Order:           uci.QueryOrderRelevance,
		Limit:           1,
	}

	first, err := application.SearchOperatorCodebase(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.Equal(t, uci.QueryRetrievalLexical, first.Retrieval.Mode)
	require.NotNil(t, first.Continuation.Value)
	require.False(t, uci.IsSemanticContinuationToken(*first.Continuation.Value))

	continuationSpec := spec
	continuationSpec.Continuation = first.Continuation.Value
	statusStore.snapshot = workerUCIApplicationStatusSnapshot(fixture.ref, uci.IndexCoverageComplete)
	continued, err := application.SearchOperatorCodebase(context.Background(), authorized, continuationSpec)
	require.NoError(t, err)
	require.Equal(t, uci.QueryRetrievalLexical, continued.Retrieval.Mode)
	require.Len(t, *continued.Items, 1)
	require.NotEqual(t, (*first.Items)[0].Ref.EntityKey, (*continued.Items)[0].Ref.EntityKey)
	require.Len(t, lexical.calls, 2)
	require.Empty(t, semanticStore.calls)
	require.Empty(t, semanticFallback.calls)
}

func newWorkerUCIApplicationEmbeddingProvider(t *testing.T, input workerUCIApplicationEmbeddingProviderInput) *httptest.Server {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		inputs, ok := workerUCIApplicationEmbeddingInputs(t, writer, request, input.model)
		if !ok {
			return
		}
		input.providerCalls.Add(1)
		workerUCIApplicationTrackEmbeddingInputs(inputs, input)
		workerUCIApplicationWriteEmbeddingResponse(t, writer, len(inputs))
	}))
	t.Cleanup(provider.Close)
	return provider
}

func workerUCIApplicationEmbeddingInputs(t *testing.T, writer http.ResponseWriter, request *http.Request, model string) ([]string, bool) {
	t.Helper()
	if request.Method != http.MethodPost || request.URL.Path != "/v1/embeddings" {
		t.Errorf("embedding request = %s %s, want POST /v1/embeddings", request.Method, request.URL.Path)
		http.Error(writer, "unexpected embedding request", http.StatusBadRequest)
		return nil, false
	}
	var payload struct {
		Model      string   `json:"model"`
		Dimensions int      `json:"dimensions"`
		Input      []string `json:"input"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Errorf("decode embedding request: %v", err)
		http.Error(writer, "invalid embedding request", http.StatusBadRequest)
		return nil, false
	}
	if payload.Model != model || payload.Dimensions != embedding.EmbeddingDim || len(payload.Input) == 0 {
		t.Errorf("embedding request payload = %#v", payload)
		http.Error(writer, "unexpected embedding payload", http.StatusBadRequest)
		return nil, false
	}
	return payload.Input, true
}

func workerUCIApplicationTrackEmbeddingInputs(inputs []string, provider workerUCIApplicationEmbeddingProviderInput) {
	for _, embeddingInput := range inputs {
		if strings.Contains(embeddingInput, `"content_digest"`) {
			provider.corpusProviderInputs.Add(1)
		} else {
			provider.queryProviderInputs.Add(1)
		}
	}
}

func workerUCIApplicationWriteEmbeddingResponse(t *testing.T, writer http.ResponseWriter, length int) {
	t.Helper()
	data := make([]map[string]any, length)
	for index := range data {
		vector := make([]float32, embedding.EmbeddingDim)
		vector[0] = 1
		data[index] = map[string]any{
			"embedding": vector,
			"index":     index,
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{"data": data}); err != nil {
		t.Errorf("encode embedding response: %v", err)
	}
}

func TestUCIApplicationGraphResponseKeepsNonconclusiveOutcomesExplicit(t *testing.T) {
	ref := uci.ContextRef{
		SourceID:          "10000000-0000-4000-8000-000000000001",
		CheckoutID:        "20000000-0000-4000-8000-000000000002",
		ViewID:            "30000000-0000-4000-8000-000000000003",
		AnalysisProfileID: "40000000-0000-4000-8000-000000000004",
		Generation:        7,
	}
	candidateA := uci.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: "fixture.A"}
	candidateB := uci.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: "fixture.B"}
	candidateC := uci.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: "fixture.C"}

	for _, testCase := range []struct {
		name          string
		outcome       uci.GraphOutcome
		coverage      uci.IndexCoverageState
		stop          uci.QueryGraphStopReason
		candidates    []uci.QueryEntityRef
		maxNodes      int
		wantStatus    uci.QueryResponseStatus
		wantWarning   string
		wantNodes     int
		wantGraph     bool
		wantError     uci.QueryErrorCode
		wantTruncated bool
	}{
		{
			name:          "ambiguous candidates remain evidence",
			outcome:       uci.GraphOutcomeAmbiguous,
			coverage:      uci.IndexCoverageComplete,
			stop:          uci.QueryGraphComplete,
			candidates:    []uci.QueryEntityRef{candidateA, candidateB},
			maxNodes:      2,
			wantStatus:    uci.QueryStatusPartial,
			wantWarning:   "graph_outcome:ambiguous",
			wantNodes:     2,
			wantGraph:     true,
			wantTruncated: false,
		},
		{
			name:          "ambiguous candidates respect declared node budget",
			outcome:       uci.GraphOutcomeAmbiguous,
			coverage:      uci.IndexCoverageComplete,
			stop:          uci.QueryGraphComplete,
			candidates:    []uci.QueryEntityRef{candidateA, candidateB, candidateC},
			maxNodes:      2,
			wantStatus:    uci.QueryStatusPartial,
			wantWarning:   "graph_candidates_truncated",
			wantNodes:     2,
			wantGraph:     true,
			wantTruncated: false,
		},
		{
			name:          "capped",
			outcome:       uci.GraphOutcomeUnknownOrTruncated,
			coverage:      uci.IndexCoveragePartial,
			stop:          uci.QueryGraphNodeCap,
			maxNodes:      2,
			wantStatus:    uci.QueryStatusPartial,
			wantWarning:   "graph_outcome:unknown_or_truncated",
			wantGraph:     true,
			wantTruncated: false,
		},
		{
			name:          "deadline",
			outcome:       uci.GraphOutcomeUnknownOrTruncated,
			coverage:      uci.IndexCoveragePartial,
			stop:          uci.QueryGraphDeadline,
			maxNodes:      2,
			wantStatus:    uci.QueryStatusPartial,
			wantWarning:   "graph_outcome:unknown_or_truncated",
			wantGraph:     true,
			wantTruncated: false,
		},
		{
			name:          "no path",
			outcome:       uci.GraphOutcomeNoPath,
			coverage:      uci.IndexCoverageComplete,
			stop:          uci.QueryGraphComplete,
			maxNodes:      2,
			wantStatus:    uci.QueryStatusEmpty,
			wantWarning:   "graph_outcome:no_path",
			wantGraph:     true,
			wantTruncated: false,
		},
		{
			name:          "no impact",
			outcome:       uci.GraphOutcomeNoImpact,
			coverage:      uci.IndexCoverageComplete,
			stop:          uci.QueryGraphComplete,
			maxNodes:      2,
			wantStatus:    uci.QueryStatusEmpty,
			wantWarning:   "graph_outcome:no_impact",
			wantGraph:     true,
			wantTruncated: false,
		},
		{
			name:          "unavailable coverage remains contextual unavailable",
			outcome:       uci.GraphOutcomeUnknownOrTruncated,
			coverage:      uci.IndexCoverageUnavailable,
			stop:          uci.QueryGraphCoverageGap,
			maxNodes:      2,
			wantStatus:    uci.QueryStatusUnavailable,
			wantWarning:   "graph_unavailable",
			wantGraph:     false,
			wantError:     uci.QueryErrorBuildIncomplete,
			wantTruncated: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response, err := uciApplicationGraphResponse(ref, uci.GraphResult{
				Outcome:    testCase.outcome,
				Semantics:  uci.GraphSemanticsStatic,
				Coverage:   testCase.coverage,
				Candidates: testCase.candidates,
				Graph: uci.QueryGraph{
					Nodes:      []uci.QueryEntityRef{},
					Edges:      []uci.QueryGraphEdge{},
					StopReason: testCase.stop,
				},
			}, uci.GraphBudget{MaxNodes: testCase.maxNodes, MaxEdges: 2})
			require.NoError(t, err)
			require.NoError(t, response.ValidatePreExposure())
			require.Equal(t, testCase.wantStatus, response.Status)
			require.Contains(t, *response.Warnings, testCase.wantWarning)
			require.NotNil(t, response.Truncated)
			require.Equal(t, testCase.wantTruncated, *response.Truncated)
			if testCase.wantGraph {
				require.NotNil(t, response.Graph)
				require.Len(t, response.Graph.Nodes, testCase.wantNodes)
			} else {
				require.Nil(t, response.Graph)
				require.NotNil(t, response.Error)
				require.Equal(t, testCase.wantError, response.Error.Code)
			}
		})
	}
}

func TestUCIApplicationOperatorPortsKeepStructureAndRelationsInOneView(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	store := openWorkerUCIContextCompositionStore(t)
	server := mcp.NewServer(mcp.ServerOptions{Version: "uci-application-operator-ports"})
	composition, err := composeUCIContext(true, store.GetDB(), server, workerUCISemanticConfig())
	require.NoError(t, err)
	fixture := newWorkerUCIApplicationFixture(t, composition)
	adapter, err := composeOperatorCodeHTTPAdapter(store.GetDB(), composition)
	require.NoError(t, err)
	ref := fixture.historical.Context
	authorized, err := composition.resolver.Authorize(context.Background(), uci.ResolveContextInput{
		ClientSessionID: fixture.clientSessionID,
		AuthRealm:       string(auth.SourceClient),
		Principal:       fixture.principal,
		Ref:             &ref,
	})
	require.NoError(t, err)

	structureSpec := uci.QuerySpec{
		ClientSessionID: fixture.clientSessionID,
		Mode:            uci.QueryModeStructure,
		Filter:          uci.QueryFilter{PathPrefix: "internal"},
		Order:           uci.QueryOrderPath,
		Limit:           1,
	}
	structure, err := composition.application.StructureOperatorCodebase(context.Background(), authorized, structureSpec)
	require.NoError(t, err)
	require.NoError(t, structure.ValidatePreExposure())
	require.True(t, operatorCodeStructureResponseValid(structure, authorized))
	exposure, err := composition.exposureRecorder.Record(context.Background(), authorized, uci.ExposureInput{
		AuthRealm:            string(auth.SourceClient),
		ClientKeycard:        fixture.workstationID,
		ClientSession:        fixture.clientSessionID,
		RequestID:            "worker-uci-structure",
		RequestBindingDigest: workerUCIApplicationDigest("worker-uci-structure"),
		Operation:            uci.ExposureOperationCodeSearch,
		Response:             structure,
		RecordedAt:           time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NotEmpty(t, exposure.ExposureRef)
	require.NotNil(t, structure.Contexts)
	require.Equal(t, fixture.historical.Context.ViewID, (*structure.Contexts)[0].ViewID)
	require.NotNil(t, structure.Retrieval)
	require.Equal(t, uci.QueryRetrievalStructure, structure.Retrieval.Mode)
	require.NotNil(t, structure.Truncated)
	require.True(t, *structure.Truncated)
	require.NotNil(t, structure.Continuation)
	require.NotNil(t, structure.Continuation.Value)

	for _, testCase := range []struct {
		name      string
		target    string
		direction uci.GraphDirection
	}{
		{name: "direct", target: "fixture.Alpha", direction: uci.GraphDirectionOutgoing},
		{name: "reverse", target: "fixture.Beta", direction: uci.GraphDirectionIncoming},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := uci.GraphSpec{
				ClientSessionID: fixture.clientSessionID,
				Action:          uci.GraphActionNeighbors,
				Target:          uci.GraphTarget{EntityKey: testCase.target},
				Filter:          uci.GraphFilter{Direction: testCase.direction, Relations: []uci.IndexRelation{"calls"}, EvidenceKinds: []uci.QueryEvidenceKind{uci.QueryEvidenceResolved}},
				Budget:          uci.GraphBudget{MaxDepth: 1, MaxVisited: 10, MaxNodes: 10, MaxEdges: 10},
			}
			response, exploreErr := composition.application.ExploreOperatorCodebase(context.Background(), authorized, input)
			require.NoError(t, exploreErr)
			require.True(t, operatorCodeGraphResponseValid(response, authorized, input))
			require.NoError(t, response.ValidatePreExposure())
			require.NotNil(t, response.Graph)
			require.Len(t, response.Graph.Edges, 1)
			edge := response.Graph.Edges[0]
			require.Equal(t, fixture.historical.Context.ViewID, edge.From.ViewID)
			require.Equal(t, fixture.historical.Context.ViewID, edge.To.ViewID)
			require.Len(t, edge.Evidence, 1)
			require.Equal(t, edge.From, edge.Evidence[0].Ref)
			require.Equal(t, uci.QueryEvidencePrecisionReferenceSite, edge.Evidence[0].Precision)
			navigation := adapter.graphNavigation(context.Background(), authorized, response.Graph)
			require.Len(t, navigation.Nodes, 2)
			for _, node := range navigation.Nodes {
				require.Equal(t, "available", node.SourceState)
				require.NotNil(t, node.SourceRead)
				require.Equal(t, node.Entity.EntityKey, node.SourceRead.EntityKey)
			}
			require.Len(t, navigation.Edges, 1)
			require.Len(t, navigation.Edges[0].Evidence, 1)
			reference := navigation.Edges[0].Evidence[0]
			require.Equal(t, "available", reference.SourceState)
			require.NotNil(t, reference.SourceRead)
			require.Equal(t, edge.From.EntityKey, reference.SourceRead.EntityKey)
			descriptor, available, err := composition.projectionStore.DescribeGraphEvidence(context.Background(), authorized, edge.Evidence[0])
			require.NoError(t, err)
			require.True(t, available)
			require.Equal(t, descriptor.Span, reference.SourceRead.Span)
			require.Equal(t, descriptor.ContentDigest, reference.SourceRead.ContentDigest)
			read, err := composition.projectionStore.ReadExact(context.Background(), authorized, descriptor)
			require.NoError(t, err)
			require.NotNil(t, read.Hit)
			require.Equal(t, "Beta", read.Hit.Text)
			responseRead, err := composition.application.ReadCodebase(context.Background(), authorized, mcp.CodebaseReadInput{
				Ref: descriptor.Entity, Span: descriptor.Span, ContentDigest: descriptor.ContentDigest,
				ReferenceSiteID: descriptor.ReferenceSiteID, MaxBytes: int(descriptor.Span.ByteEnd - descriptor.Span.ByteStart),
			})
			require.NoError(t, err)
			require.Equal(t, uci.QueryStatusOK, responseRead.Status)
			require.NotNil(t, responseRead.Items)
			require.Len(t, *responseRead.Items, 1)
			require.Equal(t, "Beta", (*responseRead.Items)[0].Excerpt)
		})
	}
}

type workerUCIApplicationFixture struct {
	source          *gormstore.UCISource
	checkout        *gormstore.UCICheckout
	profile         *gormstore.UCIAnalysisProfile
	historical      uci.IndexPublishedView
	current         uci.IndexPublishedView
	principal       string
	workstationID   string
	clientSessionID string
	legacyProject   string
}

type workerUCIApplicationArtifact struct {
	artifact   *gormstore.UCIParseArtifact
	definition *gormstore.UCIDefinition
	reference  *gormstore.UCIReferenceSite
	proof      uci.IndexArtifactProof
}

type workerUCIApplicationEmbeddingStatusResponse struct {
	EmbeddingProfileID *string                   `json:"embedding_profile_id"`
	Coverage           uci.IndexCoverageState    `json:"coverage"`
	TotalCandidates    uint64                    `json:"total_candidates"`
	ReadyCandidates    uint64                    `json:"ready_candidates"`
	PendingJobs        uint64                    `json:"pending_jobs"`
	JobState           *uci.IndexStatusJobState  `json:"job_state"`
	ErrorCode          *uci.EmbeddingFailureCode `json:"error_code"`
	RetryAfter         *time.Time                `json:"retry_after"`
}

type workerUCIApplicationStatusResponse struct {
	Context          uci.QueryContextRef                         `json:"context"`
	TotalChunks      int64                                       `json:"total_chunks"`
	EmbeddedChunks   int64                                       `json:"embedded_chunks"`
	Embedding        workerUCIApplicationEmbeddingStatusResponse `json:"embedding"`
	EvidenceRecorder mcp.CodebaseEvidenceRecorderHealth          `json:"evidence_recorder"`
	Freshness        uci.QueryFreshness                          `json:"freshness"`
}

type workerUCIApplicationArtifactInput struct {
	sourceID      string
	profileDigest string
	label         string
	name          string
	symbol        string
	source        string
	rawTarget     string
}

type workerUCIApplicationPublicationInput struct {
	checkout     *gormstore.UCICheckout
	profileID    string
	key          string
	jobKind      uci.IndexJobKind
	parent       *uci.ContextRef
	part         uci.IndexPart
	memberships  []uci.IndexMembership
	replacements []uci.IndexEdgeReplacement
	sequence     int64
}

type workerUCIApplicationEmbeddingProviderInput struct {
	model                string
	providerCalls        *atomic.Int32
	corpusProviderInputs *atomic.Int32
	queryProviderInputs  *atomic.Int32
}

func newWorkerUCIApplicationFixture(t *testing.T, composition *uciContextComposition) workerUCIApplicationFixture {
	t.Helper()

	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	principal := "agent/worker-uci-application-" + token
	workstationID := uuid.NewString()
	clientSessionID := "worker-uci-application-session-" + token
	ctx := context.Background()

	source, err := composition.contextStore.CreateSource(ctx, gormstore.CreateSourceInput{
		AuthRealm:   string(auth.SourceClient),
		Kind:        gormstore.UCISourceGit,
		DisplayName: "worker-uci-application-source-" + token,
	})
	require.NoError(t, err)
	checkout, err := composition.contextStore.RegisterCheckout(ctx, gormstore.RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  workstationID,
		Kind:           gormstore.UCICheckoutWorkingTree,
		OwnerPrincipal: principal,
		LocatorRef:     "file:///worker-uci-application/" + token,
	})
	require.NoError(t, err)
	profile, err := composition.contextStore.CreateProfile(ctx, gormstore.CreateProfileInput{
		ParserBundleDigest:   workerUCIApplicationDigest("parser-" + token),
		ResolverRevision:     "worker-uci-application-resolver-" + token,
		ChunkerRevision:      "worker-uci-application-chunker-" + token,
		IgnorePolicyDigest:   workerUCIApplicationDigest("ignore-" + token),
		BuildContextJSON:     `{"fixture":"worker-uci-application"}`,
		SecretPolicyRevision: "worker-uci-application-secret-policy-" + token,
	})
	require.NoError(t, err)

	alphaOld := workerUCIApplicationAddArtifact(t, composition.projectionStore, workerUCIApplicationArtifactInput{
		sourceID:      source.SourceID,
		profileDigest: profile.ParserBundleDigest,
		label:         "alpha-old-" + token,
		name:          "Alpha",
		symbol:        "alpha",
		source:        "func Alpha() { Beta(); _ = \"historical-published-body SearchNeedle\" }\n",
		rawTarget:     "Beta",
	})
	beta := workerUCIApplicationAddArtifact(t, composition.projectionStore, workerUCIApplicationArtifactInput{
		sourceID:      source.SourceID,
		profileDigest: profile.ParserBundleDigest,
		label:         "beta-" + token,
		name:          "Beta",
		symbol:        "beta",
		source:        "func Beta() { _ = \"beta SearchNeedle\" }\n",
	})
	outside := workerUCIApplicationAddArtifact(t, composition.projectionStore, workerUCIApplicationArtifactInput{
		sourceID:      source.SourceID,
		profileDigest: profile.ParserBundleDigest,
		label:         "outside-" + token,
		name:          "Outside",
		symbol:        "outside",
		source:        "func Outside() { _ = \"outside SearchNeedle\" }\n",
	})

	firstMemberships := []uci.IndexMembership{
		workerUCIApplicationMembership("internal/alpha.go", alphaOld),
		workerUCIApplicationMembership("internal/beta.go", beta),
		workerUCIApplicationMembership("outside.go", outside),
	}
	firstReplacements := []uci.IndexEdgeReplacement{
		workerUCIApplicationReplacement(t, "internal/alpha.go", alphaOld, "internal/beta.go", beta),
		{SourcePath: "internal/beta.go", Edges: []uci.IndexEdge{}},
		{SourcePath: "outside.go", Edges: []uci.IndexEdge{}},
	}
	firstPart := workerUCIApplicationPart([]workerUCIApplicationArtifact{alphaOld, beta, outside}, firstMemberships, firstReplacements)
	publisher, err := composition.projectionStore.Publisher(composition.authorizer, uci.IndexPublicationConfig{
		Limits:           uci.DefaultIndexPublicationLimits(),
		EmbeddingProfile: composition.embeddingProfile,
	})
	require.NoError(t, err)
	caller := uci.IndexCaller{
		AuthRealm:     string(auth.SourceClient),
		Principal:     principal,
		OwnerInstance: "worker-uci-application-owner-" + token,
	}
	historical := workerUCIApplicationPublish(t, publisher, caller, workerUCIApplicationPublicationInput{
		checkout:     checkout,
		profileID:    profile.ProfileID,
		key:          "worker-uci-historical-" + token,
		jobKind:      uci.IndexJobInitial,
		part:         firstPart,
		memberships:  firstMemberships,
		replacements: firstReplacements,
		sequence:     11,
	})

	alphaCurrent := workerUCIApplicationAddArtifact(t, composition.projectionStore, workerUCIApplicationArtifactInput{
		sourceID:      source.SourceID,
		profileDigest: profile.ParserBundleDigest,
		label:         "alpha-current-" + token,
		name:          "Alpha",
		symbol:        "alpha",
		source:        "func Alpha() { Beta(); _ = \"current-published-body SearchNeedle\" }\n",
		rawTarget:     "Beta",
	})
	currentMemberships := []uci.IndexMembership{
		workerUCIApplicationMembership("internal/alpha.go", alphaCurrent),
		workerUCIApplicationMembership("internal/beta.go", beta),
		workerUCIApplicationMembership("outside.go", outside),
	}
	currentReplacements := []uci.IndexEdgeReplacement{
		workerUCIApplicationReplacement(t, "internal/alpha.go", alphaCurrent, "internal/beta.go", beta),
		{SourcePath: "internal/beta.go", Edges: []uci.IndexEdge{}},
		{SourcePath: "outside.go", Edges: []uci.IndexEdge{}},
	}
	currentPart := workerUCIApplicationPart([]workerUCIApplicationArtifact{alphaCurrent, beta, outside}, currentMemberships, currentReplacements)
	parent := historical.Context
	current := workerUCIApplicationPublish(t, publisher, caller, workerUCIApplicationPublicationInput{
		checkout:     checkout,
		profileID:    profile.ProfileID,
		key:          "worker-uci-current-" + token,
		jobKind:      uci.IndexJobReconcile,
		parent:       &parent,
		part:         currentPart,
		memberships:  currentMemberships,
		replacements: currentReplacements,
		sequence:     12,
	})

	legacyProject := "legacy-worker-uci-" + token
	sourceID := source.SourceID
	_, err = composition.contextStore.UpsertLegacyContextAlias(ctx, gormstore.LegacyContextAliasInput{
		AuthRealm:       string(auth.SourceClient),
		LegacyDomain:    uciApplicationLegacyProjectDomain,
		Scheme:          uciApplicationLegacyProjectScheme,
		Value:           legacyProject,
		ClientNamespace: clientSessionID,
		SourceID:        &sourceID,
		MappingState:    gormstore.UCIAliasResolved,
		Revision:        1,
		Provenance:      `{"fixture":"worker-uci-application"}`,
	})
	require.NoError(t, err)

	return workerUCIApplicationFixture{
		source:          source,
		checkout:        checkout,
		profile:         profile,
		historical:      historical,
		current:         current,
		principal:       principal,
		workstationID:   workstationID,
		clientSessionID: clientSessionID,
		legacyProject:   legacyProject,
	}
}

func workerUCIApplicationAddArtifact(t *testing.T, projection *gormstore.UCIProjectionStore, input workerUCIApplicationArtifactInput) workerUCIApplicationArtifact {
	t.Helper()

	body := []byte("package fixture\n" + input.source)
	digest := workerUCIApplicationDigestBytes(body)
	blob, err := projection.UpsertBlob(context.Background(), gormstore.UpsertUCIBlobInput{
		SourceID:         input.sourceID,
		ProtectionDomain: "source-private",
		ContentDigest:    digest,
		ByteLength:       int64(len(body)),
		SafeContent:      body,
		Encoding:         "utf-8",
		StorageState:     gormstore.UCIBlobStored,
	})
	require.NoError(t, err)
	artifact, err := projection.UpsertParseArtifact(context.Background(), gormstore.UpsertUCIParseArtifactInput{
		SourceID:                input.sourceID,
		BlobID:                  blob.BlobID,
		Language:                "go",
		ParserRevision:          "worker-uci-application-parser-" + input.label,
		GrammarDigest:           workerUCIApplicationDigest("grammar-" + input.label),
		ExtractionProfileDigest: input.profileDigest,
		Status:                  gormstore.UCIParseArtifactComplete,
		Diagnostics:             `{}`,
	})
	require.NoError(t, err)

	definitionStart := int64(strings.Index(string(body), "func "+input.name))
	require.GreaterOrEqual(t, definitionStart, int64(0))
	definition, err := projection.UpsertDefinition(context.Background(), gormstore.UpsertUCIDefinitionInput{
		ArtifactID:         artifact.ArtifactID,
		LocalSymbolKey:     input.symbol,
		Kind:               "function",
		Name:               input.name,
		QualifiedLocalName: "fixture." + input.name,
		Signature:          "func " + input.name + "()",
		ByteStart:          definitionStart,
		ByteEnd:            int64(len(body) - 1),
		LineStart:          2,
		LineEnd:            2,
	})
	require.NoError(t, err)
	chunk, err := projection.UpsertChunk(context.Background(), gormstore.UpsertUCIChunkInput{
		SourceID:      input.sourceID,
		ArtifactID:    artifact.ArtifactID,
		SymbolKey:     &definition.LocalSymbolKey,
		ChunkKind:     "definition",
		Ordinal:       0,
		ByteStart:     0,
		ByteEnd:       int64(len(body)),
		ContentDigest: digest,
		TextForSearch: string(body),
	})
	require.NoError(t, err)

	var reference *gormstore.UCIReferenceSite
	if input.rawTarget != "" {
		referenceStart := strings.Index(string(body), input.rawTarget+"(")
		require.GreaterOrEqual(t, referenceStart, 0)
		referenceSpan := fmt.Sprintf(`{"byte_start":%d,"byte_end":%d,"line_start":2,"line_end":2}`, referenceStart, referenceStart+len(input.rawTarget))
		reference, err = projection.UpsertReferenceSite(context.Background(), gormstore.UpsertUCIReferenceSiteInput{
			ArtifactID:     artifact.ArtifactID,
			SiteKey:        "reference-" + input.label,
			OwnerSymbolKey: &definition.LocalSymbolKey,
			RawTarget:      input.rawTarget,
			Relation:       "calls",
			SyntaxSpan:     referenceSpan,
			ResolverHints:  `{}`,
		})
		require.NoError(t, err)
	}

	proof, err := projection.DescribeIndexArtifact(context.Background(), input.sourceID, artifact.ArtifactID)
	require.NoError(t, err)
	require.Equal(t, chunk.ArtifactID, proof.ArtifactID)
	return workerUCIApplicationArtifact{artifact: artifact, definition: definition, reference: reference, proof: proof}
}

func workerUCIApplicationMembership(path string, artifact workerUCIApplicationArtifact) uci.IndexMembership {
	artifactID := artifact.artifact.ArtifactID
	return uci.IndexMembership{
		PathKey:     path,
		DisplayPath: path,
		Mode:        "100644",
		State:       uci.IndexFilePresent,
		ArtifactID:  &artifactID,
	}
}

func workerUCIApplicationReplacement(t *testing.T, sourcePath string, source workerUCIApplicationArtifact, targetPath string, target workerUCIApplicationArtifact) uci.IndexEdgeReplacement {
	t.Helper()
	sourceSymbol := source.definition.LocalSymbolKey
	targetSymbol := target.definition.LocalSymbolKey
	require.NotNil(t, source.reference)
	referenceID := source.reference.ReferenceSiteID
	var referenceSpan struct {
		ByteStart int64 `json:"byte_start"`
		ByteEnd   int64 `json:"byte_end"`
		LineStart int   `json:"line_start"`
		LineEnd   int   `json:"line_end"`
	}
	require.NoError(t, json.Unmarshal([]byte(source.reference.SyntaxSpan), &referenceSpan))
	return uci.IndexEdgeReplacement{
		SourcePath: sourcePath,
		Edges: []uci.IndexEdge{{
			EdgeKey:          sourcePath + "->" + targetPath,
			SourceArtifactID: source.artifact.ArtifactID,
			SourceSymbolKey:  &sourceSymbol,
			Target: &uci.IndexEdgeTarget{
				PathKey:    targetPath,
				ArtifactID: target.artifact.ArtifactID,
				SymbolKey:  &targetSymbol,
			},
			Relation:         uci.IndexRelation("calls"),
			EvidenceKind:     uci.IndexEvidenceKind("resolved"),
			ResolutionState:  uci.IndexResolutionState("resolved"),
			ResolverRevision: "worker-uci-application-resolver",
			Evidence: uci.IndexEdgeEvidence{
				ReferenceSiteID: &referenceID,
				Span:            uci.IndexSpan{ByteStart: referenceSpan.ByteStart, ByteEnd: referenceSpan.ByteEnd, LineStart: referenceSpan.LineStart, LineEnd: referenceSpan.LineEnd},
				RuleKey:         "worker-uci-application-call",
				Explanation:     "worker UCI application resolved call",
			},
		}},
	}
}

func workerUCIApplicationPart(artifacts []workerUCIApplicationArtifact, memberships []uci.IndexMembership, replacements []uci.IndexEdgeReplacement) uci.IndexPart {
	proofs := make([]uci.IndexArtifactProof, 0, len(artifacts))
	for _, artifact := range artifacts {
		proofs = append(proofs, artifact.proof)
	}
	return uci.IndexPart{Artifacts: proofs, Memberships: memberships, EdgeReplacements: replacements}
}

func workerUCIApplicationPublish(t *testing.T, publisher uci.IndexStore, caller uci.IndexCaller, input workerUCIApplicationPublicationInput) uci.IndexPublishedView {
	t.Helper()
	build, err := publisher.Begin(context.Background(), caller, uci.IndexBeginInput{
		BuildKey: input.key,
		Scope: uci.IndexScope{
			SourceID:      input.checkout.SourceID,
			CheckoutID:    input.checkout.CheckoutID,
			IncarnationID: input.checkout.IncarnationID,
		},
		ProfileID:      input.profileID,
		ExpectedParent: input.parent,
		Mode:           uci.IndexManifestFull,
		JobKind:        input.jobKind,
	})
	require.NoError(t, err)
	partDigest, err := uci.DigestIndexPart(input.part)
	require.NoError(t, err)
	ack, err := publisher.Stage(context.Background(), caller, uci.IndexStageInput{
		Build:    build.Build,
		Sequence: 0,
		Digest:   partDigest,
		Part:     input.part,
	})
	require.NoError(t, err)
	partsDigest, err := uci.DigestIndexParts([]uci.IndexPartAck{ack})
	require.NoError(t, err)
	manifestDigest, err := uci.DigestIndexManifest(input.memberships)
	require.NoError(t, err)
	edgesDigest, err := uci.DigestIndexEdges(input.replacements)
	now := time.Now().UTC().Truncate(time.Microsecond)
	headOID := strings.Repeat("a", 40)
	objectFormat := "sha1"
	refLabel := "refs/heads/worker-uci-application"
	published, err := publisher.Finalize(context.Background(), caller, uci.IndexFinalizeInput{
		Build:          build.Build,
		ExpectedParent: input.parent,
		Manifest: uci.IndexManifestCompletion{
			PartCount:      1,
			PartsDigest:    partsDigest,
			EntryCount:     uint64(len(input.memberships)),
			ManifestDigest: manifestDigest,
			EdgeCount:      uint64(workerUCIApplicationEdgeCount(input.replacements)),
			EdgesDigest:    edgesDigest,
			ScanOutcome:    uci.IndexScanComplete,
			CensusComplete: true,
			Observation: uci.IndexObservation{
				HeadOID:       &headOID,
				ObjectFormat:  &objectFormat,
				RefLabel:      &refLabel,
				ObservedFSSeq: input.sequence,
				ScanStart:     now,
				ScanEnd:       now.Add(time.Second),
			},
			Coverage: uci.IndexCoverage{
				Structural: uci.IndexCoverageComplete,
				Lexical:    uci.IndexCoverageComplete,
				Vector:     uci.IndexCoverageUnavailable,
			},
		},
	})
	require.NoError(t, err)
	return published
}

func workerUCIApplicationEdgeCount(replacements []uci.IndexEdgeReplacement) int {
	count := 0
	for _, replacement := range replacements {
		count += len(replacement.Edges)
	}
	return count
}

func workerUCIApplicationItemAtPath(t *testing.T, items uci.QueryItems, path string) uci.QueryItem {
	t.Helper()
	for _, item := range items {
		if item.Path == path {
			return item
		}
	}
	t.Fatalf("query response has no item at %q", path)
	return uci.QueryItem{}
}

func workerUCIApplicationSelectContext(t *testing.T, server *mcp.Server, ctx context.Context, ref uci.ContextRef) string {
	t.Helper()
	response := workerUCIApplicationToolResponse(t, server, ctx, "codebase_context", map[string]any{
		"action":              "select",
		"source_id":           ref.SourceID,
		"checkout_id":         ref.CheckoutID,
		"view_id":             ref.ViewID,
		"analysis_profile_id": ref.AnalysisProfileID,
		"generation":          ref.Generation,
	})
	var payload struct {
		ContextHandle string `json:"context_handle"`
	}
	require.NoError(t, json.Unmarshal([]byte(workerUCIApplicationToolText(t, response)), &payload))
	require.NotEmpty(t, payload.ContextHandle)
	return payload.ContextHandle
}

func workerUCIApplicationToolResponse(t *testing.T, server *mcp.Server, ctx context.Context, name string, arguments map[string]any) *mcp.Response {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
	require.NoError(t, err)
	response := server.HandleRequest(ctx, &mcp.Request{
		JSONRPC: "2.0",
		ID:      uuid.NewString(),
		Method:  "tools/call",
		Params:  params,
	})
	require.NotNil(t, response)
	require.Nil(t, response.Error)
	return response
}

func workerUCIApplicationToolText(t *testing.T, response *mcp.Response) string {
	t.Helper()
	result, ok := response.Result.(map[string]any)
	require.True(t, ok)
	content, ok := result["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	text, ok := content[0]["text"].(string)
	require.True(t, ok)
	return text
}

func workerUCIApplicationQueryResponse(t *testing.T, response *mcp.Response) uci.QueryResponse {
	t.Helper()
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(workerUCIApplicationToolText(t, response)), &payload))
	return payload
}

func workerUCIApplicationExposureCount(t *testing.T, db *gormlib.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&gormstore.UCIExposure{}).Count(&count).Error)
	return count
}

func workerUCIApplicationDigest(value string) string {
	return workerUCIApplicationDigestBytes([]byte(value))
}

func workerUCIApplicationDigestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", sum)
}

type workerUCIApplicationFailingExposureStore struct{}

func (workerUCIApplicationFailingExposureStore) AppendExposure(context.Context, uci.ExposureRecord) (uci.ExposureRecord, error) {
	return uci.ExposureRecord{}, errors.New("fixture exposure append failed")
}

func (workerUCIApplicationFailingExposureStore) AppendCompletion(context.Context, uci.CompletionEvidence) (uci.CompletionEvidence, error) {
	return uci.CompletionEvidence{}, errors.New("fixture completion append failed")
}

type workerUCIApplicationQueryStore struct {
	candidates []uci.QueryCandidate
	calls      []uci.QuerySpec
}

func (store *workerUCIApplicationQueryStore) SelectCandidates(_ context.Context, _ uci.AuthorizedContext, spec uci.QuerySpec) (uci.QueryStoreResult, error) {
	store.calls = append(store.calls, spec)
	if spec.Offset >= len(store.candidates) {
		return uci.QueryStoreResult{Coverage: uci.IndexCoverageComplete}, nil
	}
	return uci.QueryStoreResult{Candidates: store.candidates[spec.Offset:], Coverage: uci.IndexCoverageComplete}, nil
}

type workerUCIApplicationSemanticStore struct {
	candidates    []uci.SemanticCandidate
	calls         []uci.QuerySpec
	continuations map[string]uci.SemanticContinuation
	err           error
}

func (*workerUCIApplicationSemanticStore) LookupCandidateEmbedding(context.Context, uci.AuthorizedContext, uci.VectorProfile, uci.QueryCandidate) ([]float32, bool, error) {
	return nil, false, nil
}

func (*workerUCIApplicationSemanticStore) StoreCandidateEmbedding(context.Context, uci.AuthorizedContext, uci.VectorProfile, uci.QueryCandidate, []float32) error {
	return nil
}

func (store *workerUCIApplicationSemanticStore) SelectHybridCandidates(_ context.Context, _ uci.AuthorizedContext, _ uci.VectorProfile, _ []float32, spec uci.QuerySpec) (uci.SemanticStoreResult, error) {
	store.calls = append(store.calls, spec)
	if store.err != nil {
		return uci.SemanticStoreResult{}, store.err
	}
	if spec.Offset >= len(store.candidates) {
		return uci.SemanticStoreResult{Coverage: uci.IndexCoverageComplete, VectorCoverage: 1}, nil
	}
	end := spec.Offset + spec.Limit + 1
	if end > len(store.candidates) {
		end = len(store.candidates)
	}
	return uci.SemanticStoreResult{
		Candidates:     store.candidates[spec.Offset:end],
		Coverage:       uci.IndexCoverageComplete,
		VectorCoverage: 1,
	}, nil
}

func (store *workerUCIApplicationSemanticStore) CreateSemanticContinuation(_ context.Context, continuation uci.SemanticContinuation) error {
	if store.continuations == nil {
		store.continuations = make(map[string]uci.SemanticContinuation)
	}
	store.continuations[continuation.CursorRef] = workerUCIApplicationContinuationClone(continuation)
	return nil
}

func (store *workerUCIApplicationSemanticStore) LoadSemanticContinuation(_ context.Context, cursorRef string) (uci.SemanticContinuation, bool, error) {
	continuation, found := store.continuations[cursorRef]
	if found && !continuation.ExpiresAt.After(time.Now().UTC()) {
		delete(store.continuations, cursorRef)
		found = false
	}
	return workerUCIApplicationContinuationClone(continuation), found, nil
}

func workerUCIApplicationContinuationClone(continuation uci.SemanticContinuation) uci.SemanticContinuation {
	clone := continuation
	clone.Vector = append([]float32(nil), continuation.Vector...)
	if continuation.Context.SpaceID != nil {
		spaceID := *continuation.Context.SpaceID
		clone.Context.SpaceID = &spaceID
	}
	return clone
}

type workerUCIApplicationEmbedder struct {
	model string
}

func (embedder *workerUCIApplicationEmbedder) Model() string {
	return embedder.model
}

func (*workerUCIApplicationEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	vector := make([]float32, embedding.EmbeddingDim)
	vector[0] = 1
	return [][]float32{vector}, nil
}

type workerUCIApplicationStatusStore struct {
	snapshot uci.IndexStatusSnapshot
	err      error
}

func (store *workerUCIApplicationStatusStore) LoadIndexStatus(context.Context, uci.AuthorizedContext, *uci.VectorProfile) (uci.IndexStatusSnapshot, error) {
	return store.snapshot.Clone(), store.err
}

func workerUCIApplicationSemanticCandidate(ref uci.ContextRef, artifactID, name, path string) uci.SemanticCandidate {
	text := "func " + name + "() {}"
	return uci.SemanticCandidate{Candidate: uci.QueryCandidate{
		Context: ref,
		Proof: uci.IndexArtifactProof{
			ArtifactID:      artifactID,
			ContentDigest:   uci.IndexDigest(workerUCIApplicationDigest(text)),
			FactsDigest:     uci.IndexDigest(workerUCIApplicationDigest("facts:" + text)),
			DefinitionCount: 1,
			ChunkCount:      1,
		},
		EntityKey:    "symbol:" + name,
		LocalName:    name,
		RelativePath: path,
		Span:         uci.IndexSpan{ByteStart: 0, ByteEnd: int64(len(text)), LineStart: 1, LineEnd: 1},
		Text:         text,
		Kind:         uci.QueryItemCode,
		Language:     "go",
		Score:        1,
	}, MatchSources: []uci.QueryMatchSource{uci.QueryMatchVector}}
}

func workerUCIApplicationStatusSnapshot(ref uci.ContextRef, embeddingCoverage uci.IndexCoverageState) uci.IndexStatusSnapshot {
	zero := int64(0)
	profileID := "72000000-0000-4000-8000-000000000001"
	jobState := uci.IndexStatusJobQueued
	ready, pending := uint64(1), uint64(1)
	if embeddingCoverage == uci.IndexCoverageComplete {
		jobState = uci.IndexStatusJobSucceeded
		ready, pending = 2, 0
	}
	started := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	return uci.IndexStatusSnapshot{
		Context:             ref,
		SourceState:         uci.IndexStatusSourceActive,
		CheckoutState:       uci.IndexStatusCheckoutWatching,
		ViewState:           uci.IndexStatusViewPublished,
		CurrentViewRelation: uci.IndexStatusCurrentViewSelected,
		ObservedFSSeq:       ref.Generation,
		Coverage:            uci.IndexCoverage{Structural: uci.IndexCoverageComplete, Lexical: uci.IndexCoverageComplete, Vector: embeddingCoverage},
		PublishedAt:         started.Add(time.Minute),
		ScanStartedAt:       started,
		ScanCompletedAt:     started.Add(30 * time.Second),
		ChunkCount:          2,
		ReadyEmbeddingCount: ready,
		Embedding: uci.EmbeddingStatus{
			EmbeddingProfileID: &profileID,
			Coverage:           embeddingCoverage,
			TotalCandidates:    2,
			ReadyCandidates:    ready,
			PendingJobs:        pending,
			JobState:           &jobState,
		},
		Freshness: uci.QueryFreshness{
			State:               uci.QueryFreshnessObservedCurrent,
			Method:              uci.QueryFreshnessWatchWatermark,
			PendingChanges:      &zero,
			EnrichmentWatermark: uci.QueryEnrichmentWatermark{Sequence: ref.Generation, State: uci.QueryEnrichmentCurrent},
		},
	}
}
