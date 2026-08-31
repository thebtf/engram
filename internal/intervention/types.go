// Package intervention owns pure, immutable HAP-03 advisor-domain values.
package intervention

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/hostadvisor"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
)

const (
	// MaxOpaqueReferenceBytes bounds server-facing opaque host and receipt references.
	MaxOpaqueReferenceBytes = 128
	// MaxTypedFacts bounds the distinct phase facts on one occurrence.
	MaxTypedFacts = 16
	// MaxTypedFactBytes bounds one typed fact value.
	MaxTypedFactBytes = 256
	// MaxTypedFactsBytes bounds all typed fact values together.
	MaxTypedFactsBytes = 2048
	// MaxWireBytes is the HAP-03 private unary payload cap enforced at transport ingress.
	MaxWireBytes = 16 * 1024
	// MaxPresentationBytes bounds an emitted untrusted-reference presentation.
	MaxPresentationBytes = 256
)

var ErrInvalidInput = errors.New("intervention input is invalid")

// Digest is a fixed-width protected commitment.
type Digest [32]byte

// Bytes returns a copy suitable for a private wire projection.
func (d Digest) Bytes() []byte {
	result := make([]byte, len(d))
	copy(result, d[:])
	return result
}

func digestFromArray(value [32]byte) (Digest, bool) {
	digest := Digest(value)
	return digest, digest != (Digest{})
}

// FactKind is the closed set of admitted BEFORE_AGENT_START fact kinds.
type FactKind uint8

const (
	FactKindKeyword FactKind = iota + 1
	FactKindPath
	FactKindTool
)

func validFactKind(kind FactKind) bool {
	switch kind {
	case FactKindKeyword, FactKindPath, FactKindTool:
		return true
	default:
		return false
	}
}

// TypedFact is a validated immutable phase fact.
type TypedFact struct {
	kind  FactKind
	value string
}

// NewTypedFact validates one bounded phase fact.
func NewTypedFact(kind FactKind, value string) (TypedFact, error) {
	if !validFactKind(kind) || !validText(value, MaxTypedFactBytes) {
		return TypedFact{}, ErrInvalidInput
	}
	if kind == FactKindPath && !validRelativePath(value) {
		return TypedFact{}, ErrInvalidInput
	}
	return TypedFact{kind: kind, value: value}, nil
}

// Kind returns the closed fact kind.
func (f TypedFact) Kind() FactKind {
	return f.kind
}

// Value returns the immutable canonical fact value.
func (f TypedFact) Value() string {
	return f.value
}

func (f TypedFact) valid() bool {
	if !validFactKind(f.kind) || !validText(f.value, MaxTypedFactBytes) {
		return false
	}
	return f.kind != FactKindPath || validRelativePath(f.value)
}

// BeforeAgentStartFacts holds the only H03 Advise phase facts.
type BeforeAgentStartFacts struct {
	task  taskmemory.TaskFacts
	facts []TypedFact
}

// NewBeforeAgentStartFacts validates the accepted task-query form and sorts
// distinct typed facts into canonical order.
func NewBeforeAgentStartFacts(taskQuery string, facts []TypedFact) (BeforeAgentStartFacts, error) {
	task, err := taskmemory.NewTaskFacts(taskQuery)
	if err != nil || !validText(task.Query(), MaxWireBytes) || len(facts) > MaxTypedFacts {
		return BeforeAgentStartFacts{}, ErrInvalidInput
	}

	canonical := append([]TypedFact(nil), facts...)
	for _, fact := range canonical {
		if !fact.valid() {
			return BeforeAgentStartFacts{}, ErrInvalidInput
		}
	}
	sort.Slice(canonical, func(left, right int) bool {
		if canonical[left].kind != canonical[right].kind {
			return canonical[left].kind < canonical[right].kind
		}
		return canonical[left].value < canonical[right].value
	})

	totalBytes := 0
	for index, fact := range canonical {
		totalBytes += len(fact.value)
		if totalBytes > MaxTypedFactsBytes || (index > 0 && fact == canonical[index-1]) {
			return BeforeAgentStartFacts{}, ErrInvalidInput
		}
	}

	return BeforeAgentStartFacts{task: task, facts: canonical}, nil
}

// TaskFacts returns the accepted immutable task facts.
func (f BeforeAgentStartFacts) TaskFacts() taskmemory.TaskFacts {
	return f.task
}

// TaskQuery returns the accepted canonical query.
func (f BeforeAgentStartFacts) TaskQuery() string {
	return f.task.Query()
}

// Facts returns an independently owned canonical fact slice.
func (f BeforeAgentStartFacts) Facts() []TypedFact {
	return append([]TypedFact(nil), f.facts...)
}

func (f BeforeAgentStartFacts) valid() bool {
	if f.task.Query() == "" || !validText(f.task.Query(), MaxWireBytes) || len(f.facts) > MaxTypedFacts {
		return false
	}
	totalBytes := 0
	for index, fact := range f.facts {
		if !fact.valid() {
			return false
		}
		if index > 0 {
			prior := f.facts[index-1]
			if fact.kind < prior.kind || (fact.kind == prior.kind && fact.value <= prior.value) {
				return false
			}
		}
		totalBytes += len(fact.value)
		if totalBytes > MaxTypedFactsBytes {
			return false
		}
	}
	return true
}

// Semantic is the closed HAP-03 occurrence semantic.
type Semantic uint8

const (
	SemanticBeforeAgentStart Semantic = iota + 1
)

// BeforeAgentStartOccurrence is the only admitted Advise occurrence in T01.
// Its shape deliberately has no predecessor field: a predecessor is illegal in
// the H03 Advise contract and therefore cannot be represented here.
type BeforeAgentStartOccurrence struct {
	sessionRef     string
	phaseAnchorRef string
	facts          BeforeAgentStartFacts
}

// NewBeforeAgentStartOccurrence constructs a canonical predecessor-free H03 occurrence.
func NewBeforeAgentStartOccurrence(sessionRef, phaseAnchorRef string, facts BeforeAgentStartFacts) (BeforeAgentStartOccurrence, error) {
	if !validOpaqueReference(sessionRef) || !validOpaqueReference(phaseAnchorRef) || !facts.valid() {
		return BeforeAgentStartOccurrence{}, ErrInvalidInput
	}
	return BeforeAgentStartOccurrence{
		sessionRef:     sessionRef,
		phaseAnchorRef: phaseAnchorRef,
		facts:          facts,
	}, nil
}

// Semantic returns the sole H03 Advise semantic.
func (o BeforeAgentStartOccurrence) Semantic() Semantic {
	return SemanticBeforeAgentStart
}

// SessionRef returns the canonical host session reference.
func (o BeforeAgentStartOccurrence) SessionRef() string {
	return o.sessionRef
}

// PhaseAnchorRef returns the canonical stable phase anchor.
func (o BeforeAgentStartOccurrence) PhaseAnchorRef() string {
	return o.phaseAnchorRef
}

// Facts returns the immutable admitted phase facts.
func (o BeforeAgentStartOccurrence) Facts() BeforeAgentStartFacts {
	return o.facts
}

func (o BeforeAgentStartOccurrence) valid() bool {
	return validOpaqueReference(o.sessionRef) && validOpaqueReference(o.phaseAnchorRef) && o.facts.valid()
}

// HostFamily is the host identity dimension committed into a channel key.
type HostFamily uint8

const (
	HostFamilyOMP HostFamily = iota + 1
	HostFamilyClaudeCode
	HostFamilyCodex
)

func hostFamilyFromBinding(family hostadvisor.HostFamily) (HostFamily, bool) {
	switch family {
	case hostadvisor.HostFamilyOMP:
		return HostFamilyOMP, true
	case hostadvisor.HostFamilyClaudeCode:
		return HostFamilyClaudeCode, true
	case hostadvisor.HostFamilyCodex:
		return HostFamilyCodex, true
	default:
		return 0, false
	}
}

// BindingFacts is the immutable HAP-02 binding projection needed by HAP-03.
// It intentionally excludes binding ID and adapter version, which must not
// affect channel or occurrence identity.
type BindingFacts struct {
	subject              Digest
	hostFamily           HostFamily
	channelCommitment    Digest
	capabilityCommitment Digest
	expiresAt            time.Time
	callbackDuration     time.Duration
	adviseAllowed        bool
	observeAllowed       bool
}

// NewBindingFacts projects a current HAP-02 binding without retaining its ID.
func NewBindingFacts(binding hostadvisor.HostBinding) (BindingFacts, error) {
	subjectBytes := binding.Subject().ProofBytes()
	if len(subjectBytes) != len(Digest{}) {
		return BindingFacts{}, ErrInvalidInput
	}
	var subject Digest
	copy(subject[:], subjectBytes)
	if subject == (Digest{}) {
		return BindingFacts{}, ErrInvalidInput
	}

	snapshot := binding.Snapshot()
	channel := binding.Channel()
	hostFamily, validFamily := hostFamilyFromBinding(channel.HostFamily())
	channelCommitment := Digest(channel.Commitment())
	capabilityCommitment := Digest(snapshot.ContractDigest())
	expiresAt := binding.ExpiresAt().UTC()
	callbackDuration := binding.CallbackDeadline()
	if !validFamily || channelCommitment == (Digest{}) || capabilityCommitment == (Digest{}) || expiresAt.IsZero() || callbackDuration <= 0 || callbackDuration%time.Millisecond != 0 {
		return BindingFacts{}, ErrInvalidInput
	}

	adviseAllowed := false
	observeAllowed := false
	for _, capability := range snapshot.Capabilities() {
		if capability.Semantic != hostadvisor.SemanticBeforeAgentStart || capability.Callback.Deadline != callbackDuration {
			continue
		}
		if containsHostAction(capability.Actions, hostadvisor.ActionEmitAdvice) &&
			containsHostInjectionMode(capability.InjectionModes, hostadvisor.InjectionModeHiddenUntrustedMessage) &&
			capability.Correlation.Session && capability.Correlation.Turn && capability.Correlation.StablePhaseAnchor &&
			capability.Callback.Awaited && capability.Callback.Ordering == hostadvisor.CallbackOrderingBeforeFirstAction {
			adviseAllowed = true
		}
		if containsHostAction(capability.Actions, hostadvisor.ActionAdapterAttestation) {
			observeAllowed = true
		}
	}

	return BindingFacts{
		subject:              subject,
		hostFamily:           hostFamily,
		channelCommitment:    channelCommitment,
		capabilityCommitment: capabilityCommitment,
		expiresAt:            expiresAt,
		callbackDuration:     callbackDuration,
		adviseAllowed:        adviseAllowed,
		observeAllowed:       observeAllowed,
	}, nil
}

// Subject returns the server-derived subject commitment.
func (f BindingFacts) Subject() Digest {
	return f.subject
}

// HostFamily returns the committed host family.
func (f BindingFacts) HostFamily() HostFamily {
	return f.hostFamily
}

// ChannelCommitment returns the protected adapter/runtime channel identity.
func (f BindingFacts) ChannelCommitment() Digest {
	return f.channelCommitment
}

// CapabilityCommitment returns the protected accepted capability contract digest.
func (f BindingFacts) CapabilityCommitment() Digest {
	return f.capabilityCommitment
}

// ExpiresAt returns the accepted binding expiry in UTC.
func (f BindingFacts) ExpiresAt() time.Time {
	return f.expiresAt
}

// CallbackDuration returns the accepted host callback duration.
func (f BindingFacts) CallbackDuration() time.Duration {
	return f.callbackDuration
}

// AllowsAdvise reports whether the binding independently grants the full
// BEFORE_AGENT_START/EMIT_ADVICE admission subset. It does not require
// ADAPTER_ATTESTATION.
func (f BindingFacts) AllowsAdvise() bool {
	return f.valid() && f.adviseAllowed
}

// AllowsObserve reports whether the binding independently grants
// ADAPTER_ATTESTATION on the admitted BEFORE_AGENT_START channel.
func (f BindingFacts) AllowsObserve() bool {
	return f.valid() && f.observeAllowed
}

// LiveAt reports binding liveness only against the caller-supplied instant.
func (f BindingFacts) LiveAt(at time.Time) bool {
	return f.valid() && !at.IsZero() && at.UTC().Before(f.expiresAt)
}

func (f BindingFacts) valid() bool {
	return f.subject != (Digest{}) && f.hostFamily != 0 && f.channelCommitment != (Digest{}) && f.capabilityCommitment != (Digest{}) && !f.expiresAt.IsZero() && f.callbackDuration > 0 && f.callbackDuration%time.Millisecond == 0
}

func containsHostAction(actions []hostadvisor.Action, wanted hostadvisor.Action) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

func containsHostInjectionMode(modes []hostadvisor.InjectionMode, wanted hostadvisor.InjectionMode) bool {
	for _, mode := range modes {
		if mode == wanted {
			return true
		}
	}
	return false
}

// AdviseInput is the parsed immutable T01 advisor request.
type AdviseInput struct {
	binding    BindingFacts
	project    taskmemory.ProjectEvidenceV3
	occurrence BeforeAgentStartOccurrence
}

// NewAdviseInput joins parsed untrusted project evidence to a validated binding
// projection and predecessor-free occurrence. The evidence remains untrusted;
// the Advisor must resolve authority through the accepted TaskMemory seam.
func NewAdviseInput(binding BindingFacts, project taskmemory.ProjectEvidenceV3, occurrence BeforeAgentStartOccurrence) (AdviseInput, error) {
	if !binding.valid() || !occurrence.valid() {
		return AdviseInput{}, ErrInvalidInput
	}
	return AdviseInput{
		binding:    binding,
		project:    cloneProjectEvidence(project),
		occurrence: occurrence,
	}, nil
}

// Binding returns the immutable active binding projection.
func (i AdviseInput) Binding() BindingFacts {
	return i.binding
}

// ProjectEvidence returns an independently owned untrusted V3 evidence value.
func (i AdviseInput) ProjectEvidence() taskmemory.ProjectEvidenceV3 {
	return cloneProjectEvidence(i.project)
}

// Occurrence returns the predecessor-free admitted occurrence.
func (i AdviseInput) Occurrence() BeforeAgentStartOccurrence {
	return i.occurrence
}

// OccurrenceIdentity contains only the server-authoritative fields permitted in
// an occurrence HMAC. Binding IDs, capability revisions, and task facts are
// deliberately absent.
type OccurrenceIdentity struct {
	subject          Digest
	canonicalProject string
	actorPrincipal   string
	actorKind        string
	workstation      string
	occurrence       BeforeAgentStartOccurrence
}

// NewOccurrenceIdentity projects the accepted TaskMemory authority needed for a
// binding-independent occurrence key.
func NewOccurrenceIdentity(binding BindingFacts, authority taskmemory.AuthorizedTaskContext, occurrence BeforeAgentStartOccurrence) (OccurrenceIdentity, error) {
	if !binding.valid() || !occurrence.valid() {
		return OccurrenceIdentity{}, ErrInvalidInput
	}
	project := string(authority.CanonicalProject())
	caller := authority.KeycardContext()
	if !validOpaqueReference(project) || !validOpaqueReference(caller.WorkstationID) || !validPrincipal(caller.Principal, caller.PrincipalKind) {
		return OccurrenceIdentity{}, ErrInvalidInput
	}
	return OccurrenceIdentity{
		subject:          binding.subject,
		canonicalProject: project,
		actorPrincipal:   caller.Principal,
		actorKind:        caller.PrincipalKind,
		workstation:      caller.WorkstationID,
		occurrence:       occurrence,
	}, nil
}

func (i OccurrenceIdentity) valid() bool {
	return i.subject != (Digest{}) && validOpaqueReference(i.canonicalProject) && validOpaqueReference(i.workstation) && validPrincipal(i.actorPrincipal, i.actorKind) && i.occurrence.valid()
}

func validPrincipal(principal, kind string) bool {
	if principal == "" {
		return kind == ""
	}
	if !validOpaqueReference(principal) {
		return false
	}
	switch kind {
	case "human", "agent", "service":
		return true
	default:
		return false
	}
}

// AttestationKind is a receipt-bound adapter claim, never objective evidence.
type AttestationKind uint8

const (
	AttestationDecisionReceived AttestationKind = iota + 1
	AttestationUntrustedReferencePresented
)

func validAttestationKind(kind AttestationKind) bool {
	switch kind {
	case AttestationDecisionReceived, AttestationUntrustedReferencePresented:
		return true
	default:
		return false
	}
}

// SemanticGapCode is the closed set of adapter-reported semantic gaps.
type SemanticGapCode uint8

const (
	SemanticGapCallbackUnavailable SemanticGapCode = iota + 1
	SemanticGapContextInjectionUnavailable
	SemanticGapReceiptCorrelationUnavailable
)

func validSemanticGapCode(code SemanticGapCode) bool {
	switch code {
	case SemanticGapCallbackUnavailable, SemanticGapContextInjectionUnavailable, SemanticGapReceiptCorrelationUnavailable:
		return true
	default:
		return false
	}
}

// ObservationTargetKind distinguishes the two final Observe target shapes.
type ObservationTargetKind uint8

const (
	ObservationTargetReceiptBound ObservationTargetKind = iota + 1
	ObservationTargetChannelGap
)

// ReceiptBoundObservation is a receipt-bound attestation or semantic gap.
type ReceiptBoundObservation struct {
	receipt     ReceiptIdentity
	anchorRef   string
	attestation AttestationKind
	gap         SemanticGapCode
	targetKind  observationEvidenceKind
}

type observationEvidenceKind uint8

const (
	observationEvidenceAttestation observationEvidenceKind = iota + 1
	observationEvidenceGap
)

// Receipt returns the immutable decision receipt identity.
func (o ReceiptBoundObservation) Receipt() ReceiptIdentity {
	return o.receipt
}

// AnchorRef returns the bounded observation anchor reference.
func (o ReceiptBoundObservation) AnchorRef() string {
	return o.anchorRef
}

// Attestation returns the claim only for an attestation target.
func (o ReceiptBoundObservation) Attestation() (AttestationKind, bool) {
	return o.attestation, o.targetKind == observationEvidenceAttestation
}

// SemanticGap returns the code only for a semantic-gap target.
func (o ReceiptBoundObservation) SemanticGap() (SemanticGapCode, bool) {
	return o.gap, o.targetKind == observationEvidenceGap
}

func (o ReceiptBoundObservation) valid() bool {
	if !o.receipt.valid() || !validOpaqueReference(o.anchorRef) {
		return false
	}
	switch o.targetKind {
	case observationEvidenceAttestation:
		return validAttestationKind(o.attestation) && o.gap == 0
	case observationEvidenceGap:
		return o.attestation == 0 && validSemanticGapCode(o.gap)
	default:
		return false
	}
}

// ChannelSemanticGap is a receipt-free channel-level semantic gap.
type ChannelSemanticGap struct {
	anchorRef string
	gap       SemanticGapCode
}

// AnchorRef returns the bounded observation anchor reference.
func (o ChannelSemanticGap) AnchorRef() string {
	return o.anchorRef
}

// SemanticGap returns the closed adapter semantic gap.
func (o ChannelSemanticGap) SemanticGap() SemanticGapCode {
	return o.gap
}

func (o ChannelSemanticGap) valid() bool {
	return validOpaqueReference(o.anchorRef) && validSemanticGapCode(o.gap)
}

// ObserveInput is the parsed immutable T01 observation request.
type ObserveInput struct {
	binding      BindingFacts
	receiptBound *ReceiptBoundObservation
	channelGap   *ChannelSemanticGap
}

// NewObserveReceiptAttestation constructs the only receipt-bound attestation input.
func NewObserveReceiptAttestation(binding BindingFacts, receipt ReceiptIdentity, anchorRef string, attestation AttestationKind) (ObserveInput, error) {
	observation := ReceiptBoundObservation{
		receipt:     receipt,
		anchorRef:   anchorRef,
		attestation: attestation,
		targetKind:  observationEvidenceAttestation,
	}
	return newObserveInput(binding, &observation, nil)
}

// NewObserveReceiptSemanticGap constructs the only receipt-bound gap input.
func NewObserveReceiptSemanticGap(binding BindingFacts, receipt ReceiptIdentity, anchorRef string, gap SemanticGapCode) (ObserveInput, error) {
	observation := ReceiptBoundObservation{
		receipt:    receipt,
		anchorRef:  anchorRef,
		gap:        gap,
		targetKind: observationEvidenceGap,
	}
	return newObserveInput(binding, &observation, nil)
}

// NewObserveChannelSemanticGap constructs the only channel-only gap input.
func NewObserveChannelSemanticGap(binding BindingFacts, anchorRef string, gap SemanticGapCode) (ObserveInput, error) {
	observation := ChannelSemanticGap{anchorRef: anchorRef, gap: gap}
	return newObserveInput(binding, nil, &observation)
}

func newObserveInput(binding BindingFacts, receiptBound *ReceiptBoundObservation, channelGap *ChannelSemanticGap) (ObserveInput, error) {
	if !binding.valid() || (receiptBound == nil) == (channelGap == nil) {
		return ObserveInput{}, ErrInvalidInput
	}
	if receiptBound != nil {
		if !receiptBound.valid() {
			return ObserveInput{}, ErrInvalidInput
		}
		copy := *receiptBound
		return ObserveInput{binding: binding, receiptBound: &copy}, nil
	}
	if !channelGap.valid() {
		return ObserveInput{}, ErrInvalidInput
	}
	copy := *channelGap
	return ObserveInput{binding: binding, channelGap: &copy}, nil
}

// Binding returns the immutable active binding projection.
func (i ObserveInput) Binding() BindingFacts {
	return i.binding
}

// TargetKind returns the closed observation target kind.
func (i ObserveInput) TargetKind() ObservationTargetKind {
	if i.receiptBound != nil {
		return ObservationTargetReceiptBound
	}
	if i.channelGap != nil {
		return ObservationTargetChannelGap
	}
	return 0
}

// ReceiptBound returns the receipt-bound target when present.
func (i ObserveInput) ReceiptBound() (ReceiptBoundObservation, bool) {
	if i.receiptBound == nil {
		return ReceiptBoundObservation{}, false
	}
	return *i.receiptBound, true
}

// ChannelGap returns the channel-only gap when present.
func (i ObserveInput) ChannelGap() (ChannelSemanticGap, bool) {
	if i.channelGap == nil {
		return ChannelSemanticGap{}, false
	}
	return *i.channelGap, true
}

// Advisor is the pure HAP-03 boundary consumed by the concrete gRPC facade.
type Advisor interface {
	Advise(context.Context, AdviseInput) (Decision, error)
	Observe(context.Context, ObserveInput) (ObservationAck, error)
}

func validOpaqueReference(value string) bool {
	return validText(value, MaxOpaqueReferenceBytes)
}

func validText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validRelativePath(value string) bool {
	if strings.ContainsAny(value, `\\:`) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func cloneProjectEvidence(project taskmemory.ProjectEvidenceV3) taskmemory.ProjectEvidenceV3 {
	project.Descriptor.NormalizedGitRemotes = append([]string(nil), project.Descriptor.NormalizedGitRemotes...)
	project.Descriptor.LegacyIdentifiers = append([]projectidentity.LegacyIdentifierV3(nil), project.Descriptor.LegacyIdentifiers...)
	return project
}
