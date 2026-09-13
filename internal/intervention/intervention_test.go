package intervention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/hostadvisor"
	"github.com/thebtf/engram/internal/taskmemory"
)

var interventionTestTime = time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)

func TestBeforeAgentStartFactsBoundsCanonicalOrderAndCopies(t *testing.T) {
	keyword, facts := assertCanonicalBeforeAgentStartFacts(t)
	assertBeforeAgentStartFactRejections(t, keyword)
	assertBeforeAgentStartOccurrence(t, facts)
}

func assertCanonicalBeforeAgentStartFacts(t *testing.T) (TypedFact, BeforeAgentStartFacts) {
	t.Helper()
	keyword := mustTypedFact(t, FactKindKeyword, "advisor")
	pathFact := mustTypedFact(t, FactKindPath, "internal/intervention/types.go")
	tool := mustTypedFact(t, FactKindTool, "read")
	input := []TypedFact{tool, pathFact, keyword}
	facts, err := NewBeforeAgentStartFacts("  implement immutable HAP values  ", input)
	if err != nil {
		t.Fatalf("NewBeforeAgentStartFacts() error = %v", err)
	}
	if facts.TaskQuery() != "implement immutable HAP values" {
		t.Fatalf("TaskQuery() = %q", facts.TaskQuery())
	}
	canonical := facts.Facts()
	if len(canonical) != 3 || canonical[0].Kind() != FactKindKeyword || canonical[1].Kind() != FactKindPath || canonical[2].Kind() != FactKindTool {
		t.Fatalf("Facts() canonical order = %#v", canonical)
	}
	input[0].value = "mutated-input"
	canonical[0].value = "mutated-output"
	fresh := facts.Facts()
	if fresh[0].Value() != "advisor" || fresh[2].Value() != "read" {
		t.Fatalf("facts leaked caller slice mutation: %#v", fresh)
	}
	return keyword, facts
}

func assertBeforeAgentStartFactRejections(t *testing.T, keyword TypedFact) {
	t.Helper()
	if _, err := NewBeforeAgentStartFacts("query", []TypedFact{keyword, keyword}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate facts error = %v, want ErrInvalidInput", err)
	}
	if _, err := NewBeforeAgentStartFacts("query\nwith-control", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("control query error = %v, want ErrInvalidInput", err)
	}
	tooMany := make([]TypedFact, MaxTypedFacts+1)
	for index := range tooMany {
		tooMany[index] = mustTypedFact(t, FactKindKeyword, fmt.Sprintf("keyword-%02d", index))
	}
	if _, err := NewBeforeAgentStartFacts("query", tooMany); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("too many facts error = %v, want ErrInvalidInput", err)
	}
	overBudget := make([]TypedFact, 9)
	for index := range overBudget {
		overBudget[index] = mustTypedFact(t, FactKindKeyword, strings.Repeat("x", MaxTypedFactBytes-1)+string(rune('a'+index)))
	}
	if _, err := NewBeforeAgentStartFacts("query", overBudget); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("fact byte budget error = %v, want ErrInvalidInput", err)
	}
	if _, err := NewTypedFact(FactKindPath, "../outside"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("parent path error = %v, want ErrInvalidInput", err)
	}
	if _, err := NewTypedFact(FactKindPath, "dir//child"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("noncanonical path error = %v, want ErrInvalidInput", err)
	}
	if _, err := NewTypedFact(FactKindKeyword, strings.Repeat("x", MaxTypedFactBytes+1)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized fact error = %v, want ErrInvalidInput", err)
	}
}

func assertBeforeAgentStartOccurrence(t *testing.T, facts BeforeAgentStartFacts) {
	t.Helper()
	occurrence, err := NewBeforeAgentStartOccurrence("session-1", "turn-1", facts)
	if err != nil || occurrence.Semantic() != SemanticBeforeAgentStart {
		t.Fatalf("NewBeforeAgentStartOccurrence() = (%#v, %v)", occurrence, err)
	}
	if _, err := NewBeforeAgentStartOccurrence(strings.Repeat("s", MaxOpaqueReferenceBytes+1), "turn-1", facts); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized session ref error = %v, want ErrInvalidInput", err)
	}
}

func TestBindingFactsProjectionAndIndependentCapabilities(t *testing.T) {
	binding := fixtureHostBinding(t)
	facts, err := NewBindingFacts(binding)
	if err != nil {
		t.Fatalf("NewBindingFacts() error = %v", err)
	}
	if facts.Subject() != Digest(testBytes(19)) || facts.HostFamily() != HostFamilyOMP || facts.ChannelCommitment() == (Digest{}) {
		t.Fatalf("binding projection = %#v", facts)
	}
	if got, want := facts.CapabilityCommitment(), Digest(binding.Snapshot().ContractDigest()); got != want {
		t.Fatalf("CapabilityCommitment() = %x, want %x", got, want)
	}
	if !facts.AllowsAdvise() || !facts.AllowsObserve() || !facts.LiveAt(interventionTestTime) {
		t.Fatalf("binding capability projection was not admitted")
	}

	adviseOnly := fixtureBindingFacts()
	adviseOnly.observeAllowed = false
	if !adviseOnly.AllowsAdvise() || adviseOnly.AllowsObserve() {
		t.Fatal("Advise must not require ADAPTER_ATTESTATION")
	}
	observeOnly := fixtureBindingFacts()
	observeOnly.adviseAllowed = false
	if observeOnly.AllowsAdvise() || !observeOnly.AllowsObserve() {
		t.Fatal("Observe must independently require only ADAPTER_ATTESTATION")
	}
}

func TestKeyEpochDerivationsAreSeparatedAndStableAcrossRebind(t *testing.T) {
	epoch := fixtureKeyEpoch(t)
	binding := fixtureBindingFacts()
	occurrence := fixtureOccurrence(t, "task query", []TypedFact{
		mustTypedFact(t, FactKindTool, "read"),
		mustTypedFact(t, FactKindKeyword, "hmac"),
	})
	identity := fixtureOccurrenceIdentity(binding, occurrence)

	channel, err := epoch.DeriveChannel(binding)
	if err != nil {
		t.Fatalf("DeriveChannel() error = %v", err)
	}
	sessionKey, err := epoch.DeriveSessionKey(identity)
	if err != nil {
		t.Fatalf("DeriveSessionKey() error = %v", err)
	}
	occurrenceKey, err := epoch.DeriveOccurrence(identity)
	if err != nil {
		t.Fatalf("DeriveOccurrence() error = %v", err)
	}
	content, err := epoch.DeriveContent(occurrence)
	if err != nil {
		t.Fatalf("DeriveContent() error = %v", err)
	}
	if channel == sessionKey || channel == occurrenceKey || channel == content || sessionKey == occurrenceKey || sessionKey == content || occurrenceKey == content {
		t.Fatalf("domain-separated commitments collided: channel=%x session=%x occurrence=%x content=%x", channel, sessionKey, occurrenceKey, content)
	}

	rebound := binding
	rebound.callbackDuration = 1500 * time.Millisecond
	rebound.expiresAt = interventionTestTime.Add(3 * time.Hour)
	rebound.adviseAllowed = false
	rebound.observeAllowed = false
	reboundIdentity := fixtureOccurrenceIdentity(rebound, occurrence)
	reboundKey, err := epoch.DeriveOccurrence(reboundIdentity)
	if err != nil {
		t.Fatalf("rebound DeriveOccurrence() error = %v", err)
	}
	if reboundKey != occurrenceKey {
		t.Fatalf("occurrence key changed across a rebind: %x != %x", reboundKey, occurrenceKey)
	}

	otherRuntime := binding
	otherRuntime.channelCommitment = Digest(testBytes(29))
	otherChannel, err := epoch.DeriveChannel(otherRuntime)
	if err != nil {
		t.Fatalf("other runtime DeriveChannel() error = %v", err)
	}
	if otherChannel == channel {
		t.Fatal("channel commitment ignored runtime identity")
	}

	otherAnchor := fixtureOccurrence(t, "task query", occurrence.Facts().Facts())
	otherAnchor.phaseAnchorRef = "turn-2"
	otherIdentity := fixtureOccurrenceIdentity(binding, otherAnchor)
	otherSession, err := epoch.DeriveSessionKey(otherIdentity)
	if err != nil {
		t.Fatalf("other anchor session key error = %v", err)
	}
	if otherSession != sessionKey {
		t.Fatal("session key included a forbidden phase anchor")
	}
	otherOccurrence, err := epoch.DeriveOccurrence(otherIdentity)
	if err != nil {
		t.Fatalf("other anchor occurrence key error = %v", err)
	}
	if otherOccurrence == occurrenceKey {
		t.Fatal("occurrence commitment ignored phase anchor")
	}

	otherSessionOccurrence := occurrence
	otherSessionOccurrence.sessionRef = "session-2"
	otherSessionIdentity := fixtureOccurrenceIdentity(binding, otherSessionOccurrence)
	differentSession, err := epoch.DeriveSessionKey(otherSessionIdentity)
	if err != nil {
		t.Fatalf("other session key error = %v", err)
	}
	if differentSession == sessionKey {
		t.Fatal("session commitment ignored host session identity")
	}
	otherContent, err := epoch.DeriveContent(otherAnchor)
	if err != nil {
		t.Fatalf("other anchor DeriveContent() error = %v", err)
	}
	if otherContent != content {
		t.Fatal("content commitment included forbidden session or anchor identity")
	}

	differentFactsOccurrence := fixtureOccurrence(t, "changed task query", occurrence.Facts().Facts())
	differentContent, err := epoch.DeriveContent(differentFactsOccurrence)
	if err != nil {
		t.Fatalf("changed content DeriveContent() error = %v", err)
	}
	if differentContent == content {
		t.Fatal("content commitment ignored task query")
	}

	reordered := fixtureOccurrence(t, "task query", []TypedFact{
		mustTypedFact(t, FactKindKeyword, "hmac"),
		mustTypedFact(t, FactKindTool, "read"),
	})
	reorderedContent, err := epoch.DeriveContent(reordered)
	if err != nil {
		t.Fatalf("reordered content DeriveContent() error = %v", err)
	}
	if reorderedContent != content {
		t.Fatal("canonical fact ordering changed content commitment")
	}

	duplicateKey := testBytes(1)
	if _, err := NewKeyEpoch(testBytes(9), duplicateKey, duplicateKey, testBytes(3), testBytes(4)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate derived key error = %v, want ErrInvalidInput", err)
	}
	provider := NewStaticKeyProvider(epoch)
	current, ok := provider.Current()
	if !ok || current.EpochCommitment() != epoch.EpochCommitment() || current.ChannelKey() != epoch.ChannelKey() || current.OccurrenceKey() != epoch.OccurrenceKey() || current.ContentKey() != epoch.ContentKey() || current.ReceiptKey() != epoch.ReceiptKey() {
		t.Fatalf("current key epoch = %#v, %t", current, ok)
	}
	if _, ok := NewStaticKeyProvider(KeyEpoch{}).Current(); ok {
		t.Fatal("zero epoch must not become current")
	}
}

func TestEffectiveDeadlineMinimumAndReserves(t *testing.T) {
	entry := interventionTestTime
	for _, test := range []struct {
		name      string
		callback  time.Duration
		expiresIn time.Duration
		contextIn time.Duration
		want      time.Duration
	}{
		{name: "context", callback: 1500 * time.Millisecond, expiresIn: 1800 * time.Millisecond, contextIn: 700 * time.Millisecond, want: 700 * time.Millisecond},
		{name: "callback", callback: 600 * time.Millisecond, expiresIn: 1800 * time.Millisecond, contextIn: 1500 * time.Millisecond, want: 600 * time.Millisecond},
		{name: "expiry", callback: 1500 * time.Millisecond, expiresIn: 500 * time.Millisecond, contextIn: 1200 * time.Millisecond, want: 500 * time.Millisecond},
		{name: "hard cap", callback: 3 * time.Second, expiresIn: 4 * time.Second, contextIn: 3500 * time.Millisecond, want: HardDeadlineCap},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := fixtureBindingFacts()
			binding.callbackDuration = test.callback
			binding.expiresAt = entry.Add(test.expiresIn)
			ctx, cancel := context.WithDeadline(context.Background(), entry.Add(test.contextIn))
			t.Cleanup(cancel)
			deadline, err := NewEffectiveDeadline(ctx, entry, binding)
			if err != nil {
				t.Fatalf("NewEffectiveDeadline() error = %v", err)
			}
			if got := deadline.Deadline(); !got.Equal(entry.Add(test.want)) {
				t.Fatalf("Deadline() = %s, want %s", got, entry.Add(test.want))
			}
		})
	}

	binding := fixtureBindingFacts()
	binding.callbackDuration = time.Second
	binding.expiresAt = entry.Add(time.Second)
	deadline, err := NewEffectiveDeadline(context.Background(), entry, binding)
	if err != nil {
		t.Fatalf("NewEffectiveDeadline() error = %v", err)
	}
	if !deadline.AllowsEntry(entry) || !deadline.AllowsWork(entry.Add(700*time.Millisecond)) || deadline.AllowsWork(entry.Add(701*time.Millisecond)) {
		t.Fatal("entry/work reserve boundary is wrong")
	}
	if !deadline.AllowsCommit(entry.Add(700*time.Millisecond)) || deadline.AllowsCommit(entry.Add(701*time.Millisecond)) {
		t.Fatal("commit reserve boundary is wrong")
	}
	if !deadline.AllowsResponse(entry.Add(950*time.Millisecond)) || deadline.AllowsResponse(entry.Add(951*time.Millisecond)) {
		t.Fatal("response reserve boundary is wrong")
	}
	if got := deadline.WorkDeadline(); !got.Equal(entry.Add(700 * time.Millisecond)) {
		t.Fatalf("WorkDeadline() = %s, want %s", got, entry.Add(700*time.Millisecond))
	}

	ctx, cancel := context.WithDeadline(context.Background(), entry)
	t.Cleanup(cancel)
	if _, err := NewEffectiveDeadline(ctx, entry, binding); !errors.Is(err, ErrDeadlineElapsed) {
		t.Fatalf("elapsed deadline error = %v, want ErrDeadlineElapsed", err)
	}
}

func TestFinalTaggedDecisionAndObservationValues(t *testing.T) {
	receipt := mustReceipt(t, "receipt-1", 41)
	knowledge := mustKnowledgeReference(t, 99, 7, CandidateTierExact, 42)
	presentation := mustPresentation(t, "[untrusted reference]")
	packet := mustPacket(t, receipt, interventionTestTime.Add(time.Hour), knowledge, presentation)
	assertEmitDecision(t, receipt, packet)
	assertAbstainDecisionValue(t, receipt)
	assertDeliveryAmbiguousDecisionValue(t, receipt)
	assertUnavailableDecisionValue(t)
	assertObservationAckValues(t)
}

func assertEmitDecision(t *testing.T, receipt ReceiptIdentity, packet Packet) {
	t.Helper()
	emit, err := NewEmitDecision(receipt, packet)
	if err != nil || !emit.Valid() || emit.Kind() != DecisionEmit {
		t.Fatalf("NewEmitDecision() = (%#v, %v)", emit, err)
	}
	gotReceipt, gotPacket, ok := emit.Emit()
	if !ok || gotReceipt != receipt || gotPacket.Knowledge().MemoryID() != 99 {
		t.Fatalf("Emit() = (%#v, %#v, %t)", gotReceipt, gotPacket, ok)
	}
	if _, _, ok := emit.Abstain(); ok {
		t.Fatal("EMIT exposed an impossible abstention payload")
	}
	otherReceipt := mustReceipt(t, "receipt-2", 43)
	if _, err := NewEmitDecision(otherReceipt, packet); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mixed receipt packet error = %v, want ErrInvalidInput", err)
	}
}

func assertAbstainDecisionValue(t *testing.T, receipt ReceiptIdentity) {
	t.Helper()
	abstain, err := NewAbstainDecision(receipt, AbstentionNoCandidates)
	if err != nil || abstain.Kind() != DecisionAbstain {
		t.Fatalf("NewAbstainDecision() = (%#v, %v)", abstain, err)
	}
	if gotReceipt, reason, ok := abstain.Abstain(); !ok || gotReceipt != receipt || reason != AbstentionNoCandidates {
		t.Fatalf("Abstain() = (%#v, %v, %t)", gotReceipt, reason, ok)
	}
}

func assertDeliveryAmbiguousDecisionValue(t *testing.T, receipt ReceiptIdentity) {
	t.Helper()
	ambiguous, err := NewDeliveryAmbiguousDecision(receipt)
	if err != nil || ambiguous.Kind() != DecisionDeliveryAmbiguous {
		t.Fatalf("NewDeliveryAmbiguousDecision() = (%#v, %v)", ambiguous, err)
	}
	if gotReceipt, ok := ambiguous.DeliveryAmbiguous(); !ok || gotReceipt != receipt {
		t.Fatalf("DeliveryAmbiguous() = (%#v, %t)", gotReceipt, ok)
	}
}

func assertUnavailableDecisionValue(t *testing.T) {
	t.Helper()
	unavailable, err := NewUnavailableDecision("correlation-1", interventionTestTime.Add(time.Minute), UnavailableDependency)
	if err != nil || unavailable.Kind() != DecisionUnavailable {
		t.Fatalf("NewUnavailableDecision() = (%#v, %v)", unavailable, err)
	}
	if value, ok := unavailable.Unavailable(); !ok || value.Code() != UnavailableDependency || value.CorrelationID() != "correlation-1" {
		t.Fatalf("Unavailable() = (%#v, %t)", value, ok)
	}
}

func assertObservationAckValues(t *testing.T) {
	t.Helper()
	accepted, err := NewAcceptedObservationAck("observation-1", ObservationReasonAcceptedAttestation)
	if err != nil || !accepted.Valid() || accepted.State() != ObservationAccepted {
		t.Fatalf("NewAcceptedObservationAck() = (%#v, %v)", accepted, err)
	}
	if id, ok := accepted.ObservationID(); !ok || id != "observation-1" || accepted.Reason() != ObservationReasonAcceptedAttestation {
		t.Fatalf("accepted acknowledgement = (%q, %t, %v)", id, ok, accepted.Reason())
	}
	duplicate, err := NewDuplicateObservationAck("observation-1")
	if err != nil || !duplicate.Valid() || duplicate.State() != ObservationDuplicate || duplicate.Reason() != ObservationReasonDuplicate {
		t.Fatalf("NewDuplicateObservationAck() = (%#v, %v)", duplicate, err)
	}
	rejected := NewRejectedObservationAck()
	if !rejected.Valid() || rejected.State() != ObservationRejected || rejected.Reason() != ObservationReasonInvalidTarget {
		t.Fatalf("NewRejectedObservationAck() = %#v", rejected)
	}
	if _, ok := rejected.ObservationID(); ok {
		t.Fatal("rejected acknowledgement exposed an impossible observation ID")
	}
	unavailable, err := NewUnavailableObservationAck(ObservationReasonDependencyUnavailable)
	if err != nil || !unavailable.Valid() || unavailable.State() != ObservationUnavailable {
		t.Fatalf("NewUnavailableObservationAck() = (%#v, %v)", unavailable, err)
	}
	if _, err := NewUnavailableObservationAck(ObservationReasonDuplicate); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid unavailable reason error = %v, want ErrInvalidInput", err)
	}
}

func TestAdvisorFakeConsumesParsedImmutableInputs(t *testing.T) {
	binding := fixtureBindingFacts()
	occurrence := fixtureOccurrence(t, "query", nil)
	adviseInput, err := NewAdviseInput(binding, taskmemory.ProjectEvidenceV3{}, occurrence)
	if err != nil {
		t.Fatalf("NewAdviseInput() error = %v", err)
	}
	receipt := mustReceipt(t, "receipt-1", 61)
	observeInput, err := NewObserveChannelSemanticGap(binding, "observation-anchor", SemanticGapCallbackUnavailable)
	if err != nil {
		t.Fatalf("NewObserveChannelSemanticGap() error = %v", err)
	}
	decision := mustAbstainDecision(t, receipt, AbstentionNoCandidates)
	ack, err := NewAcceptedObservationAck("observation-1", ObservationReasonAcceptedSemanticGap)
	if err != nil {
		t.Fatalf("NewAcceptedObservationAck() error = %v", err)
	}
	fake := &recordingAdvisor{decision: decision, acknowledgement: ack}

	gotDecision, err := fake.Advise(context.Background(), adviseInput)
	if err != nil || !gotDecision.Valid() || fake.adviseCalls != 1 || fake.lastAdvise.Occurrence().SessionRef() != "session-1" {
		t.Fatalf("Advisor.Advise() = (%#v, %v), calls=%d", gotDecision, err, fake.adviseCalls)
	}
	gotAck, err := fake.Observe(context.Background(), observeInput)
	if err != nil || !gotAck.Valid() || fake.observeCalls != 1 || fake.lastObserve.TargetKind() != ObservationTargetChannelGap {
		t.Fatalf("Advisor.Observe() = (%#v, %v), calls=%d", gotAck, err, fake.observeCalls)
	}
}

type recordingAdvisor struct {
	decision        Decision
	acknowledgement ObservationAck
	adviseCalls     int
	observeCalls    int
	lastAdvise      AdviseInput
	lastObserve     ObserveInput
}

func (a *recordingAdvisor) Advise(_ context.Context, input AdviseInput) (Decision, error) {
	a.adviseCalls++
	a.lastAdvise = input
	return a.decision, nil
}

func (a *recordingAdvisor) Observe(_ context.Context, input ObserveInput) (ObservationAck, error) {
	a.observeCalls++
	a.lastObserve = input
	return a.acknowledgement, nil
}

func fixtureHostBinding(t *testing.T) hostadvisor.HostBinding {
	t.Helper()
	profile, err := hostadvisor.NewOMPAdvisor2Profile(hostadvisor.OMPAdvisor1ProfileSpec{
		HostVersion:             "1.0.0",
		AdapterID:               "omp-adapter",
		AdapterVersion:          "1.0.0",
		InstalledArtifactDigest: hostadvisor.Digest(testBytes(18)),
		RuntimeProbeReceiptID:   "probe-1",
		SnapshotID:              "snapshot-1",
		SnapshotRevision:        1,
		CallbackDeadline:        time.Second,
		BindingTTL:              time.Hour,
	})
	if err != nil {
		t.Fatalf("NewOMPAdvisor2Profile() error = %v", err)
	}
	registry, err := hostadvisor.NewRegistry(hostadvisor.RegistryConfig{
		Profiles:    []hostadvisor.AcceptedProfile{profile},
		MaxBindings: 1,
		Now:         func() time.Time { return interventionTestTime },
		NewBindingID: func() (hostadvisor.BindingID, error) {
			return hostadvisor.BindingID("binding-1"), nil
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	subject, err := hostadvisor.NewAuthenticatedSubject(hostadvisor.Digest(testBytes(19)))
	if err != nil {
		t.Fatalf("NewAuthenticatedSubject() error = %v", err)
	}
	binding, err := registry.Bind(subject, hostadvisor.HostHello{
		Protocol: profile.Protocol,
		Host: hostadvisor.HostIdentity{
			Family:             hostadvisor.HostFamilyOMP,
			HostVersion:        "1.0.0",
			AdapterID:          "omp-adapter",
			AdapterVersion:     "1.0.0",
			RuntimeInstanceRef: "runtime-1",
		},
		Requested: profile.Capabilities,
		Evidence:  profile.Evidence,
	})
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	return binding
}

func fixtureBindingFacts() BindingFacts {
	return BindingFacts{
		subject:              Digest(testBytes(19)),
		hostFamily:           HostFamilyOMP,
		channelCommitment:    Digest(testBytes(20)),
		capabilityCommitment: Digest(testBytes(21)),
		expiresAt:            interventionTestTime.Add(time.Hour),
		callbackDuration:     time.Second,
		adviseAllowed:        true,
		observeAllowed:       true,
	}
}

func fixtureKeyEpoch(t *testing.T) KeyEpoch {
	t.Helper()
	epoch, err := NewKeyEpoch(testBytes(1), testBytes(2), testBytes(3), testBytes(4), testBytes(5))
	if err != nil {
		t.Fatalf("NewKeyEpoch() error = %v", err)
	}
	return epoch
}

func fixtureOccurrence(t *testing.T, query string, facts []TypedFact) BeforeAgentStartOccurrence {
	t.Helper()
	phaseFacts, err := NewBeforeAgentStartFacts(query, facts)
	if err != nil {
		t.Fatalf("NewBeforeAgentStartFacts() error = %v", err)
	}
	occurrence, err := NewBeforeAgentStartOccurrence("session-1", "turn-1", phaseFacts)
	if err != nil {
		t.Fatalf("NewBeforeAgentStartOccurrence() error = %v", err)
	}
	return occurrence
}

func fixtureOccurrenceIdentity(binding BindingFacts, occurrence BeforeAgentStartOccurrence) OccurrenceIdentity {
	return OccurrenceIdentity{
		subject:          binding.subject,
		canonicalProject: "00000000-0000-4000-8000-000000000001",
		actorPrincipal:   "agent-1",
		actorKind:        "agent",
		workstation:      "workstation-1",
		occurrence:       occurrence,
	}
}

func mustTypedFact(t *testing.T, kind FactKind, value string) TypedFact {
	t.Helper()
	fact, err := NewTypedFact(kind, value)
	if err != nil {
		t.Fatalf("NewTypedFact(%v, %q) error = %v", kind, value, err)
	}
	return fact
}

func mustReceipt(t *testing.T, id string, integrity byte) ReceiptIdentity {
	t.Helper()
	receipt, err := NewReceiptIdentity(id, testBytes(integrity))
	if err != nil {
		t.Fatalf("NewReceiptIdentity() error = %v", err)
	}
	return receipt
}

func mustKnowledgeReference(t *testing.T, memoryID int64, version uint32, tier CandidateTier, digest byte) KnowledgeReference {
	t.Helper()
	reference, err := NewKnowledgeReference(memoryID, version, "00000000-0000-4000-8000-000000000001", tier, testBytes(digest))
	if err != nil {
		t.Fatalf("NewKnowledgeReference() error = %v", err)
	}
	return reference
}

func mustPresentation(t *testing.T, text string) Presentation {
	t.Helper()
	presentation, err := NewUntrustedReferencePresentation(text)
	if err != nil {
		t.Fatalf("NewUntrustedReferencePresentation() error = %v", err)
	}
	return presentation
}

func mustPacket(t *testing.T, receipt ReceiptIdentity, expiresAt time.Time, knowledge KnowledgeReference, presentation Presentation) Packet {
	t.Helper()
	packet, err := NewPacket(receipt, expiresAt, knowledge, presentation)
	if err != nil {
		t.Fatalf("NewPacket() error = %v", err)
	}
	return packet
}

func mustAbstainDecision(t *testing.T, receipt ReceiptIdentity, reason AbstentionReason) Decision {
	t.Helper()
	decision, err := NewAbstainDecision(receipt, reason)
	if err != nil {
		t.Fatalf("NewAbstainDecision() error = %v", err)
	}
	return decision
}

func testBytes(seed byte) [32]byte {
	var value [32]byte
	value[0] = seed
	return value
}
