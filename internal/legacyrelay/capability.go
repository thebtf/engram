package legacyrelay

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

const maxCapabilityTTL = 15 * time.Minute

// CapabilityBinding is the complete daemon-owned fact set retained for a
// capability. The bounded V3 descriptor is retained only in memory through
// expiry so T3 can satisfy the V3-only private session/ambient contract; it is
// never persisted, logged, rendered, or replaced with a raw V2 selector.
type CapabilityBinding struct {
	Generation  DaemonGeneration
	HostSession HostSessionRef
	HostProcess ProcessIdentity
	Child       ChildBinding
	Adapter     AdapterAttestation
	Descriptor  ProjectIdentityV3Descriptor
	Project     CanonicalProjectRef
	Credential  CredentialRef
	Routes      RouteSet
}

func (b CapabilityBinding) valid() bool {
	return b.Generation.valid() && b.HostSession.valid() && b.HostProcess.valid() && b.Child.valid() && b.Adapter.valid() && b.Descriptor.valid() && b.Project.valid() && b.Credential.valid() && b.Routes.valid()
}

type capabilityRecord struct {
	binding   CapabilityBinding
	expiresAt time.Time
}

// CapabilityRegistryConfig makes time/process dependencies explicit so the
// security-critical binding behavior can be tested deterministically.
type CapabilityRegistryConfig struct {
	TTL       time.Duration
	Now       func() time.Time
	Inspector ProcessInspector
	Children  *ChildRegistry
}

// CapabilityRegistry holds only expiring daemon-memory records. It has no
// persistence, body log, receipt, retry queue, or restart migration.
type CapabilityRegistry struct {
	mu        sync.RWMutex
	ttl       time.Duration
	now       func() time.Time
	inspector ProcessInspector
	children  *ChildRegistry
	records   map[string]capabilityRecord
}

// NewCapabilityRegistry constructs a bounded in-memory capability store.
func NewCapabilityRegistry(config CapabilityRegistryConfig) (*CapabilityRegistry, error) {
	if config.TTL <= 0 || config.TTL > maxCapabilityTTL {
		return nil, fmt.Errorf("capability TTL must be between 1ns and %s", maxCapabilityTTL)
	}
	if config.Now == nil || config.Inspector == nil || config.Children == nil {
		return nil, errors.New("capability registry requires clock, process inspector, and child registry")
	}
	return &CapabilityRegistry{
		ttl:       config.TTL,
		now:       config.Now,
		inspector: config.Inspector,
		children:  config.Children,
		records:   make(map[string]capabilityRecord),
	}, nil
}

// Issue mints or reuses a cryptographically random, opaque, expiring
// capability for one exact binding. It stores no raw child config, receipt,
// body log, or ancestry evidence.
func (r *CapabilityRegistry) Issue(binding CapabilityBinding) (Capability, error) {
	if r == nil || !binding.valid() {
		return Capability{}, ErrCapabilityInvalid
	}
	now := r.now()
	if now.IsZero() {
		return Capability{}, ErrCapabilityInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked(now)
	for token, record := range r.records {
		if sameCapabilityBinding(record.binding, binding) && now.Before(record.expiresAt) {
			return Capability{encoded: token}, nil
		}
	}
	capability, err := randomCapability()
	if err != nil {
		return Capability{}, err
	}
	r.records[capability.encoded] = capabilityRecord{binding: binding, expiresAt: now.Add(r.ttl)}
	return capability, nil
}

func sameCapabilityBinding(left, right CapabilityBinding) bool {
	return left.Generation == right.Generation &&
		left.HostSession == right.HostSession &&
		left.HostProcess == right.HostProcess &&
		left.Child == right.Child &&
		left.Adapter == right.Adapter &&
		bytes.Equal(left.Descriptor.raw, right.Descriptor.raw) &&
		left.Project == right.Project &&
		left.Credential == right.Credential &&
		left.Routes == right.Routes
}

// registrationReuseKey is the complete caller-observable portion of an
// identity registration binding. Project and credential authority remain in
// the existing capability record and are never accepted from the caller.
type registrationReuseKey struct {
	generation  DaemonGeneration
	hostSession HostSessionRef
	adapter     AdapterAttestation
	descriptor  ProjectIdentityV3Descriptor
	peerPID     int
}

func (key registrationReuseKey) valid() bool {
	return key.generation.valid() && key.hostSession.valid() && key.adapter.valid() &&
		key.descriptor.valid() && key.peerPID > 0
}

// reuseRegistration returns an existing live capability without repeating
// ancestry traversal or server resolution. The original binding already
// proved ancestry; unchanged host and child process incarnations preserve that
// authority. Child replacement, credential invalidation, process reuse, daemon
// generation changes, descriptor changes, and expiry all prevent reuse.
func (r *CapabilityRegistry) reuseRegistration(key registrationReuseKey) (Capability, CanonicalProjectRef, bool) {
	if r == nil || !key.valid() {
		return Capability{}, CanonicalProjectRef{}, false
	}
	now := r.now()
	if now.IsZero() {
		return Capability{}, CanonicalProjectRef{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked(now)
	for token, record := range r.records {
		binding := record.binding
		if binding.Generation != key.generation || binding.HostSession != key.hostSession ||
			binding.Adapter != key.adapter || !bytes.Equal(binding.Descriptor.raw, key.descriptor.raw) {
			continue
		}
		if !r.children.current(binding.Child) {
			delete(r.records, token)
			continue
		}
		liveHost, hostErr := r.inspector.Inspect(key.peerPID)
		liveChild, childErr := r.inspector.Inspect(binding.Child.process.pid)
		if hostErr != nil || childErr != nil || !sameProcess(binding.HostProcess, liveHost) || !sameProcess(binding.Child.process, liveChild) {
			delete(r.records, token)
			continue
		}
		return Capability{encoded: token}, binding.Project, true
	}
	return Capability{}, CanonicalProjectRef{}, false
}

// CapabilityCheck contains exactly the current peer/session/route facts that
// must agree with an in-memory record before data-plane gateway dispatch.
type CapabilityCheck struct {
	Capability  Capability
	Route       Route
	Generation  DaemonGeneration
	HostSession HostSessionRef
	Adapter     AdapterAttestation
	PeerPID     int
}

func (c CapabilityCheck) valid() bool {
	return c.Capability.valid() && validRoute(c.Route) && c.Generation.valid() && c.HostSession.valid() && c.Adapter.valid() && c.PeerPID > 0
}

// AuthorizedSession is the only result of successful validation. It carries
// safe opaque references and a bounded immutable V3 descriptor for T3. It
// cannot include a request body other than that required V3 identity handle or
// a raw token.
type AuthorizedSession struct {
	project     CanonicalProjectRef
	credential  CredentialRef
	child       ChildBinding
	hostSession HostSessionRef
	generation  DaemonGeneration
	descriptor  ProjectIdentityV3Descriptor
}

func (s AuthorizedSession) Project() CanonicalProjectRef { return s.project }
func (s AuthorizedSession) Credential() CredentialRef    { return s.credential }
func (s AuthorizedSession) Child() ChildBinding          { return s.child }
func (s AuthorizedSession) HostSession() HostSessionRef  { return s.hostSession }
func (s AuthorizedSession) Generation() DaemonGeneration { return s.generation }
func (s AuthorizedSession) Descriptor() ProjectIdentityV3Descriptor {
	return ProjectIdentityV3Descriptor{raw: s.descriptor.RawJSON()}
}

// AuthorizationLease holds the capability registry read lock through one
// gateway dispatch. Revocation blocks until already-authorized work releases
// its lease, and no new dispatch can validate after revocation acquires the
// write lock.
type AuthorizationLease struct {
	session AuthorizedSession
	release func()
	once    sync.Once
}

func (l *AuthorizationLease) Session() AuthorizedSession {
	if l == nil {
		return AuthorizedSession{}
	}
	return l.session
}

func (l *AuthorizationLease) Release() {
	if l == nil || l.release == nil {
		return
	}
	l.once.Do(l.release)
}

// Validate proves the capability still belongs to the same generation,
// adapter revision/digest, host session, allowed route, live OMP process, live
// accepted child process/config transport, and non-expired in-memory record.
// The returned lease MUST be released after the gateway call completes.
func (r *CapabilityRegistry) Validate(check CapabilityCheck) (*AuthorizationLease, error) {
	if r == nil || !check.valid() {
		return nil, ErrCapabilityInvalid
	}
	now := r.now()
	if now.IsZero() {
		return nil, ErrCapabilityInvalid
	}
	r.mu.RLock()
	record, ok := r.records[check.Capability.encoded]
	if !ok || !now.Before(record.expiresAt) {
		r.mu.RUnlock()
		if ok {
			r.Revoke(check.Capability)
		}
		return nil, ErrCapabilityInvalid
	}
	binding := record.binding
	if binding.Generation != check.Generation || binding.HostSession != check.HostSession || binding.Adapter != check.Adapter || !binding.Routes.allows(check.Route) {
		r.mu.RUnlock()
		return nil, ErrCapabilityInvalid
	}
	if !r.children.current(binding.Child) {
		r.mu.RUnlock()
		return nil, ErrCapabilityInvalid
	}
	livePeer, err := r.inspector.Inspect(check.PeerPID)
	if err != nil || !sameProcess(binding.HostProcess, livePeer) {
		r.mu.RUnlock()
		return nil, ErrCapabilityInvalid
	}
	liveChild, err := r.inspector.Inspect(binding.Child.process.pid)
	if err != nil || !sameProcess(binding.Child.process, liveChild) {
		r.mu.RUnlock()
		return nil, ErrCapabilityInvalid
	}
	session := AuthorizedSession{
		project:     binding.Project,
		credential:  binding.Credential,
		child:       binding.Child,
		hostSession: binding.HostSession,
		generation:  binding.Generation,
		descriptor:  ProjectIdentityV3Descriptor{raw: binding.Descriptor.RawJSON()},
	}
	return &AuthorizationLease{session: session, release: r.mu.RUnlock}, nil
}

// Revoke removes one capability immediately. It is idempotent and keeps no
// tombstone because capabilities are intentionally non-durable.
func (r *CapabilityRegistry) Revoke(capability Capability) {
	if r == nil || !capability.valid() {
		return
	}
	r.mu.Lock()
	delete(r.records, capability.encoded)
	r.mu.Unlock()
}

// RevokeCredential invalidates every in-memory capability bound to a rotated
// or revoked child-only credential reference.
func (r *CapabilityRegistry) RevokeCredential(credential CredentialRef) {
	if r == nil || !credential.valid() {
		return
	}
	r.mu.Lock()
	for token, record := range r.records {
		if record.binding.Credential == credential {
			delete(r.records, token)
		}
	}
	r.mu.Unlock()
}

// RevokeGeneration removes all capabilities from a no-longer-current daemon
// generation. Normal daemon restart naturally drops the entire registry.
func (r *CapabilityRegistry) RevokeGeneration(generation DaemonGeneration) {
	if r == nil || !generation.valid() {
		return
	}
	r.mu.Lock()
	for token, record := range r.records {
		if record.binding.Generation == generation {
			delete(r.records, token)
		}
	}
	r.mu.Unlock()
}

func (r *CapabilityRegistry) pruneExpiredLocked(now time.Time) {
	for token, record := range r.records {
		if !now.Before(record.expiresAt) {
			delete(r.records, token)
		}
	}
}

func randomCapability() (Capability, error) {
	bytes := make([]byte, capabilityEntropyBytes)
	if _, err := rand.Read(bytes); err != nil {
		return Capability{}, fmt.Errorf("generate session capability: %w", err)
	}
	return Capability{encoded: base64.RawURLEncoding.EncodeToString(bytes)}, nil
}
