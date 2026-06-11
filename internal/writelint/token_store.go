// Package writelint — T031 (engram vNext Milestone F TG5).
// TokenStore is an in-memory TTL store for write-lint resolution tokens.
// Per spec §Clarifications C2 + ADR-F-002: tokens live in a mutex-guarded
// map with a janitor goroutine purging expired entries every 60s (configurable).
// TTL default is 600s (ENGRAM_WRITE_LINT_TOKEN_TTL_SEC env override).
package writelint

import (
	"sync"
	"time"
)

// tokenEntry holds a payload value and its expiry time.
type tokenEntry struct {
	payload   string
	expiresAt time.Time
}

// TokenStoreConfig configures a TokenStore instance.
type TokenStoreConfig struct {
	// TTL is the default time-to-live for stored tokens.
	TTL time.Duration
	// JanitorInterval controls how often expired entries are purged.
	// Defaults to 60s per ADR-F-002.
	JanitorInterval time.Duration
}

// DefaultTokenStoreConfig returns the production-default configuration.
// TTL = 600s per ENGRAM_WRITE_LINT_TOKEN_TTL_SEC env; JanitorInterval = 60s.
func DefaultTokenStoreConfig() TokenStoreConfig {
	return TokenStoreConfig{
		TTL:             600 * time.Second,
		JanitorInterval: 60 * time.Second,
	}
}

// TokenStore is the interface for write-lint resolution token storage.
// Separated from the implementation so tests and future Redis backends can
// substitute it (per ADR-F-002 reversibility contract).
type TokenStore interface {
	// Put stores a token with the given payload and TTL.
	Put(key, payload string, ttl time.Duration) error
	// Get retrieves a token. Returns (payload, ok=true, expired=false) for
	// live tokens; (payload, ok=true, expired=true) for expired-but-not-purged
	// tokens; ("", ok=false, expired=false) when the key was never stored or
	// has been purged.
	Get(key string) (payload string, ok bool, expired bool)
	// Consume atomically retrieves and removes a token in one lock acquisition.
	// Returns the same semantics as Get. Subsequent calls for the same key will
	// return ok=false, satisfying the single-use guarantee per EC-F2.
	Consume(key string) (payload string, ok bool, expired bool)
	// Delete removes a token immediately. Idempotent — no error if absent.
	Delete(key string)
	// Close stops the janitor goroutine and releases resources.
	Close()
}

// memTokenStore is the in-process, mutex-guarded implementation of TokenStore.
type memTokenStore struct {
	mu         sync.RWMutex
	entries    map[string]tokenEntry
	done       chan struct{}
	closeOnce  sync.Once
	defaultTTL time.Duration
}

// NewTokenStore creates a new in-memory TokenStore and starts its janitor.
func NewTokenStore(cfg TokenStoreConfig) TokenStore {
	if cfg.TTL <= 0 {
		cfg.TTL = 600 * time.Second
	}
	if cfg.JanitorInterval <= 0 {
		cfg.JanitorInterval = 60 * time.Second
	}
	s := &memTokenStore{
		entries:    make(map[string]tokenEntry),
		done:       make(chan struct{}),
		defaultTTL: cfg.TTL,
	}
	go s.janitor(cfg.JanitorInterval)
	return s
}

// Put stores a token. Thread-safe.
// If ttl <= 0 the store's configured default TTL is used.
func (s *memTokenStore) Put(key, payload string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = s.defaultTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = tokenEntry{
		payload:   payload,
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}

// Get retrieves a token. Thread-safe. Returns expired=true when TTL has lapsed
// but the janitor has not yet purged the entry — callers must treat expired
// entries as unusable.
func (s *memTokenStore) Get(key string) (string, bool, bool) {
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok {
		return "", false, false
	}
	if time.Now().After(entry.expiresAt) {
		return entry.payload, true, true
	}
	return entry.payload, true, false
}

// Consume atomically retrieves and removes a token. Thread-safe.
// Returns the same semantics as Get; the entry is deleted regardless of expiry.
func (s *memTokenStore) Consume(key string) (string, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return "", false, false
	}
	delete(s.entries, key)
	if time.Now().After(entry.expiresAt) {
		return entry.payload, true, true
	}
	return entry.payload, true, false
}

// Delete removes a token immediately. Thread-safe. Idempotent.
func (s *memTokenStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// Close stops the janitor goroutine. Safe to call multiple times — uses sync.Once
// so concurrent calls cannot panic on a double-close of the done channel.
func (s *memTokenStore) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
	})
}

// janitor runs on the configured interval and purges expired entries.
// Terminates when the done channel is closed.
func (s *memTokenStore) janitor(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.purgeExpired()
		}
	}
}

// purgeExpired removes all entries whose TTL has lapsed.
func (s *memTokenStore) purgeExpired() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, key)
		}
	}
}
