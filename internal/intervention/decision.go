package intervention

import "time"

// ReceiptIdentity is the immutable IEP v2 receipt reference used by final
// advisor outcomes. It is a value projection, not receipt behavior.
type ReceiptIdentity struct {
	id        string
	integrity Digest
}

// NewReceiptIdentity validates one opaque receipt reference and full integrity digest.
func NewReceiptIdentity(id string, integrity [32]byte) (ReceiptIdentity, error) {
	digest, validDigest := digestFromArray(integrity)
	if !validOpaqueReference(id) || !validDigest {
		return ReceiptIdentity{}, ErrInvalidInput
	}
	return ReceiptIdentity{id: id, integrity: digest}, nil
}

// ID returns the opaque immutable receipt reference.
func (r ReceiptIdentity) ID() string {
	return r.id
}

// IntegrityDigest returns the full protected receipt digest.
func (r ReceiptIdentity) IntegrityDigest() Digest {
	return r.integrity
}

func (r ReceiptIdentity) valid() bool {
	return validOpaqueReference(r.id) && r.integrity != (Digest{})
}

// CandidateTier identifies the retrieval leg of a selected knowledge reference.
type CandidateTier uint8

const (
	CandidateTierExact CandidateTier = iota + 1
	CandidateTierFTS
	CandidateTierVector
)

func validCandidateTier(tier CandidateTier) bool {
	switch tier {
	case CandidateTierExact, CandidateTierFTS, CandidateTierVector:
		return true
	default:
		return false
	}
}

// KnowledgeReference is the one exact-version source reference permitted in a packet.
type KnowledgeReference struct {
	memoryID      int64
	memoryVersion uint32
	tier          CandidateTier
	textDigest    Digest
}

// NewKnowledgeReference validates one exact-version protected knowledge reference.
func NewKnowledgeReference(memoryID int64, memoryVersion uint32, tier CandidateTier, textDigest [32]byte) (KnowledgeReference, error) {
	digest, validDigest := digestFromArray(textDigest)
	if memoryID <= 0 || memoryVersion == 0 || !validCandidateTier(tier) || !validDigest {
		return KnowledgeReference{}, ErrInvalidInput
	}
	return KnowledgeReference{memoryID: memoryID, memoryVersion: memoryVersion, tier: tier, textDigest: digest}, nil
}

// MemoryID returns the exact source memory ID.
func (r KnowledgeReference) MemoryID() int64 {
	return r.memoryID
}

// MemoryVersion returns the exact source memory version.
func (r KnowledgeReference) MemoryVersion() uint32 {
	return r.memoryVersion
}

// Tier returns the closed retrieval-source tier.
func (r KnowledgeReference) Tier() CandidateTier {
	return r.tier
}

// TextDigest returns the protected selected text digest.
func (r KnowledgeReference) TextDigest() Digest {
	return r.textDigest
}

func (r KnowledgeReference) valid() bool {
	return r.memoryID > 0 && r.memoryVersion > 0 && validCandidateTier(r.tier) && r.textDigest != (Digest{})
}

// ContextInjectionMode identifies the only T01 packet rendering surface.
type ContextInjectionMode uint8

const (
	ContextInjectionHiddenUntrustedMessage ContextInjectionMode = iota + 1
)

// Presentation is an immutable bounded untrusted-reference rendering.
type Presentation struct {
	text string
}

// NewUntrustedReferencePresentation constructs the only legal packet presentation.
func NewUntrustedReferencePresentation(text string) (Presentation, error) {
	if !validText(text, MaxPresentationBytes) {
		return Presentation{}, ErrInvalidInput
	}
	return Presentation{text: text}, nil
}

// InjectionMode returns the only H03 packet presentation mode.
func (p Presentation) InjectionMode() ContextInjectionMode {
	return ContextInjectionHiddenUntrustedMessage
}

// Text returns the immutable bounded presentation content.
func (p Presentation) Text() string {
	return p.text
}

func (p Presentation) valid() bool {
	return validText(p.text, MaxPresentationBytes)
}

// Packet is the complete one-reference H03 emitted packet value.
type Packet struct {
	receipt      ReceiptIdentity
	expiresAt    time.Time
	knowledge    KnowledgeReference
	presentation Presentation
}

// NewPacket constructs an exact receipt-bound one-reference packet.
func NewPacket(receipt ReceiptIdentity, expiresAt time.Time, knowledge KnowledgeReference, presentation Presentation) (Packet, error) {
	expiresAt = expiresAt.UTC()
	if !receipt.valid() || expiresAt.IsZero() || !knowledge.valid() || !presentation.valid() {
		return Packet{}, ErrInvalidInput
	}
	return Packet{
		receipt:      receipt,
		expiresAt:    expiresAt,
		knowledge:    knowledge,
		presentation: presentation,
	}, nil
}

// Receipt returns the packet's immutable receipt identity.
func (p Packet) Receipt() ReceiptIdentity {
	return p.receipt
}

// ExpiresAt returns the UTC packet expiry.
func (p Packet) ExpiresAt() time.Time {
	return p.expiresAt
}

// Knowledge returns the sole exact-version knowledge reference.
func (p Packet) Knowledge() KnowledgeReference {
	return p.knowledge
}

// Presentation returns the sole untrusted-reference presentation.
func (p Packet) Presentation() Presentation {
	return p.presentation
}

func (p Packet) valid() bool {
	return p.receipt.valid() && !p.expiresAt.IsZero() && p.knowledge.valid() && p.presentation.valid()
}

// AbstentionReason is the closed final non-delivery reason set.
type AbstentionReason uint8

const (
	AbstentionNoCandidates AbstentionReason = iota + 1
	AbstentionPolicyObserving
	AbstentionPolicyShadow
	AbstentionPolicyCanaryBudget
	AbstentionEvidenceInsufficient
	AbstentionHarmBound
	AbstentionPolicySuppressed
	AbstentionTaskFit
	AbstentionActionability
	AbstentionEvidenceState
	AbstentionAlreadyVisible
	AbstentionContextBudget
	AbstentionAmbiguousConflict
)

func validAbstentionReason(reason AbstentionReason) bool {
	return reason >= AbstentionNoCandidates && reason <= AbstentionAmbiguousConflict
}

// UnavailableCode is the closed final unavailable reason set.
type UnavailableCode uint8

const (
	UnavailableBinding UnavailableCode = iota + 1
	UnavailableCapability
	UnavailableFacts
	UnavailablePredecessor
	UnavailableDeadline
	UnavailableReplayConflict
	UnavailableDependency
	UnavailableReceipt
)

func validUnavailableCode(code UnavailableCode) bool {
	return code >= UnavailableBinding && code <= UnavailableReceipt
}

// Unavailable is a final non-receipt outcome correlation.
type Unavailable struct {
	correlationID string
	expiresAt     time.Time
	code          UnavailableCode
}

// CorrelationID returns the non-authoritative unavailable correlation reference.
func (u Unavailable) CorrelationID() string {
	return u.correlationID
}

// ExpiresAt returns the UTC correlation expiry.
func (u Unavailable) ExpiresAt() time.Time {
	return u.expiresAt
}

// Code returns the closed unavailable reason.
func (u Unavailable) Code() UnavailableCode {
	return u.code
}

func (u Unavailable) valid() bool {
	return validOpaqueReference(u.correlationID) && !u.expiresAt.IsZero() && validUnavailableCode(u.code)
}

// DecisionKind is the closed H03 Advise result discriminator.
type DecisionKind uint8

const (
	DecisionEmit DecisionKind = iota + 1
	DecisionAbstain
	DecisionDeliveryAmbiguous
	DecisionUnavailable
)

type decisionVariant interface {
	decisionKind() DecisionKind
}

type emitDecision struct {
	receipt ReceiptIdentity
	packet  Packet
}

func (emitDecision) decisionKind() DecisionKind { return DecisionEmit }

type abstainDecision struct {
	receipt ReceiptIdentity
	reason  AbstentionReason
}

func (abstainDecision) decisionKind() DecisionKind { return DecisionAbstain }

type deliveryAmbiguousDecision struct {
	receipt ReceiptIdentity
}

func (deliveryAmbiguousDecision) decisionKind() DecisionKind { return DecisionDeliveryAmbiguous }

type unavailableDecision struct {
	unavailable Unavailable
}

func (unavailableDecision) decisionKind() DecisionKind { return DecisionUnavailable }

// Decision is a sealed tagged final advisor outcome. Its private variant makes
// mixed packet/reason/receipt states impossible to construct outside this package.
type Decision struct {
	variant decisionVariant
}

// NewEmitDecision constructs the sole packet-bearing final result.
func NewEmitDecision(receipt ReceiptIdentity, packet Packet) (Decision, error) {
	if !receipt.valid() || !packet.valid() || packet.receipt != receipt {
		return Decision{}, ErrInvalidInput
	}
	return Decision{variant: emitDecision{receipt: receipt, packet: packet}}, nil
}

// NewAbstainDecision constructs one receipt-bound abstention.
func NewAbstainDecision(receipt ReceiptIdentity, reason AbstentionReason) (Decision, error) {
	if !receipt.valid() || !validAbstentionReason(reason) {
		return Decision{}, ErrInvalidInput
	}
	return Decision{variant: abstainDecision{receipt: receipt, reason: reason}}, nil
}

// NewDeliveryAmbiguousDecision constructs the no-packet replay-safe result.
func NewDeliveryAmbiguousDecision(receipt ReceiptIdentity) (Decision, error) {
	if !receipt.valid() {
		return Decision{}, ErrInvalidInput
	}
	return Decision{variant: deliveryAmbiguousDecision{receipt: receipt}}, nil
}

// NewUnavailableDecision constructs the sole receipt-free final result.
func NewUnavailableDecision(correlationID string, expiresAt time.Time, code UnavailableCode) (Decision, error) {
	unavailable := Unavailable{correlationID: correlationID, expiresAt: expiresAt.UTC(), code: code}
	if !unavailable.valid() {
		return Decision{}, ErrInvalidInput
	}
	return Decision{variant: unavailableDecision{unavailable: unavailable}}, nil
}

// Kind returns the closed final result discriminator, or zero for an invalid value.
func (d Decision) Kind() DecisionKind {
	if d.variant == nil {
		return 0
	}
	return d.variant.decisionKind()
}

// Emit returns the receipt and packet only for DecisionEmit.
func (d Decision) Emit() (ReceiptIdentity, Packet, bool) {
	variant, ok := d.variant.(emitDecision)
	if !ok {
		return ReceiptIdentity{}, Packet{}, false
	}
	return variant.receipt, variant.packet, true
}

// Abstain returns the receipt and reason only for DecisionAbstain.
func (d Decision) Abstain() (ReceiptIdentity, AbstentionReason, bool) {
	variant, ok := d.variant.(abstainDecision)
	if !ok {
		return ReceiptIdentity{}, 0, false
	}
	return variant.receipt, variant.reason, true
}

// DeliveryAmbiguous returns the receipt only for DecisionDeliveryAmbiguous.
func (d Decision) DeliveryAmbiguous() (ReceiptIdentity, bool) {
	variant, ok := d.variant.(deliveryAmbiguousDecision)
	if !ok {
		return ReceiptIdentity{}, false
	}
	return variant.receipt, true
}

// Unavailable returns the correlation outcome only for DecisionUnavailable.
func (d Decision) Unavailable() (Unavailable, bool) {
	variant, ok := d.variant.(unavailableDecision)
	if !ok {
		return Unavailable{}, false
	}
	return variant.unavailable, true
}

// Valid reports whether the decision has exactly one valid final variant.
func (d Decision) Valid() bool {
	switch variant := d.variant.(type) {
	case emitDecision:
		return variant.receipt.valid() && variant.packet.valid() && variant.packet.receipt == variant.receipt
	case abstainDecision:
		return variant.receipt.valid() && validAbstentionReason(variant.reason)
	case deliveryAmbiguousDecision:
		return variant.receipt.valid()
	case unavailableDecision:
		return variant.unavailable.valid()
	default:
		return false
	}
}

// ObservationState is the closed Observe acknowledgement discriminator.
type ObservationState uint8

const (
	ObservationAccepted ObservationState = iota + 1
	ObservationDuplicate
	ObservationRejected
	ObservationUnavailable
)

// ObservationReason is the closed final acknowledgement reason set.
type ObservationReason uint8

const (
	ObservationReasonAcceptedAttestation ObservationReason = iota + 1
	ObservationReasonAcceptedSemanticGap
	ObservationReasonDuplicate
	ObservationReasonInvalidTarget
	ObservationReasonReceiptUnavailable
	ObservationReasonDependencyUnavailable
)

func validObservationReason(reason ObservationReason) bool {
	return reason >= ObservationReasonAcceptedAttestation && reason <= ObservationReasonDependencyUnavailable
}

type observationAckVariant interface {
	state() ObservationState
	reason() ObservationReason
	observationID() (string, bool)
}

type acceptedObservationAck struct {
	id          string
	reasonValue ObservationReason
}

func (acceptedObservationAck) state() ObservationState         { return ObservationAccepted }
func (a acceptedObservationAck) reason() ObservationReason     { return a.reasonValue }
func (a acceptedObservationAck) observationID() (string, bool) { return a.id, true }

type duplicateObservationAck struct {
	id string
}

func (duplicateObservationAck) state() ObservationState         { return ObservationDuplicate }
func (duplicateObservationAck) reason() ObservationReason       { return ObservationReasonDuplicate }
func (a duplicateObservationAck) observationID() (string, bool) { return a.id, true }

type rejectedObservationAck struct{}

func (rejectedObservationAck) state() ObservationState       { return ObservationRejected }
func (rejectedObservationAck) reason() ObservationReason     { return ObservationReasonInvalidTarget }
func (rejectedObservationAck) observationID() (string, bool) { return "", false }

type unavailableObservationAck struct {
	reasonValue ObservationReason
}

func (unavailableObservationAck) state() ObservationState       { return ObservationUnavailable }
func (a unavailableObservationAck) reason() ObservationReason   { return a.reasonValue }
func (unavailableObservationAck) observationID() (string, bool) { return "", false }

// ObservationAck is a sealed tagged final Observe acknowledgement. An ID is
// structurally available only for accepted and duplicate states.
type ObservationAck struct {
	variant observationAckVariant
}

// NewAcceptedObservationAck constructs a receipt-free accepted acknowledgement.
func NewAcceptedObservationAck(observationID string, reason ObservationReason) (ObservationAck, error) {
	if !validOpaqueReference(observationID) || (reason != ObservationReasonAcceptedAttestation && reason != ObservationReasonAcceptedSemanticGap) {
		return ObservationAck{}, ErrInvalidInput
	}
	return ObservationAck{variant: acceptedObservationAck{id: observationID, reasonValue: reason}}, nil
}

// NewDuplicateObservationAck constructs a duplicate acknowledgement with its immutable identity.
func NewDuplicateObservationAck(observationID string) (ObservationAck, error) {
	if !validOpaqueReference(observationID) {
		return ObservationAck{}, ErrInvalidInput
	}
	return ObservationAck{variant: duplicateObservationAck{id: observationID}}, nil
}

// NewRejectedObservationAck constructs the only invalid-target acknowledgement.
func NewRejectedObservationAck() ObservationAck {
	return ObservationAck{variant: rejectedObservationAck{}}
}

// NewUnavailableObservationAck constructs a receipt or dependency unavailable acknowledgement.
func NewUnavailableObservationAck(reason ObservationReason) (ObservationAck, error) {
	if reason != ObservationReasonReceiptUnavailable && reason != ObservationReasonDependencyUnavailable {
		return ObservationAck{}, ErrInvalidInput
	}
	return ObservationAck{variant: unavailableObservationAck{reasonValue: reason}}, nil
}

// State returns the closed acknowledgement state, or zero for an invalid value.
func (a ObservationAck) State() ObservationState {
	if a.variant == nil {
		return 0
	}
	return a.variant.state()
}

// ObservationID returns an immutable ID only for accepted or duplicate states.
func (a ObservationAck) ObservationID() (string, bool) {
	if a.variant == nil {
		return "", false
	}
	return a.variant.observationID()
}

// Reason returns the closed acknowledgement reason, or zero for an invalid value.
func (a ObservationAck) Reason() ObservationReason {
	if a.variant == nil {
		return 0
	}
	return a.variant.reason()
}

// Valid reports whether the acknowledgement has one allowed state/reason/ID shape.
func (a ObservationAck) Valid() bool {
	if a.variant == nil || !validObservationReason(a.variant.reason()) {
		return false
	}
	id, hasID := a.variant.observationID()
	switch a.variant.state() {
	case ObservationAccepted:
		return hasID && validOpaqueReference(id) && (a.variant.reason() == ObservationReasonAcceptedAttestation || a.variant.reason() == ObservationReasonAcceptedSemanticGap)
	case ObservationDuplicate:
		return hasID && validOpaqueReference(id) && a.variant.reason() == ObservationReasonDuplicate
	case ObservationRejected:
		return !hasID && a.variant.reason() == ObservationReasonInvalidTarget
	case ObservationUnavailable:
		return !hasID && (a.variant.reason() == ObservationReasonReceiptUnavailable || a.variant.reason() == ObservationReasonDependencyUnavailable)
	default:
		return false
	}
}
