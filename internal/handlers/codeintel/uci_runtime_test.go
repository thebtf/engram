package codeintel

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
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
