package instincts

import "context"

// IsDuplicate checks if an observation with similar content already exists.
// In v5, vector storage was removed. Always returns false so the caller proceeds to store.
func IsDuplicate(_ context.Context, _ any, _ string, _ float64) (bool, error) {
	return false, nil
}
