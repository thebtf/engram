package projectidentity

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ResolutionIntentV3 declares why a caller requests V3 resolution.
type ResolutionIntentV3 string

const (
	ResolveExistingIntentV3 ResolutionIntentV3 = "resolve_existing"
	RegisterAnchorIntentV3  ResolutionIntentV3 = "register_anchor"
	ReadFilterIntentV3      ResolutionIntentV3 = "read_filter"
	AdminTargetIntentV3     ResolutionIntentV3 = "admin_target"
)

// Valid reports whether intent belongs to the closed V3 vocabulary.
func (intent ResolutionIntentV3) Valid() bool {
	switch intent {
	case ResolveExistingIntentV3, RegisterAnchorIntentV3, ReadFilterIntentV3, AdminTargetIntentV3:
		return true
	default:
		return false
	}
}

// MayEstablishBinding reports the only intent eligible to create a V3 binding.
// Authorization and resolver policy remain required before any write.
func (intent ResolutionIntentV3) MayEstablishBinding() bool {
	return intent == RegisterAnchorIntentV3
}

// ResolutionOutcomeV3 is the closed, transport-neutral V3 result vocabulary.
type ResolutionOutcomeV3 string

const (
	ProjectResolvedOutcomeV3                    ResolutionOutcomeV3 = "PROJECT_RESOLVED"
	ProjectRedirectedOutcomeV3                  ResolutionOutcomeV3 = "PROJECT_REDIRECTED"
	ProjectOnboardingRequiredOutcomeV3          ResolutionOutcomeV3 = "PROJECT_ONBOARDING_REQUIRED"
	ProjectAnchorInvalidOutcomeV3               ResolutionOutcomeV3 = "PROJECT_ANCHOR_INVALID"
	ProjectScopeMismatchOutcomeV3               ResolutionOutcomeV3 = "PROJECT_SCOPE_MISMATCH"
	ProjectNestedRepositoryUnresolvedOutcomeV3  ResolutionOutcomeV3 = "PROJECT_NESTED_REPOSITORY_UNRESOLVED"
	ProjectAnchorDecisionRequiredOutcomeV3      ResolutionOutcomeV3 = "PROJECT_ANCHOR_DECISION_REQUIRED"
	ProjectIdentityAmbiguousOutcomeV3           ResolutionOutcomeV3 = "PROJECT_IDENTITY_AMBIGUOUS"
	ProjectDescriptorUnsupportedOutcomeV3       ResolutionOutcomeV3 = "PROJECT_DESCRIPTOR_UNSUPPORTED"
	ProjectDescriptorInvalidOutcomeV3           ResolutionOutcomeV3 = "PROJECT_DESCRIPTOR_INVALID"
	ProjectKeyClientAssertionForbiddenOutcomeV3 ResolutionOutcomeV3 = "PROJECT_KEY_CLIENT_ASSERTION_FORBIDDEN"
)

// Valid reports whether outcome belongs to the closed V3 vocabulary.
func (outcome ResolutionOutcomeV3) Valid() bool {
	switch outcome {
	case ProjectResolvedOutcomeV3,
		ProjectRedirectedOutcomeV3,
		ProjectOnboardingRequiredOutcomeV3,
		ProjectAnchorInvalidOutcomeV3,
		ProjectScopeMismatchOutcomeV3,
		ProjectNestedRepositoryUnresolvedOutcomeV3,
		ProjectAnchorDecisionRequiredOutcomeV3,
		ProjectIdentityAmbiguousOutcomeV3,
		ProjectDescriptorUnsupportedOutcomeV3,
		ProjectDescriptorInvalidOutcomeV3,
		ProjectKeyClientAssertionForbiddenOutcomeV3:
		return true
	default:
		return false
	}
}

// IsSuccess reports whether the outcome may expose a server-issued canonical key.
func (outcome ResolutionOutcomeV3) IsSuccess() bool {
	return outcome == ProjectResolvedOutcomeV3 || outcome == ProjectRedirectedOutcomeV3
}

// IsRefusal reports whether the outcome is a valid non-success result.
func (outcome ResolutionOutcomeV3) IsRefusal() bool {
	return outcome.Valid() && !outcome.IsSuccess()
}

// ProjectKeyV3 is a server-issued canonical UUID authority. It is never accepted
// from a client descriptor and is exposed only by a successful result.
type ProjectKeyV3 string

// ResolvedScopeV3 identifies the scope bound to a successful resolution.
type ResolvedScopeV3 string

const (
	RepositoryResolvedScopeV3 ResolvedScopeV3 = "repository"
	DirectoryResolvedScopeV3  ResolvedScopeV3 = "directory"
)

func (scope ResolvedScopeV3) valid() bool {
	return scope == RepositoryResolvedScopeV3 || scope == DirectoryResolvedScopeV3
}

// CorrelationV3 is a redacted opaque audit reference.
type CorrelationV3 string

// RedirectReferenceV3 is an opaque reference to the audited redirect decision.
type RedirectReferenceV3 string

// AuthorizationReferenceV3 is an opaque reference to prior authorization.
type AuthorizationReferenceV3 string

// AdministrativeTargetReferenceV3 identifies a server-issued administrative target.
type AdministrativeTargetReferenceV3 string

// AdminAuditV3 holds the required redacted audit boundary for an administrative target.
type AdminAuditV3 struct {
	actor               string
	purpose             string
	decision            string
	retentionOrRollback string
}

// ReadFilterRequirementV3 binds the read-only exception to authorization and audit correlation.
type ReadFilterRequirementV3 struct {
	authorization AuthorizationReferenceV3
	correlation   CorrelationV3
}

// AdminTargetRequirementV3 binds a privileged target to authorization and the complete audit boundary.
type AdminTargetRequirementV3 struct {
	authorization AuthorizationReferenceV3
	correlation   CorrelationV3
	target        AdministrativeTargetReferenceV3
	audit         AdminAuditV3
}

// ResolutionResultV3 has no raw input, candidate list, binding state, cache state,
// or mutation state. Its canonical key is populated only by success constructors.
type ResolutionResultV3 struct {
	intent            ResolutionIntentV3
	outcome           ResolutionOutcomeV3
	projectKey        ProjectKeyV3
	resolvedScope     ResolvedScopeV3
	redirectReference RedirectReferenceV3
	correlation       CorrelationV3
}

// ResolutionErrorV3 is the safe public representation of a refusal. It never
// retains or renders a raw descriptor, credential, path, database error, or private data.
type ResolutionErrorV3 struct {
	outcome     ResolutionOutcomeV3
	correlation CorrelationV3
}

var (
	errInvalidV3Intent     = errors.New("invalid V3 resolution intent")
	errInvalidV3Outcome    = errors.New("invalid V3 resolution outcome")
	errInvalidV3ProjectKey = errors.New("invalid V3 project key")
	errInvalidV3Scope      = errors.New("invalid V3 resolved scope")
	errInvalidV3Reference  = errors.New("invalid V3 opaque reference")
	errInvalidV3Result     = errors.New("invalid V3 resolution result")
	errInvalidV3AdminAudit = errors.New("invalid V3 administrative audit")
)

// NewProjectKeyV3 validates a canonical UUID returned by the server.
func NewProjectKeyV3(value string) (ProjectKeyV3, error) {
	if !validUUID(value) {
		return "", errInvalidV3ProjectKey
	}
	return ProjectKeyV3(value), nil
}

// NewCorrelationV3 validates a redacted opaque audit reference.
func NewCorrelationV3(value string) (CorrelationV3, error) {
	if !validOpaqueReferenceV3(value) {
		return "", errInvalidV3Reference
	}
	return CorrelationV3(value), nil
}

// NewAuthorizationReferenceV3 validates a redacted authorization reference.
func NewAuthorizationReferenceV3(value string) (AuthorizationReferenceV3, error) {
	if !validOpaqueReferenceV3(value) {
		return "", errInvalidV3Reference
	}
	return AuthorizationReferenceV3(value), nil
}

// NewAdministrativeTargetReferenceV3 validates a server-issued target reference.
func NewAdministrativeTargetReferenceV3(value string) (AdministrativeTargetReferenceV3, error) {
	if !validOpaqueReferenceV3(value) {
		return "", errInvalidV3Reference
	}
	return AdministrativeTargetReferenceV3(value), nil
}

// NewAdminAuditV3 requires every administrative audit field named by the V3 contract.
func NewAdminAuditV3(actor, purpose, decision, retentionOrRollback string) (AdminAuditV3, error) {
	if !validOpaqueReferenceV3(actor) || !validOpaqueReferenceV3(purpose) || !validOpaqueReferenceV3(decision) || !validOpaqueReferenceV3(retentionOrRollback) {
		return AdminAuditV3{}, errInvalidV3AdminAudit
	}
	return AdminAuditV3{actor: actor, purpose: purpose, decision: decision, retentionOrRollback: retentionOrRollback}, nil
}

// NewReadFilterRequirementV3 requires authorization and correlation for the read-only exception.
func NewReadFilterRequirementV3(authorization AuthorizationReferenceV3, correlation CorrelationV3) (ReadFilterRequirementV3, error) {
	if !validOpaqueReferenceV3(string(authorization)) || !validOpaqueReferenceV3(string(correlation)) {
		return ReadFilterRequirementV3{}, errInvalidV3Reference
	}
	return ReadFilterRequirementV3{authorization: authorization, correlation: correlation}, nil
}

// NewAdminTargetRequirementV3 requires authorization, correlation, a server-issued target, and full audit data.
func NewAdminTargetRequirementV3(authorization AuthorizationReferenceV3, correlation CorrelationV3, target AdministrativeTargetReferenceV3, audit AdminAuditV3) (AdminTargetRequirementV3, error) {
	if !validOpaqueReferenceV3(string(authorization)) || !validOpaqueReferenceV3(string(correlation)) || !validOpaqueReferenceV3(string(target)) || !audit.valid() {
		return AdminTargetRequirementV3{}, errInvalidV3Reference
	}
	return AdminTargetRequirementV3{authorization: authorization, correlation: correlation, target: target, audit: audit}, nil
}

// NewSuccessResultV3 builds the only result that can expose a canonical key.
func NewSuccessResultV3(intent ResolutionIntentV3, outcome ResolutionOutcomeV3, projectKey ProjectKeyV3, scope ResolvedScopeV3, redirect RedirectReferenceV3, correlation CorrelationV3) (ResolutionResultV3, error) {
	if !intent.Valid() {
		return ResolutionResultV3{}, errInvalidV3Intent
	}
	if !outcome.IsSuccess() {
		return ResolutionResultV3{}, errInvalidV3Outcome
	}
	if !validUUID(string(projectKey)) {
		return ResolutionResultV3{}, errInvalidV3ProjectKey
	}
	if !scope.valid() {
		return ResolutionResultV3{}, errInvalidV3Scope
	}
	if !validOpaqueReferenceV3(string(correlation)) {
		return ResolutionResultV3{}, errInvalidV3Reference
	}
	if outcome == ProjectRedirectedOutcomeV3 && !validOpaqueReferenceV3(string(redirect)) {
		return ResolutionResultV3{}, errInvalidV3Result
	}
	if outcome == ProjectResolvedOutcomeV3 && redirect != "" {
		return ResolutionResultV3{}, errInvalidV3Result
	}
	return ResolutionResultV3{intent: intent, outcome: outcome, projectKey: projectKey, resolvedScope: scope, redirectReference: redirect, correlation: correlation}, nil
}

// NewRefusalResultV3 builds a result that cannot expose scoped authority.
func NewRefusalResultV3(intent ResolutionIntentV3, outcome ResolutionOutcomeV3, correlation CorrelationV3) (ResolutionResultV3, error) {
	if !intent.Valid() {
		return ResolutionResultV3{}, errInvalidV3Intent
	}
	if !outcome.IsRefusal() {
		return ResolutionResultV3{}, errInvalidV3Outcome
	}
	if !validOpaqueReferenceV3(string(correlation)) {
		return ResolutionResultV3{}, errInvalidV3Reference
	}
	return ResolutionResultV3{intent: intent, outcome: outcome, correlation: correlation}, nil
}

// NewResolutionErrorV3 builds the transport-safe error for a refusal.
func NewResolutionErrorV3(outcome ResolutionOutcomeV3, correlation CorrelationV3) (ResolutionErrorV3, error) {
	if !outcome.IsRefusal() {
		return ResolutionErrorV3{}, errInvalidV3Outcome
	}
	if !validOpaqueReferenceV3(string(correlation)) {
		return ResolutionErrorV3{}, errInvalidV3Reference
	}
	return ResolutionErrorV3{outcome: outcome, correlation: correlation}, nil
}

// Intent returns the declared purpose of the resolution request.
func (result ResolutionResultV3) Intent() ResolutionIntentV3 { return result.intent }

// Outcome returns the typed V3 result code.
func (result ResolutionResultV3) Outcome() ResolutionOutcomeV3 { return result.outcome }

// CanonicalProjectKey returns a value only for successful resolution.
func (result ResolutionResultV3) CanonicalProjectKey() ProjectKeyV3 { return result.projectKey }

// ResolvedScope returns a value only for successful resolution.
func (result ResolutionResultV3) ResolvedScope() ResolvedScopeV3 { return result.resolvedScope }

// RedirectReference returns a value only for PROJECT_REDIRECTED.
func (result ResolutionResultV3) RedirectReference() RedirectReferenceV3 {
	return result.redirectReference
}

// Correlation returns the redacted audit reference carried by every result.
func (result ResolutionResultV3) Correlation() CorrelationV3 { return result.correlation }

// IsRefusal reports whether this result withholds project-scoped authority.
func (result ResolutionResultV3) IsRefusal() bool { return result.outcome.IsRefusal() }

// MayEstablishBinding reports whether this intent is eligible to establish a binding.
func (result ResolutionResultV3) MayEstablishBinding() bool {
	return result.intent.MayEstablishBinding()
}

// PermitsScopedMutation reports contract-level eligibility only. Authorization,
// privacy, and resolver checks remain required before any mutation.
func (result ResolutionResultV3) PermitsScopedMutation() bool {
	return result.outcome.IsSuccess() && result.intent != ReadFilterIntentV3
}

// Error returns only the stable typed outcome; correlation is deliberately not rendered.
func (err ResolutionErrorV3) Error() string { return string(err.outcome) }

// Outcome returns the typed refusal code.
func (err ResolutionErrorV3) Outcome() ResolutionOutcomeV3 { return err.outcome }

// Correlation returns the redacted correlation reference.
func (err ResolutionErrorV3) Correlation() CorrelationV3 { return err.correlation }

// AllowsScopedMutation is always false for the read-only filter exception.
func (requirement ReadFilterRequirementV3) AllowsScopedMutation() bool { return false }

// Authorization returns the required authorization reference.
func (requirement ReadFilterRequirementV3) Authorization() AuthorizationReferenceV3 {
	return requirement.authorization
}

// Correlation returns the required redacted audit correlation.
func (requirement ReadFilterRequirementV3) Correlation() CorrelationV3 {
	return requirement.correlation
}

// AllowsScopedMutation reports that administrative mutation remains subject to the attached authorization and audit boundary.
func (requirement AdminTargetRequirementV3) AllowsScopedMutation() bool { return true }

// Authorization returns the explicit administrative authorization reference.
func (requirement AdminTargetRequirementV3) Authorization() AuthorizationReferenceV3 {
	return requirement.authorization
}

// Correlation returns the required redacted audit correlation.
func (requirement AdminTargetRequirementV3) Correlation() CorrelationV3 {
	return requirement.correlation
}

// Target returns the server-issued administrative target reference.
func (requirement AdminTargetRequirementV3) Target() AdministrativeTargetReferenceV3 {
	return requirement.target
}

func (audit AdminAuditV3) valid() bool {
	return validOpaqueReferenceV3(audit.actor) && validOpaqueReferenceV3(audit.purpose) && validOpaqueReferenceV3(audit.decision) && validOpaqueReferenceV3(audit.retentionOrRollback)
}

func validOpaqueReferenceV3(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= 256 && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("/\\@", r)
	}) == -1
}
