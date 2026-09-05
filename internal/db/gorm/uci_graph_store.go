package gorm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lib/pq"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

// The graph port has no per-call row limit. Bound one adjacency lookup at the
// same maximum number of vertices that the graph service may visit, and mark
// the result partial if the bound is reached rather than claiming completeness.
const uciGraphStoreAdjacencyLimit = 5_000

var _ ucidomain.GraphStore = (*UCIProjectionStore)(nil)

// ResolveGraphTargets resolves one exact key or local name only inside the
// caller's already-authorized immutable View. Historical superseded Views are
// intentionally eligible; retired, staging, stale, and mismatched tuples are
// closed as unavailable without exposing candidate data.
func (s *UCIProjectionStore) ResolveGraphTargets(ctx context.Context, authorized ucidomain.AuthorizedContext, target ucidomain.GraphTarget) (ucidomain.GraphTargetResolution, error) {
	if err := s.requireDB("resolve graph targets"); err != nil {
		return ucidomain.GraphTargetResolution{}, err
	}
	if ctx == nil {
		return ucidomain.GraphTargetResolution{}, fmt.Errorf("uci graph: context is required")
	}
	if err := ctx.Err(); err != nil {
		return ucidomain.GraphTargetResolution{}, err
	}

	ref := authorized.Ref()
	if err := validateUCIQueryContext(ref); err != nil {
		return ucidomain.GraphTargetResolution{}, err
	}
	var err error
	target, err = normalizeUCIGraphStoreTarget(target)
	if err != nil {
		return ucidomain.GraphTargetResolution{}, err
	}

	coverage, available, err := s.loadUCIGraphCoverage(ctx, ref)
	if err != nil {
		return ucidomain.GraphTargetResolution{}, err
	}
	if !available {
		return ucidomain.GraphTargetResolution{Coverage: ucidomain.IndexCoverageUnavailable}, nil
	}

	query, arguments := buildUCIGraphTargetsSQL(ref, target)
	var rows []uciGraphTargetRow
	if err := s.db.WithContext(ctx).Raw(query, arguments...).Scan(&rows).Error; err != nil {
		return ucidomain.GraphTargetResolution{}, fmt.Errorf("uci projection resolve graph targets: %w", err)
	}

	result := ucidomain.GraphTargetResolution{Coverage: coverage}
	for _, row := range rows {
		if !validUCIGraphStoreIdentity(row.EntityKey) {
			continue
		}
		result.Candidates = append(result.Candidates, ucidomain.QueryEntityRef{
			SourceID:  ref.SourceID,
			ViewID:    ref.ViewID,
			EntityKey: row.EntityKey,
		})
	}
	return result, nil
}

// SelectGraphEdges selects resolved and unresolved source-grounded evidence
// only from the caller's selected View. The selected-view CTE scopes every
// membership, artifact, and edge interval before relation, evidence, direction,
// ordering, or row-bound predicates are applied.
func (s *UCIProjectionStore) SelectGraphEdges(ctx context.Context, authorized ucidomain.AuthorizedContext, input ucidomain.GraphEdgeQuery) (ucidomain.GraphStoreResult, error) {
	if err := s.requireDB("select graph edges"); err != nil {
		return ucidomain.GraphStoreResult{}, err
	}
	if ctx == nil {
		return ucidomain.GraphStoreResult{}, fmt.Errorf("uci graph: context is required")
	}
	if err := ctx.Err(); err != nil {
		return ucidomain.GraphStoreResult{}, err
	}

	ref := authorized.Ref()
	if err := validateUCIQueryContext(ref); err != nil {
		return ucidomain.GraphStoreResult{}, err
	}
	filter, err := normalizeUCIGraphStoreFilter(input.Filter)
	if err != nil {
		return ucidomain.GraphStoreResult{}, err
	}

	coverage, available, err := s.loadUCIGraphCoverage(ctx, ref)
	if err != nil {
		return ucidomain.GraphStoreResult{}, err
	}
	if !available {
		return ucidomain.GraphStoreResult{Coverage: ucidomain.IndexCoverageUnavailable}, nil
	}

	nodes := uciGraphStoreScopedNodeKeys(ref, input.Nodes)
	if len(nodes) == 0 {
		return ucidomain.GraphStoreResult{Coverage: coverage}, nil
	}

	query, arguments := buildUCIGraphEdgesSQL(ref, nodes, filter)
	var rows []uciGraphEdgeRow
	if err := s.db.WithContext(ctx).Raw(query, arguments...).Scan(&rows).Error; err != nil {
		return ucidomain.GraphStoreResult{}, fmt.Errorf("uci projection select graph edges: %w", err)
	}

	result := ucidomain.GraphStoreResult{Coverage: coverage}
	if len(rows) > uciGraphStoreAdjacencyLimit {
		rows = rows[:uciGraphStoreAdjacencyLimit]
		result.Coverage = ucidomain.IndexCoveragePartial
	}
	for _, row := range rows {
		from, ok := uciGraphStoreEntityRef(ref, row.SourceEntityKey)
		if !ok || !isUCIGraphStoreRelation(row.Relation) {
			continue
		}
		if !row.Resolved {
			result.Unresolved = appendUCIGraphStoreUnresolved(result.Unresolved, ucidomain.GraphUnresolvedSite{
				From:         from,
				Relation:     ucidomain.IndexRelation(row.Relation),
				EvidenceRefs: []ucidomain.QueryEntityRef{from},
			})
			continue
		}

		to, ok := uciGraphStoreEntityRef(ref, row.TargetEntityKey)
		if !ok {
			continue
		}
		evidence, ok := uciGraphStoreEvidenceKind(row.EvidenceKind)
		if !ok {
			continue
		}
		result.Edges = appendUCIGraphStoreEdge(result.Edges, ucidomain.QueryGraphEdge{
			From:         from,
			To:           to,
			Relation:     ucidomain.IndexRelation(row.Relation),
			EvidenceKind: evidence,
			EvidenceRefs: []ucidomain.QueryEntityRef{from},
		})
	}
	sortUCIGraphStoreEdges(result.Edges)
	sortUCIGraphStoreUnresolved(result.Unresolved)
	return result, nil
}

type uciGraphTargetRow struct {
	EntityKey string `gorm:"column:entity_key"`
}

type uciGraphEdgeRow struct {
	Resolved        bool   `gorm:"column:resolved"`
	ResolvedEdgeID  string `gorm:"column:resolved_edge_id"`
	SourceEntityKey string `gorm:"column:source_entity_key"`
	TargetEntityKey string `gorm:"column:target_entity_key"`
	Relation        string `gorm:"column:relation"`
	EvidenceKind    string `gorm:"column:evidence_kind"`
}

func (s *UCIProjectionStore) loadUCIGraphCoverage(ctx context.Context, ref ucidomain.ContextRef) (ucidomain.IndexCoverageState, bool, error) {
	metadata, found, err := s.loadUCIQueryViewMetadata(ctx, ref)
	if err != nil {
		return "", false, err
	}
	if !found || (metadata.State != UCIViewPublished && metadata.State != UCIViewSuperseded) {
		return ucidomain.IndexCoverageUnavailable, false, nil
	}
	coverage, ok := parseUCIQueryCoverage(metadata.Structural)
	if !ok || coverage == ucidomain.IndexCoverageUnavailable {
		return ucidomain.IndexCoverageUnavailable, false, nil
	}
	return coverage, true, nil
}

func buildUCIGraphTargetsSQL(ref ucidomain.ContextRef, target ucidomain.GraphTarget) (string, []any) {
	arguments := []any{
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
		UCIFilePresent,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
	}
	predicate := "entity_key = ?"
	if target.Name != "" {
		predicate = "local_name = ?"
		arguments = append(arguments, target.Name)
	} else {
		arguments = append(arguments, target.EntityKey)
	}
	return `
		WITH selected_view AS (
			SELECT
				view_row.source_id,
				view_row.checkout_id,
				view_row.generation,
				profile.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
			WHERE view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.checkout_id = ?
				AND view_row.profile_id = ?
				AND view_row.generation = ?
				AND view_row.state IN (?, ?)
		),
		scoped_entities AS (
			SELECT DISTINCT
				COALESCE(
					NULLIF(definition.qualified_local_name, ''),
					NULLIF(chunk.symbol_key, ''),
					membership.path_key
				) AS entity_key,
				COALESCE(definition.name, '') AS local_name
			FROM selected_view AS view_row
			JOIN ci_memberships AS membership
				ON membership.source_id = view_row.source_id
				AND membership.checkout_id = view_row.checkout_id
				AND membership.valid_from_generation <= view_row.generation
				AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view_row.generation)
				AND membership.file_state = ?
			JOIN ci_parse_artifacts AS artifact
				ON artifact.source_id = membership.source_id
				AND artifact.artifact_id = membership.artifact_id
				AND artifact.extraction_profile_digest = view_row.parser_bundle_digest
				AND artifact.status IN (?, ?)
				AND artifact.sealed_at IS NOT NULL
				AND artifact.facts_digest IS NOT NULL
			JOIN ci_chunks AS chunk
				ON chunk.source_id = artifact.source_id
				AND chunk.artifact_id = artifact.artifact_id
			LEFT JOIN ci_definitions AS definition
				ON definition.artifact_id = chunk.artifact_id
				AND definition.local_symbol_key = chunk.symbol_key
		)
		SELECT entity_key
		FROM scoped_entities
		WHERE ` + predicate + `
		ORDER BY entity_key ASC`, arguments
}

func buildUCIGraphEdgesSQL(ref ucidomain.ContextRef, nodes []string, filter ucidomain.GraphFilter) (string, []any) {
	arguments := []any{
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
		pq.Array(nodes),
		UCIFilePresent,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
		UCIResolvedEdgeResolved,
		UCIFilePresent,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
		UCIResolvedEdgeResolved,
		UCIResolvedEdgeEvidenceExtracted,
		UCIResolvedEdgeEvidenceResolved,
		UCIResolvedEdgeEvidenceHeuristic,
		UCIResolvedEdgeEvidenceSemantic,
	}

	relationCondition, relationArguments := uciGraphStoreFilterCondition("edge.relation", graphStoreRelationStrings(filter.Relations))
	evidenceCondition, evidenceArguments := uciGraphStoreFilterCondition("edge.evidence_kind", uciGraphStoreEvidenceStrings(filter.EvidenceKinds))
	arguments = append(arguments, relationArguments...)
	arguments = append(arguments, evidenceArguments...)
	arguments = append(arguments, UCIResolvedEdgeResolved, UCIResolvedEdgeEvidenceUnresolved)
	arguments = append(arguments, relationArguments...)
	arguments = append(arguments, uciGraphStoreAdjacencyLimit+1)

	resolvedNodeJoin := "edge.source_entity_key = node.entity_key"
	unresolvedNodeJoin := "edge.source_entity_key = node.entity_key"
	unresolvedCondition := "TRUE"
	switch filter.Direction {
	case ucidomain.GraphDirectionIncoming:
		resolvedNodeJoin = "edge.target_entity_key = node.entity_key"
		unresolvedCondition = "FALSE"
	case ucidomain.GraphDirectionBoth:
		resolvedNodeJoin = "(edge.source_entity_key = node.entity_key OR edge.target_entity_key = node.entity_key)"
	}

	return `
		WITH selected_view AS (
			SELECT
				view_row.source_id,
				view_row.checkout_id,
				view_row.generation,
				profile.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
			WHERE view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.checkout_id = ?
				AND view_row.profile_id = ?
				AND view_row.generation = ?
				AND view_row.state IN (?, ?)
		),
		selected_nodes AS (
			SELECT DISTINCT node.entity_key
			FROM unnest(?::text[]) AS node(entity_key)
		),
		scoped_edges AS (
			SELECT
				edge.resolved_edge_id,
				edge.relation,
				edge.evidence_kind,
				edge.resolution_state,
				COALESCE(
					NULLIF(source_definition.qualified_local_name, ''),
					NULLIF(edge.source_symbol, ''),
					source_membership.path_key
				) AS source_entity_key,
				CASE WHEN target_artifact.artifact_id IS NOT NULL THEN COALESCE(
					NULLIF(target_definition.qualified_local_name, ''),
					NULLIF(edge.target_symbol, ''),
					target_membership.path_key
				) END AS target_entity_key,
				target_artifact.artifact_id AS target_artifact_id
			FROM selected_view AS view_row
			JOIN ci_resolved_edges AS edge
				ON edge.source_id = view_row.source_id
				AND edge.checkout_id = view_row.checkout_id
				AND edge.valid_from_generation <= view_row.generation
				AND (edge.valid_to_generation IS NULL OR edge.valid_to_generation > view_row.generation)
			JOIN ci_memberships AS source_membership
				ON source_membership.source_id = view_row.source_id
				AND source_membership.checkout_id = view_row.checkout_id
				AND source_membership.path_key = edge.source_path
				AND source_membership.artifact_id = edge.source_artifact
				AND source_membership.valid_from_generation <= view_row.generation
				AND (source_membership.valid_to_generation IS NULL OR source_membership.valid_to_generation > view_row.generation)
				AND source_membership.file_state = ?
			JOIN ci_parse_artifacts AS source_artifact
				ON source_artifact.source_id = view_row.source_id
				AND source_artifact.artifact_id = edge.source_artifact
				AND source_artifact.extraction_profile_digest = view_row.parser_bundle_digest
				AND source_artifact.status IN (?, ?)
				AND source_artifact.sealed_at IS NOT NULL
				AND source_artifact.facts_digest IS NOT NULL
			LEFT JOIN ci_definitions AS source_definition
				ON source_definition.artifact_id = source_artifact.artifact_id
				AND source_definition.local_symbol_key = edge.source_symbol
			LEFT JOIN ci_memberships AS target_membership
				ON edge.resolution_state = ?
				AND target_membership.source_id = view_row.source_id
				AND target_membership.checkout_id = view_row.checkout_id
				AND target_membership.path_key = edge.target_path
				AND target_membership.artifact_id = edge.target_artifact
				AND target_membership.valid_from_generation <= view_row.generation
				AND (target_membership.valid_to_generation IS NULL OR target_membership.valid_to_generation > view_row.generation)
				AND target_membership.file_state = ?
			LEFT JOIN ci_parse_artifacts AS target_artifact
				ON target_artifact.source_id = view_row.source_id
				AND target_artifact.artifact_id = target_membership.artifact_id
				AND target_artifact.extraction_profile_digest = view_row.parser_bundle_digest
				AND target_artifact.status IN (?, ?)
				AND target_artifact.sealed_at IS NOT NULL
				AND target_artifact.facts_digest IS NOT NULL
			LEFT JOIN ci_definitions AS target_definition
				ON target_definition.artifact_id = target_artifact.artifact_id
				AND target_definition.local_symbol_key = edge.target_symbol
		),
		filtered_edges AS (
			SELECT
				TRUE AS resolved,
				edge.resolved_edge_id,
				edge.source_entity_key,
				edge.target_entity_key,
				edge.relation,
				edge.evidence_kind
			FROM scoped_edges AS edge
			JOIN selected_nodes AS node ON ` + resolvedNodeJoin + `
			WHERE edge.resolution_state = ?
				AND edge.target_artifact_id IS NOT NULL
				AND edge.evidence_kind IN (?, ?, ?, ?)
				AND ` + relationCondition + `
				AND ` + evidenceCondition + `
			UNION ALL
			SELECT
				FALSE AS resolved,
				edge.resolved_edge_id,
				edge.source_entity_key,
				NULL::text AS target_entity_key,
				edge.relation,
				edge.evidence_kind
			FROM scoped_edges AS edge
			JOIN selected_nodes AS node ON ` + unresolvedNodeJoin + `
			WHERE ` + unresolvedCondition + `
				AND (edge.resolution_state <> ? OR edge.evidence_kind = ? OR edge.target_artifact_id IS NULL)
				AND ` + relationCondition + `
		)
		SELECT resolved, resolved_edge_id, source_entity_key, target_entity_key, relation, evidence_kind
		FROM filtered_edges
		ORDER BY resolved DESC, source_entity_key ASC, target_entity_key ASC NULLS LAST, relation ASC, evidence_kind ASC, resolved_edge_id ASC
		LIMIT ?`, arguments
}

func normalizeUCIGraphStoreTarget(target ucidomain.GraphTarget) (ucidomain.GraphTarget, error) {
	if (target.EntityKey == "") == (target.Name == "") {
		return ucidomain.GraphTarget{}, fmt.Errorf("uci graph: provide exactly one entity key or name")
	}
	if target.EntityKey != "" && !validUCIGraphStoreIdentity(target.EntityKey) {
		return ucidomain.GraphTarget{}, fmt.Errorf("uci graph: entity key is invalid")
	}
	if target.Name != "" && !validUCIGraphStoreIdentity(target.Name) {
		return ucidomain.GraphTarget{}, fmt.Errorf("uci graph: name is invalid")
	}
	return target, nil
}

func normalizeUCIGraphStoreFilter(filter ucidomain.GraphFilter) (ucidomain.GraphFilter, error) {
	normalized := ucidomain.GraphFilter{Direction: ucidomain.GraphDirection(strings.ToLower(strings.TrimSpace(string(filter.Direction))))}
	if normalized.Direction == "" {
		normalized.Direction = ucidomain.GraphDirectionBoth
	}
	switch normalized.Direction {
	case ucidomain.GraphDirectionIncoming, ucidomain.GraphDirectionOutgoing, ucidomain.GraphDirectionBoth:
	default:
		return ucidomain.GraphFilter{}, fmt.Errorf("uci graph: direction is invalid")
	}
	if len(filter.Relations) > 32 {
		return ucidomain.GraphFilter{}, fmt.Errorf("uci graph: relation filter exceeds limit")
	}
	for _, relation := range filter.Relations {
		relation = ucidomain.IndexRelation(strings.ToLower(strings.TrimSpace(string(relation))))
		if !isUCIGraphStoreRelation(string(relation)) {
			return ucidomain.GraphFilter{}, fmt.Errorf("uci graph: relation filter is invalid")
		}
		normalized.Relations = append(normalized.Relations, relation)
	}
	sort.Slice(normalized.Relations, func(left, right int) bool { return normalized.Relations[left] < normalized.Relations[right] })
	normalized.Relations = uniqueUCIGraphStoreRelations(normalized.Relations)

	if len(filter.EvidenceKinds) > 4 {
		return ucidomain.GraphFilter{}, fmt.Errorf("uci graph: evidence filter exceeds limit")
	}
	for _, evidence := range filter.EvidenceKinds {
		canonical, ok := uciGraphStoreEvidenceKind(strings.ToLower(strings.TrimSpace(string(evidence))))
		if !ok {
			return ucidomain.GraphFilter{}, fmt.Errorf("uci graph: evidence filter is invalid")
		}
		normalized.EvidenceKinds = append(normalized.EvidenceKinds, canonical)
	}
	sort.Slice(normalized.EvidenceKinds, func(left, right int) bool { return normalized.EvidenceKinds[left] < normalized.EvidenceKinds[right] })
	normalized.EvidenceKinds = uniqueUCIGraphStoreEvidenceKinds(normalized.EvidenceKinds)
	return normalized, nil
}

func uciGraphStoreScopedNodeKeys(ref ucidomain.ContextRef, nodes []ucidomain.QueryEntityRef) []string {
	unique := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.SourceID != ref.SourceID || node.ViewID != ref.ViewID || !validUCIGraphStoreIdentity(node.EntityKey) {
			continue
		}
		unique[node.EntityKey] = struct{}{}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uciGraphStoreEntityRef(ref ucidomain.ContextRef, entityKey string) (ucidomain.QueryEntityRef, bool) {
	if !validUCIGraphStoreIdentity(entityKey) {
		return ucidomain.QueryEntityRef{}, false
	}
	return ucidomain.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: entityKey}, true
}

func uciGraphStoreFilterCondition(column string, values []string) (string, []any) {
	if len(values) == 0 {
		return "TRUE", nil
	}
	placeholders := make([]string, len(values))
	arguments := make([]any, len(values))
	for index, value := range values {
		placeholders[index] = "?"
		arguments[index] = value
	}
	return column + " IN (" + strings.Join(placeholders, ", ") + ")", arguments
}

func graphStoreRelationStrings(relations []ucidomain.IndexRelation) []string {
	values := make([]string, len(relations))
	for index, relation := range relations {
		values[index] = string(relation)
	}
	return values
}

func uciGraphStoreEvidenceStrings(evidenceKinds []ucidomain.QueryEvidenceKind) []string {
	values := make([]string, len(evidenceKinds))
	for index, evidence := range evidenceKinds {
		values[index] = strings.ToLower(string(evidence))
	}
	return values
}

func uciGraphStoreEvidenceKind(value string) (ucidomain.QueryEvidenceKind, bool) {
	switch strings.ToLower(value) {
	case string(UCIResolvedEdgeEvidenceExtracted):
		return ucidomain.QueryEvidenceExtracted, true
	case string(UCIResolvedEdgeEvidenceResolved):
		return ucidomain.QueryEvidenceResolved, true
	case string(UCIResolvedEdgeEvidenceHeuristic):
		return ucidomain.QueryEvidenceHeuristic, true
	case string(UCIResolvedEdgeEvidenceSemantic):
		return ucidomain.QueryEvidenceSemantic, true
	default:
		return "", false
	}
}

func isUCIGraphStoreRelation(value string) bool {
	switch ucidomain.IndexRelation(value) {
	case "contains", "imports", "exports", "references", "calls", "may_call", "inherits", "implements", "documents", "mentions", "configures", "schema_references", "tests", "depends_on":
		return true
	default:
		return false
	}
}

func validUCIGraphStoreIdentity(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func uniqueUCIGraphStoreRelations(relations []ucidomain.IndexRelation) []ucidomain.IndexRelation {
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

func uniqueUCIGraphStoreEvidenceKinds(evidence []ucidomain.QueryEvidenceKind) []ucidomain.QueryEvidenceKind {
	if len(evidence) == 0 {
		return nil
	}
	unique := evidence[:0]
	for _, kind := range evidence {
		if len(unique) == 0 || unique[len(unique)-1] != kind {
			unique = append(unique, kind)
		}
	}
	return unique
}

func appendUCIGraphStoreEdge(edges []ucidomain.QueryGraphEdge, candidate ucidomain.QueryGraphEdge) []ucidomain.QueryGraphEdge {
	for _, existing := range edges {
		if existing.From == candidate.From && existing.To == candidate.To && existing.Relation == candidate.Relation && existing.EvidenceKind == candidate.EvidenceKind {
			return edges
		}
	}
	return append(edges, candidate)
}

func appendUCIGraphStoreUnresolved(sites []ucidomain.GraphUnresolvedSite, candidate ucidomain.GraphUnresolvedSite) []ucidomain.GraphUnresolvedSite {
	for _, existing := range sites {
		if existing.From == candidate.From && existing.Relation == candidate.Relation {
			return sites
		}
	}
	return append(sites, candidate)
}

func sortUCIGraphStoreEdges(edges []ucidomain.QueryGraphEdge) {
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
		return edges[left].EvidenceKind < edges[right].EvidenceKind
	})
}

func sortUCIGraphStoreUnresolved(sites []ucidomain.GraphUnresolvedSite) {
	sort.Slice(sites, func(left, right int) bool {
		if sites[left].From.EntityKey != sites[right].From.EntityKey {
			return sites[left].From.EntityKey < sites[right].From.EntityKey
		}
		return sites[left].Relation < sites[right].Relation
	})
}
