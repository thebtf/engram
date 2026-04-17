package instincts

import (
	"context"
)

// IsDuplicate checks if an observation with similar content already exists.
// In v5, vector storage was removed. Always returns false so the caller proceeds to store.
func IsDuplicate(ctx context.Context, vectorClient any, title string, threshold float64) (bool, error) {
	_ = ctx
	_ = vectorClient
	_ = title
	_ = threshold
	return false, nil
}
