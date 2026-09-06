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
	require.Contains(t, searchResponse.Retrieval.DegradationReasons, "vector_provider_unavailable")
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

func TestUCIApplicationUsesConfiguredSharedEmbeddingClient(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	const model = "worker-uci-shared-embedding"
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/embeddings" {
			t.Errorf("embedding request = %s %s, want POST /v1/embeddings", request.Method, request.URL.Path)
			http.Error(writer, "unexpected embedding request", http.StatusBadRequest)
			return
		}
		var payload struct {
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
			Input      []string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode embedding request: %v", err)
			http.Error(writer, "invalid embedding request", http.StatusBadRequest)
			return
		}
		if payload.Model != model || payload.Dimensions != embedding.EmbeddingDim || len(payload.Input) != 1 {
			t.Errorf("embedding request payload = %#v", payload)
			http.Error(writer, "unexpected embedding payload", http.StatusBadRequest)
			return
		}
		providerCalls.Add(1)
		vector := make([]float32, embedding.EmbeddingDim)
		vector[0] = 1
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]any{{
				"embedding": vector,
				"index":     0,
			}},
		}); err != nil {
			t.Errorf("encode embedding response: %v", err)
		}
	}))
	t.Cleanup(provider.Close)
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
	require.Contains(t, response.Retrieval.DegradationReasons, "vector_coverage_incomplete")
	require.NotContains(t, response.Retrieval.DegradationReasons, "vector_provider_unavailable")
	require.NotNil(t, response.Items)
	require.NotEmpty(t, *response.Items)
	require.Equal(t, int32(1), providerCalls.Load())
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

type workerUCIApplicationStatusResponse struct {
	Context          uci.QueryContextRef                `json:"context"`
	TotalChunks      int64                              `json:"total_chunks"`
	EmbeddedChunks   int64                              `json:"embedded_chunks"`
	EvidenceRecorder mcp.CodebaseEvidenceRecorderHealth `json:"evidence_recorder"`
	Freshness        uci.QueryFreshness                 `json:"freshness"`
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

	alphaOld := workerUCIApplicationAddArtifact(t, composition.projectionStore, source.SourceID, profile.ParserBundleDigest, "alpha-old-"+token, "Alpha", "alpha", "func Alpha() { Beta(); _ = \"historical-published-body SearchNeedle\" }\n", "Beta")
	beta := workerUCIApplicationAddArtifact(t, composition.projectionStore, source.SourceID, profile.ParserBundleDigest, "beta-"+token, "Beta", "beta", "func Beta() { _ = \"beta SearchNeedle\" }\n", "")
	outside := workerUCIApplicationAddArtifact(t, composition.projectionStore, source.SourceID, profile.ParserBundleDigest, "outside-"+token, "Outside", "outside", "func Outside() { _ = \"outside SearchNeedle\" }\n", "")

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
	publisher, err := composition.projectionStore.Publisher(composition.authorizer, uci.DefaultIndexPublicationLimits())
	require.NoError(t, err)
	caller := uci.IndexCaller{
		AuthRealm:     string(auth.SourceClient),
		Principal:     principal,
		OwnerInstance: "worker-uci-application-owner-" + token,
	}
	historical := workerUCIApplicationPublish(t, publisher, caller, checkout, profile.ProfileID, "worker-uci-historical-"+token, uci.IndexJobInitial, nil, firstPart, firstMemberships, firstReplacements, 11)

	alphaCurrent := workerUCIApplicationAddArtifact(t, composition.projectionStore, source.SourceID, profile.ParserBundleDigest, "alpha-current-"+token, "Alpha", "alpha", "func Alpha() { Beta(); _ = \"current-published-body SearchNeedle\" }\n", "Beta")
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
	current := workerUCIApplicationPublish(t, publisher, caller, checkout, profile.ProfileID, "worker-uci-current-"+token, uci.IndexJobReconcile, &parent, currentPart, currentMemberships, currentReplacements, 12)

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

func workerUCIApplicationAddArtifact(t *testing.T, projection *gormstore.UCIProjectionStore, sourceID, profileDigest, label, name, symbol, source, rawTarget string) workerUCIApplicationArtifact {
	t.Helper()

	body := []byte("package fixture\n" + source)
	digest := workerUCIApplicationDigestBytes(body)
	blob, err := projection.UpsertBlob(context.Background(), gormstore.UpsertUCIBlobInput{
		SourceID:         sourceID,
		ProtectionDomain: "source-private",
		ContentDigest:    digest,
		ByteLength:       int64(len(body)),
		SafeContent:      body,
		Encoding:         "utf-8",
		StorageState:     gormstore.UCIBlobStored,
	})
	require.NoError(t, err)
	artifact, err := projection.UpsertParseArtifact(context.Background(), gormstore.UpsertUCIParseArtifactInput{
		SourceID:                sourceID,
		BlobID:                  blob.BlobID,
		Language:                "go",
		ParserRevision:          "worker-uci-application-parser-" + label,
		GrammarDigest:           workerUCIApplicationDigest("grammar-" + label),
		ExtractionProfileDigest: profileDigest,
		Status:                  gormstore.UCIParseArtifactComplete,
		Diagnostics:             `{}`,
	})
	require.NoError(t, err)

	definitionStart := int64(strings.Index(string(body), "func "+name))
	require.GreaterOrEqual(t, definitionStart, int64(0))
	definition, err := projection.UpsertDefinition(context.Background(), gormstore.UpsertUCIDefinitionInput{
		ArtifactID:         artifact.ArtifactID,
		LocalSymbolKey:     symbol,
		Kind:               "function",
		Name:               name,
		QualifiedLocalName: "fixture." + name,
		Signature:          "func " + name + "()",
		ByteStart:          definitionStart,
		ByteEnd:            int64(len(body) - 1),
		LineStart:          2,
		LineEnd:            2,
	})
	require.NoError(t, err)
	chunk, err := projection.UpsertChunk(context.Background(), gormstore.UpsertUCIChunkInput{
		SourceID:      sourceID,
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
	if rawTarget != "" {
		reference, err = projection.UpsertReferenceSite(context.Background(), gormstore.UpsertUCIReferenceSiteInput{
			ArtifactID:     artifact.ArtifactID,
			SiteKey:        "reference-" + label,
			OwnerSymbolKey: &definition.LocalSymbolKey,
			RawTarget:      rawTarget,
			Relation:       "calls",
			SyntaxSpan:     `{"byte_start":0,"byte_end":1,"line_start":1,"line_end":1}`,
			ResolverHints:  `{}`,
		})
		require.NoError(t, err)
	}

	proof, err := projection.DescribeIndexArtifact(context.Background(), sourceID, artifact.ArtifactID)
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
				Span:            uci.IndexSpan{ByteStart: 0, ByteEnd: 1, LineStart: 1, LineEnd: 1},
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

func workerUCIApplicationPublish(t *testing.T, publisher uci.IndexStore, caller uci.IndexCaller, checkout *gormstore.UCICheckout, profileID, key string, jobKind uci.IndexJobKind, parent *uci.ContextRef, part uci.IndexPart, memberships []uci.IndexMembership, replacements []uci.IndexEdgeReplacement, sequence int64) uci.IndexPublishedView {
	t.Helper()
	build, err := publisher.Begin(context.Background(), caller, uci.IndexBeginInput{
		BuildKey: key,
		Scope: uci.IndexScope{
			SourceID:      checkout.SourceID,
			CheckoutID:    checkout.CheckoutID,
			IncarnationID: checkout.IncarnationID,
		},
		ProfileID:      profileID,
		ExpectedParent: parent,
		Mode:           uci.IndexManifestFull,
		JobKind:        jobKind,
	})
	require.NoError(t, err)
	partDigest, err := uci.DigestIndexPart(part)
	require.NoError(t, err)
	ack, err := publisher.Stage(context.Background(), caller, uci.IndexStageInput{
		Build:    build.Build,
		Sequence: 0,
		Digest:   partDigest,
		Part:     part,
	})
	require.NoError(t, err)
	partsDigest, err := uci.DigestIndexParts([]uci.IndexPartAck{ack})
	require.NoError(t, err)
	manifestDigest, err := uci.DigestIndexManifest(memberships)
	require.NoError(t, err)
	edgesDigest, err := uci.DigestIndexEdges(replacements)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	headOID := strings.Repeat("a", 40)
	objectFormat := "sha1"
	refLabel := "refs/heads/worker-uci-application"
	published, err := publisher.Finalize(context.Background(), caller, uci.IndexFinalizeInput{
		Build:          build.Build,
		ExpectedParent: parent,
		Manifest: uci.IndexManifestCompletion{
			PartCount:      1,
			PartsDigest:    partsDigest,
			EntryCount:     uint64(len(memberships)),
			ManifestDigest: manifestDigest,
			EdgeCount:      uint64(workerUCIApplicationEdgeCount(replacements)),
			EdgesDigest:    edgesDigest,
			ScanOutcome:    uci.IndexScanComplete,
			CensusComplete: true,
			Observation: uci.IndexObservation{
				HeadOID:       &headOID,
				ObjectFormat:  &objectFormat,
				RefLabel:      &refLabel,
				ObservedFSSeq: sequence,
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
