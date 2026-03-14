package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/instincts"
)

// handleImportInstincts imports ECC instinct files as guidance observations.
// Supports two modes:
//   - files: array of {name, content} objects (client-server, preferred)
//   - path: filesystem path (legacy, only works when server has local access)
func (s *Server) handleImportInstincts(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Files []instincts.InstinctFile
		Path  string
	}
	params.Path = coerceString(m["path"], "")
	if filesRaw, ok := m["files"].([]any); ok {
		for _, item := range filesRaw {
			if fm, ok := item.(map[string]any); ok {
				params.Files = append(params.Files, instincts.InstinctFile{
					Name:    coerceString(fm["name"], ""),
					Content: coerceString(fm["content"], ""),
				})
			}
		}
	}

	var result *instincts.ImportResult

	if len(params.Files) > 0 {
		// Client-server mode: content sent over the wire
		result, err = instincts.ImportFromContent(ctx, params.Files, s.vectorClient, s.observationStore)
	} else {
		// Legacy mode: read from local filesystem (deprecated)
		log.Warn().Str("path", params.Path).Msg("import_instincts: using deprecated path-based import; 'path' parameter will be removed. Use 'files' with content instead.")

		dir, resolveErr := instincts.ResolveDir(params.Path)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve path: %w", resolveErr)
		}

		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			return "", fmt.Errorf("instincts directory not found: %s (hint: use 'files' parameter to send content directly)", dir)
		}

		result, err = instincts.Import(ctx, dir, s.vectorClient, s.observationStore)
	}

	if err != nil {
		return "", fmt.Errorf("import instincts: %w", err)
	}

	out, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		return "", fmt.Errorf("marshal result: %w", marshalErr)
	}

	return string(out), nil
}
