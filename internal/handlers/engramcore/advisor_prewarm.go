package engramcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/legacyrelay"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	maxAdvisorProfileTextBytes       = 512
	maxAdvisorBindingCacheItems      = 64
	maxAdvisorSubjectProofCacheItems = 64
	ompAdvisorProtocolVersion        = uint32(1)
	ompAdvisorCapabilityRevision     = "omp-advisor-1"
)

type advisorSubjectProof [sha256.Size]byte

type advisorProofKey struct {
	authority        grpcConnectionAuthority
	tokenFingerprint [sha256.Size]byte
}

// advisorProofCache retains only accepted, server-derived subject proofs. Its
// key deliberately excludes project identity and cwd: a proof authenticates a
// workstation channel, not a project resolution.
type advisorProofCache struct {
	mu      sync.RWMutex
	entries map[advisorProofKey]advisorSubjectProof
}

func newAdvisorProofCache() *advisorProofCache {
	return &advisorProofCache{entries: make(map[advisorProofKey]advisorSubjectProof)}
}

func advisorProofKeyFor(serverURL, token string) (advisorProofKey, error) {
	authority, err := grpcConnectionAuthorityFor(serverURL)
	if err != nil {
		return advisorProofKey{}, err
	}
	return advisorProofKey{
		authority:        authority,
		tokenFingerprint: sha256.Sum256([]byte(token)),
	}, nil
}

func parseAdvisorSubjectProof(raw []byte) (advisorSubjectProof, error) {
	if len(raw) != sha256.Size {
		return advisorSubjectProof{}, errors.New("Initialize returned an invalid authenticated subject proof")
	}
	var proof advisorSubjectProof
	copy(proof[:], raw)
	if proof == (advisorSubjectProof{}) {
		return advisorSubjectProof{}, errors.New("Initialize returned an invalid authenticated subject proof")
	}
	return proof, nil
}

// record is called only after ProxyTools has accepted the complete Initialize
// response. Empty proofs preserve old-server compatibility but remove a prior
// proof for the same authority/token channel so prewarm remains fail-closed.
func (c *advisorProofCache) record(serverURL, token string, raw []byte) error {
	if c == nil {
		return errors.New("advisor proof cache is unavailable")
	}
	key, err := advisorProofKeyFor(serverURL, token)
	if err != nil {
		return fmt.Errorf("normalize Initialize authority: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[advisorProofKey]advisorSubjectProof)
	}
	if len(raw) == 0 {
		delete(c.entries, key)
		return nil
	}
	proof, err := parseAdvisorSubjectProof(raw)
	if err != nil {
		delete(c.entries, key)
		return err
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= maxAdvisorSubjectProofCacheItems {
		return errors.New("advisor proof cache is full")
	}
	c.entries[key] = proof
	return nil
}

func (c *advisorProofCache) lookup(serverURL, token string) (advisorProofKey, advisorSubjectProof, bool, error) {
	key, err := advisorProofKeyFor(serverURL, token)
	if err != nil {
		return advisorProofKey{}, advisorSubjectProof{}, false, fmt.Errorf("normalize advisor authority: %w", err)
	}
	if c == nil {
		return key, advisorSubjectProof{}, false, nil
	}
	c.mu.RLock()
	proof, ok := c.entries[key]
	c.mu.RUnlock()
	return key, proof, ok, nil
}

func (c *advisorProofCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

// AdvisorClientProfileSpec supplies the non-secret, immutable facts that the
// daemon is allowed to present for the code-owned omp-advisor-1 profile. There
// are intentionally no project, cwd, token, or caller-selected capability
// fields.
type AdvisorClientProfileSpec struct {
	HostVersion             string
	AdapterID               string
	AdapterVersion          string
	InstalledArtifactSHA256 [sha256.Size]byte
	RuntimeProbeReceiptID   string
	SnapshotID              string
	SnapshotRevision        uint64
	CallbackDeadline        time.Duration
	BindingTTL              time.Duration
}

// AdvisorClientProfile is a validated immutable client projection of the
// server-owned omp-advisor-1 profile. It constructs exactly one code-owned
// requested capability and cannot carry project authority.
type AdvisorClientProfile struct {
	protocolMin             uint32
	protocolMax             uint32
	hostVersion             string
	adapterID               string
	adapterVersion          string
	installedArtifactSHA256 [sha256.Size]byte
	runtimeProbeReceiptID   string
	snapshotID              string
	snapshotRevision        uint64
	capabilityRevision      string
	callbackDeadline        time.Duration
	callbackDeadlineMS      uint32
	bindingTTL              time.Duration
}

func NewAdvisorClientProfile(spec AdvisorClientProfileSpec) (*AdvisorClientProfile, error) {
	if spec.SnapshotRevision == 0 {
		return nil, errors.New("advisor profile snapshot revision is invalid")
	}
	for _, field := range []string{
		spec.HostVersion,
		spec.AdapterID,
		spec.AdapterVersion,
		spec.RuntimeProbeReceiptID,
		spec.SnapshotID,
	} {
		if !validAdvisorProfileText(field) {
			return nil, errors.New("advisor profile contains invalid text")
		}
	}
	if !hasNonZeroBytes(spec.InstalledArtifactSHA256[:]) {
		return nil, errors.New("advisor profile artifact digest is invalid")
	}
	if spec.CallbackDeadline <= 0 || spec.CallbackDeadline%time.Millisecond != 0 {
		return nil, errors.New("advisor profile callback deadline is invalid")
	}
	deadlineMS := spec.CallbackDeadline.Milliseconds()
	if deadlineMS > int64(^uint32(0)) {
		return nil, errors.New("advisor profile callback deadline is invalid")
	}
	if spec.BindingTTL <= 0 {
		return nil, errors.New("advisor profile binding TTL is invalid")
	}

	return &AdvisorClientProfile{
		protocolMin:             ompAdvisorProtocolVersion,
		protocolMax:             ompAdvisorProtocolVersion,
		hostVersion:             spec.HostVersion,
		adapterID:               spec.AdapterID,
		adapterVersion:          spec.AdapterVersion,
		installedArtifactSHA256: spec.InstalledArtifactSHA256,
		runtimeProbeReceiptID:   spec.RuntimeProbeReceiptID,
		snapshotID:              spec.SnapshotID,
		snapshotRevision:        spec.SnapshotRevision,
		capabilityRevision:      ompAdvisorCapabilityRevision,
		callbackDeadline:        spec.CallbackDeadline,
		callbackDeadlineMS:      uint32(deadlineMS),
		bindingTTL:              spec.BindingTTL,
	}, nil
}

func (p *AdvisorClientProfile) valid() bool {
	if p == nil {
		return false
	}
	canonical, err := NewAdvisorClientProfile(AdvisorClientProfileSpec{
		HostVersion:             p.hostVersion,
		AdapterID:               p.adapterID,
		AdapterVersion:          p.adapterVersion,
		InstalledArtifactSHA256: p.installedArtifactSHA256,
		RuntimeProbeReceiptID:   p.runtimeProbeReceiptID,
		SnapshotID:              p.snapshotID,
		SnapshotRevision:        p.snapshotRevision,
		CallbackDeadline:        p.callbackDeadline,
		BindingTTL:              p.bindingTTL,
	})
	return err == nil && *canonical == *p
}

func validAdvisorProfileText(value string) bool {
	if value == "" || len(value) > maxAdvisorProfileTextBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func hasNonZeroBytes(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return true
		}
	}
	return false
}

func advisorBindResponseMalformed(response *pb.HostAdvisorBindResponse) bool {
	if advisorProtoMessageMalformed(response) {
		return true
	}
	binding := response.GetBinding()
	if advisorProtoMessageMalformed(binding) || advisorProtoMessageMalformed(binding.GetExpiresAt()) || advisorProtoMessageMalformed(binding.GetCapabilitySnapshot()) {
		return true
	}
	for _, capability := range binding.GetCapabilitySnapshot().GetCapabilities() {
		if advisorProtoMessageMalformed(capability) || advisorProtoMessageMalformed(capability.GetCorrelation()) || advisorProtoMessageMalformed(capability.GetCallback()) {
			return true
		}
	}
	return false
}

func advisorProtoMessageMalformed(message proto.Message) bool {
	return message == nil || !message.ProtoReflect().IsValid() || len(message.ProtoReflect().GetUnknown()) != 0
}

func (p *AdvisorClientProfile) requestedCapability() *pb.HostCapability {
	return &pb.HostCapability{
		Semantic: pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_BEFORE_AGENT_START,
		AllowedActions: []pb.HostAdvisorAction{
			pb.HostAdvisorAction_HOST_ADVISOR_ACTION_EMIT_ADVICE,
			pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ADAPTER_ATTESTATION,
		},
		ContextInjectionModes: []pb.HostAdvisorContextInjectionMode{
			pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_HIDDEN_UNTRUSTED_MESSAGE,
		},
		Correlation: &pb.HostAdvisorCorrelation{
			Session:           true,
			Turn:              true,
			ToolAction:        false,
			StablePhaseAnchor: true,
		},
		Callback: &pb.HostAdvisorCallback{
			Awaited:    true,
			DeadlineMs: p.callbackDeadlineMS,
			Ordering:   pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_BEFORE_FIRST_ACTION,
		},
		Acknowledgement: pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_ADAPTER_ATTESTED,
	}
}

func (p *AdvisorClientProfile) hello(runtimeRef legacyrelay.RuntimeInstanceRef) (*pb.HostHello, error) {
	if p == nil || runtimeRef.Value() == "" {
		return nil, errors.New("advisor profile or runtime reference is unavailable")
	}
	return &pb.HostHello{
		ProtocolRange: &pb.HostAdvisorProtocolRange{MinVersion: p.protocolMin, MaxVersion: p.protocolMax},
		Host: &pb.HostAdvisorHost{
			Family:             pb.HostAdvisorHostFamily_HOST_ADVISOR_HOST_FAMILY_OMP,
			HostVersion:        p.hostVersion,
			AdapterId:          p.adapterID,
			AdapterVersion:     p.adapterVersion,
			RuntimeInstanceRef: runtimeRef.Value(),
		},
		RequestedCapabilities: []*pb.HostCapability{p.requestedCapability()},
		EvidenceRef: &pb.HostAdvisorEvidenceRef{
			Artifact: &pb.HostAdvisorArtifact{
				Kind:         pb.HostAdvisorArtifactKind_HOST_ADVISOR_ARTIFACT_KIND_INSTALLED,
				DigestSha256: append([]byte(nil), p.installedArtifactSHA256[:]...),
			},
			RuntimeProbeReceiptId: p.runtimeProbeReceiptID,
		},
	}, nil
}

func (p *AdvisorClientProfile) materialDigest(authority grpcConnectionAuthority, proof advisorSubjectProof, hello *pb.HostHello) ([sha256.Size]byte, error) {
	encodedHello, err := (proto.MarshalOptions{Deterministic: true}).Marshal(hello)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("marshal advisor hello: %w", err)
	}

	material := make([]byte, 0, len(encodedHello)+256)
	material = append(material, "engram.host-advisor.prewarm-material/v1\x00"...)
	material = appendAdvisorMaterialPart(material, []byte(authority.addr))
	material = appendAdvisorMaterialPart(material, []byte(authority.tlsMode))
	material = appendAdvisorMaterialPart(material, []byte(authority.tlsCAHash))
	material = appendAdvisorMaterialPart(material, proof[:])
	material = appendAdvisorMaterialPart(material, []byte(p.snapshotID))
	material = appendAdvisorMaterialUint64(material, p.snapshotRevision)
	material = appendAdvisorMaterialPart(material, []byte(p.capabilityRevision))
	material = appendAdvisorMaterialUint64(material, uint64(p.bindingTTL))
	material = appendAdvisorMaterialPart(material, encodedHello)
	return sha256.Sum256(material), nil
}

func appendAdvisorMaterialPart(material, value []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	material = append(material, length[:]...)
	return append(material, value...)
}

func appendAdvisorMaterialUint64(material []byte, value uint64) []byte {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return append(material, encoded[:]...)
}

func (p *AdvisorClientProfile) validateBinding(response *pb.HostAdvisorBindResponse, proof advisorSubjectProof, now time.Time) (*pb.HostBinding, time.Time, error) {
	if p == nil || advisorBindResponseMalformed(response) {
		return nil, time.Time{}, errors.New("advisor Bind returned an invalid binding")
	}
	binding := response.GetBinding()
	if !validAdvisorProfileText(binding.GetBindingId()) {
		return nil, time.Time{}, errors.New("advisor Bind returned an invalid binding")
	}
	if !bytes.Equal(binding.GetAuthenticatedSubjectProofSha256(), proof[:]) {
		return nil, time.Time{}, errors.New("advisor Bind subject proof did not match Initialize")
	}
	expiresAt := binding.GetExpiresAt()
	if expiresAt == nil || expiresAt.CheckValid() != nil {
		return nil, time.Time{}, errors.New("advisor Bind returned an invalid expiry")
	}
	expiry := expiresAt.AsTime()
	if !expiry.After(now) || expiry.After(now.Add(p.bindingTTL)) {
		return nil, time.Time{}, errors.New("advisor Bind returned an invalid expiry")
	}
	if binding.GetCallbackDeadlineMs() != p.callbackDeadlineMS {
		return nil, time.Time{}, errors.New("advisor Bind callback deadline did not match profile")
	}

	snapshot := binding.GetCapabilitySnapshot()
	if snapshot == nil || snapshot.GetSnapshotId() != p.snapshotID || snapshot.GetRevision() != p.snapshotRevision || snapshot.GetCapabilityRevision() != p.capabilityRevision || len(snapshot.GetContractSha256()) != sha256.Size || !hasNonZeroBytes(snapshot.GetContractSha256()) {
		return nil, time.Time{}, errors.New("advisor Bind capability snapshot did not match profile")
	}
	capabilities := snapshot.GetCapabilities()
	if len(capabilities) != 1 || !proto.Equal(capabilities[0], p.requestedCapability()) {
		return nil, time.Time{}, errors.New("advisor Bind returned a non-exact capability subset")
	}
	return proto.Clone(binding).(*pb.HostBinding), expiry, nil
}

type advisorBindingChannel struct {
	authority  grpcConnectionAuthority
	subject    advisorSubjectProof
	hostFamily pb.HostAdvisorHostFamily
	adapterID  string
	runtimeRef string
}

type advisorBindingCacheEntry struct {
	child    legacyrelay.ChildBinding
	material [sha256.Size]byte
	binding  *pb.HostBinding
	expires  time.Time
}

// advisorBindingCache retains only validated Bind responses. A cache entry is
// never a substitute for the next session-start Bind; it is private state for a
// later qualified consumer in this daemon process.
type advisorBindingCache struct {
	mu      sync.Mutex
	entries map[advisorBindingChannel]advisorBindingCacheEntry
}

func newAdvisorBindingCache() *advisorBindingCache {
	return &advisorBindingCache{entries: make(map[advisorBindingChannel]advisorBindingCacheEntry)}
}

func (c *advisorBindingCache) invalidate(channel advisorBindingChannel) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.pruneLocked(time.Now())
	delete(c.entries, channel)
	c.mu.Unlock()
}

func (c *advisorBindingCache) removeChild(child legacyrelay.ChildBinding) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for channel, entry := range c.entries {
		if entry.child == child {
			delete(c.entries, channel)
		}
	}
	c.mu.Unlock()
}

func (c *advisorBindingCache) store(channel advisorBindingChannel, child legacyrelay.ChildBinding, material [sha256.Size]byte, binding *pb.HostBinding, expiry time.Time) error {
	if c == nil || binding == nil || !expiry.After(time.Now()) {
		return errors.New("advisor binding cache cannot store an invalid binding")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(time.Now())
	if _, exists := c.entries[channel]; !exists && len(c.entries) >= maxAdvisorBindingCacheItems {
		return errors.New("advisor binding cache is full")
	}
	c.entries[channel] = advisorBindingCacheEntry{
		child:    child,
		material: material,
		binding:  proto.Clone(binding).(*pb.HostBinding),
		expires:  expiry,
	}
	return nil
}

func (c *advisorBindingCache) cached(channel advisorBindingChannel, material [sha256.Size]byte, now time.Time) (*pb.HostBinding, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	entry, ok := c.entries[channel]
	if !ok || entry.material != material {
		return nil, false
	}
	return proto.Clone(entry.binding).(*pb.HostBinding), true
}

func (c *advisorBindingCache) pruneLocked(now time.Time) {
	for channel, entry := range c.entries {
		if !entry.expires.After(now) {
			delete(c.entries, channel)
		}
	}
}

// AdvisorPrewarmRequest contains only already-accepted process bootstrap facts
// and daemon generation. It intentionally has no project, cwd, or descriptor
// field.
type AdvisorPrewarmRequest struct {
	Bootstrap  legacyrelay.BootstrapSelection
	Generation legacyrelay.DaemonGeneration
}

// PrewarmAdvisorBinding performs a new Bind for one qualified OMP session
// start. The profile is constructor-injected and absent by default, so the
// production legacy relay path remains dark in HAP-02.
func (g *LegacyRelayGateway) PrewarmAdvisorBinding(ctx context.Context, request AdvisorPrewarmRequest) (*pb.HostBinding, error) {
	if g == nil || g.module == nil || g.advisorProfile == nil || g.advisorBindings == nil {
		return nil, errors.New("advisor prewarm is not configured")
	}
	if ctx == nil {
		return nil, errors.New("advisor prewarm context is unavailable")
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || !deadline.After(time.Now()) {
		return nil, errors.New("advisor prewarm requires a live bounded context deadline")
	}
	subject, err := g.advisorPrewarmSubject(request)
	if err != nil {
		return nil, err
	}

	// Remove the exact current channel before any dial or Bind attempt. A
	// connection failure, a rejected retry, or a malformed response therefore
	// cannot leave a prior local binding visible to the current session start.
	g.advisorBindings.invalidate(subject.channel)
	binding, expiry, err := g.advisorBind(ctx, subject.config, subject.hello, subject.proof)
	if err != nil {
		return nil, err
	}
	if err := g.advisorRequireCurrentSubject(subject.child, subject.config, subject.proofKey, subject.proof); err != nil {
		return nil, err
	}
	if err := g.advisorBindings.store(subject.channel, subject.child, subject.material, binding, expiry); err != nil {
		return nil, err
	}
	if err := g.advisorRequireCurrentSubject(subject.child, subject.config, subject.proofKey, subject.proof); err != nil {
		if errors.Is(err, errAdvisorProofChanged) {
			g.advisorBindings.invalidate(subject.channel)
		} else {
			g.advisorBindings.removeChild(subject.child)
		}
		return nil, err
	}
	return proto.Clone(binding).(*pb.HostBinding), nil
}

var errAdvisorProofChanged = errors.New("advisor Initialize subject proof changed during Bind")

type advisorPrewarmSubject struct {
	child    legacyrelay.ChildBinding
	config   advisorChildConfig
	proofKey advisorProofKey
	proof    advisorSubjectProof
	hello    *pb.HostHello
	channel  advisorBindingChannel
	material [sha256.Size]byte
}

func (g *LegacyRelayGateway) advisorPrewarmSubject(request AdvisorPrewarmRequest) (advisorPrewarmSubject, error) {
	runtimeRef, err := request.Bootstrap.RuntimeInstanceRef(request.Generation)
	if err != nil {
		return advisorPrewarmSubject{}, fmt.Errorf("derive advisor runtime reference: %w", err)
	}
	child := request.Bootstrap.Child()
	config, err := g.advisorConfigFor(child)
	if err != nil {
		g.advisorBindings.removeChild(child)
		return advisorPrewarmSubject{}, err
	}
	proofKey, proof, ok, err := g.module.advisorProofs.lookup(config.serverURL, config.token)
	if err != nil {
		g.advisorBindings.removeChild(child)
		return advisorPrewarmSubject{}, err
	}
	if !ok {
		g.advisorBindings.removeChild(child)
		return advisorPrewarmSubject{}, errors.New("advisor prewarm requires an accepted Initialize subject proof")
	}
	if !g.module.advisorProofsMatches(proofKey, proof) {
		g.advisorBindings.removeChild(child)
		return advisorPrewarmSubject{}, errors.New("advisor Initialize subject proof changed")
	}
	hello, err := g.advisorProfile.hello(runtimeRef)
	if err != nil {
		return advisorPrewarmSubject{}, err
	}
	material, err := g.advisorProfile.materialDigest(proofKey.authority, proof, hello)
	if err != nil {
		return advisorPrewarmSubject{}, err
	}
	return advisorPrewarmSubject{
		child:    child,
		config:   config,
		proofKey: proofKey,
		proof:    proof,
		hello:    hello,
		channel: advisorBindingChannel{
			authority:  proofKey.authority,
			subject:    proof,
			hostFamily: hello.GetHost().GetFamily(),
			adapterID:  hello.GetHost().GetAdapterId(),
			runtimeRef: hello.GetHost().GetRuntimeInstanceRef(),
		},
		material: material,
	}, nil
}

func (g *LegacyRelayGateway) advisorBind(ctx context.Context, config advisorChildConfig, hello *pb.HostHello, proof advisorSubjectProof) (*pb.HostBinding, time.Time, error) {
	connection, err := g.module.pool.getOrDialGRPC(config.serverURL, config.token)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("advisor Bind gRPC connection: %w", err)
	}
	response, err := pb.NewEngramServiceClient(connection).Bind(ctx, &pb.HostAdvisorBindRequest{Hello: hello}, grpc.WaitForReady(true))
	if err != nil {
		if code := status.Code(err); code == codes.Unauthenticated || code == codes.PermissionDenied {
			g.module.pool.closeTokenHash(hashToken(config.token))
		}
		return nil, time.Time{}, fmt.Errorf("advisor Bind: %w", err)
	}
	binding, expiry, err := g.advisorProfile.validateBinding(response, proof, time.Now())
	if err != nil {
		return nil, time.Time{}, err
	}
	return binding, expiry, nil
}

func (g *LegacyRelayGateway) advisorRequireCurrentSubject(child legacyrelay.ChildBinding, expectedConfig advisorChildConfig, expectedProofKey advisorProofKey, expectedProof advisorSubjectProof) error {
	currentConfig, err := g.advisorConfigFor(child)
	if err != nil || currentConfig != expectedConfig {
		return errors.New("advisor child configuration changed during Bind")
	}
	currentProofKey, currentProof, currentProofOK, err := g.module.advisorProofs.lookup(currentConfig.serverURL, currentConfig.token)
	if err != nil || !currentProofOK || currentProofKey != expectedProofKey || currentProof != expectedProof {
		return errAdvisorProofChanged
	}
	return nil
}

func (g *LegacyRelayGateway) advisorConfigFor(child legacyrelay.ChildBinding) (advisorChildConfig, error) {
	env, ok := g.children.ConfigFor(child.ConfigRef())
	if !ok {
		return advisorChildConfig{}, errors.New("advisor child configuration is unavailable")
	}
	serverURL := strings.TrimSpace(env[config.EnvServerURL])
	if serverURL == "" {
		serverURL = strings.TrimSpace(env[config.EnvServerURLAlt])
	}
	if err := validateRelayServerURL(serverURL); err != nil {
		return advisorChildConfig{}, errors.New("advisor server URL is unavailable")
	}
	token := strings.TrimSpace(env[config.EnvWorkstationToken])
	if !validRelayKeycard(token) {
		return advisorChildConfig{}, errors.New("advisor workstation keycard is unavailable")
	}
	return advisorChildConfig{serverURL: serverURL, token: token}, nil
}

type advisorChildConfig struct {
	serverURL string
	token     string
}

func (m *Module) advisorProofsMatches(key advisorProofKey, expected advisorSubjectProof) bool {
	if m == nil || m.advisorProofs == nil {
		return false
	}
	m.advisorProofs.mu.RLock()
	actual, ok := m.advisorProofs.entries[key]
	m.advisorProofs.mu.RUnlock()
	return ok && actual == expected
}
