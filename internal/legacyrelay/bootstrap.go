package legacyrelay

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// RuntimeInstanceRef is an opaque host-runtime reference. Its value never
// exposes process facts, diagnostic project identity, or a working directory.
type RuntimeInstanceRef struct{ value string }

func (r RuntimeInstanceRef) Value() string { return r.value }
func (r RuntimeInstanceRef) valid() bool {
	return validOpaqueReference(r.value, maxOpaqueReferenceBytes)
}

// BootstrapSelection contains only the direct OMP/child process observations
// needed to bind a capability. It deliberately retains no ancestry chain.
type BootstrapSelection struct {
	host  ProcessIdentity
	child ChildBinding
}

func (s BootstrapSelection) Host() ProcessIdentity { return s.host }
func (s BootstrapSelection) Child() ChildBinding   { return s.child }

// RuntimeInstanceRef derives a stable reference for this accepted OMP peer,
// child-process incarnation, and daemon lifecycle. It intentionally accepts no
// project, cwd, session, or environment input.
func (s BootstrapSelection) RuntimeInstanceRef(generation DaemonGeneration) (RuntimeInstanceRef, error) {
	if !s.valid() || !generation.valid() {
		return RuntimeInstanceRef{}, ErrBootstrapUnavailable
	}

	hash := sha256.New()
	_, _ = hash.Write([]byte("engram.host-advisor.runtime-instance/v1\x00"))
	for _, part := range [...]string{s.host.incarnation, s.child.process.incarnation, generation.Value()} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(part)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(part))
	}
	return RuntimeInstanceRef{value: "sha256-" + hex.EncodeToString(hash.Sum(nil))}, nil
}

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

	matches := b.descendantAcceptedChildren(host)

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

func (b Bootstrapper) descendantAcceptedChildren(host ProcessIdentity) []ChildBinding {
	var matches []ChildBinding
	for _, candidate := range b.children.acceptedRecords() {
		if !b.currentChildDescendsFrom(candidate.binding, host) {
			continue
		}
		matches = append(matches, candidate.binding)
	}
	return matches
}

func (b Bootstrapper) currentChildDescendsFrom(candidate ChildBinding, host ProcessIdentity) bool {
	if !b.children.current(candidate) {
		return false
	}
	liveChild, err := b.inspector.Inspect(candidate.process.pid)
	if err != nil || !sameProcess(candidate.process, liveChild) || !b.hostIsAncestor(liveChild, host) {
		return false
	}
	afterWalk, err := b.inspector.Inspect(candidate.process.pid)
	return err == nil && sameProcess(candidate.process, afterWalk)
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
