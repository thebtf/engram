package engramcore

// module_test.go — unit tests for engramcore.Module lifecycle behaviour.
// Uses the same package as the module (not engramcore_test) to access internal
// pool and cache helpers.

import (
	"context"
	"testing"

	"github.com/thebtf/engram/internal/moduletest"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// TestOnProjectRemoved_ClearsSlugCache verifies that OnProjectRemoved deletes
// the slug cache entry for the removed project, so a subsequent session does not
// reuse stale identity metadata.
func TestOnProjectRemoved_ClearsSlugCache(t *testing.T) {
	t.Parallel()

	mod := NewModule()
	h := moduletest.New(t)
	if err := h.Register(mod); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.Freeze()

	const projectID = "proj-remove-me"

	// Pre-populate the slug cache with a synthetic entry bypassing git I/O.
	mod.cache.ForceCacheEntry(muxcore.ProjectContext{ID: projectID, Cwd: t.TempDir()}, "some-slug-value")
	if !mod.cache.HasEntry(projectID) {
		t.Fatal("pre-condition: cache entry not set")
	}

	h.SimulateProjectRemoved(projectID)

	if mod.cache.HasEntry(projectID) {
		t.Error("slug cache entry MUST be cleared after OnProjectRemoved")
	}
}

// TestOnProjectRemoved_DoesNotClosePooledConns verifies that the gRPC connection
// pool is preserved after OnProjectRemoved. Connections are keyed by (addr,
// tls mode), not by project, so per-project removal must NOT close them.
func TestOnProjectRemoved_DoesNotClosePooledConns(t *testing.T) {
	t.Parallel()

	mod := NewModule()
	h := moduletest.New(t)
	if err := h.Register(mod); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.Freeze()

	// Inject a dummy pool entry. We do not need a real connection — the test
	// only checks that the sync.Map entry survives the project removal call.
	dummyKey := connKey{addr: "dummy-host:9999", tlsMode: "plaintext"}
	mod.pool.conns.Store(dummyKey, (*struct{ closed bool })(nil)) // store any non-nil value

	h.SimulateProjectRemoved("proj-does-not-matter")

	if _, ok := mod.pool.conns.Load(dummyKey); !ok {
		t.Error("gRPC pool entry MUST NOT be removed by OnProjectRemoved (pool is addr-keyed, not project-keyed)")
	}
}

// TestModule_Name_Stable verifies that Name() returns the stable constant
// "engramcore" and is not subject to accidental mutation.
func TestModule_Name_Stable(t *testing.T) {
	t.Parallel()

	mod := NewModule()
	if got := mod.Name(); got != moduleName {
		t.Errorf("Name() = %q, want %q", got, moduleName)
	}
	// Verify the constant itself has not drifted.
	if moduleName != "engramcore" {
		t.Errorf("moduleName constant = %q, want %q (changing this is a breaking change — see module.go)", moduleName, "engramcore")
	}
}

// TestModule_Shutdown_IsIdempotent verifies that calling Shutdown twice does not
// panic and does not return an error on the second call. The gRPC pool closeAll
// uses sync.Map.Range which is safe to call multiple times.
func TestModule_Shutdown_IsIdempotent(t *testing.T) {
	t.Parallel()

	mod := NewModule()
	ctx := context.Background()

	// First Shutdown — must succeed.
	if err := mod.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	// Second Shutdown — must also succeed without panic.
	if err := mod.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

// TestModule_OnSessionConnectDisconnect_Noops verifies that OnSessionConnect
// and OnSessionDisconnect do not panic even when ModuleDeps.Logger is nil
// (which is the case before Init is called, or when Init provides nil deps).
func TestModule_OnSessionConnectDisconnect_Noops(t *testing.T) {
	t.Parallel()

	mod := NewModule()
	// deps.Logger is nil because Init was not called — this is the adversarial case.

	p := muxcore.ProjectContext{
		ID:  "noop-project",
		Cwd: t.TempDir(),
		Env: nil, // no ENGRAM_URL — slug resolution will fall back to p.ID
	}

	// Must not panic. Any error from ResolveProjectSlug is handled internally
	// with a fallback to p.ID, so the noop behaviour is safe even without git.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("OnSessionConnect panicked: %v", r)
		}
	}()
	mod.OnSessionConnect(p)
	mod.OnSessionDisconnect(p.ID)
}
