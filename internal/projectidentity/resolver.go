package projectidentity

import (
	"context"
	"errors"
)

var errResolverStorageV3 = errors.New("V3 project resolver storage unavailable")

// AnchorBindingStateV3 is the storage projection of an anchor lookup. It is
// deliberately not a resolution outcome: only ResolverV3 maps it to public
// authority or a typed refusal.
type AnchorBindingStateV3 string

const (
	AnchorBindingMissingV3          AnchorBindingStateV3 = "missing"
	AnchorBindingActiveV3           AnchorBindingStateV3 = "active"
	AnchorBindingDecisionRequiredV3 AnchorBindingStateV3 = "decision_required"
)

// AnchorBindingV3 is a narrow persistence fact. ProjectKey is never returned
// to a caller except after ResolverV3 validates this record and builds a
// successful ResolutionResultV3.
type AnchorBindingV3 struct {
	State      AnchorBindingStateV3
	ProjectKey string
	Scope      string
}

// CausalFirstMutationFenceV3 binds a successful resolution to its correlation
// and canonical key. A project-scoped mutation port must require this fence so
// a result from another resolution attempt cannot authorize its first write.
type CausalFirstMutationFenceV3 struct {
	correlation CorrelationV3
	intent      ResolutionIntentV3
	projectKey  ProjectKeyV3
	valid       bool
}

// Correlation identifies the exact resolution attempt that produced this fence.
func (fence CausalFirstMutationFenceV3) Correlation() CorrelationV3 { return fence.correlation }

// CanonicalProjectKey is populated only after a successful resolution.
func (fence CausalFirstMutationFenceV3) CanonicalProjectKey() ProjectKeyV3 {
	return fence.projectKey
}

// PermitsScopedMutation reports whether the fence authorizes a first scoped
// mutation. Read filters deliberately never receive such a fence.
func (fence CausalFirstMutationFenceV3) PermitsScopedMutation() bool {
	return fence.valid && fence.projectKey != "" && fence.intent != ReadFilterIntentV3
}

func (fence CausalFirstMutationFenceV3) isRegistrationFence() bool {
	return fence.valid && fence.intent == RegisterAnchorIntentV3 && fence.projectKey == ""
}

// AnchorRegistrationV3 is the only persistence command that may create a V3
// anchor binding. Its fields are built only after descriptor validation and
// authorization by ResolverV3.
type AnchorRegistrationV3 struct {
	anchorProjectID string
	scope           ResolvedScopeV3
	authorization   AuthorizationReferenceV3
	fence           CausalFirstMutationFenceV3
}

func (command AnchorRegistrationV3) AnchorProjectID() string { return command.anchorProjectID }
func (command AnchorRegistrationV3) Scope() ResolvedScopeV3  { return command.scope }
func (command AnchorRegistrationV3) Authorization() AuthorizationReferenceV3 {
	return command.authorization
}

func (command AnchorRegistrationV3) CausalFirstMutationFence() CausalFirstMutationFenceV3 {
	return command.fence
}

func (command AnchorRegistrationV3) IsCausallyBoundFirstMutation() bool {
	return command.fence.isRegistrationFence()
}

// ProjectResolutionStoreV3 is the complete V3 persistence boundary. The
// lookup methods are read-only. RegisterAnchorBindingV3 is the sole mutation
// port and accepts only the resolver-created first-mutation command.
type ProjectResolutionStoreV3 interface {
	LookupAnchorBindingV3(context.Context, string) (AnchorBindingV3, error)
	RegisterAnchorBindingV3(context.Context, AnchorRegistrationV3) (AnchorBindingV3, error)
	LookupAdministrativeTargetV3(context.Context, AdministrativeTargetReferenceV3) (AnchorBindingV3, error)
}

// ResolveProjectRequestV3 contains all inputs accepted by central V3
// resolution. It intentionally has no client-selected project key or generic
// project selector.
type ResolveProjectRequestV3 struct {
	Intent                    ResolutionIntentV3
	Anchor                    AnchorV3
	Descriptor                DescriptorV3
	Correlation               CorrelationV3
	RegistrationAuthorization AuthorizationReferenceV3
	ReadFilter                *ReadFilterRequirementV3
	AdminTarget               *AdminTargetRequirementV3
}

// ResolveProjectResultV3 carries the typed response and, only when permitted,
// the causal fence required by the first project-scoped mutation.
type ResolveProjectResultV3 struct {
	resolution ResolutionResultV3
	fence      CausalFirstMutationFenceV3
}

func (result ResolveProjectResultV3) Resolution() ResolutionResultV3 { return result.resolution }

// FirstMutationFence returns no fence for refusals and the read-only filter.
func (result ResolveProjectResultV3) FirstMutationFence() (CausalFirstMutationFenceV3, bool) {
	if !result.fence.PermitsScopedMutation() {
		return CausalFirstMutationFenceV3{}, false
	}
	return result.fence, true
}

// ResolverV3 is the transport-independent V3 authority. It is the only layer
// that maps stored anchor facts to a canonical project key or authorizes a new
// anchor binding.
type ResolverV3 struct {
	store ProjectResolutionStoreV3
}

func NewResolverV3(store ProjectResolutionStoreV3) ResolverV3 { return ResolverV3{store: store} }

// ResolveProjectV3 validates every identity fact before accessing the
// project-resolution store. A refusal returns a typed ResolutionErrorV3 and a
// result without canonical or mutation authority.
func (resolver ResolverV3) ResolveProjectV3(ctx context.Context, request ResolveProjectRequestV3) (ResolveProjectResultV3, error) {
	correlation, err := NewCorrelationV3(string(request.Correlation))
	if err != nil {
		return ResolveProjectResultV3{}, err
	}
	request.Correlation = correlation

	if !request.Intent.Valid() {
		return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
	}
	if !validAnchorV3(request.Anchor) {
		return refusalV3(request, ProjectAnchorInvalidOutcomeV3)
	}
	if request.Descriptor.Version != 3 {
		return refusalV3(request, ProjectDescriptorUnsupportedOutcomeV3)
	}
	if !validDescriptorV3(request.Descriptor) {
		return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
	}
	if request.Descriptor.AnchorProjectID != request.Anchor.ProjectID || request.Descriptor.Name != request.Anchor.Name {
		return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
	}
	if request.Descriptor.Scope != request.Anchor.Scope {
		return refusalV3(request, ProjectScopeMismatchOutcomeV3)
	}

	switch request.Intent {
	case ResolveExistingIntentV3:
		if request.RegistrationAuthorization != "" || request.ReadFilter != nil || request.AdminTarget != nil {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		return resolver.resolveAnchorV3(ctx, request)
	case RegisterAnchorIntentV3:
		authorization, err := NewAuthorizationReferenceV3(string(request.RegistrationAuthorization))
		if err != nil || request.ReadFilter != nil || request.AdminTarget != nil {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		if resolver.store == nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		command := AnchorRegistrationV3{
			anchorProjectID: request.Anchor.ProjectID,
			scope:           resolvedScopeV3(request.Anchor.Scope),
			authorization:   authorization,
			fence: CausalFirstMutationFenceV3{
				correlation: request.Correlation,
				intent:      request.Intent,
				valid:       true,
			},
		}
		binding, err := resolver.store.RegisterAnchorBindingV3(ctx, command)
		if err != nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		return bindingResultV3(request, binding)
	case ReadFilterIntentV3:
		if request.RegistrationAuthorization != "" || request.AdminTarget != nil || !validReadFilterV3(request.ReadFilter, request.Correlation) {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		return resolver.resolveAnchorV3(ctx, request)
	case AdminTargetIntentV3:
		if request.RegistrationAuthorization != "" || request.ReadFilter != nil || !validAdminTargetV3(request.AdminTarget, request.Correlation) {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		if resolver.store == nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		binding, err := resolver.store.LookupAdministrativeTargetV3(ctx, request.AdminTarget.Target())
		if err != nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		return bindingResultV3(request, binding)
	default:
		return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
	}
}

func (resolver ResolverV3) resolveAnchorV3(ctx context.Context, request ResolveProjectRequestV3) (ResolveProjectResultV3, error) {
	if resolver.store == nil {
		return ResolveProjectResultV3{}, errResolverStorageV3
	}
	binding, err := resolver.store.LookupAnchorBindingV3(ctx, request.Anchor.ProjectID)
	if err != nil {
		return ResolveProjectResultV3{}, errResolverStorageV3
	}
	return bindingResultV3(request, binding)
}

func validDescriptorV3(descriptor DescriptorV3) bool {
	if descriptor.Version != 3 || !validUUID(descriptor.AnchorProjectID) || descriptor.Name == "" || descriptor.Scope == "" {
		return false
	}
	if _, err := NewCorrelationV3(descriptor.ClientInstanceID); err != nil {
		return false
	}
	for _, remote := range descriptor.NormalizedGitRemotes {
		if !validNormalizedRemote(remote) {
			return false
		}
	}
	for _, identifier := range descriptor.LegacyIdentifiers {
		if !validLegacyIdentifierSchemeV3(identifier.Scheme) || !validDescriptorEvidence(identifier.Value) || !validDescriptorEvidence(string(identifier.Provenance)) {
			return false
		}
	}
	return true
}

func validReadFilterV3(requirement *ReadFilterRequirementV3, correlation CorrelationV3) bool {
	if requirement == nil || requirement.Correlation() != correlation {
		return false
	}
	_, err := NewReadFilterRequirementV3(requirement.Authorization(), requirement.Correlation())
	return err == nil
}

func validAdminTargetV3(requirement *AdminTargetRequirementV3, correlation CorrelationV3) bool {
	if requirement == nil || requirement.Correlation() != correlation {
		return false
	}
	_, err := NewAdminTargetRequirementV3(requirement.Authorization(), requirement.Correlation(), requirement.Target(), requirement.audit)
	return err == nil
}

func bindingResultV3(request ResolveProjectRequestV3, binding AnchorBindingV3) (ResolveProjectResultV3, error) {
	switch binding.State {
	case AnchorBindingMissingV3:
		if request.Intent == AdminTargetIntentV3 {
			return refusalV3(request, ProjectIdentityAmbiguousOutcomeV3)
		}
		return refusalV3(request, ProjectOnboardingRequiredOutcomeV3)
	case AnchorBindingDecisionRequiredV3:
		return refusalV3(request, ProjectAnchorDecisionRequiredOutcomeV3)
	case AnchorBindingActiveV3:
		projectKey, err := NewProjectKeyV3(binding.ProjectKey)
		if err != nil {
			return refusalV3(request, ProjectAnchorDecisionRequiredOutcomeV3)
		}
		scope := resolvedScopeV3(binding.Scope)
		if scope == "" || scope != resolvedScopeV3(request.Anchor.Scope) {
			return refusalV3(request, ProjectScopeMismatchOutcomeV3)
		}
		result, err := NewSuccessResultV3(request.Intent, ProjectResolvedOutcomeV3, projectKey, scope, "", request.Correlation)
		if err != nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		return ResolveProjectResultV3{
			resolution: result,
			fence: CausalFirstMutationFenceV3{
				correlation: request.Correlation,
				intent:      request.Intent,
				projectKey:  projectKey,
				valid:       true,
			},
		}, nil
	default:
		return refusalV3(request, ProjectAnchorDecisionRequiredOutcomeV3)
	}
}

func refusalV3(request ResolveProjectRequestV3, outcome ResolutionOutcomeV3) (ResolveProjectResultV3, error) {
	result, err := NewRefusalResultV3(request.Intent, outcome, request.Correlation)
	if err != nil {
		return ResolveProjectResultV3{}, errResolverStorageV3
	}
	public, err := NewResolutionErrorV3(outcome, request.Correlation)
	if err != nil {
		return ResolveProjectResultV3{}, errResolverStorageV3
	}
	return ResolveProjectResultV3{resolution: result}, public
}

func resolvedScopeV3(scope string) ResolvedScopeV3 {
	switch scope {
	case string(RepositoryResolvedScopeV3):
		return RepositoryResolvedScopeV3
	case string(DirectoryResolvedScopeV3):
		return DirectoryResolvedScopeV3
	default:
		return ""
	}
}
