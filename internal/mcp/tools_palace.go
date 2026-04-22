package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// handleWakeUp was observation-backed in pre-v5 engram.
// recall(action="wake_up", project="...")
func (s *Server) handleWakeUp(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("wake_up not available in v5 (palace wake-up still depends on removed observations store)")
}

// handleTaxonomy was observation-backed in pre-v5 engram.
// recall(action="taxonomy", project="...")
func (s *Server) handleTaxonomy(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("taxonomy not available in v5 (palace taxonomy still depends on removed observations store)")
}

// handleTunnels was observation-backed in pre-v5 engram.
// recall(action="tunnels", project_a="...", project_b="...")
func (s *Server) handleTunnels(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("tunnels not available in v5 (palace tunnel analysis still depends on removed observations store)")
}

// handleMine was observation-backed in pre-v5 engram.
// store(action="mine", content="...", project="...", source_file="...")
func (s *Server) handleMine(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("mine not available in v5 (palace mining still depends on removed observations store)")
}

// handleMineDirectory was observation-backed in pre-v5 engram.
// store(action="mine_directory", path="...", project="...")
func (s *Server) handleMineDirectory(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("mine_directory not available in v5 (palace mining still depends on removed observations store)")
}

// handleCompressAAK was observation-backed in pre-v5 engram.
// admin(action="compress_aaak", observation_id=123)
func (s *Server) handleCompressAAK(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("compress_aaak not available in v5 (AAAK compression still depends on removed observations store)")
}

// handleSetAAKCode was observation-backed in pre-v5 engram.
// admin(action="set_aaak_code", entity="PostgreSQL", code="POS")
func (s *Server) handleSetAAKCode(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("set_aaak_code not available in v5 (AAAK entity code updates still depend on removed observations store)")
}

// handleTaxonomyStats was observation-backed in pre-v5 engram.
// admin(action="taxonomy_stats")
func (s *Server) handleTaxonomyStats(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("taxonomy_stats not available in v5 (palace taxonomy stats still depend on removed observations store)")
}

