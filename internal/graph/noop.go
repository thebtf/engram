package graph

import (
	"context"
	"errors"

	"github.com/thebtf/engram/pkg/models"
)

// ErrGraphStoreNotConfigured is returned by NoopGraphStore.Ping.
var ErrGraphStoreNotConfigured = errors.New("graph store not configured")

// NoopGraphStore is a no-op implementation of GraphStore.
// It returns empty results for all queries and ErrGraphStoreNotConfigured for Ping.
// Used as fallback when no graph backend is configured.
type NoopGraphStore struct{}

var _ GraphStore = (*NoopGraphStore)(nil)

func (n *NoopGraphStore) Ping(_ context.Context) error {
	return ErrGraphStoreNotConfigured
}

func (n *NoopGraphStore) StoreEdge(_ context.Context, _ RelationEdge) error {
	return nil
}

func (n *NoopGraphStore) StoreEdgesBatch(_ context.Context, _ []RelationEdge) error {
	return nil
}

func (n *NoopGraphStore) GetNeighbors(_ context.Context, _ int64, _ int, _ int) ([]Neighbor, error) {
	return nil, nil
}

func (n *NoopGraphStore) GetPath(_ context.Context, _, _ int64) ([]int64, error) {
	return nil, nil
}

func (n *NoopGraphStore) SyncFromRelations(_ context.Context, _ []*models.ObservationRelation) error {
	return nil
}

func (n *NoopGraphStore) GetCluster(_ context.Context, _ int64, _ int) ([]int64, error) {
	return nil, nil
}

func (n *NoopGraphStore) Stats(_ context.Context) (GraphStoreStats, error) {
	return GraphStoreStats{Provider: "none", Connected: false}, nil
}

func (n *NoopGraphStore) Close() error {
	return nil
}
