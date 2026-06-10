package mcp

// wiring_assertion_test.go — startup wiring regression guard (milestone-B T015-gap, C T016-gap).
//
// These tests verify that:
//  1. With ENGRAM_LIFECYCLE_ENABLED=true AND promotionStore+memoryStore set,
//     tools/list includes the "lifecycle" tool.
//  2. With ENGRAM_GRAPH_ENABLED=true AND graphStore set, tools/list includes "graph".
//  3. With both flags OFF (or stores nil), neither tool appears in tools/list.
//
// This is the regression guard that prevents re-orphaning the stores and
// silently removing the tools from the advertised surface.

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
	t.Setenv("ENGRAM_GRAPH_ENABLED", "false")

	srv := NewServer(ServerOptions{Version: "test"})

	// Wire the two stores the lifecycle tool gate checks.
	// Use zero-value store structs — tools/list only inspects nil vs non-nil.
	srv.SetMemoryStore(&gorm.MemoryStore{})
	srv.SetPromotionStore(&gorm.PromotionStore{})

	names := buildToolsList(srv)
	assert.Contains(t, names, "lifecycle",
		"lifecycle tool must appear when ENGRAM_LIFECYCLE_ENABLED=true and stores are set")
	assert.NotContains(t, names, "graph",
		"graph tool must NOT appear when ENGRAM_GRAPH_ENABLED=false")
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

// TestWiring_GraphTool_AppearsWhenStoreSetAndFlagOn verifies that
// "graph" appears in tools/list when ENGRAM_GRAPH_ENABLED=true and graphStore is wired.
func TestWiring_GraphTool_AppearsWhenStoreSetAndFlagOn(t *testing.T) {
	t.Setenv("ENGRAM_GRAPH_ENABLED", "true")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")

	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetGraphStore(&graph.Store{})

	names := buildToolsList(srv)
	assert.Contains(t, names, "graph",
		"graph tool must appear when ENGRAM_GRAPH_ENABLED=true and graphStore is set")
	assert.NotContains(t, names, "lifecycle",
		"lifecycle tool must NOT appear when ENGRAM_LIFECYCLE_ENABLED=false")
}

// TestWiring_GraphTool_AbsentWhenFlagOff verifies "graph" is absent
// when ENGRAM_GRAPH_ENABLED is not set, even with graphStore wired.
func TestWiring_GraphTool_AbsentWhenFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_GRAPH_ENABLED", "false")

	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetGraphStore(&graph.Store{})

	names := buildToolsList(srv)
	assert.NotContains(t, names, "graph",
		"graph tool must NOT appear when ENGRAM_GRAPH_ENABLED=false")
}

// TestWiring_GraphTool_AbsentWhenStoreNil verifies "graph" is absent
// when the flag is on but graphStore is nil.
func TestWiring_GraphTool_AbsentWhenStoreNil(t *testing.T) {
	t.Setenv("ENGRAM_GRAPH_ENABLED", "true")

	srv := NewServer(ServerOptions{Version: "test"})
	// Do NOT call SetGraphStore.

	names := buildToolsList(srv)
	assert.NotContains(t, names, "graph",
		"graph tool must NOT appear when graphStore is nil")
}

// TestWiring_BothToolsOff_FlagsUnset verifies that with no env vars set,
// neither lifecycle nor graph appears, and no new goroutines would be launched.
func TestWiring_BothToolsOff_FlagsUnset(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "")
	t.Setenv("ENGRAM_GRAPH_ENABLED", "")

	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetMemoryStore(&gorm.MemoryStore{})
	srv.SetPromotionStore(&gorm.PromotionStore{})
	srv.SetGraphStore(&graph.Store{})

	names := buildToolsList(srv)
	assert.NotContains(t, names, "lifecycle",
		"lifecycle must be absent when flag is empty string")
	assert.NotContains(t, names, "graph",
		"graph must be absent when flag is empty string")
}
