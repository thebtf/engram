package instincts

import (
	"context"
	"fmt"

	"github.com/thebtf/engram/internal/vector"
)

// IsDuplicate checks if an observation with similar content already exists.
// It performs a vector similarity query filtered to observation vectors only,
// and returns true if a result with similarity >= threshold is found.
// Returns an error if vectorClient is nil, since without dedup the import
// cannot guarantee idempotency.
func IsDuplicate(ctx context.Context, vectorClient vector.Client, title string, threshold float64) (bool, error) {
	if vectorClient == nil {
		return false, fmt.Errorf("vector client is nil: dedup requires a vector backend")
	}

	// Filter to observation vectors only to avoid false matches from prompts or summaries
	filter := vector.BuildWhereFilter(vector.DocTypeObservation, "", false, nil)

	results, err := vectorClient.Query(ctx, title, 1, filter)
	if err != nil {
		return false, err
	}

	if len(results) > 0 && results[0].Similarity >= threshold {
		return true, nil
	}

	return false, nil
}
