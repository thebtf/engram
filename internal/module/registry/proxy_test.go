package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/module"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// proxyFake implements EngramModule + ProxyToolProvider only. Used by the
// FR-11a tests to verify single-instance enforcement, capability discovery,
// and GetProxyToolProvider routing.
type proxyFake struct {
	name       string
	proxyTools []module.ToolDef
	proxyErr   error
	handleResp json.RawMessage
	handleErr  error

	// Call counters for assertions.
	proxyToolsCalls  int
	handleToolsCalls int
}

func (f *proxyFake) Name() string                                       { return f.name }
func (f *proxyFake) Init(_ context.Context, _ module.ModuleDeps) error  { return nil }
func (f *proxyFake) Shutdown(_ context.Context) error                   { return nil }

func (f *proxyFake) ProxyTools(_ context.Context, _ muxcore.ProjectContext) ([]module.ToolDef, error) {
	f.proxyToolsCalls++
	if f.proxyErr != nil {
		return nil, f.proxyErr
	}
	return f.proxyTools, nil
}

func (f *proxyFake) ProxyHandleTool(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
	f.handleToolsCalls++
	return f.handleResp, f.handleErr
}

// staticAndProxyFake implements EngramModule + ToolProvider + ProxyToolProvider.
// Ensures that a module can legally combine both — the registry must cache
// both cap refs and the dispatcher must route based on name lookup precedence.
type staticAndProxyFake struct {
	name        string
	staticTools []module.ToolDef
	proxyTools  []module.ToolDef
}

func (f *staticAndProxyFake) Name() string                                       { return f.name }
func (f *staticAndProxyFake) Init(_ context.Context, _ module.ModuleDeps) error  { return nil }
func (f *staticAndProxyFake) Shutdown(_ context.Context) error                   { return nil }

func (f *staticAndProxyFake) Tools() []module.ToolDef { return f.staticTools }
func (f *staticAndProxyFake) HandleTool(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`"static"`), nil
}

func (f *staticAndProxyFake) ProxyTools(_ context.Context, _ muxcore.ProjectContext) ([]module.ToolDef, error) {
	return f.proxyTools, nil
}
func (f *staticAndProxyFake) ProxyHandleTool(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`"proxy"`), nil
}

// TestRegister_ProxyToolProvider_Accepted verifies that a module implementing
// only ProxyToolProvider is accepted and exposed via GetProxyToolProvider.
func TestRegister_ProxyToolProvider_Accepted(t *testing.T) {
	t.Parallel()

	r := New()
	m := &proxyFake{name: "engramcore"}
	require.NoError(t, r.Register(m))

	proxy, name, ok := r.GetProxyToolProvider()
	require.True(t, ok, "proxy must be registered")
	assert.Equal(t, "engramcore", name)
	assert.NotNil(t, proxy)
	// Cached ref must point at the SAME object.
	assert.Same(t, module.ProxyToolProvider(m), proxy)
}

// TestRegister_NoProxyProvider verifies GetProxyToolProvider returns false
// when no proxy was registered.
func TestRegister_NoProxyProvider(t *testing.T) {
	t.Parallel()

	r := New()
	require.NoError(t, r.Register(&coreOnlyFake{name: "static"}))

	proxy, name, ok := r.GetProxyToolProvider()
	assert.False(t, ok)
	assert.Nil(t, proxy)
	assert.Empty(t, name)
}

// TestRegister_SecondProxyRejected verifies single-instance enforcement per
// FR-11a: registering a second ProxyToolProvider MUST return
// ErrMultipleProxyToolProviders and leave the registry unchanged.
func TestRegister_SecondProxyRejected(t *testing.T) {
	t.Parallel()

	r := New()
	first := &proxyFake{name: "engramcore"}
	second := &proxyFake{name: "otherproxy"}

	require.NoError(t, r.Register(first))

	err := r.Register(second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMultipleProxyToolProviders,
		"must wrap ErrMultipleProxyToolProviders for errors.Is matching")
	// Error message MUST name BOTH conflicting modules for clear debugging.
	assert.Contains(t, err.Error(), "engramcore", "error must name the existing proxy")
	assert.Contains(t, err.Error(), "otherproxy", "error must name the rejected proxy")

	// Registry state must be unchanged: only the first proxy registered.
	proxy, name, ok := r.GetProxyToolProvider()
	require.True(t, ok)
	assert.Equal(t, "engramcore", name)
	assert.Same(t, module.ProxyToolProvider(first), proxy)

	// Entries must only contain the first module.
	assert.Len(t, r.Entries(), 1)
}

// TestRegister_ProxyAfterStaticIsFine verifies the two capabilities can
// coexist: a static ToolProvider and a proxy can be registered side-by-side.
func TestRegister_ProxyAfterStaticIsFine(t *testing.T) {
	t.Parallel()

	r := New()
	staticOne := &toolFake{
		name: "static",
		tools: []module.ToolDef{
			{Name: "static.ping", Description: "ping"},
		},
	}
	require.NoError(t, r.Register(staticOne))

	proxy := &proxyFake{
		name: "proxy",
		proxyTools: []module.ToolDef{
			{Name: "proxy.echo", Description: "echo"},
		},
	}
	require.NoError(t, r.Register(proxy))

	// Static tool lookup still works.
	entry, def, ok := r.ToolByName("static.ping")
	require.True(t, ok)
	assert.Equal(t, "static", entry.Module.Name())
	assert.Equal(t, "static.ping", def.Name)

	// Proxy is exposed separately.
	got, gotName, gotOk := r.GetProxyToolProvider()
	require.True(t, gotOk)
	assert.Equal(t, "proxy", gotName)
	assert.Same(t, module.ProxyToolProvider(proxy), got)
}

// TestRegister_ProxyAndStaticOnSameModule verifies a single module can
// implement BOTH ToolProvider and ProxyToolProvider simultaneously. Both
// cap refs must be cached and returned correctly.
func TestRegister_ProxyAndStaticOnSameModule(t *testing.T) {
	t.Parallel()

	r := New()
	m := &staticAndProxyFake{
		name: "hybrid",
		staticTools: []module.ToolDef{
			{Name: "hybrid.static1", Description: "s1"},
		},
		proxyTools: []module.ToolDef{
			{Name: "hybrid.proxy1", Description: "p1"},
		},
	}
	require.NoError(t, r.Register(m))

	// Static lookup path.
	entry, def, ok := r.ToolByName("hybrid.static1")
	require.True(t, ok)
	assert.Equal(t, "hybrid", entry.Module.Name())
	assert.Equal(t, "hybrid.static1", def.Name)
	assert.NotNil(t, entry.ProxyTool, "Entry must carry ProxyTool cache")

	// Proxy lookup path.
	proxy, name, proxyOk := r.GetProxyToolProvider()
	require.True(t, proxyOk)
	assert.Equal(t, "hybrid", name)
	assert.NotNil(t, proxy)
}

// TestRegister_ProxyRejected_NoPartialRegistration verifies that when a
// second proxy is rejected, the rejected module does NOT appear in Entries()
// and any tools it might have declared do NOT leak into the tool index.
func TestRegister_ProxyRejected_NoPartialRegistration(t *testing.T) {
	t.Parallel()

	r := New()
	first := &staticAndProxyFake{
		name: "first",
		staticTools: []module.ToolDef{
			{Name: "first.a", Description: "a"},
		},
	}
	require.NoError(t, r.Register(first))

	// Second module also has a static tool AND is a proxy — rejection must
	// happen BEFORE the tool index is mutated.
	second := &staticAndProxyFake{
		name: "second",
		staticTools: []module.ToolDef{
			{Name: "second.b", Description: "b"},
		},
	}
	err := r.Register(second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMultipleProxyToolProviders)

	// "second.b" MUST NOT be findable via ToolByName.
	_, _, ok := r.ToolByName("second.b")
	assert.False(t, ok, "rejected module's static tools must not leak into the index")

	// Only the first module is visible.
	assert.Len(t, r.Entries(), 1)
	assert.Equal(t, []string{"first"}, r.ListNames())
}

// TestGetProxyToolProvider_ErrorFromProxyToolsBubblesUp ensures the error
// return from ProxyTools is observable by the caller (used by the dispatcher
// graceful degradation test in dispatcher_test.go).
func TestGetProxyToolProvider_ErrorFromProxyToolsBubblesUp(t *testing.T) {
	t.Parallel()

	r := New()
	m := &proxyFake{
		name:     "engramcore",
		proxyErr: errors.New("backend unreachable"),
	}
	require.NoError(t, r.Register(m))

	proxy, _, ok := r.GetProxyToolProvider()
	require.True(t, ok)

	_, err := proxy.ProxyTools(context.Background(), muxcore.ProjectContext{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend unreachable")
	assert.Equal(t, 1, m.proxyToolsCalls)
}
