package grpcserver

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/hostadvisor"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm/logger"
)

const t02VaultHexKey = "d10102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

type t02AuthorityResolver struct {
	calls int
}

func (r *t02AuthorityResolver) ResolveTaskAuthority(ctx context.Context, evidence taskmemory.ProjectEvidenceV3) (taskmemory.AuthorizedTaskContext, error) {
	r.calls++
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return taskmemory.AuthorizedTaskContext{}, taskmemory.ErrUnauthorized
	}
	caller, err := taskmemory.NewAuthenticatedCaller(
		string(identity.Source),
		string(identity.Role),
		identity.WorkstationID(),
		identity.Principal,
		string(identity.PrincipalKind),
		identity.ExpiresAt,
	)
	if err != nil {
		return taskmemory.AuthorizedTaskContext{}, err
	}
	if evidence.Anchor.ProjectID != grpcV3Identity().GetAnchorProjectId() {
		return taskmemory.AuthorizedTaskContext{}, taskmemory.ErrUnauthorized
	}
	correlation, err := projectidentity.NewCorrelationV3("hap03-t02-correlation")
	if err != nil {
		return taskmemory.AuthorizedTaskContext{}, err
	}
	projectKey, err := projectidentity.NewProjectKeyV3(grpcV3ProjectKey)
	if err != nil {
		return taskmemory.AuthorizedTaskContext{}, err
	}
	resolution, err := projectidentity.NewSuccessResultV3(
		projectidentity.ReadFilterIntentV3,
		projectidentity.ProjectResolvedOutcomeV3,
		projectKey,
		projectidentity.RepositoryResolvedScopeV3,
		"",
		correlation,
	)
	if err != nil {
		return taskmemory.AuthorizedTaskContext{}, err
	}
	return taskmemory.NewAuthorizedTaskContext(caller, resolution, "hap03-t02-authority-session")
}

type t02EmptyCandidateProvider struct {
	mu        sync.Mutex
	calls     int
	delayNext time.Duration
}

func (p *t02EmptyCandidateProvider) Snapshot(ctx context.Context, _ taskmemory.AuthorizedCandidateQuery) (taskmemory.CandidateSnapshot, error) {
	p.mu.Lock()
	p.calls++
	delay := p.delayNext
	p.delayNext = 0
	p.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return taskmemory.CandidateSnapshot{}, ctx.Err()
		case <-timer.C:
		}
	}
	return taskmemory.NewCandidateSnapshot(taskmemory.RetrievalEmpty, nil)
}

func (p *t02EmptyCandidateProvider) delayOneRead(delay time.Duration) {
	p.mu.Lock()
	p.delayNext = delay
	p.mu.Unlock()
}

func TestHostAdvisorT02NoCandidateReceiptReplayConflictAndDeadline(t *testing.T) {
	store, receiptStore := openT02HostAdvisorStore(t)
	keyProvider, epoch := t02ExistingVaultKeyProvider(t)
	resolver := &t02AuthorityResolver{}
	candidates := &t02EmptyCandidateProvider{}
	preparer, err := taskmemory.NewPreparer(taskmemory.PreparerConfig{
		Authority:  resolver,
		Candidates: candidates,
	})
	require.NoError(t, err)
	runtime, err := intervention.NewRuntimeAdvisor(intervention.RuntimeAdvisorConfig{
		Preparer:     preparer,
		ReceiptStore: receiptStore,
		KeyProvider:  keyProvider,
		Clock:        time.Now,
		NewUUID: func() (string, error) {
			return uuid.NewString(), nil
		},
	})
	require.NoError(t, err)

	profile := grpcAdvisorProfileWithCallbackDeadline(t, 700*time.Millisecond)
	clock := time.Now().UTC()
	registry := grpcAdvisorRegistry(t, profile, &clock)
	rawToken := "engram_dddd444400000000000000000000beef"
	keycard := makeKeycardRow(t, "hap03-t02-workstation", rawToken, "read-write")
	keycard.Principal = "agent/hap03-t02"
	keycard.PrincipalKind = "agent"
	validator := auth.NewValidator("hap03-t02-master", &stubReader{rows: map[string][]gormdb.APIToken{
		"dddd4444": {keycard},
	}})
	client, stop := newT02HostAdvisorClient(t, validator, registry, runtime)
	t.Cleanup(stop)
	ctx := bearerOutgoingContext(rawToken)

	bound, err := client.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	require.NoError(t, err)
	bindingID := bound.GetBinding().GetBindingId()
	require.NotEmpty(t, bindingID)

	request := grpcAdvisorAdviseRequest(bindingID)
	first, err := client.Advise(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, first.GetAbstain())
	require.Equal(t, pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_NO_CANDIDATES, first.GetAbstain().GetReason())
	firstReceiptID := first.GetAbstain().GetReceipt().GetReceiptId()
	require.NotEmpty(t, firstReceiptID)
	require.EqualValues(t, 1, t02ReceiptCount(t, store))

	replayed, err := client.Advise(ctx, proto.Clone(request).(*pb.HostAdvisorAdviseRequest))
	require.NoError(t, err)
	require.NotNil(t, replayed.GetAbstain())
	require.Equal(t, firstReceiptID, replayed.GetAbstain().GetReceipt().GetReceiptId())
	require.Equal(t, first.GetAbstain().GetReceipt().GetIntegritySha256(), replayed.GetAbstain().GetReceipt().GetIntegritySha256())
	require.Nil(t, replayed.GetEmit(), "exact ABSTAIN replay must never return a packet")
	require.EqualValues(t, 1, t02ReceiptCount(t, store))

	conflicting := proto.Clone(request).(*pb.HostAdvisorAdviseRequest)
	conflicting.Occurrence.BeforeAgentStart.TaskQuery = "Changed content on the same occurrence"
	conflictResponse, err := client.Advise(ctx, conflicting)
	require.NoError(t, err)
	require.NotNil(t, conflictResponse.GetUnavailable())
	require.Equal(t, pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_REPLAY_CONFLICT, conflictResponse.GetUnavailable().GetCode())
	require.EqualValues(t, 1, t02ReceiptCount(t, store))

	candidates.delayOneRead(450 * time.Millisecond)
	deadlineRequest := proto.Clone(request).(*pb.HostAdvisorAdviseRequest)
	deadlineRequest.Occurrence.PhaseAnchorRef = "turn-deadline"
	deadlineResponse, err := client.Advise(ctx, deadlineRequest)
	require.NoError(t, err)
	require.NotNil(t, deadlineResponse.GetUnavailable())
	require.Equal(t, pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_DEADLINE_EXPIRED, deadlineResponse.GetUnavailable().GetCode())
	require.EqualValues(t, 1, t02ReceiptCount(t, store), "deadline-before-commit must write zero receipts")

	epochs, err := receiptStore.DistinctInterventionReceiptEpochs(context.Background())
	require.NoError(t, err)
	require.Equal(t, [][32]byte{epoch.EpochCommitment()}, epochs)
	require.Equal(t, 4, resolver.calls, "each Advise must call the accepted Preparer authority exactly once")
}

func newT02HostAdvisorClient(t *testing.T, validator *auth.Validator, registry *hostadvisor.Registry, runtime intervention.Advisor) (pb.EngramServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(bufSize)
	server, internal := New(hostAdvisorMCPHandler{}, validator)
	internal.SetHostAdvisorRegistry(registry)
	internal.SetInterventionAdvisor(runtime)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient(
		"passthrough:///hap03-t02",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	return pb.NewEngramServiceClient(conn), func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	}
}

func openT02HostAdvisorStore(t *testing.T) (*gormdb.Store, *gormdb.InterventionReceiptStore) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping H03-T02 bufconn/PostgreSQL integration test")
	}
	adminConfig, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	adminSQL := stdlib.OpenDB(*adminConfig)
	require.NoError(t, adminSQL.Ping())
	schema := "hap03_t02_grpc_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = adminSQL.ExecContext(context.Background(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)

	var store *gormdb.Store
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
		_, dropErr := adminSQL.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		if dropErr != nil {
			t.Errorf("drop isolated H03-T02 schema %s: %v", schema, dropErr)
		}
		_ = adminSQL.Close()
	})

	testConfig := *adminConfig
	testConfig.RuntimeParams = make(map[string]string, len(adminConfig.RuntimeParams)+2)
	for key, value := range adminConfig.RuntimeParams {
		testConfig.RuntimeParams[key] = value
	}
	testConfig.RuntimeParams["application_name"] = "engram_hap03_t02_grpc"
	testConfig.RuntimeParams["search_path"] = schema + ", public"
	store, err = gormdb.NewStore(gormdb.Config{DSN: testConfig.ConnString(), MaxConns: 8, LogLevel: logger.Silent})
	require.NoError(t, err)
	return store, gormdb.NewInterventionReceiptStore(store.GetDB())
}

func t02ExistingVaultKeyProvider(t *testing.T) (intervention.KeyProvider, intervention.KeyEpoch) {
	t.Helper()
	vault, err := crypto.OpenExistingVault(&config.Config{EncryptionKey: t02VaultHexKey})
	require.NoError(t, err)
	epoch, err := intervention.NewKeyEpoch(
		vault.KeyCommitment(),
		vault.DeriveKey("engram.hap03/channel/v1"),
		vault.DeriveKey("engram.hap03/occurrence/v1"),
		vault.DeriveKey("engram.hap03/content/v1"),
		vault.DeriveKey("engram.hap03/receipt/v1"),
	)
	require.NoError(t, err)
	require.Equal(t, "env", vault.KeySource())
	return intervention.NewStaticKeyProvider(epoch), epoch
}

func t02ReceiptCount(t *testing.T, store *gormdb.Store) int64 {
	t.Helper()
	var count int64
	require.NoError(t, store.GetDB().Table("task_memory_intervention_receipts").Count(&count).Error)
	return count
}

var (
	_ taskmemory.AuthorityResolver           = (*t02AuthorityResolver)(nil)
	_ taskmemory.AuthorizedCandidateProvider = (*t02EmptyCandidateProvider)(nil)
)
