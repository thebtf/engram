package uci

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	graphMaxDepth               = 64
	graphMaxVisited             = 5_000
	graphMaxFilterRelations     = 32
	graphMaxFilterEvidenceKinds = 4
)

// GraphAction selects a bounded static-graph operation.
type GraphAction string

const (
	GraphActionExplain   GraphAction = "explain"
	GraphActionNeighbors GraphAction = "neighbors"
	GraphActionPath      GraphAction = "path"
	GraphActionImpact    GraphAction = "impact"
	GraphActionFlow      GraphAction = "flow"
	GraphActionCycles    GraphAction = "cycles"
)

// GraphDirection selects how a directed edge is traversed.
type GraphDirection string

const (
	GraphDirectionIncoming GraphDirection = "incoming"
	GraphDirectionOutgoing GraphDirection = "outgoing"
	GraphDirectionBoth     GraphDirection = "both"
)

// GraphSemantics labels the strength of a graph result.
type GraphSemantics string

const (
	GraphSemanticsStatic GraphSemantics = "static"
)

// GraphOutcome reports whether the requested graph conclusion is established.
type GraphOutcome string

const (
	GraphOutcomeComplete           GraphOutcome = "complete"
	GraphOutcomeAmbiguous          GraphOutcome = "ambiguous"
	GraphOutcomeUnknownOrTruncated GraphOutcome = "unknown_or_truncated"
	GraphOutcomeNoPath             GraphOutcome = "no_path"
	GraphOutcomeNoImpact           GraphOutcome = "no_impact"
)

// GraphTarget identifies one entity exactly or one source-local name.
type GraphTarget struct {
	EntityKey string
	Name      string
}

// GraphFilter limits traversal without widening the authorized View.
type GraphFilter struct {
	Direction     GraphDirection
	Relations     []IndexRelation
	EvidenceKinds []QueryEvidenceKind
}

// GraphBudget bounds static graph exploration and response materialization.
type GraphBudget struct {
	MaxDepth   int
	MaxVisited int
	MaxNodes   int
	MaxEdges   int
	Deadline   time.Time
}

// GraphSpec is untrusted graph input presented after context authorization.
type GraphSpec struct {
	ClientSessionID string
	Action          GraphAction
	Target          GraphTarget
	Destination     *GraphTarget
	Filter          GraphFilter
	Budget          GraphBudget
	Continuation    *string
}

// GraphTargetResolution is the store-owned resolution evidence for one target.
type GraphTargetResolution struct {
	Candidates []QueryEntityRef
	Unresolved []GraphUnresolvedSite
	Coverage   IndexCoverageState
}

// GraphUnresolvedSite records a source-grounded unresolved static link.
type GraphUnresolvedSite struct {
	From         QueryEntityRef
	Relation     IndexRelation
	EvidenceRefs []QueryEntityRef
}

// GraphEdgeQuery is one bounded adjacency query inside an authorized View.
type GraphEdgeQuery struct {
	Nodes  []QueryEntityRef
	Filter GraphFilter
}

// GraphStoreResult contains one bounded graph adjacency result.
type GraphStoreResult struct {
	Edges      []QueryGraphEdge
	Unresolved []GraphUnresolvedSite
	Coverage   IndexCoverageState
}

// GraphStore resolves and selects only evidence from the supplied authorized View.
type GraphStore interface {
	ResolveGraphTargets(context.Context, AuthorizedContext, GraphTarget) (GraphTargetResolution, error)
	SelectGraphEdges(context.Context, AuthorizedContext, GraphEdgeQuery) (GraphStoreResult, error)
}

// GraphResult is the service-owned static graph result.
type GraphResult struct {
	Outcome      GraphOutcome
	Semantics    GraphSemantics
	Coverage     IndexCoverageState
	Graph        QueryGraph
	Candidates   []QueryEntityRef
	Unresolved   []GraphUnresolvedSite
	Continuation *string
}

func normalizeGraphSpec(spec GraphSpec) (GraphSpec, error) {
	normalized := GraphSpec{
		ClientSessionID: strings.TrimSpace(spec.ClientSessionID),
		Action:          GraphAction(strings.ToLower(strings.TrimSpace(string(spec.Action)))),
		Budget:          spec.Budget,
	}
	if !validQueryIdentity(normalized.ClientSessionID, queryMaxClientSessionID) {
		return GraphSpec{}, fmt.Errorf("uci graph: client session ID is invalid")
	}
	if !normalized.Action.valid() {
		return GraphSpec{}, fmt.Errorf("uci graph: action is invalid")
	}

	var err error
	if normalized.Target, err = normalizeGraphTarget(spec.Target); err != nil {
		return GraphSpec{}, fmt.Errorf("uci graph: target: %w", err)
	}
	if spec.Destination != nil {
		destination, err := normalizeGraphTarget(*spec.Destination)
		if err != nil {
			return GraphSpec{}, fmt.Errorf("uci graph: destination: %w", err)
		}
		normalized.Destination = &destination
	}
	if normalized.Action == GraphActionPath {
		if normalized.Destination == nil {
			return GraphSpec{}, fmt.Errorf("uci graph: path requires a destination")
		}
	} else if normalized.Destination != nil {
		return GraphSpec{}, fmt.Errorf("uci graph: destination is only valid for path")
	}

	if normalized.Filter, err = normalizeGraphFilter(spec.Filter); err != nil {
		return GraphSpec{}, err
	}
	if !validGraphBudget(normalized.Budget) {
		return GraphSpec{}, fmt.Errorf("uci graph: budget is invalid")
	}
	if !normalized.Budget.Deadline.IsZero() {
		normalized.Budget.Deadline = normalized.Budget.Deadline.UTC()
	}
	if spec.Continuation != nil {
		token := *spec.Continuation
		if len(token) == 0 || len(token) > queryMaxContinuation || !validQueryIdentity(token, queryMaxContinuation) {
			return GraphSpec{}, fmt.Errorf("uci graph: continuation is invalid")
		}
		normalized.Continuation = &token
	}
	return normalized, nil
}

func normalizeGraphTarget(target GraphTarget) (GraphTarget, error) {
	target.EntityKey = strings.TrimSpace(target.EntityKey)
	target.Name = strings.TrimSpace(target.Name)
	if (target.EntityKey == "") == (target.Name == "") {
		return GraphTarget{}, fmt.Errorf("provide exactly one entity key or name")
	}
	if target.EntityKey != "" && !validQueryIdentity(target.EntityKey, queryMaxEntityKey) {
		return GraphTarget{}, fmt.Errorf("entity key is invalid")
	}
	if target.Name != "" && !validQueryIdentity(target.Name, queryMaxEntityKey) {
		return GraphTarget{}, fmt.Errorf("name is invalid")
	}
	return target, nil
}

func normalizeGraphFilter(filter GraphFilter) (GraphFilter, error) {
	normalized := GraphFilter{
		Direction: GraphDirection(strings.ToLower(strings.TrimSpace(string(filter.Direction)))),
	}
	if normalized.Direction == "" {
		normalized.Direction = GraphDirectionBoth
	}
	if !normalized.Direction.valid() {
		return GraphFilter{}, fmt.Errorf("uci graph: direction is invalid")
	}
	if len(filter.Relations) > graphMaxFilterRelations {
		return GraphFilter{}, fmt.Errorf("uci graph: relation filter exceeds limit")
	}
	for _, relation := range filter.Relations {
		relation = IndexRelation(strings.ToLower(strings.TrimSpace(string(relation))))
		if !isIndexRelation(relation) {
			return GraphFilter{}, fmt.Errorf("uci graph: relation filter is invalid")
		}
		normalized.Relations = append(normalized.Relations, relation)
	}
	sort.Slice(normalized.Relations, func(left, right int) bool {
		return normalized.Relations[left] < normalized.Relations[right]
	})
	normalized.Relations = graphUniqueRelations(normalized.Relations)

	if len(filter.EvidenceKinds) > graphMaxFilterEvidenceKinds {
		return GraphFilter{}, fmt.Errorf("uci graph: evidence filter exceeds limit")
	}
	for _, kind := range filter.EvidenceKinds {
		kind = QueryEvidenceKind(strings.ToUpper(strings.TrimSpace(string(kind))))
		if !kind.valid() {
			return GraphFilter{}, fmt.Errorf("uci graph: evidence filter is invalid")
		}
		normalized.EvidenceKinds = append(normalized.EvidenceKinds, kind)
	}
	sort.Slice(normalized.EvidenceKinds, func(left, right int) bool {
		return normalized.EvidenceKinds[left] < normalized.EvidenceKinds[right]
	})
	normalized.EvidenceKinds = graphUniqueEvidenceKinds(normalized.EvidenceKinds)
	return normalized, nil
}

func validGraphBudget(budget GraphBudget) bool {
	return budget.MaxDepth >= 0 && budget.MaxDepth <= graphMaxDepth &&
		budget.MaxVisited >= 1 && budget.MaxVisited <= graphMaxVisited &&
		budget.MaxNodes >= 1 && budget.MaxNodes <= queryMaxGraphNodes &&
		budget.MaxEdges >= 1 && budget.MaxEdges <= queryMaxGraphEdges
}

func (action GraphAction) valid() bool {
	switch action {
	case GraphActionExplain, GraphActionNeighbors, GraphActionPath, GraphActionImpact, GraphActionFlow, GraphActionCycles:
		return true
	default:
		return false
	}
}

func (direction GraphDirection) valid() bool {
	return direction == GraphDirectionIncoming || direction == GraphDirectionOutgoing || direction == GraphDirectionBoth
}

func graphTargetEqual(left, right GraphTarget) bool {
	return left.EntityKey == right.EntityKey && left.Name == right.Name
}

func graphRefsEqual(left, right QueryEntityRef) bool {
	return left.SourceID == right.SourceID && left.ViewID == right.ViewID && left.EntityKey == right.EntityKey
}

func graphRefKey(ref QueryEntityRef) string {
	return ref.SourceID + "\x00" + ref.ViewID + "\x00" + ref.EntityKey
}

func graphScopedRef(ref QueryEntityRef, contextRef ContextRef) (bool, error) {
	if ref.SourceID != contextRef.SourceID || ref.ViewID != contextRef.ViewID {
		return false, nil
	}
	if !validQueryIdentity(ref.EntityKey, queryMaxEntityKey) {
		return false, fmt.Errorf("uci graph: store returned an invalid current-view entity reference")
	}
	return true, nil
}

func graphScopedTargetResolution(result GraphTargetResolution, contextRef ContextRef) (GraphTargetResolution, error) {
	if !isIndexCoverageState(result.Coverage) {
		return GraphTargetResolution{}, fmt.Errorf("uci graph: store returned invalid target coverage %q", result.Coverage)
	}
	clean := GraphTargetResolution{Coverage: result.Coverage}
	for _, candidate := range result.Candidates {
		accepted, err := graphScopedRef(candidate, contextRef)
		if err != nil {
			return GraphTargetResolution{}, err
		}
		if accepted {
			clean.Candidates = append(clean.Candidates, candidate)
		}
	}
	clean.Candidates = graphUniqueRefs(clean.Candidates)
	graphOrderRefs(clean.Candidates)
	for _, unresolved := range result.Unresolved {
		cleaned, accepted, err := graphScopedUnresolved(unresolved, contextRef)
		if err != nil {
			return GraphTargetResolution{}, err
		}
		if accepted {
			clean.Unresolved = append(clean.Unresolved, cleaned)
		}
	}
	graphOrderUnresolved(clean.Unresolved)
	return clean, nil
}

func graphScopedEdge(edge QueryGraphEdge, contextRef ContextRef) (QueryGraphEdge, bool, error) {
	for _, ref := range []QueryEntityRef{edge.From, edge.To} {
		accepted, err := graphScopedRef(ref, contextRef)
		if err != nil {
			return QueryGraphEdge{}, false, err
		}
		if !accepted {
			return QueryGraphEdge{}, false, nil
		}
	}
	if !isIndexRelation(edge.Relation) || !edge.EvidenceKind.valid() || len(edge.EvidenceRefs) == 0 {
		return QueryGraphEdge{}, false, fmt.Errorf("uci graph: store returned invalid current-view edge")
	}
	clean := QueryGraphEdge{
		From:         edge.From,
		To:           edge.To,
		Relation:     edge.Relation,
		EvidenceKind: edge.EvidenceKind,
	}
	for _, evidence := range edge.EvidenceRefs {
		accepted, err := graphScopedRef(evidence, contextRef)
		if err != nil {
			return QueryGraphEdge{}, false, err
		}
		if !accepted {
			return QueryGraphEdge{}, false, nil
		}
		clean.EvidenceRefs = append(clean.EvidenceRefs, evidence)
	}
	clean.EvidenceRefs = graphUniqueRefs(clean.EvidenceRefs)
	graphOrderRefs(clean.EvidenceRefs)
	if edge.Evidence != nil {
		contexts := queryContextSet{queryContextKey{SourceID: contextRef.SourceID, ViewID: contextRef.ViewID}: {}}
		for _, evidence := range edge.Evidence {
			if err := evidence.Validate(contexts); err != nil {
				return QueryGraphEdge{}, false, err
			}
			if !graphContainsRef(clean.EvidenceRefs, evidence.Ref) {
				return QueryGraphEdge{}, false, fmt.Errorf("uci graph: evidence detail is not a current-view evidence reference")
			}
			clean.Evidence = append(clean.Evidence, evidence)
		}
		clean.Evidence = graphUniqueRelationEvidence(clean.Evidence)
	}
	return clean, true, nil
}

func graphScopedUnresolved(site GraphUnresolvedSite, contextRef ContextRef) (GraphUnresolvedSite, bool, error) {
	accepted, err := graphScopedRef(site.From, contextRef)
	if err != nil {
		return GraphUnresolvedSite{}, false, err
	}
	if !accepted {
		return GraphUnresolvedSite{}, false, nil
	}
	if !isIndexRelation(site.Relation) || len(site.EvidenceRefs) == 0 {
		return GraphUnresolvedSite{}, false, fmt.Errorf("uci graph: store returned invalid current-view unresolved site")
	}
	clean := GraphUnresolvedSite{From: site.From, Relation: site.Relation}
	for _, evidence := range site.EvidenceRefs {
		accepted, err := graphScopedRef(evidence, contextRef)
		if err != nil {
			return GraphUnresolvedSite{}, false, err
		}
		if !accepted {
			return GraphUnresolvedSite{}, false, nil
		}
		clean.EvidenceRefs = append(clean.EvidenceRefs, evidence)
	}
	clean.EvidenceRefs = graphUniqueRefs(clean.EvidenceRefs)
	graphOrderRefs(clean.EvidenceRefs)
	return clean, true, nil
}

func graphFilterMatchesEdge(edge QueryGraphEdge, filter GraphFilter) bool {
	return graphFilterHasRelation(filter, edge.Relation) && graphFilterHasEvidenceKind(filter, edge.EvidenceKind)
}

func graphFilterMatchesUnresolved(site GraphUnresolvedSite, filter GraphFilter) bool {
	return graphFilterHasRelation(filter, site.Relation)
}

func graphFilterHasRelation(filter GraphFilter, relation IndexRelation) bool {
	return len(filter.Relations) == 0 || graphContainsRelation(filter.Relations, relation)
}

func graphFilterHasEvidenceKind(filter GraphFilter, kind QueryEvidenceKind) bool {
	return len(filter.EvidenceKinds) == 0 || graphContainsEvidenceKind(filter.EvidenceKinds, kind)
}

func graphContainsRelation(relations []IndexRelation, want IndexRelation) bool {
	for _, relation := range relations {
		if relation == want {
			return true
		}
	}
	return false
}

func graphContainsEvidenceKind(kinds []QueryEvidenceKind, want QueryEvidenceKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func graphUniqueRelations(relations []IndexRelation) []IndexRelation {
	if len(relations) == 0 {
		return nil
	}
	unique := relations[:0]
	for _, relation := range relations {
		if len(unique) == 0 || unique[len(unique)-1] != relation {
			unique = append(unique, relation)
		}
	}
	return unique
}

func graphUniqueEvidenceKinds(kinds []QueryEvidenceKind) []QueryEvidenceKind {
	if len(kinds) == 0 {
		return nil
	}
	unique := kinds[:0]
	for _, kind := range kinds {
		if len(unique) == 0 || unique[len(unique)-1] != kind {
			unique = append(unique, kind)
		}
	}
	return unique
}

func graphUniqueRefs(refs []QueryEntityRef) []QueryEntityRef {
	if len(refs) == 0 {
		return nil
	}
	byKey := make(map[string]QueryEntityRef, len(refs))
	for _, ref := range refs {
		byKey[graphRefKey(ref)] = ref
	}
	unique := make([]QueryEntityRef, 0, len(byKey))
	for _, ref := range byKey {
		unique = append(unique, ref)
	}
	graphOrderRefs(unique)
	return unique
}

func graphOrderRefs(refs []QueryEntityRef) {
	sort.Slice(refs, func(left, right int) bool {
		if refs[left].EntityKey != refs[right].EntityKey {
			return refs[left].EntityKey < refs[right].EntityKey
		}
		if refs[left].SourceID != refs[right].SourceID {
			return refs[left].SourceID < refs[right].SourceID
		}
		return refs[left].ViewID < refs[right].ViewID
	})
}

func graphContainsRef(refs []QueryEntityRef, want QueryEntityRef) bool {
	for _, ref := range refs {
		if graphRefsEqual(ref, want) {
			return true
		}
	}
	return false
}

func graphUniqueRelationEvidence(values []QueryRelationEvidence) []QueryRelationEvidence {
	if values == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	unique := make([]QueryRelationEvidence, 0, len(values))
	for _, value := range values {
		key := value.key()
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, value)
	}
	sort.Slice(unique, func(left, right int) bool { return unique[left].key() < unique[right].key() })
	return unique
}

func graphEdgeKey(edge QueryGraphEdge) string {
	return graphRefKey(edge.From) + "\x00" + graphRefKey(edge.To) + "\x00" + string(edge.Relation) + "\x00" + string(edge.EvidenceKind)
}

func graphMergeEdge(edges *[]QueryGraphEdge, candidate QueryGraphEdge) bool {
	key := graphEdgeKey(candidate)
	for index := range *edges {
		if graphEdgeKey((*edges)[index]) != key {
			continue
		}
		(*edges)[index].EvidenceRefs = graphUniqueRefs(append((*edges)[index].EvidenceRefs, candidate.EvidenceRefs...))
		(*edges)[index].Evidence = graphUniqueRelationEvidence(append((*edges)[index].Evidence, candidate.Evidence...))
		return false
	}
	*edges = append(*edges, candidate)
	return true
}

func graphOrderEdges(edges []QueryGraphEdge) {
	for index := range edges {
		edges[index].EvidenceRefs = graphUniqueRefs(edges[index].EvidenceRefs)
		edges[index].Evidence = graphUniqueRelationEvidence(edges[index].Evidence)
	}
	sort.Slice(edges, func(left, right int) bool {
		if edges[left].From.EntityKey != edges[right].From.EntityKey {
			return edges[left].From.EntityKey < edges[right].From.EntityKey
		}
		if edges[left].To.EntityKey != edges[right].To.EntityKey {
			return edges[left].To.EntityKey < edges[right].To.EntityKey
		}
		if edges[left].Relation != edges[right].Relation {
			return edges[left].Relation < edges[right].Relation
		}
		if edges[left].EvidenceKind != edges[right].EvidenceKind {
			return edges[left].EvidenceKind < edges[right].EvidenceKind
		}
		return graphEdgeKey(edges[left]) < graphEdgeKey(edges[right])
	})
}

func graphUnresolvedKey(site GraphUnresolvedSite) string {
	return graphRefKey(site.From) + "\x00" + string(site.Relation)
}

func graphMergeUnresolved(sites *[]GraphUnresolvedSite, candidate GraphUnresolvedSite) bool {
	key := graphUnresolvedKey(candidate)
	for index := range *sites {
		if graphUnresolvedKey((*sites)[index]) != key {
			continue
		}
		(*sites)[index].EvidenceRefs = graphUniqueRefs(append((*sites)[index].EvidenceRefs, candidate.EvidenceRefs...))
		return false
	}
	*sites = append(*sites, candidate)
	return true
}

func graphOrderUnresolved(sites []GraphUnresolvedSite) {
	for index := range sites {
		sites[index].EvidenceRefs = graphUniqueRefs(sites[index].EvidenceRefs)
	}
	sort.Slice(sites, func(left, right int) bool {
		if sites[left].From.EntityKey != sites[right].From.EntityKey {
			return sites[left].From.EntityKey < sites[right].From.EntityKey
		}
		if sites[left].Relation != sites[right].Relation {
			return sites[left].Relation < sites[right].Relation
		}
		return graphUnresolvedKey(sites[left]) < graphUnresolvedKey(sites[right])
	})
}

func graphCombineCoverage(left, right IndexCoverageState) IndexCoverageState {
	if left == IndexCoverageUnavailable || right == IndexCoverageUnavailable {
		return IndexCoverageUnavailable
	}
	if left == IndexCoveragePartial || right == IndexCoveragePartial {
		return IndexCoveragePartial
	}
	return IndexCoverageComplete
}
