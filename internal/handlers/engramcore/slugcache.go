package engramcore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/thebtf/engram/internal/proxy"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// slugCache is the project-ID resolution cache. The cwd is part of the key:
// one muxcore project ID can serve sessions rooted at different subdirectories,
// and their compatibility selectors must stay aligned with their v2 identities.
//
// Thread-safety: sync.Map — Resolve is called from muxcore dispatch
// goroutines and may race with OnProjectRemoved clearing entries.
type slugCache struct {
	entries sync.Map // slugCacheKey → resolvedSlug
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

// Forget removes every cwd-scoped cache entry for a project ID. Called from
// Module.OnProjectRemoved so a subsequent session does not reuse stale identity
// metadata from any subdirectory.
func (c *slugCache) Forget(projectID string) {
	c.entries.Range(func(rawKey, _ any) bool {
		key := rawKey.(slugCacheKey)
		if key.projectID == projectID {
			c.entries.Delete(key)
		}
		return true
	})
}

// ForceCacheEntry injects a synthetic entry for one project/cwd pair. Test-only.
func (c *slugCache) ForceCacheEntry(p muxcore.ProjectContext, slug string) {
	c.entries.Store(cacheKey(p), resolvedSlug{id: slug, announce: true})
}

// HasEntry reports whether any cwd-scoped entry exists for projectID. Test-only.
func (c *slugCache) HasEntry(projectID string) bool {
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
