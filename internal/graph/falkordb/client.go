// Package falkordb implements graph.GraphStore against FalkorDB (Redis module).
package falkordb

import (
	"context"
	"fmt"
	"sync"
	"time"

	redisgraph "github.com/falkordb/falkordb-go"
	"github.com/gomodule/redigo/redis"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

// FalkorDBGraphStore implements graph.GraphStore against a FalkorDB instance.
type FalkorDBGraphStore struct {
	pool      *redis.Pool
	graphName string
	mu        sync.Mutex
}

var _ graph.GraphStore = (*FalkorDBGraphStore)(nil)

// NewFalkorDBGraphStore creates a FalkorDB-backed GraphStore.
func NewFalkorDBGraphStore(cfg *config.Config) (*FalkorDBGraphStore, error) {
	if cfg.FalkorDBAddr == "" {
		return nil, fmt.Errorf("FalkorDB address is required")
	}

	graphName := cfg.FalkorDBGraphName
	if graphName == "" {
		graphName = "engram"
	}

	dialOpts := []redis.DialOption{
		redis.DialConnectTimeout(5 * time.Second),
		redis.DialReadTimeout(10 * time.Second),
		redis.DialWriteTimeout(5 * time.Second),
	}
	if cfg.FalkorDBPassword != "" {
		dialOpts = append(dialOpts, redis.DialPassword(cfg.FalkorDBPassword))
	}

	pool := &redis.Pool{
		MaxIdle:     3,
		MaxActive:   10,
		IdleTimeout: 240 * time.Second,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", cfg.FalkorDBAddr, dialOpts...)
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			if time.Since(t) < 30*time.Second {
				return nil
			}
			_, err := c.Do("PING")
			return err
		},
	}

	store := &FalkorDBGraphStore{
		pool:      pool,
		graphName: graphName,
	}

	// Verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("FalkorDB ping failed: %w", err)
	}

	// Create index on Observation(id) if not exists
	if err := store.ensureIndex(); err != nil {
		log.Warn().Err(err).Msg("FalkorDB: failed to create index (non-fatal)")
	}

	log.Info().
		Str("addr", cfg.FalkorDBAddr).
		Str("graph", graphName).
		Msg("FalkorDB graph store connected")

	return store, nil
}

func (s *FalkorDBGraphStore) getGraph() (*redisgraph.Graph, redis.Conn, error) {
	conn := s.pool.Get()
	if err := conn.Err(); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("FalkorDB connection error: %w", err)
	}
	g := redisgraph.GraphNew(s.graphName, conn)
	return &g, conn, nil
}

func (s *FalkorDBGraphStore) ensureIndex() error {
	g, conn, err := s.getGraph()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = g.Query("CREATE INDEX ON :Observation(id)")
	// Ignore "already exists" errors
	if err != nil && err.Error() != "ERR Already indexed" {
		return err
	}
	return nil
}

// Ping checks FalkorDB connectivity.
func (s *FalkorDBGraphStore) Ping(_ context.Context) error {
	conn := s.pool.Get()
	defer conn.Close()
	_, err := conn.Do("PING")
	return err
}

// StoreEdge stores a single relation edge in FalkorDB.
func (s *FalkorDBGraphStore) StoreEdge(_ context.Context, edge graph.RelationEdge) error {
	g, conn, err := s.getGraph()
	if err != nil {
		return err
	}
	defer conn.Close()

	params := map[string]interface{}{
		"src":     int(edge.SourceID),
		"tgt":     int(edge.TargetID),
		"relType": string(edge.RelationType),
		"conf":    edge.Confidence,
	}

	_, err = g.ParameterizedQuery(
		"MERGE (a:Observation {id: $src}) "+
			"MERGE (b:Observation {id: $tgt}) "+
			"MERGE (a)-[r:REL {type: $relType}]->(b) "+
			"SET r.confidence = $conf",
		params,
	)
	return err
}

// StoreEdgesBatch stores multiple edges in FalkorDB.
func (s *FalkorDBGraphStore) StoreEdgesBatch(_ context.Context, edges []graph.RelationEdge) error {
	if len(edges) == 0 {
		return nil
	}

	g, conn, err := s.getGraph()
	if err != nil {
		return err
	}
	defer conn.Close()

	for _, edge := range edges {
		params := map[string]interface{}{
			"src":     int(edge.SourceID),
			"tgt":     int(edge.TargetID),
			"relType": string(edge.RelationType),
			"conf":    edge.Confidence,
		}

		_, err = g.ParameterizedQuery(
			"MERGE (a:Observation {id: $src}) "+
				"MERGE (b:Observation {id: $tgt}) "+
				"MERGE (a)-[r:REL {type: $relType}]->(b) "+
				"SET r.confidence = $conf",
			params,
		)
		if err != nil {
			return fmt.Errorf("batch edge store failed at src=%d tgt=%d: %w", edge.SourceID, edge.TargetID, err)
		}
	}
	return nil
}

// GetNeighbors returns multi-hop neighbors of an observation.
func (s *FalkorDBGraphStore) GetNeighbors(_ context.Context, obsID int64, maxHops int, limit int) ([]graph.Neighbor, error) {
	g, conn, err := s.getGraph()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if maxHops < 1 {
		maxHops = 2
	}
	if limit < 1 {
		limit = 20
	}

	// Variable-length path query — use named path to avoid FalkorDB
	// "Type mismatch: expected List or Null but was Path" on relationship list ops.
	query := fmt.Sprintf(
		"MATCH p = (a:Observation {id: $id})-[:REL*1..%d]-(b:Observation) "+
			"WHERE b.id <> $id "+
			"WITH DISTINCT b, length(p)-1 as hops, relationships(p) as rels "+
			"RETURN b.id, hops, type(rels[0]) as rel_type "+
			"ORDER BY hops "+
			"LIMIT %d",
		maxHops, limit,
	)

	params := map[string]interface{}{
		"id": int(obsID),
	}

	res, err := g.ParameterizedQuery(query, params)
	if err != nil {
		return nil, fmt.Errorf("GetNeighbors query failed: %w", err)
	}

	var neighbors []graph.Neighbor
	for res.Next() {
		rec := res.Record()
		bID, _ := rec.Get("b.id")
		hops, _ := rec.Get("hops")
		relType, _ := rec.Get("rel_type")

		var id int64
		switch v := bID.(type) {
		case int:
			id = int64(v)
		case int64:
			id = v
		case float64:
			id = int64(v)
		}

		var h int
		switch v := hops.(type) {
		case int:
			h = v
		case int64:
			h = int(v)
		case float64:
			h = int(v)
		}

		var rt string
		if relType != nil {
			rt = fmt.Sprintf("%v", relType)
		}

		neighbors = append(neighbors, graph.Neighbor{
			ObsID:        id,
			Hops:         h,
			RelationType: models.RelationType(rt),
		})
	}

	return neighbors, nil
}

// GetPath returns the shortest path between two observations.
func (s *FalkorDBGraphStore) GetPath(_ context.Context, fromID, toID int64) ([]int64, error) {
	g, conn, err := s.getGraph()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	params := map[string]interface{}{
		"from": int(fromID),
		"to":   int(toID),
	}

	// FalkorDB requires shortestPath in WITH/RETURN clauses
	res, err := g.ParameterizedQuery(
		"MATCH (a:Observation {id: $from}), (b:Observation {id: $to}) "+
			"WITH shortestPath((a)-[:REL*]->(b)) AS p "+
			"RETURN [n IN nodes(p) | n.id] AS path",
		params,
	)
	if err != nil {
		return nil, fmt.Errorf("GetPath query failed: %w", err)
	}

	if !res.Next() {
		return nil, nil // No path found
	}

	rec := res.Record()
	pathVal, _ := rec.Get("path")

	arr, ok := pathVal.([]interface{})
	if !ok {
		return nil, nil
	}

	path := make([]int64, 0, len(arr))
	for _, v := range arr {
		switch id := v.(type) {
		case int:
			path = append(path, int64(id))
		case int64:
			path = append(path, id)
		case float64:
			path = append(path, int64(id))
		}
	}

	return path, nil
}

// SyncFromRelations bulk-loads PostgreSQL relations into FalkorDB.
func (s *FalkorDBGraphStore) SyncFromRelations(_ context.Context, relations []*models.ObservationRelation) error {
	if len(relations) == 0 {
		return nil
	}

	g, conn, err := s.getGraph()
	if err != nil {
		return err
	}
	defer conn.Close()

	synced := 0
	for _, rel := range relations {
		params := map[string]interface{}{
			"src":     int(rel.SourceID),
			"tgt":     int(rel.TargetID),
			"relType": string(rel.RelationType),
			"conf":    rel.Confidence,
		}

		_, err = g.ParameterizedQuery(
			"MERGE (a:Observation {id: $src}) "+
				"MERGE (b:Observation {id: $tgt}) "+
				"MERGE (a)-[r:REL {type: $relType}]->(b) "+
				"SET r.confidence = $conf",
			params,
		)
		if err != nil {
			log.Warn().Err(err).
				Int64("src", rel.SourceID).
				Int64("tgt", rel.TargetID).
				Msg("FalkorDB: sync edge failed, continuing")
			continue
		}
		synced++
	}

	log.Info().
		Int("total", len(relations)).
		Int("synced", synced).
		Msg("FalkorDB: sync complete")

	return nil
}

// GetCluster returns observation IDs in the same cluster as the given node using BFS traversal.
func (s *FalkorDBGraphStore) GetCluster(_ context.Context, nodeID int64, maxNodes int) ([]int64, error) {
	g, conn, err := s.getGraph()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if maxNodes < 1 {
		maxNodes = 50
	}

	// BFS: find all connected nodes up to 3 hops, limited by maxNodes.
	// nodeID is passed as a parameter to avoid injection risk; LIMIT does not
	// support parameters in Cypher so maxNodes (an int) is interpolated safely.
	query := fmt.Sprintf(
		"MATCH (a:Observation {id: $nodeID})-[:REL*1..3]-(b:Observation) "+
			"WHERE b.id <> $nodeID "+
			"RETURN DISTINCT b.id "+
			"LIMIT %d",
		maxNodes,
	)
	params := map[string]interface{}{
		"nodeID": nodeID,
	}

	result, err := g.ParameterizedQuery(query, params)
	if err != nil {
		return nil, fmt.Errorf("cluster query: %w", err)
	}

	var ids []int64
	for result.Next() {
		record := result.Record()
		if id, ok := record.Get("b.id"); ok {
			switch v := id.(type) {
			case int64:
				ids = append(ids, v)
			case float64:
				ids = append(ids, int64(v))
			}
		}
	}
	return ids, nil
}

// Stats returns graph statistics.
func (s *FalkorDBGraphStore) Stats(_ context.Context) (graph.GraphStoreStats, error) {
	g, conn, err := s.getGraph()
	if err != nil {
		return graph.GraphStoreStats{Provider: "falkordb", Connected: false}, err
	}
	defer conn.Close()

	stats := graph.GraphStoreStats{
		Provider:  "falkordb",
		Connected: true,
	}

	// Node count
	res, err := g.Query("MATCH (n) RETURN count(n)")
	if err == nil && res.Next() {
		if v, ok := res.Record().GetByIndex(0).(int); ok {
			stats.NodeCount = v
		}
	}

	// Edge count
	res, err = g.Query("MATCH ()-[r]->() RETURN count(r)")
	if err == nil && res.Next() {
		if v, ok := res.Record().GetByIndex(0).(int); ok {
			stats.EdgeCount = v
		}
	}

	return stats, nil
}

// Close releases the connection pool.
func (s *FalkorDBGraphStore) Close() error {
	return s.pool.Close()
}
