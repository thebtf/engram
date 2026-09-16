// Package legacyrelay owns Engram's private, one-frame legacy OMP relay.
// It deliberately has no gRPC, credential-store, or project-authority dependency.
package legacyrelay

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Protocol is the only wire protocol accepted by this private relay.
	Protocol = "engram-legacy-relay/1"

	// AdapterRevisionHAP01B is a version-skew gate, not caller authentication.
	AdapterRevisionHAP01B = "omp-hap-01b/1"

	// DefaultMaxFrameBytes caps one complete JSON line before its trailing LF.
	DefaultMaxFrameBytes = 64 * 1024

	maxOpaqueReferenceBytes     = 256
	maxDescriptorBytes          = 16 * 1024
	maxSessionStartPayloadBytes = 48 * 1024
	maxAdditionalContextBytes   = 12 * 1024
	capabilityEntropyBytes      = 32
	minimumCapabilityWireLength = 43
)

var (
	ErrFrameTooLarge        = errors.New("legacy relay frame exceeds cap")
	ErrInvalidFrame         = errors.New("invalid legacy relay frame")
	ErrProtocol             = errors.New("unsupported legacy relay protocol")
	ErrStaleGeneration      = errors.New("legacy relay daemon generation is stale")
	ErrDeadlineElapsed      = errors.New("legacy relay absolute deadline elapsed")
	ErrBootstrapUnavailable = errors.New("legacy relay bootstrap unavailable")
	ErrBootstrapAmbiguous   = errors.New("legacy relay bootstrap ambiguous")
	ErrCapabilityInvalid    = errors.New("legacy relay capability invalid")
	ErrCredentialInvalid    = errors.New("legacy relay credential invalid")
)

// Route is a closed relay route. It is intentionally numeric internally so a
// caller cannot construct a semantically valid route from an arbitrary string.
type Route uint8

const (
	RouteIdentityRegistration Route = iota + 1
	RouteSessionStartContext
	RouteAmbientCandidates
)

func (r Route) String() string {
	switch r {
	case RouteIdentityRegistration:
		return "IDENTITY_REGISTRATION"
	case RouteSessionStartContext:
		return "SESSION_START_CONTEXT"
	case RouteAmbientCandidates:
		return "AMBIENT_CANDIDATES"
	default:
		return ""
	}
}

func parseRoute(raw string) (Route, error) {
	switch raw {
	case "IDENTITY_REGISTRATION":
		return RouteIdentityRegistration, nil
	case "SESSION_START_CONTEXT":
		return RouteSessionStartContext, nil
	case "AMBIENT_CANDIDATES":
		return RouteAmbientCandidates, nil
	default:
		return 0, fmt.Errorf("%w: unsupported route", ErrInvalidFrame)
	}
}

func validRoute(route Route) bool {
	return route == RouteIdentityRegistration || route == RouteSessionStartContext || route == RouteAmbientCandidates
}

// RouteSet is a closed bitset for capabilities. It cannot contain routes
// outside the three-route protocol through its constructor.
type RouteSet uint8

const (
	routeBitIdentity RouteSet = 1 << iota
	routeBitSessionStart
	routeBitAmbient
	routeBitsAll = routeBitIdentity | routeBitSessionStart | routeBitAmbient
)

func routeBit(route Route) (RouteSet, bool) {
	switch route {
	case RouteIdentityRegistration:
		return routeBitIdentity, true
	case RouteSessionStartContext:
		return routeBitSessionStart, true
	case RouteAmbientCandidates:
		return routeBitAmbient, true
	default:
		return 0, false
	}
}

// NewRouteSet constructs a nonempty valid allowed-route set.
func NewRouteSet(routes ...Route) (RouteSet, error) {
	var set RouteSet
	for _, route := range routes {
		bit, ok := routeBit(route)
		if !ok {
			return 0, fmt.Errorf("invalid capability route %d", route)
		}
		set |= bit
	}
	if set == 0 {
		return 0, errors.New("capability route set is empty")
	}
	return set, nil
}

func (s RouteSet) allows(route Route) bool {
	bit, ok := routeBit(route)
	return ok && s&bit != 0
}

func (s RouteSet) valid() bool {
	return s != 0 && s&^routeBitsAll == 0
}

// DaemonGeneration is an opaque daemon-lifecycle reference.
type DaemonGeneration struct{ value string }

func NewDaemonGeneration(raw string) (DaemonGeneration, error) {
	if !validOpaqueReference(raw, maxOpaqueReferenceBytes) {
		return DaemonGeneration{}, errors.New("invalid daemon generation")
	}
	return DaemonGeneration{value: raw}, nil
}

func (r DaemonGeneration) Value() string { return r.value }
func (r DaemonGeneration) valid() bool   { return validOpaqueReference(r.value, maxOpaqueReferenceBytes) }

// RequestID is transport correlation only. It never becomes a receipt or
// semantic occurrence identifier.
type RequestID struct{ value string }

func newRequestID(raw string) (RequestID, error) {
	if !validOpaqueReference(raw, maxOpaqueReferenceBytes) {
		return RequestID{}, errors.New("invalid request ID")
	}
	return RequestID{value: raw}, nil
}

func (r RequestID) Value() string { return r.value }
func (r RequestID) valid() bool   { return validOpaqueReference(r.value, maxOpaqueReferenceBytes) }

// HostSessionRef is opaque host-provided callback state. It is never inferred
// from a process, CWD, muxcore ProjectContext ID, or daemon generation.
type HostSessionRef struct{ value string }

func NewHostSessionRef(raw string) (HostSessionRef, error) {
	if !validOpaqueReference(raw, maxOpaqueReferenceBytes) {
		return HostSessionRef{}, errors.New("invalid host session reference")
	}
	return HostSessionRef{value: raw}, nil
}

func (r HostSessionRef) Value() string { return r.value }
func (r HostSessionRef) valid() bool   { return validOpaqueReference(r.value, maxOpaqueReferenceBytes) }

// CanonicalProjectRef is only constructible after server-side V3 resolution.
// It is opaque to the relay and never originates from a relay request field.
type CanonicalProjectRef struct{ value string }

func NewCanonicalProjectRef(raw string) (CanonicalProjectRef, error) {
	if !validOpaqueReference(raw, maxOpaqueReferenceBytes) {
		return CanonicalProjectRef{}, errors.New("invalid canonical project reference")
	}
	return CanonicalProjectRef{value: raw}, nil
}

func (r CanonicalProjectRef) Value() string { return r.value }
func (r CanonicalProjectRef) valid() bool {
	return validOpaqueReference(r.value, maxOpaqueReferenceBytes)
}

// CredentialRef is a daemon-local reference, never a raw token or token path.
type CredentialRef struct{ value string }

func NewCredentialRef(raw string) (CredentialRef, error) {
	if !validOpaqueReference(raw, maxOpaqueReferenceBytes) {
		return CredentialRef{}, errors.New("invalid credential reference")
	}
	return CredentialRef{value: raw}, nil
}

func (r CredentialRef) Value() string { return r.value }
func (r CredentialRef) valid() bool   { return validOpaqueReference(r.value, maxOpaqueReferenceBytes) }

// AdapterAttestation is only a package revision/digest compatibility claim
// from the installed extension TCB. It authenticates neither a peer nor a body.
type AdapterAttestation struct {
	revision string
	digest   string
}

func NewAdapterAttestation(revision, installedArtifactSHA256 string) (AdapterAttestation, error) {
	if revision != AdapterRevisionHAP01B {
		return AdapterAttestation{}, errors.New("unsupported adapter revision")
	}
	if len(installedArtifactSHA256) != sha256.Size*2 {
		return AdapterAttestation{}, errors.New("invalid adapter digest length")
	}
	for _, char := range installedArtifactSHA256 {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return AdapterAttestation{}, errors.New("invalid adapter digest")
		}
	}
	return AdapterAttestation{revision: revision, digest: installedArtifactSHA256}, nil
}

func (a AdapterAttestation) Revision() string { return a.revision }
func (a AdapterAttestation) Digest() string   { return a.digest }
func (a AdapterAttestation) valid() bool {
	_, err := NewAdapterAttestation(a.revision, a.digest)
	return err == nil
}

// AdapterGate keeps digest comparison as a version gate. It does not claim
// cryptographic containment against the same-user/local-admin TCB.
type AdapterGate interface {
	Accept(AdapterAttestation) bool
}

type exactAdapterGate struct{ expected AdapterAttestation }

// ExactAdapterGate accepts one approved installed artifact revision/digest.
func ExactAdapterGate(expected AdapterAttestation) AdapterGate {
	return exactAdapterGate{expected: expected}
}

func (g exactAdapterGate) Accept(actual AdapterAttestation) bool {
	return g.expected.valid() && g.expected.revision == actual.revision && g.expected.digest == actual.digest
}

type rejectingAdapterGate struct{}

func (rejectingAdapterGate) Accept(AdapterAttestation) bool { return false }

// RejectAllAdapters is the safe default for a daemon that has no exact
// HAP-01C-qualified artifact attestation configured.
func RejectAllAdapters() AdapterGate { return rejectingAdapterGate{} }

// Capability is a random daemon-memory bearer value. Its encoding is exposed
// only at the relay wire boundary; no String method exists to discourage logs.
type Capability struct{ encoded string }

func parseCapability(raw string) (Capability, error) {
	if len(raw) < minimumCapabilityWireLength || len(raw) > base64.RawURLEncoding.EncodedLen(capabilityEntropyBytes) || strings.Contains(raw, "=") {
		return Capability{}, errors.New("invalid capability encoding")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != capabilityEntropyBytes {
		return Capability{}, errors.New("invalid capability encoding")
	}
	return Capability{encoded: raw}, nil
}

func (c Capability) wireValue() string { return c.encoded }
func (c Capability) valid() bool {
	_, err := parseCapability(c.encoded)
	return err == nil
}

// ProjectIdentityV3Descriptor is a bounded, validated-but-untrusted V3 hint.
// Its raw document is never stored in a capability record.
type ProjectIdentityV3Descriptor struct{ raw []byte }

func (d ProjectIdentityV3Descriptor) RawJSON() []byte { return bytes.Clone(d.raw) }
func (d ProjectIdentityV3Descriptor) valid() bool {
	return len(d.raw) > 0 && len(d.raw) <= maxDescriptorBytes
}

// SessionStartPayload remains structured JSON owned by the existing server
// behavior. Relay only bounds and forwards it.
type SessionStartPayload struct{ raw []byte }

func NewSessionStartPayload(raw []byte) (SessionStartPayload, error) {
	if len(raw) == 0 || len(raw) > maxSessionStartPayloadBytes || !utf8.Valid(raw) {
		return SessionStartPayload{}, errors.New("invalid session-start payload size or encoding")
	}
	if err := validateJSONObject(raw); err != nil {
		return SessionStartPayload{}, fmt.Errorf("invalid session-start payload: %w", err)
	}
	return SessionStartPayload{raw: bytes.Clone(raw)}, nil
}

func (p SessionStartPayload) RawJSON() []byte { return bytes.Clone(p.raw) }
func (p SessionStartPayload) valid() bool {
	return len(p.raw) > 0 && len(p.raw) <= maxSessionStartPayloadBytes
}

// AdditionalContext is bounded untrusted text. Existing OMP rendering owns the
// hidden-message wrapper; relay does not render or interpret it.
type AdditionalContext struct{ value string }

func NewAdditionalContext(raw string) (AdditionalContext, error) {
	if !utf8.ValidString(raw) || len(raw) > maxAdditionalContextBytes {
		return AdditionalContext{}, errors.New("invalid additional context")
	}
	return AdditionalContext{value: raw}, nil
}

func (c AdditionalContext) Value() string { return c.value }
func (c AdditionalContext) valid() bool {
	return utf8.ValidString(c.value) && len(c.value) <= maxAdditionalContextBytes
}

// requestHeader is shared only by well-formed request variants.
type requestHeader struct {
	requestID  RequestID
	generation DaemonGeneration
	adapter    AdapterAttestation
	deadlineMs int64
}

func (h requestHeader) valid() bool {
	return h.requestID.valid() && h.generation.valid() && h.adapter.valid() && h.deadlineMs > 0
}

// IncomingRequest is a sealed union. Route/body combinations are created only
// by the codec after exact key/type validation.
type IncomingRequest interface {
	Route() Route
	RequestID() RequestID
	DaemonGeneration() DaemonGeneration
	Adapter() AdapterAttestation
	DeadlineUnixMs() int64
	requestHeaderValue() requestHeader
	incomingRequestSeal()
}

// IdentityRegistrationRequest carries only an opaque host session reference
// and an untrusted bounded V3 descriptor. It has no project/keycard/scope field.
type IdentityRegistrationRequest struct {
	header      requestHeader
	hostSession HostSessionRef
	descriptor  ProjectIdentityV3Descriptor
}

func (IdentityRegistrationRequest) incomingRequestSeal() {
	// This unexported marker seals IncomingRequest to codec-created variants.
}
func (r IdentityRegistrationRequest) Route() Route                       { return RouteIdentityRegistration }
func (r IdentityRegistrationRequest) RequestID() RequestID               { return r.header.requestID }
func (r IdentityRegistrationRequest) DaemonGeneration() DaemonGeneration { return r.header.generation }
func (r IdentityRegistrationRequest) Adapter() AdapterAttestation        { return r.header.adapter }
func (r IdentityRegistrationRequest) DeadlineUnixMs() int64              { return r.header.deadlineMs }
func (r IdentityRegistrationRequest) requestHeaderValue() requestHeader  { return r.header }
func (r IdentityRegistrationRequest) HostSession() HostSessionRef        { return r.hostSession }
func (r IdentityRegistrationRequest) Descriptor() ProjectIdentityV3Descriptor {
	return ProjectIdentityV3Descriptor{raw: bytes.Clone(r.descriptor.raw)}
}

// SessionStartRequest has a capability-bearing body and cannot carry a V3
// descriptor, raw project selector, principal, endpoint, or credential.
type SessionStartRequest struct {
	header      requestHeader
	hostSession HostSessionRef
	capability  Capability
}

func (SessionStartRequest) incomingRequestSeal() {
	// This unexported marker seals IncomingRequest to codec-created variants.
}
func (r SessionStartRequest) Route() Route                       { return RouteSessionStartContext }
func (r SessionStartRequest) RequestID() RequestID               { return r.header.requestID }
func (r SessionStartRequest) DaemonGeneration() DaemonGeneration { return r.header.generation }
func (r SessionStartRequest) Adapter() AdapterAttestation        { return r.header.adapter }
func (r SessionStartRequest) DeadlineUnixMs() int64              { return r.header.deadlineMs }
func (r SessionStartRequest) requestHeaderValue() requestHeader  { return r.header }
func (r SessionStartRequest) HostSession() HostSessionRef        { return r.hostSession }
func (r SessionStartRequest) Capability() Capability             { return r.capability }

// AmbientRequest has the only prompt-text input and remains capability-bound.
type AmbientRequest struct {
	header      requestHeader
	hostSession HostSessionRef
	capability  Capability
	queryText   string
}

func (AmbientRequest) incomingRequestSeal() {
	// This unexported marker seals IncomingRequest to codec-created variants.
}
func (r AmbientRequest) Route() Route                       { return RouteAmbientCandidates }
func (r AmbientRequest) RequestID() RequestID               { return r.header.requestID }
func (r AmbientRequest) DaemonGeneration() DaemonGeneration { return r.header.generation }
func (r AmbientRequest) Adapter() AdapterAttestation        { return r.header.adapter }
func (r AmbientRequest) DeadlineUnixMs() int64              { return r.header.deadlineMs }
func (r AmbientRequest) requestHeaderValue() requestHeader  { return r.header }
func (r AmbientRequest) HostSession() HostSessionRef        { return r.hostSession }
func (r AmbientRequest) Capability() Capability             { return r.capability }
func (r AmbientRequest) QueryText() string                  { return r.queryText }

// noDeliveryReason is closed so relay cannot serialize arbitrary internal
// errors or credentials to its local caller.
type noDeliveryReason uint8

const (
	noDeliveryLocatorMissing noDeliveryReason = iota + 1
	noDeliveryDialFailed
	noDeliveryStaleGeneration
	noDeliveryBootstrapUnavailable
	noDeliveryBootstrapAmbiguous
	noDeliveryCapabilityInvalid
	noDeliveryDeadlineElapsed
	noDeliveryServerUnavailable
)

func (r noDeliveryReason) String() string {
	switch r {
	case noDeliveryLocatorMissing:
		return "LOCATOR_MISSING"
	case noDeliveryDialFailed:
		return "DIAL_FAILED"
	case noDeliveryStaleGeneration:
		return "STALE_GENERATION"
	case noDeliveryBootstrapUnavailable:
		return "BOOTSTRAP_UNAVAILABLE"
	case noDeliveryBootstrapAmbiguous:
		return "BOOTSTRAP_AMBIGUOUS"
	case noDeliveryCapabilityInvalid:
		return "CAPABILITY_INVALID"
	case noDeliveryDeadlineElapsed:
		return "DEADLINE_ELAPSED"
	case noDeliveryServerUnavailable:
		return "SERVER_UNAVAILABLE"
	default:
		return ""
	}
}

type rejectionReason uint8

const (
	rejectionInvalidRequest rejectionReason = iota + 1
	rejectionAdapterUnaccepted
)

func (r rejectionReason) String() string {
	switch r {
	case rejectionInvalidRequest:
		return "INVALID_REQUEST"
	case rejectionAdapterUnaccepted:
		return "ADAPTER_UNACCEPTED"
	default:
		return ""
	}
}

// relayResult is a sealed response union. An OK identity response cannot carry
// a session payload, and non-OK responses cannot carry any delivery body.
type relayResult interface {
	relayResultSeal()
}

type identityDelivery struct {
	capability Capability
	project    CanonicalProjectRef
}

func (identityDelivery) relayResultSeal() {
	// This unexported marker seals relayResult to local response variants.
}

type sessionStartDelivery struct{ payload SessionStartPayload }

func (sessionStartDelivery) relayResultSeal() {
	// This unexported marker seals relayResult to local response variants.
}

type ambientDelivery struct{ context AdditionalContext }

func (ambientDelivery) relayResultSeal() {
	// This unexported marker seals relayResult to local response variants.
}

type noDelivery struct{ reason noDeliveryReason }

func (noDelivery) relayResultSeal() {
	// This unexported marker seals relayResult to local response variants.
}

type rejected struct{ reason rejectionReason }

func (rejected) relayResultSeal() {
	// This unexported marker seals relayResult to local response variants.
}

type responseEnvelope struct {
	requestID  RequestID
	generation DaemonGeneration
	route      Route
	result     relayResult
}

func newResponse(request IncomingRequest, result relayResult) responseEnvelope {
	return responseEnvelope{
		requestID:  request.RequestID(),
		generation: request.DaemonGeneration(),
		route:      request.Route(),
		result:     result,
	}
}

func validOpaqueReference(raw string, maxBytes int) bool {
	if raw == "" || len(raw) > maxBytes || !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return false
	}
	for _, char := range raw {
		if unicode.IsControl(char) || unicode.IsSpace(char) || char == '/' || char == '\\' || char == '@' {
			return false
		}
	}
	return true
}
