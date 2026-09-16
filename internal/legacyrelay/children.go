package legacyrelay

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"sync"
)

// ProcessImage is an inspected executable image reference. It is never a
// project identity or a credential source.
type ProcessImage struct{ value string }

func (i ProcessImage) Value() string { return i.value }
func (i ProcessImage) valid() bool   { return validDescriptorText(i.value, 4096) }

// ProcessIdentity binds a PID to an OS-issued incarnation and observed image.
// PID alone is intentionally never sufficient for bootstrap or capability use.
type ProcessIdentity struct {
	pid         int
	incarnation string
	image       ProcessImage
	parentPID   int
}

// NewProcessIdentity constructs a complete OS process observation. Platform
// inspectors use this after reading kernel-owned process facts; tests can use
// it to model an exact process-incarnation topology.
func NewProcessIdentity(pid int, incarnation, image string, parentPID int) (ProcessIdentity, error) {
	if pid <= 0 || parentPID < 0 || parentPID == pid || !validOpaqueReference(incarnation, maxOpaqueReferenceBytes) || !validDescriptorText(image, 4096) {
		return ProcessIdentity{}, errors.New("invalid process identity")
	}
	return ProcessIdentity{pid: pid, incarnation: incarnation, image: ProcessImage{value: image}, parentPID: parentPID}, nil
}

func (p ProcessIdentity) PID() int            { return p.pid }
func (p ProcessIdentity) Image() ProcessImage { return p.image }
func (p ProcessIdentity) ParentPID() int      { return p.parentPID }
func (p ProcessIdentity) valid() bool {
	return p.pid > 0 && p.parentPID >= 0 && p.parentPID != p.pid && validOpaqueReference(p.incarnation, maxOpaqueReferenceBytes) && p.image.valid()
}

func sameProcess(left, right ProcessIdentity) bool {
	return left.valid() && right.valid() && left.pid == right.pid && left.incarnation == right.incarnation && left.image == right.image
}

// ProcessInspector reads current OS process facts without launching a helper
// process. Any error is a failed bootstrap/capability proof, never a fallback.
type ProcessInspector interface {
	Inspect(pid int) (ProcessIdentity, error)
}

// ChildImageGate accepts known Engram MCP child images. An unset gate rejects
// every image, preserving a fail-closed default until HAP-01C qualifies one.
type ChildImageGate interface {
	Accept(ProcessImage) bool
}

// ChildImageGateFunc adapts a pure image policy for tests or daemon wiring.
type ChildImageGateFunc func(ProcessImage) bool

func (f ChildImageGateFunc) Accept(image ProcessImage) bool { return f != nil && f(image) }

type rejectingChildImageGate struct{}

func (rejectingChildImageGate) Accept(ProcessImage) bool { return false }

// RejectAllChildImages is the safe runtime default before exact installed-child
// image qualification exists.
func RejectAllChildImages() ChildImageGate { return rejectingChildImageGate{} }

// ChildConfigRef and TransportRef are daemon-issued opaque references. Neither
// reveals child environment values, project selectors, or credentials.
type (
	ChildConfigRef struct{ value string }
	TransportRef   struct{ value string }
)

func (r ChildConfigRef) valid() bool { return validOpaqueReference(r.value, maxOpaqueReferenceBytes) }
func (r TransportRef) valid() bool   { return validOpaqueReference(r.value, maxOpaqueReferenceBytes) }

// ChildBinding is the safe bootstrap evidence retained by a capability. It has
// no ProjectContext CWD/ID and no raw environment map.
type ChildBinding struct {
	process      ProcessIdentity
	configRef    ChildConfigRef
	transportRef TransportRef
}

func (b ChildBinding) Process() ProcessIdentity   { return b.process }
func (b ChildBinding) ConfigRef() ChildConfigRef  { return b.configRef }
func (b ChildBinding) TransportRef() TransportRef { return b.transportRef }
func (b ChildBinding) valid() bool {
	return b.process.valid() && b.configRef.valid() && b.transportRef.valid()
}

type childRecord struct {
	binding           ChildBinding
	diagnosticProject string
	env               map[string]string
	accepted          bool
}

// ChildRegistry records only current accepted MCP-child process/config evidence
// supplied through the SessionHandlerWithSessionMeta wrapper. ProjectContext ID
// is retained solely to remove diagnostics on disconnect; it never selects a
// project or affects bootstrap matching.
type ChildRegistry struct {
	mu        sync.RWMutex
	inspector ProcessInspector
	imageGate ChildImageGate
	records   map[TransportRef]childRecord
	onReplace func(ChildBinding)
}

// NewChildRegistry returns a ready in-memory registry. Nil dependencies are
// fail-closed rather than substituted with ambient process/config authority.
func NewChildRegistry(inspector ProcessInspector, imageGate ChildImageGate) *ChildRegistry {
	if imageGate == nil {
		imageGate = RejectAllChildImages()
	}
	return &ChildRegistry{
		inspector: inspector,
		imageGate: imageGate,
		records:   make(map[TransportRef]childRecord),
	}
}

// SetReplacementHandler installs the daemon-private callback used to revoke
// capabilities and pooled credentials when the same live child is observed
// with changed child-only configuration.
func (r *ChildRegistry) SetReplacementHandler(handler func(ChildBinding)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.onReplace = handler
	r.mu.Unlock()
}

// Observe records a muxcore child only after its peer PID has been inspected.
// It copies the per-session environment only in memory for the future T3
// gateway; no raw configuration crosses a relay response or capability.
func (r *ChildRegistry) Observe(pid int, diagnosticProject string, env map[string]string) (ChildBinding, error) {
	if r == nil || r.inspector == nil {
		return ChildBinding{}, ErrBootstrapUnavailable
	}
	process, err := r.inspector.Inspect(pid)
	if err != nil || !process.valid() {
		return ChildBinding{}, ErrBootstrapUnavailable
	}
	copiedEnv := maps.Clone(env)
	accepted := r.imageGate.Accept(process.image)
	configRef, err := randomChildConfigRef()
	if err != nil {
		return ChildBinding{}, err
	}
	transportRef, err := randomTransportRef()
	if err != nil {
		return ChildBinding{}, err
	}

	r.mu.Lock()
	r.pruneDeadLocked()
	var replaced []ChildBinding
	for transport, record := range r.records {
		if !sameProcess(record.binding.process, process) || record.diagnosticProject != diagnosticProject {
			continue
		}
		if maps.Equal(record.env, copiedEnv) {
			binding := record.binding
			r.mu.Unlock()
			return binding, nil
		}
		delete(r.records, transport)
		replaced = append(replaced, record.binding)
	}
	binding := ChildBinding{process: process, configRef: configRef, transportRef: transportRef}
	r.records[transportRef] = childRecord{
		binding:           binding,
		diagnosticProject: diagnosticProject,
		env:               copiedEnv,
		accepted:          accepted,
	}
	onReplace := r.onReplace
	r.mu.Unlock()
	if onReplace != nil {
		for _, oldBinding := range replaced {
			onReplace(oldBinding)
		}
	}
	return binding, nil
}

// ForgetProjectDiagnostic removes stale evidence after muxcore reports a
// disconnect. Matching uses the diagnostic only as a cleanup key, never as
// host-session or canonical-project authority.
func (r *ChildRegistry) ForgetProjectDiagnostic(projectID string) {
	if r == nil || projectID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for transport, record := range r.records {
		if record.diagnosticProject == projectID {
			delete(r.records, transport)
		}
	}
}

// ConfigFor returns a copied in-memory child config only to daemon-private T3
// integration code that already holds the opaque reference. It never serializes
// this map on the relay wire.
func (r *ChildRegistry) ConfigFor(ref ChildConfigRef) (map[string]string, bool) {
	if r == nil || !ref.valid() {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.recordsByConfigRefLocked(ref)
	if !ok || !record.accepted || !r.recordIsLive(record) {
		if ok {
			delete(r.records, record.binding.transportRef)
		}
		return nil, false
	}
	return maps.Clone(record.env), true
}

func (r *ChildRegistry) recordsByConfigRefLocked(ref ChildConfigRef) (childRecord, bool) {
	for _, record := range r.records {
		if record.binding.configRef == ref {
			return record, true
		}
	}
	return childRecord{}, false
}

func (r *ChildRegistry) acceptedRecords() []childRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneDeadLocked()
	result := make([]childRecord, 0, len(r.records))
	for _, record := range r.records {
		if record.accepted {
			result = append(result, childRecord{
				binding:           record.binding,
				diagnosticProject: record.diagnosticProject,
				env:               nil,
				accepted:          true,
			})
		}
	}
	return result
}

func (r *ChildRegistry) current(binding ChildBinding) bool {
	if r == nil || !binding.valid() {
		return false
	}
	r.mu.RLock()
	record, ok := r.records[binding.transportRef]
	r.mu.RUnlock()
	return ok && record.accepted && record.binding == binding
}

func (r *ChildRegistry) pruneDeadLocked() {
	for transport, record := range r.records {
		if !r.recordIsLive(record) {
			delete(r.records, transport)
		}
	}
}

func (r *ChildRegistry) recordIsLive(record childRecord) bool {
	if r.inspector == nil || !record.binding.valid() {
		return false
	}
	current, err := r.inspector.Inspect(record.binding.process.pid)
	return err == nil && sameProcess(record.binding.process, current)
}

func randomChildConfigRef() (ChildConfigRef, error) {
	value, err := randomOpaqueReference()
	return ChildConfigRef{value: value}, err
}

func randomTransportRef() (TransportRef, error) {
	value, err := randomOpaqueReference()
	return TransportRef{value: value}, err
}

func randomOpaqueReference() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate daemon-local reference: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
