// Package registry implements the frozen-after-boot module container with
// capability discovery for the engram modular daemon framework.
//
// Design reference: design.md Section 2.2 (Registry responsibilities) and
// Section 3.3 (optional capabilities).
package registry

import (
	"github.com/thebtf/engram/internal/module"
)

// moduleEntry caches one registered module alongside all typed capability
// references discovered via type assertions at Register time.
//
// Type assertions are performed ONCE during Register and their results stored
// here. The lifecycle pipeline and dispatcher iterate these pre-filtered typed
// refs with zero runtime type assertions in the hot path — see design.md
// Section 2.2 and architectural decision D18.
type moduleEntry struct {
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
	// At most one module in the registry may have a non-nil ProxyTool —
	// enforced at Register time via [ErrMultipleProxyToolProviders].
	ProxyTool module.ProxyToolProvider
}
