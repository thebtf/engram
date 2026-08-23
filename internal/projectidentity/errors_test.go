package projectidentity

import (
	"strings"
	"testing"
)

func TestResolutionIntentV3Vocabulary(t *testing.T) {
	intents := []ResolutionIntentV3{
		ResolveExistingIntentV3,
		RegisterAnchorIntentV3,
		ReadFilterIntentV3,
		AdminTargetIntentV3,
	}
	for _, intent := range intents {
		if !intent.Valid() {
			t.Errorf("intent %q is not valid", intent)
		}
		if intent.MayEstablishBinding() != (intent == RegisterAnchorIntentV3) {
			t.Errorf("intent %q binding eligibility is wrong", intent)
		}
	}
	if ResolutionIntentV3("implicit_selector").Valid() {
		t.Fatal("undeclared intent was accepted")
	}
}

func TestResolutionOutcomeV3Vocabulary(t *testing.T) {
	outcomes := []ResolutionOutcomeV3{
		ProjectResolvedOutcomeV3,
		ProjectRedirectedOutcomeV3,
		ProjectOnboardingRequiredOutcomeV3,
		ProjectAnchorInvalidOutcomeV3,
		ProjectScopeMismatchOutcomeV3,
		ProjectNestedRepositoryUnresolvedOutcomeV3,
		ProjectAnchorDecisionRequiredOutcomeV3,
		ProjectIdentityAmbiguousOutcomeV3,
		ProjectDescriptorUnsupportedOutcomeV3,
		ProjectDescriptorInvalidOutcomeV3,
		ProjectKeyClientAssertionForbiddenOutcomeV3,
	}
	for _, outcome := range outcomes {
		if !outcome.Valid() {
			t.Errorf("outcome %q is not valid", outcome)
		}
		wantSuccess := outcome == ProjectResolvedOutcomeV3 || outcome == ProjectRedirectedOutcomeV3
		if outcome.IsSuccess() != wantSuccess || outcome.IsRefusal() == wantSuccess {
			t.Errorf("outcome %q success/refusal classification is wrong", outcome)
		}
	}
	if ResolutionOutcomeV3("PROJECT_IDENTITY_UNAVAILABLE").Valid() {
		t.Fatal("legacy outcome was accepted")
	}
}

func TestRefusalResultV3WithholdsCanonicalAuthority(t *testing.T) {
	correlation := mustCorrelationV3(t, "resolution-9d15")
	result, err := NewRefusalResultV3(ResolveExistingIntentV3, ProjectDescriptorInvalidOutcomeV3, correlation)
	if err != nil {
		t.Fatalf("new refusal result: %v", err)
	}
	if !result.IsRefusal() || result.PermitsScopedMutation() {
		t.Fatalf("refusal result has unsafe state: %#v", result)
	}
	if result.CanonicalProjectKey() != "" || result.ResolvedScope() != "" || result.RedirectReference() != "" {
		t.Fatalf("refusal exposed scoped authority: %#v", result)
	}

	rawCredential := "postgres://user:secret@db.example/private"
	rawPath := `C:\private\workspace\anchor.json`
	rawDatabaseError := "pq: password authentication failed"
	public, err := NewResolutionErrorV3(ProjectDescriptorInvalidOutcomeV3, correlation)
	if err != nil {
		t.Fatalf("new refusal error: %v", err)
	}
	if got, want := public.Error(), "PROJECT_DESCRIPTOR_INVALID"; got != want {
		t.Fatalf("public error = %q, want %q", got, want)
	}
	for _, raw := range []string{rawCredential, rawPath, rawDatabaseError, string(correlation)} {
		if strings.Contains(public.Error(), raw) {
			t.Errorf("public error leaked %q", raw)
		}
	}
	if _, err := NewCorrelationV3(rawCredential); err == nil {
		t.Fatal("credential-shaped correlation was accepted")
	}
	if _, err := NewCorrelationV3(rawPath); err == nil {
		t.Fatal("path-shaped correlation was accepted")
	}
}

func TestResolutionResultV3Invariants(t *testing.T) {
	key, err := NewProjectKeyV3("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("new project key: %v", err)
	}
	correlation := mustCorrelationV3(t, "resolution-a1")

	resolved, err := NewSuccessResultV3(ResolveExistingIntentV3, ProjectResolvedOutcomeV3, key, RepositoryResolvedScopeV3, "", correlation)
	if err != nil {
		t.Fatalf("new resolved result: %v", err)
	}
	if got := resolved.CanonicalProjectKey(); got != key || resolved.ResolvedScope() != RepositoryResolvedScopeV3 || resolved.RedirectReference() != "" || !resolved.PermitsScopedMutation() {
		t.Fatalf("resolved result invariants failed: %#v", resolved)
	}

	redirected, err := NewSuccessResultV3(ResolveExistingIntentV3, ProjectRedirectedOutcomeV3, key, DirectoryResolvedScopeV3, RedirectReferenceV3("merge-audit-9"), correlation)
	if err != nil {
		t.Fatalf("new redirected result: %v", err)
	}
	if redirected.RedirectReference() != "merge-audit-9" || !redirected.PermitsScopedMutation() {
		t.Fatalf("redirected result invariants failed: %#v", redirected)
	}

	readOnly, err := NewSuccessResultV3(ReadFilterIntentV3, ProjectResolvedOutcomeV3, key, RepositoryResolvedScopeV3, "", correlation)
	if err != nil {
		t.Fatalf("new read-filter result: %v", err)
	}
	if readOnly.PermitsScopedMutation() {
		t.Fatal("read filter permitted scoped mutation")
	}

	for _, test := range []struct {
		name     string
		intent   ResolutionIntentV3
		outcome  ResolutionOutcomeV3
		key      ProjectKeyV3
		scope    ResolvedScopeV3
		redirect RedirectReferenceV3
	}{
		{"refusal with key", ResolveExistingIntentV3, ProjectAnchorInvalidOutcomeV3, key, RepositoryResolvedScopeV3, ""},
		{"success without key", ResolveExistingIntentV3, ProjectResolvedOutcomeV3, "", RepositoryResolvedScopeV3, ""},
		{"success without scope", ResolveExistingIntentV3, ProjectResolvedOutcomeV3, key, "", ""},
		{"redirect without reference", ResolveExistingIntentV3, ProjectRedirectedOutcomeV3, key, RepositoryResolvedScopeV3, ""},
		{"resolved with redirect", ResolveExistingIntentV3, ProjectResolvedOutcomeV3, key, RepositoryResolvedScopeV3, "merge-audit-9"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSuccessResultV3(test.intent, test.outcome, test.key, test.scope, test.redirect, correlation); err == nil {
				t.Fatal("invalid result was accepted")
			}
		})
	}
}

func TestScopeExceptionRequirementsV3(t *testing.T) {
	authorization, err := NewAuthorizationReferenceV3("authorization-42")
	if err != nil {
		t.Fatalf("new authorization: %v", err)
	}
	correlation := mustCorrelationV3(t, "audit-42")

	filter, err := NewReadFilterRequirementV3(authorization, correlation)
	if err != nil {
		t.Fatalf("new read filter requirement: %v", err)
	}
	if filter.AllowsScopedMutation() || filter.Authorization() != authorization || filter.Correlation() != correlation {
		t.Fatalf("read filter requirement is unsafe: %#v", filter)
	}
	if _, err := NewReadFilterRequirementV3("", correlation); err == nil {
		t.Fatal("read filter accepted no authorization")
	}

	target, err := NewAdministrativeTargetReferenceV3("admin-target-42")
	if err != nil {
		t.Fatalf("new admin target: %v", err)
	}
	audit, err := NewAdminAuditV3("actor-42", "retention-review", "approved", "rollback-boundary-42")
	if err != nil {
		t.Fatalf("new admin audit: %v", err)
	}
	admin, err := NewAdminTargetRequirementV3(authorization, correlation, target, audit)
	if err != nil {
		t.Fatalf("new admin requirement: %v", err)
	}
	if !admin.AllowsScopedMutation() || admin.Target() != target || admin.Authorization() != authorization || admin.Correlation() != correlation {
		t.Fatalf("admin requirement lost its authorization or audit boundary: %#v", admin)
	}
	if _, err := NewAdminAuditV3("actor-42", "retention-review", "approved", ""); err == nil {
		t.Fatal("admin audit accepted no retention or rollback boundary")
	}
	if _, err := NewAdministrativeTargetReferenceV3("C:/private/project"); err == nil {
		t.Fatal("admin target accepted raw path")
	}
}

func mustCorrelationV3(t *testing.T, value string) CorrelationV3 {
	t.Helper()
	correlation, err := NewCorrelationV3(value)
	if err != nil {
		t.Fatalf("new correlation %q: %v", value, err)
	}
	return correlation
}
