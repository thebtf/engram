package hostadvisor

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

// Registry holds only bounded, expiring process-memory bindings. Restarting the
// server constructs a new registry and deliberately drops every binding.
type Registry struct {
	mu           sync.Mutex
	profiles     []AcceptedProfile
	maxBindings  int
	now          func() time.Time
	newBindingID func() (BindingID, error)
	bindings     map[BindingID]bindingRecord
	channels     map[channelKey]BindingID
}

type bindingRecord struct {
	binding  HostBinding
	material Digest
	channel  channelKey
}

type channelKey struct {
	subject            Digest
	hostFamily         HostFamily
	adapterID          string
	runtimeInstanceRef string
}

// NewRegistry constructs a bounded server-owned catalog and registry. It copies
// the supplied profiles before returning so later caller mutations cannot alter
// admission or a live binding.
func NewRegistry(config RegistryConfig) (*Registry, error) {
	if config.MaxBindings <= 0 {
		return nil, fmt.Errorf("%w: max bindings must be positive", ErrInvalidInput)
	}
	if len(config.Profiles) == 0 {
		return nil, fmt.Errorf("%w: at least one accepted profile is required", ErrInvalidInput)
	}

	profiles := make([]AcceptedProfile, len(config.Profiles))
	for index, configured := range config.Profiles {
		profile, err := normalizeProfile(configured)
		if err != nil {
			return nil, err
		}
		for priorIndex := range index {
			if profilesOverlap(profiles[priorIndex], profile) {
				return nil, fmt.Errorf("%w: accepted profiles overlap", ErrInvalidInput)
			}
		}
		profiles[index] = profile
	}

	now := config.Now
	if now == nil {
		now = time.Now
	}
	newBindingID := config.NewBindingID
	if newBindingID == nil {
		newBindingID = randomBindingID
	}

	return &Registry{
		profiles:     profiles,
		maxBindings:  config.MaxBindings,
		now:          now,
		newBindingID: newBindingID,
		bindings:     make(map[BindingID]bindingRecord),
		channels:     make(map[channelKey]BindingID),
	}, nil
}

// Bind admits a normalized host capability subset. Exact material reuse keeps
// its original ID and expiry. A different valid material atomically replaces
// only the matching authenticated runtime channel.
func (r *Registry) Bind(subject AuthenticatedSubject, hello HostHello) (HostBinding, error) {
	if r == nil || !r.ready() {
		return HostBinding{}, ErrUnsupported
	}
	if subject.proof.isZero() {
		return HostBinding{}, ErrInvalidInput
	}
	normalized, err := normalizeHello(hello)
	if err != nil {
		return HostBinding{}, err
	}
	profile, found := r.profileFor(normalized)
	if !found {
		return HostBinding{}, ErrUnsupported
	}

	now := r.now().UTC()
	if now.IsZero() {
		return HostBinding{}, fmt.Errorf("%w: registry clock is invalid", ErrInvalidInput)
	}
	channel := channelKey{
		subject:            subject.proof,
		hostFamily:         normalized.host.Family,
		adapterID:          normalized.host.AdapterID,
		runtimeInstanceRef: normalized.host.RuntimeInstanceRef,
	}
	channelWriter := newDigestWriter("engram.host-advisor.channel/v1")
	channelWriter.uint32(uint32(normalized.host.Family))
	channelWriter.text(normalized.host.AdapterID)
	channelWriter.text(normalized.host.RuntimeInstanceRef)
	boundChannel := BoundChannel{
		hostFamily: normalized.host.Family,
		commitment: channelWriter.sum(),
	}
	material := materialDigest(subject, profile, normalized)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked(now)

	oldID, hasOld := r.channels[channel]
	if hasOld {
		old, exists := r.bindings[oldID]
		if !exists {
			delete(r.channels, channel)
			hasOld = false
		} else if old.material == material {
			return cloneBinding(old.binding), nil
		}
	}
	if !hasOld && len(r.bindings) >= r.maxBindings {
		return HostBinding{}, ErrCapacity
	}

	bindingID, err := r.allocateBindingIDLocked()
	if err != nil {
		return HostBinding{}, err
	}
	expiresAt := now.Add(profile.BindingTTL).UTC()
	if !expiresAt.After(now) {
		return HostBinding{}, fmt.Errorf("%w: binding expiry is invalid", ErrInvalidInput)
	}
	binding := HostBinding{
		id:               bindingID,
		snapshot:         snapshotFor(profile, normalized.requested),
		expiresAt:        expiresAt,
		callbackDeadline: profileCallbackDeadline(profile),
		subject:          subject,
		channel:          boundChannel,
	}

	if hasOld {
		delete(r.bindings, oldID)
	}
	r.bindings[bindingID] = bindingRecord{binding: binding, material: material, channel: channel}
	r.channels[channel] = bindingID
	return cloneBinding(binding), nil
}

// RequireActive returns a live binding only when its original authenticated
// subject matches. Expired, replaced, unknown, and cross-subject IDs have one
// deliberately generic unavailable outcome.
func (r *Registry) RequireActive(subject AuthenticatedSubject, bindingID BindingID) (HostBinding, error) {
	if r == nil || !r.ready() || subject.proof.isZero() || !validBindingID(bindingID) {
		return HostBinding{}, ErrBindingUnavailable
	}
	now := r.now().UTC()
	if now.IsZero() {
		return HostBinding{}, ErrBindingUnavailable
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked(now)
	record, exists := r.bindings[bindingID]
	if !exists || !record.binding.subject.equal(subject) {
		return HostBinding{}, ErrBindingUnavailable
	}
	return cloneBinding(record.binding), nil
}

func (r *Registry) ready() bool {
	return r.now != nil && r.newBindingID != nil && r.maxBindings > 0 && len(r.profiles) > 0 && r.bindings != nil && r.channels != nil
}

func (r *Registry) profileFor(hello normalizedHello) (AcceptedProfile, bool) {
	for _, profile := range r.profiles {
		if profileSupports(profile, hello) {
			return profile, true
		}
	}
	return AcceptedProfile{}, false
}

func profilesOverlap(left, right AcceptedProfile) bool {
	if left.HostFamily != right.HostFamily || left.HostVersion != right.HostVersion || left.AdapterID != right.AdapterID || left.AdapterVersion != right.AdapterVersion || left.Evidence != right.Evidence {
		return false
	}
	return left.Protocol.Min <= right.Protocol.Max && right.Protocol.Min <= left.Protocol.Max
}

func (r *Registry) allocateBindingIDLocked() (BindingID, error) {
	for range 4 {
		bindingID, err := r.newBindingID()
		if err != nil {
			return "", err
		}
		if !validBindingID(bindingID) {
			return "", fmt.Errorf("%w: registry generated an invalid binding ID", ErrInvalidInput)
		}
		if _, exists := r.bindings[bindingID]; !exists {
			return bindingID, nil
		}
	}
	return "", fmt.Errorf("host advisor binding ID collision")
}

func (r *Registry) pruneExpiredLocked(now time.Time) {
	for bindingID, record := range r.bindings {
		if now.Before(record.binding.expiresAt) {
			continue
		}
		delete(r.bindings, bindingID)
		if current, exists := r.channels[record.channel]; exists && current == bindingID {
			delete(r.channels, record.channel)
		}
	}
}

func cloneBinding(binding HostBinding) HostBinding {
	binding.snapshot = cloneSnapshot(binding.snapshot)
	return binding
}

func validBindingID(bindingID BindingID) bool {
	return validText(bindingID.String())
}

func randomBindingID() (BindingID, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate host advisor binding ID: %w", err)
	}
	return BindingID(base64.RawURLEncoding.EncodeToString(raw)), nil
}
