// Package graph provides typed knowledge graph operations over engram memories.
package graph

// Edge type constants for the knowledge graph.
const (
	EdgeUses          = "uses"
	EdgeDependsOn     = "depends_on"
	EdgeSupersedes    = "supersedes"
	EdgeContradicts   = "contradicts"
	EdgeCaused        = "caused"
	EdgeFixedBy       = "fixed_by"
	EdgeLearnedFrom   = "learned_from"
	EdgePromotedTo    = "promoted_to"
	EdgeBelongsTo     = "belongs_to"
	EdgeImports       = "imports"
	EdgeModifies      = "modifies"
	EdgeBlockedBy     = "blocked_by"
	EdgeAvoids        = "avoids"
	EdgeSucceededBy   = "succeeded_by"
	EdgeSynonymOf     = "synonym_of"
	EdgeSameConceptAs = "same_concept_as"
)

var validEdgeTypes = map[string]bool{
	EdgeUses: true, EdgeDependsOn: true, EdgeSupersedes: true,
	EdgeContradicts: true, EdgeCaused: true, EdgeFixedBy: true,
	EdgeLearnedFrom: true, EdgePromotedTo: true, EdgeBelongsTo: true,
	EdgeImports: true, EdgeModifies: true, EdgeBlockedBy: true,
	EdgeAvoids: true, EdgeSucceededBy: true, EdgeSynonymOf: true,
	EdgeSameConceptAs: true,
}

// ValidEdgeType returns true if t is a recognized edge type.
func ValidEdgeType(t string) bool {
	return validEdgeTypes[t]
}

// TraversalResult is a single edge returned from a graph traversal.
type TraversalResult struct {
	EdgeID    int64   `json:"edge_id"`
	SourceID  int64   `json:"source_id"`
	TargetID  int64   `json:"target_id"`
	EdgeType  string  `json:"edge_type"`
	Weight    float64 `json:"weight"`
	Reasoning string  `json:"reasoning,omitempty"`
	Depth     int     `json:"depth"`
}
