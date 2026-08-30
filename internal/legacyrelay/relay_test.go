package legacyrelay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAdapterDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestReadRequestFrameRejectsExtraRouteFieldsAndTrailingData(t *testing.T) {
	now := time.Now()
	deadline := now.Add(time.Minute).UnixMilli()

	tests := []struct {
		name string
		line string
	}{
		{
			name: "unknown top level field",
			line: testIdentityFrame(deadline, `,"unexpected":true`),
		},
		{
			name: "route body mismatch",
			line: testFrame(deadline, "SESSION_START_CONTEXT", `{"hostSessionRef":"host-session","projectIdentityV3":`+testDescriptor()+`}`),
		},
		{
			name: "extra body field",
			line: testFrame(deadline, "SESSION_START_CONTEXT", `{"hostSessionRef":"host-session","sessionCapability":"`+testCapabilityWire()+`","unexpected":true}`),
		},
		{
			name: "trailing buffered request",
			line: testIdentityFrame(deadline, "") + "\n{}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadRequestFrame(bufio.NewReader(strings.NewReader(tt.line+"\n")), DefaultMaxFrameBytes, now)
			if err == nil {
				t.Fatal("ReadRequestFrame accepted malformed or trailing request")
			}
		})
	}
}

func TestReadRequestFrameRejectsOversizeBeforeDispatch(t *testing.T) {
	now := time.Now()
	line := testIdentityFrame(now.Add(time.Minute).UnixMilli(), "")
	if len(line) < 64 {
		t.Fatal("test fixture unexpectedly too small")
	}
	_, err := ReadRequestFrame(bufio.NewReader(strings.NewReader(line+"\n")), 64, now)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadRequestFrame error = %v, want ErrFrameTooLarge", err)
	}
}

func TestReadRequestFrameRejectsElapsedAbsoluteDeadline(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	_, err := ReadRequestFrame(
		bufio.NewReader(strings.NewReader(testIdentityFrame(now.Add(-time.Millisecond).UnixMilli(), "")+"\n")),
		DefaultMaxFrameBytes,
		now,
	)
	if !errors.Is(err, ErrDeadlineElapsed) {
		t.Fatalf("ReadRequestFrame error = %v, want ErrDeadlineElapsed", err)
	}
}

func TestBootstrapSelectsOnlyOneLiveAcceptedDescendant(t *testing.T) {
	host := mustProcess(t, 100, "host-incarnation", "/opt/omp", 1)
	child := mustProcess(t, 200, "child-incarnation", "/opt/engram", 100)
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{100: host, 200: child}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(image ProcessImage) bool {
		return image.Value() == "/opt/engram"
	}))
	if _, err := children.Observe(child.PID(), "diagnostic-project", map[string]string{"ENGRAM_CHILD_ONLY": "present"}); err != nil {
		t.Fatalf("Observe child: %v", err)
	}

	selection, err := NewBootstrapper(children, inspector, 8).SelectPeer(host.PID())
	if err != nil {
		t.Fatalf("SelectPeer: %v", err)
	}
	if selection.Host().PID() != host.PID() || selection.Child().Process().PID() != child.PID() {
		t.Fatalf("selection = host %d child %d", selection.Host().PID(), selection.Child().Process().PID())
	}

	otherChild := mustProcess(t, 201, "second-child", "/opt/engram", 100)
	inspector.processes[otherChild.PID()] = otherChild
	if _, err := children.Observe(otherChild.PID(), "diagnostic-project-2", nil); err != nil {
		t.Fatalf("Observe second child: %v", err)
	}
	if _, err := NewBootstrapper(children, inspector, 8).SelectPeer(host.PID()); !errors.Is(err, ErrBootstrapAmbiguous) {
		t.Fatalf("SelectPeer with two children error = %v, want ErrBootstrapAmbiguous", err)
	}
}

func TestBootstrapRejectsReusedOrExitedChild(t *testing.T) {
	host := mustProcess(t, 100, "host-incarnation", "/opt/omp", 1)
	child := mustProcess(t, 200, "child-incarnation", "/opt/engram", 100)
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{100: host, 200: child}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(ProcessImage) bool { return true }))
	if _, err := children.Observe(child.PID(), "diagnostic-project", nil); err != nil {
		t.Fatalf("Observe child: %v", err)
	}

	inspector.processes[child.PID()] = mustProcess(t, child.PID(), "reused-incarnation", "/opt/engram", host.PID())
	if _, err := NewBootstrapper(children, inspector, 8).SelectPeer(host.PID()); !errors.Is(err, ErrBootstrapUnavailable) {
		t.Fatalf("SelectPeer with reused child error = %v, want ErrBootstrapUnavailable", err)
	}

	delete(inspector.processes, child.PID())
	if _, err := NewBootstrapper(children, inspector, 8).SelectPeer(host.PID()); !errors.Is(err, ErrBootstrapUnavailable) {
		t.Fatalf("SelectPeer with exited child error = %v, want ErrBootstrapUnavailable", err)
	}
}

func TestCapabilityRejectsCrossSessionExpiryAndProcessReuse(t *testing.T) {
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	host := mustProcess(t, 100, "host-incarnation", "/opt/omp", 1)
	child := mustProcess(t, 200, "child-incarnation", "/opt/engram", 100)
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{100: host, 200: child}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(ProcessImage) bool { return true }))
	childBinding, err := children.Observe(child.PID(), "diagnostic-project", map[string]string{"CHILD_ONLY": "yes"})
	if err != nil {
		t.Fatalf("Observe child: %v", err)
	}
	clock := now
	registry, err := NewCapabilityRegistry(CapabilityRegistryConfig{
		TTL:       time.Minute,
		Now:       func() time.Time { return clock },
		Inspector: inspector,
		Children:  children,
	})
	if err != nil {
		t.Fatalf("NewCapabilityRegistry: %v", err)
	}
	generation := mustGeneration(t, "daemon-generation")
	hostSession := mustHostSession(t, "host-session")
	adapter := mustAdapter(t)
	project := mustProject(t, "0b7f7fe6-9990-4fa0-9225-a7f02670f96c")
	credential := mustCredential(t, "daemon-project-keycard-ref")
	descriptor := mustDescriptor(t)
	routes, err := NewRouteSet(RouteSessionStartContext, RouteAmbientCandidates)
	if err != nil {
		t.Fatalf("NewRouteSet: %v", err)
	}
	capability, err := registry.Issue(CapabilityBinding{
		Generation:  generation,
		HostSession: hostSession,
		HostProcess: host,
		Child:       childBinding,
		Adapter:     adapter,
		Descriptor:  descriptor,
		Project:     project,
		Credential:  credential,
		Routes:      routes,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	reuseKey := registrationReuseKey{
		generation: generation, hostSession: hostSession, hostProcess: host,
		child: childBinding, adapter: adapter, descriptor: descriptor,
	}
	reused, reusedProject, ok := registry.reuseRegistration(reuseKey)
	if !ok || reused != capability || reusedProject != project {
		t.Fatalf("reuseRegistration = %#v, %#v, %t; want original capability and project", reused, reusedProject, ok)
	}
	reuseKey.hostSession = mustHostSession(t, "other-host-session")
	if _, _, ok := registry.reuseRegistration(reuseKey); ok {
		t.Fatal("reuseRegistration accepted a cross-session key")
	}

	lease, err := registry.Validate(CapabilityCheck{
		Capability: capability, Route: RouteSessionStartContext, Generation: generation, HostSession: hostSession, Adapter: adapter, PeerPID: host.PID(),
	})
	if err != nil {
		t.Fatalf("Validate same binding: %v", err)
	}
	lease.Release()
	otherSession := mustHostSession(t, "other-host-session")
	if _, err := registry.Validate(CapabilityCheck{
		Capability: capability, Route: RouteSessionStartContext, Generation: generation, HostSession: otherSession, Adapter: adapter, PeerPID: host.PID(),
	}); !errors.Is(err, ErrCapabilityInvalid) {
		t.Fatalf("Validate cross-session error = %v, want ErrCapabilityInvalid", err)
	}

	inspector.processes[host.PID()] = mustProcess(t, host.PID(), "host-reused", "/opt/omp", 1)
	if _, err := registry.Validate(CapabilityCheck{
		Capability: capability, Route: RouteSessionStartContext, Generation: generation, HostSession: hostSession, Adapter: adapter, PeerPID: host.PID(),
	}); !errors.Is(err, ErrCapabilityInvalid) {
		t.Fatalf("Validate reused host error = %v, want ErrCapabilityInvalid", err)
	}

	inspector.processes[host.PID()] = host
	clock = clock.Add(2 * time.Minute)
	if _, err := registry.Validate(CapabilityCheck{
		Capability: capability, Route: RouteAmbientCandidates, Generation: generation, HostSession: hostSession, Adapter: adapter, PeerPID: host.PID(),
	}); !errors.Is(err, ErrCapabilityInvalid) {
		t.Fatalf("Validate expired capability error = %v, want ErrCapabilityInvalid", err)
	}
}

func TestCapabilityRevocationWaitsForAuthorizationLease(t *testing.T) {
	host := mustProcess(t, 100, "host-incarnation", "/opt/omp", 1)
	child := mustProcess(t, 200, "child-incarnation", "/opt/engram", 100)
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{100: host, 200: child}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(ProcessImage) bool { return true }))
	childBinding, err := children.Observe(child.PID(), "diagnostic-project", nil)
	if err != nil {
		t.Fatalf("Observe child: %v", err)
	}
	registry, err := NewCapabilityRegistry(CapabilityRegistryConfig{TTL: time.Minute, Now: time.Now, Inspector: inspector, Children: children})
	if err != nil {
		t.Fatalf("NewCapabilityRegistry: %v", err)
	}
	generation := mustGeneration(t, "daemon-generation")
	hostSession := mustHostSession(t, "host-session")
	adapter := mustAdapter(t)
	credential := mustCredential(t, "daemon-project-keycard-ref")
	routes := mustRouteSet(t, RouteSessionStartContext, RouteAmbientCandidates)
	capability, err := registry.Issue(CapabilityBinding{
		Generation: generation, HostSession: hostSession, HostProcess: host, Child: childBinding,
		Adapter: adapter, Descriptor: mustDescriptor(t), Project: mustProject(t, "0b7f7fe6-9990-4fa0-9225-a7f02670f96c"), Credential: credential, Routes: routes,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	lease, err := registry.Validate(CapabilityCheck{Capability: capability, Route: RouteSessionStartContext, Generation: generation, HostSession: hostSession, Adapter: adapter, PeerPID: host.PID()})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	revoked := make(chan struct{})
	go func() {
		registry.RevokeCredential(credential)
		close(revoked)
	}()
	select {
	case <-revoked:
		t.Fatal("credential revocation completed while an authorization lease was active")
	case <-time.After(20 * time.Millisecond):
	}
	lease.Release()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("credential revocation did not complete after lease release")
	}
	if _, err := registry.Validate(CapabilityCheck{Capability: capability, Route: RouteSessionStartContext, Generation: generation, HostSession: hostSession, Adapter: adapter, PeerPID: host.PID()}); !errors.Is(err, ErrCapabilityInvalid) {
		t.Fatalf("post-revocation Validate error = %v, want ErrCapabilityInvalid", err)
	}
}

func TestRelayNilGatewayReturnsOneNoDeliveryResponse(t *testing.T) {
	now := time.Now()
	host := mustProcess(t, 100, "host-incarnation", "/opt/omp", 1)
	child := mustProcess(t, 200, "child-incarnation", "/opt/engram", 100)
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{100: host, 200: child}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(ProcessImage) bool { return true }))
	if _, err := children.Observe(child.PID(), "diagnostic-project", nil); err != nil {
		t.Fatalf("Observe child: %v", err)
	}
	capabilities, err := NewCapabilityRegistry(CapabilityRegistryConfig{
		TTL:       time.Minute,
		Now:       time.Now,
		Inspector: inspector,
		Children:  children,
	})
	if err != nil {
		t.Fatalf("NewCapabilityRegistry: %v", err)
	}
	relay, err := NewRelay(RelayConfig{
		Generation:       mustGeneration(t, "daemon-generation"),
		AdapterGate:      ExactAdapterGate(mustAdapter(t)),
		Bootstrapper:     NewBootstrapper(children, inspector, 8),
		Capabilities:     capabilities,
		PeerResolver:     PeerResolverFunc(func(net.Conn) (int, error) { return host.PID(), nil }),
		Now:              time.Now,
		SafetyDeadline:   time.Second,
		MaxFrameBytes:    DefaultMaxFrameBytes,
		CapabilityRoutes: mustRouteSet(t, RouteSessionStartContext, RouteAmbientCandidates),
	})
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}

	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.ServeConn(context.Background(), server)
	}()
	if _, err := fmt.Fprint(client, testIdentityFrame(now.Add(time.Second).UnixMilli(), "")+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	line, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("read terminal response: %v", err)
	}
	if !strings.Contains(line, `"kind":"NO_DELIVERY"`) || !strings.Contains(line, `"reason":"SERVER_UNAVAILABLE"`) {
		t.Fatalf("unexpected nil-gateway response: %s", line)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not terminally close the connection")
	}
}

func TestRelayPreservesCallerAbsoluteDeadlineForGateway(t *testing.T) {
	now := time.Now()
	deadlineUnixMs := now.Add(2 * time.Second).UnixMilli()
	host := mustProcess(t, 100, "host-incarnation", "/opt/omp", 1)
	child := mustProcess(t, 200, "child-incarnation", "/opt/engram", 100)
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{100: host, 200: child}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(ProcessImage) bool { return true }))
	if _, err := children.Observe(child.PID(), "diagnostic-project", nil); err != nil {
		t.Fatalf("Observe child: %v", err)
	}
	capabilities, err := NewCapabilityRegistry(CapabilityRegistryConfig{TTL: time.Minute, Now: time.Now, Inspector: inspector, Children: children})
	if err != nil {
		t.Fatalf("NewCapabilityRegistry: %v", err)
	}
	gateway := &deadlineRecordingGateway{}
	relay, err := NewRelay(RelayConfig{
		Generation:       mustGeneration(t, "daemon-generation"),
		AdapterGate:      ExactAdapterGate(mustAdapter(t)),
		Bootstrapper:     NewBootstrapper(children, inspector, 8),
		Capabilities:     capabilities,
		Gateway:          gateway,
		PeerResolver:     PeerResolverFunc(func(net.Conn) (int, error) { return host.PID(), nil }),
		Now:              time.Now,
		SafetyDeadline:   5 * time.Second,
		MaxFrameBytes:    DefaultMaxFrameBytes,
		CapabilityRoutes: mustRouteSet(t, RouteSessionStartContext, RouteAmbientCandidates),
	})
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}

	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.ServeConn(context.Background(), server)
	}()
	if _, err := fmt.Fprint(client, testIdentityFrame(deadlineUnixMs, "")+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if _, err := bufio.NewReader(client).ReadString('\n'); err != nil {
		t.Fatalf("read terminal response: %v", err)
	}
	<-done
	if gateway.deadlineUnixMs != deadlineUnixMs {
		t.Fatalf("gateway absolute deadline = %d, want original %d", gateway.deadlineUnixMs, deadlineUnixMs)
	}
	if !gateway.contextHasDeadline || gateway.contextDeadline.UnixMilli() != deadlineUnixMs {
		t.Fatalf("gateway context deadline = %v, want original absolute deadline %d", gateway.contextDeadline, deadlineUnixMs)
	}
	secondClient, secondServer := net.Pipe()
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		relay.ServeConn(context.Background(), secondServer)
	}()
	if _, err := fmt.Fprint(secondClient, testIdentityFrame(deadlineUnixMs, "")+"\n"); err != nil {
		t.Fatalf("write repeated request: %v", err)
	}
	if _, err := bufio.NewReader(secondClient).ReadString('\n'); err != nil {
		t.Fatalf("read repeated terminal response: %v", err)
	}
	_ = secondClient.Close()
	<-secondDone
	if gateway.calls != 1 {
		t.Fatalf("gateway registration calls = %d, want one server resolution", gateway.calls)
	}
}

func TestLocatorRoundTripUsesStrictProtocolAndGeneration(t *testing.T) {
	temp := t.TempDir()
	generation := mustGeneration(t, "daemon-generation")
	endpoint, err := RuntimeEndpoint(temp, generation)
	if err != nil {
		t.Fatalf("RuntimeEndpoint: %v", err)
	}
	path := RuntimeLocatorPath(temp)
	if err := PublishLocator(path, Locator{Protocol: Protocol, Generation: generation, Endpoint: endpoint}); err != nil {
		t.Fatalf("PublishLocator: %v", err)
	}
	locator, err := ReadLocator(path)
	if err != nil {
		t.Fatalf("ReadLocator: %v", err)
	}
	if locator.Generation != generation || locator.Endpoint != endpoint || locator.Protocol != Protocol {
		t.Fatalf("locator = %#v", locator)
	}
}

func TestPublishLocatorAtomicallyReplacesPriorGeneration(t *testing.T) {
	temp := t.TempDir()
	path := RuntimeLocatorPath(temp)
	first := mustGeneration(t, "daemon-generation-one")
	second := mustGeneration(t, "daemon-generation-two")
	firstEndpoint, err := RuntimeEndpoint(temp, first)
	if err != nil {
		t.Fatalf("first RuntimeEndpoint: %v", err)
	}
	secondEndpoint, err := RuntimeEndpoint(temp, second)
	if err != nil {
		t.Fatalf("second RuntimeEndpoint: %v", err)
	}
	if err := PublishLocator(path, Locator{Protocol: Protocol, Generation: first, Endpoint: firstEndpoint}); err != nil {
		t.Fatalf("publish first locator: %v", err)
	}
	if err := PublishLocator(path, Locator{Protocol: Protocol, Generation: second, Endpoint: secondEndpoint}); err != nil {
		t.Fatalf("replace locator: %v", err)
	}
	locator, err := ReadLocator(path)
	if err != nil {
		t.Fatalf("read replaced locator: %v", err)
	}
	if locator.Generation != second || locator.Endpoint != secondEndpoint {
		t.Fatalf("replaced locator = %#v", locator)
	}
}

type deadlineRecordingGateway struct {
	deadlineUnixMs     int64
	contextDeadline    time.Time
	contextHasDeadline bool
	calls              int
}

func (g *deadlineRecordingGateway) RegisterIdentity(ctx context.Context, call IdentityRegistrationCall) (RegistrationResult, error) {
	g.calls += 1
	g.deadlineUnixMs = call.DeadlineUnixMs()
	g.contextDeadline, g.contextHasDeadline = ctx.Deadline()
	project, err := NewCanonicalProjectRef("0b7f7fe6-9990-4fa0-9225-a7f02670f96c")
	if err != nil {
		return RegistrationResult{}, err
	}
	credential, err := NewCredentialRef("daemon-project-keycard-ref")
	if err != nil {
		return RegistrationResult{}, err
	}
	routes, err := NewRouteSet(RouteSessionStartContext, RouteAmbientCandidates)
	if err != nil {
		return RegistrationResult{}, err
	}
	return NewRegistrationResult(project, credential, routes)
}

func (*deadlineRecordingGateway) GetSessionStartContext(context.Context, SessionStartCall) (SessionStartPayload, error) {
	return SessionStartPayload{}, errors.New("unexpected session-start call")
}

func (*deadlineRecordingGateway) GetAmbientCandidates(context.Context, AmbientCandidatesCall) (AdditionalContext, error) {
	return AdditionalContext{}, errors.New("unexpected ambient call")
}

type fakeProcessInspector struct {
	mu        sync.Mutex
	processes map[int]ProcessIdentity
	errors    map[int]error
}

func (f *fakeProcessInspector) Inspect(pid int) (ProcessIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errors[pid]; err != nil {
		return ProcessIdentity{}, err
	}
	process, ok := f.processes[pid]
	if !ok {
		return ProcessIdentity{}, errors.New("process unavailable")
	}
	return process, nil
}

func mustProcess(t *testing.T, pid int, incarnation, image string, parentPID int) ProcessIdentity {
	t.Helper()
	process, err := NewProcessIdentity(pid, incarnation, image, parentPID)
	if err != nil {
		t.Fatalf("NewProcessIdentity: %v", err)
	}
	return process
}

func mustGeneration(t *testing.T, raw string) DaemonGeneration {
	t.Helper()
	generation, err := NewDaemonGeneration(raw)
	if err != nil {
		t.Fatalf("NewDaemonGeneration: %v", err)
	}
	return generation
}

func mustHostSession(t *testing.T, raw string) HostSessionRef {
	t.Helper()
	value, err := NewHostSessionRef(raw)
	if err != nil {
		t.Fatalf("NewHostSessionRef: %v", err)
	}
	return value
}

func mustAdapter(t *testing.T) AdapterAttestation {
	t.Helper()
	adapter, err := NewAdapterAttestation(AdapterRevisionHAP01B, testAdapterDigest)
	if err != nil {
		t.Fatalf("NewAdapterAttestation: %v", err)
	}
	return adapter
}

func mustDescriptor(t *testing.T) ProjectIdentityV3Descriptor {
	t.Helper()
	value, err := parseProjectIdentityV3Descriptor([]byte(testDescriptor()))
	if err != nil {
		t.Fatalf("parseProjectIdentityV3Descriptor: %v", err)
	}
	return value
}

func mustProject(t *testing.T, raw string) CanonicalProjectRef {
	t.Helper()
	value, err := NewCanonicalProjectRef(raw)
	if err != nil {
		t.Fatalf("NewCanonicalProjectRef: %v", err)
	}
	return value
}

func mustCredential(t *testing.T, raw string) CredentialRef {
	t.Helper()
	value, err := NewCredentialRef(raw)
	if err != nil {
		t.Fatalf("NewCredentialRef: %v", err)
	}
	return value
}

func mustRouteSet(t *testing.T, routes ...Route) RouteSet {
	t.Helper()
	value, err := NewRouteSet(routes...)
	if err != nil {
		t.Fatalf("NewRouteSet: %v", err)
	}
	return value
}

func testIdentityFrame(deadline int64, suffix string) string {
	return testFrame(deadline, "IDENTITY_REGISTRATION", `{"hostSessionRef":"host-session","projectIdentityV3":`+testDescriptor()+`}`) + suffix
}

func testFrame(deadline int64, route, body string) string {
	return fmt.Sprintf(`{"protocol":"%s","requestId":"request-1","daemonGeneration":"daemon-generation","adapter":{"revision":"%s","installedArtifactSha256":"%s"},"route":"%s","deadlineUnixMs":%d,"body":%s}`,
		Protocol, AdapterRevisionHAP01B, testAdapterDigest, route, deadline, body)
}

func testDescriptor() string {
	return `{"version":3,"anchor_project_id":"0b7f7fe6-9990-4fa0-9225-a7f02670f96c","name":"engram","scope":"repository","normalized_git_remotes":[],"legacy_identifiers":[],"client_instance_id":"fixture-client"}`
}

func testCapabilityWire() string {
	return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}
