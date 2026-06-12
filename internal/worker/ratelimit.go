// Package worker provides the main worker service for engram.
package worker

import (
	"net/http"
	"sync"
	"time"
)

// RateLimiter is a token-bucket rate limiter. Tokens replenish at the
// configured rate and are capped at the burst maximum.
type RateLimiter struct {
	updatedAt time.Time
	rate      float64
	burst     int
	available float64
	totalReqs int64
	dropped   int64
	mu        sync.Mutex
}

// LastUpdateTime returns the timestamp of the most recent token replenishment.
// Acquires the mutex before reading, making this safe for concurrent callers.
func (rl *RateLimiter) LastUpdateTime() time.Time {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.updatedAt
}

// lastUpdateTimeUnlocked reads the last update timestamp without acquiring the mutex.
// The caller must already hold rl.mu.
func (rl *RateLimiter) lastUpdateTimeUnlocked() time.Time {
	return rl.updatedAt
}

// NewRateLimiter builds a token-bucket limiter.
// rate is the sustained token refill rate (requests per second).
// burst is the maximum number of tokens that can accumulate.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	return &RateLimiter{
		rate:      rate,
		burst:     burst,
		available: float64(burst),
		updatedAt: time.Now(),
	}
}

// Allow returns true when the request is permitted and consumes one token.
// Returns false when the bucket is empty, incrementing the rejection counter.
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.totalReqs++

	// Replenish tokens proportional to elapsed time, capped at burst.
	now := time.Now()
	elapsed := now.Sub(rl.updatedAt).Seconds()
	rl.available += elapsed * rl.rate
	if rl.available > float64(rl.burst) {
		rl.available = float64(rl.burst)
	}
	rl.updatedAt = now

	if rl.available >= 1 {
		rl.available--
		return true
	}

	rl.dropped++
	return false
}

// Stats returns a snapshot of the limiter's current configuration and counters.
func (rl *RateLimiter) Stats() map[string]any {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return map[string]any{
		"rate":           rl.rate,
		"burst":          rl.burst,
		"current_tokens": rl.available,
		"total_requests": rl.totalReqs,
		"rejected":       rl.dropped,
		"rejection_rate": float64(rl.dropped) / max(float64(rl.totalReqs), 1),
	}
}

// RateLimitMiddleware wraps a handler with a shared token-bucket rate limiter.
// Requests that exceed the limit receive 429 Too Many Requests.
func RateLimitMiddleware(limiter *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow() {
				http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PerClientRateLimiter maintains independent token-bucket limiters per client key.
// Idle limiters are evicted periodically to bound memory usage.
type PerClientRateLimiter struct {
	cleanedAt       time.Time
	entries         map[string]*RateLimiter
	rate            float64
	burst           int
	sweepInterval   time.Duration
	idleExpiry      time.Duration
	mu              sync.Mutex
}

// NewPerClientRateLimiter creates a per-client limiter with the given rate and burst.
// Entries idle longer than 10 minutes are pruned every 5 minutes.
func NewPerClientRateLimiter(rate float64, burst int) *PerClientRateLimiter {
	return &PerClientRateLimiter{
		rate:          rate,
		burst:         burst,
		entries:       make(map[string]*RateLimiter),
		sweepInterval: 5 * time.Minute,
		idleExpiry:    10 * time.Minute,
		cleanedAt:     time.Now(),
	}
}

// limiterFor retrieves or creates the per-client limiter for key.
// A periodic sweep evicts entries that have been idle longer than idleExpiry.
func (pcrl *PerClientRateLimiter) limiterFor(key string) *RateLimiter {
	pcrl.mu.Lock()
	defer pcrl.mu.Unlock()

	if time.Since(pcrl.cleanedAt) > pcrl.sweepInterval {
		pcrl.sweepLocked()
	}

	lim, found := pcrl.entries[key]
	if !found {
		lim = NewRateLimiter(pcrl.rate, pcrl.burst)
		pcrl.entries[key] = lim
	}

	return lim
}

// sweepLocked removes per-client limiters that have been idle past idleExpiry.
// Caller must hold pcrl.mu. Acquires each entry's mutex briefly to read its
// last-update timestamp — the order (pcrl.mu then entry.mu) is consistent
// throughout this type, so no deadlock can occur.
func (pcrl *PerClientRateLimiter) sweepLocked() {
	cutoff := time.Now()
	var stale []string

	for key, lim := range pcrl.entries {
		lim.mu.Lock()
		lastSeen := lim.lastUpdateTimeUnlocked()
		lim.mu.Unlock()

		if cutoff.Sub(lastSeen) > pcrl.idleExpiry {
			stale = append(stale, key)
		}
	}

	for _, key := range stale {
		delete(pcrl.entries, key)
	}
	pcrl.cleanedAt = cutoff
}

// Allow returns whether the request from clientKey is within its rate limit.
func (pcrl *PerClientRateLimiter) Allow(clientKey string) bool {
	return pcrl.limiterFor(clientKey).Allow()
}

// Stats aggregates counters across all tracked clients.
// Uses a two-phase approach to avoid nested lock acquisition: collect the
// entry pointers under pcrl.mu, then read each entry's counters separately.
func (pcrl *PerClientRateLimiter) Stats() map[string]any {
	// Phase 1: snapshot entry slice under the parent lock.
	pcrl.mu.Lock()
	rate := pcrl.rate
	burst := pcrl.burst
	count := len(pcrl.entries)
	snapshot := make([]*RateLimiter, 0, count)
	for _, lim := range pcrl.entries {
		snapshot = append(snapshot, lim)
	}
	pcrl.mu.Unlock()

	// Phase 2: read each entry's counters under its own lock only.
	var totalReqs, totalDropped int64
	for _, lim := range snapshot {
		lim.mu.Lock()
		totalReqs += lim.totalReqs
		totalDropped += lim.dropped
		lim.mu.Unlock()
	}

	return map[string]any{
		"rate":           rate,
		"burst":          burst,
		"active_clients": count,
		"total_requests": totalReqs,
		"total_rejected": totalDropped,
	}
}

// PerClientRateLimitMiddleware wraps a handler with per-client rate limiting.
// The client key is the X-Real-IP header when present, or RemoteAddr otherwise.
func PerClientRateLimitMiddleware(limiter *PerClientRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// X-Real-IP is set by the RealIP middleware that runs earlier in the chain.
			key := r.RemoteAddr
			if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
				key = realIP
			}

			if !limiter.Allow(key) {
				http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
