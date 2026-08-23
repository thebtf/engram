package projectidentity

import (
	"context"
	"errors"
	"testing"
)

const resolverTestProjectKeyV3 = "aa0f4c76-f04c-4cd6-ae41-10294a4ab57d"

type resolverStoreV3Fake struct {
	anchor        AnchorBindingV3
	admin         AnchorBindingV3
	lookupCalls   int
	registerCalls int
	adminCalls    int
	creates       int
	registration  AnchorRegistrationV3
}

func (store *resolverStoreV3Fake) LookupAnchorBindingV3(_ context.Context, _ string) (AnchorBindingV3, error) {
	store.lookupCalls++
	return store.anchor, nil
}

func (store *resolverStoreV3Fake) RegisterAnchorBindingV3(_ context.Context, registration AnchorRegistrationV3) (AnchorBindingV3, error) {
	store.registerCalls++
	store.registration = registration
	if store.anchor.State == AnchorBindingMissingV3 {
		store.creates++
		store.anchor = AnchorBindingV3{
			State:      AnchorBindingActiveV3,
			ProjectKey: resolverTestProjectKeyV3,
			Scope:      string(registration.Scope()),
		}
	}
	return store.anchor, nil
}

func (store *resolverStoreV3Fake) LookupAdministrativeTargetV3(_ context.Context, _ AdministrativeTargetReferenceV3) (AnchorBindingV3, error) {
	store.adminCalls++
	return store.admin, nil
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
				if store.lookupCalls != 1 || store.registerCalls != 0 || store.adminCalls != 0 {
					t.Fatalf("resolve_existing port calls = lookup:%d register:%d admin:%d", store.lookupCalls, store.registerCalls, store.adminCalls)
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
				if store.lookupCalls != 1 || store.registerCalls != 0 || store.adminCalls != 0 {
					t.Fatalf("read_filter port calls = lookup:%d register:%d admin:%d", store.lookupCalls, store.registerCalls, store.adminCalls)
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
				target, err := NewAdministrativeTargetReferenceV3(resolverTestProjectKeyV3)
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
			result, err := NewResolverV3(store).ResolveProjectV3(context.Background(), request)
			assertResolvedV3(t, result, err)
			test.assert(t, store, result)
		})
	}
}

func TestResolveProjectV3RegistrationIsIdempotent(t *testing.T) {
	store := &resolverStoreV3Fake{anchor: AnchorBindingV3{State: AnchorBindingMissingV3}}
	resolver := NewResolverV3(store)
	request := resolverRequestV3(RegisterAnchorIntentV3)
	request.RegistrationAuthorization = resolverAuthorizationV3(t)

	first, err := resolver.ResolveProjectV3(context.Background(), request)
	assertResolvedV3(t, first, err)
	second, err := resolver.ResolveProjectV3(context.Background(), request)
	assertResolvedV3(t, second, err)

	if first.Resolution().CanonicalProjectKey() != second.Resolution().CanonicalProjectKey() || store.creates != 1 || store.registerCalls != 2 {
		t.Fatalf("registration was not idempotent: first=%q second=%q creates=%d registrations=%d", first.Resolution().CanonicalProjectKey(), second.Resolution().CanonicalProjectKey(), store.creates, store.registerCalls)
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
			result, err := NewResolverV3(store).ResolveProjectV3(context.Background(), resolverRequestV3(ResolveExistingIntentV3))
			assertRefusalV3(t, result, err, test.outcome)
			if store.lookupCalls != 1 || store.registerCalls != 0 || store.adminCalls != 0 || store.creates != 0 {
				t.Fatalf("refusal mutated or used another port: %#v", store)
			}
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
			result, err := NewResolverV3(store).ResolveProjectV3(context.Background(), test.request())
			assertRefusalV3(t, result, err, test.outcome)
			if store.lookupCalls != 0 || store.registerCalls != 0 || store.adminCalls != 0 || store.creates != 0 {
				t.Fatalf("precondition refusal reached persistence: %#v", store)
			}
		})
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
	return ResolveProjectRequestV3{Intent: intent, Anchor: anchor, Descriptor: descriptor, Correlation: correlation}
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
