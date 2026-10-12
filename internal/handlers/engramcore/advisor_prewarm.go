package engramcore

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
)

const maxAdvisorSubjectProofCacheItems = 64

type advisorSubjectProof [sha256.Size]byte

type advisorProofKey struct {
	authority        grpcConnectionAuthority
	tokenFingerprint [sha256.Size]byte
}

// advisorProofCache retains only accepted, server-derived subject proofs. Its
// key deliberately excludes project identity and cwd: a proof authenticates a
// workstation channel, not a project resolution.
type advisorProofCache struct {
	mu      sync.RWMutex
	entries map[advisorProofKey]advisorSubjectProof
}

func newAdvisorProofCache() *advisorProofCache {
	return &advisorProofCache{entries: make(map[advisorProofKey]advisorSubjectProof)}
}

func advisorProofKeyFor(serverURL, token string) (advisorProofKey, error) {
	authority, err := grpcConnectionAuthorityFor(serverURL)
	if err != nil {
		return advisorProofKey{}, err
	}
	return advisorProofKey{
		authority:        authority,
		tokenFingerprint: sha256.Sum256([]byte(token)),
	}, nil
}

func parseAdvisorSubjectProof(raw []byte) (advisorSubjectProof, error) {
	if len(raw) != sha256.Size {
		return advisorSubjectProof{}, errors.New("Initialize returned an invalid authenticated subject proof")
	}
	var proof advisorSubjectProof
	copy(proof[:], raw)
	if proof == (advisorSubjectProof{}) {
		return advisorSubjectProof{}, errors.New("Initialize returned an invalid authenticated subject proof")
	}
	return proof, nil
}

// record is called only after ProxyTools has accepted the complete Initialize
// response. Empty proofs preserve old-server compatibility but remove a prior
// proof for the same authority/token channel.
func (c *advisorProofCache) record(serverURL, token string, raw []byte) error {
	if c == nil {
		return errors.New("advisor proof cache is unavailable")
	}
	key, err := advisorProofKeyFor(serverURL, token)
	if err != nil {
		return fmt.Errorf("normalize Initialize authority: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[advisorProofKey]advisorSubjectProof)
	}
	if len(raw) == 0 {
		delete(c.entries, key)
		return nil
	}
	proof, err := parseAdvisorSubjectProof(raw)
	if err != nil {
		delete(c.entries, key)
		return err
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= maxAdvisorSubjectProofCacheItems {
		return errors.New("advisor proof cache is full")
	}
	c.entries[key] = proof
	return nil
}

func (c *advisorProofCache) lookup(serverURL, token string) (advisorProofKey, advisorSubjectProof, bool, error) {
	key, err := advisorProofKeyFor(serverURL, token)
	if err != nil {
		return advisorProofKey{}, advisorSubjectProof{}, false, fmt.Errorf("normalize advisor authority: %w", err)
	}
	if c == nil {
		return key, advisorSubjectProof{}, false, nil
	}
	c.mu.RLock()
	proof, ok := c.entries[key]
	c.mu.RUnlock()
	return key, proof, ok, nil
}

func (c *advisorProofCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}
