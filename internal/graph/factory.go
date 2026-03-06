package graph

import (
	"fmt"

	"github.com/thebtf/engram/internal/config"
)

// NewGraphStore creates a GraphStore based on configuration.
// Returns NoopGraphStore if no graph provider is configured.
func NewGraphStore(cfg *config.Config) (GraphStore, error) {
	switch cfg.GraphProvider {
	case "falkordb":
		if cfg.FalkorDBAddr == "" {
			return nil, fmt.Errorf("ENGRAM_FALKORDB_ADDR is required when graph_provider=falkordb")
		}
		// FalkorDB implementation is in internal/graph/falkordb/ package.
		// Caller should use falkordb.NewFalkorDBGraphStore() directly.
		return nil, fmt.Errorf("use falkordb.NewFalkorDBGraphStore() for falkordb provider")
	case "", "none":
		return &NoopGraphStore{}, nil
	default:
		return nil, fmt.Errorf("unknown graph provider: %q", cfg.GraphProvider)
	}
}
