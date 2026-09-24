package codeintel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/uci"
)

const (
	// EnvUCIParserBundleDigest names the trusted daemon-startup configuration
	// whose value must match the server-selected analysis profile's parser
	// bundle. It is deliberately not inferred from source, a project, or a path.
	EnvUCIParserBundleDigest = "ENGRAM_UCI_PARSER_BUNDLE_DIGEST"
	// EnvUCIParserExecutable names the installed parser artifact paired with the
	// running daemon. It is an absolute installed-artifact path, never a project
	// selector or a PATH lookup.
	EnvUCIParserExecutable = "ENGRAM_UCI_PARSER_EXECUTABLE"

	uciRuntimeRegistryFile         = "uci-registry.sqlite"
	uciRuntimeGitLineLimit         = 4096
	uciRuntimeWatcherDebounceDelay = 250 * time.Millisecond
	uciRuntimeWatcherMaxBatchDelay = 2 * time.Second
	uciRuntimeWatcherQueueCapacity = 64
	uciRuntimeParserMaxInputBytes  = 4 << 20
	uciRuntimeParserMaxOutputBytes = 16 << 20
	uciRuntimeParserTimeout        = 10 * time.Second
	uciRuntimeGoProfileKey         = "go-structure-v1"
	uciRuntimeGoParserKey          = "go-parser-v1"
	uciRuntimeGitFingerprintDomain = "engram.uci.local-git-directory/v1"
)

const (
	uciRuntimeAgentDirectoryName = ".agent"
	uciRuntimeGitRevParse        = "rev-parse"
)

// UCIRuntimeConfig is the daemon-owned, source-independent configuration for
// the UCI prepared-index path. The server remains authoritative for selecting
// a checkout and profile; this configuration only proves which local scanner
// implementation is allowed to produce a publication for that selection.
type UCIRuntimeConfig struct {
	ClientInstanceID   string
	ParserBundleDigest uci.IndexDigest
	// ParserExecutable is the canonical installed parser sibling selected at
	// daemon startup. An empty value leaves parser-required source unavailable.
	ParserExecutable string
	GoProfile        uci.GoExtractionProfile
}

// RuntimeConfigFromEnvironment snapshots the existing daemon runtime inputs at
// module registration time. It intentionally has no repository, project, CWD,
// or client-request fallback.
func RuntimeConfigFromEnvironment() UCIRuntimeConfig {
	return UCIRuntimeConfig{
		ClientInstanceID:   os.Getenv(config.EnvClientInstanceID),
		ParserBundleDigest: uci.IndexDigest(os.Getenv(EnvUCIParserBundleDigest)),
		ParserExecutable:   os.Getenv(EnvUCIParserExecutable),
		GoProfile: uci.GoExtractionProfile{
			ProfileKey: uciRuntimeGoProfileKey,
			ParserKey:  uciRuntimeGoParserKey,
		},
	}
}

type uciRuntime struct {
	core   *engramcore.Module
	config UCIRuntimeConfig

	stateMu                  sync.RWMutex
	started                  bool
	closed                   bool
	workstationID            string
	daemonCtx                context.Context
	logger                   *slog.Logger
	db                       *sql.DB
	registry                 *UCILocalRegistry
	treeSitterParser         UCIPreparedTreeSitterParser
	scannerAggregateObserver uciPreparedScannerAggregateObserver
	authorizedTarget         map[uciRuntimeAuthorizedTargetKey]uciRuntimeAuthorizedTarget

	watcherMu sync.Mutex
	watchers  map[string]uciRuntimeWatcher
}

type uciRuntimeAuthorizedTargetKey struct {
	checkoutID      string
	clientSessionID string
	contextHandle   string
}

func uciRuntimeAuthorizedTargetKeyFor(target engramcore.ResolvedIndexTarget) uciRuntimeAuthorizedTargetKey {
	binding := target.BindingClone()
	return uciRuntimeAuthorizedTargetKey{
		checkoutID:      binding.Scope.CheckoutID,
		clientSessionID: target.ClientSessionID,
		contextHandle:   target.ContextHandle,
	}
}

type uciRuntimeAuthorizedTarget struct {
	target        engramcore.ResolvedIndexTarget
	rootPath      string
	incarnationID string
}

type uciRuntimeWatcher struct {
	incarnationID string
	rootPath      string
	privateGitDir string
	watcher       *UCIWatcher
}

// uciRuntimeWatcherChangeSource is the narrow handoff from the runtime's
// retained server-authorized target to codeintel's execution scheduler. It
// contains no project selector, raw root, or server configuration.
type uciRuntimeWatcherChangeSource struct {
	checkoutID    string
	incarnationID string
	rootPath      string
	targetKey     uciRuntimeAuthorizedTargetKey
	changes       <-chan struct{}
}

type uciRuntimeIndexSnapshot struct {
	target   engramcore.ResolvedIndexTarget
	rootPath string
}

// uciRuntimeWatcherSource adapts fsnotify's field-based API to the existing
// watcher boundary. It registers every admitted directory recursively and
// deliberately never follows symlinks or Windows reparse points.
type uciRuntimeWatcherSource struct {
	watcher       *fsnotify.Watcher
	rootPath      string
	privateGitDir string

	mu      sync.Mutex
	watched map[string]struct{}
}

func newUCIRuntimeWatcherSource(watcher *fsnotify.Watcher, rootPath, privateGitDir string) *uciRuntimeWatcherSource {
	return &uciRuntimeWatcherSource{
		watcher:       watcher,
		rootPath:      rootPath,
		privateGitDir: privateGitDir,
		watched:       make(map[string]struct{}),
	}
}

func (source *uciRuntimeWatcherSource) Add(path string) error {
	if source == nil || source.watcher == nil {
		return errors.New("uci runtime watcher source: unavailable")
	}
	path, admitted := source.admittedPath(path)
	if !admitted {
		return nil
	}
	return source.addDirectoryTree(path)
}

func (source *uciRuntimeWatcherSource) Events() <-chan fsnotify.Event {
	return source.watcher.Events
}

func (source *uciRuntimeWatcherSource) Errors() <-chan error {
	return source.watcher.Errors
}

func (source *uciRuntimeWatcherSource) Close() error {
	return source.watcher.Close()
}

func (source *uciRuntimeWatcherSource) AcceptsEvent(event fsnotify.Event) bool {
	_, admitted := source.admittedPath(event.Name)
	return admitted
}

func (source *uciRuntimeWatcherSource) addDirectoryTree(rootPath string) error {
	pending := []string{rootPath}
	for len(pending) != 0 {
		current := pending[0]
		pending = pending[1:]
		physical, watchable, err := source.watchableDirectory(current)
		if err != nil {
			return err
		}
		if !watchable {
			continue
		}
		if err := source.addDirectory(physical); err != nil {
			return err
		}
		entries, err := os.ReadDir(physical)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("uci runtime watcher source: list %q: %w", physical, err)
		}
		for _, entry := range entries {
			pending = append(pending, filepath.Join(physical, entry.Name()))
		}
	}
	return nil
}

func (source *uciRuntimeWatcherSource) watchableDirectory(current string) (string, bool, error) {
	current, admitted := source.admittedPath(current)
	if !admitted {
		return "", false, nil
	}
	info, err := os.Lstat(current)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("uci runtime watcher source: inspect %q: %w", current, err)
	}
	if !info.IsDir() || uciRuntimeWatcherIsReparse(info) {
		return "", false, nil
	}
	physical, err := uciRuntimeCanonicalPath(current)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("uci runtime watcher source: resolve %q: %w", current, err)
	}
	physical, admitted = source.admittedPath(physical)
	return physical, admitted, nil
}

func (source *uciRuntimeWatcherSource) addDirectory(path string) error {
	source.mu.Lock()
	defer source.mu.Unlock()
	if _, found := source.watched[path]; found {
		return nil
	}
	if err := source.watcher.Add(path); err != nil {
		return fmt.Errorf("uci runtime watcher source: add %q: %w", path, err)
	}
	source.watched[path] = struct{}{}
	return nil
}

func (source *uciRuntimeWatcherSource) admittedPath(path string) (string, bool) {
	if path == "" || !utf8.ValidString(path) || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return "", false
	}
	path = filepath.Clean(path)
	if uciRuntimePathContains(filepath.Join(source.rootPath, uciRuntimeAgentDirectoryName, "worktrees"), path) {
		return "", false
	}
	if source.privateGitDir != "" && uciRuntimePathContains(source.privateGitDir, path) {
		return path, true
	}
	if !uciRuntimePathContains(source.rootPath, path) {
		return "", false
	}
	relativePath, err := filepath.Rel(source.rootPath, path)
	if err != nil || relativePath == "." {
		return path, err == nil
	}
	return path, !uciRuntimeWatcherProtectedRelativePath(relativePath)
}

func uciRuntimeWatcherProtectedRelativePath(relativePath string) bool {
	for _, component := range strings.Split(filepath.ToSlash(relativePath), "/") {
		component = strings.ToLower(component)
		if uciRuntimeProtectedSecretPath(component) {
			return true
		}
		switch component {
		case uciRuntimeAgentDirectoryName, ".cache", "build", "coverage", "dist", "keys", "node_modules", "target", "transcripts", "vendor":
			return true
		}
	}
	return false
}

type uciRuntimeWorktreeEvidence struct {
	rootPath                 string
	commonGitDirFingerprint  string
	privateGitDir            string
	privateGitDirFingerprint string
}

func newUCIRuntime(core *engramcore.Module, configuration UCIRuntimeConfig) (*uciRuntime, error) {
	if core == nil {
		return nil, errors.New("uci runtime: engramcore module is required")
	}
	if err := validateUCIRuntimeConfig(configuration); err != nil {
		return nil, err
	}
	treeSitterParser, err := newUCIRuntimeTreeSitterParser(configuration)
	if err != nil {
		return nil, err
	}
	return &uciRuntime{
		core:                     core,
		config:                   configuration,
		treeSitterParser:         treeSitterParser,
		scannerAggregateObserver: newUCIPreparedScannerAggregateObserverFromEnvironment(),
		authorizedTarget:         make(map[uciRuntimeAuthorizedTargetKey]uciRuntimeAuthorizedTarget),
		watchers:                 make(map[string]uciRuntimeWatcher),
	}, nil
}

func validateUCIRuntimeConfig(configuration UCIRuntimeConfig) error {
	if !validUCIPreparedIndexIdentity(configuration.ClientInstanceID) {
		return errors.New("uci runtime: client instance identity is invalid")
	}
	if !validUCIPreparedIndexDigest(configuration.ParserBundleDigest) {
		return fmt.Errorf("uci runtime: %s must be a sha256 parser bundle digest", EnvUCIParserBundleDigest)
	}
	if _, err := uci.GoIndexAdmissionArtifactProfile(configuration.GoProfile); err != nil {
		return fmt.Errorf("uci runtime: Go extraction profile is invalid: %w", err)
	}
	return nil
}

// Start opens the local operational registry before codeintel can receive a
// request. The prepared collaborator is configured lazily from the first
// server-authorized binding because the binding's workstation ID is the
// authenticated keycard identity, not the machine-local WORKSTATION_ID value.
func (runtimeState *uciRuntime) Start(deps module.ModuleDeps) error {
	if runtimeState == nil {
		return errors.New("uci runtime: unavailable")
	}
	if deps.DaemonCtx == nil {
		return errors.New("uci runtime: daemon context is required")
	}
	if deps.StorageDir == "" || !filepath.IsAbs(deps.StorageDir) {
		return errors.New("uci runtime: module storage directory must be absolute")
	}

	runtimeState.stateMu.Lock()
	defer runtimeState.stateMu.Unlock()
	if runtimeState.closed {
		return errors.New("uci runtime: already closed")
	}
	if runtimeState.started {
		return errors.New("uci runtime: already started")
	}

	db, err := sql.Open("sqlite", filepath.Join(deps.StorageDir, uciRuntimeRegistryFile))
	if err != nil {
		return fmt.Errorf("uci runtime: open local registry: %w", err)
	}
	registry, err := NewUCILocalRegistry(db)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("uci runtime: initialise local registry: %w", err)
	}

	runtimeState.daemonCtx = deps.DaemonCtx
	runtimeState.logger = deps.Logger
	runtimeState.db = db
	runtimeState.registry = registry
	runtimeState.started = true
	return nil
}

func (runtimeState *uciRuntime) configureCollaboratorLocked(workstationID string) error {
	if runtimeState.workstationID != "" {
		if runtimeState.workstationID != workstationID {
			return errors.New("uci runtime: server binding workstation changed")
		}
		return nil
	}
	if !validUCIPreparedIndexIdentity(workstationID) {
		return errors.New("uci runtime: server binding workstation is invalid")
	}
	collaborator, err := NewUCIPreparedIndexCollaborator(UCIPreparedIndexConfig{
		WorkstationID:      workstationID,
		ClientInstanceID:   runtimeState.config.ClientInstanceID,
		ParserBundleDigest: runtimeState.config.ParserBundleDigest,
		Registry:           runtimeState.registry,
		Scanner:            newUCIRuntimeScanner(),
		TreeSitterParser:   runtimeState.treeSitterParser,
		GoProfile:          runtimeState.config.GoProfile,
		Logger:             runtimeState.logger,
	})
	if err != nil {
		return fmt.Errorf("uci runtime: construct prepared index collaborator: %w", err)
	}
	collaborator.scannerAggregateObserver = runtimeState.scannerAggregateObserver
	if err := runtimeState.core.ConfigurePreparedIndexCollaborator(engramcore.PreparedIndexConfiguration{
		WorkstationID:      workstationID,
		ClientInstanceID:   runtimeState.config.ClientInstanceID,
		ParserBundleDigest: string(runtimeState.config.ParserBundleDigest),
	}, collaborator); err != nil {
		return fmt.Errorf("uci runtime: configure engramcore collaborator: %w", err)
	}
	runtimeState.workstationID = workstationID
	return nil
}

// newUCIRuntimeTreeSitterParser wires only a verified installed parser artifact.
// An unset executable makes parser-required source fail before publication; a
// configured but missing, foreign, or invalid artifact fails runtime creation.
func newUCIRuntimeTreeSitterParser(configuration UCIRuntimeConfig) (*uci.TreeSitterWorker, error) {
	if configuration.ParserExecutable == "" {
		return nil, nil
	}
	executablePath, err := uciRuntimeInstalledParserExecutable(configuration.ParserExecutable)
	if err != nil {
		return nil, err
	}
	worker, err := uci.NewTreeSitterWorker(uci.TreeSitterWorkerConfig{
		ExecutablePath:       executablePath,
		ExpectedBundleDigest: configuration.ParserBundleDigest,
		MaxInputBytes:        uciRuntimeParserMaxInputBytes,
		MaxOutputBytes:       uciRuntimeParserMaxOutputBytes,
		Timeout:              uciRuntimeParserTimeout,
		Environment:          uciRuntimeParserEnvironment(),
	})
	if err != nil {
		return nil, fmt.Errorf("uci runtime: configure installed Tree-sitter parser: %w", err)
	}
	return worker, nil
}

// uciRuntimeParserEnvironment grants the parser only the Windows loader
// variables it needs when they are present. Other platforms receive an empty,
// non-inherited environment.
func uciRuntimeParserEnvironment() []string {
	if runtime.GOOS != "windows" {
		return []string{}
	}
	environment := make([]string, 0, 3)
	for _, name := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC"} {
		if value, present := os.LookupEnv(name); present {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func uciRuntimeInstalledParserExecutable(configuredPath string) (string, error) {
	parserPath, err := uciRuntimeCanonicalPath(configuredPath)
	if err != nil {
		return "", fmt.Errorf("uci runtime: %s is invalid: %w", EnvUCIParserExecutable, err)
	}
	info, err := os.Stat(parserPath)
	if err != nil {
		return "", fmt.Errorf("uci runtime: inspect configured parser artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("uci runtime: configured parser artifact is not a regular file")
	}
	daemonPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("uci runtime: resolve running daemon executable: %w", err)
	}
	daemonPath, err = uciRuntimeCanonicalPath(daemonPath)
	if err != nil {
		return "", fmt.Errorf("uci runtime: canonicalize running daemon executable: %w", err)
	}
	expectedPath := filepath.Join(filepath.Dir(daemonPath), "parser", "parser"+filepath.Ext(daemonPath))
	expectedPath, err = uciRuntimeCanonicalPath(expectedPath)
	if err != nil {
		return "", fmt.Errorf("uci runtime: resolve installed parser sibling: %w", err)
	}
	if !uciRuntimeSamePath(parserPath, expectedPath) {
		return "", errors.New("uci runtime: configured parser is not the installed sibling of the running daemon")
	}
	return parserPath, nil
}

func newUCIRuntimeScanner() *uci.Scanner {
	return uci.NewScanner(
		uciRuntimeGitRunner{},
		uci.OSScannerFileSystem{},
		uci.ScannerPolicy{
			IncludeUntracked: true,
			ProtectedPaths: []string{
				uciRuntimeAgentDirectoryName,
				".env",
				".env.*",
				".cache",
				"build",
				"coverage",
				"credentials",
				"dist",
				"keys",
				"node_modules",
				"target",
				"transcripts",
				"vendor",
			},
			SecretDetector: uci.ScannerSecretDetectorFunc(func(filePath string, body []byte) bool {
				return uciRuntimeProtectedSecretPath(filePath) || privacy.ContainsSecrets(string(body))
			}),
		},
	)
}

func uciRuntimeProtectedSecretPath(filePath string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(filePath, "\\", "/"))
	base := normalized
	if separator := strings.LastIndexByte(normalized, '/'); separator >= 0 {
		base = normalized[separator+1:]
	}
	return base == ".env" || strings.HasPrefix(base, ".env.") || base == "credentials" || base == "credentials.json" || base == "secrets" || base == "secrets.json"
}

func uciRuntimeFileLocatorPath(locator string) (string, error) {
	parsed, err := url.Parse(locator)
	if err != nil || parsed.Scheme != "file" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("checkout locator is not a closed file URI")
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return "", errors.New("checkout locator host is unsupported")
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || decoded == "" {
		return "", errors.New("checkout locator path is invalid")
	}
	if runtime.GOOS == "windows" && len(decoded) >= 3 && decoded[0] == '/' && decoded[2] == ':' {
		decoded = decoded[1:]
	}
	return uciRuntimeCanonicalPath(filepath.FromSlash(decoded))
}

// Prepare derives local correlation evidence only after the server has bound
// the opaque client handle. The selected session root is the sole local input;
// an explicit tool root is accepted only when it names that exact selected root.
func (runtimeState *uciRuntime) Prepare(ctx context.Context, target engramcore.ResolvedIndexTarget, selectedRoot, requestedRoot string) (string, error) {
	if runtimeState == nil {
		return "", errors.New("uci runtime: unavailable")
	}
	if ctx == nil {
		return "", errors.New("uci runtime: request context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	runtimeState.stateMu.Lock()
	defer runtimeState.stateMu.Unlock()
	if !runtimeState.started || runtimeState.closed || runtimeState.registry == nil || runtimeState.daemonCtx == nil {
		return "", errors.New("uci runtime: unavailable")
	}

	binding := target.BindingClone()
	if err := binding.Validate(); err != nil {
		return "", fmt.Errorf("uci runtime: server binding is invalid: %w", err)
	}
	targetKey := uciRuntimeAuthorizedTargetKeyFor(target)
	if authorized, found := runtimeState.authorizedTarget[targetKey]; found &&
		sameUCIRuntimeTargetIdentity(authorized.target, target) &&
		uciRuntimeSamePath(selectedRoot, authorized.rootPath) &&
		(uciRuntimeSamePath(requestedRoot, selectedRoot) || uciRuntimeSamePath(requestedRoot, authorized.rootPath)) {
		return authorized.rootPath, nil
	}

	evidence, err := runtimeState.currentWorktreeEvidence(ctx, selectedRoot)
	if err != nil {
		return "", err
	}
	if !uciRuntimeSamePath(requestedRoot, selectedRoot) && !uciRuntimeSamePath(requestedRoot, evidence.rootPath) {
		return "", errors.New("uci runtime: requested root does not match the selected worktree")
	}
	locatorPath, err := uciRuntimeFileLocatorPath(binding.LocalRootID)
	if err != nil || !uciRuntimeSamePath(locatorPath, evidence.rootPath) {
		return "", errors.New("uci runtime: server-authorized checkout locator does not match the selected worktree")
	}
	if err := runtimeState.configureCollaboratorLocked(binding.WorkstationID); err != nil {
		return "", err
	}
	if err := runtimeState.recordApprovedRoot(ctx, binding, evidence); err != nil {
		return "", err
	}
	registration := UCILocalCheckoutRegistration{
		RootID:                   binding.LocalRootID,
		SourceID:                 binding.Scope.SourceID,
		CheckoutID:               binding.Scope.CheckoutID,
		IncarnationID:            binding.Scope.IncarnationID,
		CommonGitDirFingerprint:  evidence.commonGitDirFingerprint,
		PrivateGitDirFingerprint: evidence.privateGitDirFingerprint,
		WorkstationID:            binding.WorkstationID,
		ClientInstanceID:         runtimeState.config.ClientInstanceID,
	}
	if _, err := runtimeState.registry.RegisterCheckout(ctx, registration); err != nil {
		return "", fmt.Errorf("uci runtime: register checkout evidence: %w", err)
	}
	if err := runtimeState.ensureWatcher(ctx, runtimeState.registry, registration, evidence); err != nil {
		return "", err
	}
	runtimeState.authorizedTarget[targetKey] = uciRuntimeAuthorizedTarget{
		target:        target.Clone(),
		rootPath:      evidence.rootPath,
		incarnationID: registration.IncarnationID,
	}
	return evidence.rootPath, nil
}

func (runtimeState *uciRuntime) ensureWatcher(ctx context.Context, registry *UCILocalRegistry, registration UCILocalCheckoutRegistration, evidence uciRuntimeWorktreeEvidence) error {
	runtimeState.watcherMu.Lock()
	defer runtimeState.watcherMu.Unlock()

	existing, found := runtimeState.watchers[registration.CheckoutID]
	if found && existing.incarnationID == registration.IncarnationID && existing.rootPath == evidence.rootPath && existing.privateGitDir == evidence.privateGitDir && uciRuntimeWatcherAlive(existing.watcher) {
		return nil
	}
	if found {
		delete(runtimeState.watchers, registration.CheckoutID)
		_ = existing.watcher.Stop()
	}

	source, err := fsnotify.NewWatcher()
	if err != nil {
		return runtimeState.watcherFailure(ctx, registry, registration.CheckoutID, fmt.Errorf("uci runtime: create watcher: %w", err))
	}
	watcher, err := NewUCIWatcher(UCIWatcherConfig{
		Registry:      registry,
		Source:        newUCIRuntimeWatcherSource(source, evidence.rootPath, evidence.privateGitDir),
		CheckoutID:    registration.CheckoutID,
		RootPath:      evidence.rootPath,
		GitDir:        evidence.privateGitDir,
		DebounceDelay: uciRuntimeWatcherDebounceDelay,
		MaxBatchDelay: uciRuntimeWatcherMaxBatchDelay,
		QueueCapacity: uciRuntimeWatcherQueueCapacity,
	})
	if err != nil {
		_ = source.Close()
		return runtimeState.watcherFailure(ctx, registry, registration.CheckoutID, fmt.Errorf("uci runtime: configure watcher: %w", err))
	}
	if err := watcher.Start(runtimeState.daemonCtx); err != nil {
		_ = watcher.Stop()
		return runtimeState.watcherFailure(ctx, registry, registration.CheckoutID, fmt.Errorf("uci runtime: start watcher: %w", err))
	}
	runtimeState.watchers[registration.CheckoutID] = uciRuntimeWatcher{
		incarnationID: registration.IncarnationID,
		rootPath:      evidence.rootPath,
		privateGitDir: evidence.privateGitDir,
		watcher:       watcher,
	}

	return nil
}

func (runtimeState *uciRuntime) watcherChangeSource(target engramcore.ResolvedIndexTarget) (uciRuntimeWatcherChangeSource, error) {
	if runtimeState == nil {
		return uciRuntimeWatcherChangeSource{}, errors.New("uci runtime: unavailable")
	}
	binding := target.BindingClone()
	if err := binding.Validate(); err != nil {
		return uciRuntimeWatcherChangeSource{}, errors.New("uci runtime: server binding is invalid")
	}
	targetKey := uciRuntimeAuthorizedTargetKeyFor(target)
	runtimeState.stateMu.RLock()
	authorized, found := runtimeState.authorizedTarget[targetKey]
	available := runtimeState.started && !runtimeState.closed
	runtimeState.stateMu.RUnlock()
	if !available || !found || !sameUCIRuntimeTargetIdentity(authorized.target, target) {
		return uciRuntimeWatcherChangeSource{}, errors.New("uci runtime: authorized target is unavailable")
	}

	runtimeState.watcherMu.Lock()
	retained, found := runtimeState.watchers[binding.Scope.CheckoutID]
	runtimeState.watcherMu.Unlock()
	if !found || retained.incarnationID != authorized.incarnationID || retained.rootPath != authorized.rootPath || !uciRuntimeWatcherAlive(retained.watcher) {
		return uciRuntimeWatcherChangeSource{}, errors.New("uci runtime: watcher is unavailable")
	}
	return uciRuntimeWatcherChangeSource{
		checkoutID:    binding.Scope.CheckoutID,
		incarnationID: authorized.incarnationID,
		rootPath:      authorized.rootPath,
		targetKey:     targetKey,
		changes:       retained.watcher.Changes(),
	}, nil
}

func (runtimeState *uciRuntime) watcherIndexSnapshot(source uciRuntimeWatcherChangeSource) (uciRuntimeIndexSnapshot, bool) {
	if runtimeState == nil || source.checkoutID == "" || source.incarnationID == "" || source.rootPath == "" || source.targetKey.clientSessionID == "" || source.targetKey.contextHandle == "" || source.changes == nil {
		return uciRuntimeIndexSnapshot{}, false
	}
	runtimeState.stateMu.RLock()
	authorized, found := runtimeState.authorizedTarget[source.targetKey]
	available := runtimeState.started && !runtimeState.closed
	runtimeState.stateMu.RUnlock()
	if !found || !available || authorized.incarnationID != source.incarnationID || authorized.rootPath != source.rootPath {
		return uciRuntimeIndexSnapshot{}, false
	}
	runtimeState.watcherMu.Lock()
	retained, found := runtimeState.watchers[source.checkoutID]
	runtimeState.watcherMu.Unlock()
	if !found || retained.incarnationID != source.incarnationID || retained.rootPath != source.rootPath || !uciRuntimeWatcherAlive(retained.watcher) || retained.watcher.Changes() != source.changes {
		return uciRuntimeIndexSnapshot{}, false
	}
	return uciRuntimeIndexSnapshot{target: authorized.target.Clone(), rootPath: source.rootPath}, true
}

func (runtimeState *uciRuntime) indexIntentTargets() []uciRuntimeIndexSnapshot {
	if runtimeState == nil {
		return nil
	}
	runtimeState.stateMu.RLock()
	defer runtimeState.stateMu.RUnlock()
	if !runtimeState.started || runtimeState.closed {
		return nil
	}
	targets := make([]uciRuntimeIndexSnapshot, 0, len(runtimeState.authorizedTarget))
	for _, authorized := range runtimeState.authorizedTarget {
		targets = append(targets, uciRuntimeIndexSnapshot{target: authorized.target.Clone(), rootPath: authorized.rootPath})
	}
	return targets
}

func (runtimeState *uciRuntime) updateReboundTarget(target engramcore.ResolvedIndexTarget) error {
	if runtimeState == nil {
		return errors.New("uci runtime: unavailable")
	}
	binding := target.BindingClone()
	if err := binding.Validate(); err != nil {
		return errors.New("uci runtime: rebound binding is invalid")
	}
	runtimeState.stateMu.Lock()
	defer runtimeState.stateMu.Unlock()
	targetKey := uciRuntimeAuthorizedTargetKeyFor(target)
	authorized, found := runtimeState.authorizedTarget[targetKey]
	if !found || !runtimeState.started || runtimeState.closed || !sameUCIRuntimeTargetIdentity(authorized.target, target) {
		return errors.New("uci runtime: rebound target changed authorization")
	}
	authorized.target = target.Clone()
	runtimeState.authorizedTarget[targetKey] = authorized
	return nil
}

func sameUCIRuntimeTargetIdentity(left, right engramcore.ResolvedIndexTarget) bool {
	leftBinding := left.BindingClone()
	rightBinding := right.BindingClone()
	return left.ClientSessionID == right.ClientSessionID &&
		left.ContextHandle == right.ContextHandle &&
		leftBinding.Scope.SourceID == rightBinding.Scope.SourceID &&
		leftBinding.Scope.CheckoutID == rightBinding.Scope.CheckoutID &&
		leftBinding.Scope.IncarnationID == rightBinding.Scope.IncarnationID &&
		leftBinding.ProfileID == rightBinding.ProfileID &&
		leftBinding.LocalRootID == rightBinding.LocalRootID &&
		leftBinding.WorkstationID == rightBinding.WorkstationID
}

func (runtimeState *uciRuntime) watcherFailure(ctx context.Context, registry *UCILocalRegistry, checkoutID string, watcherErr error) error {
	if _, err := registry.RequireRescan(ctx, checkoutID, UCILocalRescanWatcherRegistrationFailure); err != nil {
		return errors.Join(watcherErr, fmt.Errorf("uci runtime: record watcher failure: %w", err))
	}
	return watcherErr
}

func uciRuntimeWatcherAlive(watcher *UCIWatcher) bool {
	if watcher == nil {
		return false
	}
	select {
	case <-watcher.Done():
		return false
	default:
		return true
	}
}

// Close stops every retained watcher before releasing the SQLite handle. Prepare
// holds stateMu while it records local authority and watcher registration, so
// Close cannot race those transitions.
func (runtimeState *uciRuntime) Close(ctx context.Context) error {
	if runtimeState == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("uci runtime: shutdown context is required")
	}

	runtimeState.stateMu.Lock()
	if runtimeState.closed {
		runtimeState.stateMu.Unlock()
		return nil
	}
	runtimeState.closed = true
	runtimeState.started = false
	db := runtimeState.db
	runtimeState.db = nil
	runtimeState.registry = nil
	runtimeState.authorizedTarget = make(map[uciRuntimeAuthorizedTargetKey]uciRuntimeAuthorizedTarget)
	runtimeState.stateMu.Unlock()

	runtimeState.watcherMu.Lock()
	watchers := make([]*UCIWatcher, 0, len(runtimeState.watchers))
	for _, retained := range runtimeState.watchers {
		watchers = append(watchers, retained.watcher)
	}
	runtimeState.watchers = make(map[string]uciRuntimeWatcher)
	runtimeState.watcherMu.Unlock()

	var shutdownErr error
	// The daemon context is normally already canceled when lifecycle shutdown
	// begins. Stop must still run for every watcher before SQLite closes; that
	// cancellation is exactly what wakes each watch loop.
	for _, watcher := range watchers {
		if err := watcher.Stop(); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	if db != nil {
		if err := db.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	return shutdownErr
}

func (runtimeState *uciRuntime) currentWorktreeEvidence(ctx context.Context, selectedRoot string) (uciRuntimeWorktreeEvidence, error) {
	selectedRoot, err := uciRuntimeCanonicalPath(selectedRoot)
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	rootPath, err := runtimeState.gitPath(ctx, selectedRoot, "resolve worktree root", uciRuntimeGitRevParse, "--show-toplevel")
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	rootPath, err = uciRuntimeCanonicalPath(rootPath)
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	if !uciRuntimePathContains(rootPath, selectedRoot) {
		return uciRuntimeWorktreeEvidence{}, errors.New("uci runtime: selected root is outside resolved worktree")
	}
	privateGitDir, err := runtimeState.gitPath(ctx, rootPath, "resolve private Git directory", uciRuntimeGitRevParse, "--absolute-git-dir")
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	privateGitDir, err = uciRuntimeCanonicalPath(privateGitDir)
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	commonGitDir, err := runtimeState.gitPath(ctx, rootPath, "resolve common Git directory", uciRuntimeGitRevParse, "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	commonGitDir, err = uciRuntimeCanonicalPath(commonGitDir)
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	return uciRuntimeWorktreeEvidence{
		rootPath:                 rootPath,
		commonGitDirFingerprint:  uciRuntimeGitDirectoryFingerprint(commonGitDir),
		privateGitDir:            privateGitDir,
		privateGitDirFingerprint: uciRuntimeGitDirectoryFingerprint(privateGitDir),
	}, nil
}

func (runtimeState *uciRuntime) recordApprovedRoot(ctx context.Context, binding uci.IndexBinding, evidence uciRuntimeWorktreeEvidence) error {
	existing, found, err := runtimeState.registry.ApprovedRoot(ctx, binding.LocalRootID)
	if err != nil {
		return fmt.Errorf("uci runtime: load approved root: %w", err)
	}
	if found && !uciRuntimeSamePath(existing.RootPath, evidence.rootPath) {
		return errors.New("uci runtime: server root ID is already registered for a different physical checkout")
	}
	if err := runtimeState.registry.RecordApprovedRoot(ctx, UCILocalApprovedRoot{
		RootID:                  binding.LocalRootID,
		SourceID:                binding.Scope.SourceID,
		CommonGitDirFingerprint: evidence.commonGitDirFingerprint,
		RootPath:                evidence.rootPath,
	}); err != nil {
		return fmt.Errorf("uci runtime: record approved root: %w", err)
	}
	return nil
}

func (runtimeState *uciRuntime) gitPath(ctx context.Context, rootPath, operation string, command ...string) (string, error) {
	args := make([]string, 0, 2+len(command))
	args = append(args, "-C", rootPath)
	args = append(args, command...)
	result, err := (uciRuntimeGitRunner{}).Run(ctx, uci.GitInvocation{Args: args})
	if err != nil {
		return "", fmt.Errorf("uci runtime: %s: %w", operation, err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("uci runtime: %s failed", operation)
	}
	value, err := uciRuntimeGitLine(result.Stdout)
	if err != nil {
		return "", fmt.Errorf("uci runtime: %s returned invalid output: %w", operation, err)
	}
	return value, nil
}

type uciRuntimeGitRunner struct{}

func (uciRuntimeGitRunner) Run(ctx context.Context, invocation uci.GitInvocation) (uci.GitResult, error) {
	invocation.Args = append([]string(nil), invocation.Args...)
	invocation.Env = uciRuntimeGitEnvironment()
	return (uci.ExecGitRunner{}).Run(ctx, invocation)
}

func uciRuntimeGitLine(output []byte) (string, error) {
	if len(output) == 0 || len(output) > uciRuntimeGitLineLimit || bytes.IndexByte(output, 0) >= 0 {
		return "", errors.New("line is empty or malformed")
	}
	if output[len(output)-1] == '\n' {
		output = output[:len(output)-1]
		if len(output) > 0 && output[len(output)-1] == '\r' {
			output = output[:len(output)-1]
		}
	}
	if len(output) == 0 || !utf8.Valid(output) || bytes.ContainsAny(output, "\r\n") {
		return "", errors.New("line is invalid")
	}
	return string(output), nil
}

func uciRuntimeCanonicalPath(raw string) (string, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.IndexByte(raw, 0) >= 0 || !filepath.IsAbs(raw) {
		return "", errors.New("path is invalid")
	}
	for _, value := range raw {
		if value < 0x20 || value == 0x7f {
			return "", errors.New("path contains control characters")
		}
	}
	physical, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return "", fmt.Errorf("resolve physical path: %w", err)
	}
	return filepath.Clean(physical), nil
}

func uciRuntimeSamePath(left, right string) bool {
	left, leftErr := uciRuntimeCanonicalPath(left)
	right, rightErr := uciRuntimeCanonicalPath(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func uciRuntimePathContains(rootPath, candidatePath string) bool {
	if rootPath == "" || candidatePath == "" || !filepath.IsAbs(rootPath) || !filepath.IsAbs(candidatePath) || strings.IndexByte(rootPath, 0) >= 0 || strings.IndexByte(candidatePath, 0) >= 0 {
		return false
	}
	rootPath = filepath.Clean(rootPath)
	candidatePath = filepath.Clean(candidatePath)
	if runtime.GOOS == "windows" {
		rootPath = strings.ToLower(rootPath)
		candidatePath = strings.ToLower(candidatePath)
	}
	relativePath, err := filepath.Rel(rootPath, candidatePath)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(relativePath) {
		return false
	}
	return true
}

func uciRuntimeGitDirectoryFingerprint(directory string) string {
	normalized := filepath.ToSlash(filepath.Clean(directory))
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	sum := sha256.Sum256([]byte(uciRuntimeGitFingerprintDomain + "\x00" + normalized))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func uciRuntimeGitEnvironment() []string {
	environment := os.Environ()
	filtered := make([]string, 0, len(environment)+22)
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || uciRuntimeInheritedGitInfluence(name) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0="+os.DevNull,
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=core.useBuiltinFSMonitor",
		"GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=core.untrackedCache",
		"GIT_CONFIG_VALUE_3=false",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_SSH=",
		"GIT_SSH_COMMAND=",
		"GIT_ALLOW_PROTOCOL=",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GCM_INTERACTIVE=Never",
	)
}

func uciRuntimeInheritedGitInfluence(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "GIT_") {
		return true
	}
	switch upper {
	case "SSH_ASKPASS", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GCM_INTERACTIVE", "PAGER", "EDITOR", "VISUAL":
		return true
	default:
		return false
	}
}
