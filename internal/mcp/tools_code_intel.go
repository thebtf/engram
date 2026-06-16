// Package mcp — code intelligence MCP tools (CR-006).
//
// Exposes three tools when ENGRAM_CODE_INTEL_ENABLED=true:
//   - codebase_search  (server-side): FTS+vector hybrid search over code_chunks.
//   - codebase_index   (daemon-side): triggers a code index run on a project root.
//   - codebase_status  (server-side): reports chunk counts and last-indexed time.
//
// Flag contract: when ENGRAM_CODE_INTEL_ENABLED != "true", none of these tools
// appear in tools/list and any tools/call for them returns "unknown tool".
// The ListTools output MUST be byte-identical to the pre-CR-006 surface when
// the flag is off — no new entries, no schema changes to existing tools.
//
// V1 design note: QueryVec is empty for all codebase_search calls (FTS-only
// mode). Embedding-based vector leg requires a client-side embedding step that
// is deferred to a follow-up CR. FTS-only degrades gracefully per the
// CodeHybridSearch spec (QueryVec empty → FTS-only, not an error).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/retrieval"
)

// codeIntelEnabled reports whether the code intelligence feature is enabled.
// All three codebase_* tools are gated behind this flag. String equality with
// "true" matches the convention used by vnextFEnabled and other flag checks in
// this package.
func codeIntelEnabled() bool {
	return os.Getenv("ENGRAM_CODE_INTEL_ENABLED") == "true"
}

// SetCodeChunkStore wires the code chunk store into the MCP server.
// Must be called when ENGRAM_CODE_INTEL_ENABLED=true to enable codebase_search
// and codebase_status. When nil, the server still starts; those two tools are
// simply absent from tools/list (guarded by the nil check below).
func (s *Server) SetCodeChunkStore(cs *gorm.CodeChunkStore) {
	s.codeChunkStore = cs
}

// codebaseSearchTool returns the codebase_search tool definition.
// Advertised only when codeIntelEnabled() && s.codeChunkStore != nil.
func codebaseSearchTool() Tool {
	return Tool{
		Name:        "codebase_search",
		Description: "Search the indexed codebase using full-text search (FTS). Returns ranked code chunks with file path, byte range, language, content, and score. Requires ENGRAM_CODE_INTEL_ENABLED=true and a prior codebase_index run.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural language or keyword search query",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum results to return (default 10, max 50)",
					"default":     10,
					"minimum":     1,
					"maximum":     50,
				},
				"project": map[string]any{
					"type":        "string",
					"description": "Project ID to search (defaults to the current session project)",
				},
			},
		},
	}
}

// codebaseStatusTool returns the codebase_status tool definition.
// Advertised only when codeIntelEnabled() && s.codeChunkStore != nil.
func codebaseStatusTool() Tool {
	return Tool{
		Name:        "codebase_status",
		Description: "Report the code index status for the current project: total chunks, embedded chunks, and last indexed timestamp. Requires ENGRAM_CODE_INTEL_ENABLED=true.",
		tier:        tierUseful,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": map[string]any{
					"type":        "string",
					"description": "Project ID (defaults to the current session project)",
				},
			},
		},
	}
}

// handleCodebaseSearch executes the codebase_search tool.
// Parses {query, limit, project}, resolves the project ID, delegates to
// retrieval.CodeHybridSearch in FTS-only mode (QueryVec empty), and formats
// the result as a JSON array of code hit objects.
func (s *Server) handleCodebaseSearch(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_search requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.codeChunkStore == nil {
		return "", fmt.Errorf("codebase_search: code chunk store not wired")
	}

	var params struct {
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		Project string `json:"project"`
	}
	if args != nil {
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("codebase_search: invalid args: %w", err)
		}
	}
	if params.Query == "" {
		return "", fmt.Errorf("codebase_search: query is required")
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}
	if params.Limit > 50 {
		params.Limit = 50
	}

	// Project ID resolution: use the explicit arg when provided, otherwise derive
	// from the context. For server-side tools the project context is not available
	// the same way as in the daemon, so we require the caller to supply it when
	// the tool is used from a direct gRPC/HTTP path. In V1 we accept the
	// project field as the authoritative project ID (same convention as the
	// store/recall tools that accept a "project" param).
	projectID := params.Project
	if projectID == "" {
		return "", fmt.Errorf("codebase_search: project ID is required (supply via 'project' param)")
	}

	// V1: FTS-only mode. QueryVec is empty, which causes CodeHybridSearch to
	// skip the vector leg and run FTS only. This is explicitly documented as
	// acceptable for V1 — see file header note.
	opts := retrieval.CodeHybridOptions{
		QueryVec: nil, // FTS-only for V1
	}

	hits, err := retrieval.CodeHybridSearch(ctx, projectID, params.Query, params.Limit, s.codeChunkStore, opts)
	if err != nil {
		return "", fmt.Errorf("codebase_search: %w", err)
	}

	// Format results as a JSON array. Each element carries the fields a
	// SocratiCode-compatible client expects: file_path, byte_start, byte_end,
	// language, content, score.
	type hitResult struct {
		ID        int64   `json:"id"`
		FilePath  string  `json:"file_path"`
		ByteStart int     `json:"byte_start"`
		ByteEnd   int     `json:"byte_end"`
		Language  string  `json:"language"`
		Content   string  `json:"content"`
		Score     float64 `json:"score"`
	}

	results := make([]hitResult, len(hits))
	for i, h := range hits {
		results[i] = hitResult{
			ID:        h.ID,
			FilePath:  h.FilePath,
			ByteStart: h.ByteStart,
			ByteEnd:   h.ByteEnd,
			Language:  h.Language,
			Content:   h.Content,
			Score:     h.Score,
		}
	}

	out, err := json.Marshal(map[string]any{
		"results": results,
		"count":   len(results),
		"query":   params.Query,
		"project": projectID,
	})
	if err != nil {
		return "", fmt.Errorf("codebase_search: marshal results: %w", err)
	}
	return string(out), nil
}

// handleCodebaseStatus executes the server-side portion of codebase_status.
// Returns total_chunks, embedded_chunks, and last_indexed_at for the project.
// This handler is called directly (not via proxy) when the server-side flag is on.
// The daemon-side codebase_status merges its in-memory run state with the counts
// from this handler via the engramcore proxy.
func (s *Server) handleCodebaseStatus(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_status requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.codeChunkStore == nil {
		return "", fmt.Errorf("codebase_status: code chunk store not wired")
	}

	var params struct {
		Project string `json:"project"`
	}
	if args != nil {
		_ = json.Unmarshal(args, &params)
	}
	projectID := params.Project
	if projectID == "" {
		return "", fmt.Errorf("codebase_status: project ID is required (supply via 'project' param)")
	}

	total, err := s.codeChunkStore.CountByProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("codebase_status count_total: %w", err)
	}
	embedded, err := s.codeChunkStore.CountEmbeddedByProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("codebase_status count_embedded: %w", err)
	}
	lastAt, hasAt, err := s.codeChunkStore.MaxUpdatedAtByProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("codebase_status max_updated_at: %w", err)
	}

	result := map[string]any{
		"project":         projectID,
		"total_chunks":    total,
		"embedded_chunks": embedded,
	}
	if hasAt {
		result["last_indexed_at"] = lastAt.Format(time.RFC3339)
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("codebase_status: marshal: %w", err)
	}
	return string(out), nil
}
