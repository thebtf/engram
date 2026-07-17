package engramcore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/thebtf/engram/internal/proxy"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// slugCache caches the compatibility slug and v2 project identity by project
// and cwd. One muxcore project ID can serve sessions rooted at different
// subdirectories, and their routing metadata must stay aligned.
//
// Thread-safety: the RWMutex makes invalidation wait for every in-flight
// resolution through its cache write; sync.Map keeps concurrent project reads
// independent while no invalidation is running.
type slugCache struct {
	mu         sync.RWMutex
	entries    sync.Map // slugCacheKey → resolvedSlug
	identities sync.Map // slugCacheKey → *pb.ProjectIdentityV2
}

type slugCacheKey struct {
	projectID string
	cwd       string
}

// resolvedSlug is the cached value — project slug + whether logging has
// already announced this project. The announce flag prevents spamming the
// stderr log once per subsequent request on the same ID.
type resolvedSlug struct {
	id       string
	announce bool
}

func cacheKey(p muxcore.ProjectContext) slugCacheKey {
	cwd, err := filepath.Abs(p.Cwd)
	if err != nil {
		cwd = filepath.Clean(p.Cwd)
	}
	return slugCacheKey{projectID: p.ID, cwd: cwd}
}

// Resolve returns the engram project slug for the given session and cwd. On
// first lookup it calls proxy.ResolveProjectSlug and logs the result once.
//
// On error it falls back to the muxcore-provided ID (which is already
// git-hash-derived inside muxcore's session layer) so the daemon never
// fails to respond due to a git lookup hiccup.
func (c *slugCache) Resolve(p muxcore.ProjectContext) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := cacheKey(p)
	if cached, ok := c.entries.Load(key); ok {
		return cached.(resolvedSlug).id
	}

	id, displayName, remote, err := proxy.ResolveProjectSlug(p.Cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[engram] warning: project identity failed for %s: %v\n", p.Cwd, err)
		id = p.ID
		displayName = filepath.Base(p.Cwd)
	}
	if remote != "" {
		fmt.Fprintf(os.Stderr, "[engram] project: %s (%s, remote: %s)\n", displayName, id, safeRemoteURL(remote))
	} else {
		fmt.Fprintf(os.Stderr, "[engram] project: %s (%s)\n", displayName, id)
	}

	c.entries.Store(key, resolvedSlug{id: id, announce: true})
	return id
}

// ResolveIdentity returns stable v2 metadata for the given project and cwd.
// The first successful resolution is reused until OnProjectRemoved calls
// Forget, avoiding synchronous git subprocesses on every tool request.
func (c *slugCache) ResolveIdentity(p muxcore.ProjectContext) (*pb.ProjectIdentityV2, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := cacheKey(p)
	if cached, ok := c.identities.Load(key); ok {
		return cached.(*pb.ProjectIdentityV2), nil
	}

	identity, err := resolveProjectIdentityV2(p.Cwd)
	if err != nil {
		return nil, err
	}
	stored, _ := c.identities.LoadOrStore(key, identity)
	return stored.(*pb.ProjectIdentityV2), nil
}

// Forget removes every cwd-scoped cache entry for a project ID. Called from
// Module.OnProjectRemoved so a subsequent session does not reuse stale identity
// metadata from any subdirectory.
func (c *slugCache) Forget(projectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries.Range(func(rawKey, _ any) bool {
		key := rawKey.(slugCacheKey)
		if key.projectID == projectID {
			c.entries.Delete(key)
		}
		return true
	})
	c.identities.Range(func(rawKey, _ any) bool {
		key := rawKey.(slugCacheKey)
		if key.projectID == projectID {
			c.identities.Delete(key)
		}
		return true
	})
}

// ForceCacheEntry injects a synthetic entry for one project/cwd pair. Test-only.
func (c *slugCache) ForceCacheEntry(p muxcore.ProjectContext, slug string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.entries.Store(cacheKey(p), resolvedSlug{id: slug, announce: true})
}

// HasEntry reports whether any cwd-scoped entry exists for projectID. Test-only.
func (c *slugCache) HasEntry(projectID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	found := false
	c.entries.Range(func(rawKey, _ any) bool {
		if rawKey.(slugCacheKey).projectID == projectID {
			found = true
			return false
		}
		return true
	})
	return found
}
