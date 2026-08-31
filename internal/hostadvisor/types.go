// Package hostadvisor owns the closed, process-memory host-advisor binding domain.
package hostadvisor

import (
	"errors"
	"time"
)

var (
	// ErrInvalidInput identifies a malformed or non-canonical host-advisor input.
	ErrInvalidInput = errors.New("host advisor input is invalid")
	// ErrUnsupported identifies a well-formed request outside the immutable catalog.
	ErrUnsupported = errors.New("host advisor request is unsupported")
	// ErrBindingUnavailable identifies an expired, superseded, or subject-mismatched binding.
	ErrBindingUnavailable = errors.New("host advisor binding is unavailable")
	// ErrCapacity identifies a registry that cannot allocate another live channel.
	ErrCapacity = errors.New("host advisor binding capacity exhausted")
)

// Digest is a fixed SHA-256 digest.
type Digest [32]byte

func (d Digest) isZero() bool {
	return d == Digest{}
}

// AuthenticatedSubject is the server-derived, opaque binding subject.
type AuthenticatedSubject struct {
	proof Digest
}

// NewAuthenticatedSubject constructs a subject from a non-zero server proof.
func NewAuthenticatedSubject(proof Digest) (AuthenticatedSubject, error) {
	if proof.isZero() {
		return AuthenticatedSubject{}, ErrInvalidInput
	}
	return AuthenticatedSubject{proof: proof}, nil
}

// ProofBytes returns a copy of the subject proof suitable for the private wire response.
func (s AuthenticatedSubject) ProofBytes() []byte {
	proof := make([]byte, len(s.proof))
	copy(proof, s.proof[:])
	return proof
}

func (s AuthenticatedSubject) equal(other AuthenticatedSubject) bool {
	return s.proof == other.proof
}

// BindingID is an opaque registry-generated binding reference.
type BindingID string

// String returns the opaque binding reference for the private wire response.
func (id BindingID) String() string {
	return string(id)
}

// HostFamily is the closed set of recognized host families.
type HostFamily uint8

const (
	HostFamilyUnspecified HostFamily = iota
	HostFamilyOMP
	HostFamilyClaudeCode
	HostFamilyCodex
)

// ArtifactKind is the closed set of artifact provenance kinds.
type ArtifactKind uint8

const (
	ArtifactKindUnspecified ArtifactKind = iota
	ArtifactKindInstalled
	ArtifactKindSource
)

// Semantic is the closed set of host callback semantics.
type Semantic uint8

const (
	SemanticUnspecified Semantic = iota
	SemanticBeforeAgentStart
	SemanticToolResult
	SemanticToolAction
	SemanticSessionFinalization
)

// Action is the closed set of host-advisor actions.
type Action uint8

const (
	ActionUnspecified Action = iota
	ActionEmitAdvice
	ActionAdapterAttestation
	ActionAllow
	ActionBlock
	ActionRewrite
)

// InjectionMode is the closed set of host context injection surfaces.
type InjectionMode uint8

const (
	InjectionModeUnspecified InjectionMode = iota
	InjectionModeHiddenUntrustedMessage
	InjectionModeDeveloperContext
	InjectionModeAdditionalContext
)

// CallbackOrdering is the closed set of callback ordering guarantees.
type CallbackOrdering uint8

const (
	CallbackOrderingUnspecified CallbackOrdering = iota
	CallbackOrderingBeforeFirstAction
	CallbackOrderingAfterToolResult
	CallbackOrderingOtherProvenOrder
)

// Acknowledgement is the closed set of host acknowledgement semantics.
type Acknowledgement uint8

const (
	AcknowledgementUnspecified Acknowledgement = iota
	AcknowledgementNone
	AcknowledgementAdapterAttested
	AcknowledgementHostAcknowledged
	AcknowledgementConsumptionObserved
)

// ProtocolRange is the inclusive protocol range offered by a host or accepted by a profile.
type ProtocolRange struct {
	Min uint32
	Max uint32
}

// Correlation declares the host references the adapter guarantees for a capability.
type Correlation struct {
	Session           bool
	Turn              bool
	ToolAction        bool
	StablePhaseAnchor bool
}

// CallbackContract declares the host callback properties required for a capability.
type CallbackContract struct {
	Awaited  bool
	Deadline time.Duration
	Ordering CallbackOrdering
}

// Capability is one closed host-advisor capability row.
type Capability struct {
	Semantic        Semantic
	Actions         []Action
	InjectionModes  []InjectionMode
	Correlation     Correlation
	Callback        CallbackContract
	Acknowledgement Acknowledgement
}

// EvidenceRef ties a host profile to a bounded, proven artifact and runtime probe.
type EvidenceRef struct {
	ArtifactKind          ArtifactKind
	ArtifactSHA256        Digest
	RuntimeProbeReceiptID string
}

// HostIdentity carries only host and adapter runtime identity facts.
type HostIdentity struct {
	Family             HostFamily
	HostVersion        string
	AdapterID          string
	AdapterVersion     string
	RuntimeInstanceRef string
}

// BoundChannel is the immutable normalized host-channel commitment stored with
// a binding. Raw adapter and runtime references never survive in HostBinding.
type BoundChannel struct {
	hostFamily HostFamily
	commitment Digest
}

// HostFamily returns the recognized host family for the bound channel.
func (c BoundChannel) HostFamily() HostFamily {
	return c.hostFamily
}

// Commitment returns the deterministic commitment to adapter and runtime
// identity without exposing either raw value.
func (c BoundChannel) Commitment() Digest {
	return c.commitment
}

// HostHello is the complete binding material supplied by the authenticated daemon.
type HostHello struct {
	Protocol  ProtocolRange
	Host      HostIdentity
	Requested []Capability
	Evidence  EvidenceRef
}

// AcceptedProfile is a server-owned immutable catalog entry after NewRegistry copies it.
type AcceptedProfile struct {
	CapabilityRevision string
	SnapshotID         string
	SnapshotRevision   uint64
	Protocol           ProtocolRange
	HostFamily         HostFamily
	HostVersion        string
	AdapterID          string
	AdapterVersion     string
	Evidence           EvidenceRef
	Capabilities       []Capability
	BindingTTL         time.Duration
}

// OMPAdvisor1ProfileSpec supplies the host-specific facts for the code-owned omp-advisor-1 profile.
type OMPAdvisor1ProfileSpec struct {
	HostVersion             string
	AdapterID               string
	AdapterVersion          string
	InstalledArtifactDigest Digest
	RuntimeProbeReceiptID   string
	SnapshotID              string
	SnapshotRevision        uint64
	CallbackDeadline        time.Duration
	BindingTTL              time.Duration
}

// RegistryConfig makes the process-memory registry dependencies deterministic and bounded.
type RegistryConfig struct {
	Profiles     []AcceptedProfile
	MaxBindings  int
	Now          func() time.Time
	NewBindingID func() (BindingID, error)
}

// AcceptedCapabilitySnapshot is the immutable subset returned by a successful binding.
type AcceptedCapabilitySnapshot struct {
	snapshotID         string
	contractDigest     Digest
	revision           uint64
	capabilityRevision string
	capabilities       []Capability
}

// SnapshotID returns the server-owned snapshot identity.
func (s AcceptedCapabilitySnapshot) SnapshotID() string {
	return s.snapshotID
}

// ContractDigest returns a value copy of the deterministic accepted-subset digest.
func (s AcceptedCapabilitySnapshot) ContractDigest() Digest {
	return s.contractDigest
}

// Revision returns the server-owned snapshot revision.
func (s AcceptedCapabilitySnapshot) Revision() uint64 {
	return s.revision
}

// CapabilityRevision returns the server-owned capability revision.
func (s AcceptedCapabilitySnapshot) CapabilityRevision() string {
	return s.capabilityRevision
}

// Capabilities returns a deep copy of the accepted requested subset.
func (s AcceptedCapabilitySnapshot) Capabilities() []Capability {
	return cloneCapabilities(s.capabilities)
}

// HostBinding is an immutable, expiring binding result.
type HostBinding struct {
	id               BindingID
	snapshot         AcceptedCapabilitySnapshot
	expiresAt        time.Time
	callbackDeadline time.Duration
	subject          AuthenticatedSubject
	channel          BoundChannel
}

// ID returns the opaque binding reference.
func (b HostBinding) ID() BindingID {
	return b.id
}

// Snapshot returns an immutable accepted subset snapshot.
func (b HostBinding) Snapshot() AcceptedCapabilitySnapshot {
	return cloneSnapshot(b.snapshot)
}

// ExpiresAt returns the original UTC expiry of the binding.
func (b HostBinding) ExpiresAt() time.Time {
	return b.expiresAt
}

// CallbackDeadline returns the accepted callback deadline.
func (b HostBinding) CallbackDeadline() time.Duration {
	return b.callbackDeadline
}

// Subject returns the server-derived binding subject.
func (b HostBinding) Subject() AuthenticatedSubject {
	return b.subject
}

// Channel returns the immutable normalized host channel for the binding.
func (b HostBinding) Channel() BoundChannel {
	return b.channel
}

func cloneCapabilities(capabilities []Capability) []Capability {
	if len(capabilities) == 0 {
		return nil
	}
	clone := make([]Capability, len(capabilities))
	for index, capability := range capabilities {
		clone[index] = capability
		clone[index].Actions = append([]Action(nil), capability.Actions...)
		clone[index].InjectionModes = append([]InjectionMode(nil), capability.InjectionModes...)
	}
	return clone
}

func cloneSnapshot(snapshot AcceptedCapabilitySnapshot) AcceptedCapabilitySnapshot {
	snapshot.capabilities = cloneCapabilities(snapshot.capabilities)
	return snapshot
}
