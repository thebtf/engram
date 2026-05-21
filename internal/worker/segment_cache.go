package worker

import (
	"strconv"
	"sync"
)

// segmentEmbeddingCache stores per-session, per-segment running centroid
// embeddings in memory. These are ephemeral — lost on server restart, which is
// acceptable since segments are re-created from scratch in a new session.
//
// Hard size cap prevents memory exhaustion when sessions never call
// session-end (crashed clients, attacker spamming random session_id values).
// On overflow we drop a random entry — segment centroids degrade gracefully
// (worst case: one segment loses its running centroid and falls back to
// uniform sampling on its next prompt).
type segmentEmbeddingCache struct {
	mu    sync.RWMutex
	cache map[string][]float32 // key: "sessionID:segmentIndex"
}

const segmentCacheMaxEntries = 2000

var globalSegmentCache = &segmentEmbeddingCache{
	cache: make(map[string][]float32),
}

func segmentKey(sessionID string, segmentIndex int) string {
	return sessionID + ":" + strconv.Itoa(segmentIndex)
}

func (s *Service) storeSegmentEmbedding(sessionID string, segmentIndex int, vec []float32) {
	globalSegmentCache.mu.Lock()
	defer globalSegmentCache.mu.Unlock()
	if len(globalSegmentCache.cache) >= segmentCacheMaxEntries {
		// Drop one entry at random to bound memory. Map iteration order is
		// already randomized in Go, so the first key is effectively random.
		for k := range globalSegmentCache.cache {
			delete(globalSegmentCache.cache, k)
			break
		}
	}
	globalSegmentCache.cache[segmentKey(sessionID, segmentIndex)] = vec
}

func (s *Service) getSegmentEmbedding(sessionID string, segmentIndex int) []float32 {
	globalSegmentCache.mu.RLock()
	defer globalSegmentCache.mu.RUnlock()
	return globalSegmentCache.cache[segmentKey(sessionID, segmentIndex)]
}

func (s *Service) clearSegmentEmbeddings(sessionID string) {
	prefix := sessionID + ":"
	globalSegmentCache.mu.Lock()
	defer globalSegmentCache.mu.Unlock()
	for k := range globalSegmentCache.cache {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(globalSegmentCache.cache, k)
		}
	}
}
