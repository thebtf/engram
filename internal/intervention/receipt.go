package intervention

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/taskmemory"
)

const (
	receiptIntegrityDomain = "engram.intervention-receipt/v1"
	maxReceiptSnapshotRefs = 8
)

// ReceiptOutcome is the closed persisted IEP v2 decision outcome.
type ReceiptOutcome uint8

const (
	ReceiptOutcomeEmit ReceiptOutcome = iota + 1
	ReceiptOutcomeAbstain
)

func validReceiptOutcome(outcome ReceiptOutcome) bool {
	switch outcome {
	case ReceiptOutcomeEmit, ReceiptOutcomeAbstain:
		return true
	default:
		return false
	}
}

// ReceiptDecisionMode is the closed persisted decision eligibility mode.
type ReceiptDecisionMode uint8

const (
	ReceiptDecisionModeEligible ReceiptDecisionMode = iota + 1
	ReceiptDecisionModeCanary
	ReceiptDecisionModeNone
)

func validReceiptDecisionMode(mode ReceiptDecisionMode) bool {
	switch mode {
	case ReceiptDecisionModeEligible, ReceiptDecisionModeCanary, ReceiptDecisionModeNone:
		return true
	default:
		return false
	}
}

// ReceiptAxis is the immutable protected fact projection used for receipt
// replay and auditing. It retains no raw host session, task query, or project
// evidence.
type ReceiptAxis struct {
	channelKey           Digest
	hostFamily           HostFamily
	canonicalProject     string
	actorPrincipal       string
	actorKind            string
	workstation          string
	sessionKey           Digest
	occurrenceKey        Digest
	contentCommitment    Digest
	capabilityCommitment Digest
}

// NewReceiptAxis derives the complete T02 receipt projection from admitted host
// facts and the canonical authority returned by the accepted Preparer.
func NewReceiptAxis(epoch KeyEpoch, binding BindingFacts, authority taskmemory.AuthorizedTaskContext, occurrence BeforeAgentStartOccurrence) (ReceiptAxis, error) {
	identity, err := NewOccurrenceIdentity(binding, authority, occurrence)
	if err != nil || !validCanonicalUUID(identity.canonicalProject) {
		return ReceiptAxis{}, ErrInvalidInput
	}

	channelKey, err := epoch.DeriveChannel(binding)
	if err != nil {
		return ReceiptAxis{}, ErrInvalidInput
	}
	sessionKey, err := epoch.DeriveSessionKey(identity)
	if err != nil {
		return ReceiptAxis{}, ErrInvalidInput
	}
	occurrenceKey, err := epoch.DeriveOccurrence(identity)
	if err != nil {
		return ReceiptAxis{}, ErrInvalidInput
	}
	contentCommitment, err := epoch.DeriveContent(occurrence)
	if err != nil {
		return ReceiptAxis{}, ErrInvalidInput
	}

	axis := ReceiptAxis{
		channelKey:           channelKey,
		hostFamily:           binding.hostFamily,
		canonicalProject:     identity.canonicalProject,
		actorPrincipal:       identity.actorPrincipal,
		actorKind:            identity.actorKind,
		workstation:          identity.workstation,
		sessionKey:           sessionKey,
		occurrenceKey:        occurrenceKey,
		contentCommitment:    contentCommitment,
		capabilityCommitment: binding.capabilityCommitment,
	}
	if !axis.valid() {
		return ReceiptAxis{}, ErrInvalidInput
	}
	return axis, nil
}

// ChannelKey returns the protected server-derived channel commitment.
func (a ReceiptAxis) ChannelKey() Digest {
	return a.channelKey
}

// HostFamily returns the audited closed host family fact.
func (a ReceiptAxis) HostFamily() HostFamily {
	return a.hostFamily
}

// CanonicalProject returns the authoritative canonical project UUID.
func (a ReceiptAxis) CanonicalProject() string {
	return a.canonicalProject
}

// ActorPrincipal returns the authoritative actor principal, or its empty sentinel.
func (a ReceiptAxis) ActorPrincipal() string {
	return a.actorPrincipal
}

// ActorKind returns the authoritative actor kind, or its empty sentinel.
func (a ReceiptAxis) ActorKind() string {
	return a.actorKind
}

// Workstation returns the authoritative workstation reference.
func (a ReceiptAxis) Workstation() string {
	return a.workstation
}

// SessionKey returns the protected host-session commitment.
func (a ReceiptAxis) SessionKey() Digest {
	return a.sessionKey
}

// OccurrenceKey returns the protected occurrence commitment.
func (a ReceiptAxis) OccurrenceKey() Digest {
	return a.occurrenceKey
}

// ContentCommitment returns the protected canonical task-content commitment.
func (a ReceiptAxis) ContentCommitment() Digest {
	return a.contentCommitment
}

// CapabilityCommitment returns the protected accepted capability contract digest.
func (a ReceiptAxis) CapabilityCommitment() Digest {
	return a.capabilityCommitment
}

func (a ReceiptAxis) valid() bool {
	return a.channelKey != (Digest{}) &&
		validReceiptHostFamily(a.hostFamily) &&
		validCanonicalUUID(a.canonicalProject) &&
		validPrincipal(a.actorPrincipal, a.actorKind) &&
		validOpaqueReference(a.workstation) &&
		a.sessionKey != (Digest{}) &&
		a.occurrenceKey != (Digest{}) &&
		a.contentCommitment != (Digest{}) &&
		a.capabilityCommitment != (Digest{})
}

func validReceiptHostFamily(family HostFamily) bool {
	switch family {
	case HostFamilyOMP, HostFamilyClaudeCode, HostFamilyCodex:
		return true
	default:
		return false
	}
}

// ReceiptSelectionRecord is the complete selected-reference projection for a
// future emitted receipt. T02 no-candidate receipts always leave it absent.
type ReceiptSelectionRecord struct {
	MemoryID        int64
	MemoryVersion   int
	PolicyID        [32]byte
	SnapshotID      string
	SnapshotVersion int64
	TextDigest      [32]byte
}

func (r ReceiptSelectionRecord) valid() bool {
	return r.MemoryID > 0 &&
		r.MemoryVersion > 0 &&
		r.PolicyID != ([32]byte{}) &&
		validCanonicalUUID(r.SnapshotID) &&
		r.SnapshotVersion > 0 &&
		r.TextDigest != ([32]byte{})
}

// ReceiptPersistenceRecord is the complete safe immutable storage projection.
// SnapshotRefsJSON is canonical JSON for an array containing at most eight refs.
type ReceiptPersistenceRecord struct {
	ReceiptID            string
	OperationID          string
	KeyEpochCommitment   [32]byte
	IntegrityDigest      [32]byte
	ChannelKey           [32]byte
	HostFamily           HostFamily
	CanonicalProject     string
	ActorPrincipal       string
	ActorKind            string
	Workstation          string
	SessionKey           [32]byte
	OccurrenceKey        [32]byte
	ContentCommitment    [32]byte
	CapabilityCommitment [32]byte
	Outcome              ReceiptOutcome
	ClosedReason         *AbstentionReason
	DecisionMode         ReceiptDecisionMode
	EvaluatedCount       int
	EligibleCount        int
	SnapshotRefsJSON     []byte
	Selection            *ReceiptSelectionRecord
	CreatedAt            time.Time
	ExpiresAt            time.Time
}

type receiptTrust uint8

const (
	receiptTrustUnverified receiptTrust = iota
	receiptTrustVerified
	receiptTrustAuthorable
)

// Receipt is an immutable IEP v2 persistence value with an internal trust state.
// Only a local T02 signing constructor can produce an authorable receipt.
type Receipt struct {
	record ReceiptPersistenceRecord
	axis   ReceiptAxis
	trust  receiptTrust
}

// ReceiptStore is the sole first-writer-wins persistence port for IEP v2
// receipts. Commit reports whether this call inserted the immutable row.
type ReceiptStore interface {
	Lookup(context.Context, ReceiptAxis) (Receipt, bool, error)
	Commit(context.Context, Receipt) (Receipt, bool, error)
}

// NewNoCandidatesReceipt creates the one T02 authorable receipt state. Its
// expiry is always the already-bounded callback context deadline.
func NewNoCandidatesReceipt(ctx context.Context, epoch KeyEpoch, axis ReceiptAxis, receiptID, operationID string, createdAt time.Time) (Receipt, error) {
	if ctx == nil || !epoch.valid() || !axis.valid() {
		return Receipt{}, ErrInvalidInput
	}
	expiresAt, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return Receipt{}, ErrInvalidInput
	}
	reason := AbstentionNoCandidates
	record := ReceiptPersistenceRecord{
		ReceiptID:            receiptID,
		OperationID:          operationID,
		KeyEpochCommitment:   epoch.EpochCommitment(),
		ChannelKey:           [32]byte(axis.channelKey),
		HostFamily:           axis.hostFamily,
		CanonicalProject:     axis.canonicalProject,
		ActorPrincipal:       axis.actorPrincipal,
		ActorKind:            axis.actorKind,
		Workstation:          axis.workstation,
		SessionKey:           [32]byte(axis.sessionKey),
		OccurrenceKey:        [32]byte(axis.occurrenceKey),
		ContentCommitment:    [32]byte(axis.contentCommitment),
		CapabilityCommitment: [32]byte(axis.capabilityCommitment),
		Outcome:              ReceiptOutcomeAbstain,
		ClosedReason:         &reason,
		DecisionMode:         ReceiptDecisionModeNone,
		SnapshotRefsJSON:     []byte("[]"),
		CreatedAt:            normalizeReceiptTimestamp(createdAt),
		ExpiresAt:            normalizeReceiptTimestamp(expiresAt),
	}
	return signReceipt(epoch, record)
}

// NewPolicyAbstentionReceipt creates one T03-only receipt-backed abstention
// for a nonempty prepared candidate set. It never selects a candidate.
func NewPolicyAbstentionReceipt(ctx context.Context, epoch KeyEpoch, axis ReceiptAxis, receiptID, operationID string, createdAt time.Time, reason AbstentionReason, evaluatedCount int) (Receipt, error) {
	if ctx == nil || !epoch.valid() || !axis.valid() || !validT03PolicyAbstentionReason(reason) ||
		evaluatedCount < 1 || evaluatedCount > maxReceiptSnapshotRefs {
		return Receipt{}, ErrInvalidInput
	}
	expiresAt, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return Receipt{}, ErrInvalidInput
	}
	record := ReceiptPersistenceRecord{
		ReceiptID:            receiptID,
		OperationID:          operationID,
		KeyEpochCommitment:   epoch.EpochCommitment(),
		ChannelKey:           [32]byte(axis.channelKey),
		HostFamily:           axis.hostFamily,
		CanonicalProject:     axis.canonicalProject,
		ActorPrincipal:       axis.actorPrincipal,
		ActorKind:            axis.actorKind,
		Workstation:          axis.workstation,
		SessionKey:           [32]byte(axis.sessionKey),
		OccurrenceKey:        [32]byte(axis.occurrenceKey),
		ContentCommitment:    [32]byte(axis.contentCommitment),
		CapabilityCommitment: [32]byte(axis.capabilityCommitment),
		Outcome:              ReceiptOutcomeAbstain,
		ClosedReason:         &reason,
		DecisionMode:         ReceiptDecisionModeNone,
		EvaluatedCount:       evaluatedCount,
		EligibleCount:        0,
		SnapshotRefsJSON:     []byte("[]"),
		CreatedAt:            normalizeReceiptTimestamp(createdAt),
		ExpiresAt:            normalizeReceiptTimestamp(expiresAt),
	}
	return signReceipt(epoch, record)
}

// RestoreUnverifiedReceipt copies and structurally validates one persistence
// projection loaded from storage. Its facts remain untrusted until
// VerifyTrusted authenticates them against the current KeyEpoch.
func RestoreUnverifiedReceipt(record ReceiptPersistenceRecord) (Receipt, error) {
	record = cloneReceiptPersistenceRecord(record)
	record.CreatedAt = record.CreatedAt.UTC()
	record.ExpiresAt = record.ExpiresAt.UTC()
	if !validReceiptPersistenceRecord(record, true) {
		return Receipt{}, ErrInvalidInput
	}
	axis := receiptAxisFromRecord(record)
	if !axis.valid() {
		return Receipt{}, ErrInvalidInput
	}
	return Receipt{record: record, axis: axis, trust: receiptTrustUnverified}, nil
}

// Axis returns the immutable receipt replay and audit projection.
func (r Receipt) Axis() ReceiptAxis {
	return r.axis
}

// PersistenceRecord returns an independently owned persistence projection.
func (r Receipt) PersistenceRecord() ReceiptPersistenceRecord {
	return cloneReceiptPersistenceRecord(r.record)
}

// CanCommit reports whether r was created by a T02 signing constructor and is
// therefore legal immutable-store input. Verified persistence replays are
// deliberately never authorable.
func (r Receipt) CanCommit() bool {
	return r.trust == receiptTrustAuthorable && r.valid()
}

// Identity returns the receipt reference carried by final advisor decisions
// only after internal receipt trust has been established.
func (r Receipt) Identity() ReceiptIdentity {
	if !r.valid() {
		return ReceiptIdentity{}
	}
	identity, err := NewReceiptIdentity(r.record.ReceiptID, r.record.IntegrityDigest)
	if err != nil {
		return ReceiptIdentity{}
	}
	return identity
}

// Verify reports whether the current epoch authenticates r without changing
// its trust state. Replay consumers must use VerifyTrusted to consume receipt
// facts or identity.
func (r Receipt) Verify(epoch KeyEpoch) bool {
	_, ok := r.VerifyTrusted(epoch)
	return ok
}

// VerifyTrusted authenticates r against the current epoch and returns a
// trusted, non-authorable replay copy. It never turns a restored row into a
// new commit candidate.
func (r Receipt) VerifyTrusted(epoch KeyEpoch) (Receipt, bool) {
	if !r.structurallyValid() || !epoch.valid() || !hmac.Equal(r.record.KeyEpochCommitment[:], epoch.epoch[:]) {
		return Receipt{}, false
	}
	expected, err := receiptIntegrity(epoch, r.record)
	if err != nil || !hmac.Equal(r.record.IntegrityDigest[:], expected[:]) {
		return Receipt{}, false
	}
	r.trust = receiptTrustVerified
	return r, true
}

func (r Receipt) valid() bool {
	switch r.trust {
	case receiptTrustVerified, receiptTrustAuthorable:
		return r.structurallyValid()
	default:
		return false
	}
}

func (r Receipt) structurallyValid() bool {
	if !validReceiptPersistenceRecord(r.record, true) || !r.axis.valid() {
		return false
	}
	return r.axis == receiptAxisFromRecord(r.record)
}

func signReceipt(epoch KeyEpoch, record ReceiptPersistenceRecord) (Receipt, error) {
	if !epoch.valid() || !validReceiptPersistenceRecord(record, false) || !hmac.Equal(record.KeyEpochCommitment[:], epoch.epoch[:]) {
		return Receipt{}, ErrInvalidInput
	}
	integrity, err := receiptIntegrity(epoch, record)
	if err != nil {
		return Receipt{}, ErrInvalidInput
	}
	record.IntegrityDigest = [32]byte(integrity)
	receipt, err := RestoreUnverifiedReceipt(record)
	if err != nil {
		return Receipt{}, ErrInvalidInput
	}
	receipt.trust = receiptTrustAuthorable
	return receipt, nil
}

func receiptIntegrity(epoch KeyEpoch, record ReceiptPersistenceRecord) (Digest, error) {
	if !epoch.valid() || !validReceiptPersistenceRecord(record, false) {
		return Digest{}, ErrInvalidInput
	}
	encoder := newHMACEncoder(epoch.receipt)
	encoder.text(receiptIntegrityDomain)
	encoder.text(record.ReceiptID)
	encoder.text(record.OperationID)
	encoder.digest(Digest(record.KeyEpochCommitment))
	encoder.digest(Digest(record.ChannelKey))
	encoder.uint32(uint32(record.HostFamily))
	encoder.text(record.CanonicalProject)
	encoder.text(record.ActorPrincipal)
	encoder.text(record.ActorKind)
	encoder.text(record.Workstation)
	encoder.digest(Digest(record.SessionKey))
	encoder.digest(Digest(record.OccurrenceKey))
	encoder.digest(Digest(record.ContentCommitment))
	encoder.digest(Digest(record.CapabilityCommitment))
	encoder.uint32(uint32(record.Outcome))
	encoder.boolean(record.ClosedReason != nil)
	if record.ClosedReason != nil {
		encoder.uint32(uint32(*record.ClosedReason))
	}
	encoder.uint32(uint32(record.DecisionMode))
	encoder.uint32(uint32(record.EvaluatedCount))
	encoder.uint32(uint32(record.EligibleCount))
	encoder.bytes(record.SnapshotRefsJSON)
	encoder.boolean(record.Selection != nil)
	if record.Selection != nil {
		encoder.int64(record.Selection.MemoryID)
		encoder.int64(int64(record.Selection.MemoryVersion))
		encoder.digest(Digest(record.Selection.PolicyID))
		encoder.text(record.Selection.SnapshotID)
		encoder.int64(record.Selection.SnapshotVersion)
		encoder.digest(Digest(record.Selection.TextDigest))
	}
	encoder.int64(record.CreatedAt.UTC().UnixMicro())
	encoder.int64(record.ExpiresAt.UTC().UnixMicro())
	return encoder.sum(), nil
}

func validReceiptPersistenceRecord(record ReceiptPersistenceRecord, requireIntegrity bool) bool {
	if !validCanonicalUUID(record.ReceiptID) ||
		!validCanonicalUUID(record.OperationID) ||
		record.KeyEpochCommitment == ([32]byte{}) ||
		(record.IntegrityDigest == ([32]byte{}) && requireIntegrity) ||
		!receiptAxisFromRecord(record).valid() ||
		!validReceiptOutcome(record.Outcome) ||
		!validReceiptDecisionMode(record.DecisionMode) ||
		record.EvaluatedCount < 0 ||
		record.EligibleCount < 0 ||
		record.EligibleCount > record.EvaluatedCount ||
		record.EvaluatedCount > maxReceiptSnapshotRefs ||
		!validReceiptSnapshotRefs(record.SnapshotRefsJSON) ||
		!validReceiptTimestamp(record.CreatedAt) ||
		!validReceiptTimestamp(record.ExpiresAt) ||
		!record.ExpiresAt.After(record.CreatedAt) {
		return false
	}

	if record.Selection != nil && !record.Selection.valid() {
		return false
	}
	switch record.Outcome {
	case ReceiptOutcomeEmit:
		return record.ClosedReason == nil &&
			record.Selection != nil &&
			record.EvaluatedCount > 0 &&
			((record.DecisionMode == ReceiptDecisionModeEligible && record.EligibleCount > 0) || record.DecisionMode == ReceiptDecisionModeCanary)
	case ReceiptOutcomeAbstain:
		return validReceiptAbstentionRecord(record)
	default:
		return false
	}
}

func validReceiptAbstentionRecord(record ReceiptPersistenceRecord) bool {
	if record.ClosedReason == nil || !validAbstentionReason(*record.ClosedReason) ||
		record.Selection != nil || record.DecisionMode != ReceiptDecisionModeNone ||
		!bytes.Equal(record.SnapshotRefsJSON, []byte("[]")) || record.EligibleCount != 0 {
		return false
	}
	switch *record.ClosedReason {
	case AbstentionNoCandidates:
		return record.EvaluatedCount == 0
	case AbstentionPolicyObserving, AbstentionEvidenceInsufficient, AbstentionEvidenceState:
		return record.EvaluatedCount >= 1 && record.EvaluatedCount <= maxReceiptSnapshotRefs
	default:
		return false
	}
}

func validT03PolicyAbstentionReason(reason AbstentionReason) bool {
	switch reason {
	case AbstentionPolicyObserving, AbstentionEvidenceInsufficient, AbstentionEvidenceState:
		return true
	default:
		return false
	}
}

func receiptAxisFromRecord(record ReceiptPersistenceRecord) ReceiptAxis {
	return ReceiptAxis{
		channelKey:           Digest(record.ChannelKey),
		hostFamily:           record.HostFamily,
		canonicalProject:     record.CanonicalProject,
		actorPrincipal:       record.ActorPrincipal,
		actorKind:            record.ActorKind,
		workstation:          record.Workstation,
		sessionKey:           Digest(record.SessionKey),
		occurrenceKey:        Digest(record.OccurrenceKey),
		contentCommitment:    Digest(record.ContentCommitment),
		capabilityCommitment: Digest(record.CapabilityCommitment),
	}
}

func cloneReceiptPersistenceRecord(record ReceiptPersistenceRecord) ReceiptPersistenceRecord {
	record.SnapshotRefsJSON = append([]byte(nil), record.SnapshotRefsJSON...)
	if record.ClosedReason != nil {
		reason := *record.ClosedReason
		record.ClosedReason = &reason
	}
	if record.Selection != nil {
		selection := *record.Selection
		record.Selection = &selection
	}
	return record
}

func validCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func normalizeReceiptTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func validReceiptTimestamp(value time.Time) bool {
	return !value.IsZero() && value.Nanosecond()%int(time.Microsecond) == 0
}

func validReceiptSnapshotRefs(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	var refs []any
	if err := json.Unmarshal(value, &refs); err != nil || refs == nil || len(refs) > maxReceiptSnapshotRefs {
		return false
	}
	canonical, err := json.Marshal(refs)
	return err == nil && bytes.Equal(value, canonical)
}
