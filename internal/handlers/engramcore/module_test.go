package engramcore

// module_test.go — unit tests for engramcore.Module lifecycle behaviour.
// Uses the same package as the module (not engramcore_test) to access internal
// pool and cache helpers.

import (
	"context"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/moduletest"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

func TestRequireServerURL_SessionAliasOverridesHostCanonical(t *testing.T) {
	t.Setenv(config.EnvServerURL, "https://host.example")
	t.Setenv(config.EnvServerURLAlt, "https://host-alias.example")
	p := muxcore.ProjectContext{
		ID: "session-alias",
		Env: map[string]string{
			config.EnvServerURLAlt: "https://session-alias.example",
		},
	}

	got, err := NewModule().requireServerURL(p)
	if err != nil {
		t.Fatalf("requireServerURL: %v", err)
	}
	if want := "https://session-alias.example"; got != want {
		t.Fatalf("requireServerURL=%q, want session alias %q", got, want)
	}
}

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

func TestSlugCacheResolutionAndForgetSerialize(t *testing.T) {
	cache := &slugCache{}
	project := muxcore.ProjectContext{ID: "serialized-project", Cwd: t.TempDir()}
	key := cacheKey(project)
	cache.entries.Store(key, resolvedSlug{id: "cached"})
	cache.identities.Store(key, &pb.ProjectIdentityV2{Version: 2})

	cache.mu.Lock()
	slugStarted, slugDone := make(chan struct{}), make(chan struct{})
	identityStarted, identityDone := make(chan struct{}), make(chan struct{})
	go func() {
		close(slugStarted)
		_ = cache.Resolve(project)
		close(slugDone)
	}()
	go func() {
		close(identityStarted)
		_, _ = cache.ResolveIdentity(project)
		close(identityDone)
	}()
	<-slugStarted
	<-identityStarted
	assertBlocked := func(name string, done <-chan struct{}) {
		t.Helper()
		select {
		case <-done:
			t.Fatalf("%s bypassed cache lifecycle lock", name)
		case <-time.After(50 * time.Millisecond):
		}
	}
	assertBlocked("Resolve", slugDone)
	assertBlocked("ResolveIdentity", identityDone)
	cache.mu.Unlock()

	for name, done := range map[string]<-chan struct{}{"Resolve": slugDone, "ResolveIdentity": identityDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s did not resume after cache lifecycle unlock", name)
		}
	}

	cache.mu.RLock()
	forgetStarted, forgetDone := make(chan struct{}), make(chan struct{})
	go func() {
		close(forgetStarted)
		cache.Forget(project.ID)
		close(forgetDone)
	}()
	<-forgetStarted
	assertBlocked("Forget", forgetDone)
	cache.mu.RUnlock()
	select {
	case <-forgetDone:
	case <-time.After(time.Second):
		t.Fatal("Forget did not resume after active resolutions completed")
	}
	if cache.HasEntry(project.ID) {
		t.Fatal("Forget left a compatibility slug entry")
	}
	if _, ok := cache.identities.Load(key); ok {
		t.Fatal("Forget left a v2 identity entry")
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
