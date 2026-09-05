package hostadvisor

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ompAdvisor1CapabilityRevision = "omp-advisor-1"
	ompAdvisor2CapabilityRevision = "omp-advisor-2"
	ompAdvisor1ProtocolVersion    = uint32(1)
	maxAdvisorTextBytes           = 512
	maxAdvisorCapabilities        = 4
	maxAdvisorActions             = 5
	maxAdvisorInjectionModes      = 3
)

// NewOMPAdvisor1Profile constructs the legacy HAP-02 OMP capability profile.
// It remains available only so existing adapters can bind and then fail closed
// at the context-reference action boundary.
func NewOMPAdvisor1Profile(spec OMPAdvisor1ProfileSpec) (AcceptedProfile, error) {
	return newOMPAdvisorProfile(spec, ompAdvisor1CapabilityRevision, []Action{ActionEmitAdvice, ActionAdapterAttestation})
}

// NewOMPAdvisor2Profile constructs the M1 context-reference OMP capability
// profile. The action is distinct from legacy generic advice so old adapters
// cannot silently receive reference packets.
func NewOMPAdvisor2Profile(spec OMPAdvisor1ProfileSpec) (AcceptedProfile, error) {
	return newOMPAdvisorProfile(spec, ompAdvisor2CapabilityRevision, []Action{ActionAdapterAttestation, ActionEmitContextReference})
}

func newOMPAdvisorProfile(spec OMPAdvisor1ProfileSpec, capabilityRevision string, actions []Action) (AcceptedProfile, error) {
	if spec.CallbackDeadline <= 0 || spec.CallbackDeadline%time.Millisecond != 0 || spec.CallbackDeadline.Milliseconds() > math.MaxUint32 {
		return AcceptedProfile{}, fmt.Errorf("%w: omp callback deadline must be a positive whole millisecond", ErrInvalidInput)
	}
	if spec.BindingTTL <= 0 {
		return AcceptedProfile{}, fmt.Errorf("%w: omp binding TTL must be positive", ErrInvalidInput)
	}

	profile := AcceptedProfile{
		CapabilityRevision: capabilityRevision,
		SnapshotID:         spec.SnapshotID,
		SnapshotRevision:   spec.SnapshotRevision,
		Protocol: ProtocolRange{
			Min: ompAdvisor1ProtocolVersion,
			Max: ompAdvisor1ProtocolVersion,
		},
		HostFamily:     HostFamilyOMP,
		HostVersion:    spec.HostVersion,
		AdapterID:      spec.AdapterID,
		AdapterVersion: spec.AdapterVersion,
		Evidence: EvidenceRef{
			ArtifactKind:          ArtifactKindInstalled,
			ArtifactSHA256:        spec.InstalledArtifactDigest,
			RuntimeProbeReceiptID: spec.RuntimeProbeReceiptID,
		},
		Capabilities: []Capability{{
			Semantic:       SemanticBeforeAgentStart,
			Actions:        actions,
			InjectionModes: []InjectionMode{InjectionModeHiddenUntrustedMessage},
			Correlation: Correlation{
				Session:           true,
				Turn:              true,
				ToolAction:        false,
				StablePhaseAnchor: true,
			},
			Callback: CallbackContract{
				Awaited:  true,
				Deadline: spec.CallbackDeadline,
				Ordering: CallbackOrderingBeforeFirstAction,
			},
			Acknowledgement: AcknowledgementAdapterAttested,
		}},
		BindingTTL: spec.BindingTTL,
	}
	return normalizeProfile(profile)
}

type normalizedHello struct {
	protocol  ProtocolRange
	host      HostIdentity
	requested []Capability
	evidence  EvidenceRef
}

func normalizeProfile(profile AcceptedProfile) (AcceptedProfile, error) {
	if !validText(profile.CapabilityRevision) || !validText(profile.SnapshotID) || profile.SnapshotRevision == 0 {
		return AcceptedProfile{}, fmt.Errorf("%w: profile snapshot metadata is invalid", ErrInvalidInput)
	}
	if !validProtocolRange(profile.Protocol) || !validHostFamily(profile.HostFamily) || !validText(profile.HostVersion) || !validText(profile.AdapterID) || !validText(profile.AdapterVersion) {
		return AcceptedProfile{}, fmt.Errorf("%w: profile host identity is invalid", ErrInvalidInput)
	}
	if !validEvidence(profile.Evidence) {
		return AcceptedProfile{}, fmt.Errorf("%w: profile evidence is invalid", ErrInvalidInput)
	}
	if profile.BindingTTL <= 0 || len(profile.Capabilities) == 0 || len(profile.Capabilities) > maxAdvisorCapabilities {
		return AcceptedProfile{}, fmt.Errorf("%w: profile binding contract is invalid", ErrInvalidInput)
	}

	profile.Capabilities = cloneCapabilities(profile.Capabilities)
	var prior Semantic
	var callbackDeadline time.Duration
	for index, capability := range profile.Capabilities {
		if !validCapability(capability) {
			return AcceptedProfile{}, fmt.Errorf("%w: profile capability is invalid", ErrInvalidInput)
		}
		if index != 0 && capability.Semantic <= prior {
			return AcceptedProfile{}, fmt.Errorf("%w: profile capabilities are not canonical", ErrInvalidInput)
		}
		if index == 0 {
			callbackDeadline = capability.Callback.Deadline
		} else if capability.Callback.Deadline != callbackDeadline {
			return AcceptedProfile{}, fmt.Errorf("%w: profile callback deadlines disagree", ErrInvalidInput)
		}
		prior = capability.Semantic
	}
	if !isOMPAdvisor1Profile(profile) && !isOMPAdvisor2Profile(profile) {
		return AcceptedProfile{}, fmt.Errorf("%w: profile capability contract is not code-owned", ErrInvalidInput)
	}
	return profile, nil
}

func isOMPAdvisor1Profile(profile AcceptedProfile) bool {
	return isOMPAdvisorProfile(profile, ompAdvisor1CapabilityRevision, []Action{ActionEmitAdvice, ActionAdapterAttestation})
}

func isOMPAdvisor2Profile(profile AcceptedProfile) bool {
	return isOMPAdvisorProfile(profile, ompAdvisor2CapabilityRevision, []Action{ActionAdapterAttestation, ActionEmitContextReference})
}

func isOMPAdvisorProfile(profile AcceptedProfile, capabilityRevision string, actions []Action) bool {
	if profile.CapabilityRevision != capabilityRevision ||
		profile.Protocol != (ProtocolRange{Min: ompAdvisor1ProtocolVersion, Max: ompAdvisor1ProtocolVersion}) ||
		profile.HostFamily != HostFamilyOMP || profile.Evidence.ArtifactKind != ArtifactKindInstalled ||
		len(profile.Capabilities) != 1 {
		return false
	}
	capability := profile.Capabilities[0]
	return capability.Semantic == SemanticBeforeAgentStart &&
		slices.Equal(capability.Actions, actions) &&
		slices.Equal(capability.InjectionModes, []InjectionMode{InjectionModeHiddenUntrustedMessage}) &&
		capability.Correlation == (Correlation{Session: true, Turn: true, StablePhaseAnchor: true}) &&
		capability.Callback.Awaited && capability.Callback.Deadline > 0 &&
		capability.Callback.Ordering == CallbackOrderingBeforeFirstAction &&
		capability.Acknowledgement == AcknowledgementAdapterAttested
}

func normalizeHello(hello HostHello) (normalizedHello, error) {
	if !validProtocolRange(hello.Protocol) || !validHostFamily(hello.Host.Family) || !validText(hello.Host.HostVersion) || !validText(hello.Host.AdapterID) || !validText(hello.Host.AdapterVersion) || !validText(hello.Host.RuntimeInstanceRef) {
		return normalizedHello{}, fmt.Errorf("%w: host identity is invalid", ErrInvalidInput)
	}
	if !validEvidence(hello.Evidence) {
		return normalizedHello{}, fmt.Errorf("%w: host evidence is invalid", ErrInvalidInput)
	}
	if len(hello.Requested) == 0 || len(hello.Requested) > maxAdvisorCapabilities {
		return normalizedHello{}, fmt.Errorf("%w: requested capabilities are required", ErrInvalidInput)
	}

	requested := cloneCapabilities(hello.Requested)
	var prior Semantic
	for index, capability := range requested {
		if !validCapability(capability) {
			return normalizedHello{}, fmt.Errorf("%w: requested capability is invalid", ErrInvalidInput)
		}
		if index != 0 && capability.Semantic <= prior {
			return normalizedHello{}, fmt.Errorf("%w: requested capabilities are not canonical", ErrInvalidInput)
		}
		prior = capability.Semantic
	}
	return normalizedHello{
		protocol:  hello.Protocol,
		host:      hello.Host,
		requested: requested,
		evidence:  hello.Evidence,
	}, nil
}

func validProtocolRange(protocol ProtocolRange) bool {
	return protocol.Min != 0 && protocol.Min <= protocol.Max
}

func validText(value string) bool {
	if value == "" || len(value) > maxAdvisorTextBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validEvidence(evidence EvidenceRef) bool {
	return validArtifactKind(evidence.ArtifactKind) && !evidence.ArtifactSHA256.isZero() && validText(evidence.RuntimeProbeReceiptID)
}

func validCapability(capability Capability) bool {
	if !validSemantic(capability.Semantic) || !strictActions(capability.Actions) || !strictInjectionModes(capability.InjectionModes) || !validCallbackOrdering(capability.Callback.Ordering) || !validAcknowledgement(capability.Acknowledgement) {
		return false
	}
	return capability.Callback.Deadline > 0 && capability.Callback.Deadline%time.Millisecond == 0 && capability.Callback.Deadline.Milliseconds() <= math.MaxUint32
}

func strictActions(actions []Action) bool {
	if len(actions) == 0 || len(actions) > maxAdvisorActions {
		return false
	}
	var prior Action
	for index, action := range actions {
		if !validAction(action) || (index != 0 && action <= prior) {
			return false
		}
		prior = action
	}
	return true
}

func strictInjectionModes(modes []InjectionMode) bool {
	if len(modes) == 0 || len(modes) > maxAdvisorInjectionModes {
		return false
	}
	var prior InjectionMode
	for index, mode := range modes {
		if !validInjectionMode(mode) || (index != 0 && mode <= prior) {
			return false
		}
		prior = mode
	}
	return true
}

func validHostFamily(family HostFamily) bool {
	switch family {
	case HostFamilyOMP, HostFamilyClaudeCode, HostFamilyCodex:
		return true
	default:
		return false
	}
}

func validArtifactKind(kind ArtifactKind) bool {
	switch kind {
	case ArtifactKindInstalled, ArtifactKindSource:
		return true
	default:
		return false
	}
}

func validSemantic(semantic Semantic) bool {
	switch semantic {
	case SemanticBeforeAgentStart, SemanticToolResult, SemanticToolAction, SemanticSessionFinalization:
		return true
	default:
		return false
	}
}

func validAction(action Action) bool {
	switch action {
	case ActionEmitAdvice, ActionAdapterAttestation, ActionAllow, ActionBlock, ActionRewrite, ActionEmitContextReference:
		return true
	default:
		return false
	}
}

func validInjectionMode(mode InjectionMode) bool {
	switch mode {
	case InjectionModeHiddenUntrustedMessage, InjectionModeDeveloperContext, InjectionModeAdditionalContext:
		return true
	default:
		return false
	}
}

func validCallbackOrdering(ordering CallbackOrdering) bool {
	switch ordering {
	case CallbackOrderingBeforeFirstAction, CallbackOrderingAfterToolResult, CallbackOrderingOtherProvenOrder:
		return true
	default:
		return false
	}
}

func validAcknowledgement(acknowledgement Acknowledgement) bool {
	switch acknowledgement {
	case AcknowledgementNone, AcknowledgementAdapterAttested, AcknowledgementHostAcknowledged, AcknowledgementConsumptionObserved:
		return true
	default:
		return false
	}
}

func profileSupports(profile AcceptedProfile, hello normalizedHello) bool {
	if hello.protocol.Min < profile.Protocol.Min || hello.protocol.Max > profile.Protocol.Max {
		return false
	}
	if hello.host.Family != profile.HostFamily || hello.host.HostVersion != profile.HostVersion || hello.host.AdapterID != profile.AdapterID || hello.host.AdapterVersion != profile.AdapterVersion {
		return false
	}
	if hello.evidence != profile.Evidence {
		return false
	}
	for _, requested := range hello.requested {
		accepted, found := profileCapability(profile, requested.Semantic)
		if !found || !capabilitySubset(requested, accepted) {
			return false
		}
	}
	return true
}

func profileCapability(profile AcceptedProfile, semantic Semantic) (Capability, bool) {
	for _, capability := range profile.Capabilities {
		if capability.Semantic == semantic {
			return capability, true
		}
	}
	return Capability{}, false
}

func capabilitySubset(requested, accepted Capability) bool {
	if requested.Semantic != accepted.Semantic || !actionsSubset(requested.Actions, accepted.Actions) || !injectionModesSubset(requested.InjectionModes, accepted.InjectionModes) {
		return false
	}
	if requested.Correlation.Session && !accepted.Correlation.Session || requested.Correlation.Turn && !accepted.Correlation.Turn || requested.Correlation.ToolAction && !accepted.Correlation.ToolAction || requested.Correlation.StablePhaseAnchor && !accepted.Correlation.StablePhaseAnchor {
		return false
	}
	return requested.Callback == accepted.Callback && requested.Acknowledgement == accepted.Acknowledgement
}

func actionsSubset(requested, accepted []Action) bool {
	acceptedIndex := 0
	for _, action := range requested {
		for acceptedIndex < len(accepted) && accepted[acceptedIndex] < action {
			acceptedIndex++
		}
		if acceptedIndex == len(accepted) || accepted[acceptedIndex] != action {
			return false
		}
	}
	return true
}

func injectionModesSubset(requested, accepted []InjectionMode) bool {
	acceptedIndex := 0
	for _, mode := range requested {
		for acceptedIndex < len(accepted) && accepted[acceptedIndex] < mode {
			acceptedIndex++
		}
		if acceptedIndex == len(accepted) || accepted[acceptedIndex] != mode {
			return false
		}
	}
	return true
}

func profileCallbackDeadline(profile AcceptedProfile) time.Duration {
	return profile.Capabilities[0].Callback.Deadline
}

func snapshotFor(profile AcceptedProfile, capabilities []Capability) AcceptedCapabilitySnapshot {
	snapshot := AcceptedCapabilitySnapshot{
		snapshotID:         profile.SnapshotID,
		revision:           profile.SnapshotRevision,
		capabilityRevision: profile.CapabilityRevision,
		capabilities:       cloneCapabilities(capabilities),
	}
	snapshot.contractDigest = contractDigest(profile, snapshot.capabilities)
	return snapshot
}

func contractDigest(profile AcceptedProfile, capabilities []Capability) Digest {
	writer := newDigestWriter("engram.host-advisor.contract/v1")
	writer.text(profile.CapabilityRevision)
	writer.text(profile.SnapshotID)
	writer.uint64(profile.SnapshotRevision)
	writer.protocol(profile.Protocol)
	writer.hostProfile(profile)
	writer.evidence(profile.Evidence)
	writer.capabilities(capabilities)
	return writer.sum()
}

func materialDigest(subject AuthenticatedSubject, profile AcceptedProfile, hello normalizedHello) Digest {
	writer := newDigestWriter("engram.host-advisor.material/v1")
	writer.bytes(subject.proof[:])
	writer.digest(contractDigest(profile, hello.requested))
	writer.protocol(hello.protocol)
	writer.host(hello.host)
	writer.evidence(hello.evidence)
	writer.capabilities(hello.requested)
	return writer.sum()
}

type digestWriter struct {
	hash hash.Hash
}

func newDigestWriter(domain string) *digestWriter {
	writer := &digestWriter{hash: sha256.New()}
	writer.text(domain)
	return writer
}

func (w *digestWriter) sum() Digest {
	var digest Digest
	copy(digest[:], w.hash.Sum(nil))
	return digest
}

func (w *digestWriter) bytes(value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = w.hash.Write(length[:])
	_, _ = w.hash.Write(value)
}

func (w *digestWriter) text(value string) {
	w.bytes([]byte(value))
}

func (w *digestWriter) uint32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = w.hash.Write(encoded[:])
}

func (w *digestWriter) uint64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = w.hash.Write(encoded[:])
}

func (w *digestWriter) boolean(value bool) {
	if value {
		w.uint32(1)
		return
	}
	w.uint32(0)
}

func (w *digestWriter) digest(digest Digest) {
	w.bytes(digest[:])
}

func (w *digestWriter) protocol(protocol ProtocolRange) {
	w.uint32(protocol.Min)
	w.uint32(protocol.Max)
}

func (w *digestWriter) host(host HostIdentity) {
	w.uint32(uint32(host.Family))
	w.text(host.HostVersion)
	w.text(host.AdapterID)
	w.text(host.AdapterVersion)
	w.text(host.RuntimeInstanceRef)
}

func (w *digestWriter) hostProfile(profile AcceptedProfile) {
	w.uint32(uint32(profile.HostFamily))
	w.text(profile.HostVersion)
	w.text(profile.AdapterID)
	w.text(profile.AdapterVersion)
}

func (w *digestWriter) evidence(evidence EvidenceRef) {
	w.uint32(uint32(evidence.ArtifactKind))
	w.digest(evidence.ArtifactSHA256)
	w.text(evidence.RuntimeProbeReceiptID)
}

func (w *digestWriter) capabilities(capabilities []Capability) {
	w.uint32(uint32(len(capabilities)))
	for _, capability := range capabilities {
		w.uint32(uint32(capability.Semantic))
		w.uint32(uint32(len(capability.Actions)))
		for _, action := range capability.Actions {
			w.uint32(uint32(action))
		}
		w.uint32(uint32(len(capability.InjectionModes)))
		for _, mode := range capability.InjectionModes {
			w.uint32(uint32(mode))
		}
		w.boolean(capability.Correlation.Session)
		w.boolean(capability.Correlation.Turn)
		w.boolean(capability.Correlation.ToolAction)
		w.boolean(capability.Correlation.StablePhaseAnchor)
		w.boolean(capability.Callback.Awaited)
		w.uint64(uint64(capability.Callback.Deadline))
		w.uint32(uint32(capability.Callback.Ordering))
		w.uint32(uint32(capability.Acknowledgement))
	}
}
