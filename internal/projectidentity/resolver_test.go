package projectidentity

import (
	"context"
	"errors"
	"testing"
)

const resolverTestProjectKeyV3 = "aa0f4c76-f04c-4cd6-ae41-10294a4ab57d"

type resolverStoreV3Fake struct {
	anchor              AnchorBindingV3
	admin               AnchorBindingV3
	lookupCalls         int
	lookupAuthorization VerifiedAuthorizationV3
	registerCalls       int
	adminCalls          int
	creates             int
	registration        AnchorRegistrationV3
	adminAuthorization  VerifiedAuthorizationV3
	attempts            []ResolutionAttemptV3
	recordAttemptErr    error
}

func (store *resolverStoreV3Fake) LookupAnchorBindingV3(_ context.Context, authorization VerifiedAuthorizationV3, _ string) (AnchorBindingV3, error) {
	store.lookupCalls++
	store.lookupAuthorization = authorization
	return store.anchor, nil
}

func (store *resolverStoreV3Fake) RegisterAnchorBindingAndRecordAttemptV3(_ context.Context, registration AnchorRegistrationV3, buildAttempt RegistrationAttemptBuilderV3) error {
	store.registerCalls++
	store.registration = registration
	previousAnchor, previousCreates := store.anchor, store.creates
	if store.anchor.State == AnchorBindingMissingV3 {
		store.creates++
		store.anchor = AnchorBindingV3{
			State:      AnchorBindingActiveV3,
			ProjectKey: resolverTestProjectKeyV3,
			Scope:      string(registration.Scope()),
		}
	}
	attempt, err := buildAttempt(store.anchor)
	if err == nil {
		err = store.RecordResolutionAttemptV3(context.Background(), attempt)
	}
	if err != nil {
		store.anchor, store.creates = previousAnchor, previousCreates
	}
	return err
}

func (store *resolverStoreV3Fake) LookupAdministrativeTargetV3(_ context.Context, authorization VerifiedAuthorizationV3) (AnchorBindingV3, error) {
	store.adminCalls++
	store.adminAuthorization = authorization
	return store.admin, nil
}

func (store *resolverStoreV3Fake) RecordResolutionAttemptV3(_ context.Context, attempt ResolutionAttemptV3) error {
	if store.recordAttemptErr != nil {
		return store.recordAttemptErr
	}
	store.attempts = append(store.attempts, attempt)
	return nil
}

type resolverVerifierV3Fake struct {
	authorized  bool
	adminTarget ProjectKeyV3
	calls       int
	lastRequest AuthorizationVerificationRequestV3
}

func (verifier *resolverVerifierV3Fake) VerifyAuthorizationV3(_ context.Context, request AuthorizationVerificationRequestV3) (AuthorizationVerificationV3, error) {
	verifier.calls++
	verifier.lastRequest = request
	response := AuthorizationVerificationV3{Authorized: verifier.authorized}
	if request.Intent() == AdminTargetIntentV3 {
		response.AdministrativeTargetProjectKey = verifier.adminTarget
	}
	return response, nil
}

func TestResolveProjectV3EveryIntent(t *testing.T) {
	for _, test := range []struct {
		name      string
		intent    ResolutionIntentV3
		configure func(*ResolveProjectRequestV3)
		assert    func(*testing.T, *resolverStoreV3Fake, ResolveProjectResultV3)
	}{
		{
			name:   "resolve existing",
			intent: ResolveExistingIntentV3,
			assert: func(t *testing.T, store *resolverStoreV3Fake, result ResolveProjectResultV3) {
				if store.lookupCalls != 1 || !store.lookupAuthorization.PermitsAnchorLookup() || store.registerCalls != 0 || store.adminCalls != 0 {
					t.Fatalf("resolve_existing port calls = lookup:%d authorization=%#v register:%d admin:%d", store.lookupCalls, store.lookupAuthorization, store.registerCalls, store.adminCalls)
				}
				assertFirstMutationFenceV3(t, result)
			},
		},
		{
			name:   "register anchor",
			intent: RegisterAnchorIntentV3,
			configure: func(request *ResolveProjectRequestV3) {
				request.RegistrationAuthorization = resolverAuthorizationV3(t)
			},
			assert: func(t *testing.T, store *resolverStoreV3Fake, result ResolveProjectResultV3) {
				if store.lookupCalls != 0 || store.registerCalls != 1 || store.adminCalls != 0 || store.creates != 1 {
					t.Fatalf("register_anchor port calls = lookup:%d register:%d admin:%d creates:%d", store.lookupCalls, store.registerCalls, store.adminCalls, store.creates)
				}
				if !store.registration.IsCausallyBoundFirstMutation() || store.registration.CausalFirstMutationFence().Correlation() != result.Resolution().Correlation() {
					t.Fatalf("registration was not bound to the successful resolution: %#v", store.registration)
				}
				assertFirstMutationFenceV3(t, result)
			},
		},
		{
			name:   "read filter",
			intent: ReadFilterIntentV3,
			configure: func(request *ResolveProjectRequestV3) {
				requirement, err := NewReadFilterRequirementV3(resolverAuthorizationV3(t), request.Correlation)
				if err != nil {
					t.Fatalf("new read-filter requirement: %v", err)
				}
				request.ReadFilter = &requirement
			},
			assert: func(t *testing.T, store *resolverStoreV3Fake, result ResolveProjectResultV3) {
				if store.lookupCalls != 1 || !store.lookupAuthorization.PermitsAnchorLookup() || store.registerCalls != 0 || store.adminCalls != 0 {
					t.Fatalf("read_filter port calls = lookup:%d authorization=%#v register:%d admin:%d", store.lookupCalls, store.lookupAuthorization, store.registerCalls, store.adminCalls)
				}
				if result.Resolution().RedirectReference() != "" {
					t.Fatalf("read_filter redirected: %#v", result.Resolution())
				}
				if _, ok := result.FirstMutationFence(); ok || result.Resolution().PermitsScopedMutation() {
					t.Fatalf("read_filter received mutation authority: %#v", result)
				}
			},
		},
		{
			name:   "admin target",
			intent: AdminTargetIntentV3,
			configure: func(request *ResolveProjectRequestV3) {
				audit, err := NewAdminAuditV3("admin-17", "repair-17", "approved-17", "retain-17")
				if err != nil {
					t.Fatalf("new admin audit: %v", err)
				}
				target, err := NewAdministrativeTargetReferenceV3("opaque-admin-target-17")
				if err != nil {
					t.Fatalf("new administrative target: %v", err)
				}
				requirement, err := NewAdminTargetRequirementV3(resolverAuthorizationV3(t), request.Correlation, target, audit)
				if err != nil {
					t.Fatalf("new admin-target requirement: %v", err)
				}
				request.AdminTarget = &requirement
			},
			assert: func(t *testing.T, store *resolverStoreV3Fake, result ResolveProjectResultV3) {
				if store.lookupCalls != 0 || store.registerCalls != 0 || store.adminCalls != 1 {
					t.Fatalf("admin_target port calls = lookup:%d register:%d admin:%d", store.lookupCalls, store.registerCalls, store.adminCalls)
				}
				projectKey, ok := store.adminAuthorization.AdministrativeTargetProjectKey()
				if !ok || projectKey != result.Resolution().CanonicalProjectKey() {
					t.Fatalf("admin target did not receive a verified server target: %#v", store.adminAuthorization)
				}
				assertFirstMutationFenceV3(t, result)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &resolverStoreV3Fake{anchor: resolverActiveBindingV3(), admin: resolverActiveBindingV3()}
			if test.intent == RegisterAnchorIntentV3 {
				store.anchor = AnchorBindingV3{State: AnchorBindingMissingV3}
			}
			request := resolverRequestV3(test.intent)
			if test.configure != nil {
				test.configure(&request)
			}
			result, err := resolverV3(t, store).ResolveProjectV3(context.Background(), request)
			assertResolvedV3(t, result, err)
			test.assert(t, store, result)
			assertResolutionAttemptV3(t, store, result.Resolution())
		})
	}
}

func TestResolveProjectV3RegistrationIsIdempotent(t *testing.T) {
	store := &resolverStoreV3Fake{anchor: AnchorBindingV3{State: AnchorBindingMissingV3}}
	resolver := resolverV3(t, store)
	request := resolverRequestV3(RegisterAnchorIntentV3)
	request.RegistrationAuthorization = resolverAuthorizationV3(t)

	first, err := resolver.ResolveProjectV3(context.Background(), request)
	assertResolvedV3(t, first, err)
	second, err := resolver.ResolveProjectV3(context.Background(), request)
	assertResolvedV3(t, second, err)

	if first.Resolution().CanonicalProjectKey() != second.Resolution().CanonicalProjectKey() || store.creates != 1 || store.registerCalls != 2 {
		t.Fatalf("registration was not idempotent: first=%q second=%q creates=%d registrations=%d", first.Resolution().CanonicalProjectKey(), second.Resolution().CanonicalProjectKey(), store.creates, store.registerCalls)
	}
	if len(store.attempts) != 2 {
		t.Fatalf("registration attempts = %d, want 2", len(store.attempts))
	}
}

func TestResolveProjectV3RegistrationAuditFailureDoesNotCommitBinding(t *testing.T) {
	store := &resolverStoreV3Fake{
		anchor:           AnchorBindingV3{State: AnchorBindingMissingV3},
		recordAttemptErr: errors.New("forced resolution attempt failure"),
	}
	request := resolverRequestV3(RegisterAnchorIntentV3)
	request.RegistrationAuthorization = resolverAuthorizationV3(t)

	result, err := resolverV3(t, store).ResolveProjectV3(context.Background(), request)
	if !errors.Is(err, errResolverStorageV3) || result.Resolution().CanonicalProjectKey() != "" {
		t.Fatalf("registration audit failure result = %#v, %v", result, err)
	}
	if store.creates != 0 || store.anchor.State != AnchorBindingMissingV3 || len(store.attempts) != 0 {
		t.Fatalf("failed audit committed binding or attempt: %#v", store)
	}
}

func TestResolveProjectV3UnboundAndCollisionRefuseWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		binding AnchorBindingV3
		outcome ResolutionOutcomeV3
	}{
		{name: "unbound", binding: AnchorBindingV3{State: AnchorBindingMissingV3}, outcome: ProjectOnboardingRequiredOutcomeV3},
		{name: "collision", binding: AnchorBindingV3{State: AnchorBindingDecisionRequiredV3}, outcome: ProjectAnchorDecisionRequiredOutcomeV3},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &resolverStoreV3Fake{anchor: test.binding}
			result, err := resolverV3(t, store).ResolveProjectV3(context.Background(), resolverRequestV3(ResolveExistingIntentV3))
			assertRefusalV3(t, result, err, test.outcome)
			if store.lookupCalls != 1 || store.registerCalls != 0 || store.adminCalls != 0 || store.creates != 0 {
				t.Fatalf("refusal mutated or used another port: %#v", store)
			}
			assertResolutionAttemptV3(t, store, result.Resolution())
		})
	}
}

func TestResolveProjectV3DescriptorAndExceptionRefusalsDoNotReachStore(t *testing.T) {
	for _, test := range []struct {
		name    string
		request func() ResolveProjectRequestV3
		outcome ResolutionOutcomeV3
	}{
		{
			name: "descriptor scope mismatch",
			request: func() ResolveProjectRequestV3 {
				request := resolverRequestV3(ResolveExistingIntentV3)
				request.Descriptor.Scope = "directory"
				return request
			},
			outcome: ProjectScopeMismatchOutcomeV3,
		},
		{
			name: "descriptor client evidence invalid",
			request: func() ResolveProjectRequestV3 {
				request := resolverRequestV3(ResolveExistingIntentV3)
				request.Descriptor.ClientInstanceID = "client / private"
				return request
			},
			outcome: ProjectDescriptorInvalidOutcomeV3,
		},
		{
			name: "descriptor drive-colon client evidence invalid",
			request: func() ResolveProjectRequestV3 {
				request := resolverRequestV3(ResolveExistingIntentV3)
				request.Descriptor.ClientInstanceID = "C:private"
				return request
			},
			outcome: ProjectDescriptorInvalidOutcomeV3,
		},
		{
			name: "descriptor http-scheme client evidence invalid",
			request: func() ResolveProjectRequestV3 {
				request := resolverRequestV3(ResolveExistingIntentV3)
				request.Descriptor.ClientInstanceID = "http:private"
				return request
			},
			outcome: ProjectDescriptorInvalidOutcomeV3,
		},
		{
			name: "descriptor ssh-scheme client evidence invalid",
			request: func() ResolveProjectRequestV3 {
				request := resolverRequestV3(ResolveExistingIntentV3)
				request.Descriptor.ClientInstanceID = "ssh:private"
				return request
			},
			outcome: ProjectDescriptorInvalidOutcomeV3,
		},
		{
			name: "read filter lacks authorization correlation",
			request: func() ResolveProjectRequestV3 {
				return resolverRequestV3(ReadFilterIntentV3)
			},
			outcome: ProjectDescriptorInvalidOutcomeV3,
		},
		{
			name: "admin target lacks audit boundary",
			request: func() ResolveProjectRequestV3 {
				return resolverRequestV3(AdminTargetIntentV3)
			},
			outcome: ProjectDescriptorInvalidOutcomeV3,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &resolverStoreV3Fake{anchor: resolverActiveBindingV3(), admin: resolverActiveBindingV3()}
			result, err := resolverV3(t, store).ResolveProjectV3(context.Background(), test.request())
			assertRefusalV3(t, result, err, test.outcome)
			if store.lookupCalls != 0 || store.registerCalls != 0 || store.adminCalls != 0 || store.creates != 0 {
				t.Fatalf("precondition refusal reached a non-audit persistence port: %#v", store)
			}
			assertResolutionAttemptV3(t, store, result.Resolution())
		})
	}
}

func TestResolveProjectV3OpaqueAuthorizationCannotAuthorize(t *testing.T) {
	registerRequest := func(t *testing.T) ResolveProjectRequestV3 {
		t.Helper()
		request := resolverRequestV3(RegisterAnchorIntentV3)
		request.RegistrationAuthorization = resolverAuthorizationV3(t)
		return request
	}
	t.Run("nil verifier registration", func(t *testing.T) {
		store := &resolverStoreV3Fake{anchor: AnchorBindingV3{State: AnchorBindingMissingV3}}
		result, err := NewResolverV3(store, nil).ResolveProjectV3(context.Background(), registerRequest(t))
		assertRefusalV3(t, result, err, ProjectDescriptorInvalidOutcomeV3)
		if store.registerCalls != 0 || store.creates != 0 {
			t.Fatalf("opaque caller authorization created a binding: %#v", store)
		}
		assertResolutionAttemptV3(t, store, result.Resolution())
	})
	t.Run("unverified server response", func(t *testing.T) {
		store := &resolverStoreV3Fake{anchor: AnchorBindingV3{State: AnchorBindingMissingV3}}
		result, err := NewResolverV3(store, &resolverVerifierV3Fake{}).ResolveProjectV3(context.Background(), registerRequest(t))
		assertRefusalV3(t, result, err, ProjectDescriptorInvalidOutcomeV3)
		if store.registerCalls != 0 || store.creates != 0 {
			t.Fatalf("unverified authorization created a binding: %#v", store)
		}
		assertResolutionAttemptV3(t, store, result.Resolution())
	})
	t.Run("nil verifier read filter", func(t *testing.T) {
		store := &resolverStoreV3Fake{anchor: resolverActiveBindingV3()}
		request := resolverRequestV3(ReadFilterIntentV3)
		requirement, err := NewReadFilterRequirementV3(resolverAuthorizationV3(t), request.Correlation)
		if err != nil {
			t.Fatalf("new read filter requirement: %v", err)
		}
		request.ReadFilter = &requirement

		result, err := NewResolverV3(store, nil).ResolveProjectV3(context.Background(), request)
		assertRefusalV3(t, result, err, ProjectDescriptorInvalidOutcomeV3)
		if store.lookupCalls != 0 || store.registerCalls != 0 || store.adminCalls != 0 {
			t.Fatalf("opaque read filter reached persistence: %#v", store)
		}
		assertResolutionAttemptV3(t, store, result.Resolution())
	})
	t.Run("unverified server read filter", func(t *testing.T) {
		store := &resolverStoreV3Fake{anchor: resolverActiveBindingV3()}
		verifier := &resolverVerifierV3Fake{}
		request := resolverRequestV3(ReadFilterIntentV3)
		requirement, err := NewReadFilterRequirementV3(resolverAuthorizationV3(t), request.Correlation)
		if err != nil {
			t.Fatalf("new read filter requirement: %v", err)
		}
		request.ReadFilter = &requirement

		result, err := NewResolverV3(store, verifier).ResolveProjectV3(context.Background(), request)
		assertRefusalV3(t, result, err, ProjectDescriptorInvalidOutcomeV3)
		if verifier.calls != 1 || store.lookupCalls != 0 || store.registerCalls != 0 || store.adminCalls != 0 {
			t.Fatalf("unverified read filter reached persistence: verifier=%d store=%#v", verifier.calls, store)
		}
		assertResolutionAttemptV3(t, store, result.Resolution())
	})
	t.Run("nil verifier administrative target", func(t *testing.T) {
		store := &resolverStoreV3Fake{admin: resolverActiveBindingV3()}
		request := resolverRequestV3(AdminTargetIntentV3)
		audit, err := NewAdminAuditV3("admin-17", "repair-17", "approved-17", "retain-17")
		if err != nil {
			t.Fatalf("new admin audit: %v", err)
		}
		target, err := NewAdministrativeTargetReferenceV3("opaque-admin-target-17")
		if err != nil {
			t.Fatalf("new administrative target: %v", err)
		}
		requirement, err := NewAdminTargetRequirementV3(resolverAuthorizationV3(t), request.Correlation, target, audit)
		if err != nil {
			t.Fatalf("new admin target requirement: %v", err)
		}
		request.AdminTarget = &requirement

		result, err := NewResolverV3(store, nil).ResolveProjectV3(context.Background(), request)
		assertRefusalV3(t, result, err, ProjectDescriptorInvalidOutcomeV3)
		if store.adminCalls != 0 {
			t.Fatalf("opaque caller target reached persistence: %#v", store)
		}
		assertResolutionAttemptV3(t, store, result.Resolution())
	})
}

func TestResolveProjectV3ResolveExistingRejectsUnverifiedBeforeLookup(t *testing.T) {
	store := &resolverStoreV3Fake{anchor: resolverActiveBindingV3()}
	verifier := &resolverVerifierV3Fake{}

	result, err := NewResolverV3(store, verifier).ResolveProjectV3(context.Background(), resolverRequestV3(ResolveExistingIntentV3))
	assertRefusalV3(t, result, err, ProjectDescriptorInvalidOutcomeV3)
	if verifier.calls != 1 || store.lookupCalls != 0 || store.registerCalls != 0 || store.adminCalls != 0 {
		t.Fatalf("unverified resolve_existing reached persistence: verifier=%d store=%#v", verifier.calls, store)
	}
	assertResolutionAttemptV3(t, store, result.Resolution())
}

func TestResolveProjectV3MergedAnchorRedirectsAndRecordsAttempt(t *testing.T) {
	store := &resolverStoreV3Fake{anchor: AnchorBindingV3{
		State:             AnchorBindingRedirectedV3,
		ProjectKey:        resolverTestProjectKeyV3,
		Scope:             "repository",
		RedirectReference: "merge-audit-17",
	}}
	result, err := resolverV3(t, store).ResolveProjectV3(context.Background(), resolverRequestV3(ResolveExistingIntentV3))
	if err != nil || result.Resolution().Outcome() != ProjectRedirectedOutcomeV3 || result.Resolution().CanonicalProjectKey() != resolverTestProjectKeyV3 || result.Resolution().RedirectReference() != "merge-audit-17" {
		t.Fatalf("merged anchor result = %#v, %v", result.Resolution(), err)
	}
	assertFirstMutationFenceV3(t, result)
	assertResolutionAttemptV3(t, store, result.Resolution())
}

func TestResolveProjectV3ReadFilterRefusesMergedAnchor(t *testing.T) {
	binding := resolverActiveBindingV3()
	binding.State = AnchorBindingRedirectedV3
	binding.RedirectReference = "merge-audit-filter-17"
	store := &resolverStoreV3Fake{anchor: binding}
	request := resolverRequestV3(ReadFilterIntentV3)
	filter, err := NewReadFilterRequirementV3(resolverAuthorizationV3(t), request.Correlation)
	if err != nil {
		t.Fatalf("new read-filter requirement: %v", err)
	}
	request.ReadFilter = &filter

	result, err := resolverV3(t, store).ResolveProjectV3(context.Background(), request)
	assertRefusalV3(t, result, err, ProjectAnchorDecisionRequiredOutcomeV3)
	if store.lookupCalls != 1 || store.registerCalls != 0 || store.adminCalls != 0 {
		t.Fatalf("read_filter redirect port calls = lookup:%d register:%d admin:%d", store.lookupCalls, store.registerCalls, store.adminCalls)
	}
	assertResolutionAttemptV3(t, store, result.Resolution())
}

func resolverV3(t *testing.T, store ProjectResolutionStoreV3) ResolverV3 {
	t.Helper()
	projectKey, err := NewProjectKeyV3(resolverTestProjectKeyV3)
	if err != nil {
		t.Fatalf("new resolver test project key: %v", err)
	}
	return NewResolverV3(store, &resolverVerifierV3Fake{authorized: true, adminTarget: projectKey})
}

func assertResolutionAttemptV3(t *testing.T, store *resolverStoreV3Fake, resolution ResolutionResultV3) {
	t.Helper()
	if len(store.attempts) != 1 {
		t.Fatalf("resolution attempts = %#v, want exactly one", store.attempts)
	}
	attempt := store.attempts[0]
	if !attempt.Valid() || attempt.Correlation() != resolution.Correlation() || attempt.Intent() != resolution.Intent() || attempt.Outcome() != resolution.Outcome() || attempt.Provenance() != "anchor_v3" || attempt.DescriptorVersion() != 3 {
		t.Fatalf("redacted resolution attempt = %#v, resolution = %#v", attempt, resolution)
	}
}

func resolverRequestV3(intent ResolutionIntentV3) ResolveProjectRequestV3 {
	anchor := AnchorV3{
		Version:   3,
		ProjectID: "b42c9854-bf02-4d1b-867d-a2a096f9e4d0",
		Name:      "engram",
		Scope:     "repository",
	}
	descriptor, err := BuildDescriptorV3(anchor, []string{"github.com/thebtf/engram"}, nil, "client-17")
	if err != nil {
		panic(err)
	}
	correlation, err := NewCorrelationV3("resolution-17")
	if err != nil {
		panic(err)
	}
	request := ResolveProjectRequestV3{Intent: intent, Anchor: anchor, Descriptor: descriptor, Correlation: correlation}
	if intent == ResolveExistingIntentV3 {
		authorization, err := NewAuthorizationReferenceV3("resolve-existing-authorization-17")
		if err != nil {
			panic(err)
		}
		request.ResolveExistingAuthorization = authorization
	}
	return request
}

func resolverActiveBindingV3() AnchorBindingV3 {
	return AnchorBindingV3{State: AnchorBindingActiveV3, ProjectKey: resolverTestProjectKeyV3, Scope: "repository"}
}

func resolverAuthorizationV3(t *testing.T) AuthorizationReferenceV3 {
	t.Helper()
	authorization, err := NewAuthorizationReferenceV3("authorization-17")
	if err != nil {
		t.Fatalf("new authorization: %v", err)
	}
	return authorization
}

func assertResolvedV3(t *testing.T, result ResolveProjectResultV3, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	if result.Resolution().Outcome() != ProjectResolvedOutcomeV3 || result.Resolution().CanonicalProjectKey() == "" {
		t.Fatalf("resolution did not expose a success-only key: %#v", result.Resolution())
	}
}

func assertRefusalV3(t *testing.T, result ResolveProjectResultV3, err error, outcome ResolutionOutcomeV3) {
	t.Helper()
	var public ResolutionErrorV3
	if !errors.As(err, &public) || public.Outcome() != outcome {
		t.Fatalf("public refusal = %v, want %s", err, outcome)
	}
	if got := result.Resolution(); got.Outcome() != outcome || !got.IsRefusal() || got.CanonicalProjectKey() != "" || got.ResolvedScope() != "" || got.RedirectReference() != "" {
		t.Fatalf("refusal exposed authority: %#v", got)
	}
	if _, ok := result.FirstMutationFence(); ok {
		t.Fatalf("refusal exposed mutation fence: %#v", result)
	}
}

func assertFirstMutationFenceV3(t *testing.T, result ResolveProjectResultV3) {
	t.Helper()
	fence, ok := result.FirstMutationFence()
	if !ok || !fence.PermitsScopedMutation() || fence.Correlation() != result.Resolution().Correlation() || fence.CanonicalProjectKey() != result.Resolution().CanonicalProjectKey() {
		t.Fatalf("missing causal first-mutation fence: %#v", result)
	}
}
