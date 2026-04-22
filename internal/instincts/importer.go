package instincts

import (
	"context"
	"fmt"
)

// Import reads all instinct files from dir and returns an ImportResult.
// v5 (US3): ObservationStore removed; instinct import is disabled until
// chunk 3 wires the MemoryStore replacement.
func Import(ctx context.Context, dir string) (*ImportResult, error) {
	_ = ctx
	_ = dir
	return nil, fmt.Errorf("instinct import from filesystem disabled in v5 — use 'files' parameter to send content directly")
}
