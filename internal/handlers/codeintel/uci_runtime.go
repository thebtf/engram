package codeintel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

	uciRuntimeRegistryFile         = "uci-registry.sqlite"
	uciRuntimeGitLineLimit         = 4096
	uciRuntimeWatcherDebounceDelay = 250 * time.Millisecond
	uciRuntimeWatcherMaxBatchDelay = 2 * time.Second
	uciRuntimeWatcherQueueCapacity = 64
	uciRuntimeGoProfileKey         = "go-structure-v1"
	uciRuntimeGoParserKey          = "go-parser-v1"
	uciRuntimeGitFingerprintDomain = "engram.uci.local-git-directory/v1"
)

// UCIRuntimeConfig is the daemon-owned, source-independent configuration for
// the UCI prepared-index path. The server remains authoritative for selecting
// a checkout and profile; this configuration only proves which local scanner
// implementation is allowed to produce a publication for that selection.
type UCIRuntimeConfig struct {
	WorkstationID      string
	ClientInstanceID   string
	ParserBundleDigest uci.IndexDigest
	GoProfile          uci.GoExtractionProfile
}

// RuntimeConfigFromEnvironment snapshots the existing daemon runtime inputs at
// module registration time. It intentionally has no repository, project, CWD,
// or client-request fallback.
func RuntimeConfigFromEnvironment() UCIRuntimeConfig {
	return UCIRuntimeConfig{
		WorkstationID:      config.GetWorkstationID(),
		ClientInstanceID:   os.Getenv(config.EnvClientInstanceID),
		ParserBundleDigest: uci.IndexDigest(os.Getenv(EnvUCIParserBundleDigest)),
		GoProfile: uci.GoExtractionProfile{
			ProfileKey: uciRuntimeGoProfileKey,
			ParserKey:  uciRuntimeGoParserKey,
		},
	}
}

type uciRuntime struct {
	core   *engramcore.Module
	config UCIRuntimeConfig

	stateMu   sync.RWMutex
	started   bool
	closed    bool
	daemonCtx context.Context
	db        *sql.DB
	registry  *UCILocalRegistry

	watcherMu sync.Mutex
	watchers  map[string]uciRuntimeWatcher
}

type uciRuntimeWatcher struct {
	incarnationID string
	rootPath      string
	privateGitDir string
	watcher       *UCIWatcher
}

// uciRuntimeWatcherSource adapts fsnotify's field-based API to the existing
// watcher boundary without widening that boundary or introducing a second
// watcher implementation.
type uciRuntimeWatcherSource struct {
	watcher *fsnotify.Watcher
}

func (source uciRuntimeWatcherSource) Add(path string) error {
	return source.watcher.Add(path)
}

func (source uciRuntimeWatcherSource) Events() <-chan fsnotify.Event {
	return source.watcher.Events
}

func (source uciRuntimeWatcherSource) Errors() <-chan error {
	return source.watcher.Errors
}

func (source uciRuntimeWatcherSource) Close() error {
	return source.watcher.Close()
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
	return &uciRuntime{
		core:     core,
		config:   configuration,
		watchers: make(map[string]uciRuntimeWatcher),
	}, nil
}

func validateUCIRuntimeConfig(configuration UCIRuntimeConfig) error {
	if !validUCIPreparedIndexIdentity(configuration.WorkstationID) {
		return errors.New("uci runtime: workstation identity is invalid")
	}
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

// Start opens the local operational registry and installs the prepared
// collaborator before codeintel can receive a request. The registry remains
// daemon-local; it cannot select or authorize a server target.
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

	collaborator, err := NewUCIPreparedIndexCollaborator(UCIPreparedIndexConfig{
		WorkstationID:      runtimeState.config.WorkstationID,
		ClientInstanceID:   runtimeState.config.ClientInstanceID,
		ParserBundleDigest: runtimeState.config.ParserBundleDigest,
		Registry:           registry,
		Scanner:            newUCIRuntimeScanner(),
		GoProfile:          runtimeState.config.GoProfile,
	})
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("uci runtime: construct prepared index collaborator: %w", err)
	}
	if err := runtimeState.core.ConfigurePreparedIndexCollaborator(engramcore.PreparedIndexConfiguration{
		WorkstationID:      runtimeState.config.WorkstationID,
		ClientInstanceID:   runtimeState.config.ClientInstanceID,
		ParserBundleDigest: string(runtimeState.config.ParserBundleDigest),
	}, collaborator); err != nil {
		_ = db.Close()
		return fmt.Errorf("uci runtime: configure engramcore collaborator: %w", err)
	}

	runtimeState.daemonCtx = deps.DaemonCtx
	runtimeState.db = db
	runtimeState.registry = registry
	runtimeState.started = true
	return nil
}

// The configured production profile is the existing Go extractor. A parser
// worker/bundle for other languages is intentionally not inferred from source,
// project state, or a latest-version lookup, so that runtime seam remains dark.

func newUCIRuntimeScanner() *uci.Scanner {
	return uci.NewScanner(
		uciRuntimeGitRunner{},
		uci.OSScannerFileSystem{},
		uci.ScannerPolicy{
			IncludeUntracked: true,
			ProtectedPaths: []string{
				".env",
				".env.*",
				"keys",
				"credentials",
				"transcripts",
			},
			SecretDetector: uci.ScannerSecretDetectorFunc(func(_ string, body []byte) bool {
				return privacy.ContainsSecrets(string(body))
			}),
		},
	)
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

	runtimeState.stateMu.RLock()
	defer runtimeState.stateMu.RUnlock()
	if !runtimeState.started || runtimeState.closed || runtimeState.registry == nil || runtimeState.daemonCtx == nil {
		return "", errors.New("uci runtime: unavailable")
	}

	binding := target.BindingClone()
	if err := binding.Validate(); err != nil {
		return "", fmt.Errorf("uci runtime: server binding is invalid: %w", err)
	}
	if binding.WorkstationID != runtimeState.config.WorkstationID {
		return "", errors.New("uci runtime: server binding workstation does not match daemon configuration")
	}
	if !uciRuntimeSamePath(selectedRoot, requestedRoot) {
		return "", errors.New("uci runtime: requested root does not match the selected session root")
	}

	evidence, err := runtimeState.currentWorktreeEvidence(ctx, selectedRoot)
	if err != nil {
		return "", err
	}
	if err := runtimeState.registry.RecordApprovedRoot(ctx, UCILocalApprovedRoot{
		RootID:                  binding.LocalRootID,
		SourceID:                binding.Scope.SourceID,
		CommonGitDirFingerprint: evidence.commonGitDirFingerprint,
		RootPath:                evidence.rootPath,
	}); err != nil {
		return "", fmt.Errorf("uci runtime: record approved root: %w", err)
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
		Source:        uciRuntimeWatcherSource{watcher: source},
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

	// UCIWatcher persists dirty/rescan evidence only. Automatic reindex needs a
	// server lease/recovery trigger, and the typed engramcore boundary exposes no
	// such trigger; a later authorized codebase_index consumes this durable state.
	return nil
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

// Close stops every retained watcher before releasing the SQLite handle. A
// request that entered Prepare holds stateMu's read lock, so Close cannot race
// a registry transaction or a watcher registration.
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
	rootPath, err := runtimeState.gitPath(ctx, selectedRoot, "resolve worktree root", "rev-parse", "--show-toplevel")
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
	privateGitDir, err := runtimeState.gitPath(ctx, rootPath, "resolve private Git directory", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	privateGitDir, err = uciRuntimeCanonicalPath(privateGitDir)
	if err != nil {
		return uciRuntimeWorktreeEvidence{}, err
	}
	commonGitDir, err := runtimeState.gitPath(ctx, rootPath, "resolve common Git directory", "rev-parse", "--path-format=absolute", "--git-common-dir")
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
	return filepath.Clean(raw), nil
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
