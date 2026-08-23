package projectidentity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const behaviorMatrixProjectKeyV3 = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type resolverMatrixStoreV3 struct {
	bindings      map[string]AnchorBindingV3
	admin         AnchorBindingV3
	lookupIDs     []string
	registerCalls int
	adminCalls    int
	attempts      []ResolutionAttemptV3
}

func (store *resolverMatrixStoreV3) LookupAnchorBindingV3(_ context.Context, _ VerifiedAuthorizationV3, anchorProjectID string) (AnchorBindingV3, error) {
	store.lookupIDs = append(store.lookupIDs, anchorProjectID)
	if binding, ok := store.bindings[anchorProjectID]; ok {
		return binding, nil
	}
	return AnchorBindingV3{State: AnchorBindingMissingV3}, nil
}

func (store *resolverMatrixStoreV3) RegisterAnchorBindingAndRecordAttemptV3(_ context.Context, registration AnchorRegistrationV3, buildAttempt RegistrationAttemptBuilderV3) error {
	store.registerCalls++
	binding, ok := store.bindings[registration.AnchorProjectID()]
	if !ok || binding.State == AnchorBindingMissingV3 {
		binding = AnchorBindingV3{State: AnchorBindingActiveV3, ProjectKey: behaviorMatrixProjectKeyV3, Scope: string(registration.Scope())}
		store.bindings[registration.AnchorProjectID()] = binding
	}
	attempt, err := buildAttempt(binding)
	if err != nil {
		return err
	}
	return store.RecordResolutionAttemptV3(context.Background(), attempt)
}

func (store *resolverMatrixStoreV3) LookupAdministrativeTargetV3(_ context.Context, _ VerifiedAuthorizationV3) (AnchorBindingV3, error) {
	store.adminCalls++
	return store.admin, nil
}

func (store *resolverMatrixStoreV3) RecordResolutionAttemptV3(_ context.Context, attempt ResolutionAttemptV3) error {
	store.attempts = append(store.attempts, attempt)
	return nil
}

func TestResolveProjectV3TopologyBehaviorMatrix(t *testing.T) {
	corpus := loadProjectIdentityV3Corpus(t)
	primary := t.TempDir()
	primaryAnchor := corpus.vector(t, "repository-primary-resolves")
	if err := os.WriteFile(filepath.Join(primary, anchorFilenameV3), primaryAnchor.Input.Anchor, 0o600); err != nil {
		t.Fatalf("write primary anchor: %v", err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "matrix@example.test"},
		{"config", "user.name", "V3 Matrix"},
		{"add", anchorFilenameV3},
		{"commit", "-m", "add matrix anchor"},
		{"remote", "add", "origin", "https://git.example.test/Platform/Widget.git"},
	} {
		runGit(t, primary, args...)
	}

	store := &resolverMatrixStoreV3{bindings: map[string]AnchorBindingV3{
		"11111111-1111-4111-8111-111111111111": {State: AnchorBindingActiveV3, ProjectKey: behaviorMatrixProjectKeyV3, Scope: "repository"},
	}}
	resolver := matrixResolverV3(t, store)
	resolve := func(vectorID, root, remoteName string) {
		t.Helper()
		vector := corpus.vector(t, vectorID)
		anchor, err := DiscoverAnchorV3(root, "repository")
		if err != nil {
			t.Fatalf("%s: discover anchor: %v", vectorID, err)
		}
		assertProjectIdentityV3JSON(t, vectorID+" anchor", vector.Input.Anchor, anchor)
		remote, disposition, err := NormalizeGitRemoteV3(matrixGitOutput(t, root, "remote", "get-url", remoteName))
		if err != nil || disposition != RemoteNormalizedV3 {
			t.Fatalf("%s: remote = (%q, %q, %v)", vectorID, remote, disposition, err)
		}
		descriptor, err := BuildDescriptorV3(anchor, []string{remote}, nil, "matrix-"+vectorID)
		if err != nil {
			t.Fatalf("%s: build descriptor: %v", vectorID, err)
		}
		result, err := resolver.ResolveProjectV3(context.Background(), matrixResolveExistingRequestV3(t, anchor, descriptor, vectorID))
		if err != nil {
			t.Fatalf("%s: resolve: %v", vectorID, err)
		}
		if got := result.Resolution(); got.Outcome() != vector.Expected.Outcome || got.CanonicalProjectKey() != vector.Expected.CanonicalProjectKey || got.PermitsScopedMutation() != vector.Expected.ScopedMutationPermitted {
			t.Fatalf("%s: resolution = %#v, expected = %#v", vectorID, got, vector.Expected)
		}
		assertFirstMutationFenceV3(t, result)
	}

	resolve("repository-primary-resolves", primary, "origin")
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGit(t, primary, "worktree", "add", "-b", "matrix-worktree", worktree)
	resolve("repository-worktree-equivalence", worktree, "origin")

	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(clone), "clone", primary, clone)
	runGit(t, clone, "remote", "set-url", "origin", "https://git.example.test/Platform/Widget.git")
	resolve("repository-clone-equivalence", clone, "origin")

	runGit(t, primary, "checkout", "-b", "matrix-branch")
	resolve("repository-branch-equivalence", primary, "origin")

	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(clone, moved); err != nil {
		t.Fatalf("move clone: %v", err)
	}
	resolve("repository-moved-directory-equivalence", moved, "origin")

	runGit(t, primary, "remote", "rename", "origin", "upstream")
	resolve("repository-renamed-remote-equivalence", primary, "upstream")

	nested := filepath.Join(primary, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatalf("make nested repository: %v", err)
	}
	runGit(t, nested, "init")
	if _, err := DiscoverAnchorV3(nested, "repository"); err == nil {
		t.Fatal("nested repository inherited its parent anchor")
	}

	directoryParent := t.TempDir()
	if err := os.WriteFile(filepath.Join(directoryParent, anchorFilenameV3), []byte(`{"version":3,"project_id":"22222222-2222-4222-8222-222222222222","name":"directory","scope":"directory"}`), 0o600); err != nil {
		t.Fatalf("write directory parent anchor: %v", err)
	}
	directoryChild := filepath.Join(directoryParent, "child")
	if err := os.Mkdir(directoryChild, 0o700); err != nil {
		t.Fatalf("make directory child: %v", err)
	}
	if _, err := DiscoverAnchorV3(directoryChild, "directory"); err == nil {
		t.Fatal("directory discovery searched upward")
	}

	if store.registerCalls != 0 || len(store.lookupIDs) != 6 || len(store.attempts) != 6 {
		t.Fatalf("topology matrix crossed registration boundary: registers=%d lookups=%d attempts=%d", store.registerCalls, len(store.lookupIDs), len(store.attempts))
	}
}

func TestResolveProjectV3RefusalBehaviorMatrix(t *testing.T) {
	corpus := loadProjectIdentityV3Corpus(t)
	anchor, err := ParseAnchorV3(corpus.vector(t, "repository-primary-resolves").Input.Anchor)
	if err != nil {
		t.Fatalf("parse matrix anchor: %v", err)
	}

	for _, test := range []struct {
		name        string
		vectorID    string
		binding     AnchorBindingV3
		admin       bool
		mutate      func(*ResolveProjectRequestV3)
		lookupCalls int
		adminCalls  int
	}{
		{name: "missing binding", vectorID: "missing-anchor-requires-onboarding", lookupCalls: 1},
		{name: "unbound anchor", vectorID: "unbound-anchor-requires-onboarding", lookupCalls: 1},
		{name: "legacy anchor", vectorID: "legacy-name-only-anchor-is-invalid", mutate: func(request *ResolveProjectRequestV3) { request.Anchor = AnchorV3{Version: 3, Name: "legacy"} }},
		{name: "malformed anchor", vectorID: "anchor-malformed-project-id-is-invalid", mutate: func(request *ResolveProjectRequestV3) { request.Anchor.ProjectID = "not-a-uuid" }},
		{name: "copied anchor", vectorID: "copied-anchor-requires-decision", binding: AnchorBindingV3{State: AnchorBindingDecisionRequiredV3}, lookupCalls: 1},
		{name: "scope mismatch", vectorID: "descriptor-scope-mismatch-is-refused", mutate: func(request *ResolveProjectRequestV3) { request.Descriptor.Scope = "directory" }},
		{name: "unsupported descriptor", vectorID: "descriptor-version-unsupported", mutate: func(request *ResolveProjectRequestV3) { request.Descriptor.Version = 4 }},
		{name: "invalid client instance", vectorID: "descriptor-missing-client-instance-id-is-invalid", mutate: func(request *ResolveProjectRequestV3) { request.Descriptor.ClientInstanceID = "" }},
		{name: "credential remote", vectorID: "credential-bearing-remote-is-refused-without-raw-persistence", mutate: func(request *ResolveProjectRequestV3) {
			request.Descriptor.NormalizedGitRemotes = []string{"https://fixture-user:fixture-password@git.example.test/private/repo"}
		}},
		{name: "ambiguous target", vectorID: "read-filter-ambiguous-legacy-identifier-is-refused", admin: true, adminCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &resolverMatrixStoreV3{bindings: map[string]AnchorBindingV3{}, admin: AnchorBindingV3{State: AnchorBindingMissingV3}}
			if test.binding.State != "" {
				store.bindings[anchor.ProjectID] = test.binding
			}
			name := strings.ReplaceAll(test.name, " ", "-")
			request := matrixResolveExistingRequestV3(t, anchor, matrixDescriptorV3(t, anchor, "matrix-"+name), "refusal-"+name)
			if test.admin {
				request = matrixAdminTargetRequestV3(t, anchor, matrixDescriptorV3(t, anchor, "matrix-admin"))
			}
			if test.mutate != nil {
				test.mutate(&request)
			}

			result, err := matrixResolverV3(t, store).ResolveProjectV3(context.Background(), request)
			assertRefusalV3(t, result, err, corpus.vector(t, test.vectorID).Expected.Outcome)
			if store.registerCalls != 0 || len(store.lookupIDs) != test.lookupCalls || store.adminCalls != test.adminCalls || len(store.attempts) != 1 {
				t.Fatalf("refusal mutated or crossed an unexpected store boundary: registers=%d lookups=%d admin=%d attempts=%d", store.registerCalls, len(store.lookupIDs), store.adminCalls, len(store.attempts))
			}
			if test.name == "credential remote" && strings.Contains(strings.Join([]string{store.attempts[0].Provenance(), string(store.attempts[0].Outcome()), store.attempts[0].AnchorProjectID()}, "|"), "fixture-password") {
				t.Fatal("credential-bearing descriptor reached the redacted resolution attempt")
			}
		})
	}
}

func TestResolveProjectV3BehaviorMatrixRequiresExplicitRegistration(t *testing.T) {
	anchor, err := ParseAnchorV3(loadProjectIdentityV3Corpus(t).vector(t, "repository-primary-resolves").Input.Anchor)
	if err != nil {
		t.Fatalf("parse matrix anchor: %v", err)
	}
	store := &resolverMatrixStoreV3{bindings: map[string]AnchorBindingV3{}}
	resolver := matrixResolverV3(t, store)
	descriptor := matrixDescriptorV3(t, anchor, "matrix-registration")

	result, err := resolver.ResolveProjectV3(context.Background(), matrixResolveExistingRequestV3(t, anchor, descriptor, "normal"))
	assertRefusalV3(t, result, err, ProjectOnboardingRequiredOutcomeV3)
	if store.registerCalls != 0 {
		t.Fatalf("normal resolution established a binding: %d", store.registerCalls)
	}

	result, err = resolver.ResolveProjectV3(context.Background(), matrixRegistrationRequestV3(t, anchor, descriptor))
	assertResolvedV3(t, result, err)
	if store.registerCalls != 1 || store.bindings[anchor.ProjectID].ProjectKey != behaviorMatrixProjectKeyV3 {
		t.Fatalf("explicit registration did not create the sole binding: %#v", store)
	}
}

func TestProjectIdentityV3BehaviorMatrixComparisonClasses(t *testing.T) {
	for _, test := range []struct {
		name      string
		v3Outcome ResolutionOutcomeV3
		legacy    LegacyComparisonOutcomeV2
		want      ComparisonClassV3
	}{
		{name: "equal", v3Outcome: ProjectResolvedOutcomeV3, legacy: LegacyComparisonResolvedV2, want: ComparisonEqualV3},
		{name: "mismatch", v3Outcome: ProjectResolvedOutcomeV3, legacy: LegacyComparisonRefusalV2, want: ComparisonMismatchV3},
		{name: "refusal", v3Outcome: ProjectOnboardingRequiredOutcomeV3, legacy: LegacyComparisonResolvedV2, want: ComparisonRefusalV3},
		{name: "legacy unavailable", v3Outcome: ProjectResolvedOutcomeV3, legacy: LegacyComparisonUnavailableV2, want: ComparisonUnavailableV3},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := strings.ReplaceAll(test.name, " ", "-")
			correlation, err := NewCorrelationV3("matrix-comparison-" + name)
			if err != nil {
				t.Fatalf("new correlation: %v", err)
			}
			observation, err := NewComparisonObservationV3(
				comparisonTestFingerprint("matrix-idempotency-"+name),
				correlation,
				test.v3Outcome,
				test.legacy,
				"matrix-client-"+name,
				ComparisonTransportHTTPV3,
				ComparisonRepositoryScopeV3,
				ComparisonFreshV3,
				comparisonTestFingerprint("matrix-evidence-"+name),
			)
			if err != nil {
				t.Fatalf("new observation: %v", err)
			}
			receipt, err := RecordComparisonV3(context.Background(), &comparisonStoreSpy{}, observation)
			if err != nil || receipt.Classification != test.want {
				t.Fatalf("comparison = %#v, %v; want %s", receipt, err, test.want)
			}
		})
	}
}

func matrixResolverV3(t *testing.T, store ProjectResolutionStoreV3) ResolverV3 {
	t.Helper()
	projectKey, err := NewProjectKeyV3(behaviorMatrixProjectKeyV3)
	if err != nil {
		t.Fatalf("new matrix project key: %v", err)
	}
	return NewResolverV3(store, &resolverVerifierV3Fake{authorized: true, adminTarget: projectKey})
}

func matrixDescriptorV3(t *testing.T, anchor AnchorV3, clientInstanceID string) DescriptorV3 {
	t.Helper()
	descriptor, err := BuildDescriptorV3(anchor, nil, nil, clientInstanceID)
	if err != nil {
		t.Fatalf("build matrix descriptor: %v", err)
	}
	return descriptor
}

func matrixResolveExistingRequestV3(t *testing.T, anchor AnchorV3, descriptor DescriptorV3, suffix string) ResolveProjectRequestV3 {
	t.Helper()
	correlation, err := NewCorrelationV3("matrix-correlation-" + suffix)
	if err != nil {
		t.Fatalf("new correlation: %v", err)
	}
	authorization, err := NewAuthorizationReferenceV3("matrix-authorization-" + suffix)
	if err != nil {
		t.Fatalf("new authorization: %v", err)
	}
	return ResolveProjectRequestV3{Intent: ResolveExistingIntentV3, Anchor: anchor, Descriptor: descriptor, Correlation: correlation, ResolveExistingAuthorization: authorization}
}

func matrixRegistrationRequestV3(t *testing.T, anchor AnchorV3, descriptor DescriptorV3) ResolveProjectRequestV3 {
	t.Helper()
	correlation, err := NewCorrelationV3("matrix-registration-correlation")
	if err != nil {
		t.Fatalf("new registration correlation: %v", err)
	}
	authorization, err := NewAuthorizationReferenceV3("matrix-registration-authorization")
	if err != nil {
		t.Fatalf("new registration authorization: %v", err)
	}
	return ResolveProjectRequestV3{Intent: RegisterAnchorIntentV3, Anchor: anchor, Descriptor: descriptor, Correlation: correlation, RegistrationAuthorization: authorization}
}

func matrixAdminTargetRequestV3(t *testing.T, anchor AnchorV3, descriptor DescriptorV3) ResolveProjectRequestV3 {
	t.Helper()
	correlation, err := NewCorrelationV3("matrix-admin-correlation")
	if err != nil {
		t.Fatalf("new admin correlation: %v", err)
	}
	authorization, err := NewAuthorizationReferenceV3("matrix-admin-authorization")
	if err != nil {
		t.Fatalf("new admin authorization: %v", err)
	}
	target, err := NewAdministrativeTargetReferenceV3("matrix-admin-target")
	if err != nil {
		t.Fatalf("new admin target: %v", err)
	}
	audit, err := NewAdminAuditV3("matrix-admin", "matrix-purpose", "matrix-decision", "matrix-retain")
	if err != nil {
		t.Fatalf("new admin audit: %v", err)
	}
	requirement, err := NewAdminTargetRequirementV3(authorization, correlation, target, audit)
	if err != nil {
		t.Fatalf("new admin requirement: %v", err)
	}
	return ResolveProjectRequestV3{Intent: AdminTargetIntentV3, Anchor: anchor, Descriptor: descriptor, Correlation: correlation, AdminTarget: &requirement}
}

func matrixGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

var _ ProjectResolutionStoreV3 = (*resolverMatrixStoreV3)(nil)
