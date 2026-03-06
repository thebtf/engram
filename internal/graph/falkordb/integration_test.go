// +build integration

package falkordb

import (
	"os"
	"testing"

	redisgraph "github.com/falkordb/falkordb-go"
	"github.com/gomodule/redigo/redis"
)

func getTestAddr() string {
	addr := os.Getenv("ENGRAM_FALKORDB_ADDR")
	if addr == "" {
		addr = "unleashed.lan:6379"
	}
	return addr
}

func TestFalkorDBConnection(t *testing.T) {
	conn, err := redis.Dial("tcp", getTestAddr())
	if err != nil {
		t.Fatalf("Failed to connect to FalkorDB: %v", err)
	}
	defer conn.Close()

	graph := redisgraph.GraphNew("engram_test", conn)
	defer graph.Delete()

	// Test 1: RETURN 1 (ping)
	res, err := graph.Query("RETURN 1")
	if err != nil {
		t.Fatalf("Query RETURN 1 failed: %v", err)
	}
	if !res.Next() {
		t.Fatal("Expected result from RETURN 1")
	}
	val := res.Record().GetByIndex(0)
	if val != 1 {
		t.Fatalf("Expected 1, got %v (%T)", val, val)
	}
	t.Logf("Ping OK: RETURN 1 = %v", val)

	// Test 2: MERGE nodes and edges
	_, err = graph.Query("MERGE (a:Observation {id: 100}) MERGE (b:Observation {id: 200}) MERGE (a)-[:REL {type: 'causes', confidence: 0.85}]->(b)")
	if err != nil {
		t.Fatalf("MERGE failed: %v", err)
	}
	t.Log("MERGE nodes+edge OK")

	// Test 3: Parameterized query
	params := map[string]interface{}{
		"src":     int(100),
		"tgt":     int(200),
		"relType": "fixes",
		"conf":    0.9,
	}
	_, err = graph.ParameterizedQuery(
		"MERGE (a:Observation {id: $src}) MERGE (b:Observation {id: $tgt}) MERGE (a)-[:REL {type: $relType, confidence: $conf}]->(b)",
		params,
	)
	if err != nil {
		t.Fatalf("Parameterized MERGE failed: %v", err)
	}
	t.Log("Parameterized MERGE OK")

	// Test 4: Query neighbors
	res, err = graph.Query("MATCH (a:Observation {id: 100})-[r:REL]->(b:Observation) RETURN b.id, r.type, r.confidence")
	if err != nil {
		t.Fatalf("Neighbor query failed: %v", err)
	}
	count := 0
	for res.Next() {
		rec := res.Record()
		bID := rec.GetByIndex(0)
		relType := rec.GetByIndex(1)
		conf := rec.GetByIndex(2)
		t.Logf("  Neighbor: id=%v, type=%v, confidence=%v", bID, relType, conf)
		count++
	}
	if count == 0 {
		t.Fatal("Expected at least one neighbor")
	}
	t.Logf("Neighbor query OK: %d results", count)

	// Test 5: Multi-hop variable-length path
	// Add a third node to test multi-hop
	_, err = graph.Query("MERGE (b:Observation {id: 200}) MERGE (c:Observation {id: 300}) MERGE (b)-[:REL {type: 'explains', confidence: 0.7}]->(c)")
	if err != nil {
		t.Fatalf("Third node MERGE failed: %v", err)
	}

	res, err = graph.Query("MATCH (a:Observation {id: 100})-[r:REL*1..2]->(b:Observation) WHERE b.id <> 100 RETURN DISTINCT b.id, length(r) as hops")
	if err != nil {
		t.Fatalf("Multi-hop query failed: %v", err)
	}
	hopCount := 0
	for res.Next() {
		rec := res.Record()
		bID := rec.GetByIndex(0)
		hops := rec.GetByIndex(1)
		t.Logf("  Multi-hop: id=%v, hops=%v", bID, hops)
		hopCount++
	}
	if hopCount < 2 {
		t.Fatalf("Expected at least 2 multi-hop results (200 at 1 hop, 300 at 2 hops), got %d", hopCount)
	}
	t.Logf("Multi-hop query OK: %d results", hopCount)

	// Test 6: shortestPath — FalkorDB requires it in WITH/RETURN clauses
	res, err = graph.Query("MATCH (a:Observation {id: 100}), (b:Observation {id: 300}) WITH shortestPath((a)-[:REL*]->(b)) AS p RETURN length(p)")
	if err != nil {
		t.Fatalf("shortestPath query failed: %v", err)
	}
	if !res.Next() {
		t.Fatal("Expected result from shortestPath")
	}
	pathLen := res.Record().GetByIndex(0)
	t.Logf("shortestPath 100->300 length: %v", pathLen)

	// Test 7: Node and edge counts
	res, err = graph.Query("MATCH (n) RETURN count(n)")
	if err != nil {
		t.Fatalf("Node count query failed: %v", err)
	}
	res.Next()
	nodeCount := res.Record().GetByIndex(0)
	t.Logf("Node count: %v", nodeCount)

	res, err = graph.Query("MATCH ()-[r]->() RETURN count(r)")
	if err != nil {
		t.Fatalf("Edge count query failed: %v", err)
	}
	res.Next()
	edgeCount := res.Record().GetByIndex(0)
	t.Logf("Edge count: %v", edgeCount)

	// Test 8: Create index
	res, err = graph.Query("CREATE INDEX ON :Observation(id)")
	if err != nil {
		t.Fatalf("Create index failed: %v", err)
	}
	t.Logf("Index created: %d indices", res.IndicesCreated())
}
