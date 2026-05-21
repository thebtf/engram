package worker

import "sync"

// segmentEmbeddingCache stores per-session, per-segment running centroid
// embeddings in memory. These are ephemeral — lost on server restart, which is
// acceptable since segments are re-created from scratch in a new session.
type segmentEmbeddingCache struct {
	mu    sync.RWMutex
	cache map[string][]float32 // key: "sessionID:segmentIndex"
}

var globalSegmentCache = &segmentEmbeddingCache{
	cache: make(map[string][]float32),
}

func segmentKey(sessionID string, segmentIndex int) string {
	return sessionID + ":" + string(rune('0'+segmentIndex))
}

func (s *Service) storeSegmentEmbedding(sessionID string, segmentIndex int, vec []float32) {
	globalSegmentCache.mu.Lock()
	defer globalSegmentCache.mu.Unlock()
	globalSegmentCache.cache[segmentKey(sessionID, segmentIndex)] = vec
}

func (s *Service) getSegmentEmbedding(sessionID string, segmentIndex int) []float32 {
	globalSegmentCache.mu.RLock()
	defer globalSegmentCache.mu.RUnlock()
	return globalSegmentCache.cache[segmentKey(sessionID, segmentIndex)]
}

func (s *Service) clearSegmentEmbeddings(sessionID string) {
	globalSegmentCache.mu.Lock()
	defer globalSegmentCache.mu.Unlock()
	for k := range globalSegmentCache.cache {
		if len(k) > len(sessionID) && k[:len(sessionID)] == sessionID {
			delete(globalSegmentCache.cache, k)
		}
	}
}
