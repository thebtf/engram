package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func TestUCICompletionGRPCRejectsUnauthorizedParentContext(t *testing.T) {
	fixture := newWorkerUCICompletionAuthorityFixture(t)
	fixture.accept(t, fixture.callerB, fixture.request(fixture.projectB, fixture.parentB))
	control := fixture.children(t, fixture.parentB)
	require.Len(t, control, 1, "service B must authenticate through the real interceptor and persist its authorized control")
	fixture.accept(t, fixture.callerB, fixture.request(fixture.projectB, fixture.parentB))
	require.Equal(t, control, fixture.children(t, fixture.parentB), "an authorized same-key retry must preserve the original child")

	request := fixture.request(fixture.projectB, fixture.parentA)
	fixture.deny(t, fixture.callerB, request)
	require.Empty(t, fixture.children(t, fixture.parentA), "matching service B/request B must not append a child to unauthorized parent A")
	state, err := fixture.exposures.CompletionState(context.Background(), fixture.parentA)
	require.NoError(t, err)
	require.Equal(t, uci.QueryCompletionUnknown, state)
	require.Equal(t, control, fixture.children(t, fixture.parentB))
}

func TestUCICompletionGRPCReauthorizesExactReplay(t *testing.T) {
	fixture := newWorkerUCICompletionAuthorityFixture(t)
	original := fixture.request(fixture.projectA, fixture.parentA)
	fixture.accept(t, fixture.callerA, original)
	before := fixture.children(t, fixture.parentA)
	require.Len(t, before, 1)
	fixture.accept(t, fixture.callerA, original)
	require.Equal(t, before, fixture.children(t, fixture.parentA))

	foreignReplay := proto.Clone(original).(*pb.RecordUCICompletionRequest)
	foreignReplay.CanonicalProject = fixture.projectB
	fixture.deny(t, fixture.callerB, foreignReplay)
	require.Equal(t, before, fixture.children(t, fixture.parentA), "the idempotency lookup must not authorize a foreign caller or rewrite evidence")

	foreignReplay.Outcome = pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_FAILED
	fixture.deny(t, fixture.callerB, foreignReplay)
	require.Equal(t, before, fixture.children(t, fixture.parentA), "a denied caller must not reach the idempotency-mismatch oracle")
}

func TestUCICompletionGRPCAllowsAuthorizedSharedSourceHandoff(t *testing.T) {
	fixture := newWorkerUCICompletionAuthorityFixture(t)
	original := fixture.request(fixture.projectA, fixture.parentA)
	fixture.accept(t, fixture.callerA, original)
	before := fixture.children(t, fixture.parentA)
	require.Len(t, before, 1)

	// Transfer only this synthetic checkout; source equality alone did not grant B access.
	require.NoError(t, fixture.store.GetDB().Model(&gormstore.UCICheckout{}).
		Where("checkout_id = ?", fixture.refA.CheckoutID).
		Updates(map[string]any{"owner_principal": fixture.identityB.Principal, "workstation_id": fixture.identityB.WorkstationID()}).Error)
	fixture.requireAuthorized(t, fixture.identityB, fixture.refA)
	fixture.requireUnauthorized(t, fixture.identityA, fixture.refA)

	replay := proto.Clone(original).(*pb.RecordUCICompletionRequest)
	replay.CanonicalProject = fixture.projectB
	fixture.accept(t, fixture.callerB, replay)
	require.Equal(t, before, fixture.children(t, fixture.parentA), "a genuinely authorized handoff must retain exact retries across project-service identities")

	newCallback := proto.Clone(replay).(*pb.RecordUCICompletionRequest)
	newCallback.CallbackRef = "handoff-callback"
	newCallback.IdempotencyKey = "handoff-idempotency"
	fixture.accept(t, fixture.callerB, newCallback)
	after := fixture.children(t, fixture.parentA)
	require.Len(t, after, 2)
	fixture.accept(t, fixture.callerB, newCallback)
	require.Equal(t, after, fixture.children(t, fixture.parentA))

	fixture.deny(t, fixture.callerA, original)
	require.Equal(t, after, fixture.children(t, fixture.parentA), "the prior owner must lose even the exact-replay path after handoff")
}

func TestUCICompletionGRPCRejectsInactiveParentView(t *testing.T) {
	fixture := newWorkerUCICompletionAuthorityFixture(t)
	fixture.accept(t, fixture.callerA, fixture.request(fixture.projectA, fixture.parentA))
	before := fixture.children(t, fixture.parentA)
	require.Len(t, before, 1)
	// Parent A is historical, so retiring it does not alter a current-View pointer.
	require.NoError(t, fixture.store.GetDB().Model(&gormstore.UCIView{}).
		Where("view_id = ?", fixture.refA.ViewID).Update("state", gormstore.UCIViewRetired).Error)
	fixture.requireUnauthorized(t, fixture.identityA, fixture.refA)

	fixture.deny(t, fixture.callerA, fixture.request(fixture.projectA, fixture.parentA))
	require.Equal(t, before, fixture.children(t, fixture.parentA), "retired parent Views must not retain an idempotent admission bypass")
	newCallback := fixture.request(fixture.projectA, fixture.parentA)
	newCallback.IdempotencyKey = "retired-view-new-child"
	newCallback.CallbackRef = "retired-view-new-callback"
	fixture.deny(t, fixture.callerA, newCallback)
	require.Equal(t, before, fixture.children(t, fixture.parentA), "a retired parent View must not receive a new durable child")
}

type workerUCICompletionAuthorityFixture struct {
	store       *gormstore.Store
	composition *uciContextComposition
	exposures   *gormstore.UCIExposureStore
	client      pb.EngramServiceClient
	callerA     context.Context
	callerB     context.Context
	identityA   auth.Identity
	identityB   auth.Identity
	projectA    string
	projectB    string
	refA        uci.ContextRef
	refB        uci.ContextRef
	parentA     string
	parentB     string
}

func newWorkerUCICompletionAuthorityFixture(t *testing.T) *workerUCICompletionAuthorityFixture {
	t.Helper()
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	store := openWorkerUCIContextCompositionStore(t)
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "completion-authority-postgres"})
	composition, err := composeUCIContext(true, store.GetDB(), mcpServer, workerUCISemanticConfig())
	require.NoError(t, err)
	published := newWorkerUCIApplicationFixture(t, composition)
	fixture := &workerUCICompletionAuthorityFixture{
		store: store, composition: composition, exposures: gormstore.NewUCIExposureStore(store.GetDB()),
		projectA: uuid.NewString(), projectB: uuid.NewString(), refA: published.historical.Context,
	}

	tokens := gormstore.NewTokenStore(store)
	validator := auth.NewValidator("", tokens)
	var previousRaw string
	createCaller := func(project, label string) (auth.Identity, context.Context) {
		randomBytes := make([]byte, auth.TokenBodyLen/2)
		_, randomErr := rand.Read(randomBytes)
		require.NoError(t, randomErr)
		raw := auth.TokenRawPrefix + hex.EncodeToString(randomBytes)
		require.True(t, raw != previousRaw, "fixture service tokens must be distinct")
		previousRaw = raw
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, hashErr)
		_, createErr := tokens.CreateWithPrincipal(context.Background(), gormstore.TokenCreatePrincipalInput{
			Name: "completion-service-" + label, TokenHash: string(hash), TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen], Scope: "read-write",
			Principal: auth.ProjectServicePrincipal(project), PrincipalKind: string(auth.PrincipalKindService),
		})
		require.NoError(t, createErr)
		identity, validateErr := validator.Validate(context.Background(), raw)
		require.NoError(t, validateErr, "synthetic fixture credentials must be valid before a denial can establish parent authorization")
		require.Equal(t, auth.SourceClient, identity.Source)
		require.True(t, identity.IsHAPProjectServiceFor(project))
		caller := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
			"authorization", "Bearer "+raw, auditcontext.SourceSessionMetadataKey, "completion-session-"+label,
		))
		return identity, caller
	}
	fixture.identityA, fixture.callerA = createCaller(fixture.projectA, "a")
	fixture.identityB, fixture.callerB = createCaller(fixture.projectB, "b")
	require.NoError(t, store.GetDB().Model(&gormstore.UCICheckout{}).
		Where("checkout_id = ?", published.checkout.CheckoutID).
		Updates(map[string]any{"owner_principal": fixture.identityA.Principal, "workstation_id": fixture.identityA.WorkstationID()}).Error)

	// Both services have a real authorized View of the same Source, but not the same checkout.
	checkoutB, err := composition.contextStore.RegisterCheckout(context.Background(), gormstore.RegisterCheckoutInput{
		SourceID: published.source.SourceID, WorkstationID: fixture.identityB.WorkstationID(), Kind: gormstore.UCICheckoutWorkingTree,
		OwnerPrincipal: fixture.identityB.Principal, LocatorRef: "file:///completion-authority/b/" + uuid.NewString(),
	})
	require.NoError(t, err)
	artifactB := workerUCIApplicationAddArtifact(t, composition.projectionStore, workerUCIApplicationArtifactInput{
		sourceID: published.source.SourceID, profileDigest: published.profile.ParserBundleDigest,
		label: "completion-authorized-b", name: "CompletionControl", symbol: "completion-control", source: "func CompletionControl() {}\n",
	})
	memberships := []uci.IndexMembership{workerUCIApplicationMembership("internal/completion.go", artifactB)}
	replacements := []uci.IndexEdgeReplacement{{SourcePath: "internal/completion.go"}}
	publisher, err := composition.projectionStore.Publisher(composition.authorizer, uci.IndexPublicationConfig{Limits: uci.DefaultIndexPublicationLimits()})
	require.NoError(t, err)
	viewB := workerUCIApplicationPublish(t, publisher, uci.IndexCaller{
		AuthRealm: string(auth.SourceClient), Principal: fixture.identityB.Principal, OwnerInstance: "completion-authority-b",
	}, workerUCIApplicationPublicationInput{
		checkout: checkoutB, profileID: published.profile.ProfileID, key: "completion-authority-b", jobKind: uci.IndexJobInitial,
		part:        workerUCIApplicationPart([]workerUCIApplicationArtifact{artifactB}, memberships, replacements),
		memberships: memberships, replacements: replacements, sequence: 1,
	})
	fixture.refB = viewB.Context
	require.Equal(t, fixture.refA.SourceID, fixture.refB.SourceID)
	require.NotEqual(t, fixture.refA.CheckoutID, fixture.refB.CheckoutID)
	fixture.requireAuthorized(t, fixture.identityA, fixture.refA)
	fixture.requireAuthorized(t, fixture.identityB, fixture.refB)
	fixture.requireUnauthorized(t, fixture.identityB, fixture.refA)
	fixture.parentA = fixture.appendParent(t, fixture.refA, "a")
	fixture.parentB = fixture.appendParent(t, fixture.refB, "b")

	adapter := &mcpHandlerAdapter{mcpServer: mcpServer}
	server, internal := grpcserver.New(adapter, validator)
	internal.SetDB(store.GetDB())
	internal.SetUCITransport(composition.transport)
	internal.SetUCICompletionRecorder(adapter)
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///uci-completion-authority",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	fixture.client = pb.NewEngramServiceClient(connection)
	return fixture
}

func (fixture *workerUCICompletionAuthorityFixture) authorization(identity auth.Identity, ref uci.ContextRef) error {
	_, err := fixture.composition.resolver.Authorize(context.Background(), uci.ResolveContextInput{
		ClientSessionID: "completion-authority-check", AuthRealm: string(identity.Source), Principal: identity.Principal,
		WorkstationID: identity.WorkstationID(), Ref: &ref,
	})
	return err
}

func (fixture *workerUCICompletionAuthorityFixture) requireAuthorized(t *testing.T, identity auth.Identity, ref uci.ContextRef) {
	t.Helper()
	require.NoError(t, fixture.authorization(identity, ref), "the real catalog and UCIContextAuthorizer must admit the positive control")
}

func (fixture *workerUCICompletionAuthorityFixture) requireUnauthorized(t *testing.T, identity auth.Identity, ref uci.ContextRef) {
	t.Helper()
	require.Error(t, fixture.authorization(identity, ref), "the real catalog/authorizer must establish that this exact parent is unauthorized")
}

func (fixture *workerUCICompletionAuthorityFixture) appendParent(t *testing.T, ref uci.ContextRef, label string) string {
	t.Helper()
	digest := workerUCIApplicationDigest
	record := uci.ExposureRecord{
		AuthRealm: string(auth.SourceClient), SourceID: ref.SourceID, CheckoutID: ref.CheckoutID, ViewID: ref.ViewID,
		ClientRef: digest("client-" + label), ClientSessionRef: digest("session-" + label), RequestRef: digest("request-" + label),
		Operation: uci.ExposureOperationCodeSearch, Result: uci.ExposureResultOK, Retrieval: uci.ExposureRetrievalLexical,
		Coverage: uci.ExposureCoverageComplete, Evidence: uci.ExposureEvidenceFTS, Certainty: uci.ExposureCertaintyEstablished,
		IdempotencyKey: digest("idempotency-" + label), RecordedAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
	}
	binding, err := uci.CanonicalExposureBindingDigest(record)
	require.NoError(t, err)
	record.BindingDigest = binding
	parent, err := fixture.exposures.AppendExposure(context.Background(), record)
	require.NoError(t, err)
	require.True(t, uci.ValidExposureRef(parent.ExposureRef))
	return parent.ExposureRef
}

func (fixture *workerUCICompletionAuthorityFixture) request(project, parent string) *pb.RecordUCICompletionRequest {
	return &pb.RecordUCICompletionRequest{
		CanonicalProject: project, ExposureRef: parent, SupportedHostRef: "supported-host", CallbackRef: "callback",
		Outcome: pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_PARTIAL, IdempotencyKey: "completion-key",
	}
}

func (fixture *workerUCICompletionAuthorityFixture) accept(t *testing.T, caller context.Context, request *pb.RecordUCICompletionRequest) {
	t.Helper()
	response, err := fixture.client.RecordUCICompletion(caller, request)
	require.NoError(t, err, "authorized project-service control must traverse the real gRPC authentication/interceptor and recorder")
	require.NotNil(t, response)
	require.True(t, response.GetAccepted())
}

func (fixture *workerUCICompletionAuthorityFixture) deny(t *testing.T, caller context.Context, request *pb.RecordUCICompletionRequest) {
	t.Helper()
	response, err := fixture.client.RecordUCICompletion(caller, request)
	assert.Nil(t, response, "unauthorized completion must not return an accepted response")
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "parent authorization must precede both append and idempotency lookup")
	if err != nil {
		wireError := status.Convert(err).Proto().String()
		for _, forbidden := range []string{fixture.parentA, fixture.parentB, fixture.refA.SourceID, fixture.refA.CheckoutID, fixture.refA.ViewID, fixture.identityA.Principal} {
			assert.NotContains(t, wireError, forbidden, "safe denial must not disclose parent context or receipt details")
		}
	}
}

func (fixture *workerUCICompletionAuthorityFixture) children(t *testing.T, parent string) []gormstore.UCICompletionEvidence {
	t.Helper()
	var exposure gormstore.UCIExposure
	require.NoError(t, fixture.store.GetDB().Where("exposure_ref = ?", parent).First(&exposure).Error)
	var rows []gormstore.UCICompletionEvidence
	require.NoError(t, fixture.store.GetDB().Where("exposure_id = ?", exposure.ExposureID).
		Order("completion_evidence_id").Find(&rows).Error)
	return rows
}
