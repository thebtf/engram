package registry

import (
	"errors"
	"fmt"

	"github.com/thebtf/engram/internal/module"
)

// ErrRegistryFrozen is returned by Register when called after Freeze.
// Per design.md Section 2.2: the registry is append-only-before-freeze and
// read-only-after-freeze for the lifetime of the daemon.
var ErrRegistryFrozen = errors.New("registry is frozen: Register called after Freeze")

// ErrMultipleProxyToolProviders is returned by Register when a second module
// implementing module.ProxyToolProvider is registered. Per FR-11a: at most
// ONE proxy tool provider is allowed in the registry to prevent ambiguous
// routing when a tool name falls through the static ToolProvider lookup.
var ErrMultipleProxyToolProviders = errors.New("only one module may implement ProxyToolProvider")

// Registry is the frozen-after-boot module container. It stores modules in
// registration order and caches capability references discovered at Register
// time, enabling zero-allocation, zero-type-assertion reads during runtime.
//
// Thread-safety: Register and Freeze are called sequentially during daemon
// startup (before muxcore engine.Run). After Freeze the registry is read-only
// and therefore safe for concurrent access without locks — see design decision
// D18 in plan.md. A sync.RWMutex is deferred to v0.2+.
//
// Registry also implements module.ModuleLookup so it can be passed as
// ModuleDeps.Lookup to Init — see FR-2 and design.md Section 3.2.
type Registry struct {
	entries   []moduleEntry
	toolIndex map[string]int // tool name → index into entries slice
	// proxyIdx is the entries[] index of the registered ProxyToolProvider, or
	// -1 if none. Only one proxy provider is allowed per FR-11a. Cached here
	// for O(1) lookup by the dispatcher on every tools/call fallthrough.
	proxyIdx int
	frozen   bool
}

// New returns an empty, unfrozen Registry.
func New() *Registry {
	return &Registry{
		toolIndex: make(map[string]int),
		proxyIdx:  -1,
	}
}

// Register adds m to the registry, performs capability discovery via four type
// assertions, and validates tool-name uniqueness across all registered modules.
//
// Returns ErrRegistryFrozen if Freeze has already been called.
// Returns an error if m.Name() is empty or duplicate.
// Returns an error (naming BOTH conflicting modules) if any tool name in
// m.Tools() already belongs to another registered module — see FR-3.
func (r *Registry) Register(m module.EngramModule) error {
	if r.frozen {
		return ErrRegistryFrozen
	}
	if m.Name() == "" {
		return errors.New("module name must not be empty")
	}
	// Duplicate name check.
	for i := range r.entries {
		if r.entries[i].Module.Name() == m.Name() {
			return fmt.Errorf("module %q is already registered", m.Name())
		}
	}

	// Capability discovery — five type assertions, results cached in entry.
	entry := moduleEntry{Module: m}
	if s, ok := m.(module.Snapshotter); ok {
		entry.Snap = s
	}
	if l, ok := m.(module.ProjectLifecycle); ok {
		entry.Lifecycle = l
	}
	if ra, ok := m.(module.ProjectRemovalAware); ok {
		entry.RemovalAware = ra
	}
	if tp, ok := m.(module.ToolProvider); ok {
		entry.ToolProv = tp
	}
	if ptp, ok := m.(module.ProxyToolProvider); ok {
		// Single-instance enforcement — FR-11a. Reject the second proxy
		// provider before mutating any registry state so partial-registration
		// bugs are impossible.
		if r.proxyIdx >= 0 {
			return fmt.Errorf(
				"%w: module %q conflicts with already-registered proxy %q",
				ErrMultipleProxyToolProviders,
				m.Name(),
				r.entries[r.proxyIdx].Module.Name(),
			)
		}
		entry.ProxyTool = ptp
	}

	// Tool-name conflict detection — iterate new module's tools before appending.
	if entry.ToolProv != nil {
		for _, td := range entry.ToolProv.Tools() {
			if existingIdx, conflict := r.toolIndex[td.Name]; conflict {
				return fmt.Errorf(
					"tool name %q already provided by module %q, conflicts with module %q",
					td.Name, r.entries[existingIdx].Module.Name(), m.Name(),
				)
			}
		}
		// No conflicts — register tools into the index.
		idx := len(r.entries)
		r.entries = append(r.entries, entry)
		for _, td := range entry.ToolProv.Tools() {
			r.toolIndex[td.Name] = idx
		}
		if entry.ProxyTool != nil {
			r.proxyIdx = idx
		}
		return nil
	}

	idx := len(r.entries)
	r.entries = append(r.entries, entry)
	if entry.ProxyTool != nil {
		r.proxyIdx = idx
	}
	return nil
}

// Freeze marks the registry as immutable. Any subsequent Register call returns
// ErrRegistryFrozen. Freeze is idempotent — calling it multiple times is safe.
//
// Per design.md Section 4.1: Freeze is called in main.go after all modules are
// registered, before lifecycle.Pipeline.Start.
func (r *Registry) Freeze() {
	r.frozen = true
}

// Has reports whether a module with the given name is registered.
// Implements module.ModuleLookup — safe to call after Freeze.
func (r *Registry) Has(name string) bool {
	for i := range r.entries {
		if r.entries[i].Module.Name() == name {
			return true
		}
	}
	return false
}

// ListNames returns the names of all registered modules in registration order.
// Implements module.ModuleLookup — safe to call after Freeze.
func (r *Registry) ListNames() []string {
	names := make([]string, len(r.entries))
	for i, e := range r.entries {
		names[i] = e.Module.Name()
	}
	return names
}

// ForEachProjectRemovalAware calls fn for each module that implements
// module.ProjectRemovalAware, in registration order.
//
// Used by the serverevents bridge (Phase 6) and the moduletest harness
// (SimulateProjectRemoved) — see tasks-trace.md C2 and T043.
func (r *Registry) ForEachProjectRemovalAware(fn func(module.ProjectRemovalAware)) {
	for i := range r.entries {
		if r.entries[i].RemovalAware != nil {
			fn(r.entries[i].RemovalAware)
		}
	}
}

// ForEachLifecycleHandler calls fn for each module that implements
// module.ProjectLifecycle, in registration order.
func (r *Registry) ForEachLifecycleHandler(fn func(module.ProjectLifecycle)) {
	for i := range r.entries {
		if r.entries[i].Lifecycle != nil {
			fn(r.entries[i].Lifecycle)
		}
	}
}

// ForEachSnapshotter calls fn for each module that implements
// module.Snapshotter, in registration order.
func (r *Registry) ForEachSnapshotter(fn func(name string, s module.Snapshotter)) {
	for i := range r.entries {
		if r.entries[i].Snap != nil {
			fn(r.entries[i].Module.Name(), r.entries[i].Snap)
		}
	}
}

// GetProxyToolProvider returns the registered proxy tool provider and its
// owning module name, or (nil, "", false) if none is registered.
//
// Called by the dispatcher on every tools/list (to append dynamic tools) and
// on every tools/call fallthrough (to route tools not found in any static
// ToolProvider). O(1) — uses the cached proxyIdx.
func (r *Registry) GetProxyToolProvider() (module.ProxyToolProvider, string, bool) {
	if r.proxyIdx < 0 {
		return nil, "", false
	}
	e := r.entries[r.proxyIdx]
	return e.ProxyTool, e.Module.Name(), true
}

// Entry is an exported view of a registered module with cached capability
// references. It is returned by Entries for use by the lifecycle pipeline.
// Callers MUST NOT mutate any field.
//
// Design reference: design.md Section 2.2 — typed refs cached at Register
// time, zero runtime type assertions in the hot path.
type Entry struct {
	// Module is the core interface. Always non-nil.
	Module module.EngramModule
	// Snap is non-nil if Module implements module.Snapshotter.
	Snap module.Snapshotter
	// Lifecycle is non-nil if Module implements module.ProjectLifecycle.
	Lifecycle module.ProjectLifecycle
	// RemovalAware is non-nil if Module implements module.ProjectRemovalAware.
	RemovalAware module.ProjectRemovalAware
	// ToolProv is non-nil if Module implements module.ToolProvider.
	ToolProv module.ToolProvider
	// ProxyTool is non-nil if Module implements module.ProxyToolProvider.
	ProxyTool module.ProxyToolProvider
}

// ListLifecycleHandlers returns a slice of all modules that implement
// module.ProjectLifecycle, in registration order.
//
// Used by the moduletest harness (Phase 4, T028) for SimulateSessionConnect.
func (r *Registry) ListLifecycleHandlers() []module.ProjectLifecycle {
	var result []module.ProjectLifecycle
	for i := range r.entries {
		if r.entries[i].Lifecycle != nil {
			result = append(result, r.entries[i].Lifecycle)
		}
	}
	return result
}

// SnapshotterEntry pairs a module name with its Snapshotter implementation.
// Returned by ListSnapshotters for use in the moduletest harness (Phase 4).
type SnapshotterEntry struct {
	// Name is the module's stable identifier (EngramModule.Name()).
	Name string
	// Snap is the module's Snapshotter implementation.
	Snap module.Snapshotter
}

// ListSnapshotters returns a slice of SnapshotterEntry values for all modules
// that implement module.Snapshotter, in registration order.
//
// Used by the moduletest harness (Phase 4, T029) for TakeSnapshot.
func (r *Registry) ListSnapshotters() []SnapshotterEntry {
	var result []SnapshotterEntry
	for i := range r.entries {
		if r.entries[i].Snap != nil {
			result = append(result, SnapshotterEntry{
				Name: r.entries[i].Module.Name(),
				Snap: r.entries[i].Snap,
			})
		}
	}
	return result
}

// Entries returns a slice of Entry values in registration order.
// Callers MUST NOT modify the returned slice.
// Intended for lifecycle pipeline iteration.
func (r *Registry) Entries() []Entry {
	result := make([]Entry, len(r.entries))
	for i, e := range r.entries {
		result[i] = Entry{
			Module:       e.Module,
			Snap:         e.Snap,
			Lifecycle:    e.Lifecycle,
			RemovalAware: e.RemovalAware,
			ToolProv:     e.ToolProv,
			ProxyTool:    e.ProxyTool,
		}
	}
	return result
}
