package codeintel

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fsnotify/fsnotify"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/uci"
)

func TestUCIRuntimeBindsLocalEvidenceToFirstAuthorizedWorkstation(t *testing.T) {
	root := newUCIRuntimeGitRoot(t)
	core := engramcore.NewModuleWithClientInstanceID("uci-runtime-client")
	runtimeState, err := newUCIRuntime(core, UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("a", 64)),
		GoProfile: uci.GoExtractionProfile{
			ProfileKey: "go-structure-v1",
			ParserKey:  "go-parser-v1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := runtimeState.Close(context.Background()); err != nil {
			t.Errorf("close UCI runtime: %v", err)
		}
	})

	binding := uciRuntimeTestBinding(root, "keycard-workstation-a")
	target := engramcore.ResolvedIndexTarget{
		ClientSessionID: "uci-runtime-session",
		ContextHandle:   "uci-runtime-handle",
		Binding:         binding,
	}
	preparedRoot, err := runtimeState.Prepare(context.Background(), target, root, root)
	if err != nil {
		t.Fatalf("prepare authorized worktree: %v", err)
	}
	if !uciRuntimeSamePath(preparedRoot, root) {
		t.Fatalf("prepared root = %q, want %q", preparedRoot, root)
	}
	record, found, err := runtimeState.registry.Snapshot(context.Background(), binding.Scope.CheckoutID)
	if err != nil || !found {
		t.Fatalf("load local checkout record: found=%v err=%v", found, err)
	}
	if record.WorkstationID != binding.WorkstationID || record.ClientInstanceID != "uci-runtime-client" {
		t.Fatalf("local identity = workstation %q client %q", record.WorkstationID, record.ClientInstanceID)
	}

	changed := target
	changed.Binding = binding.Clone()
	changed.Binding.WorkstationID = "keycard-workstation-b"
	if _, err := runtimeState.Prepare(context.Background(), changed, root, root); err == nil || !strings.Contains(err.Error(), "workstation changed") {
		t.Fatalf("changed authorized workstation error = %v", err)
	}
}

func TestUCIRuntimeRejectsAuthorizedLocatorForAnotherWorktree(t *testing.T) {
	selected := newUCIRuntimeGitRoot(t)
	other := newUCIRuntimeGitRoot(t)
	core := engramcore.NewModuleWithClientInstanceID("uci-runtime-client")
	runtimeState, err := newUCIRuntime(core, UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("b", 64)),
		GoProfile:          uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, cancel := context.WithCancel(context.Background())
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = runtimeState.Close(context.Background())
	})

	binding := uciRuntimeTestBinding(other, "keycard-workstation-a")
	target := engramcore.ResolvedIndexTarget{ClientSessionID: "uci-runtime-session", ContextHandle: "uci-runtime-handle", Binding: binding}
	if _, err := runtimeState.Prepare(context.Background(), target, selected, selected); err == nil || !strings.Contains(err.Error(), "locator does not match") {
		t.Fatalf("mismatched locator error = %v", err)
	}
	if runtimeState.workstationID != "" {
		t.Fatal("mismatched locator configured a prepared collaborator")
	}
}

func TestUCIRuntimeProtectsNestedEnvironmentAndAgentState(t *testing.T) {
	for _, path := range []string{".env", "config/.env.local", "nested/credentials.json", "nested/secrets"} {
		if !uciRuntimeProtectedSecretPath(path) {
			t.Fatalf("protected secret path %q was not classified", path)
		}
	}
	for _, path := range []string{"env.go", "docs/credentials-guide.md", "nested/secrets.md"} {
		if uciRuntimeProtectedSecretPath(path) {
			t.Fatalf("ordinary source path %q was classified as a secret file", path)
		}
	}
}

func uciRuntimeTestBinding(root, workstationID string) uci.IndexBinding {
	return uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      uuid.NewString(),
			CheckoutID:    uuid.NewString(),
			IncarnationID: uuid.NewString(),
		},
		ProfileID:     uuid.NewString(),
		LocalRootID:   uciRuntimeTestFileURI(root),
		WorkstationID: workstationID,
	}
}

func uciRuntimeTestFileURI(root string) string {
	path := filepath.ToSlash(root)
	if runtime.GOOS == "windows" {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func newUCIRuntimeGitRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "worktree with spaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", root, "init")
	command.Env = uciRuntimeGitEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize Git fixture: %v: %s", err, output)
	}
	return root
}

func TestCodeintelModuleInitFailsBeforeRuntimeRegistration(t *testing.T) {
	core := engramcore.NewModuleWithClientInstanceID("uci-runtime-client")
	mod, err := NewModuleWithRuntimeConfig(core, UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("c", 64)),
		GoProfile:          uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"},
	})
	if err != nil {
		t.Fatalf("construct module: %v", err)
	}
	storagePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(storagePath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("create non-directory storage path: %v", err)
	}
	daemonCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err = mod.Init(context.Background(), module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: storagePath})
	if err == nil || !strings.Contains(err.Error(), "initialise codeintel runtime") {
		t.Fatalf("initialization error = %v", err)
	}
	if mod.runtime.started || mod.runtime.registry != nil {
		t.Fatal("failed runtime initialization registered local state")
	}
}

func TestCodeintelModuleInitializesAndStopsRuntimeRegistration(t *testing.T) {
	core := engramcore.NewModuleWithClientInstanceID("uci-runtime-client")
	mod, err := NewModuleWithRuntimeConfig(core, UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("e", 64)),
		GoProfile:          uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"},
	})
	if err != nil {
		t.Fatalf("construct module: %v", err)
	}
	daemonCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := mod.Init(context.Background(), module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err != nil {
		t.Fatalf("initialize module: %v", err)
	}
	if err := mod.Shutdown(context.Background()); err != nil {
		t.Fatalf("shut down module: %v", err)
	}
}

func TestUCIRuntimePrepareInvalidatesFastPathForAnotherWorktree(t *testing.T) {
	selected := newUCIRuntimeGitRoot(t)
	other := newUCIRuntimeGitRoot(t)
	core := engramcore.NewModuleWithClientInstanceID("uci-runtime-client")
	runtimeState, err := newUCIRuntime(core, UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("d", 64)),
		GoProfile:          uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if err := runtimeState.Close(context.Background()); err != nil {
			t.Errorf("close UCI runtime: %v", err)
		}
	})
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	target := engramcore.ResolvedIndexTarget{
		ClientSessionID: "uci-runtime-session",
		ContextHandle:   "uci-runtime-handle",
		Binding:         uciRuntimeTestBinding(selected, "keycard-workstation-a"),
	}
	preparedRoot, err := runtimeState.Prepare(context.Background(), target, selected, selected)
	if err != nil {
		t.Fatalf("prepare selected worktree: %v", err)
	}
	if !uciRuntimeSamePath(preparedRoot, selected) {
		t.Fatalf("prepared root = %q, want %q", preparedRoot, selected)
	}

	_, err = runtimeState.Prepare(context.Background(), target, other, other)
	if err == nil || !strings.Contains(err.Error(), "locator does not match") {
		t.Fatalf("prepare with a different selected worktree error = %v", err)
	}
	runtimeState.stateMu.RLock()
	authorized, found := runtimeState.authorizedTarget[uciRuntimeAuthorizedTargetKeyFor(target)]
	runtimeState.stateMu.RUnlock()
	if !found || !uciRuntimeSamePath(authorized.rootPath, selected) {
		t.Fatal("rejected worktree replaced the retained authorized target")
	}
}

func TestRuntimeConfigSnapshotsDaemonInputsAndRejectsInvalidBoundaries(t *testing.T) {
	t.Setenv(config.EnvClientInstanceID, "uci-runtime-client")
	t.Setenv(EnvUCIParserBundleDigest, "sha256:"+strings.Repeat("f", 64))
	t.Setenv(EnvUCIParserExecutable, "")

	configuration := RuntimeConfigFromEnvironment()
	if configuration.ClientInstanceID != "uci-runtime-client" || configuration.ParserBundleDigest != uci.IndexDigest("sha256:"+strings.Repeat("f", 64)) {
		t.Fatalf("runtime configuration did not preserve daemon inputs: %#v", configuration)
	}
	if configuration.GoProfile != (uci.GoExtractionProfile{ProfileKey: uciRuntimeGoProfileKey, ParserKey: uciRuntimeGoParserKey}) {
		t.Fatalf("runtime configuration profile = %#v", configuration.GoProfile)
	}

	if _, err := newUCIRuntime(nil, configuration); err == nil || !strings.Contains(err.Error(), "engramcore module is required") {
		t.Fatalf("nil core error = %v", err)
	}
	for _, test := range []struct {
		name          string
		configuration UCIRuntimeConfig
	}{
		{name: "invalid client identity", configuration: func() UCIRuntimeConfig {
			value := configuration
			value.ClientInstanceID = "bad\nidentity"
			return value
		}()},
		{name: "invalid parser digest", configuration: func() UCIRuntimeConfig {
			value := configuration
			value.ParserBundleDigest = "not-a-digest"
			return value
		}()},
		{name: "invalid Go profile", configuration: func() UCIRuntimeConfig {
			value := configuration
			value.GoProfile.ParserKey = ""
			return value
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newUCIRuntime(engramcore.NewModuleWithClientInstanceID("uci-runtime-client"), test.configuration); err == nil {
				t.Fatal("invalid daemon configuration constructed a runtime")
			}
		})
	}
	if _, err := newUCIRuntime(engramcore.NewModuleWithClientInstanceID("uci-runtime-client"), configuration); err != nil {
		t.Fatalf("valid daemon configuration rejected: %v", err)
	}
}

func TestUCIRuntimeLifecycleRejectsInvalidTransitions(t *testing.T) {
	configuration := UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("a", 64)),
		GoProfile:          uci.GoExtractionProfile{ProfileKey: uciRuntimeGoProfileKey, ParserKey: uciRuntimeGoParserKey},
	}
	if err := (*uciRuntime)(nil).Start(module.ModuleDeps{}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil runtime start error = %v", err)
	}
	runtimeState, err := newUCIRuntime(engramcore.NewModuleWithClientInstanceID("uci-runtime-client"), configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeState.Start(module.ModuleDeps{StorageDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "daemon context") {
		t.Fatalf("missing daemon context error = %v", err)
	}
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: context.Background(), StorageDir: "relative"}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative storage error = %v", err)
	}

	daemonCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("duplicate start error = %v", err)
	}
	if err := runtimeState.Close(nil); err == nil || !strings.Contains(err.Error(), "shutdown context") {
		t.Fatalf("nil shutdown context error = %v", err)
	}
	if err := runtimeState.Close(context.Background()); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if err := runtimeState.Close(context.Background()); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Fatalf("restart after close error = %v", err)
	}
	if _, err := runtimeState.watcherChangeSource(engramcore.ResolvedIndexTarget{}); err == nil || !strings.Contains(err.Error(), "server binding is invalid") {
		t.Fatalf("invalid watcher source error = %v", err)
	}
	if _, found := runtimeState.watcherIndexSnapshot(uciRuntimeWatcherChangeSource{}); found {
		t.Fatal("incomplete watcher source yielded a snapshot")
	}
}

func TestUCIRuntimeRejectsUnauthorizedPrepareBoundaries(t *testing.T) {
	root := newUCIRuntimeGitRoot(t)
	runtimeState, err := newUCIRuntime(engramcore.NewModuleWithClientInstanceID("uci-runtime-client"), UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("b", 64)),
		GoProfile:          uci.GoExtractionProfile{ProfileKey: uciRuntimeGoProfileKey, ParserKey: uciRuntimeGoParserKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = runtimeState.Close(context.Background())
	})
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	target := engramcore.ResolvedIndexTarget{ClientSessionID: "uci-runtime-session", ContextHandle: "uci-runtime-handle", Binding: uciRuntimeTestBinding(root, "keycard-workstation-a")}
	if _, err := runtimeState.watcherChangeSource(target); err == nil || !strings.Contains(err.Error(), "authorized target is unavailable") {
		t.Fatalf("unauthorized watcher source error = %v", err)
	}
	if err := runtimeState.updateReboundTarget(target); err == nil || !strings.Contains(err.Error(), "changed authorization") {
		t.Fatalf("unauthorized rebound update error = %v", err)
	}
	invalidRebound := target
	invalidRebound.Binding = target.Binding.Clone()
	invalidRebound.Binding.Scope.CheckoutID = ""
	if err := runtimeState.updateReboundTarget(invalidRebound); err == nil || !strings.Contains(err.Error(), "rebound binding is invalid") {
		t.Fatalf("invalid rebound update error = %v", err)
	}

	if _, err := runtimeState.Prepare(nil, target, root, root); err == nil || !strings.Contains(err.Error(), "request context") {
		t.Fatalf("nil request context error = %v", err)
	}
	cancelled, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if _, err := runtimeState.Prepare(cancelled, target, root, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request error = %v", err)
	}
	invalidBinding := target
	invalidBinding.Binding = target.Binding.Clone()
	invalidBinding.Binding.Scope.CheckoutID = ""
	if _, err := runtimeState.Prepare(context.Background(), invalidBinding, root, root); err == nil || !strings.Contains(err.Error(), "server binding is invalid") {
		t.Fatalf("invalid binding error = %v", err)
	}
	for _, locator := range []string{"https://example.test/checkout", "file://example.test/checkout", "file:relative-checkout", "file:///checkout?unexpected=query"} {
		unauthorized := target
		unauthorized.Binding = target.Binding.Clone()
		unauthorized.Binding.LocalRootID = locator
		if _, err := runtimeState.Prepare(context.Background(), unauthorized, root, root); err == nil || !strings.Contains(err.Error(), "locator does not match") {
			t.Fatalf("locator %q error = %v", locator, err)
		}
	}
	runtimeState.stateMu.RLock()
	defer runtimeState.stateMu.RUnlock()
	if runtimeState.workstationID != "" || len(runtimeState.authorizedTarget) != 0 {
		t.Fatalf("rejected authorization changed runtime state: workstation=%q targets=%d", runtimeState.workstationID, len(runtimeState.authorizedTarget))
	}
}

func TestUCIRuntimeAuthorizationLifecycleClearsWatcherAccess(t *testing.T) {
	root := newUCIRuntimeGitRoot(t)
	runtimeState, err := newUCIRuntime(engramcore.NewModuleWithClientInstanceID("uci-runtime-client"), UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("c", 64)),
		GoProfile:          uci.GoExtractionProfile{ProfileKey: uciRuntimeGoProfileKey, ParserKey: uciRuntimeGoParserKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := runtimeState.Start(module.ModuleDeps{DaemonCtx: daemonCtx, StorageDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	target := engramcore.ResolvedIndexTarget{ClientSessionID: "uci-runtime-session", ContextHandle: "uci-runtime-handle", Binding: uciRuntimeTestBinding(root, "keycard-workstation-a")}
	if _, err := runtimeState.Prepare(context.Background(), target, root, root); err != nil {
		t.Fatalf("prepare authorized runtime: %v", err)
	}
	source, err := runtimeState.watcherChangeSource(target)
	if err != nil {
		t.Fatalf("load watcher source: %v", err)
	}
	if snapshot, found := runtimeState.watcherIndexSnapshot(source); !found || snapshot.rootPath == "" || !sameUCIRuntimeTargetIdentity(snapshot.target, target) {
		t.Fatalf("authorized watcher snapshot = %#v, found=%v", snapshot, found)
	}
	if targets := runtimeState.indexIntentTargets(); len(targets) != 1 || !sameUCIRuntimeTargetIdentity(targets[0].target, target) {
		t.Fatalf("authorized index intent targets = %#v", targets)
	}
	if err := runtimeState.updateReboundTarget(target); err != nil {
		t.Fatalf("update unchanged rebound target: %v", err)
	}

	if err := runtimeState.Close(context.Background()); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if _, err := runtimeState.watcherChangeSource(target); err == nil || !strings.Contains(err.Error(), "authorized target is unavailable") {
		t.Fatalf("closed watcher source error = %v", err)
	}
	if _, found := runtimeState.watcherIndexSnapshot(source); found {
		t.Fatal("closed runtime exposed an authorized watcher snapshot")
	}
	if targets := runtimeState.indexIntentTargets(); targets != nil {
		t.Fatalf("closed runtime retained index intent targets: %#v", targets)
	}
	if err := runtimeState.updateReboundTarget(target); err == nil || !strings.Contains(err.Error(), "changed authorization") {
		t.Fatalf("closed rebound update error = %v", err)
	}
}

func TestUCIRuntimeRejectsUntrustedParserArtifacts(t *testing.T) {
	configuration := UCIRuntimeConfig{
		ClientInstanceID:   "uci-runtime-client",
		ParserBundleDigest: uci.IndexDigest("sha256:" + strings.Repeat("d", 64)),
		GoProfile:          uci.GoExtractionProfile{ProfileKey: uciRuntimeGoProfileKey, ParserKey: uciRuntimeGoParserKey},
	}
	for _, test := range []struct {
		name string
		path func(t *testing.T) string
	}{
		{name: "missing artifact", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing-parser") }},
		{name: "directory artifact", path: func(t *testing.T) string { return t.TempDir() }},
		{name: "foreign regular artifact", path: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "parser.exe")
			if err := os.WriteFile(path, []byte("not a parser"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := configuration
			candidate.ParserExecutable = test.path(t)
			if _, err := newUCIRuntime(engramcore.NewModuleWithClientInstanceID("uci-runtime-client"), candidate); err == nil {
				t.Fatal("untrusted parser artifact constructed a runtime")
			}
		})
	}
}

func TestUCIRuntimeParserEnvironmentIsNarrow(t *testing.T) {
	t.Setenv("SYSTEMROOT", "C:\\Windows")
	t.Setenv("WINDIR", "C:\\Windows")
	t.Setenv("COMSPEC", "C:\\Windows\\System32\\cmd.exe")
	t.Setenv("UNRELATED_RUNTIME_VALUE", "must-not-reach-parser")
	environment := uciRuntimeParserEnvironment()
	if runtime.GOOS != "windows" {
		if len(environment) != 0 {
			t.Fatalf("non-Windows parser environment = %#v", environment)
		}
		return
	}
	want := []string{"SYSTEMROOT=C:\\Windows", "WINDIR=C:\\Windows", "COMSPEC=C:\\Windows\\System32\\cmd.exe"}
	if len(environment) != len(want) {
		t.Fatalf("parser environment = %#v, want %#v", environment, want)
	}
	for index := range want {
		if environment[index] != want[index] {
			t.Fatalf("parser environment = %#v, want %#v", environment, want)
		}
	}
}

func TestUCIRuntimeWatcherSourceRejectsProtectedLocalPaths(t *testing.T) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watcher.Close() }()
	root := t.TempDir()
	privateGitDir := t.TempDir()
	source := newUCIRuntimeWatcherSource(watcher, root, privateGitDir)

	for _, test := range []struct {
		path     string
		accepted bool
	}{
		{path: filepath.Join(root, "main.go"), accepted: true},
		{path: filepath.Join(root, ".env"), accepted: false},
		{path: filepath.Join(root, ".agent", "worktrees", "other", "main.go"), accepted: false},
		{path: filepath.Join(root, "node_modules", "dependency.js"), accepted: false},
		{path: filepath.Join(privateGitDir, "index"), accepted: true},
		{path: filepath.Join(t.TempDir(), "outside.go"), accepted: false},
	} {
		if accepted := source.AcceptsEvent(fsnotify.Event{Name: test.path}); accepted != test.accepted {
			t.Fatalf("watcher admission for %q = %v, want %v", test.path, accepted, test.accepted)
		}
	}
}
