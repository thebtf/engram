package engramcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/legacyrelay"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const advisorFixtureToken = "engram_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type advisorPrewarmTestServer struct {
	pb.UnimplementedEngramServiceServer

	mu              sync.Mutex
	initializeProof []byte
	initializeErr   error
	initCalls       int
	initAuth        []string
	bindResponse    *pb.HostAdvisorBindResponse
	bindErr         error
	bindCalls       int
	bindAuth        []string
	bindRequests    []*pb.HostAdvisorBindRequest
}

func (s *advisorPrewarmTestServer) Initialize(ctx context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	s.mu.Lock()
	s.initCalls++
	s.initAuth = append(s.initAuth, advisorIncomingAuthorization(ctx))
	proof := bytes.Clone(s.initializeProof)
	err := s.initializeErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &pb.InitializeResponse{
		Tools:                           []*pb.ToolDefinition{{Name: "recall", Description: "fixture"}},
		AuthenticatedSubjectProofSha256: proof,
	}, nil
}

func (s *advisorPrewarmTestServer) Bind(ctx context.Context, request *pb.HostAdvisorBindRequest) (*pb.HostAdvisorBindResponse, error) {
	s.mu.Lock()
	s.bindCalls++
	s.bindAuth = append(s.bindAuth, advisorIncomingAuthorization(ctx))
	s.bindRequests = append(s.bindRequests, proto.Clone(request).(*pb.HostAdvisorBindRequest))
	response := s.bindResponse
	err := s.bindErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if response == nil {
		return &pb.HostAdvisorBindResponse{}, nil
	}
	return proto.Clone(response).(*pb.HostAdvisorBindResponse), nil
}

func (s *advisorPrewarmTestServer) setInitializeProof(proof []byte) {
	s.mu.Lock()
	s.initializeProof = bytes.Clone(proof)
	s.initializeErr = nil
	s.mu.Unlock()
}

func (s *advisorPrewarmTestServer) setBindFailure(err error) {
	s.mu.Lock()
	s.bindErr = err
	s.mu.Unlock()
}

func (s *advisorPrewarmTestServer) snapshot() advisorPrewarmServerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := make([]*pb.HostAdvisorBindRequest, len(s.bindRequests))
	for i, request := range s.bindRequests {
		requests[i] = proto.Clone(request).(*pb.HostAdvisorBindRequest)
	}
	return advisorPrewarmServerSnapshot{
		initCalls:    s.initCalls,
		initAuth:     append([]string(nil), s.initAuth...),
		bindCalls:    s.bindCalls,
		bindAuth:     append([]string(nil), s.bindAuth...),
		bindRequests: requests,
	}
}

type advisorPrewarmServerSnapshot struct {
	initCalls    int
	initAuth     []string
	bindCalls    int
	bindAuth     []string
	bindRequests []*pb.HostAdvisorBindRequest
}

func advisorIncomingAuthorization(ctx context.Context) string {
	metadataValues, _ := metadata.FromIncomingContext(ctx)
	authorization := metadataValues.Get("authorization")
	if len(authorization) == 0 {
		return ""
	}
	return authorization[0]
}

type advisorProcessInspector struct {
	mu        sync.Mutex
	processes map[int]legacyrelay.ProcessIdentity
}

func (i *advisorProcessInspector) Inspect(pid int) (legacyrelay.ProcessIdentity, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	process, ok := i.processes[pid]
	if !ok {
		return legacyrelay.ProcessIdentity{}, errors.New("process unavailable")
	}
	return process, nil
}

func (i *advisorProcessInspector) replace(pid int, process legacyrelay.ProcessIdentity) {
	i.mu.Lock()
	i.processes[pid] = process
	i.mu.Unlock()
}

type advisorPrewarmFixture struct {
	server            *advisorPrewarmTestServer
	serverURL         string
	module            *Module
	gateway           *LegacyRelayGateway
	children          *legacyrelay.ChildRegistry
	inspector         *advisorProcessInspector
	bootstrapper      legacyrelay.Bootstrapper
	host              legacyrelay.ProcessIdentity
	child             legacyrelay.ProcessIdentity
	generation        legacyrelay.DaemonGeneration
	profile           *AdvisorClientProfile
	proof             advisorSubjectProof
	env               map[string]string
	diagnosticProject string
}

func newAdvisorPrewarmFixture(t *testing.T) *advisorPrewarmFixture {
	t.Helper()
	proof := sha256.Sum256([]byte("advisor-initialize-subject"))
	server := &advisorPrewarmTestServer{initializeProof: proof[:]}
	serverURL := startAdvisorPrewarmServer(t, server)
	profile := newAdvisorFixtureProfile(t, "omp-1.0.0")

	host := advisorFixtureProcess(t, 100, "host-incarnation", "omp.exe", 1)
	child := advisorFixtureProcess(t, 200, "child-incarnation", "engram.exe", host.PID())
	inspector := &advisorProcessInspector{processes: map[int]legacyrelay.ProcessIdentity{
		host.PID():  host,
		child.PID(): child,
	}}
	children := legacyrelay.NewChildRegistry(inspector, legacyrelay.ChildImageGateFunc(func(legacyrelay.ProcessImage) bool { return true }))
	env := map[string]string{
		config.EnvServerURL:        serverURL,
		config.EnvWorkstationToken: advisorFixtureToken,
	}
	const diagnosticProject = "diagnostic-project"
	if _, err := children.Observe(child.PID(), diagnosticProject, env); err != nil {
		t.Fatalf("observe child: %v", err)
	}
	capabilities, err := legacyrelay.NewCapabilityRegistry(legacyrelay.CapabilityRegistryConfig{
		TTL:       time.Minute,
		Now:       time.Now,
		Inspector: inspector,
		Children:  children,
	})
	if err != nil {
		t.Fatalf("new capability registry: %v", err)
	}
	module := NewModule()
	gateway, err := NewLegacyRelayGatewayWithAdvisorProfile(module, children, capabilities, profile)
	if err != nil {
		t.Fatalf("new advisor gateway: %v", err)
	}
	generation, err := legacyrelay.NewDaemonGeneration("daemon-generation")
	if err != nil {
		t.Fatalf("daemon generation: %v", err)
	}
	bootstrapper := legacyrelay.NewBootstrapper(children, inspector, 4)
	server.bindResponse = &pb.HostAdvisorBindResponse{Binding: advisorFixtureBinding(profile, proof, "binding-fixture", time.Now().Add(time.Minute))}
	t.Cleanup(module.pool.closeAll)
	return &advisorPrewarmFixture{
		server:            server,
		serverURL:         serverURL,
		module:            module,
		gateway:           gateway,
		children:          children,
		inspector:         inspector,
		bootstrapper:      bootstrapper,
		host:              host,
		child:             child,
		generation:        generation,
		profile:           profile,
		proof:             advisorSubjectProof(proof),
		env:               cloneAdvisorEnv(env),
		diagnosticProject: diagnosticProject,
	}
}

func startAdvisorPrewarmServer(t *testing.T, server *advisorPrewarmTestServer) string {
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

func newAdvisorFixtureProfile(t *testing.T, hostVersion string) *AdvisorClientProfile {
	t.Helper()
	artifact := sha256.Sum256([]byte("advisor-installed-artifact"))
	profile, err := NewAdvisorClientProfile(AdvisorClientProfileSpec{
		HostVersion:             hostVersion,
		AdapterID:               "engram-memory",
		AdapterVersion:          "1.0.0",
		InstalledArtifactSHA256: artifact,
		RuntimeProbeReceiptID:   "runtime-probe-fixture",
		SnapshotID:              "omp-advisor-fixture",
		SnapshotRevision:        1,
		CallbackDeadline:        250 * time.Millisecond,
		BindingTTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("new advisor profile: %v", err)
	}
	return profile
}

func advisorFixtureBinding(profile *AdvisorClientProfile, proof [sha256.Size]byte, bindingID string, expiry time.Time) *pb.HostBinding {
	contract := sha256.Sum256([]byte("advisor-contract-fixture"))
	return &pb.HostBinding{
		BindingId: bindingID,
		CapabilitySnapshot: &pb.AcceptedCapabilitySnapshot{
			SnapshotId:         profile.snapshotID,
			ContractSha256:     contract[:],
			Revision:           profile.snapshotRevision,
			CapabilityRevision: profile.capabilityRevision,
			Capabilities:       []*pb.HostCapability{profile.requestedCapability()},
		},
		ExpiresAt:                       timestamppb.New(expiry),
		CallbackDeadlineMs:              profile.callbackDeadlineMS,
		AuthenticatedSubjectProofSha256: proof[:],
	}
}

func advisorFixtureProcess(t *testing.T, pid int, incarnation, image string, parentPID int) legacyrelay.ProcessIdentity {
	t.Helper()
	process, err := legacyrelay.NewProcessIdentity(pid, incarnation, image, parentPID)
	if err != nil {
		t.Fatalf("new process identity: %v", err)
	}
	return process
}

func cloneAdvisorEnv(env map[string]string) map[string]string {
	clone := make(map[string]string, len(env))
	for key, value := range env {
		clone[key] = value
	}
	return clone
}

func advisorPrewarmContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (f *advisorPrewarmFixture) proxyTools(projectID, cwd string) error {
	project := muxcore.ProjectContext{ID: projectID, Cwd: cwd, Env: cloneAdvisorEnv(f.env)}
	key := cacheKey(project)
	f.module.cache.entries.Store(key, resolvedSlug{id: "compatibility-project"})
	f.module.cache.identities.Store(key, &pb.ProjectIdentityV2{Version: 1, LegacyProjectId: "compatibility-project"})
	_, err := f.module.ProxyTools(context.Background(), project)
	return err
}

func (f *advisorPrewarmFixture) requireProxyTools(t *testing.T, projectID, cwd string) {
	t.Helper()
	if err := f.proxyTools(projectID, cwd); err != nil {
		t.Fatalf("ProxyTools: %v", err)
	}
}

func (f *advisorPrewarmFixture) selection(t *testing.T) legacyrelay.BootstrapSelection {
	t.Helper()
	selection, err := f.bootstrapper.SelectPeer(f.host.PID())
	if err != nil {
		t.Fatalf("select peer: %v", err)
	}
	return selection
}

func (f *advisorPrewarmFixture) channel(t *testing.T, selection legacyrelay.BootstrapSelection, generation legacyrelay.DaemonGeneration) (advisorBindingChannel, [sha256.Size]byte) {
	t.Helper()
	childConfig, err := f.gateway.advisorConfigFor(selection.Child())
	if err != nil {
		t.Fatalf("advisor child config: %v", err)
	}
	proofKey, proof, ok, err := f.module.advisorProofs.lookup(childConfig.serverURL, childConfig.token)
	if err != nil || !ok {
		t.Fatalf("advisor proof lookup ok=%t err=%v", ok, err)
	}
	runtimeRef, err := selection.RuntimeInstanceRef(generation)
	if err != nil {
		t.Fatalf("runtime reference: %v", err)
	}
	hello, err := f.gateway.advisorProfile.hello(runtimeRef)
	if err != nil {
		t.Fatalf("advisor hello: %v", err)
	}
	material, err := f.gateway.advisorProfile.materialDigest(proofKey.authority, proof, hello)
	if err != nil {
		t.Fatalf("advisor material: %v", err)
	}
	return advisorBindingChannel{
		authority:  proofKey.authority,
		subject:    proof,
		hostFamily: hello.GetHost().GetFamily(),
		adapterID:  hello.GetHost().GetAdapterId(),
		runtimeRef: hello.GetHost().GetRuntimeInstanceRef(),
	}, material
}

func TestProxyToolsCachesOnlyAcceptedInitializeSubjectProof(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	projectCWD := t.TempDir()
	fixture.requireProxyTools(t, "project-one", projectCWD)

	_, proof, ok, err := fixture.module.advisorProofs.lookup(fixture.serverURL, advisorFixtureToken)
	if err != nil || !ok || proof != fixture.proof {
		t.Fatalf("accepted proof lookup proof=%x ok=%t err=%v", proof, ok, err)
	}

	fixture.server.setInitializeProof(nil)
	fixture.requireProxyTools(t, "project-two", t.TempDir())
	_, _, ok, err = fixture.module.advisorProofs.lookup(fixture.serverURL, advisorFixtureToken)
	if err != nil || ok {
		t.Fatalf("empty old-server proof remained cached ok=%t err=%v", ok, err)
	}

	fixture.server.setInitializeProof([]byte("malformed"))
	if err := fixture.proxyTools("project-three", t.TempDir()); err == nil {
		t.Fatal("ProxyTools accepted malformed non-empty subject proof")
	}
	_, _, ok, err = fixture.module.advisorProofs.lookup(fixture.serverURL, advisorFixtureToken)
	if err != nil || ok {
		t.Fatalf("malformed proof remained cached ok=%t err=%v", ok, err)
	}
}

func TestAdvisorProofCacheIsBounded(t *testing.T) {
	cache := newAdvisorProofCache()
	proof := sha256.Sum256([]byte("bounded-proof"))
	for index := range maxAdvisorSubjectProofCacheItems {
		serverURL := fmt.Sprintf("http://127.0.0.1:%d", 30000+index)
		if err := cache.record(serverURL, advisorFixtureToken, proof[:]); err != nil {
			t.Fatalf("record proof %d: %v", index, err)
		}
	}
	if err := cache.record("http://127.0.0.1:40000", advisorFixtureToken, proof[:]); err == nil {
		t.Fatal("proof cache accepted an unbounded authority")
	}
}

func TestPrewarmAdvisorBindingRequiresLiveDeadline(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	fixture.requireProxyTools(t, "project-one", t.TempDir())
	request := AdvisorPrewarmRequest{Bootstrap: fixture.selection(t), Generation: fixture.generation}
	if _, err := fixture.gateway.PrewarmAdvisorBinding(context.Background(), request); err == nil {
		t.Fatal("prewarm accepted an unbounded context")
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := fixture.gateway.PrewarmAdvisorBinding(expired, request); err == nil {
		t.Fatal("prewarm accepted an expired deadline")
	}
}

func TestPrewarmAdvisorBindingCorrelatesInitializeProofAndOrdinaryToken(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	fixture.requireProxyTools(t, "project-one", t.TempDir())
	selection := fixture.selection(t)
	channel, material := fixture.channel(t, selection, fixture.generation)

	binding, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{
		Bootstrap:  selection,
		Generation: fixture.generation,
	})
	if err != nil {
		t.Fatalf("PrewarmAdvisorBinding: %v", err)
	}
	if !bytes.Equal(binding.GetAuthenticatedSubjectProofSha256(), fixture.proof[:]) {
		t.Fatalf("binding proof=%x want Initialize proof=%x", binding.GetAuthenticatedSubjectProofSha256(), fixture.proof)
	}
	cached, ok := fixture.gateway.advisorBindings.cached(channel, material, time.Now())
	if !ok || !proto.Equal(cached, binding) {
		t.Fatalf("validated binding was not cached cached=%#v ok=%t", cached, ok)
	}

	snapshot := fixture.server.snapshot()
	if snapshot.initCalls != 1 || snapshot.bindCalls != 1 || len(snapshot.initAuth) != 1 || len(snapshot.bindAuth) != 1 {
		t.Fatalf("server calls=%#v", snapshot)
	}
	if snapshot.initAuth[0] != "Bearer "+advisorFixtureToken || snapshot.bindAuth[0] != "Bearer "+advisorFixtureToken {
		t.Fatalf("ordinary token custody init=%q bind=%q", snapshot.initAuth, snapshot.bindAuth)
	}
	if _, hasRegistration := fixture.env[config.EnvHAP01BRegistrationToken]; hasRegistration {
		t.Fatal("advisor fixture unexpectedly supplied a HAP registration token")
	}
	if _, hasProjectTokens := fixture.env[config.EnvHAP01BProjectTokensJSON]; hasProjectTokens {
		t.Fatal("advisor fixture unexpectedly supplied HAP project tokens")
	}
	runtimeRef, err := selection.RuntimeInstanceRef(fixture.generation)
	if err != nil {
		t.Fatalf("runtime reference: %v", err)
	}
	expectedHello, err := fixture.profile.hello(runtimeRef)
	if err != nil {
		t.Fatalf("expected hello: %v", err)
	}
	if !proto.Equal(snapshot.bindRequests[0].GetHello(), expectedHello) {
		t.Fatalf("Bind hello=%v want %v", snapshot.bindRequests[0].GetHello(), expectedHello)
	}
}

func TestPrewarmAdvisorBindingRejectsMismatchedResponseProof(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	fixture.requireProxyTools(t, "project-one", t.TempDir())
	selection := fixture.selection(t)
	channel, material := fixture.channel(t, selection, fixture.generation)
	wrongProof := sha256.Sum256([]byte("different-subject"))
	fixture.server.mu.Lock()
	fixture.server.bindResponse = &pb.HostAdvisorBindResponse{Binding: advisorFixtureBinding(fixture.profile, wrongProof, "binding-wrong-proof", time.Now().Add(time.Minute))}
	fixture.server.mu.Unlock()

	if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: fixture.generation}); err == nil {
		t.Fatal("PrewarmAdvisorBinding accepted a Bind response for a different Initialize subject")
	}
	if _, ok := fixture.gateway.advisorBindings.cached(channel, material, time.Now()); ok {
		t.Fatal("proof-mismatched Bind response entered the local cache")
	}
}

func TestPrewarmAdvisorBindingRejectsOverlongExpiryAndUnknownResponse(t *testing.T) {
	t.Run("overlong expiry", func(t *testing.T) {
		fixture := newAdvisorPrewarmFixture(t)
		fixture.requireProxyTools(t, "project-one", t.TempDir())
		fixture.server.mu.Lock()
		fixture.server.bindResponse = &pb.HostAdvisorBindResponse{Binding: advisorFixtureBinding(fixture.profile, sha256.Sum256([]byte("advisor-initialize-subject")), "binding-overlong", time.Now().Add(2*time.Minute))}
		fixture.server.mu.Unlock()
		if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: fixture.selection(t), Generation: fixture.generation}); err == nil {
			t.Fatal("prewarm accepted an expiry longer than the injected profile TTL")
		}
	})

	t.Run("unknown response field", func(t *testing.T) {
		fixture := newAdvisorPrewarmFixture(t)
		fixture.requireProxyTools(t, "project-one", t.TempDir())
		fixture.server.mu.Lock()
		fixture.server.bindResponse.ProtoReflect().SetUnknown([]byte{0x18, 0x01})
		fixture.server.mu.Unlock()
		if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: fixture.selection(t), Generation: fixture.generation}); err == nil {
			t.Fatal("prewarm accepted an unknown response field")
		}
	})
}

func TestPrewarmAdvisorBindingExactRetryCallsServerAgain(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	fixture.requireProxyTools(t, "project-one", t.TempDir())
	selection := fixture.selection(t)

	first, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: fixture.generation})
	if err != nil {
		t.Fatalf("first prewarm: %v", err)
	}
	second, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: fixture.generation})
	if err != nil {
		t.Fatalf("exact retry prewarm: %v", err)
	}
	if first.GetBindingId() != second.GetBindingId() || first.GetExpiresAt().AsTime() != second.GetExpiresAt().AsTime() {
		t.Fatalf("exact retry bindings first=%v second=%v", first, second)
	}
	if got := fixture.server.snapshot().bindCalls; got != 2 {
		t.Fatalf("Bind calls=%d, want 2 because each qualified session start revalidates", got)
	}
}

func TestPrewarmAdvisorBindingMaterialFailureRemovesCurrentCache(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	fixture.requireProxyTools(t, "project-one", t.TempDir())
	selection := fixture.selection(t)
	originalChannel, originalMaterial := fixture.channel(t, selection, fixture.generation)
	if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: fixture.generation}); err != nil {
		t.Fatalf("initial prewarm: %v", err)
	}
	if _, ok := fixture.gateway.advisorBindings.cached(originalChannel, originalMaterial, time.Now()); !ok {
		t.Fatal("initial binding was not cached")
	}

	fixture.gateway.advisorProfile = newAdvisorFixtureProfile(t, "omp-2.0.0")
	currentChannel, currentMaterial := fixture.channel(t, selection, fixture.generation)
	if currentChannel != originalChannel || currentMaterial == originalMaterial {
		t.Fatalf("material change channel=%#v materialChanged=%t", currentChannel, currentMaterial != originalMaterial)
	}
	fixture.server.setBindFailure(status.Error(codes.Unavailable, "fixture network failure"))
	if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: fixture.generation}); err == nil {
		t.Fatal("material-change prewarm unexpectedly succeeded")
	}
	if _, ok := fixture.gateway.advisorBindings.cached(originalChannel, originalMaterial, time.Now()); ok {
		t.Fatal("failed current Bind exposed the stale material cache")
	}
	if _, ok := fixture.gateway.advisorBindings.cached(currentChannel, currentMaterial, time.Now()); ok {
		t.Fatal("failed current Bind cached a replacement binding")
	}
}

func TestPrewarmAdvisorBindingRuntimeFailureCannotExposeOldChannel(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	fixture.requireProxyTools(t, "project-one", t.TempDir())
	selection := fixture.selection(t)
	if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: fixture.generation}); err != nil {
		t.Fatalf("initial prewarm: %v", err)
	}
	nextGeneration, err := legacyrelay.NewDaemonGeneration("daemon-generation-next")
	if err != nil {
		t.Fatalf("next generation: %v", err)
	}
	nextChannel, nextMaterial := fixture.channel(t, selection, nextGeneration)
	fixture.server.setBindFailure(status.Error(codes.Unavailable, "fixture network failure"))
	if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: nextGeneration}); err == nil {
		t.Fatal("runtime-change prewarm unexpectedly succeeded")
	}
	if _, ok := fixture.gateway.advisorBindings.cached(nextChannel, nextMaterial, time.Now()); ok {
		t.Fatal("runtime-change failure exposed a stale binding under the new channel")
	}
}

func TestPrewarmAdvisorBindingChildReplacementInvalidatesCache(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	fixture.requireProxyTools(t, "project-one", t.TempDir())
	selection := fixture.selection(t)
	channel, material := fixture.channel(t, selection, fixture.generation)
	if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: fixture.generation}); err != nil {
		t.Fatalf("initial prewarm: %v", err)
	}
	if _, ok := fixture.gateway.advisorBindings.cached(channel, material, time.Now()); !ok {
		t.Fatal("initial binding was not cached")
	}

	rotated := cloneAdvisorEnv(fixture.env)
	rotated["ADVISOR_CHILD_ROTATED"] = "yes"
	if _, err := fixture.children.Observe(fixture.child.PID(), fixture.diagnosticProject, rotated); err != nil {
		t.Fatalf("rotate child configuration: %v", err)
	}
	if _, ok := fixture.gateway.advisorBindings.cached(channel, material, time.Now()); ok {
		t.Fatal("child replacement retained a cached advisor binding")
	}
}

func TestPrewarmAdvisorBindingHasNoProjectOrCwdAuthority(t *testing.T) {
	fixture := newAdvisorPrewarmFixture(t)
	projectOne, cwdOne := "project-selector-one", t.TempDir()
	projectTwo, cwdTwo := "project-selector-two", t.TempDir()
	fixture.requireProxyTools(t, projectOne, cwdOne)
	fixture.requireProxyTools(t, projectTwo, cwdTwo)

	fixture.module.advisorProofs.mu.RLock()
	proofEntries := len(fixture.module.advisorProofs.entries)
	fixture.module.advisorProofs.mu.RUnlock()
	if proofEntries != 1 {
		t.Fatalf("proof cache entries=%d, want one authority/token channel across projects", proofEntries)
	}

	selection := fixture.selection(t)
	if _, err := fixture.gateway.PrewarmAdvisorBinding(advisorPrewarmContext(t), AdvisorPrewarmRequest{Bootstrap: selection, Generation: fixture.generation}); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	snapshot := fixture.server.snapshot()
	if len(snapshot.bindRequests) != 1 {
		t.Fatalf("Bind requests=%d", len(snapshot.bindRequests))
	}
	encodedHello, err := proto.Marshal(snapshot.bindRequests[0].GetHello())
	if err != nil {
		t.Fatalf("marshal HostHello: %v", err)
	}
	for _, forbidden := range []string{projectOne, projectTwo, cwdOne, cwdTwo, "diagnostic-project"} {
		if bytes.Contains(encodedHello, []byte(forbidden)) {
			t.Fatalf("HostHello carried forbidden project/cwd authority %q", forbidden)
		}
	}
}

func TestCurrentRegisterIdentityAndOnSessionConnectDoNotBind(t *testing.T) {
	fixture := newLegacyGatewayFixture(t)
	fixture.gateway.advisorProfile = newAdvisorFixtureProfile(t, "omp-1.0.0")
	proof := sha256.Sum256([]byte("legacy-route-proof"))
	fixture.env[config.EnvWorkstationToken] = advisorFixtureToken
	if _, err := fixture.children.Observe(fixture.child.PID(), "diagnostic-project", fixture.env); err != nil {
		t.Fatalf("refresh legacy child configuration: %v", err)
	}
	if err := fixture.module.advisorProofs.record(fixture.env[config.EnvServerURL], advisorFixtureToken, proof[:]); err != nil {
		t.Fatalf("cache proof: %v", err)
	}

	identity := fixture.call(t, "IDENTITY_REGISTRATION", []byte(`{"hostSessionRef":"host-session","projectIdentityV3":`+relayGatewayDescriptor()+`}`))
	if identity.Kind != "OK" {
		t.Fatalf("current RegisterIdentity response=%#v", identity)
	}
	fixture.module.OnSessionConnect(muxcore.ProjectContext{
		ID:  "ordinary-project",
		Cwd: t.TempDir(),
		Env: cloneAdvisorEnv(fixture.env),
	})

	fixture.server.mu.Lock()
	bindCalls := fixture.server.bindCalls
	fixture.server.mu.Unlock()
	if bindCalls != 0 {
		t.Fatalf("current RegisterIdentity or OnSessionConnect called Bind %d times", bindCalls)
	}
}
