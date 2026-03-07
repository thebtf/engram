package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/thebtf/engram/internal/instincts"
)

// handleImportInstincts imports ECC instinct files as guidance observations.
func (s *Server) handleImportInstincts(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Resolve and validate path against allowed base directory
	dir, err := instincts.ResolveDir(params.Path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Check directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", fmt.Errorf("instincts directory not found: %s", dir)
	}

	result, err := instincts.Import(ctx, dir, s.vectorClient, s.observationStore)
	if err != nil {
		return "", fmt.Errorf("import instincts: %w", err)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}

	return string(out), nil
}
