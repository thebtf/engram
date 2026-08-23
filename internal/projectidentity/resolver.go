package projectidentity

import (
	"context"
	"errors"
)

var (
	errResolverStorageV3                   = errors.New("V3 project resolver storage unavailable")
	ErrAuthorizationVerificationRequiredV3 = errors.New("V3 server authorization verification required")
)

// AnchorBindingStateV3 is the storage projection of an anchor lookup. It is
// deliberately not a resolution outcome: only ResolverV3 maps it to public
// authority or a typed refusal.
type AnchorBindingStateV3 string

const (
	AnchorBindingMissingV3          AnchorBindingStateV3 = "missing"
	AnchorBindingActiveV3           AnchorBindingStateV3 = "active"
	AnchorBindingRedirectedV3       AnchorBindingStateV3 = "redirected"
	AnchorBindingDecisionRequiredV3 AnchorBindingStateV3 = "decision_required"
)

// AnchorBindingV3 is a narrow persistence fact. ProjectKey is never returned
// to a caller except after ResolverV3 validates this record and builds a
// successful ResolutionResultV3.
type AnchorBindingV3 struct {
	State             AnchorBindingStateV3
	ProjectKey        string
	Scope             string
	RedirectReference string
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

// AuthorizationVerificationRequestV3 is the redacted input to the trusted
// server-side authorization port. Its opaque values are never persisted by the
// resolver store.
type AuthorizationVerificationRequestV3 struct {
	intent        ResolutionIntentV3
	authorization AuthorizationReferenceV3
	correlation   CorrelationV3
	target        AdministrativeTargetReferenceV3
	audit         AdminAuditV3
}

func (request AuthorizationVerificationRequestV3) Intent() ResolutionIntentV3 {
	return request.intent
}

func (request AuthorizationVerificationRequestV3) Authorization() AuthorizationReferenceV3 {
	return request.authorization
}

func (request AuthorizationVerificationRequestV3) Correlation() CorrelationV3 {
	return request.correlation
}

func (request AuthorizationVerificationRequestV3) AdministrativeTarget() AdministrativeTargetReferenceV3 {
	return request.target
}

func (request AuthorizationVerificationRequestV3) Audit() AdminAuditV3 { return request.audit }

// AuthorizationVerificationV3 is the server port's decision. An administrative
// decision resolves the opaque target reference to a server-issued key.
type AuthorizationVerificationV3 struct {
	Authorized                     bool
	AdministrativeTargetProjectKey ProjectKeyV3
}

// AuthorizationVerifierV3 verifies caller authority on the server before the
// resolver can create a binding or act on an administrative target.
type AuthorizationVerifierV3 interface {
	VerifyAuthorizationV3(context.Context, AuthorizationVerificationRequestV3) (AuthorizationVerificationV3, error)
}

// VerifiedAuthorizationV3 is an unforgeable resolver-created capability. Its
// zero value and any capability for another intent are rejected by persistence.
type VerifiedAuthorizationV3 struct {
	intent      ResolutionIntentV3
	correlation CorrelationV3
	projectKey  ProjectKeyV3
	valid       bool
}

func (authorization VerifiedAuthorizationV3) PermitsRegistration() bool {
	return authorization.valid && authorization.intent == RegisterAnchorIntentV3 && authorization.projectKey == ""
}

// PermitsAnchorLookup reports the verifier-created capability required by every anchor-binding lookup.
func (authorization VerifiedAuthorizationV3) PermitsAnchorLookup() bool {
	return authorization.valid && authorization.projectKey == "" && (authorization.intent == ResolveExistingIntentV3 || authorization.intent == ReadFilterIntentV3)
}

func (authorization VerifiedAuthorizationV3) AdministrativeTargetProjectKey() (ProjectKeyV3, bool) {
	if !authorization.valid || authorization.intent != AdminTargetIntentV3 || authorization.projectKey == "" {
		return "", false
	}
	return authorization.projectKey, true
}

// AnchorRegistrationV3 is the only persistence command that may create a V3
// anchor binding. Its fields are built only after descriptor validation and
// server-backed authorization by ResolverV3.
type AnchorRegistrationV3 struct {
	anchorProjectID string
	scope           ResolvedScopeV3
	verification    VerifiedAuthorizationV3
	fence           CausalFirstMutationFenceV3
}

func (command AnchorRegistrationV3) AnchorProjectID() string { return command.anchorProjectID }
func (command AnchorRegistrationV3) Scope() ResolvedScopeV3  { return command.scope }

func (command AnchorRegistrationV3) CausalFirstMutationFence() CausalFirstMutationFenceV3 {
	return command.fence
}

func (command AnchorRegistrationV3) IsCausallyBoundFirstMutation() bool {
	return command.fence.isRegistrationFence() && command.verification.PermitsRegistration()
}

// ResolutionAttemptV3 contains only the redacted outcome/provenance required
// by the V3 contract. It intentionally has no descriptor body or credentials.
type ResolutionAttemptV3 struct {
	correlation          CorrelationV3
	intent               ResolutionIntentV3
	outcome              ResolutionOutcomeV3
	anchorProjectID      string
	descriptorVersion    int
	provenance           string
	redirectReference    RedirectReferenceV3
	administrativeTarget AdministrativeTargetReferenceV3
	administrativeAudit  AdminAuditV3
}

func (attempt ResolutionAttemptV3) Correlation() CorrelationV3   { return attempt.correlation }
func (attempt ResolutionAttemptV3) Intent() ResolutionIntentV3   { return attempt.intent }
func (attempt ResolutionAttemptV3) Outcome() ResolutionOutcomeV3 { return attempt.outcome }
func (attempt ResolutionAttemptV3) AnchorProjectID() string      { return attempt.anchorProjectID }
func (attempt ResolutionAttemptV3) DescriptorVersion() int       { return attempt.descriptorVersion }
func (attempt ResolutionAttemptV3) Provenance() string           { return attempt.provenance }
func (attempt ResolutionAttemptV3) RedirectReference() RedirectReferenceV3 {
	return attempt.redirectReference
}

// AdministrativeTargetReference returns the redacted opaque target used only for an auditable administrative request.
func (attempt ResolutionAttemptV3) AdministrativeTargetReference() AdministrativeTargetReferenceV3 {
	return attempt.administrativeTarget
}

// AdministrativeAudit returns the redacted actor, purpose, decision, and retention boundary for an administrative request.
func (attempt ResolutionAttemptV3) AdministrativeAudit() AdminAuditV3 {
	return attempt.administrativeAudit
}

func (attempt ResolutionAttemptV3) Valid() bool {
	if _, err := NewCorrelationV3(string(attempt.correlation)); err != nil || !attempt.intent.Valid() || !attempt.outcome.Valid() || attempt.descriptorVersion < 0 || attempt.provenance != "anchor_v3" {
		return false
	}
	if attempt.anchorProjectID != "" && !validUUID(attempt.anchorProjectID) {
		return false
	}
	if attempt.outcome == ProjectRedirectedOutcomeV3 {
		if !validOpaqueReferenceV3(string(attempt.redirectReference)) {
			return false
		}
	} else if attempt.redirectReference != "" {
		return false
	}
	if attempt.intent != AdminTargetIntentV3 {
		return attempt.administrativeTarget == "" && attempt.administrativeAudit == (AdminAuditV3{})
	}
	if attempt.administrativeTarget == "" && attempt.administrativeAudit == (AdminAuditV3{}) {
		return true
	}
	return validOpaqueReferenceV3(string(attempt.administrativeTarget)) && attempt.administrativeAudit.valid()
}

// RegistrationAttemptBuilderV3 creates the durable result audit inside the
// registration transaction after the final binding state is known.
type RegistrationAttemptBuilderV3 func(AnchorBindingV3) (ResolutionAttemptV3, error)

// ProjectResolutionStoreV3 is the complete V3 persistence boundary. Lookup
// methods are read-only. Registration and its durable attempt share one
// transaction so a failed audit cannot commit a binding.
type ProjectResolutionStoreV3 interface {
	LookupAnchorBindingV3(context.Context, VerifiedAuthorizationV3, string) (AnchorBindingV3, error)
	RegisterAnchorBindingAndRecordAttemptV3(context.Context, AnchorRegistrationV3, RegistrationAttemptBuilderV3) error
	LookupAdministrativeTargetV3(context.Context, VerifiedAuthorizationV3) (AnchorBindingV3, error)
	RecordResolutionAttemptV3(context.Context, ResolutionAttemptV3) error
}

// ResolveProjectRequestV3 contains all inputs accepted by central V3
// resolution. It intentionally has no client-selected project key or generic
// project selector.
type ResolveProjectRequestV3 struct {
	Intent                       ResolutionIntentV3
	Anchor                       AnchorV3
	Descriptor                   DescriptorV3
	Correlation                  CorrelationV3
	ResolveExistingAuthorization AuthorizationReferenceV3
	RegistrationAuthorization    AuthorizationReferenceV3
	ReadFilter                   *ReadFilterRequirementV3
	AdminTarget                  *AdminTargetRequirementV3
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
	store    ProjectResolutionStoreV3
	verifier AuthorizationVerifierV3
}

func NewResolverV3(store ProjectResolutionStoreV3, verifier AuthorizationVerifierV3) ResolverV3 {
	return ResolverV3{store: store, verifier: verifier}
}

// ResolveProjectV3 validates every identity fact before accessing the
// project-resolution store. A refusal returns a typed ResolutionErrorV3 and a
// result without canonical or mutation authority.
func (resolver ResolverV3) ResolveProjectV3(ctx context.Context, request ResolveProjectRequestV3) (result ResolveProjectResultV3, resultErr error) {
	correlation, err := NewCorrelationV3(string(request.Correlation))
	if err != nil {
		return ResolveProjectResultV3{}, err
	}
	request.Correlation = correlation
	attemptRecorded := false
	defer func() {
		if attemptRecorded || !result.Resolution().Outcome().Valid() {
			return
		}
		if err := resolver.recordResolutionAttemptV3(ctx, request, result.Resolution()); err != nil {
			result = ResolveProjectResultV3{}
			resultErr = errResolverStorageV3
		}
	}()

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
		authorization, err := NewAuthorizationReferenceV3(string(request.ResolveExistingAuthorization))
		if err != nil || request.RegistrationAuthorization != "" || request.ReadFilter != nil || request.AdminTarget != nil {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		verification, ok := resolver.verifiedAuthorizationV3(ctx, request, authorization)
		if !ok || !verification.PermitsAnchorLookup() {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		return resolver.resolveAnchorV3(ctx, request, verification)
	case RegisterAnchorIntentV3:
		authorization, err := NewAuthorizationReferenceV3(string(request.RegistrationAuthorization))
		if err != nil || request.ResolveExistingAuthorization != "" || request.ReadFilter != nil || request.AdminTarget != nil {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		verification, ok := resolver.verifiedAuthorizationV3(ctx, request, authorization)
		if !ok {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		if resolver.store == nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		command := AnchorRegistrationV3{
			anchorProjectID: request.Anchor.ProjectID,
			scope:           resolvedScopeV3(request.Anchor.Scope),
			verification:    verification,
			fence: CausalFirstMutationFenceV3{
				correlation: request.Correlation,
				intent:      request.Intent,
				valid:       true,
			},
		}
		var registrationResult ResolveProjectResultV3
		var registrationResultErr error
		err = resolver.store.RegisterAnchorBindingAndRecordAttemptV3(ctx, command, func(binding AnchorBindingV3) (ResolutionAttemptV3, error) {
			registrationResult, registrationResultErr = bindingResultV3(request, binding)
			return newResolutionAttemptV3(request, registrationResult.Resolution())
		})
		if err != nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		attemptRecorded = true
		return registrationResult, registrationResultErr
	case ReadFilterIntentV3:
		if request.ResolveExistingAuthorization != "" || request.RegistrationAuthorization != "" || request.AdminTarget != nil || !validReadFilterV3(request.ReadFilter, request.Correlation) {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		verification, ok := resolver.verifiedAuthorizationV3(ctx, request, request.ReadFilter.Authorization())
		if !ok || !verification.PermitsAnchorLookup() {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		return resolver.resolveAnchorV3(ctx, request, verification)
	case AdminTargetIntentV3:
		if request.ResolveExistingAuthorization != "" || request.RegistrationAuthorization != "" || request.ReadFilter != nil || !validAdminTargetV3(request.AdminTarget, request.Correlation) {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		verification, ok := resolver.verifiedAuthorizationV3(ctx, request, request.AdminTarget.Authorization())
		if !ok {
			return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
		}
		if resolver.store == nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		binding, err := resolver.store.LookupAdministrativeTargetV3(ctx, verification)
		if err != nil {
			return ResolveProjectResultV3{}, errResolverStorageV3
		}
		return bindingResultV3(request, binding)
	default:
		return refusalV3(request, ProjectDescriptorInvalidOutcomeV3)
	}
}

func (resolver ResolverV3) verifiedAuthorizationV3(ctx context.Context, request ResolveProjectRequestV3, authorization AuthorizationReferenceV3) (VerifiedAuthorizationV3, bool) {
	if resolver.verifier == nil {
		return VerifiedAuthorizationV3{}, false
	}
	verificationRequest := AuthorizationVerificationRequestV3{
		intent:        request.Intent,
		authorization: authorization,
		correlation:   request.Correlation,
	}
	if request.Intent == AdminTargetIntentV3 {
		verificationRequest.target = request.AdminTarget.Target()
		verificationRequest.audit = request.AdminTarget.audit
	}
	verification, err := resolver.verifier.VerifyAuthorizationV3(ctx, verificationRequest)
	if err != nil || !verification.Authorized {
		return VerifiedAuthorizationV3{}, false
	}
	capability := VerifiedAuthorizationV3{intent: request.Intent, correlation: request.Correlation, valid: true}
	switch request.Intent {
	case ResolveExistingIntentV3, RegisterAnchorIntentV3, ReadFilterIntentV3:
		if verification.AdministrativeTargetProjectKey != "" {
			return VerifiedAuthorizationV3{}, false
		}
	case AdminTargetIntentV3:
		projectKey, err := NewProjectKeyV3(string(verification.AdministrativeTargetProjectKey))
		if err != nil {
			return VerifiedAuthorizationV3{}, false
		}
		capability.projectKey = projectKey
	default:
		return VerifiedAuthorizationV3{}, false
	}
	return capability, true
}

func (resolver ResolverV3) recordResolutionAttemptV3(ctx context.Context, request ResolveProjectRequestV3, resolution ResolutionResultV3) error {
	if resolver.store == nil {
		return errResolverStorageV3
	}
	attempt, err := newResolutionAttemptV3(request, resolution)
	if err != nil {
		return errResolverStorageV3
	}
	return resolver.store.RecordResolutionAttemptV3(ctx, attempt)
}

func newResolutionAttemptV3(request ResolveProjectRequestV3, resolution ResolutionResultV3) (ResolutionAttemptV3, error) {
	anchorProjectID := ""
	if validUUID(request.Anchor.ProjectID) {
		anchorProjectID = request.Anchor.ProjectID
	}
	attempt := ResolutionAttemptV3{
		correlation:       resolution.Correlation(),
		intent:            resolution.Intent(),
		outcome:           resolution.Outcome(),
		anchorProjectID:   anchorProjectID,
		descriptorVersion: request.Descriptor.Version,
		provenance:        "anchor_v3",
		redirectReference: resolution.RedirectReference(),
	}
	if request.Intent == AdminTargetIntentV3 && request.AdminTarget != nil {
		attempt.administrativeTarget = request.AdminTarget.Target()
		attempt.administrativeAudit = request.AdminTarget.audit
	}
	if !attempt.Valid() {
		return ResolutionAttemptV3{}, errResolverStorageV3
	}
	return attempt, nil
}

func (resolver ResolverV3) resolveAnchorV3(ctx context.Context, request ResolveProjectRequestV3, authorization VerifiedAuthorizationV3) (ResolveProjectResultV3, error) {
	if resolver.store == nil {
		return ResolveProjectResultV3{}, errResolverStorageV3
	}
	if !authorization.PermitsAnchorLookup() {
		return ResolveProjectResultV3{}, ErrAuthorizationVerificationRequiredV3
	}
	binding, err := resolver.store.LookupAnchorBindingV3(ctx, authorization, request.Anchor.ProjectID)
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
	case AnchorBindingRedirectedV3:
		if request.Intent == ReadFilterIntentV3 {
			return refusalV3(request, ProjectAnchorDecisionRequiredOutcomeV3)
		}
		fallthrough
	case AnchorBindingActiveV3:
		projectKey, err := NewProjectKeyV3(binding.ProjectKey)
		if err != nil {
			return refusalV3(request, ProjectAnchorDecisionRequiredOutcomeV3)
		}
		scope := resolvedScopeV3(binding.Scope)
		if scope == "" || scope != resolvedScopeV3(request.Anchor.Scope) {
			return refusalV3(request, ProjectScopeMismatchOutcomeV3)
		}
		outcome := ProjectResolvedOutcomeV3
		redirect := RedirectReferenceV3("")
		if binding.State == AnchorBindingRedirectedV3 {
			outcome = ProjectRedirectedOutcomeV3
			redirect = RedirectReferenceV3(binding.RedirectReference)
		}
		result, err := NewSuccessResultV3(request.Intent, outcome, projectKey, scope, redirect, request.Correlation)
		if err != nil {
			return refusalV3(request, ProjectAnchorDecisionRequiredOutcomeV3)
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
