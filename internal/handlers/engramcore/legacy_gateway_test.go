package engramcore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/legacyrelay"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type relayGatewayMockServer struct {
	pb.UnimplementedEngramServiceServer
	mu sync.Mutex

	registerRequest *pb.RegisterProjectIdentityV3Request
	sessionRequest  *pb.GetSessionStartContextRequest
	ambientRequest  *pb.GetAmbientCandidatesRequest
	registerAuth    string
	sessionAuth     string
	ambientAuth     string
	sessionCalls    int
	ambientCalls    int
	sessionErr      error
}

func (s *relayGatewayMockServer) RegisterProjectIdentityV3(ctx context.Context, request *pb.RegisterProjectIdentityV3Request) (*pb.RegisterProjectIdentityV3Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registerRequest = request
	s.registerAuth = incomingAuthorization(ctx)
	return &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: resolvedV3Response()}, nil
}

func (s *relayGatewayMockServer) GetSessionStartContext(ctx context.Context, request *pb.GetSessionStartContextRequest) (*pb.GetSessionStartContextResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionRequest = request
	s.sessionAuth = incomingAuthorization(ctx)
	s.sessionCalls++
	if s.sessionErr != nil {
		return nil, s.sessionErr
	}
	return &pb.GetSessionStartContextResponse{
		Issues:              []*pb.SessionStartIssue{{Id: 1, Title: "Issue", Labels: []string{"relay"}}},
		Rules:               []*pb.SessionStartRule{{Id: 2, Content: "Rule", Version: 3}},
		Memories:            []*pb.SessionStartMemory{{Id: 3, Project: testRelayProject, Content: "Memory", Tags: []string{"relay"}}},
		GeneratedAt:         timestamppb.New(time.Unix(1_700_000_000, 0).UTC()),
		ProjectResolutionV3: resolvedV3Response(),
	}, nil
}

func (s *relayGatewayMockServer) GetAmbientCandidates(ctx context.Context, request *pb.GetAmbientCandidatesRequest) (*pb.GetAmbientCandidatesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ambientRequest = request
	s.ambientAuth = incomingAuthorization(ctx)
	s.ambientCalls++
	return &pb.GetAmbientCandidatesResponse{AdditionalContext: "bounded ambient"}, nil
}

func incomingAuthorization(ctx context.Context) string {
	values, _ := metadata.FromIncomingContext(ctx)
	authorization := values.Get("authorization")
	if len(authorization) == 0 {
		return ""
	}
	return authorization[0]
}

func startRelayGatewayMock(t *testing.T, server *relayGatewayMockServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterEngramServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	return "http://" + listener.Addr().String()
}

type relayGatewayInspector struct {
	mu        sync.Mutex
	processes map[int]legacyrelay.ProcessIdentity
}

func (i *relayGatewayInspector) Inspect(pid int) (legacyrelay.ProcessIdentity, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	process, ok := i.processes[pid]
	if !ok {
		return legacyrelay.ProcessIdentity{}, errors.New("process unavailable")
	}
	return process, nil
}

func TestLegacyRelayGatewayUsesSeparatedCredentialsAndExactCompatibilityPayload(t *testing.T) {
	fixture := newLegacyGatewayFixture(t)
	identity := fixture.call(t, "IDENTITY_REGISTRATION", json.RawMessage(`{"hostSessionRef":"host-session","projectIdentityV3":`+relayGatewayDescriptor()+`}`))
	if identity.Kind != "OK" || identity.SessionCapability == "" || identity.CanonicalProjectRef != testRelayProject {
		t.Fatalf("identity response = %#v", identity)
	}

	session := fixture.call(t, "SESSION_START_CONTEXT", mustJSON(t, map[string]any{
		"hostSessionRef": "host-session", "sessionCapability": identity.SessionCapability,
	}))
	if session.Kind != "OK" || len(session.Payload) == 0 {
		t.Fatalf("session response = %#v", session)
	}
	var payload map[string]any
	if err := json.Unmarshal(session.Payload, &payload); err != nil {
		t.Fatalf("decode session payload: %v", err)
	}
	if payload["generated_at"] != "2023-11-14T22:13:20Z" || payload["generatedAt"] != nil {
		t.Fatalf("session payload timestamp shape = %#v", payload)
	}
	rules := payload["rules"].([]any)
	if rules[0].(map[string]any)["narrative"] != "Rule" {
		t.Fatalf("session rule compatibility shape = %#v", rules[0])
	}

	ambient := fixture.call(t, "AMBIENT_CANDIDATES", mustJSON(t, map[string]any{
		"hostSessionRef": "host-session", "sessionCapability": identity.SessionCapability, "queryText": "Need context",
	}))
	if ambient.Kind != "OK" || ambient.AdditionalContext != "bounded ambient" {
		t.Fatalf("ambient response = %#v", ambient)
	}

	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	if fixture.server.registerAuth != "Bearer "+testRegistration {
		t.Fatalf("registration authorization = %q", fixture.server.registerAuth)
	}
	if fixture.server.sessionAuth != "Bearer "+testProjectToken || fixture.server.ambientAuth != "Bearer "+testProjectToken {
		t.Fatalf("project authorizations session=%q ambient=%q", fixture.server.sessionAuth, fixture.server.ambientAuth)
	}
	if fixture.server.registerRequest.GetRelayRevision() != legacyrelay.AdapterRevisionHAP01B || fixture.server.sessionRequest.GetProject() != "" || fixture.server.sessionRequest.GetProjectIdentityV3() == nil || fixture.server.sessionRequest.GetHostSessionRef() != "host-session" || fixture.server.ambientRequest.GetProjectIdentityV3() == nil || fixture.server.ambientRequest.GetQueryText() != "Need context" {
		t.Fatalf("gateway requests register=%#v session=%#v ambient=%#v", fixture.server.registerRequest, fixture.server.sessionRequest, fixture.server.ambientRequest)
	}
}

func TestLegacyRelayGatewayInvalidatesCapabilityAndPoolOnChildConfigRotation(t *testing.T) {
	fixture := newLegacyGatewayFixture(t)
	identity := fixture.call(t, "IDENTITY_REGISTRATION", json.RawMessage(`{"hostSessionRef":"host-session","projectIdentityV3":`+relayGatewayDescriptor()+`}`))
	if identity.Kind != "OK" {
		t.Fatalf("identity response = %#v", identity)
	}
	firstSession := fixture.call(t, "SESSION_START_CONTEXT", mustJSON(t, map[string]any{
		"hostSessionRef": "host-session", "sessionCapability": identity.SessionCapability,
	}))
	if firstSession.Kind != "OK" {
		t.Fatalf("first session response = %#v", firstSession)
	}
	oldHash := hashToken(testProjectToken)
	if countPoolTokenHash(fixture.module.pool, oldHash) != 1 {
		t.Fatal("old project credential connection was not created")
	}

	rotated := fixture.env
	rotated[config.EnvHAP01BProjectTokensJSON] = `{"` + testRelayProject + `":"engram_cccccccccccccccccccccccccccccccc"}`
	if _, err := fixture.children.Observe(fixture.child.PID(), "diagnostic-project", rotated); err != nil {
		t.Fatalf("observe rotated child config: %v", err)
	}
	if countPoolTokenHash(fixture.module.pool, oldHash) != 0 {
		t.Fatal("old project credential connection survived child config rotation")
	}

	afterRotation := fixture.call(t, "SESSION_START_CONTEXT", mustJSON(t, map[string]any{
		"hostSessionRef": "host-session", "sessionCapability": identity.SessionCapability,
	}))
	if afterRotation.Kind != "NO_DELIVERY" || afterRotation.Reason != "CAPABILITY_INVALID" {
		t.Fatalf("rotated capability response = %#v", afterRotation)
	}
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	if fixture.server.sessionCalls != 1 {
		t.Fatalf("session calls after rotation=%d, want 1", fixture.server.sessionCalls)
	}
}

func TestLegacyRelayGatewayAuthFailureInvalidatesAfterLeaseRelease(t *testing.T) {
	fixture := newLegacyGatewayFixture(t)
	identity := fixture.call(t, "IDENTITY_REGISTRATION", json.RawMessage(`{"hostSessionRef":"host-session","projectIdentityV3":`+relayGatewayDescriptor()+`}`))
	if identity.Kind != "OK" {
		t.Fatalf("identity response = %#v", identity)
	}
	fixture.server.mu.Lock()
	fixture.server.sessionErr = status.Error(codes.Unauthenticated, "revoked project keycard")
	fixture.server.mu.Unlock()
	response := fixture.call(t, "SESSION_START_CONTEXT", mustJSON(t, map[string]any{
		"hostSessionRef": "host-session", "sessionCapability": identity.SessionCapability,
	}))
	if response.Kind != "NO_DELIVERY" || response.Reason != "CAPABILITY_INVALID" {
		t.Fatalf("auth failure response = %#v", response)
	}
	retry := fixture.call(t, "SESSION_START_CONTEXT", mustJSON(t, map[string]any{
		"hostSessionRef": "host-session", "sessionCapability": identity.SessionCapability,
	}))
	if retry.Kind != "NO_DELIVERY" || retry.Reason != "CAPABILITY_INVALID" {
		t.Fatalf("post-invalidation response = %#v", retry)
	}
	fixture.server.mu.Lock()
	defer fixture.server.mu.Unlock()
	if fixture.server.sessionCalls != 1 {
		t.Fatalf("server session calls after invalidation=%d, want 1", fixture.server.sessionCalls)
	}
}

type legacyGatewayFixture struct {
	relay    *legacyrelay.Relay
	server   *relayGatewayMockServer
	module   *Module
	children *legacyrelay.ChildRegistry
	child    legacyrelay.ProcessIdentity
	env      map[string]string
}

func newLegacyGatewayFixture(t *testing.T) *legacyGatewayFixture {
	t.Helper()
	server := &relayGatewayMockServer{}
	serverURL := startRelayGatewayMock(t, server)
	host, err := legacyrelay.NewProcessIdentity(100, "host-incarnation", "omp.exe", 1)
	if err != nil {
		t.Fatalf("host identity: %v", err)
	}
	child, err := legacyrelay.NewProcessIdentity(200, "child-incarnation", "engram.exe", 100)
	if err != nil {
		t.Fatalf("child identity: %v", err)
	}
	inspector := &relayGatewayInspector{processes: map[int]legacyrelay.ProcessIdentity{100: host, 200: child}}
	children := legacyrelay.NewChildRegistry(inspector, legacyrelay.ChildImageGateFunc(func(legacyrelay.ProcessImage) bool { return true }))
	env := map[string]string{
		config.EnvServerURL:               serverURL,
		config.EnvHAP01BRegistrationToken: testRegistration,
		config.EnvHAP01BProjectTokensJSON: `{"` + testRelayProject + `":"` + testProjectToken + `"}`,
	}
	if _, err := children.Observe(child.PID(), "diagnostic-project", env); err != nil {
		t.Fatalf("observe child: %v", err)
	}
	capabilities, err := legacyrelay.NewCapabilityRegistry(legacyrelay.CapabilityRegistryConfig{TTL: time.Minute, Now: time.Now, Inspector: inspector, Children: children})
	if err != nil {
		t.Fatalf("capability registry: %v", err)
	}
	module := NewModuleWithClientInstanceID("fixture-daemon-install")
	gateway, err := NewLegacyRelayGateway(module, children, capabilities)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	generation, _ := legacyrelay.NewDaemonGeneration("daemon-generation")
	adapter, _ := legacyrelay.NewAdapterAttestation(legacyrelay.AdapterRevisionHAP01B, strings.Repeat("a", 64))
	routes, _ := legacyrelay.NewRouteSet(legacyrelay.RouteSessionStartContext, legacyrelay.RouteAmbientCandidates)
	relay, err := legacyrelay.NewRelay(legacyrelay.RelayConfig{
		Generation: generation, AdapterGate: legacyrelay.ExactAdapterGate(adapter),
		Bootstrapper: legacyrelay.NewBootstrapper(children, inspector, 4), Capabilities: capabilities, Gateway: gateway,
		PeerResolver: legacyrelay.PeerResolverFunc(func(net.Conn) (int, error) { return host.PID(), nil }),
		Now:          time.Now, SafetyDeadline: 5 * time.Second, MaxFrameBytes: legacyrelay.DefaultMaxFrameBytes, CapabilityRoutes: routes,
	})
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	t.Cleanup(module.pool.closeAll)
	return &legacyGatewayFixture{relay: relay, server: server, module: module, children: children, child: child, env: env}
}

type gatewayWireResponse struct {
	Kind                string          `json:"kind"`
	Reason              string          `json:"reason"`
	SessionCapability   string          `json:"sessionCapability"`
	CanonicalProjectRef string          `json:"canonicalProjectRef"`
	Payload             json.RawMessage `json:"payload"`
	AdditionalContext   string          `json:"additionalContext"`
}

func (f *legacyGatewayFixture) call(t *testing.T, route string, body json.RawMessage) gatewayWireResponse {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.relay.ServeConn(context.Background(), server)
	}()
	frame := mustJSON(t, map[string]any{
		"protocol": legacyrelay.Protocol, "requestId": "request-" + route, "daemonGeneration": "daemon-generation",
		"adapter": map[string]any{"revision": legacyrelay.AdapterRevisionHAP01B, "installedArtifactSha256": strings.Repeat("a", 64)},
		"route":   route, "deadlineUnixMs": time.Now().Add(5 * time.Second).UnixMilli(), "body": body,
	})
	if _, err := fmt.Fprintf(client, "%s\n", frame); err != nil {
		t.Fatalf("write relay frame: %v", err)
	}
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read relay response: %v", err)
	}
	_ = client.Close()
	<-done
	var response gatewayWireResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("decode relay response %s: %v", line, err)
	}
	return response
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return encoded
}

func relayGatewayDescriptor() string {
	return `{"version":3,"anchor_project_id":"22222222-2222-4222-8222-222222222222","name":"daemon-v3","scope":"repository","normalized_git_remotes":["git.example.test/Platform/Daemon"],"legacy_identifiers":[],"client_instance_id":"fixture-daemon-install"}`
}

func countPoolTokenHash(pool *grpcPool, tokenHash string) int {
	count := 0
	pool.conns.Range(func(keyValue, _ any) bool {
		if key, ok := keyValue.(connKey); ok && key.tokenHash == tokenHash {
			count++
		}
		return true
	})
	return count
}
