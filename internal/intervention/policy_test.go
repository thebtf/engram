package intervention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/internal/taskmemory"
)

func TestSourceMemoryVersionCopiesMutableInput(t *testing.T) {
	deletedAt := interventionTestTime
	validFrom := interventionTestTime.Add(-time.Hour)
	validUntil := interventionTestTime.Add(time.Hour)
	supersededBy := int64(90)
	input := policySourceInput("first source", []string{"deploy", "cache"})
	input.DeletedAt = &deletedAt
	input.ValidFrom = &validFrom
	input.ValidUntil = &validUntil
	input.SupersededBy = &supersededBy
	input.SourceSessions = []string{"session-b", "session-a", "session-a"}
	source, err := NewSourceMemoryVersion(input)
	if err != nil {
		t.Fatal(err)
	}

	input.Content = "changed source"
	input.Tags[0] = "changed"
	input.SourceSessions[0] = "changed"
	deletedAt = deletedAt.Add(24 * time.Hour)
	validFrom = validFrom.Add(24 * time.Hour)
	validUntil = validUntil.Add(24 * time.Hour)
	supersededBy = 91

	if got := source.Content(); got != "first source" {
		t.Fatalf("Content() = %q", got)
	}
	if got, want := source.Tags(), []string{"deploy", "cache"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Tags() = %#v, want %#v", got, want)
	}
	if got, want := source.Scope().SourceSessions(), []string{"session-a", "session-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SourceSessions() = %#v, want %#v", got, want)
	}
	if got := source.DeletedAt(); got == nil || !got.Equal(interventionTestTime) {
		t.Fatalf("DeletedAt() = %#v", got)
	}
	if got := source.SupersededBy(); got == nil || *got != 90 {
		t.Fatalf("SupersededBy() = %#v", got)
	}
}

func TestDerivePolicyScopeUsesOccurrenceKeyAndCanonicalScope(t *testing.T) {
	source := mustPolicySource(t, policySourceInput("scope source", []string{"scope"}))
	sameScope := mustPolicySource(t, policySourceInputWithSessions("scope source", []string{"scope"}, []string{"source-session", "source-session"}))
	changedScopeInput := policySourceInput("scope source", []string{"scope"})
	changedScopeInput.Domain = "different-domain"
	changedScope := mustPolicySource(t, changedScopeInput)

	epochOne := mustKeyEpoch(t, 31, 32, 33, 34, 35)
	epochSameOccurrence := mustKeyEpoch(t, 36, 37, 33, 38, 39)
	epochDifferentOccurrence := mustKeyEpoch(t, 40, 41, 42, 43, 44)

	one, err := epochOne.DerivePolicyScope(source.Scope())
	if err != nil {
		t.Fatal(err)
	}
	sameOccurrence, err := epochSameOccurrence.DerivePolicyScope(source.Scope())
	if err != nil {
		t.Fatal(err)
	}
	if one != sameOccurrence {
		t.Fatal("non-occurrence key material changed the policy scope commitment")
	}
	changedOccurrence, err := epochDifferentOccurrence.DerivePolicyScope(source.Scope())
	if err != nil {
		t.Fatal(err)
	}
	if one == changedOccurrence {
		t.Fatal("occurrence key material did not change the policy scope commitment")
	}
	canonicalized, err := epochOne.DerivePolicyScope(sameScope.Scope())
	if err != nil {
		t.Fatal(err)
	}
	if one != canonicalized {
		t.Fatal("source session order or duplication changed the canonical policy scope")
	}
	different, err := epochOne.DerivePolicyScope(changedScope.Scope())
	if err != nil {
		t.Fatal(err)
	}
	if one == different {
		t.Fatal("scope field drift did not change the policy scope commitment")
	}
}

func TestCorrectedSourceVersionGetsIndependentPolicy(t *testing.T) {
	originalInput := policySourceInput("cache original", []string{"cache"})
	originalInput.Version = 1
	successorID := int64(71)
	originalInput.SupersededBy = &successorID
	correctedInput := policySourceInput("cache corrected", []string{"cache"})
	correctedInput.Version = 2
	original := mustPolicySource(t, originalInput)
	corrected := mustPolicySource(t, correctedInput)
	if original.SourceFingerprint() == corrected.SourceFingerprint() {
		t.Fatal("corrected source version shared its source fingerprint")
	}

	epoch := fixtureKeyEpoch(t)
	originalScope, err := epoch.DerivePolicyScope(original.Scope())
	if err != nil {
		t.Fatal(err)
	}
	correctedScope, err := epoch.DerivePolicyScope(corrected.Scope())
	if err != nil {
		t.Fatal(err)
	}
	if originalScope != correctedScope {
		t.Fatal("currentness-only correction data changed scope identity")
	}
	compiler := mustPolicyCompiler(t, nil)
	originalPolicy, err := compiler.Compile(original)
	if err != nil {
		t.Fatal(err)
	}
	correctedPolicy, err := compiler.Compile(corrected)
	if err != nil {
		t.Fatal(err)
	}
	originalRecord := originalPolicy.PersistenceRecord()
	correctedRecord := correctedPolicy.PersistenceRecord()
	if originalRecord.PolicyID == correctedRecord.PolicyID || originalRecord.PolicyVersion == correctedRecord.PolicyVersion || originalRecord.CreatedFromEvent == correctedRecord.CreatedFromEvent {
		t.Fatal("corrected source version reused immutable policy identity or provenance")
	}
}

func TestPolicyCompilerFallsBackToLiteralTokens(t *testing.T) {
	policy, err := mustPolicyCompiler(t, nil).Compile(mustPolicySource(t, policySourceInput("Build cache now", []string{"generated:tag", "not valid tag"})))
	if err != nil {
		t.Fatal(err)
	}
	var descriptor canonicalPolicyDescriptor
	if !decodeExactJSON(policy.PersistenceRecord().DescriptorJSON, &descriptor) {
		t.Fatal("fallback descriptor was not canonical JSON")
	}
	if got, want := descriptor.Any, []canonicalPolicyAtom{{Kind: "keyword", Value: "build"}, {Kind: "keyword", Value: "cache"}, {Kind: "keyword", Value: "now"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback atoms = %#v, want %#v", got, want)
	}
}

func TestPolicyCompilerBuildsCanonicalValidDescriptor(t *testing.T) {
	literal := "deploy cache safely"
	source := mustPolicySource(t, policySourceInput("  "+literal+"  \r\nignored", []string{"Deploy", "CACHE", "ignored:namespace", "deploy"}))
	compiler := mustPolicyCompiler(t, nil)
	first, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !first.CanCommit() {
		t.Fatal("compiler output was not authorable")
	}
	firstRecord := first.PersistenceRecord()
	secondRecord := second.PersistenceRecord()
	if firstRecord.PolicyID != secondRecord.PolicyID || firstRecord.PolicyVersion != secondRecord.PolicyVersion {
		t.Fatal("compiler commitment drifted for identical source and semantic versions")
	}
	if firstRecord.DescriptorStatus != PolicyDescriptorValid || firstRecord.DescriptorOrigin != PolicyDescriptorOriginDeterministic {
		t.Fatalf("descriptor metadata = %#v", firstRecord)
	}
	var descriptor canonicalPolicyDescriptor
	if !decodeExactJSON(firstRecord.DescriptorJSON, &descriptor) {
		t.Fatalf("descriptor is not canonical typed JSON: %s", firstRecord.DescriptorJSON)
	}
	if !validCanonicalPolicyDescriptor(descriptor) {
		t.Fatalf("descriptor failed static validation: %#v", descriptor)
	}
	if got, want := descriptor.Any, []canonicalPolicyAtom{{Kind: "keyword", Value: "cache"}, {Kind: "keyword", Value: "deploy"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor any = %#v, want %#v", got, want)
	}
	if descriptor.SourceSpan.StartByte != 2 || descriptor.SourceSpan.EndByte != 2+len(literal) {
		t.Fatalf("source span = %#v", descriptor.SourceSpan)
	}
	digest := sha256.Sum256([]byte(source.Content()))
	if got, want := descriptor.SourceSpan.SourceDigest, hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("source digest = %q, want %q", got, want)
	}
	if strings.Contains(string(firstRecord.DescriptorJSON), "ignored") {
		t.Fatalf("descriptor retained an unselected source line: %s", firstRecord.DescriptorJSON)
	}
}

func TestPolicyCompilerPreservesOriginalLineForms(t *testing.T) {
	literal := "café"
	contents := []string{
		"  " + literal + "  \nnext",
		"  " + literal + "  \r\nnext",
		"  " + literal + "  \rnext",
	}
	compiler := mustPolicyCompiler(t, nil)
	var policyIDs [][32]byte
	var sourceDigests []string
	for _, content := range contents {
		source := mustPolicySource(t, policySourceInput(content, []string{"cafe"}))
		policy, err := compiler.Compile(source)
		if err != nil {
			t.Fatal(err)
		}
		record := policy.PersistenceRecord()
		var descriptor canonicalPolicyDescriptor
		if !decodeExactJSON(record.DescriptorJSON, &descriptor) {
			t.Fatalf("descriptor is not typed JSON: %s", record.DescriptorJSON)
		}
		if descriptor.SourceSpan.StartByte != 2 || descriptor.SourceSpan.EndByte != 2+len(literal) {
			t.Fatalf("source span = %#v", descriptor.SourceSpan)
		}
		policyIDs = append(policyIDs, record.PolicyID)
		sourceDigests = append(sourceDigests, descriptor.SourceSpan.SourceDigest)
	}
	for index := 1; index < len(policyIDs); index++ {
		if policyIDs[0] == policyIDs[index] || sourceDigests[0] == sourceDigests[index] {
			t.Fatal("distinct original line endings shared a policy or source digest")
		}
	}
}

func TestPolicyCompilerSkipsLeadingBlankLines(t *testing.T) {
	content := "\r\n   \n  inspect cache safely  \rnext"
	policy, err := mustPolicyCompiler(t, nil).Compile(mustPolicySource(t, policySourceInput(content, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var descriptor canonicalPolicyDescriptor
	if !decodeExactJSON(policy.PersistenceRecord().DescriptorJSON, &descriptor) {
		t.Fatalf("descriptor is not typed JSON: %s", policy.PersistenceRecord().DescriptorJSON)
	}
	wantStart := strings.Index(content, "inspect cache safely")
	if descriptor.SourceSpan.StartByte != wantStart || descriptor.SourceSpan.EndByte != wantStart+len("inspect cache safely") {
		t.Fatalf("source span = %#v, want [%d,%d)", descriptor.SourceSpan, wantStart, wantStart+len("inspect cache safely"))
	}
}

func TestPolicyCompilerProducesClosedInsufficientDescriptors(t *testing.T) {
	redactionRules, err := redaction.CompileRules([]redaction.Rule{{ID: "blocked", Pattern: "blocked", Replacement: "[redacted]"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		content string
		rules   []redaction.CompiledRule
		want    PolicyInsufficiencyReason
	}{
		{name: "oversized", content: strings.Repeat("a", maxPolicySourceBytes+1), want: PolicyInsufficiencySourceOversized},
		{name: "line oversized", content: strings.Repeat("a", MaxPolicyLiteralBytes+1), want: PolicyInsufficiencyLineOversized},
		{name: "secret", content: "api_key=" + strings.Repeat("a", 36), want: PolicyInsufficiencyContainsSecret},
		{name: "redaction", content: "blocked safe text", rules: redactionRules, want: PolicyInsufficiencyRedactionMatch},
		{name: "empty line", content: "   ", want: PolicyInsufficiencyNoSafeLine},
		{name: "control", content: "safe\x00text", want: PolicyInsufficiencyControlCharacter},
		{name: "decomposed", content: "cafe\u0301", want: PolicyInsufficiencyNonNFC},
		{name: "no atom", content: "!!!", want: PolicyInsufficiencyNoAtom},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			policy, err := mustPolicyCompiler(t, testCase.rules).Compile(mustPolicySource(t, policySourceInput(testCase.content, nil)))
			if err != nil {
				t.Fatal(err)
			}
			record := policy.PersistenceRecord()
			if record.DescriptorStatus != PolicyDescriptorInsufficient || !policy.CanCommit() {
				t.Fatalf("policy = %#v", record)
			}
			var descriptor canonicalInsufficientPolicyDescriptor
			if !decodeExactJSON(record.DescriptorJSON, &descriptor) || descriptor.InsufficiencyReason != testCase.want {
				t.Fatalf("insufficient descriptor = %s, want %q", record.DescriptorJSON, testCase.want)
			}
			if strings.Contains(string(record.DescriptorJSON), testCase.content) {
				t.Fatalf("insufficient descriptor leaked source content: %s", record.DescriptorJSON)
			}
		})
	}
}

func TestPolicyDefinitionRestoreIsNeverAuthorable(t *testing.T) {
	policy, err := mustPolicyCompiler(t, nil).Compile(mustPolicySource(t, policySourceInput("restore source", []string{"restore"})))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreUnverifiedPolicy(policy.PersistenceRecord())
	if err != nil {
		t.Fatal(err)
	}
	if restored.CanCommit() {
		t.Fatal("restored policy gained compiler authoring authority")
	}
	tampered := policy.PersistenceRecord()
	tampered.DescriptorJSON[0] ^= 0x80
	if _, err := RestoreUnverifiedPolicy(tampered); err == nil {
		t.Fatal("tampered policy record restored")
	}
}

func TestCandidatePolicyExposesNoRawPolicyData(t *testing.T) {
	ref, err := taskmemory.NewAuthorizedCandidateRef(12, 3, taskmemory.CandidateExact)
	if err != nil {
		t.Fatal(err)
	}
	scope := mustPolicySource(t, policySourceInput("candidate scope", []string{"candidate"})).Scope()
	candidate, err := NewCandidatePolicy(ref, CandidatePolicyValid, Digest(testBytes(51)), Digest(testBytes(52)), Digest(testBytes(53)), scope)
	if err != nil {
		t.Fatal(err)
	}
	candidateType := reflect.TypeOf(candidate)
	for _, forbidden := range []string{"Content", "Descriptor", "DescriptorJSON", "PolicyID"} {
		if _, found := candidateType.MethodByName(forbidden); found {
			t.Fatalf("CandidatePolicy exposes forbidden %s method", forbidden)
		}
	}
	if rendered := fmt.Sprintf("%#v", candidate); strings.Contains(rendered, "raw source body") {
		t.Fatalf("CandidatePolicy rendered raw source data: %s", rendered)
	}
}

func TestPolicyReconcilerClampsAndClassifiesResults(t *testing.T) {
	sources := []SourceMemoryVersion{
		mustPolicySource(t, policySourceInput("first", []string{"first"})),
		mustPolicySource(t, policySourceInput("second", []string{"second"})),
		mustPolicySource(t, policySourceInput("!!!", nil)),
	}
	repository := &recordingPolicyRepository{sources: sources}
	reconciler, err := NewPolicyReconciler(PolicyReconcilerConfig{
		Repository: repository,
		Compiler:   mustPolicyCompiler(t, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Reconcile(context.Background(), maxPolicyReconcileLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	if repository.limit != maxPolicyReconcileLimit || !repository.versions.Valid() {
		t.Fatalf("repository call = limit %d, versions %#v", repository.limit, repository.versions)
	}
	if want := (ReconcileResult{Scanned: 3, Inserted: 1, Existing: 1, Insufficient: 1, Stale: 1}); result != want {
		t.Fatalf("Reconcile() = %#v, want %#v", result, want)
	}
	if len(repository.committed) != 3 || !repository.committed[0].CanCommit() || !repository.committed[1].CanCommit() || !repository.committed[2].CanCommit() {
		t.Fatalf("reconciler passed non-authorable definitions: %#v", repository.committed)
	}
}

func policySourceInput(content string, tags []string) SourceMemoryInput {
	return policySourceInputWithSessions(content, tags, []string{"source-session"})
}

func policySourceInputWithSessions(content string, tags, sessions []string) SourceMemoryInput {
	return SourceMemoryInput{
		ID:                  71,
		Version:             2,
		CanonicalProject:    "00000000-0000-4000-8000-000000000071",
		Content:             content,
		Tags:                append([]string(nil), tags...),
		Status:              "active",
		PrivacyScope:        "project",
		SourceWorkstationID: "workstation-71",
		SourceSessions:      append([]string(nil), sessions...),
		OwnerPrincipal:      "agent-71",
		OwnerPrincipalKind:  "agent",
		AgentVisibility:     "shared",
		Domain:              "policy-test",
	}
}

func mustPolicySource(t *testing.T, input SourceMemoryInput) SourceMemoryVersion {
	t.Helper()
	source, err := NewSourceMemoryVersion(input)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func mustPolicyCompiler(t *testing.T, rules []redaction.CompiledRule) *PolicyCompiler {
	t.Helper()
	compiler, err := NewPolicyCompiler(PolicyCompilerConfig{
		KeyProvider:    NewStaticKeyProvider(fixtureKeyEpoch(t)),
		RedactionRules: rules,
	})
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

func mustKeyEpoch(t *testing.T, epoch, channel, occurrence, content, receipt byte) KeyEpoch {
	t.Helper()
	value, err := NewKeyEpoch(testBytes(epoch), testBytes(channel), testBytes(occurrence), testBytes(content), testBytes(receipt))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type recordingPolicyRepository struct {
	sources   []SourceMemoryVersion
	versions  PolicySemanticVersions
	limit     int
	committed []PolicyDefinition
}

func (r *recordingPolicyRepository) ListCompileSources(_ context.Context, versions PolicySemanticVersions, limit int) ([]SourceMemoryVersion, error) {
	r.versions = versions
	r.limit = limit
	return append([]SourceMemoryVersion(nil), r.sources...), nil
}

func (r *recordingPolicyRepository) CommitPolicy(_ context.Context, definition PolicyDefinition) (PolicyDefinition, bool, error) {
	r.committed = append(r.committed, definition)
	switch len(r.committed) {
	case 1:
		return PolicyDefinition{}, true, nil
	case 2:
		winner, err := RestoreUnverifiedPolicy(definition.PersistenceRecord())
		return winner, false, err
	default:
		return PolicyDefinition{}, false, nil
	}
}
