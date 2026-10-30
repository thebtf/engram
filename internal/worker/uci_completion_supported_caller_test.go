package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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

// Project membership is not a Source/Checkout grant. This discriminator keeps
// the real agent owner and requires denial even for a matching project service.
// The former installed success expectation was rejected by the owner contract.
func TestUCICompletionGRPCInstalledAgentOwnerServiceCaller(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	store := openWorkerUCIContextCompositionStore(t)
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "completion-supported-caller-discriminator"})
	composition, err := composeUCIContext(true, store.GetDB(), mcpServer, workerUCISemanticConfig())
	require.NoError(t, err)
	projectA, projectB := uuid.NewString(), uuid.NewString()
	for _, project := range []string{projectA, projectB} {
		require.NoError(t, store.GetDB().Create(&gormstore.Project{ID: project}).Error)
	}

	validator := auth.NewValidator("", gormstore.NewTokenStore(store))
	var priorRaw string
	newCaller := func(principal string, kind auth.PrincipalKind) (auth.Identity, string) {
		randomBytes := make([]byte, auth.TokenBodyLen/2)
		_, randomErr := rand.Read(randomBytes)
		require.NoError(t, randomErr)
		raw := auth.TokenRawPrefix + hex.EncodeToString(randomBytes)
		require.True(t, raw != priorRaw, "fixture credentials must be distinct")
		priorRaw = raw
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, hashErr)
		_, createErr := gormstore.NewTokenStore(store).CreateWithPrincipal(context.Background(), gormstore.TokenCreatePrincipalInput{
			Name: "completion-discriminator-" + uuid.NewString(), TokenHash: string(hash),
			TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen], Scope: "read-write",
			Principal: principal, PrincipalKind: string(kind),
		})
		require.NoError(t, createErr)
		identity, validateErr := validator.Validate(context.Background(), raw)
		require.NoError(t, validateErr)
		require.Equal(t, auth.SourceClient, identity.Source)
		require.Equal(t, principal, identity.Principal)
		return identity, raw
	}
	agent, _ := newCaller("agent/completion-discriminator-"+uuid.NewString(), auth.PrincipalKindAgent)
	serviceA, rawA := newCaller(auth.ProjectServicePrincipal(projectA), auth.PrincipalKindService)
	serviceB, rawB := newCaller(auth.ProjectServicePrincipal(projectB), auth.PrincipalKindService)
	require.True(t, serviceA.IsHAPProjectServiceFor(projectA))
	require.True(t, serviceB.IsHAPProjectServiceFor(projectB))
	require.NotEqual(t, agent.Principal, serviceA.Principal)

	ownerContext := auth.WithIdentity(mcp.ContextWithProject(mcp.ContextWithSession(context.Background(), "completion-discriminator-owner"), projectA), agent)
	locator := "file:///completion-discriminator/" + uuid.NewString()
	registrationParams, err := json.Marshal(map[string]any{
		"name": "codebase_context", "arguments": map[string]any{
			"action": "register", "source_label": "completion-discriminator", "locator": locator, "parser_bundle": false,
		},
	})
	require.NoError(t, err)
	registrationResponse := mcpServer.HandleRequest(ownerContext, &mcp.Request{
		JSONRPC: "2.0", ID: uuid.NewString(), Method: "tools/call", Params: registrationParams,
	})
	require.NotNil(t, registrationResponse)
	require.NotNil(t, registrationResponse.Error)
	require.Equal(t, string(uci.PermissionDenied), registrationResponse.Error.Data, "the current public MCP registration contract excludes agent identities; do not forge a human owner to get past it")

	// Normal registration admission still uses its real exported store owner,
	// not UPDATE sources.project_id. This is not public MCP registration proof.
	registered, err := composition.contextStore.RegisterLocalGit(context.Background(), gormstore.RegisterLocalGitInput{
		AuthRealm: string(agent.Source), Principal: agent.Principal, WorkstationID: agent.WorkstationID(),
		ProjectID: projectA, SourceLabel: "completion-discriminator", Locator: locator,
	})
	require.NoError(t, err)
	boundSource, err := composition.contextStore.GetSource(context.Background(), registered.SourceID)
	require.NoError(t, err)
	require.NotNil(t, boundSource.ProjectID)
	require.Equal(t, projectA, *boundSource.ProjectID)
	boundCheckout, err := composition.contextStore.GetCheckout(context.Background(), registered.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, agent.Principal, boundCheckout.OwnerPrincipal)
	var profile gormstore.UCIAnalysisProfile
	require.NoError(t, store.GetDB().Where("profile_id = ?", registered.ProfileID).First(&profile).Error)
	publisher, err := composition.projectionStore.Publisher(composition.authorizer, uci.IndexPublicationConfig{Limits: uci.DefaultIndexPublicationLimits()})
	require.NoError(t, err)
	newParent := func(source *gormstore.UCISource, checkout *gormstore.UCICheckout, label string) (uci.ContextRef, string) {
		artifact := workerUCIApplicationAddArtifact(t, composition.projectionStore, workerUCIApplicationArtifactInput{
			sourceID: source.SourceID, profileDigest: profile.ParserBundleDigest, label: "completion-discriminator-" + label,
			name: "AgentOwnedCompletion", symbol: "agent-owned-completion", source: "func AgentOwnedCompletion() { _ = \"SupportedCompletionNeedle\" }\n",
		})
		memberships := []uci.IndexMembership{workerUCIApplicationMembership("internal/completion.go", artifact)}
		replacements := []uci.IndexEdgeReplacement{{SourcePath: "internal/completion.go"}}
		published := workerUCIApplicationPublish(t, publisher, uci.IndexCaller{
			AuthRealm: string(agent.Source), Principal: agent.Principal, OwnerInstance: "completion-discriminator-" + label,
		}, workerUCIApplicationPublicationInput{
			checkout: checkout, profileID: profile.ProfileID, key: "completion-discriminator-" + label, jobKind: uci.IndexJobInitial,
			part:        workerUCIApplicationPart([]workerUCIApplicationArtifact{artifact}, memberships, replacements),
			memberships: memberships, replacements: replacements, sequence: 1,
		})
		handle := workerUCIApplicationSelectContext(t, mcpServer, ownerContext, published.Context)
		search := workerUCIApplicationQueryResponse(t, workerUCIApplicationToolResponse(t, mcpServer, ownerContext, "codebase_search", map[string]any{
			"context_handle": handle, "query": "SupportedCompletionNeedle", "limit": 10,
		}))
		require.NoError(t, search.Validate())
		require.Equal(t, uci.QueryStatusOK, search.Status)
		require.NotNil(t, search.Exposure)
		require.True(t, uci.ValidExposureRef(search.Exposure.ExposureRef))
		return published.Context, search.Exposure.ExposureRef
	}
	boundRef, boundParent := newParent(boundSource, boundCheckout, "registered")

	// Mirror the older installed fixture separately: its Source has no
	// project association, even though a canonical project and service exist.
	unboundSource, err := composition.contextStore.CreateSource(context.Background(), gormstore.CreateSourceInput{
		AuthRealm: string(agent.Source), Kind: gormstore.UCISourceGit, DisplayName: "completion-unbound-source",
	})
	require.NoError(t, err)
	require.Nil(t, unboundSource.ProjectID)
	unboundCheckout, err := composition.contextStore.RegisterCheckout(context.Background(), gormstore.RegisterCheckoutInput{
		SourceID: unboundSource.SourceID, WorkstationID: agent.WorkstationID(), Kind: gormstore.UCICheckoutWorkingTree,
		OwnerPrincipal: agent.Principal, LocatorRef: "file:///completion-unbound/" + uuid.NewString(),
	})
	require.NoError(t, err)
	unboundRef, unboundParent := newParent(unboundSource, unboundCheckout, "unbound")
	for _, ref := range []uci.ContextRef{boundRef, unboundRef} {
		_, ownerErr := composition.resolver.Authorize(context.Background(), uci.ResolveContextInput{
			ClientSessionID: "completion-discriminator-owner-check", AuthRealm: string(agent.Source),
			Principal: agent.Principal, WorkstationID: agent.WorkstationID(), Ref: &ref,
		})
		require.NoError(t, ownerErr)
	}

	adapter := &mcpHandlerAdapter{mcpServer: mcpServer}
	server, internal := grpcserver.New(adapter, validator)
	internal.SetDB(store.GetDB())
	internal.SetUCITransport(composition.transport)
	internal.SetUCICompletionRecorder(adapter)
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///uci-completion-supported-caller",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	client := pb.NewEngramServiceClient(connection)
	callerA := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+rawA))
	callerB := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+rawB))
	request := &pb.RecordUCICompletionRequest{
		CanonicalProject: projectA, ExposureRef: boundParent, SupportedHostRef: "supported-host", CallbackRef: "supported-callback",
		Outcome: pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_PARTIAL, IdempotencyKey: "supported-completion-key",
	}
	malformed := proto.Clone(request).(*pb.RecordUCICompletionRequest)
	malformed.CallbackRef = ""
	response, err := client.RecordUCICompletion(callerA, malformed)
	require.Nil(t, response)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "the actual interceptor must accept this project-service credential before the parent-scope discriminator")

	children := func(parent string) []gormstore.UCICompletionEvidence {
		var exposure gormstore.UCIExposure
		require.NoError(t, store.GetDB().Where("exposure_ref = ?", parent).First(&exposure).Error)
		var rows []gormstore.UCICompletionEvidence
		require.NoError(t, store.GetDB().Where("exposure_id = ?", exposure.ExposureID).Order("completion_evidence_id").Find(&rows).Error)
		return rows
	}
	for _, negative := range []struct {
		name, parent, project string
		caller                context.Context
		mismatch              bool
	}{
		{"same canonical project initial callback", boundParent, projectA, callerA, false},
		{"same canonical project exact retry", boundParent, projectA, callerA, false},
		{"same canonical project changed same key", boundParent, projectA, callerA, true},
		{"different authenticated service/project", boundParent, projectB, callerB, false},
		{"unbound historical installed Source", unboundParent, projectA, callerA, false},
	} {
		t.Run(negative.name, func(t *testing.T) {
			var auditBefore int64
			require.NoError(t, store.GetDB().Model(&gormstore.AuditLogEntry{}).Count(&auditBefore).Error)
			denied := proto.Clone(request).(*pb.RecordUCICompletionRequest)
			denied.CanonicalProject, denied.ExposureRef = negative.project, negative.parent
			if negative.mismatch {
				denied.CallbackRef = "changed-same-key-callback"
				denied.Outcome = pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_FAILED
			}
			response, callErr := client.RecordUCICompletion(negative.caller, denied)
			require.Nil(t, response)
			require.Equal(t, codes.PermissionDenied, status.Code(callErr), "owner denial must precede append, exact replay and mismatch disclosure")
			for _, forbidden := range []string{boundParent, unboundParent, boundRef.SourceID, boundRef.CheckoutID, boundRef.ViewID, unboundRef.SourceID, unboundRef.CheckoutID, unboundRef.ViewID, agent.Principal} {
				require.NotContains(t, status.Convert(callErr).Proto().String(), forbidden)
			}
			require.Empty(t, children(negative.parent))
			var auditAfter int64
			require.NoError(t, store.GetDB().Model(&gormstore.AuditLogEntry{}).Count(&auditAfter).Error)
			require.Equal(t, auditBefore, auditAfter, "a denied completion must create no audit row")
		})
	}
	var ownerAfter gormstore.UCICheckout
	require.NoError(t, store.GetDB().Where("checkout_id = ?", boundCheckout.CheckoutID).First(&ownerAfter).Error)
	require.Equal(t, agent.Principal, ownerAfter.OwnerPrincipal, "the discriminator must not manufacture authority by replacing the real agent owner")
}
