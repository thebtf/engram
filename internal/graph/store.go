package graph

import (
	"context"

	"github.com/thebtf/engram/pkg/models"
)

// GraphStore provides persistent graph operations for observation relations.
// This interface uses models.RelationType (string), NOT graph.RelationType (int)
// which is an unrelated CSR-internal enum for in-memory edge detection.
type GraphStore interface {
	// Ping checks connectivity to the graph backend.
	Ping(ctx context.Context) error

	// StoreEdge stores a single relation as a graph edge.
	StoreEdge(ctx context.Context, edge RelationEdge) error

	// StoreEdgesBatch stores multiple edges in a single operation.
	StoreEdgesBatch(ctx context.Context, edges []RelationEdge) error

	// GetNeighbors returns multi-hop neighbors of an observation.
	GetNeighbors(ctx context.Context, obsID int64, maxHops int, limit int) ([]Neighbor, error)

	// GetPath returns the shortest path between two observations as a list of IDs.
	GetPath(ctx context.Context, fromID, toID int64) ([]int64, error)

	// SyncFromRelations bulk-loads relations from PostgreSQL into the graph.
	SyncFromRelations(ctx context.Context, relations []*models.ObservationRelation) error

	// GetCluster returns observation IDs in the same cluster as the given node.
	// Uses BFS traversal up to maxNodes results.
	GetCluster(ctx context.Context, nodeID int64, maxNodes int) ([]int64, error)

	// Stats returns graph store statistics.
	Stats(ctx context.Context) (GraphStoreStats, error)

	// Close releases resources held by the graph store.
	Close() error
}

// RelationEdge represents a typed, weighted edge between two observations.
type RelationEdge struct {
	SourceID     int64
	TargetID     int64
	RelationType models.RelationType // string: "causes", "fixes", etc.
	Confidence   float64
}

// Neighbor represents a graph neighbor found via multi-hop traversal.
type Neighbor struct {
	ObsID        int64
	Hops         int
	RelationType models.RelationType
}

// GraphStoreStats contains graph backend statistics.
type GraphStoreStats struct {
	NodeCount int
	EdgeCount int
	Provider  string
	Connected bool
}
