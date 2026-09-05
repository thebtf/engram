package engramcore

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/thebtf/engram/internal/projectidentity"
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

// ResolveCompatibilityEvidence derives one non-authoritative legacy selector
// without retaining it or substituting a project ID when derivation fails.
func (c *slugCache) ResolveCompatibilityEvidence(p muxcore.ProjectContext) (string, error) {
	slug, _, _, err := proxy.ResolveProjectSlug(p.Cwd)
	if err != nil || slug == "" {
		return "", errors.New("compatibility evidence unavailable")
	}
	return slug, nil
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

// projectIdentityV3InputError carries only a stable public refusal code. It
// never retains the local anchor, path, remote, or process error.
type projectIdentityV3InputError struct{ code string }

func (e *projectIdentityV3InputError) Error() string { return e.code }

func v3InputError(code string) error { return &projectIdentityV3InputError{code: code} }

// ResolveIdentityV3 builds fresh V3 descriptor evidence. Unlike V2, it neither
// reads nor retains a slug/identity cache: server resolution owns scoped state.
func (c *slugCache) ResolveIdentityV3(p muxcore.ProjectContext, clientInstanceID string) (*pb.ProjectIdentityV3, error) {
	c.Forget(p.ID)
	root, err := repositoryRootV3(p.Cwd)
	if err != nil {
		return nil, err
	}
	anchor, err := projectidentity.DiscoverAnchorV3(root, "repository")
	if err != nil {
		return nil, v3InputError("PROJECT_ANCHOR_INVALID")
	}
	remotes, err := normalizedGitRemotesV3(root)
	if err != nil {
		return nil, err
	}
	descriptor, err := projectidentity.BuildDescriptorV3(anchor, remotes, nil, clientInstanceID)
	if err != nil {
		return nil, v3InputError("PROJECT_DESCRIPTOR_INVALID")
	}
	return &pb.ProjectIdentityV3{
		Version:              uint32(descriptor.Version),
		AnchorProjectId:      descriptor.AnchorProjectID,
		Name:                 descriptor.Name,
		Scope:                descriptor.Scope,
		NormalizedGitRemotes: descriptor.NormalizedGitRemotes,
		LegacyIdentifiers:    []*pb.ProjectLegacyIdentifierV3{},
		ClientInstanceId:     descriptor.ClientInstanceID,
	}, nil
}

func repositoryRootV3(cwd string) (string, error) {
	output, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	root := strings.TrimSpace(string(output))
	if err != nil || root == "" {
		return "", v3InputError("PROJECT_ANCHOR_INVALID")
	}
	return root, nil
}

func normalizedGitRemotesV3(root string) ([]string, error) {
	output, err := exec.Command("git", "-C", root, "config", "--get-regexp", `^remote\..*\.url$`).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return []string{}, nil
		}
		return nil, v3InputError("PROJECT_DESCRIPTOR_INVALID")
	}

	remotes := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		separator := strings.IndexAny(line, "\t ")
		if separator <= 0 {
			return nil, v3InputError("PROJECT_DESCRIPTOR_INVALID")
		}
		rawRemote := strings.TrimSpace(line[separator+1:])
		normalized, disposition, normalizeErr := projectidentity.NormalizeGitRemoteV3(rawRemote)
		if normalizeErr != nil || disposition == projectidentity.RemoteRefusedV3 {
			return nil, v3InputError("PROJECT_DESCRIPTOR_INVALID")
		}
		if disposition == projectidentity.RemoteNormalizedV3 {
			remotes = append(remotes, normalized)
		}
	}
	sort.Strings(remotes)
	return compactStrings(remotes), nil
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	end := 1
	for _, value := range values[1:] {
		if value != values[end-1] {
			values[end] = value
			end++
		}
	}
	return values[:end]
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
