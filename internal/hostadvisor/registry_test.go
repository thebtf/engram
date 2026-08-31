package hostadvisor

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewOMPAdvisor1ProfileOwnsExactCapability(t *testing.T) {
	profile := fixtureProfile(t)
	if profile.CapabilityRevision != ompAdvisor1CapabilityRevision {
		t.Fatalf("capability revision = %q, want %q", profile.CapabilityRevision, ompAdvisor1CapabilityRevision)
	}
	if profile.Protocol != (ProtocolRange{Min: 1, Max: 1}) || profile.HostFamily != HostFamilyOMP || profile.Evidence.ArtifactKind != ArtifactKindInstalled {
		t.Fatalf("profile identity = %#v", profile)
	}
	if len(profile.Capabilities) != 1 {
		t.Fatalf("capabilities = %#v", profile.Capabilities)
	}
	capability := profile.Capabilities[0]
	if capability.Semantic != SemanticBeforeAgentStart || len(capability.Actions) != 2 || capability.Actions[0] != ActionEmitAdvice || capability.Actions[1] != ActionAdapterAttestation || len(capability.InjectionModes) != 1 || capability.InjectionModes[0] != InjectionModeHiddenUntrustedMessage {
		t.Fatalf("capability = %#v", capability)
	}
	if !capability.Correlation.Session || !capability.Correlation.Turn || capability.Correlation.ToolAction || !capability.Correlation.StablePhaseAnchor || !capability.Callback.Awaited || capability.Callback.Ordering != CallbackOrderingBeforeFirstAction || capability.Acknowledgement != AcknowledgementAdapterAttested {
		t.Fatalf("capability guarantees = %#v", capability)
	}

	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	registry := fixtureRegistry(t, []AcceptedProfile{profile}, 2, &clock, bindingIDs("binding-one"))
	profile.HostVersion = "mutated-host"
	profile.Capabilities[0].Actions[0] = ActionRewrite
	binding, err := registry.Bind(fixtureSubject(t, 1), fixtureHello(fixtureProfile(t), "runtime-one"))
	if err != nil {
		t.Fatalf("Bind after source profile mutation: %v", err)
	}
	if got := binding.Snapshot().Capabilities()[0].Actions[0]; got != ActionEmitAdvice {
		t.Fatalf("registry retained caller-mutated capability %v", got)
	}
}

func TestRegistryRejectsOverlappingAcceptedProfiles(t *testing.T) {
	profile := fixtureProfile(t)
	_, err := NewRegistry(RegistryConfig{Profiles: []AcceptedProfile{profile, profile}, MaxBindings: 2})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("overlapping catalog error = %v, want invalid input", err)
	}
}

func TestRegistryRejectsCallerDefinedCapabilityContract(t *testing.T) {
	profile := fixtureProfile(t)
	profile.Capabilities = cloneCapabilities(profile.Capabilities)
	profile.Capabilities[0].Actions = []Action{ActionEmitAdvice, ActionBlock}
	_, err := NewRegistry(RegistryConfig{Profiles: []AcceptedProfile{profile}, MaxBindings: 2})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("caller-defined capability error = %v, want invalid input", err)
	}
}

func TestNewOMPAdvisor1ProfileRejectsIncompleteTimingAndEvidence(t *testing.T) {
	valid := fixtureProfileSpec()
	for name, mutate := range map[string]func(*OMPAdvisor1ProfileSpec){
		"zero callback deadline":           func(spec *OMPAdvisor1ProfileSpec) { spec.CallbackDeadline = 0 },
		"submillisecond callback deadline": func(spec *OMPAdvisor1ProfileSpec) { spec.CallbackDeadline = time.Nanosecond },
		"zero binding ttl":                 func(spec *OMPAdvisor1ProfileSpec) { spec.BindingTTL = 0 },
		"zero artifact digest":             func(spec *OMPAdvisor1ProfileSpec) { spec.InstalledArtifactDigest = Digest{} },
		"empty snapshot id":                func(spec *OMPAdvisor1ProfileSpec) { spec.SnapshotID = "" },
		"zero snapshot revision":           func(spec *OMPAdvisor1ProfileSpec) { spec.SnapshotRevision = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			spec := valid
			mutate(&spec)
			_, err := NewOMPAdvisor1Profile(spec)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want invalid input", err)
			}
		})
	}
}

func TestRegistryExactRetryPreservesOriginalBindingAndExpiry(t *testing.T) {
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	profile := fixtureProfile(t)
	registry := fixtureRegistry(t, []AcceptedProfile{profile}, 2, &clock, bindingIDs("binding-one", "binding-two"))
	subject := fixtureSubject(t, 1)
	hello := fixtureHello(profile, "runtime-one")

	first, err := registry.Bind(subject, hello)
	if err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	clock = clock.Add(10 * time.Second)
	second, err := registry.Bind(subject, hello)
	if err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if first.ID() != second.ID() || !first.ExpiresAt().Equal(second.ExpiresAt()) || first.Snapshot().ContractDigest() != second.Snapshot().ContractDigest() || first.CallbackDeadline() != second.CallbackDeadline() {
		t.Fatalf("exact retry changed binding: first=%#v second=%#v", first, second)
	}
}

func TestRegistryReplacesSameChannelOnValidMaterialChange(t *testing.T) {
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	base := fixtureProfile(t)
	nextSpec := fixtureProfileSpec()
	nextSpec.HostVersion = "omp-2.0.0"
	nextSpec.SnapshotID = "snapshot-two"
	nextSpec.SnapshotRevision = 2
	next := fixtureProfileFromSpec(t, nextSpec)
	registry := fixtureRegistry(t, []AcceptedProfile{base, next}, 2, &clock, bindingIDs("binding-one", "binding-two"))
	subject := fixtureSubject(t, 1)
	first, err := registry.Bind(subject, fixtureHello(base, "runtime-one"))
	if err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	second, err := registry.Bind(subject, fixtureHello(next, "runtime-one"))
	if err != nil {
		t.Fatalf("changed Bind: %v", err)
	}
	if first.ID() == second.ID() {
		t.Fatalf("material change reused binding ID %q", first.ID())
	}
	if _, err := registry.RequireActive(subject, first.ID()); !errors.Is(err, ErrBindingUnavailable) {
		t.Fatalf("old binding error = %v, want unavailable", err)
	}
	if active, err := registry.RequireActive(subject, second.ID()); err != nil || active.ID() != second.ID() {
		t.Fatalf("replacement active=%#v err=%v", active, err)
	}
}

func TestRegistryAllocationFailurePreservesCurrentBinding(t *testing.T) {
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	base := fixtureProfile(t)
	nextSpec := fixtureProfileSpec()
	nextSpec.HostVersion = "omp-2.0.0"
	nextSpec.SnapshotID = "snapshot-two"
	nextSpec.SnapshotRevision = 2
	next := fixtureProfileFromSpec(t, nextSpec)
	allocationCalls := 0
	registry := fixtureRegistry(t, []AcceptedProfile{base, next}, 2, &clock, func() (BindingID, error) {
		allocationCalls++
		if allocationCalls == 1 {
			return "binding-one", nil
		}
		return "", errors.New("binding allocation failed")
	})
	subject := fixtureSubject(t, 1)
	first, err := registry.Bind(subject, fixtureHello(base, "runtime-one"))
	if err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	if _, err := registry.Bind(subject, fixtureHello(next, "runtime-one")); err == nil {
		t.Fatal("material change unexpectedly succeeded after ID allocation failure")
	}
	if active, err := registry.RequireActive(subject, first.ID()); err != nil || active.ID() != first.ID() {
		t.Fatalf("allocation failure invalidated current binding: active=%#v err=%v", active, err)
	}
}

func TestRegistryProfileAndCapabilityMaterialChangesReplaceAtomically(t *testing.T) {
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	base := fixtureProfile(t)
	hostSpec := fixtureProfileSpec()
	hostSpec.HostVersion = "omp-2.0.0"
	hostSpec.SnapshotID = "snapshot-host-change"
	hostSpec.SnapshotRevision = 2
	hostChanged := fixtureProfileFromSpec(t, hostSpec)
	evidenceSpec := fixtureProfileSpec()
	evidenceSpec.InstalledArtifactDigest = fixtureDigest(4)
	evidenceSpec.RuntimeProbeReceiptID = "probe-receipt-two"
	evidenceSpec.SnapshotID = "snapshot-evidence-change"
	evidenceSpec.SnapshotRevision = 3
	evidenceChanged := fixtureProfileFromSpec(t, evidenceSpec)
	registry := fixtureRegistry(t, []AcceptedProfile{base, hostChanged, evidenceChanged}, 3, &clock, bindingIDs("binding-one", "binding-two", "binding-three", "binding-four"))
	subject := fixtureSubject(t, 1)

	first, err := registry.Bind(subject, fixtureHello(base, "runtime-one"))
	if err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	second, err := registry.Bind(subject, fixtureHello(hostChanged, "runtime-one"))
	if err != nil {
		t.Fatalf("host profile change: %v", err)
	}
	if _, err := registry.RequireActive(subject, first.ID()); !errors.Is(err, ErrBindingUnavailable) {
		t.Fatalf("host change did not invalidate old binding: %v", err)
	}
	third, err := registry.Bind(subject, fixtureHello(evidenceChanged, "runtime-one"))
	if err != nil {
		t.Fatalf("evidence profile change: %v", err)
	}
	if _, err := registry.RequireActive(subject, second.ID()); !errors.Is(err, ErrBindingUnavailable) {
		t.Fatalf("evidence change did not invalidate old binding: %v", err)
	}

	subsetHello := fixtureHello(evidenceChanged, "runtime-one")
	subsetHello.Requested[0].Actions = []Action{ActionEmitAdvice}
	fourth, err := registry.Bind(subject, subsetHello)
	if err != nil {
		t.Fatalf("capability subset change: %v", err)
	}
	if fourth.ID() == third.ID() {
		t.Fatal("capability material change reused old binding")
	}
	if got := fourth.Snapshot().Capabilities()[0].Actions; len(got) != 1 || got[0] != ActionEmitAdvice {
		t.Fatalf("server augmented capability subset: %#v", got)
	}
}

func TestRegistryRejectsNonCanonicalAndUnsupportedRequestsWithoutInvalidating(t *testing.T) {
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	profile := fixtureProfile(t)
	registry := fixtureRegistry(t, []AcceptedProfile{profile}, 2, &clock, bindingIDs("binding-one", "binding-two"))
	subject := fixtureSubject(t, 1)
	hello := fixtureHello(profile, "runtime-one")
	binding, err := registry.Bind(subject, hello)
	if err != nil {
		t.Fatalf("baseline Bind: %v", err)
	}

	nonCanonical := fixtureHello(profile, "runtime-one")
	nonCanonical.Requested[0].Actions = []Action{ActionAdapterAttestation, ActionEmitAdvice}
	if _, err := registry.Bind(subject, nonCanonical); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-canonical actions error = %v, want invalid", err)
	}

	unsupported := fixtureHello(profile, "runtime-one")
	unsupported.Requested[0].Actions = []Action{ActionEmitAdvice, ActionAllow}
	if _, err := registry.Bind(subject, unsupported); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("self-granted action error = %v, want unsupported", err)
	}
	if active, err := registry.RequireActive(subject, binding.ID()); err != nil || active.ID() != binding.ID() {
		t.Fatalf("rejected request invalidated active binding: active=%#v err=%v", active, err)
	}
}

func TestRegistryExpiresRejectsCapacityAndSeparatesChannels(t *testing.T) {
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	profile := fixtureProfile(t)
	profile.BindingTTL = time.Second
	registry := fixtureRegistry(t, []AcceptedProfile{profile}, 1, &clock, bindingIDs("binding-one", "binding-two"))
	subjectOne := fixtureSubject(t, 1)
	first, err := registry.Bind(subjectOne, fixtureHello(profile, "runtime-one"))
	if err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	if _, err := registry.Bind(subjectOne, fixtureHello(profile, "runtime-two")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error = %v, want capacity", err)
	}
	if active, err := registry.RequireActive(subjectOne, first.ID()); err != nil || active.ID() != first.ID() {
		t.Fatalf("capacity refusal invalidated current binding: active=%#v err=%v", active, err)
	}
	clock = clock.Add(time.Second)
	if _, err := registry.RequireActive(subjectOne, first.ID()); !errors.Is(err, ErrBindingUnavailable) {
		t.Fatalf("expired binding error = %v, want unavailable", err)
	}
	second, err := registry.Bind(subjectOne, fixtureHello(profile, "runtime-two"))
	if err != nil {
		t.Fatalf("Bind after expiry: %v", err)
	}
	if second.ID() != BindingID("binding-two") {
		t.Fatalf("new binding ID = %q", second.ID())
	}

	fresh := fixtureRegistry(t, []AcceptedProfile{profile}, 1, &clock, bindingIDs("restart-binding"))
	if _, err := fresh.RequireActive(subjectOne, second.ID()); !errors.Is(err, ErrBindingUnavailable) {
		t.Fatalf("server restart preserved binding: %v", err)
	}
}

func TestRegistryConcurrentRuntimeChannelsRemainIndependent(t *testing.T) {
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	profile := fixtureProfile(t)
	registry := fixtureRegistry(t, []AcceptedProfile{profile}, 32, &clock, nil)
	subject := fixtureSubject(t, 1)
	const channels = 16

	bindings := make(chan HostBinding, channels)
	errorsByWorker := make(chan error, channels)
	var group sync.WaitGroup
	group.Add(channels)
	for index := range channels {
		go func(index int) {
			defer group.Done()
			binding, err := registry.Bind(subject, fixtureHello(profile, fmt.Sprintf("runtime-%d", index)))
			if err != nil {
				errorsByWorker <- err
				return
			}
			bindings <- binding
		}(index)
	}
	group.Wait()
	close(bindings)
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Fatalf("concurrent Bind: %v", err)
	}
	seen := make(map[BindingID]struct{}, channels)
	for binding := range bindings {
		if _, exists := seen[binding.ID()]; exists {
			t.Fatalf("duplicate binding ID %q", binding.ID())
		}
		seen[binding.ID()] = struct{}{}
		if _, err := registry.RequireActive(subject, binding.ID()); err != nil {
			t.Fatalf("active concurrent binding: %v", err)
		}
	}
	if len(seen) != channels {
		t.Fatalf("bindings = %d, want %d", len(seen), channels)
	}
}

func TestRegistrySeparatesSubjectAndAdapterChannels(t *testing.T) {
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	profile := fixtureProfile(t)
	adapterSpec := fixtureProfileSpec()
	adapterSpec.AdapterID = "omp-adapter-two"
	adapterSpec.SnapshotID = "snapshot-adapter-two"
	adapterSpec.SnapshotRevision = 2
	adapterProfile := fixtureProfileFromSpec(t, adapterSpec)
	registry := fixtureRegistry(t, []AcceptedProfile{profile, adapterProfile}, 4, &clock, bindingIDs("binding-one", "binding-two", "binding-three"))

	one, err := registry.Bind(fixtureSubject(t, 1), fixtureHello(profile, "runtime-one"))
	if err != nil {
		t.Fatalf("first channel: %v", err)
	}
	two, err := registry.Bind(fixtureSubject(t, 2), fixtureHello(profile, "runtime-one"))
	if err != nil {
		t.Fatalf("subject channel: %v", err)
	}
	three, err := registry.Bind(fixtureSubject(t, 1), fixtureHello(adapterProfile, "runtime-one"))
	if err != nil {
		t.Fatalf("adapter channel: %v", err)
	}
	for _, test := range []struct {
		subject AuthenticatedSubject
		id      BindingID
	}{
		{fixtureSubject(t, 1), one.ID()},
		{fixtureSubject(t, 2), two.ID()},
		{fixtureSubject(t, 1), three.ID()},
	} {
		if _, err := registry.RequireActive(test.subject, test.id); err != nil {
			t.Fatalf("independent channel %q unavailable: %v", test.id, err)
		}
	}
	if _, err := registry.RequireActive(fixtureSubject(t, 2), one.ID()); !errors.Is(err, ErrBindingUnavailable) {
		t.Fatalf("cross-subject binding error = %v, want unavailable", err)
	}
}

func TestRegistryDigestsAreDeterministicAndOutputContainsNoRawMaterial(t *testing.T) {
	profile := fixtureProfile(t)
	subject := fixtureSubject(t, 1)
	hello := fixtureHello(profile, "runtime-one")
	normalized, err := normalizeHello(hello)
	if err != nil {
		t.Fatalf("normalize Hello: %v", err)
	}
	firstDigest := materialDigest(subject, profile, normalized)
	secondDigest := materialDigest(subject, profile, normalized)
	if firstDigest != secondDigest {
		t.Fatalf("material digest is not deterministic: %x != %x", firstDigest, secondDigest)
	}

	nonCanonical := hello
	nonCanonical.Requested = cloneCapabilities(hello.Requested)
	nonCanonical.Requested[0].Actions = []Action{ActionAdapterAttestation, ActionEmitAdvice}
	if _, err := normalizeHello(nonCanonical); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("set-order attack error = %v, want invalid", err)
	}

	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	registry := fixtureRegistry(t, []AcceptedProfile{profile}, 1, &clock, bindingIDs("binding-one"))
	binding, err := registry.Bind(subject, hello)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	rendered := fmt.Sprintf("%#v", binding)
	for _, forbidden := range []string{"runtime-one", profile.HostVersion, profile.AdapterID, profile.Evidence.RuntimeProbeReceiptID, "project", "credential"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("binding leaked raw material %q in %s", forbidden, rendered)
		}
	}
}

func fixtureProfileSpec() OMPAdvisor1ProfileSpec {
	return OMPAdvisor1ProfileSpec{
		HostVersion:             "omp-1.0.0",
		AdapterID:               "omp-adapter",
		AdapterVersion:          "adapter-1.0.0",
		InstalledArtifactDigest: fixtureDigest(1),
		RuntimeProbeReceiptID:   "probe-receipt-one",
		SnapshotID:              "snapshot-one",
		SnapshotRevision:        1,
		CallbackDeadline:        250 * time.Millisecond,
		BindingTTL:              time.Minute,
	}
}

func fixtureProfile(t *testing.T) AcceptedProfile {
	t.Helper()
	return fixtureProfileFromSpec(t, fixtureProfileSpec())
}

func fixtureProfileFromSpec(t *testing.T, spec OMPAdvisor1ProfileSpec) AcceptedProfile {
	t.Helper()
	profile, err := NewOMPAdvisor1Profile(spec)
	if err != nil {
		t.Fatalf("NewOMPAdvisor1Profile: %v", err)
	}
	return profile
}

func fixtureHello(profile AcceptedProfile, runtimeRef string) HostHello {
	return HostHello{
		Protocol: profile.Protocol,
		Host: HostIdentity{
			Family:             profile.HostFamily,
			HostVersion:        profile.HostVersion,
			AdapterID:          profile.AdapterID,
			AdapterVersion:     profile.AdapterVersion,
			RuntimeInstanceRef: runtimeRef,
		},
		Requested: cloneCapabilities(profile.Capabilities),
		Evidence:  profile.Evidence,
	}
}

func fixtureSubject(t *testing.T, value byte) AuthenticatedSubject {
	t.Helper()
	subject, err := NewAuthenticatedSubject(fixtureDigest(value))
	if err != nil {
		t.Fatalf("NewAuthenticatedSubject: %v", err)
	}
	return subject
}

func fixtureDigest(value byte) Digest {
	var digest Digest
	for index := range digest {
		digest[index] = value + byte(index)
	}
	return digest
}

func fixtureRegistry(t *testing.T, profiles []AcceptedProfile, maxBindings int, clock *time.Time, newBindingID func() (BindingID, error)) *Registry {
	t.Helper()
	registry, err := NewRegistry(RegistryConfig{
		Profiles:     profiles,
		MaxBindings:  maxBindings,
		Now:          func() time.Time { return *clock },
		NewBindingID: newBindingID,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return registry
}

func bindingIDs(ids ...BindingID) func() (BindingID, error) {
	index := 0
	return func() (BindingID, error) {
		if index == len(ids) {
			return "", errors.New("test binding IDs exhausted")
		}
		bindingID := ids[index]
		index++
		return bindingID, nil
	}
}
