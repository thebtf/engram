package grpcserver

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/worker/ambientcore"
	"github.com/thebtf/engram/pkg/cognitive"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	gormlib "gorm.io/gorm"
)

const hapBridgeRevision = "omp-hap-01b/1"

type hapBridgeProposer struct {
	calls int
}

func (*hapBridgeProposer) Name() string    { return "test.hap.bridge.proposer" }
func (*hapBridgeProposer) Version() string { return "v1" }
func (*hapBridgeProposer) Start(context.Context, cognitivecore.Dependencies) error {
	return nil
}
func (*hapBridgeProposer) Stop() error          { return nil }
func (*hapBridgeProposer) Implements() []string { return []string{"CandidateProposer"} }
func (p *hapBridgeProposer) Propose(_ context.Context, _ cognitive.AttentionEvent, _ int) ([]cognitive.HintProposal, error) {
	p.calls++
	return []cognitive.HintProposal{{ID: "ambient-1", Title: "Bridge ambient context"}}, nil
}

type hapBridgeEmitter struct {
	surfaces []cognitive.HintSurface
}

func (*hapBridgeEmitter) Name() string    { return "test.hap.bridge.emitter" }
func (*hapBridgeEmitter) Version() string { return "v1" }
func (*hapBridgeEmitter) Start(context.Context, cognitivecore.Dependencies) error {
	return nil
}
func (*hapBridgeEmitter) Stop() error          { return nil }
func (*hapBridgeEmitter) Implements() []string { return []string{"HintEmitter"} }
func (e *hapBridgeEmitter) Render(_ context.Context, surface cognitive.HintSurface, _ string, _ []cognitive.HintProposal) (cognitive.HintDelivery, error) {
	e.surfaces = append(e.surfaces, surface)
	return cognitive.HintDelivery{Surface: surface, AdditionalContext: "bounded bridge ambient context"}, nil
}

type recordingSessionStartCommitter struct {
	calls []sessionStartCommit
}

type sessionStartCommit struct {
	hostSessionRef string
	project        string
	memoryCount    int
}

func (c *recordingSessionStartCommitter) CommitSessionStartDelivery(hostSessionRef, canonicalProject string, memories []*pb.SessionStartMemory) {
	c.calls = append(c.calls, sessionStartCommit{hostSessionRef: hostSessionRef, project: canonicalProject, memoryCount: len(memories)})
}

func enableHAPBridge(t *testing.T) {
	t.Helper()
	t.Setenv("ENGRAM_HAP_01B_RELAY_ENABLED", "true")
	t.Setenv("ENGRAM_HAP_01B_RELAY_REVISION", hapBridgeRevision)
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "true")
}

func hapProjectContext() context.Context {
	expiresAt := time.Now().UTC().Add(time.Hour)
	identity := auth.ClientWithPrincipalExpiry("read-write", "project-keycard", auth.ProjectServicePrincipal(grpcV3ProjectKey), auth.PrincipalKindService, &expiresAt)
	return auth.WithIdentity(context.Background(), identity)
}

func hapResolution(t *testing.T) func(context.Context, *gormlib.DB, projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
	t.Helper()
	return func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}
}

func newHAPAmbientServer(t *testing.T) (*Server, *hapBridgeProposer, *hapBridgeEmitter, cognitivecore.HintQueue) {
	t.Helper()
	enableHAPBridge(t)
	registry := cognitivecore.NewRegistry()
	meter := cognitivecore.NewLocalMeter()
	queue := cognitivecore.NewHintQueue()
	proposer := &hapBridgeProposer{}
	emitter := &hapBridgeEmitter{}
	require.NoError(t, registry.Register(proposer))
	require.NoError(t, registry.Register(emitter))
	require.NoError(t, registry.Enable(proposer.Name()))
	require.NoError(t, registry.Enable(emitter.Name()))

	server := &Server{identityResolverV3: hapResolution(t)}
	server.SetAmbientDependencies(cognitivecoreToAmbientDependencies(registry, meter, queue))
	return server, proposer, emitter, queue
}

func cognitivecoreToAmbientDependencies(registry cognitivecore.SubsystemRegistry, meter cognitivecore.SubsystemMeter, queue cognitivecore.HintQueue) ambientcore.Dependencies {
	return ambientcore.Dependencies{
		Registry: registry,
		Meter:    meter,
		Queue:    queue,
		Flags:    cognitivecore.LoadFlagConfigFromEnv(),
	}
}

func hapAmbientRequest() *pb.GetAmbientCandidatesRequest {
	return &pb.GetAmbientCandidatesRequest{
		ProjectIdentityV3: grpcV3Identity(),
		HostSessionRef:    "host-session-ref",
		QueryText:         "Need bounded bridge ambient context",
		RelayRevision:     hapBridgeRevision,
	}
}

func TestRelayRegistrationUsesTypedAuthorizationPath(t *testing.T) {
	enableHAPBridge(t)
	store := &grpcRegistrationStoreV3{binding: projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingMissingV3}}
	server := &Server{identityResolverV3: grpcRegistrationResolverV3(t, store)}
	registrationIdentity := auth.ClientWithPrincipal("read-write", "registration-keycard", auth.RegistrationServicePrincipal("workstation-a"), auth.PrincipalKindService)

	response, err := server.RegisterProjectIdentityV3(auth.WithIdentity(context.Background(), registrationIdentity), &pb.RegisterProjectIdentityV3Request{
		ProjectIdentityV3: grpcV3Identity(),
		RelayRevision:     hapBridgeRevision,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, response.GetProjectResolutionV3().GetOutcome())
	require.Equal(t, 1, store.registrationCalls)

	wrongClass := auth.ClientWithPrincipal("read-write", "project-keycard", auth.ProjectServicePrincipal(grpcV3ProjectKey), auth.PrincipalKindService)
	response, err = server.RegisterProjectIdentityV3(auth.WithIdentity(context.Background(), wrongClass), &pb.RegisterProjectIdentityV3Request{
		ProjectIdentityV3: grpcV3Identity(),
		RelayRevision:     hapBridgeRevision,
	})
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, 1, store.registrationCalls, "wrong credential class must not reach V3 resolution")
}

func TestGenericGRPCRejectsEveryHAPCredentialClass(t *testing.T) {
	server := &Server{handler: staticMCPHandler{}, identityResolverV3: hapResolution(t)}
	registration := auth.ClientWithPrincipal("read-write", "registration-keycard", auth.RegistrationServicePrincipal("workstation-a"), auth.PrincipalKindService)
	response, err := server.CallTool(auth.WithIdentity(context.Background(), registration), &pb.CallToolRequest{
		ToolName: "recall_memory", ArgumentsJson: []byte(`{}`), ProjectIdentityV3: grpcV3Identity(),
	})
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	negotiated, err := server.NegotiateVersion(auth.WithIdentity(context.Background(), registration), &pb.NegotiateVersionRequest{ClientVersion: "v1.0.0"})
	require.Nil(t, negotiated)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	future := time.Now().UTC().Add(time.Hour)
	legacy := auth.ClientWithPrincipalExpiry("read-write", "legacy-keycard", auth.LegacyDirectPrincipal(grpcV3ProjectKey), auth.PrincipalKindAgent, &future)
	initialized, err := server.Initialize(auth.WithIdentity(context.Background(), legacy), &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()})
	require.Nil(t, initialized)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	wrongProject := auth.ClientWithPrincipal("read-write", "project-keycard", auth.ProjectServicePrincipal("22222222-2222-4222-8222-222222222222"), auth.PrincipalKindService)
	response, err = server.CallTool(auth.WithIdentity(context.Background(), wrongProject), &pb.CallToolRequest{
		ToolName: "recall_memory", ArgumentsJson: []byte(`{}`), ProjectIdentityV3: grpcV3Identity(),
	})
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	matchingProject := auth.ClientWithPrincipal("read-write", "project-keycard", auth.ProjectServicePrincipal(grpcV3ProjectKey), auth.PrincipalKindService)
	response, err = server.CallTool(auth.WithIdentity(context.Background(), matchingProject), &pb.CallToolRequest{
		ToolName: "vault", ArgumentsJson: []byte(`{"action":"get","name":"global-secret","scope":"global"}`), ProjectIdentityV3: grpcV3Identity(),
	})
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestRelaySessionStartRequiresV3WithoutRawProject(t *testing.T) {
	enableHAPBridge(t)
	server := &Server{}
	response, err := server.GetSessionStartContext(hapProjectContext(), &pb.GetSessionStartContextRequest{
		Project:           "raw-project-is-forbidden",
		ProjectIdentityV3: grpcV3Identity(),
		HostSessionRef:    "host-session-ref",
		RelayRevision:     hapBridgeRevision,
	})
	require.Nil(t, response)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestRelaySessionStartRejectsWrongResolvedProjectKeycard(t *testing.T) {
	enableHAPBridge(t)
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()
	server := &Server{db: db, identityResolverV3: hapResolution(t)}
	expiresAt := time.Now().UTC().Add(time.Hour)
	wrongProjectIdentity := auth.ClientWithPrincipalExpiry("read-write", "wrong-keycard", auth.ProjectServicePrincipal("another-project"), auth.PrincipalKindService, &expiresAt)

	response, err := server.GetSessionStartContext(auth.WithIdentity(context.Background(), wrongProjectIdentity), &pb.GetSessionStartContextRequest{
		ProjectIdentityV3: grpcV3Identity(),
		HostSessionRef:    "host-session-ref",
		RelayRevision:     hapBridgeRevision,
	})
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestGetAmbientCandidatesUsesSharedCoreAndCommitsLostResponse(t *testing.T) {
	server, proposer, emitter, queue := newHAPAmbientServer(t)
	require.NoError(t, queue.Enqueue(context.Background(), "host-session-ref", cognitivecore.HintProposalPayload{ID: "fallback-1", Title: "Fallback", CreatedAt: time.Now().UTC()}))

	response, err := server.GetAmbientCandidates(hapProjectContext(), hapAmbientRequest())
	require.NoError(t, err)
	require.Equal(t, "bounded bridge ambient context", response.GetAdditionalContext())
	require.Equal(t, 1, proposer.calls)
	require.Equal(t, []cognitive.HintSurface{cognitive.HintSurfaceUserPromptSubmit}, emitter.surfaces)
	// Deliberately discard response after formation: queue commit must already be
	// durable attempted-delivery state rather than dependent on client acknowledgement.
	require.Zero(t, queue.Stats("host-session-ref").QueuedNow)
}

func TestGetAmbientCandidatesRejectsWrongResolvedProjectKeycard(t *testing.T) {
	server, proposer, _, _ := newHAPAmbientServer(t)
	expiresAt := time.Now().UTC().Add(time.Hour)
	wrongProjectIdentity := auth.ClientWithPrincipalExpiry("read-write", "wrong-keycard", auth.ProjectServicePrincipal("another-project"), auth.PrincipalKindService, &expiresAt)
	response, err := server.GetAmbientCandidates(auth.WithIdentity(context.Background(), wrongProjectIdentity), hapAmbientRequest())
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Zero(t, proposer.calls)
}

func TestRelayBoundaryRejectsInvalidHostSessionAndOversizeQuery(t *testing.T) {
	server, proposer, _, _ := newHAPAmbientServer(t)
	for _, test := range []struct {
		name    string
		request *pb.GetAmbientCandidatesRequest
	}{
		{name: "control character host session", request: func() *pb.GetAmbientCandidatesRequest {
			request := hapAmbientRequest()
			request.HostSessionRef = "bad\nsession"
			return request
		}()},
		{name: "oversize query", request: func() *pb.GetAmbientCandidatesRequest {
			request := hapAmbientRequest()
			request.QueryText = strings.Repeat("x", maxAmbientQueryTextBytes+1)
			return request
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := server.GetAmbientCandidates(hapProjectContext(), test.request)
			require.Nil(t, response)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	require.Zero(t, proposer.calls, "invalid boundary input must not reach ambient core")
}

func TestRelaySessionStartCommitsAttemptBeforeResponseTransport(t *testing.T) {
	enableHAPBridge(t)
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()
	committer := &recordingSessionStartCommitter{}
	server := &Server{db: db, identityResolverV3: hapResolution(t)}
	server.SetSessionStartDeliveryCommitter(committer)

	response, err := server.GetSessionStartContext(hapProjectContext(), &pb.GetSessionStartContextRequest{
		ProjectIdentityV3: grpcV3Identity(),
		HostSessionRef:    "lost-session-start-response",
		RelayRevision:     hapBridgeRevision,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, committer.calls, 1)
	require.Equal(t, "lost-session-start-response", committer.calls[0].hostSessionRef)
	require.Equal(t, grpcV3ProjectKey, committer.calls[0].project)
}

func TestBridgeProtoFieldsAndGeneratedServiceMethodSet(t *testing.T) {
	sessionRequest := (&pb.GetSessionStartContextRequest{}).ProtoReflect().Descriptor()
	require.Equal(t, 6, sessionRequest.Fields().Len())
	require.EqualValues(t, 5, sessionRequest.Fields().ByName("host_session_ref").Number())
	require.EqualValues(t, 6, sessionRequest.Fields().ByName("relay_revision").Number())

	ambientRequest := (&pb.GetAmbientCandidatesRequest{}).ProtoReflect().Descriptor()
	require.Equal(t, 5, ambientRequest.Fields().Len())
	for name, number := range map[string]int{"project_identity_v3": 1, "host_session_ref": 2, "query_text": 3, "limit": 4, "relay_revision": 5} {
		require.EqualValues(t, number, ambientRequest.Fields().ByName(protoreflect.Name(name)).Number(), name)
	}
	ambientResponse := (&pb.GetAmbientCandidatesResponse{}).ProtoReflect().Descriptor()
	require.Equal(t, 1, ambientResponse.Fields().Len())
	require.EqualValues(t, 1, ambientResponse.Fields().ByName("additional_context").Number())

	service := sessionRequest.ParentFile().Services().ByName("EngramService")
	require.NotNil(t, service)
	methods := make([]string, 0, service.Methods().Len())
	for index := range service.Methods().Len() {
		methods = append(methods, string(service.Methods().Get(index).Name()))
	}
	sort.Strings(methods)
	require.Subset(t, methods, []string{
		"Advise",
		"Bind",
		"CallTool",
		"CodeIndexNegotiate",
		"CodeIndexUpload",
		"GetAmbientCandidates",
		"GetSessionStartContext",
		"Initialize",
		"NegotiateVersion",
		"Observe",
		"Ping",
		"ProjectEvents",
		"RegisterProjectIdentityV3",
		"SyncProjectState",
	})
}
