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
	"runtime"
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
	digest   [32]byte
	info     os.FileInfo
	config   bool
	resolved string
	links    []legacyConfigLink
}

type legacyConfigLink struct {
	path   string
	target string
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
func (c *slugCache) ResolveIdentityV3(ctx context.Context, p muxcore.ProjectContext, clientInstanceID string) (*pb.ProjectIdentityV3, error) {
	c.Forget(p.ID)
	root, err := repositoryRootV3(ctx, p.Cwd)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if verifiedAnchorlessDirectoryV3(ctx, p.Cwd) {
			return nil, nil
		}
		return nil, err
	}
	anchor, err := projectidentity.DiscoverAnchorV3(ctx, root, "repository")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, projectidentity.ErrAnchorMissingV3) {
			return nil, nil
		}
		return nil, v3InputError("PROJECT_ANCHOR_INVALID")
	}
	remotes, err := normalizedGitRemotesV3(ctx, root)
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
	environment := legacyGitEnvironment()
	output, err := exec.CommandContext(ctx, "git", "-C", key.cwd, gitRevParse, "--path-format=absolute", gitShowTopLevel, "--show-prefix", "--git-path", "config", "--git-path", "index", "--git-path", "HEAD", "--git-path", "config.worktree").Output()
	if err != nil {
		return false, ctx.Err()
	}
	metadata := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(metadata) != 6 {
		return false, nil
	}
	for index := range metadata {
		metadata[index] = strings.TrimSpace(metadata[index])
	}
	root, prefix := metadata[0], metadata[1]
	if root == "" {
		return false, nil
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
	gitPaths := metadata[2:]
	gitPaths = append(gitPaths, filepath.Join(root, ".git"))
	configFiles := map[string]bool{filepath.Clean(gitPaths[0]): true, filepath.Clean(gitPaths[3]): true}
	for _, variable := range []string{"GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL"} {
		output, err := exec.CommandContext(ctx, "git", "-C", root, "var", variable).Output()
		if err != nil {
			return false, ctx.Err()
		}
		for _, configPath := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if configPath != "" {
				if !filepath.IsAbs(configPath) {
					configPath = filepath.Join(root, configPath)
				}
				configPath = filepath.Clean(configPath)
				configFiles[configPath] = true
				gitPaths = append(gitPaths, configPath)
			}
		}
	}
	for _, filename := range []string{gitPaths[1], gitPaths[2], filepath.Join(root, ".git")} {
		delete(configFiles, filepath.Clean(filename))
	}
	initial, err := legacyFileFingerprints(gitPaths, configFiles)
	if err != nil {
		return false, nil
	}
	if err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--error-unmatch", "--", ".engram-project").Run(); err != nil {
		return false, ctx.Err()
	}
	dependencies, eligible, err := legacyConfigDependencies(ctx, root)
	if err != nil {
		return false, ctx.Err()
	}
	for _, filename := range dependencies {
		configFiles[filename] = true
	}
	// Index, HEAD and the repository marker are never config-link allowances.
	for _, filename := range []string{gitPaths[1], gitPaths[2], filepath.Join(root, ".git")} {
		delete(configFiles, filepath.Clean(filename))
	}
	before, err := legacyFileFingerprints(append(gitPaths, dependencies...), configFiles)
	if err != nil || !legacyFilesUnchanged(initial) {
		return false, v3InputError("PROJECT_ANCHOR_INVALID")
	}
	slug, identity, err := resolveLegacyGitIdentity(ctx, key.cwd, name, prefix)
	if err != nil {
		return false, ctx.Err()
	}
	// Without includes or non-file origins, the fingerprinted inputs fully
	// determine the dependency set, including currently absent config files.
	afterDependencies, afterEligible := dependencies, eligible
	if !eligible {
		afterDependencies, afterEligible, err = legacyConfigDependencies(ctx, root)
	}
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
		key, content, _ := strings.Cut(value, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if strings.HasPrefix(key, "GIT_") || key == "HOME" || key == "XDG_CONFIG_HOME" || key == "USERPROFILE" || key == "HOMEDRIVE" || key == "HOMEPATH" {
			values = append(values, key+"="+content)
		}
	}
	sort.Strings(values)
	return sha256.Sum256([]byte(strings.Join(values, "\x00")))
}

func legacyFileFingerprints(paths []string, configFiles map[string]bool) (map[string]legacyFileState, error) {
	if len(paths) > 128 {
		return nil, errors.New("legacy Git dependency limit")
	}
	result := make(map[string]legacyFileState, len(paths))
	for _, filename := range paths {
		filename = filepath.Clean(strings.TrimSpace(filename))
		if configFiles[filename] {
			state, err := legacyConfigFingerprint(filename)
			if err != nil {
				return nil, err
			}
			result[filename] = state
			continue
		}
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
	configFiles := make(map[string]bool)
	for filename := range before {
		paths = append(paths, filename)
		configFiles[filename] = before[filename].config
	}
	after, err := legacyFileFingerprints(paths, configFiles)
	if err != nil {
		return false
	}
	for filename, fingerprint := range before {
		if !legacyFileStatesEqual(fingerprint, after[filename]) {
			return false
		}
	}
	return true
}

func legacySameFileMetadata(left, right os.FileInfo) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return os.SameFile(left, right) && left.Mode() == right.Mode() &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func legacyFileStatesEqual(left, right legacyFileState) bool {
	if left.digest != right.digest || left.config != right.config || left.resolved != right.resolved || len(left.links) != len(right.links) ||
		(left.info == nil) != (right.info == nil) {
		return false
	}
	if left.info != nil && (!os.SameFile(left.info, right.info) || left.info.Mode() != right.info.Mode()) {
		return false
	}
	if left.config && !legacySameFileMetadata(left.info, right.info) {
		return false
	}
	for index, link := range left.links {
		current := right.links[index]
		if link.path != current.path || link.target != current.target || !legacySameFileMetadata(link.info, current.info) {
			return false
		}
	}
	return true
}

// legacyConfigPath binds every followed link, including directory links, and
// its resolved target. Only explicitly identified Git config inputs use it.
func legacyConfigPath(filename string) (legacyFileState, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return legacyFileState{}, err
	}
	volume := filepath.VolumeName(absolute)
	resolved := volume + string(os.PathSeparator)
	pending := strings.Split(filepath.ToSlash(absolute[len(volume):]), "/")
	state := legacyFileState{config: true}
	for len(pending) != 0 {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			resolved = filepath.Dir(resolved)
			continue
		}
		candidate := filepath.Join(resolved, part)
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			state.resolved = filepath.Join(append([]string{candidate}, pending...)...)
			state.info = nil
			return state, nil
		}
		if err != nil {
			return legacyFileState{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if len(state.links) == 40 {
				return legacyFileState{}, errors.New("legacy Git config link limit")
			}
			// SameFile loads Windows' otherwise lazy file ID before any later
			// replacement can make this snapshot refer to the new link.
			if !os.SameFile(info, info) {
				return legacyFileState{}, errors.New("legacy Git config link identity unavailable")
			}
			target, err := os.Readlink(candidate)
			if err != nil {
				return legacyFileState{}, err
			}
			state.links = append(state.links, legacyConfigLink{path: candidate, target: target, info: info})
			if filepath.IsAbs(target) {
				volume = filepath.VolumeName(target)
				resolved = volume + string(os.PathSeparator)
				target = target[len(volume):]
			} else if filepath.VolumeName(target) != "" {
				return legacyFileState{}, errors.New("legacy Git config link target unsupported")
			}
			pending = append(strings.Split(filepath.ToSlash(target), "/"), pending...)
			continue
		}
		if len(pending) != 0 && !info.IsDir() {
			return legacyFileState{}, errors.New("legacy Git config path unsupported")
		}
		resolved = candidate
		state.info = info
	}
	state.resolved = resolved
	if state.info != nil && !os.SameFile(state.info, state.info) {
		return legacyFileState{}, errors.New("legacy Git config target identity unavailable")
	}
	return state, nil
}

func legacyConfigFingerprint(filename string) (legacyFileState, error) {
	state, err := legacyConfigPath(filename)
	if err != nil || state.info == nil {
		return state, err
	}
	if !state.info.Mode().IsRegular() || state.info.Size() > 1024*1024 {
		return legacyFileState{}, errors.New("legacy Git config dependency unsupported")
	}
	file, err := os.Open(state.resolved)
	if err != nil {
		return legacyFileState{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !legacySameFileMetadata(state.info, opened) {
		return legacyFileState{}, errors.New("legacy Git config target changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return legacyFileState{}, errors.New("legacy Git config read unsupported")
	}
	finished, err := file.Stat()
	if err != nil || !legacySameFileMetadata(opened, finished) {
		return legacyFileState{}, errors.New("legacy Git config target changed")
	}
	after, err := legacyConfigPath(filename)
	if err != nil || !legacyFileStatesEqual(state, after) {
		return legacyFileState{}, errors.New("legacy Git config links changed")
	}
	state.info = opened
	state.digest = sha256.Sum256(data)
	return state, nil
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

func repositoryRootV3(ctx context.Context, cwd string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", cwd, gitRevParse, gitShowTopLevel)
	command.Env = projectidentity.RepositoryGitEnvironmentV3()
	output, err := command.Output()
	root := strings.TrimSpace(string(output))
	if err != nil || root == "" {
		return "", v3InputError("PROJECT_ANCHOR_INVALID")
	}
	return root, nil
}

func verifiedAnchorlessDirectoryV3(ctx context.Context, cwd string) bool {
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
	command := exec.CommandContext(ctx, "git", "-C", root, gitRevParse, gitShowTopLevel)
	command.Env = projectidentity.RepositoryGitEnvironmentV3()
	output, err := command.Output()
	return ctx.Err() == nil && err != nil && strings.TrimSpace(string(output)) == ""
}

func normalizedGitRemotesV3(ctx context.Context, root string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "config", "--get-regexp", `^remote\..*\.url$`)
	command.Env = projectidentity.RepositoryGitEnvironmentV3()
	output, err := command.Output()
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
