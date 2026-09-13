package uci

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const graphContinuationTokenVersion = "v1"

// GraphService owns static traversal, result bounds, and opaque continuation binding.
type GraphService struct {
	store           GraphStore
	continuationKey [sha256.Size]byte
}

// NewGraphService creates a graph service for one injected static graph store.
func NewGraphService(store GraphStore) *GraphService {
	return &GraphService{
		store:           store,
		continuationKey: newQueryContinuationKey(),
	}
}

type graphContinuationPayload struct {
	Version         string           `json:"version"`
	ClientSessionID string           `json:"client_session_id"`
	SpaceID         *string          `json:"space_id,omitempty"`
	SourceID        string           `json:"source_id"`
	CheckoutID      string           `json:"checkout_id"`
	ViewID          string           `json:"view_id"`
	ProfileID       string           `json:"profile_id"`
	Generation      int64            `json:"generation"`
	Action          GraphAction      `json:"action"`
	Target          GraphTarget      `json:"target"`
	Destination     *GraphTarget     `json:"destination,omitempty"`
	Filter          GraphFilter      `json:"filter"`
	Budget          graphTokenBudget `json:"budget"`
	NodeOffset      int              `json:"node_offset"`
	EdgeOffset      int              `json:"edge_offset"`
}

type graphTokenBudget struct {
	MaxDepth         int   `json:"max_depth"`
	MaxVisited       int   `json:"max_visited"`
	MaxNodes         int   `json:"max_nodes"`
	MaxEdges         int   `json:"max_edges"`
	HasDeadline      bool  `json:"has_deadline"`
	DeadlineUnixNano int64 `json:"deadline_unix_nano"`
}

type graphCursor struct {
	nodeOffset int
	edgeOffset int
}

type graphTraversal struct {
	coverage   IndexCoverageState
	stop       QueryGraphStopReason
	nodes      map[string]QueryEntityRef
	edges      []QueryGraphEdge
	unresolved []GraphUnresolvedSite
	parents    map[string]graphPathParent
	foundPath  bool
}

type graphPathParent struct {
	previous QueryEntityRef
	edge     QueryGraphEdge
}
type graphWorkItem struct {
	ref   QueryEntityRef
	depth int
}

// Explore evaluates one graph request inside an already-authorized immutable View.
// Relations are static index evidence only. In particular, calls and may_call never
// assert runtime execution.
func (service *GraphService) Explore(ctx context.Context, authorized AuthorizedContext, spec GraphSpec) (GraphResult, error) {
	if service == nil || service.store == nil {
		return GraphResult{}, fmt.Errorf("uci graph: store is not configured")
	}
	if ctx == nil {
		return GraphResult{}, fmt.Errorf("uci graph: context is required")
	}
	if err := ctx.Err(); err != nil {
		return GraphResult{}, err
	}
	contextRef := authorized.Ref()
	if !contextRef.valid() {
		return GraphResult{}, fmt.Errorf("uci graph: authorized context is invalid")
	}
	normalized, err := normalizeGraphSpec(spec)
	if err != nil {
		return GraphResult{}, err
	}
	cursor, err := service.graphContinuationCursor(contextRef, normalized)
	if err != nil {
		return GraphResult{}, err
	}
	if graphDeadlineExpired(normalized.Budget.Deadline) {
		return graphUnknownResult(IndexCoveragePartial, QueryGraphDeadline), nil
	}

	operationCtx, cancel := graphOperationContext(ctx, normalized.Budget.Deadline)
	defer cancel()

	resolved, expired, err := service.resolveTarget(operationCtx, authorized, normalized.Target)
	if err != nil {
		return GraphResult{}, err
	}
	if expired {
		return graphUnknownResult(IndexCoveragePartial, QueryGraphDeadline), nil
	}
	if len(resolved.Candidates) > 1 {
		return GraphResult{
			Outcome:    GraphOutcomeAmbiguous,
			Semantics:  GraphSemanticsStatic,
			Coverage:   resolved.Coverage,
			Graph:      QueryGraph{Nodes: []QueryEntityRef{}, Edges: []QueryGraphEdge{}, StopReason: QueryGraphComplete},
			Candidates: resolved.Candidates,
			Unresolved: resolved.Unresolved,
		}, nil
	}
	if len(resolved.Candidates) == 0 {
		return graphTargetUnknownResult(resolved), nil
	}

	start := resolved.Candidates[0]
	var destination *QueryEntityRef
	coverage := resolved.Coverage
	unresolved := append([]GraphUnresolvedSite(nil), resolved.Unresolved...)
	if normalized.Action == GraphActionPath {
		resolvedDestination, expired, err := service.resolveTarget(operationCtx, authorized, *normalized.Destination)
		if err != nil {
			return GraphResult{}, err
		}
		if expired {
			return graphUnknownResult(graphCombineCoverage(coverage, IndexCoveragePartial), QueryGraphDeadline), nil
		}
		coverage = graphCombineCoverage(coverage, resolvedDestination.Coverage)
		for _, site := range resolvedDestination.Unresolved {
			graphMergeUnresolved(&unresolved, site)
		}
		graphOrderUnresolved(unresolved)
		if len(resolvedDestination.Candidates) > 1 {
			return GraphResult{
				Outcome:    GraphOutcomeAmbiguous,
				Semantics:  GraphSemanticsStatic,
				Coverage:   coverage,
				Graph:      QueryGraph{Nodes: []QueryEntityRef{}, Edges: []QueryGraphEdge{}, StopReason: QueryGraphComplete},
				Candidates: resolvedDestination.Candidates,
				Unresolved: unresolved,
			}, nil
		}
		if len(resolvedDestination.Candidates) == 0 {
			return graphTargetUnknownResult(GraphTargetResolution{Coverage: coverage, Unresolved: unresolved}), nil
		}
		value := resolvedDestination.Candidates[0]
		destination = &value
	}

	if graphDeadlineExpired(normalized.Budget.Deadline) {
		return graphUnknownResult(graphCombineCoverage(coverage, IndexCoveragePartial), QueryGraphDeadline), nil
	}

	var traversal graphTraversal
	switch normalized.Action {
	case GraphActionExplain, GraphActionNeighbors:
		traversal, err = service.exploreNeighbors(operationCtx, authorized, contextRef, normalized, start, coverage)
	case GraphActionPath, GraphActionImpact, GraphActionFlow, GraphActionCycles:
		traversal, err = service.exploreReachable(operationCtx, authorized, contextRef, normalized, start, destination, coverage)
	default:
		return GraphResult{}, fmt.Errorf("uci graph: action is invalid")
	}
	if err != nil {
		return GraphResult{}, err
	}
	for _, site := range unresolved {
		graphMergeUnresolved(&traversal.unresolved, site)
	}
	graphOrderUnresolved(traversal.unresolved)
	if len(traversal.unresolved) != 0 {
		traversal.mark(QueryGraphCoverageGap)
	}
	if traversal.coverage != IndexCoverageComplete {
		traversal.mark(QueryGraphCoverageGap)
	}

	nodes, edges := graphResultShape(normalized.Action, traversal, start, destination)
	if normalized.Action == GraphActionPath && traversal.foundPath {
		nodes, edges = graphPathShape(traversal, start, *destination)
	}
	return service.graphResult(contextRef, normalized, cursor, traversal, nodes, edges), nil
}

func (service *GraphService) resolveTarget(ctx context.Context, authorized AuthorizedContext, target GraphTarget) (GraphTargetResolution, bool, error) {
	result, err := service.store.ResolveGraphTargets(ctx, authorized, target)
	if err != nil {
		if graphContextDeadlineExpired(ctx) {
			return GraphTargetResolution{}, true, nil
		}
		return GraphTargetResolution{}, false, err
	}
	if graphContextDeadlineExpired(ctx) {
		return GraphTargetResolution{}, true, nil
	}
	clean, err := graphScopedTargetResolution(result, authorized.Ref())
	if err != nil {
		return GraphTargetResolution{}, false, err
	}
	return clean, false, nil
}

func (service *GraphService) exploreNeighbors(ctx context.Context, authorized AuthorizedContext, contextRef ContextRef, spec GraphSpec, start QueryEntityRef, coverage IndexCoverageState) (graphTraversal, error) {
	traversal := newGraphTraversal(coverage)
	traversal.addNode(start)
	if spec.Budget.MaxDepth == 0 {
		traversal.mark(QueryGraphDepthCap)
		return traversal, nil
	}
	if graphContextDeadlineExpired(ctx) {
		traversal.mark(QueryGraphDeadline)
		return traversal, nil
	}
	result, err := service.store.SelectGraphEdges(ctx, authorized, GraphEdgeQuery{
		Nodes:  []QueryEntityRef{start},
		Filter: spec.Filter,
	})
	if err != nil {
		if graphContextDeadlineExpired(ctx) {
			traversal.mark(QueryGraphDeadline)
			return traversal, nil
		}
		return graphTraversal{}, err
	}
	if graphContextDeadlineExpired(ctx) {
		traversal.mark(QueryGraphDeadline)
		return traversal, nil
	}
	if !isIndexCoverageState(result.Coverage) {
		return graphTraversal{}, fmt.Errorf("uci graph: store returned invalid edge coverage %q", result.Coverage)
	}
	traversal.coverage = graphCombineCoverage(traversal.coverage, result.Coverage)
	if traversal.coverage != IndexCoverageComplete {
		traversal.mark(QueryGraphCoverageGap)
	}
	if err := traversal.addStoreResult(graphStoreResultInput{
		result:            result,
		contextRef:        contextRef,
		filter:            spec.Filter,
		queried:           []QueryEntityRef{start},
		maxVisited:        spec.Budget.MaxVisited,
		maxTraversalEdges: graphTraversalEdgeLimit(spec.Budget),
	}); err != nil {
		return graphTraversal{}, err
	}
	return traversal, nil
}

func (service *GraphService) exploreReachable(ctx context.Context, authorized AuthorizedContext, contextRef ContextRef, spec GraphSpec, start QueryEntityRef, destination *QueryEntityRef, coverage IndexCoverageState) (graphTraversal, error) {
	traversal := newGraphTraversal(coverage)
	traversal.addNode(start)
	if destination != nil && graphRefsEqual(start, *destination) {
		traversal.addNode(*destination)
		traversal.foundPath = true
		return traversal, nil
	}

	queue := []graphWorkItem{{ref: start, depth: 0}}
	seen := map[string]struct{}{graphRefKey(start): {}}
	for len(queue) != 0 && !traversal.foundPath {
		if graphContextDeadlineExpired(ctx) {
			traversal.mark(QueryGraphDeadline)
			break
		}
		item := queue[0]
		queue = queue[1:]
		if item.depth >= spec.Budget.MaxDepth {
			traversal.mark(QueryGraphDepthCap)
			continue
		}

		result, err := service.store.SelectGraphEdges(ctx, authorized, GraphEdgeQuery{
			Nodes:  []QueryEntityRef{item.ref},
			Filter: spec.Filter,
		})
		if err != nil {
			if graphContextDeadlineExpired(ctx) {
				traversal.mark(QueryGraphDeadline)
				break
			}
			return graphTraversal{}, err
		}
		if graphContextDeadlineExpired(ctx) {
			traversal.mark(QueryGraphDeadline)
			break
		}
		if !isIndexCoverageState(result.Coverage) {
			return graphTraversal{}, fmt.Errorf("uci graph: store returned invalid edge coverage %q", result.Coverage)
		}
		traversal.coverage = graphCombineCoverage(traversal.coverage, result.Coverage)
		if traversal.coverage != IndexCoverageComplete {
			traversal.mark(QueryGraphCoverageGap)
		}
		if err := traversal.addStoreResult(graphStoreResultInput{
			result:            result,
			contextRef:        contextRef,
			filter:            spec.Filter,
			queried:           []QueryEntityRef{item.ref},
			maxVisited:        spec.Budget.MaxVisited,
			maxTraversalEdges: graphTraversalEdgeLimit(spec.Budget),
			expand:            true,
			queue:             &queue,
			expansion: graphTraversalExpansion{
				seen:        seen,
				depth:       item.depth + 1,
				destination: destination,
			},
		}); err != nil {
			return graphTraversal{}, err
		}
	}
	return traversal, nil
}

type graphTraversalExpansion struct {
	seen        map[string]struct{}
	depth       int
	destination *QueryEntityRef
}

type graphStoreResultInput struct {
	result            GraphStoreResult
	contextRef        ContextRef
	filter            GraphFilter
	queried           []QueryEntityRef
	maxVisited        int
	maxTraversalEdges int
	expand            bool
	queue             *[]graphWorkItem
	expansion         graphTraversalExpansion
}

func graphTraversalEdgeLimit(budget GraphBudget) int {
	if budget.MaxVisited > budget.MaxEdges {
		return budget.MaxVisited
	}
	return budget.MaxEdges
}

func newGraphTraversal(coverage IndexCoverageState) graphTraversal {
	return graphTraversal{
		coverage: coverage,
		stop:     QueryGraphComplete,
		nodes:    make(map[string]QueryEntityRef),
		parents:  make(map[string]graphPathParent),
	}
}

func (traversal *graphTraversal) addStoreResult(input graphStoreResultInput) error {
	if err := traversal.addUnresolved(input); err != nil {
		return err
	}
	edges, err := graphAcceptedEdges(input)
	if err != nil {
		return err
	}
	for _, edge := range edges {
		if err := traversal.addEdge(input, edge); err != nil {
			return err
		}
		if traversal.foundPath {
			break
		}
	}
	return nil
}

func (traversal *graphTraversal) addUnresolved(input graphStoreResultInput) error {
	for _, rawSite := range input.result.Unresolved {
		site, accepted, err := graphScopedUnresolved(rawSite, input.contextRef)
		if err != nil {
			return err
		}
		if !accepted || !graphFilterMatchesUnresolved(site, input.filter) || !graphUnresolvedTouches(site, input.queried, input.filter.Direction) {
			continue
		}
		graphMergeUnresolved(&traversal.unresolved, site)
		traversal.addNode(site.From)
		traversal.mark(QueryGraphCoverageGap)
	}
	return nil
}

func graphAcceptedEdges(input graphStoreResultInput) ([]QueryGraphEdge, error) {
	edges := make([]QueryGraphEdge, 0, len(input.result.Edges))
	for _, rawEdge := range input.result.Edges {
		edge, accepted, err := graphScopedEdge(rawEdge, input.contextRef)
		if err != nil {
			return nil, err
		}
		if !accepted || !graphFilterMatchesEdge(edge, input.filter) || !graphEdgeTouches(edge, input.queried, input.filter.Direction) {
			continue
		}
		edges = append(edges, edge)
	}
	graphOrderEdges(edges)
	return edges, nil
}

func (traversal *graphTraversal) addEdge(input graphStoreResultInput, edge QueryGraphEdge) error {
	newEdge := graphMergeEdge(&traversal.edges, edge)
	if newEdge && len(traversal.edges) > input.maxTraversalEdges {
		traversal.edges = traversal.edges[:len(traversal.edges)-1]
		traversal.mark(QueryGraphNodeCap)
		return nil
	}
	traversal.addNode(edge.From)
	traversal.addNode(edge.To)
	if !input.expand || !newEdge {
		return nil
	}
	if input.queue == nil {
		return fmt.Errorf("uci graph: internal traversal queue is invalid")
	}
	return traversal.enqueueNeighbors(input, edge)
}

func (traversal *graphTraversal) enqueueNeighbors(input graphStoreResultInput, edge QueryGraphEdge) error {
	for _, next := range graphNextRefs(edge, input.queried, input.filter.Direction) {
		if traversal.enqueueNeighbor(input, edge, next) {
			return nil
		}
	}
	return nil
}

func (traversal *graphTraversal) enqueueNeighbor(input graphStoreResultInput, edge QueryGraphEdge, next QueryEntityRef) bool {
	key := graphRefKey(next)
	if _, found := input.expansion.seen[key]; found {
		return false
	}
	if len(input.expansion.seen) >= input.maxVisited {
		traversal.mark(QueryGraphNodeCap)
		return false
	}
	input.expansion.seen[key] = struct{}{}
	traversal.parents[key] = graphPathParent{previous: input.queried[0], edge: edge}
	*input.queue = append(*input.queue, graphWorkItem{ref: next, depth: input.expansion.depth})
	if input.expansion.destination == nil || !graphRefsEqual(next, *input.expansion.destination) {
		return false
	}
	traversal.foundPath = true
	return true
}

func (traversal *graphTraversal) addNode(ref QueryEntityRef) {
	traversal.nodes[graphRefKey(ref)] = ref
}

func (traversal *graphTraversal) mark(reason QueryGraphStopReason) {
	if graphStopPriority(reason) > graphStopPriority(traversal.stop) {
		traversal.stop = reason
	}
}

func graphStopPriority(reason QueryGraphStopReason) int {
	switch reason {
	case QueryGraphDeadline:
		return 4
	case QueryGraphNodeCap:
		return 3
	case QueryGraphDepthCap:
		return 2
	case QueryGraphCoverageGap:
		return 1
	default:
		return 0
	}
}

func graphEdgeTouches(edge QueryGraphEdge, nodes []QueryEntityRef, direction GraphDirection) bool {
	for _, node := range nodes {
		switch direction {
		case GraphDirectionIncoming:
			if graphRefsEqual(edge.To, node) {
				return true
			}
		case GraphDirectionOutgoing:
			if graphRefsEqual(edge.From, node) {
				return true
			}
		case GraphDirectionBoth:
			if graphRefsEqual(edge.From, node) || graphRefsEqual(edge.To, node) {
				return true
			}
		}
	}
	return false
}

func graphUnresolvedTouches(site GraphUnresolvedSite, nodes []QueryEntityRef, direction GraphDirection) bool {
	if direction == GraphDirectionIncoming {
		return false
	}
	for _, node := range nodes {
		if graphRefsEqual(site.From, node) {
			return true
		}
	}
	return false
}

func graphNextRefs(edge QueryGraphEdge, nodes []QueryEntityRef, direction GraphDirection) []QueryEntityRef {
	var next []QueryEntityRef
	for _, node := range nodes {
		switch direction {
		case GraphDirectionIncoming:
			if graphRefsEqual(edge.To, node) {
				next = append(next, edge.From)
			}
		case GraphDirectionOutgoing:
			if graphRefsEqual(edge.From, node) {
				next = append(next, edge.To)
			}
		case GraphDirectionBoth:
			if graphRefsEqual(edge.From, node) {
				next = append(next, edge.To)
			}
			if graphRefsEqual(edge.To, node) {
				next = append(next, edge.From)
			}
		}
	}
	return graphUniqueRefs(next)
}

func graphResultShape(action GraphAction, traversal graphTraversal, start QueryEntityRef, destination *QueryEntityRef) ([]QueryEntityRef, []QueryGraphEdge) {
	if action == GraphActionCycles {
		return graphCycleShape(traversal)
	}
	nodes := make([]QueryEntityRef, 0, len(traversal.nodes)+1)
	for _, node := range traversal.nodes {
		nodes = append(nodes, node)
	}
	if destination != nil {
		nodes = append(nodes, *destination)
	}
	nodes = graphUniqueRefs(nodes)
	edges := append([]QueryGraphEdge(nil), traversal.edges...)
	graphOrderEdges(edges)
	return nodes, edges
}

func graphPathShape(traversal graphTraversal, start, destination QueryEntityRef) ([]QueryEntityRef, []QueryGraphEdge) {
	if graphRefsEqual(start, destination) {
		return []QueryEntityRef{start}, []QueryGraphEdge{}
	}
	var reversed []QueryGraphEdge
	current := destination
	for !graphRefsEqual(current, start) {
		parent, found := traversal.parents[graphRefKey(current)]
		if !found {
			return []QueryEntityRef{start}, []QueryGraphEdge{}
		}
		reversed = append(reversed, parent.edge)
		current = parent.previous
	}
	edges := make([]QueryGraphEdge, len(reversed))
	for index := range reversed {
		edges[len(reversed)-1-index] = reversed[index]
	}
	nodes := []QueryEntityRef{start}
	current = start
	for _, edge := range edges {
		next := graphNextRefs(edge, []QueryEntityRef{current}, GraphDirectionOutgoing)
		if len(next) == 0 {
			next = graphNextRefs(edge, []QueryEntityRef{current}, GraphDirectionIncoming)
		}
		if len(next) == 0 {
			break
		}
		current = next[0]
		nodes = append(nodes, current)
	}
	nodes = graphUniqueRefs(nodes)
	return nodes, edges
}

func graphCycleShape(traversal graphTraversal) ([]QueryEntityRef, []QueryGraphEdge) {
	vertices := make([]QueryEntityRef, 0, len(traversal.nodes))
	for _, node := range traversal.nodes {
		vertices = append(vertices, node)
	}
	vertices = graphUniqueRefs(vertices)
	adjacency := make(map[string][]QueryEntityRef, len(vertices))
	for _, edge := range traversal.edges {
		adjacency[graphRefKey(edge.From)] = append(adjacency[graphRefKey(edge.From)], edge.To)
	}
	for key := range adjacency {
		adjacency[key] = graphUniqueRefs(adjacency[key])
	}

	index := 0
	indexes := make(map[string]int, len(vertices))
	lowlinks := make(map[string]int, len(vertices))
	onStack := make(map[string]bool, len(vertices))
	stack := make([]QueryEntityRef, 0, len(vertices))
	cyclic := make(map[string]bool, len(vertices))
	var visit func(QueryEntityRef)
	visit = func(node QueryEntityRef) {
		key := graphRefKey(node)
		index++
		indexes[key] = index
		lowlinks[key] = index
		stack = append(stack, node)
		onStack[key] = true
		for _, next := range adjacency[key] {
			nextKey := graphRefKey(next)
			if indexes[nextKey] == 0 {
				visit(next)
				if lowlinks[nextKey] < lowlinks[key] {
					lowlinks[key] = lowlinks[nextKey]
				}
			} else if onStack[nextKey] && indexes[nextKey] < lowlinks[key] {
				lowlinks[key] = indexes[nextKey]
			}
		}
		if lowlinks[key] != indexes[key] {
			return
		}
		component := []QueryEntityRef{}
		for {
			last := len(stack) - 1
			member := stack[last]
			stack = stack[:last]
			memberKey := graphRefKey(member)
			onStack[memberKey] = false
			component = append(component, member)
			if graphRefsEqual(member, node) {
				break
			}
		}
		if len(component) > 1 {
			for _, member := range component {
				cyclic[graphRefKey(member)] = true
			}
			return
		}
		member := component[0]
		for _, next := range adjacency[graphRefKey(member)] {
			if graphRefsEqual(member, next) {
				cyclic[graphRefKey(member)] = true
				return
			}
		}
	}
	for _, vertex := range vertices {
		if indexes[graphRefKey(vertex)] == 0 {
			visit(vertex)
		}
	}

	nodes := make([]QueryEntityRef, 0, len(cyclic))
	for _, vertex := range vertices {
		if cyclic[graphRefKey(vertex)] {
			nodes = append(nodes, vertex)
		}
	}
	edges := make([]QueryGraphEdge, 0, len(traversal.edges))
	for _, edge := range traversal.edges {
		if cyclic[graphRefKey(edge.From)] && cyclic[graphRefKey(edge.To)] {
			edges = append(edges, edge)
		}
	}
	graphOrderEdges(edges)
	return nodes, edges
}

func (service *GraphService) graphResult(contextRef ContextRef, spec GraphSpec, cursor graphCursor, traversal graphTraversal, nodes []QueryEntityRef, edges []QueryGraphEdge) GraphResult {
	if cursor.nodeOffset > len(nodes) || cursor.edgeOffset > len(edges) {
		return GraphResult{Outcome: GraphOutcomeUnknownOrTruncated, Semantics: GraphSemanticsStatic, Coverage: IndexCoveragePartial, Graph: QueryGraph{Nodes: []QueryEntityRef{}, Edges: []QueryGraphEdge{}, StopReason: QueryGraphCoverageGap}}
	}
	nodeEnd := cursor.nodeOffset + spec.Budget.MaxNodes
	if nodeEnd > len(nodes) {
		nodeEnd = len(nodes)
	}
	edgeEnd := cursor.edgeOffset + spec.Budget.MaxEdges
	if edgeEnd > len(edges) {
		edgeEnd = len(edges)
	}
	pageTruncated := nodeEnd < len(nodes) || edgeEnd < len(edges)
	stop := traversal.stop
	if pageTruncated && stop == QueryGraphComplete {
		stop = QueryGraphNodeCap
	}
	outcome := graphOutcomeFor(spec.Action, traversal, pageTruncated)
	result := GraphResult{
		Outcome:    outcome,
		Semantics:  GraphSemanticsStatic,
		Coverage:   traversal.coverage,
		Graph:      QueryGraph{Nodes: append([]QueryEntityRef(nil), nodes[cursor.nodeOffset:nodeEnd]...), Edges: append([]QueryGraphEdge(nil), edges[cursor.edgeOffset:edgeEnd]...), StopReason: stop},
		Unresolved: append([]GraphUnresolvedSite(nil), traversal.unresolved...),
	}
	if pageTruncated {
		token, err := service.encodeGraphContinuation(contextRef, spec, graphCursor{nodeOffset: nodeEnd, edgeOffset: edgeEnd})
		if err == nil {
			result.Continuation = &token
		} else {
			result.Outcome = GraphOutcomeUnknownOrTruncated
			result.Graph.StopReason = QueryGraphNodeCap
		}
	}
	return result
}

func graphOutcomeFor(action GraphAction, traversal graphTraversal, pageTruncated bool) GraphOutcome {
	if pageTruncated || traversal.stop != QueryGraphComplete || traversal.coverage != IndexCoverageComplete || len(traversal.unresolved) != 0 {
		return GraphOutcomeUnknownOrTruncated
	}
	switch action {
	case GraphActionPath:
		if !traversal.foundPath {
			return GraphOutcomeNoPath
		}
	case GraphActionImpact:
		if len(traversal.edges) == 0 {
			return GraphOutcomeNoImpact
		}
	}
	return GraphOutcomeComplete
}

func graphTargetUnknownResult(resolved GraphTargetResolution) GraphResult {
	stop := QueryGraphCoverageGap
	coverage := resolved.Coverage
	if coverage == IndexCoverageComplete && len(resolved.Unresolved) == 0 {
		coverage = IndexCoveragePartial
	}
	return GraphResult{
		Outcome:    GraphOutcomeUnknownOrTruncated,
		Semantics:  GraphSemanticsStatic,
		Coverage:   coverage,
		Graph:      QueryGraph{Nodes: []QueryEntityRef{}, Edges: []QueryGraphEdge{}, StopReason: stop},
		Unresolved: append([]GraphUnresolvedSite(nil), resolved.Unresolved...),
	}
}

func graphUnknownResult(coverage IndexCoverageState, stop QueryGraphStopReason) GraphResult {
	return GraphResult{
		Outcome:   GraphOutcomeUnknownOrTruncated,
		Semantics: GraphSemanticsStatic,
		Coverage:  coverage,
		Graph:     QueryGraph{Nodes: []QueryEntityRef{}, Edges: []QueryGraphEdge{}, StopReason: stop},
	}
}

func graphOperationContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}

func graphDeadlineExpired(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}

func graphContextDeadlineExpired(ctx context.Context) bool {
	return ctx.Err() != nil && strings.Contains(ctx.Err().Error(), context.DeadlineExceeded.Error())
}

func (service *GraphService) graphContinuationCursor(contextRef ContextRef, spec GraphSpec) (graphCursor, error) {
	if spec.Continuation == nil {
		return graphCursor{}, nil
	}
	payload, err := service.decodeGraphContinuation(*spec.Continuation)
	if err != nil {
		return graphCursor{}, err
	}
	if !graphContinuationMatches(payload, contextRef, spec) {
		return graphCursor{}, fmt.Errorf("uci graph: continuation binding does not match request")
	}
	return graphCursor{nodeOffset: payload.NodeOffset, edgeOffset: payload.EdgeOffset}, nil
}

func (service *GraphService) encodeGraphContinuation(contextRef ContextRef, spec GraphSpec, cursor graphCursor) (string, error) {
	if cursor.nodeOffset < 0 || cursor.edgeOffset < 0 {
		return "", fmt.Errorf("uci graph: continuation offset is invalid")
	}
	payload := graphContinuationPayloadFor(contextRef, spec, cursor)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("uci graph: encode continuation: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(encoded)
	signature := queryContinuationSignature(service.continuationKey, graphContinuationTokenVersion+"."+body)
	token := graphContinuationTokenVersion + "." + body + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(token) > queryMaxContinuation {
		return "", fmt.Errorf("uci graph: continuation exceeds response bound")
	}
	return token, nil
}

func (service *GraphService) decodeGraphContinuation(token string) (graphContinuationPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != graphContinuationTokenVersion {
		return graphContinuationPayload{}, fmt.Errorf("uci graph: continuation version is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return graphContinuationPayload{}, fmt.Errorf("uci graph: continuation signature is invalid")
	}
	want := queryContinuationSignature(service.continuationKey, parts[0]+"."+parts[1])
	if !hmacEqual(signature, want) {
		return graphContinuationPayload{}, fmt.Errorf("uci graph: continuation integrity check failed")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return graphContinuationPayload{}, fmt.Errorf("uci graph: continuation payload is invalid")
	}
	var payload graphContinuationPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return graphContinuationPayload{}, fmt.Errorf("uci graph: continuation payload is invalid")
	}
	if payload.Version != graphContinuationTokenVersion || payload.NodeOffset < 0 || payload.EdgeOffset < 0 {
		return graphContinuationPayload{}, fmt.Errorf("uci graph: continuation payload is invalid")
	}
	return payload, nil
}

func hmacEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var mismatch byte
	for index := range left {
		mismatch |= left[index] ^ right[index]
	}
	return mismatch == 0
}

func graphContinuationPayloadFor(contextRef ContextRef, spec GraphSpec, cursor graphCursor) graphContinuationPayload {
	var destination *GraphTarget
	if spec.Destination != nil {
		copy := *spec.Destination
		destination = &copy
	}
	return graphContinuationPayload{
		Version:         graphContinuationTokenVersion,
		ClientSessionID: spec.ClientSessionID,
		SpaceID:         cloneQuerySpaceID(contextRef.SpaceID),
		SourceID:        contextRef.SourceID,
		CheckoutID:      contextRef.CheckoutID,
		ViewID:          contextRef.ViewID,
		ProfileID:       contextRef.AnalysisProfileID,
		Generation:      contextRef.Generation,
		Action:          spec.Action,
		Target:          spec.Target,
		Destination:     destination,
		Filter:          spec.Filter,
		Budget: graphTokenBudget{
			MaxDepth:         spec.Budget.MaxDepth,
			MaxVisited:       spec.Budget.MaxVisited,
			MaxNodes:         spec.Budget.MaxNodes,
			MaxEdges:         spec.Budget.MaxEdges,
			HasDeadline:      !spec.Budget.Deadline.IsZero(),
			DeadlineUnixNano: spec.Budget.Deadline.UnixNano(),
		},
		NodeOffset: cursor.nodeOffset,
		EdgeOffset: cursor.edgeOffset,
	}
}

func graphContinuationMatches(payload graphContinuationPayload, contextRef ContextRef, spec GraphSpec) bool {
	expected := graphContinuationPayloadFor(contextRef, spec, graphCursor{nodeOffset: payload.NodeOffset, edgeOffset: payload.EdgeOffset})
	return payload.Version == expected.Version &&
		payload.ClientSessionID == expected.ClientSessionID &&
		queryOptionalStringEqual(payload.SpaceID, expected.SpaceID) &&
		payload.SourceID == expected.SourceID &&
		payload.CheckoutID == expected.CheckoutID &&
		payload.ViewID == expected.ViewID &&
		payload.ProfileID == expected.ProfileID &&
		payload.Generation == expected.Generation &&
		payload.Action == expected.Action &&
		graphTargetEqual(payload.Target, expected.Target) &&
		graphOptionalTargetEqual(payload.Destination, expected.Destination) &&
		graphFiltersEqual(payload.Filter, expected.Filter) &&
		payload.Budget == expected.Budget
}

func graphOptionalTargetEqual(left, right *GraphTarget) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return graphTargetEqual(*left, *right)
}

func graphFiltersEqual(left, right GraphFilter) bool {
	if left.Direction != right.Direction || len(left.Relations) != len(right.Relations) || len(left.EvidenceKinds) != len(right.EvidenceKinds) {
		return false
	}
	for index := range left.Relations {
		if left.Relations[index] != right.Relations[index] {
			return false
		}
	}
	for index := range left.EvidenceKinds {
		if left.EvidenceKinds[index] != right.EvidenceKinds[index] {
			return false
		}
	}
	return true
}
