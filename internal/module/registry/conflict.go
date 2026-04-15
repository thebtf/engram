package registry

import (
	"github.com/thebtf/engram/internal/module"
)

// ToolByName looks up a tool by its globally unique name in the registry's
// tool index.
//
// Returns (entry, toolDef, true) if the tool is found, or (nil, nil, false)
// if it is not. The returned Entry pointer is valid for the daemon lifetime
// (registry is immutable after Freeze).
//
// Callers MUST NOT mutate the returned ToolDef — ToolDef values are owned by
// the module and treated as immutable after registration.
//
// Design reference: design.md Section 5.1 (happy-path tool call data flow)
// and FR-3 (tool-name conflict detection).
func (r *Registry) ToolByName(name string) (*Entry, *module.ToolDef, bool) {
	idx, ok := r.toolIndex[name]
	if !ok {
		return nil, nil, false
	}
	me := &r.entries[idx]
	// Find the ToolDef within the provider's list.
	for _, td := range me.ToolProv.Tools() {
		if td.Name == name {
			def := td // copy so caller has a stable pointer
			e := &Entry{
				Module:       me.Module,
				Snap:         me.Snap,
				Lifecycle:    me.Lifecycle,
				RemovalAware: me.RemovalAware,
				ToolProv:     me.ToolProv,
				ProxyTool:    me.ProxyTool,
			}
			return e, &def, true
		}
	}
	// Defensive: tool index referenced this entry but the tool is gone.
	// Should never happen — Tools() must be stable per the module contract.
	return nil, nil, false
}

// AggregateTools returns a flat slice of all ToolDef values from all
// ToolProvider modules, in module registration order.
//
// This centralises the tools/list aggregation used by the dispatcher so that
// the Dispatcher itself has zero knowledge of module iteration — see
// design.md Section 2.2 (Dispatcher responsibilities) and FR-4.
func (r *Registry) AggregateTools() []module.ToolDef {
	var result []module.ToolDef
	for i := range r.entries {
		if r.entries[i].ToolProv != nil {
			result = append(result, r.entries[i].ToolProv.Tools()...)
		}
	}
	return result
}
