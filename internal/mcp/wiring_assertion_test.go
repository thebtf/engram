package mcp

// wiring_assertion_test.go — optional-tool startup wiring regression guard.
//
// These tests verify that lifecycle remains flag-gated while historical graph
// readers are present whenever their store is wired, independently of the
// retired ENGRAM_GRAPH_ENABLED flag.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
)

// toolNames extracts the Name field from a slice of Tool.
func toolNames(tools []Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}

// buildToolsList is a helper that calls handleToolsList without a DB round-trip.
// It uses include_all=true to get the full advertised surface — matching how the
// gRPC adapter calls ListTools() and how clients discover optional tools.
func buildToolsList(s *Server) []string {
	req := &Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/list",
		Params:  json.RawMessage(`{"include_all":true}`),
	}
	resp := s.handleToolsList(req)
	if resp == nil || resp.Error != nil {
		return nil
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := result["tools"]
	if !ok {
		return nil
	}
	tools, ok := raw.([]Tool)
	if !ok {
		return nil
	}
	return toolNames(tools)
}

// TestWiring_LifecycleTool_AppearsWhenStoresSetAndFlagOn verifies that
// "lifecycle" appears in tools/list when ENGRAM_LIFECYCLE_ENABLED=true and
// both memoryStore and promotionStore are wired.
func TestWiring_LifecycleTool_AppearsWhenStoresSetAndFlagOn(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "true")

	srv := NewServer(ServerOptions{Version: "test"})

	// Wire the two stores the lifecycle tool gate checks.
	// Use zero-value store structs — tools/list only inspects nil vs non-nil.
	srv.SetMemoryStore(&gorm.MemoryStore{})
	srv.SetPromotionStore(&gorm.PromotionStore{})

	names := buildToolsList(srv)
	assert.Contains(t, names, "lifecycle",
		"lifecycle tool must appear when ENGRAM_LIFECYCLE_ENABLED=true and stores are set")
	assert.NotContains(t, names, "graph",
		"graph tool must not appear when graphStore is nil")
}

// TestWiring_LifecycleTool_AbsentWhenFlagOff verifies "lifecycle" is absent
// when ENGRAM_LIFECYCLE_ENABLED is not set (or false), even with stores wired.
func TestWiring_LifecycleTool_AbsentWhenFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")

	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetMemoryStore(&gorm.MemoryStore{})
	srv.SetPromotionStore(&gorm.PromotionStore{})

	names := buildToolsList(srv)
	assert.NotContains(t, names, "lifecycle",
		"lifecycle tool must NOT appear when ENGRAM_LIFECYCLE_ENABLED=false")
}

// TestWiring_LifecycleTool_AbsentWhenStoresNil verifies "lifecycle" is absent
// when the flag is on but stores are nil (partial wiring state).
func TestWiring_LifecycleTool_AbsentWhenStoresNil(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "true")

	srv := NewServer(ServerOptions{Version: "test"})
	// Do NOT call SetMemoryStore / SetPromotionStore — stores remain nil.

	names := buildToolsList(srv)
	assert.NotContains(t, names, "lifecycle",
		"lifecycle tool must NOT appear when stores are nil (partial wiring)")
}

func TestWiring_HistoricalGraphToolIgnoresLegacyFlag(t *testing.T) {
	for _, value := range []string{"", "false", "true"} {
		t.Run("flag_"+value, func(t *testing.T) {
			t.Setenv("ENGRAM_GRAPH_ENABLED", value)
			srv := NewServer(ServerOptions{Version: "test"})
			srv.SetGraphStore(&graph.Store{})
			assert.Contains(t, buildToolsList(srv), "graph")
		})
	}
}

func TestWiring_GraphToolAbsentWhenStoreNil(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	assert.NotContains(t, buildToolsList(srv), "graph")
}

func TestWiring_FlaglessLifecycleAndHistoricalGraphReaders(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "")
	t.Setenv("ENGRAM_GRAPH_ENABLED", "")

	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetMemoryStore(&gorm.MemoryStore{})
	srv.SetPromotionStore(&gorm.PromotionStore{})
	srv.SetGraphStore(&graph.Store{})

	names := buildToolsList(srv)
	assert.NotContains(t, names, "lifecycle")
	assert.Contains(t, names, "graph")
}
