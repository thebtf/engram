package legacyrelay

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"time"
)

// PeerResolver extracts the local OS peer PID from the accepted relay socket.
// It is distinct from muxcore SessionMeta because a relay connection is not
// MCP and must observe its own peer at its own transport boundary.
type PeerResolver interface {
	PeerPID(net.Conn) (int, error)
}

// PeerResolverFunc adapts a resolver function for tests and daemon wiring.
type PeerResolverFunc func(net.Conn) (int, error)

func (f PeerResolverFunc) PeerPID(conn net.Conn) (int, error) {
	if f == nil {
		return 0, ErrBootstrapUnavailable
	}
	return f(conn)
}

// RelayConfig has no credential, server endpoint, principal, or project
// selector. T3 supplies a Gateway only after it owns those daemon-only details.
type RelayConfig struct {
	Generation       DaemonGeneration
	AdapterGate      AdapterGate
	Bootstrapper     Bootstrapper
	Capabilities     *CapabilityRegistry
	Gateway          Gateway
	PeerResolver     PeerResolver
	Now              func() time.Time
	SafetyDeadline   time.Duration
	MaxFrameBytes    int
	CapabilityRoutes RouteSet
}

// Relay serves one strict request/response connection at a time. Listener
// ownership and locator publication live separately so a default-dark daemon
// never opens this endpoint.
type Relay struct {
	generation       DaemonGeneration
	adapterGate      AdapterGate
	bootstrapper     Bootstrapper
	capabilities     *CapabilityRegistry
	gateway          Gateway
	peerResolver     PeerResolver
	now              func() time.Time
	safetyDeadline   time.Duration
	maxFrameBytes    int
	capabilityRoutes RouteSet
}

// NewRelay constructs the private relay. A nil Gateway is deliberately valid:
// it returns NO_DELIVERY rather than inventing a direct transport or credential.
func NewRelay(config RelayConfig) (*Relay, error) {
	if !config.Generation.valid() || config.Capabilities == nil || config.PeerResolver == nil || config.Now == nil || config.SafetyDeadline <= 0 || config.MaxFrameBytes <= 0 || config.MaxFrameBytes > DefaultMaxFrameBytes || !config.CapabilityRoutes.valid() || config.CapabilityRoutes.allows(RouteIdentityRegistration) {
		return nil, errors.New("invalid legacy relay configuration")
	}
	if config.Bootstrapper.children == nil || config.Bootstrapper.inspector == nil || config.Bootstrapper.maxDepth <= 0 {
		return nil, errors.New("legacy relay requires a bounded bootstrapper")
	}
	if config.AdapterGate == nil {
		config.AdapterGate = RejectAllAdapters()
	}
	return &Relay{
		generation:       config.Generation,
		adapterGate:      config.AdapterGate,
		bootstrapper:     config.Bootstrapper,
		capabilities:     config.Capabilities,
		gateway:          config.Gateway,
		peerResolver:     config.PeerResolver,
		now:              config.Now,
		safetyDeadline:   config.SafetyDeadline,
		maxFrameBytes:    config.MaxFrameBytes,
		capabilityRoutes: config.CapabilityRoutes,
	}, nil
}

// ServeConn consumes one frame, derives one callback context deadline, writes
// at most one terminal response, and closes the connection. Malformed frames
// that cannot supply trustworthy correlation fields receive no response rather
// than a fabricated route/ID.
func (r *Relay) ServeConn(parent context.Context, conn net.Conn) {
	if r == nil || conn == nil {
		return
	}
	defer conn.Close()
	if parent == nil {
		parent = context.Background()
	}
	receivedAt := r.now()
	if receivedAt.IsZero() {
		return
	}
	_ = conn.SetDeadline(receivedAt.Add(r.safetyDeadline))
	reader := bufio.NewReaderSize(conn, r.maxFrameBytes+1)
	request, err := ReadRequestFrame(reader, r.maxFrameBytes, receivedAt)
	if err != nil {
		return
	}
	effectiveDeadline, ok := r.effectiveDeadline(parent, request.DeadlineUnixMs(), receivedAt)
	if !ok || parent.Err() != nil {
		r.writeTerminalResponse(conn, newResponse(request, noDelivery{reason: noDeliveryDeadlineElapsed}))
		return
	}
	if err := conn.SetDeadline(effectiveDeadline); err != nil {
		r.writeTerminalResponse(conn, newResponse(request, noDelivery{reason: noDeliveryDeadlineElapsed}))
		return
	}
	callbackCtx, cancel := context.WithDeadline(parent, effectiveDeadline)
	defer cancel()
	r.writeTerminalResponse(conn, r.dispatch(callbackCtx, conn, request))
}

func (r *Relay) effectiveDeadline(parent context.Context, deadlineUnixMs int64, receivedAt time.Time) (time.Time, bool) {
	effectiveDeadline := receivedAt.Add(r.safetyDeadline)
	requestDeadline := unixMillis(deadlineUnixMs)
	if requestDeadline.Before(effectiveDeadline) {
		effectiveDeadline = requestDeadline
	}
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(effectiveDeadline) {
		effectiveDeadline = parentDeadline
	}
	return effectiveDeadline, receivedAt.Before(effectiveDeadline)
}

func (r *Relay) writeTerminalResponse(conn net.Conn, response responseEnvelope) {
	frame, err := EncodeResponseFrame(response)
	if err != nil || len(frame) > r.maxFrameBytes {
		return
	}
	_ = writeLine(conn, frame)
}

func (r *Relay) dispatch(ctx context.Context, conn net.Conn, request IncomingRequest) responseEnvelope {
	if request.DaemonGeneration() != r.generation {
		return newResponse(request, noDelivery{reason: noDeliveryStaleGeneration})
	}
	if !r.adapterGate.Accept(request.Adapter()) {
		return newResponse(request, rejected{reason: rejectionAdapterUnaccepted})
	}

	switch typed := request.(type) {
	case IdentityRegistrationRequest:
		return r.dispatchIdentity(ctx, conn, typed)
	case SessionStartRequest:
		return r.dispatchSessionStart(ctx, conn, typed)
	case AmbientRequest:
		return r.dispatchAmbient(ctx, conn, typed)
	default:
		return newResponse(request, rejected{reason: rejectionInvalidRequest})
	}
}

func (r *Relay) dispatchIdentity(ctx context.Context, conn net.Conn, request IdentityRegistrationRequest) responseEnvelope {
	peerPID, err := r.peerResolver.PeerPID(conn)
	if err != nil || peerPID <= 0 {
		log.Print("legacy relay identity unavailable stage=peer")
		return newResponse(request, noDelivery{reason: noDeliveryBootstrapUnavailable})
	}
	selection, err := r.bootstrapper.SelectPeer(peerPID)
	if errors.Is(err, ErrBootstrapAmbiguous) {
		log.Print("legacy relay identity unavailable stage=bootstrap_ambiguous")
		return newResponse(request, noDelivery{reason: noDeliveryBootstrapAmbiguous})
	}
	if err != nil {
		log.Print("legacy relay identity unavailable stage=bootstrap_unavailable")
		return newResponse(request, noDelivery{reason: noDeliveryBootstrapUnavailable})
	}
	if r.gateway == nil {
		log.Print("legacy relay identity unavailable stage=gateway_missing")
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	registered, err := r.gateway.RegisterIdentity(ctx, IdentityRegistrationCall{
		descriptor:     request.Descriptor(),
		hostSession:    request.HostSession(),
		bootstrap:      selection,
		deadlineUnixMs: request.DeadlineUnixMs(),
	})
	if err != nil {
		log.Print("legacy relay identity unavailable stage=gateway_error")
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	if !registered.valid() {
		log.Print("legacy relay identity unavailable stage=registration_invalid")
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	if ctx.Err() != nil {
		log.Print("legacy relay identity unavailable stage=registration_deadline")
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	capability, err := r.capabilities.Issue(CapabilityBinding{
		Generation:  r.generation,
		HostSession: request.HostSession(),
		HostProcess: selection.Host(),
		Child:       selection.Child(),
		Adapter:     request.Adapter(),
		Descriptor:  request.Descriptor(),
		Project:     registered.Project(),
		Credential:  registered.Credential(),
		Routes:      registered.Routes(),
	})
	if err != nil {
		log.Print("legacy relay identity unavailable stage=capability")
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	if ctx.Err() != nil {
		log.Print("legacy relay identity unavailable stage=capability_deadline")
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	return newResponse(request, identityDelivery{capability: capability, project: registered.Project()})
}

func (r *Relay) dispatchSessionStart(ctx context.Context, conn net.Conn, request SessionStartRequest) responseEnvelope {
	lease, response := r.validateCapability(conn, request, request.Capability())
	if response != nil {
		return *response
	}
	defer lease.Release()
	if r.gateway == nil {
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	authorization := lease.Session()
	payload, err := r.gateway.GetSessionStartContext(ctx, SessionStartCall{authorization: authorization, deadlineUnixMs: request.DeadlineUnixMs()})
	if errors.Is(err, ErrCredentialInvalid) {
		lease.Release()
		r.invalidateCredential(authorization.Credential())
		return newResponse(request, noDelivery{reason: noDeliveryCapabilityInvalid})
	}
	if err != nil || !payload.valid() || ctx.Err() != nil {
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	return newResponse(request, sessionStartDelivery{payload: payload})
}

func (r *Relay) dispatchAmbient(ctx context.Context, conn net.Conn, request AmbientRequest) responseEnvelope {
	lease, response := r.validateCapability(conn, request, request.Capability())
	if response != nil {
		return *response
	}
	defer lease.Release()
	if r.gateway == nil {
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	authorization := lease.Session()
	additionalContext, err := r.gateway.GetAmbientCandidates(ctx, AmbientCandidatesCall{authorization: authorization, queryText: request.QueryText(), deadlineUnixMs: request.DeadlineUnixMs()})
	if errors.Is(err, ErrCredentialInvalid) {
		lease.Release()
		r.invalidateCredential(authorization.Credential())
		return newResponse(request, noDelivery{reason: noDeliveryCapabilityInvalid})
	}
	if err != nil || !additionalContext.valid() || ctx.Err() != nil {
		return newResponse(request, noDelivery{reason: noDeliveryServerUnavailable})
	}
	return newResponse(request, ambientDelivery{context: additionalContext})
}

func (r *Relay) invalidateCredential(credential CredentialRef) {
	r.capabilities.RevokeCredential(credential)
	if invalidator, ok := r.gateway.(CredentialInvalidator); ok {
		invalidator.InvalidateCredential(credential)
	}
}

func (r *Relay) validateCapability(conn net.Conn, request IncomingRequest, capability Capability) (*AuthorizationLease, *responseEnvelope) {
	peerPID, err := r.peerResolver.PeerPID(conn)
	if err != nil || peerPID <= 0 {
		response := newResponse(request, noDelivery{reason: noDeliveryCapabilityInvalid})
		return nil, &response
	}
	lease, err := r.capabilities.Validate(CapabilityCheck{
		Capability:  capability,
		Route:       request.Route(),
		Generation:  r.generation,
		HostSession: hostSessionFor(request),
		Adapter:     request.Adapter(),
		PeerPID:     peerPID,
	})
	if err != nil {
		response := newResponse(request, noDelivery{reason: noDeliveryCapabilityInvalid})
		return nil, &response
	}
	return lease, nil
}

func hostSessionFor(request IncomingRequest) HostSessionRef {
	switch typed := request.(type) {
	case SessionStartRequest:
		return typed.HostSession()
	case AmbientRequest:
		return typed.HostSession()
	default:
		return HostSessionRef{}
	}
}

func unixMillis(milliseconds int64) time.Time {
	return time.Unix(milliseconds/1000, (milliseconds%1000)*int64(time.Millisecond))
}

func writeLine(conn net.Conn, frame []byte) error {
	frame = append(frame, '\n')
	for len(frame) > 0 {
		written, err := conn.Write(frame)
		if err != nil {
			return err
		}
		if written <= 0 {
			return net.ErrClosed
		}
		frame = frame[written:]
	}
	return nil
}
