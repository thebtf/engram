package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciApplicationLegacyProjectDomain = "project"
	uciApplicationLegacyProjectScheme = "project_id"
)

// UCIApplication is the worker-owned composition of the client-scoped context
// application and the exact-View code intelligence services. It returns only
// pre-exposure responses; MCP owns the durable exposure append and release.
type UCIApplication struct {
	*mcp.UCIContextApplication

	aliasResolver        *uci.AliasResolver
	queryService         *uci.QueryService
	semanticService      *uci.SemanticService
	graphService         *uci.GraphService
	versionedReadService *uci.VersionedReadService
	indexStatusService   *uci.IndexStatusService
}

var (
	_ mcp.CodebaseContextApplication      = (*UCIApplication)(nil)
	_ mcp.CodebaseIntelligenceApplication = (*UCIApplication)(nil)
	_ mcp.CodebaseFreshnessApplication    = (*UCIApplication)(nil)
	_ mcp.CodebaseGraphApplication        = (*UCIApplication)(nil)
	_ mcp.CodebaseReadApplication         = (*UCIApplication)(nil)
)

// NewUCIApplication composes the exact existing context application with the
// real PostgreSQL-backed UCI services. Semantic retrieval is deliberately
// optional: callers receive structural lexical FTS until the selected View's
// durable embedding status is complete.
func NewUCIApplication(
	contextApplication *mcp.UCIContextApplication,
	aliasResolver *uci.AliasResolver,
	queryService *uci.QueryService,
	semanticService *uci.SemanticService,
	graphService *uci.GraphService,
	versionedReadService *uci.VersionedReadService,
	indexStatusService *uci.IndexStatusService,
) (*UCIApplication, error) {
	if contextApplication == nil {
		return nil, errors.New("UCI application requires a context application")
	}
	if aliasResolver == nil {
		return nil, errors.New("UCI application requires an alias resolver")
	}
	if queryService == nil {
		return nil, errors.New("UCI application requires a query service")
	}
	if graphService == nil {
		return nil, errors.New("UCI application requires a graph service")
	}
	if versionedReadService == nil {
		return nil, errors.New("UCI application requires a versioned read service")
	}
	if indexStatusService == nil {
		return nil, errors.New("UCI application requires an index status service")
	}

	return &UCIApplication{
		UCIContextApplication: contextApplication,
		aliasResolver:         aliasResolver,
		queryService:          queryService,
		semanticService:       semanticService,
		graphService:          graphService,
		versionedReadService:  versionedReadService,
		indexStatusService:    indexStatusService,
	}, nil
}

// ResolveLegacyProject treats a legacy project identifier only as compatibility
// evidence. It never resolves, selects, or widens an authorized ContextRef.
func (application *UCIApplication) ResolveLegacyProject(ctx context.Context, _ uci.AuthorizedContext, project string) (uci.AliasTarget, error) {
	if application == nil || application.aliasResolver == nil {
		return uci.AliasTarget{}, errors.New("UCI application alias resolver is not configured")
	}
	caller, err := uciApplicationAuthenticatedCaller(ctx)
	if err != nil {
		return uci.AliasTarget{}, err
	}

	return application.aliasResolver.Resolve(ctx, uci.LegacyAliasKey{
		AuthRealm:       caller.authRealm,
		LegacyDomain:    uciApplicationLegacyProjectDomain,
		Scheme:          uciApplicationLegacyProjectScheme,
		Value:           project,
		ClientNamespace: caller.clientSessionID,
	})
}

// SearchCodebase executes structural FTS inside the already-authorized immutable
// View. A queued embedding job must not delay this structural publication
// boundary; semantic retrieval becomes eligible only after durable embedding
// status is complete.
func (application *UCIApplication) SearchCodebase(ctx context.Context, authorized uci.AuthorizedContext, input mcp.CodebaseSearchInput) (uci.QueryResponse, error) {
	if application == nil || application.queryService == nil {
		return uci.QueryResponse{}, errors.New("UCI application query service is not configured")
	}
	clientSessionID, err := uciApplicationClientSession(ctx)
	if err != nil {
		return uci.QueryResponse{}, err
	}
	pathPrefix, err := uci.NormalizeQueryPathPrefix(input.PathPrefix)
	if err != nil {
		return uci.QueryResponse{}, fmt.Errorf("UCI application search path prefix: %w", err)
	}

	spec := uci.QuerySpec{
		ClientSessionID: clientSessionID,
		Mode:            uci.QueryModeFTS,
		Text:            input.Query,
		Filter:          uci.QueryFilter{PathPrefix: pathPrefix},
		Order:           uci.QueryOrderRelevance,
		Limit:           input.Limit,
	}
	var result uci.QueryResult
	if application.semanticService != nil && application.indexStatusService != nil {
		status, statusErr := application.indexStatusService.Status(ctx, authorized, "")
		if statusErr != nil {
			return uci.QueryResponse{}, statusErr
		}
		if status.Embedding.Coverage == uci.IndexCoverageComplete {
			result, err = application.semanticService.Query(ctx, authorized, spec)
		} else {
			result, err = application.queryService.Query(ctx, authorized, spec)
		}
	} else {
		result, err = application.queryService.Query(ctx, authorized, spec)
	}
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if err := result.Response.ValidatePreExposure(); err != nil {
		return uci.QueryResponse{}, fmt.Errorf("UCI application search response: %w", err)
	}
	return result.Response, nil
}

// SearchOperatorCodebase executes a browser-bound lexical query. Its caller
// supplies a tab-scoped client session and any continuation only after the HTTP
// boundary has resolved its server-owned cursor; no daemon transport changes.
func (application *UCIApplication) SearchOperatorCodebase(ctx context.Context, authorized uci.AuthorizedContext, spec uci.QuerySpec) (uci.QueryResponse, error) {
	if application == nil || application.queryService == nil {
		return uci.QueryResponse{}, errors.New("UCI application browser query service is not configured")
	}
	if spec.Mode != uci.QueryModeFTS || spec.Order != uci.QueryOrderRelevance {
		return uci.QueryResponse{}, errors.New("UCI application browser query must be lexical relevance")
	}
	result, err := application.queryService.Query(ctx, authorized, spec)
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if err := result.Response.ValidatePreExposure(); err != nil {
		return uci.QueryResponse{}, fmt.Errorf("UCI application browser search response: %w", err)
	}
	return result.Response, nil
}

// ReadCodebase maps a View-grounded citation directly to the exact persisted
// source-read service. It does not have a working-copy or disk fallback.
func (application *UCIApplication) ReadCodebase(ctx context.Context, authorized uci.AuthorizedContext, input mcp.CodebaseReadInput) (uci.QueryResponse, error) {
	if application == nil || application.versionedReadService == nil {
		return uci.QueryResponse{}, errors.New("UCI application versioned read service is not configured")
	}

	response, err := application.versionedReadService.Read(ctx, authorized, uci.VersionedReadSpec{
		Entity:            input.Ref,
		Span:              input.Span,
		ContentDigest:     input.ContentDigest,
		MaxBytes:          input.MaxBytes,
		VerifyWorkingCopy: input.VerifyWorkingCopy,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if err := response.ValidatePreExposure(); err != nil {
		return uci.QueryResponse{}, fmt.Errorf("UCI application read response: %w", err)
	}
	return response, nil
}

// CodebaseStatus returns exact selected-View counts. MCP adds recorder health
// from the same recorder that owns release-time evidence appends.
func (application *UCIApplication) CodebaseStatus(ctx context.Context, authorized uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error) {
	if application == nil || application.indexStatusService == nil {
		return mcp.CodebaseStatusSnapshot{}, errors.New("UCI application index status service is not configured")
	}

	snapshot, err := application.indexStatusService.Status(ctx, authorized, "")
	if err != nil {
		return mcp.CodebaseStatusSnapshot{}, err
	}
	totalChunks, err := uciApplicationInt64Count(snapshot.ChunkCount)
	if err != nil {
		return mcp.CodebaseStatusSnapshot{}, fmt.Errorf("UCI application chunk count: %w", err)
	}
	embeddedChunks, err := uciApplicationInt64Count(snapshot.ReadyEmbeddingCount)
	if err != nil {
		return mcp.CodebaseStatusSnapshot{}, fmt.Errorf("UCI application embedded chunk count: %w", err)
	}
	freshness := snapshot.Freshness
	return mcp.CodebaseStatusSnapshot{
		TotalChunks:    totalChunks,
		EmbeddedChunks: embeddedChunks,
		Embedding:      snapshot.Embedding,
		Freshness:      &freshness,
	}, nil
}

// CodebaseFreshness returns the exact durable snapshot freshness for an empty
// barrier. A nonempty token is deliberately delegated to IndexStatusService,
// which returns its closed BARRIER_UNAVAILABLE outcome until a real issuer is
// composed.
func (application *UCIApplication) CodebaseFreshness(ctx context.Context, authorized uci.AuthorizedContext, barrierToken string) (uci.QueryFreshness, error) {
	if application == nil || application.indexStatusService == nil {
		return uci.QueryFreshness{}, errors.New("UCI application index status service is not configured")
	}

	snapshot, err := application.indexStatusService.Status(ctx, authorized, barrierToken)
	if err != nil {
		return uci.QueryFreshness{}, err
	}
	return snapshot.Freshness, nil
}

// ExploreCodebase maps a bounded static graph result to the common pre-exposure
// response envelope. The outcome remains explicit in warnings so empty graph
// payloads cannot be mistaken for a conclusive absence when ambiguous, partial,
// or capped.
func (application *UCIApplication) ExploreCodebase(ctx context.Context, authorized uci.AuthorizedContext, input mcp.CodebaseGraphInput) (uci.QueryResponse, error) {
	if application == nil || application.graphService == nil {
		return uci.QueryResponse{}, errors.New("UCI application graph service is not configured")
	}
	clientSessionID, err := uciApplicationClientSession(ctx)
	if err != nil {
		return uci.QueryResponse{}, err
	}

	result, err := application.graphService.Explore(ctx, authorized, uci.GraphSpec{
		ClientSessionID: clientSessionID,
		Action:          input.Action,
		Target:          input.Target,
		Destination:     input.Destination,
		Filter:          input.Filter,
		Budget:          input.Budget,
		Continuation:    input.Continuation,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}

	response, err := uciApplicationGraphResponse(authorized.Ref(), result, input.Budget)
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if err := response.ValidatePreExposure(); err != nil {
		return uci.QueryResponse{}, fmt.Errorf("UCI application graph response: %w", err)
	}
	return response, nil
}

type uciApplicationCaller struct {
	authRealm       string
	principal       string
	clientSessionID string
}

func uciApplicationAuthenticatedCaller(ctx context.Context) (uciApplicationCaller, error) {
	if ctx == nil {
		return uciApplicationCaller{}, errors.New("UCI application caller context is required")
	}
	identity, ok := auth.IdentityFrom(ctx)
	if !ok || identity.Source != auth.SourceClient || identity.WorkstationID() == "" {
		return uciApplicationCaller{}, errors.New("UCI application authenticated client caller is required")
	}
	principal, _, ok := identity.MemoryOwner()
	if !ok || principal != identity.Principal {
		return uciApplicationCaller{}, errors.New("UCI application authenticated principal is required")
	}
	clientSessionID, err := uciApplicationClientSession(ctx)
	if err != nil {
		return uciApplicationCaller{}, err
	}
	return uciApplicationCaller{
		authRealm:       string(identity.Source),
		principal:       principal,
		clientSessionID: clientSessionID,
	}, nil
}

func uciApplicationClientSession(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("UCI application caller context is required")
	}
	clientSessionID := auditcontext.SourceSession(ctx)
	if clientSessionID == "" {
		return "", errors.New("UCI application client session is required")
	}
	return clientSessionID, nil
}

func uciApplicationGraphResponse(ref uci.ContextRef, result uci.GraphResult, budget uci.GraphBudget) (uci.QueryResponse, error) {
	if result.Semantics != uci.GraphSemanticsStatic {
		return uci.QueryResponse{}, fmt.Errorf("UCI application graph semantics %q are unsupported", result.Semantics)
	}

	contexts := uci.QueryContexts{uciApplicationQueryContextRef(ref)}
	items := uci.QueryItems{}
	warnings := uci.QueryWarnings{}
	status := uci.QueryStatusOK
	switch result.Outcome {
	case uci.GraphOutcomeComplete:
	case uci.GraphOutcomeAmbiguous:
		status = uci.QueryStatusPartial
		warnings = append(warnings, "graph_outcome:ambiguous")
	case uci.GraphOutcomeUnknownOrTruncated:
		status = uci.QueryStatusPartial
		warnings = append(warnings, "graph_outcome:unknown_or_truncated")
	case uci.GraphOutcomeNoPath:
		status = uci.QueryStatusEmpty
		warnings = append(warnings, "graph_outcome:no_path")
	case uci.GraphOutcomeNoImpact:
		status = uci.QueryStatusEmpty
		warnings = append(warnings, "graph_outcome:no_impact")
	default:
		return uci.QueryResponse{}, fmt.Errorf("UCI application graph outcome %q is invalid", result.Outcome)
	}

	if result.Coverage == uci.IndexCoverageUnavailable {
		warnings = append(warnings, "graph_outcome:unknown_or_truncated", "graph_unavailable")
		truncated := false
		continuation := uci.QueryContinuation{}
		return uci.QueryResponse{
			Schema:    uci.QueryResponseSchema,
			Status:    uci.QueryStatusUnavailable,
			Contexts:  &contexts,
			Freshness: uciApplicationPinnedFreshness(ref.Generation),
			Retrieval: &uci.QueryRetrieval{
				Mode:               uci.QueryRetrievalGraph,
				DegradationReasons: []string{},
			},
			Coverage:     &uci.QueryCoverage{Structural: uci.IndexCoverageUnavailable},
			Error:        &uci.QueryError{Code: uci.QueryErrorBuildIncomplete},
			Items:        &items,
			Truncated:    &truncated,
			Warnings:     &warnings,
			Continuation: &continuation,
		}, nil
	}
	if result.Coverage != uci.IndexCoverageComplete && result.Coverage != uci.IndexCoveragePartial {
		return uci.QueryResponse{}, fmt.Errorf("UCI application graph coverage %q is invalid", result.Coverage)
	}
	if budget.MaxNodes < 1 || budget.MaxEdges < 1 {
		return uci.QueryResponse{}, errors.New("UCI application graph budget is invalid")
	}

	graph := uci.QueryGraph{
		Nodes:      append([]uci.QueryEntityRef{}, result.Graph.Nodes...),
		Edges:      append([]uci.QueryGraphEdge{}, result.Graph.Edges...),
		StopReason: result.Graph.StopReason,
	}
	if len(graph.Nodes) > budget.MaxNodes || len(graph.Edges) > budget.MaxEdges {
		return uci.QueryResponse{}, errors.New("UCI application graph result exceeds its budget")
	}
	candidateNodesTruncated := false
	if result.Outcome == uci.GraphOutcomeAmbiguous {
		known := make(map[uci.QueryEntityRef]struct{}, len(graph.Nodes))
		for _, node := range graph.Nodes {
			known[node] = struct{}{}
		}
		for _, candidate := range result.Candidates {
			if _, exists := known[candidate]; exists {
				continue
			}
			if len(graph.Nodes) == budget.MaxNodes {
				candidateNodesTruncated = true
				break
			}
			known[candidate] = struct{}{}
			graph.Nodes = append(graph.Nodes, candidate)
		}
		if candidateNodesTruncated {
			warnings = append(warnings, "graph_candidates_truncated")
		}
	}

	if result.Coverage != uci.IndexCoverageComplete {
		status = uci.QueryStatusPartial
	}
	unresolved := int64(len(result.Unresolved))
	unsupported := int64(0)
	truncated := result.Continuation != nil
	continuation := uci.QueryContinuation{Value: result.Continuation}
	return uci.QueryResponse{
		Schema:    uci.QueryResponseSchema,
		Status:    status,
		Contexts:  &contexts,
		Freshness: uciApplicationPinnedFreshness(ref.Generation),
		Retrieval: &uci.QueryRetrieval{
			Mode:               uci.QueryRetrievalGraph,
			DegradationReasons: []string{},
		},
		Coverage: &uci.QueryCoverage{
			Structural:       result.Coverage,
			UnresolvedSites:  &unresolved,
			UnsupportedFiles: &unsupported,
		},
		Items:        &items,
		Graph:        &graph,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}, nil
}

func uciApplicationQueryContextRef(ref uci.ContextRef) uci.QueryContextRef {
	contextRef := uci.QueryContextRef{
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
		ViewID:     ref.ViewID,
		Generation: ref.Generation,
		ProfileID:  ref.AnalysisProfileID,
	}
	if ref.SpaceID != nil {
		spaceID := *ref.SpaceID
		contextRef.SpaceID = &spaceID
	}
	return contextRef
}

func uciApplicationPinnedFreshness(generation int64) *uci.QueryFreshness {
	return &uci.QueryFreshness{
		State:          uci.QueryFreshnessHistorical,
		Method:         uci.QueryFreshnessPinnedHistory,
		PendingChanges: nil,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: generation,
			State:    uci.QueryEnrichmentCurrent,
		},
	}
}

func uciApplicationInt64Count(value uint64) (int64, error) {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		return 0, errors.New("count exceeds int64")
	}
	return int64(value), nil
}
