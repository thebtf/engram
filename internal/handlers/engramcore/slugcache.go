package engramcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/proxy"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

const (
	gitRevParse     = "rev-parse"
	gitShowTopLevel = "--show-toplevel"
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
	legacy     map[slugCacheKey]legacyWorkspace
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

type legacyWorkspace struct {
	root          string
	selected      os.FileInfo
	marker        []byte
	files         map[string]legacyFileState
	environment   [32]byte
	cacheEligible bool
}

type legacyFileState struct {
	digest [32]byte
	info   os.FileInfo
}

var resolveLegacyGitIdentity = proxy.ResolveGitProjectIdentityV2

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
// Non-cancellation errors fall back to the muxcore-provided ID (which is
// already git-hash-derived inside muxcore's session layer) so the daemon never
// fails to respond due to a git lookup hiccup. Caller and Git-derived
// cancellation/deadline errors are transient and return that fallback only to
// the caller, leaving the key uncached.
func (c *slugCache) Resolve(ctx context.Context, p muxcore.ProjectContext) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := cacheKey(p)
	if cached, ok := c.entries.Load(key); ok {
		return cached.(resolvedSlug).id
	}

	id, displayName, remote, err := proxy.ResolveProjectSlug(ctx, p.Cwd)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return p.ID
		}
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
	slug, _, _, err := proxy.ResolveProjectSlug(context.Background(), p.Cwd)
	if err != nil || slug == "" {
		return "", errors.New("compatibility evidence unavailable")
	}
	return slug, nil
}

// ResolveIdentity returns stable v2 metadata for the given project and cwd.
// The first successful resolution is reused until OnProjectRemoved calls
// Forget, avoiding synchronous git subprocesses on every tool request.
func (c *slugCache) ResolveIdentity(ctx context.Context, p muxcore.ProjectContext) (*pb.ProjectIdentityV2, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := cacheKey(p)
	if cached, ok := c.identities.Load(key); ok {
		return cached.(*pb.ProjectIdentityV2), nil
	}

	identity, err := resolveProjectIdentityV2(ctx, p.Cwd)
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
// A nil descriptor with a nil error is a verified authority-free root usable
// only for unscoped UCI discovery; it never grants scoped V3 authority.
func (c *slugCache) ResolveIdentityV3(p muxcore.ProjectContext, clientInstanceID string) (*pb.ProjectIdentityV3, error) {
	c.Forget(p.ID)
	root, err := repositoryRootV3(p.Cwd)
	if err != nil {
		if verifiedAnchorlessDirectoryV3(p.Cwd) {
			return nil, nil
		}
		return nil, err
	}
	anchor, err := projectidentity.DiscoverAnchorV3(root, "repository")
	if err != nil {
		if verifiedUnbornRepositoryV3(p.Cwd, root) {
			return nil, nil
		}
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

// resolveLegacyWorkspace retains only the bounded name-only workspace route.
// Marker bytes and Git config/index freshness are checked before cache reuse.
func (c *slugCache) resolveLegacyWorkspace(ctx context.Context, p muxcore.ProjectContext) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	key := cacheKey(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	selectedInfo, err := os.Stat(key.cwd)
	if err != nil || !selectedInfo.IsDir() {
		return false, ctx.Err()
	}
	if cached, ok := c.legacy[key]; ok {
		raw, err := os.ReadFile(filepath.Join(cached.root, ".engram-project"))
		if cached.cacheEligible && err == nil && bytes.Equal(raw, cached.marker) && os.SameFile(selectedInfo, cached.selected) &&
			cached.environment == legacyGitEnvironment() && legacyFilesUnchanged(cached.files) &&
			legacySelectedScopeUnchanged(key.cwd, cached.root) {
			return true, ctx.Err()
		}
		delete(c.legacy, key)
		c.entries.Delete(key)
		c.identities.Delete(key)
	}
	output, err := exec.CommandContext(ctx, "git", "-C", key.cwd, gitRevParse, gitShowTopLevel).Output()
	root := strings.TrimSpace(string(output))
	if err != nil || root == "" {
		return false, ctx.Err()
	}
	if !legacySelectedScopeUnchanged(key.cwd, root) {
		return false, nil
	}
	raw, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil || !utf8.Valid(raw) {
		return false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false, nil
	}
	field, err := decoder.Token()
	if err != nil || field != "name" {
		return false, nil
	}
	var name string
	if err := decoder.Decode(&name); err != nil || name == "" ||
		utf8.RuneCountInString(name) > 256 || strings.TrimSpace(name) != name ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false, nil
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false, nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false, nil
	}
	paths, err := exec.CommandContext(ctx, "git", "-C", root, gitRevParse, "--path-format=absolute", "--git-path", "config", "--git-path", "index", "--git-path", "HEAD", "--git-path", "config.worktree").Output()
	if err != nil {
		return false, ctx.Err()
	}
	gitPaths := strings.Split(strings.TrimSpace(string(paths)), "\n")
	if len(gitPaths) != 4 {
		return false, nil
	}
	gitPaths = append(gitPaths, filepath.Join(root, ".git"))
	for _, variable := range []string{"GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL"} {
		output, err := exec.CommandContext(ctx, "git", "-C", root, "var", variable).Output()
		if err != nil {
			return false, ctx.Err()
		}
		for _, configPath := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if configPath != "" {
				gitPaths = append(gitPaths, configPath)
			}
		}
	}
	initial, err := legacyFileFingerprints(gitPaths)
	if err != nil {
		return false, nil
	}
	if err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--error-unmatch", "--", ".engram-project").Run(); err != nil {
		return false, ctx.Err()
	}
	environment := legacyGitEnvironment()
	dependencies, eligible, err := legacyConfigDependencies(ctx, root)
	if err != nil {
		return false, ctx.Err()
	}
	before, err := legacyFileFingerprints(append(gitPaths, dependencies...))
	if err != nil || !legacyFilesUnchanged(initial) {
		return false, v3InputError("PROJECT_ANCHOR_INVALID")
	}
	slug, identity, err := resolveLegacyGitIdentity(ctx, key.cwd, name)
	if err != nil {
		return false, ctx.Err()
	}
	afterDependencies, afterEligible, err := legacyConfigDependencies(ctx, root)
	afterRaw, markerErr := os.ReadFile(filepath.Join(root, ".engram-project"))
	afterSelected, selectedErr := os.Stat(key.cwd)
	if err != nil || markerErr != nil || selectedErr != nil || !os.SameFile(selectedInfo, afterSelected) || !bytes.Equal(raw, afterRaw) ||
		strings.Join(dependencies, "\x00") != strings.Join(afterDependencies, "\x00") || eligible != afterEligible ||
		environment != legacyGitEnvironment() || !legacyFilesUnchanged(before) || !legacySelectedScopeUnchanged(key.cwd, root) {
		return false, v3InputError("PROJECT_ANCHOR_INVALID")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c.legacy == nil {
		c.legacy = make(map[slugCacheKey]legacyWorkspace)
	}
	if len(c.legacy) == 64 {
		for oldKey := range c.legacy {
			delete(c.legacy, oldKey)
			c.entries.Delete(oldKey)
			c.identities.Delete(oldKey)
			break
		}
	}
	c.legacy[key] = legacyWorkspace{root: root, selected: selectedInfo, marker: raw, files: before, environment: environment, cacheEligible: eligible}
	c.entries.Store(key, resolvedSlug{id: slug, announce: true})
	c.identities.Store(key, &pb.ProjectIdentityV2{Version: identity.Version, LegacyProjectId: identity.LegacyProjectID, DisplayName: identity.DisplayName, GitRemote: identity.GitRemote, RelativePath: identity.RelativePath})
	return true, nil
}

func legacyGitEnvironment() [32]byte {
	values := make([]string, 0)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "GIT_") || key == "HOME" || key == "XDG_CONFIG_HOME" || key == "USERPROFILE" || key == "HOMEDRIVE" || key == "HOMEPATH" {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return sha256.Sum256([]byte(strings.Join(values, "\x00")))
}

func legacyFileFingerprints(paths []string) (map[string]legacyFileState, error) {
	if len(paths) > 128 {
		return nil, errors.New("legacy Git dependency limit")
	}
	result := make(map[string]legacyFileState, len(paths))
	for _, filename := range paths {
		filename = filepath.Clean(strings.TrimSpace(filename))
		info, err := os.Lstat(filename)
		if errors.Is(err, os.ErrNotExist) {
			result[filename] = legacyFileState{}
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			result[filename] = legacyFileState{info: info}
			continue
		}
		if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return nil, errors.New("legacy Git dependency unsupported")
		}
		bytes, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		result[filename] = legacyFileState{digest: sha256.Sum256(bytes), info: info}
	}
	return result, nil
}

func legacyFilesUnchanged(before map[string]legacyFileState) bool {
	paths := make([]string, 0, len(before))
	for filename := range before {
		paths = append(paths, filename)
	}
	after, err := legacyFileFingerprints(paths)
	if err != nil {
		return false
	}
	for filename, fingerprint := range before {
		current := after[filename]
		if current.digest != fingerprint.digest || (current.info == nil) != (fingerprint.info == nil) ||
			current.info != nil && (!os.SameFile(current.info, fingerprint.info) || current.info.Mode() != fingerprint.info.Mode()) {
			return false
		}
	}
	return true
}

func legacyConfigDependencies(ctx context.Context, root string) ([]string, bool, error) {
	output, err := exec.CommandContext(ctx, "git", "-C", root, "config", "--null", "--show-origin", "--includes", "--name-only", "--list").Output()
	if err != nil {
		return nil, false, err
	}
	fields := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	if len(fields)%2 != 0 || len(fields) > 8192 {
		return nil, false, errors.New("legacy Git config origins unsupported")
	}
	paths := make([]string, 0)
	eligible := true
	for index := 0; index < len(fields); index += 2 {
		origin, key := fields[index], strings.ToLower(fields[index+1])
		if strings.HasPrefix(origin, "file:") {
			filename := strings.TrimPrefix(origin, "file:")
			if !filepath.IsAbs(filename) {
				filename = filepath.Join(root, filename)
			}
			paths = append(paths, filepath.Clean(filename))
		} else {
			eligible = false
		}
		// Conditional/missing include targets are not complete filesystem
		// dependencies. Derive afresh instead of reusing their scoped identity.
		if strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") {
			eligible = false
		}
	}
	sort.Strings(paths)
	return compactStrings(paths), eligible, nil
}

func legacySelectedScopeUnchanged(selected, root string) bool {
	selected, root = filepath.Clean(selected), filepath.Clean(root)
	selected, err := filepath.EvalSymlinks(selected)
	if err != nil {
		return false
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return false
	}
	for {
		selectedInfo, err := os.Stat(selected)
		if err != nil || !selectedInfo.IsDir() {
			return false
		}
		if os.SameFile(selectedInfo, rootInfo) {
			return true
		}
		for _, marker := range []string{".engram-project", ".git"} {
			if _, err := os.Lstat(filepath.Join(selected, marker)); !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
		parent := filepath.Dir(selected)
		if parent == selected {
			return false
		}
		selected = parent
	}
}

func repositoryRootV3(cwd string) (string, error) {
	output, err := exec.Command("git", "-C", cwd, gitRevParse, gitShowTopLevel).Output()
	root := strings.TrimSpace(string(output))
	if err != nil || root == "" {
		return "", v3InputError("PROJECT_ANCHOR_INVALID")
	}
	return root, nil
}

func verifiedUnbornRepositoryV3(cwd, root string) bool {
	prefix, err := exec.Command("git", "-C", cwd, gitRevParse, "--show-prefix").Output()
	if err != nil || strings.TrimSpace(string(prefix)) != "" {
		return false
	}
	if _, err := os.Lstat(filepath.Join(root, ".engram-project")); err == nil || !errors.Is(err, os.ErrNotExist) {
		return false
	}
	parentRoot, err := exec.Command("git", "-C", filepath.Dir(root), gitRevParse, gitShowTopLevel).Output()
	if err == nil && filepath.Clean(strings.TrimSpace(string(parentRoot))) != filepath.Clean(root) {
		return false
	}
	objectFormat, err := exec.Command("git", "-C", root, gitRevParse, "--show-object-format").Output()
	if err != nil || (strings.TrimSpace(string(objectFormat)) != "sha1" && strings.TrimSpace(string(objectFormat)) != "sha256") {
		return false
	}
	head, err := exec.Command("git", "-C", root, gitRevParse, "--verify", "HEAD").Output()
	if err == nil || strings.TrimSpace(string(head)) != "" {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || (exitErr.ExitCode() != 1 && exitErr.ExitCode() != 128) {
		return false
	}
	refLabel, err := exec.Command("git", "-C", root, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	return err == nil && strings.TrimSpace(string(refLabel)) != ""
}

func verifiedAnchorlessDirectoryV3(cwd string) bool {
	root := filepath.Clean(strings.TrimSpace(cwd))
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return false
	}
	for _, marker := range []string{".git", ".engram-project"} {
		if _, err := os.Lstat(filepath.Join(root, marker)); err == nil || !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	output, err := exec.Command("git", "-C", root, gitRevParse, gitShowTopLevel).Output()
	return err != nil && strings.TrimSpace(string(output)) == ""
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
	for key := range c.legacy {
		if key.projectID == projectID {
			delete(c.legacy, key)
		}
	}
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
