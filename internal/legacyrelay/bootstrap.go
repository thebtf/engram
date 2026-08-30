package legacyrelay

// BootstrapSelection contains only the direct OMP/child process observations
// needed to bind a capability. It deliberately retains no ancestry chain.
type BootstrapSelection struct {
	host  ProcessIdentity
	child ChildBinding
}

func (s BootstrapSelection) Host() ProcessIdentity { return s.host }
func (s BootstrapSelection) Child() ChildBinding   { return s.child }
func (s BootstrapSelection) valid() bool {
	return s.host.valid() && s.child.valid()
}

// Bootstrapper selects one current accepted MCP child from bounded OS ancestry
// evidence. It never treats CWD, muxcore ProjectContext ID, PID alone, or the
// daemon generation as semantic host/project identity.
type Bootstrapper struct {
	children  *ChildRegistry
	inspector ProcessInspector
	maxDepth  int
}

// NewBootstrapper makes a fail-closed selector. A non-positive depth disables
// selection rather than allowing an unbounded parent walk.
func NewBootstrapper(children *ChildRegistry, inspector ProcessInspector, maxDepth int) Bootstrapper {
	return Bootstrapper{children: children, inspector: inspector, maxDepth: maxDepth}
}

// SelectPeer proves that exactly one accepted child is currently a descendant
// of the requesting OMP peer incarnation. Any inspection failure, PID reuse,
// exit, cycle, depth exhaustion, zero match, or multiple match fails closed.
func (b Bootstrapper) SelectPeer(peerPID int) (BootstrapSelection, error) {
	if b.children == nil || b.inspector == nil || b.maxDepth <= 0 || peerPID <= 0 {
		return BootstrapSelection{}, ErrBootstrapUnavailable
	}
	host, err := b.inspector.Inspect(peerPID)
	if err != nil || !host.valid() {
		return BootstrapSelection{}, ErrBootstrapUnavailable
	}

	var matches []ChildBinding
	for _, candidate := range b.children.acceptedRecords() {
		if !b.children.current(candidate.binding) {
			continue
		}
		liveChild, err := b.inspector.Inspect(candidate.binding.process.pid)
		if err != nil || !sameProcess(candidate.binding.process, liveChild) {
			continue
		}
		if !b.hostIsAncestor(liveChild, host) {
			continue
		}
		// Re-read the child once after its ancestry walk. This catches a process
		// exit or PID reuse that raced the evidence walk without storing ancestry.
		afterWalk, err := b.inspector.Inspect(candidate.binding.process.pid)
		if err != nil || !sameProcess(candidate.binding.process, afterWalk) {
			continue
		}
		matches = append(matches, candidate.binding)
	}

	if len(matches) == 0 {
		return BootstrapSelection{}, ErrBootstrapUnavailable
	}
	if len(matches) != 1 {
		return BootstrapSelection{}, ErrBootstrapAmbiguous
	}
	afterSelection, err := b.inspector.Inspect(peerPID)
	if err != nil || !sameProcess(host, afterSelection) {
		return BootstrapSelection{}, ErrBootstrapUnavailable
	}
	return BootstrapSelection{host: host, child: matches[0]}, nil
}

func (b Bootstrapper) hostIsAncestor(child, host ProcessIdentity) bool {
	current := child
	for depth := 0; depth < b.maxDepth; depth++ {
		parentPID := current.parentPID
		if parentPID <= 0 || parentPID == current.pid {
			return false
		}
		parent, err := b.inspector.Inspect(parentPID)
		if err != nil || !parent.valid() {
			return false
		}
		if parent.pid == host.pid {
			return sameProcess(parent, host)
		}
		current = parent
	}
	return false
}
