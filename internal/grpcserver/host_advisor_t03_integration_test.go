package grpcserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/taskmemory"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

type t03ScriptedCandidateProvider struct {
	mu        sync.Mutex
	snapshots []taskmemory.CandidateSnapshot
	calls     int
}

func (p *t03ScriptedCandidateProvider) Snapshot(_ context.Context, _ taskmemory.AuthorizedCandidateQuery) (taskmemory.CandidateSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls >= len(p.snapshots) {
		return taskmemory.CandidateSnapshot{}, errors.New("unexpected T03 candidate read")
	}
	snapshot := p.snapshots[p.calls]
	p.calls++
	return snapshot, nil
}

func TestHostAdvisorT03CompiledPoliciesProduceReceiptBackedAbstentions(t *testing.T) {
	store, receiptStore := openT02HostAdvisorStore(t)
	keyProvider, epoch := t02ExistingVaultKeyProvider(t)
	memoryStore := gormdb.NewMemoryStore(store)
	policyStore := gormdb.NewInterventionPolicyStore(store.GetDB())
	compiler, err := intervention.NewPolicyCompiler(intervention.PolicyCompilerConfig{KeyProvider: keyProvider})
	require.NoError(t, err)
	reconciler, err := intervention.NewPolicyReconciler(intervention.PolicyReconcilerConfig{
		Repository: policyStore,
		Compiler:   compiler,
	})
	require.NoError(t, err)

	validMemory, err := memoryStore.Create(context.Background(), &models.Memory{
		Project:             grpcV3ProjectKey,
		Content:             "Inspect the retry path before changing policy.",
		Tags:                []string{"retry"},
		PrivacyScope:        "project",
		SourceWorkstationID: "hap03-t03-workstation",
		OwnerPrincipal:      "agent/hap03-t03",
		OwnerPrincipalKind:  "agent",
		AgentVisibility:     models.AgentVisibilityShared,
	})
	require.NoError(t, err)
	insufficientMemory, err := memoryStore.Create(context.Background(), &models.Memory{
		Project:             grpcV3ProjectKey,
		Content:             "!!!",
		PrivacyScope:        "project",
		SourceWorkstationID: "hap03-t03-workstation",
		OwnerPrincipal:      "agent/hap03-t03",
		OwnerPrincipalKind:  "agent",
		AgentVisibility:     models.AgentVisibilityShared,
	})
	require.NoError(t, err)

	reconciled, err := reconciler.Reconcile(context.Background(), 128)
	require.NoError(t, err)
	require.Equal(t, 2, reconciled.Scanned)
	require.Equal(t, 2, reconciled.Inserted)
	require.Equal(t, 1, reconciled.Insufficient)
	probeVersions := compiler.Versions()
	probeVersions.Parameter += ":probe"
	probeSources, err := policyStore.ListCompileSources(context.Background(), probeVersions, 1)
	require.NoError(t, err)
	require.Len(t, probeSources, 1)
	probeDefinition, err := compiler.Compile(probeSources[0])
	require.NoError(t, err)
	probeScopeCommitment := intervention.Digest(probeDefinition.PersistenceRecord().ScopeCommitment)

	validRef, err := taskmemory.NewAuthorizedCandidateRef(validMemory.ID, validMemory.Version, taskmemory.CandidateExact)
	require.NoError(t, err)
	insufficientRef, err := taskmemory.NewAuthorizedCandidateRef(insufficientMemory.ID, insufficientMemory.Version, taskmemory.CandidateExact)
	require.NoError(t, err)
	directResolver := &t02AuthorityResolver{}
	directAnchor, directDescriptor := grpcProjectIdentityV3Evidence(grpcV3Identity())
	directAuthority, err := directResolver.ResolveTaskAuthority(
		auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "hap03-t03-workstation", "agent/hap03-t03", auth.PrincipalKindAgent)),
		taskmemory.ProjectEvidenceV3{Anchor: directAnchor, Descriptor: directDescriptor},
	)
	require.NoError(t, err)
	directPolicies, err := policyStore.ReadCandidatePolicies(context.Background(), directAuthority, []taskmemory.AuthorizedCandidateRef{validRef}, compiler.Versions())
	require.NoError(t, err)
	require.Len(t, directPolicies, 1)
	directScope, present := directPolicies[0].CurrentScope()
	require.True(t, present)
	require.Equal(t, probeSources[0].Scope(), directScope, "policy reader must reconstruct the compiler source scope exactly")
	directCommitment, err := epoch.DerivePolicyScope(directScope)
	require.NoError(t, err)
	require.Equal(t, probeScopeCommitment, directPolicies[0].ScopeCommitment(), "persisted policy scope must match compiler output")
	require.Equal(t, directPolicies[0].ScopeCommitment(), directCommitment, "stored policy scope must match current source scope")
	validSnapshot, err := taskmemory.NewCandidateSnapshot(taskmemory.RetrievalExact, []taskmemory.AuthorizedCandidateRef{validRef})
	require.NoError(t, err)
	insufficientSnapshot, err := taskmemory.NewCandidateSnapshot(taskmemory.RetrievalExact, []taskmemory.AuthorizedCandidateRef{insufficientRef})
	require.NoError(t, err)
	candidateProvider := &t03ScriptedCandidateProvider{snapshots: []taskmemory.CandidateSnapshot{
		validSnapshot, validSnapshot,
		insufficientSnapshot, insufficientSnapshot,
	}}
	resolver := &t02AuthorityResolver{}
	preparer, err := taskmemory.NewPreparer(taskmemory.PreparerConfig{
		Authority:  resolver,
		Candidates: candidateProvider,
	})
	require.NoError(t, err)
	runtime, err := intervention.NewRuntimeAdvisor(intervention.RuntimeAdvisorConfig{
		Preparer:       preparer,
		ReceiptStore:   receiptStore,
		KeyProvider:    keyProvider,
		PolicyReader:   policyStore,
		PolicyVersions: compiler.Versions(),
		Clock:          time.Now,
		NewUUID: func() (string, error) {
			return uuid.NewString(), nil
		},
	})
	require.NoError(t, err)

	profile := grpcAdvisorProfileWithCallbackDeadline(t, 900*time.Millisecond)
	clock := time.Now().UTC()
	registry := grpcAdvisorRegistry(t, profile, &clock)
	rawToken := "engram_eeee555500000000000000000000beef"
	keycard := makeKeycardRow(t, "hap03-t03-workstation", rawToken, "read-write")
	keycard.Principal = "agent/hap03-t03"
	keycard.PrincipalKind = "agent"
	validator := auth.NewValidator("hap03-t03-master", &stubReader{rows: map[string][]gormdb.APIToken{
		"eeee5555": {keycard},
	}})
	client, stop := newT02HostAdvisorClient(t, validator, registry, runtime)
	t.Cleanup(stop)
	ctx := bearerOutgoingContext(rawToken)
	bound, err := client.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	require.NoError(t, err)
	bindingID := bound.GetBinding().GetBindingId()

	observingRequest := grpcAdvisorAdviseRequest(bindingID)
	observing, err := client.Advise(ctx, observingRequest)
	require.NoError(t, err)
	require.NotNil(t, observing.GetAbstain())
	require.Equal(t, pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_POLICY_OBSERVING, observing.GetAbstain().GetReason())
	require.NotEmpty(t, observing.GetAbstain().GetReceipt().GetReceiptId())

	insufficientRequest := grpcAdvisorAdviseRequest(bindingID)
	insufficientRequest.Occurrence.PhaseAnchorRef = "turn-insufficient"
	insufficientRequest.Occurrence.BeforeAgentStart.TaskQuery = "Inspect an insufficient compiled policy"
	insufficient, err := client.Advise(ctx, insufficientRequest)
	require.NoError(t, err)
	require.NotNil(t, insufficient.GetAbstain())
	require.Equal(t, pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_EVIDENCE_INSUFFICIENT, insufficient.GetAbstain().GetReason())
	require.NotEmpty(t, insufficient.GetAbstain().GetReceipt().GetReceiptId())

	var policyCount int64
	require.NoError(t, store.GetDB().Table("intervention_evidence_policies").Count(&policyCount).Error)
	require.EqualValues(t, 2, policyCount)
	require.EqualValues(t, 2, t02ReceiptCount(t, store))
	require.Equal(t, 2, resolver.calls)

	var descriptors []string
	require.NoError(t, store.GetDB().Raw(`SELECT descriptor::text FROM intervention_evidence_policies ORDER BY memory_id`).Scan(&descriptors).Error)
	require.Len(t, descriptors, 2)
	require.NotContains(t, descriptors[0], validMemory.Content)
	require.NotContains(t, descriptors[1], insufficientMemory.Content)
}

var _ taskmemory.AuthorizedCandidateProvider = (*t03ScriptedCandidateProvider)(nil)
