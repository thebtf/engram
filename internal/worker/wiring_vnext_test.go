package worker

// wiring_vnext_test.go — regression guard for the wireVnextStores helper (Finding 3 fix).
//
// The wiring_assertion_test.go in internal/mcp tests the SERVER-SIDE gate:
// "given stores set on mcp.Server, tools/list includes lifecycle/graph".
// That test does NOT cover the SERVICE-SIDE wiring: if wireVnextStores is
// deleted from service.go, or its body emptied, the mcp test still passes
// because it sets stores directly.
//
// This test covers the helper itself: given a fresh mcp.Server and stores,
// wireVnextStores wires them in such a way that the server then advertises
// the lifecycle tool. Deleting wireVnextStores or its body breaks this test.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/mcp"
)

// buildMCPToolNames calls ListTools on the server and returns tool name strings.
func buildMCPToolNames(srv *mcp.Server) []string {
	tools := srv.ListTools()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestWireVnextStores_LifecycleAdvertisedAfterWiring verifies that wireVnextStores
// causes the lifecycle tool to appear in the server's tool list when the flag is on
// and both stores are provided. Deleting wireVnextStores or removing either Set call
// from its body will break this test.
func TestWireVnextStores_LifecycleAdvertisedAfterWiring(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "true")
	t.Setenv("ENGRAM_GRAPH_ENABLED", "false")

	srv := mcp.NewServer(mcp.ServerOptions{Version: "wiring-test"})
	// The lifecycle gate checks both memoryStore and promotionStore.
	// Wire memoryStore directly (wireVnextStores doesn't touch it).
	srv.SetMemoryStore(&gormdb.MemoryStore{})

	// Verify the tool is ABSENT before wiring promotion store.
	namesBefore := buildMCPToolNames(srv)
	assert.NotContains(t, namesBefore, "lifecycle",
		"lifecycle must be absent before wireVnextStores is called (promotionStore nil)")

	// Call the helper under test.
	wireVnextStores(srv, &gormdb.PromotionStore{}, &graph.Store{})

	// Now the tool must be present.
	namesAfter := buildMCPToolNames(srv)
	assert.Contains(t, namesAfter, "lifecycle",
		"lifecycle must be advertised after wireVnextStores wires promotionStore")
}

// TestWireVnextStores_GraphAdvertisedAfterWiring verifies that wireVnextStores
// causes the graph tool to appear when ENGRAM_GRAPH_ENABLED=true.
func TestWireVnextStores_GraphAdvertisedAfterWiring(t *testing.T) {
	t.Setenv("ENGRAM_GRAPH_ENABLED", "true")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")

	srv := mcp.NewServer(mcp.ServerOptions{Version: "wiring-test"})

	namesBefore := buildMCPToolNames(srv)
	assert.NotContains(t, namesBefore, "graph",
		"graph must be absent before wireVnextStores is called (graphStore nil)")

	wireVnextStores(srv, &gormdb.PromotionStore{}, &graph.Store{})

	namesAfter := buildMCPToolNames(srv)
	assert.Contains(t, namesAfter, "graph",
		"graph must be advertised after wireVnextStores wires graphStore")
}

// TestWireVnextStores_NoLeakWhenFlagsOff verifies that wire does not spuriously
// advertise tools when both feature flags are off.
func TestWireVnextStores_NoLeakWhenFlagsOff(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	t.Setenv("ENGRAM_GRAPH_ENABLED", "false")

	srv := mcp.NewServer(mcp.ServerOptions{Version: "wiring-test"})
	srv.SetMemoryStore(&gormdb.MemoryStore{})

	wireVnextStores(srv, &gormdb.PromotionStore{}, &graph.Store{})

	names := buildMCPToolNames(srv)
	assert.NotContains(t, names, "lifecycle",
		"lifecycle must not appear when ENGRAM_LIFECYCLE_ENABLED=false")
	assert.NotContains(t, names, "graph",
		"graph must not appear when ENGRAM_GRAPH_ENABLED=false")
}
