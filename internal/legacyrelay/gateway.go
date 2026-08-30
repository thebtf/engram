package legacyrelay

import "context"

// Gateway is the only T1-to-T3 integration seam. T3 may implement it with the
// existing EngramService and child-only credential map, but this package never
// imports gRPC, generated protobufs, auth, persistence, or raw keycards.
type Gateway interface {
	RegisterIdentity(context.Context, IdentityRegistrationCall) (RegistrationResult, error)
	GetSessionStartContext(context.Context, SessionStartCall) (SessionStartPayload, error)
	GetAmbientCandidates(context.Context, AmbientCandidatesCall) (AdditionalContext, error)
}

// CredentialInvalidator is an optional post-lease cleanup seam. Relay calls it
// only after releasing the capability authorization lease and deleting the
// capability records for the credential.
type CredentialInvalidator interface {
	InvalidateCredential(CredentialRef)
}

// IdentityRegistrationCall gives T3 the untrusted V3 descriptor plus only the
// bounded direct-process/config evidence selected during bootstrap.
type IdentityRegistrationCall struct {
	descriptor     ProjectIdentityV3Descriptor
	hostSession    HostSessionRef
	bootstrap      BootstrapSelection
	deadlineUnixMs int64
}

func (c IdentityRegistrationCall) Descriptor() ProjectIdentityV3Descriptor {
	return ProjectIdentityV3Descriptor{raw: c.descriptor.RawJSON()}
}
func (c IdentityRegistrationCall) HostSession() HostSessionRef   { return c.hostSession }
func (c IdentityRegistrationCall) Bootstrap() BootstrapSelection { return c.bootstrap }
func (c IdentityRegistrationCall) DeadlineUnixMs() int64         { return c.deadlineUnixMs }

// RegistrationResult must be constructed by T3 only after V3 server resolution
// and child-only project-keycard selection. Its route set cannot include the
// registration route because identity never consumes a bearer capability.
type RegistrationResult struct {
	project    CanonicalProjectRef
	credential CredentialRef
	routes     RouteSet
}

func NewRegistrationResult(project CanonicalProjectRef, credential CredentialRef, routes RouteSet) (RegistrationResult, error) {
	if !project.valid() || !credential.valid() || !routes.valid() || routes.allows(RouteIdentityRegistration) {
		return RegistrationResult{}, ErrCapabilityInvalid
	}
	return RegistrationResult{project: project, credential: credential, routes: routes}, nil
}

func (r RegistrationResult) Project() CanonicalProjectRef { return r.project }
func (r RegistrationResult) Credential() CredentialRef    { return r.credential }
func (r RegistrationResult) Routes() RouteSet             { return r.routes }
func (r RegistrationResult) valid() bool {
	return r.project.valid() && r.credential.valid() && r.routes.valid() && !r.routes.allows(RouteIdentityRegistration)
}

// SessionStartCall carries a previously validated authorization and the exact
// callback absolute deadline parsed at the relay boundary. T3 uses its child
// config reference privately to select a keycard; no direct caller field can
// select project, principal, scope, endpoint, or credential.
type SessionStartCall struct {
	authorization  AuthorizedSession
	deadlineUnixMs int64
}

func (c SessionStartCall) Authorization() AuthorizedSession { return c.authorization }
func (c SessionStartCall) DeadlineUnixMs() int64            { return c.deadlineUnixMs }

// AmbientCandidatesCall is the same validated authorization plus bounded
// untrusted prompt text and the exact callback absolute deadline. Relay does
// not rank, render, or store the text.
type AmbientCandidatesCall struct {
	authorization  AuthorizedSession
	queryText      string
	deadlineUnixMs int64
}

func (c AmbientCandidatesCall) Authorization() AuthorizedSession { return c.authorization }
func (c AmbientCandidatesCall) QueryText() string                { return c.queryText }
func (c AmbientCandidatesCall) DeadlineUnixMs() int64            { return c.deadlineUnixMs }
