package gorm

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
	"github.com/thebtf/engram/pkg/models"
)

type interventionPolicyStoreFixture struct {
	db          *gormlib.DB
	project     string
	workstation string
	session     string
	principal   string
	memories    *MemoryStore
	policies    *InterventionPolicyStore
	compiler    *intervention.PolicyCompiler
	reconciler  intervention.PolicyReconciler
	versions    intervention.PolicySemanticVersions
}

func newInterventionPolicyStoreFixture(t *testing.T) interventionPolicyStoreFixture {
	t.Helper()
	db, _ := openInterventionReceiptMigrationTestDB(t)
	epoch, err := intervention.NewKeyEpoch(
		interventionPolicyStoreKeyMaterial(1),
		interventionPolicyStoreKeyMaterial(2),
		interventionPolicyStoreKeyMaterial(3),
		interventionPolicyStoreKeyMaterial(4),
		interventionPolicyStoreKeyMaterial(5),
	)
	require.NoError(t, err)
	compiler, err := intervention.NewPolicyCompiler(intervention.PolicyCompilerConfig{
		KeyProvider: intervention.NewStaticKeyProvider(epoch),
	})
	require.NoError(t, err)

	policies := NewInterventionPolicyStore(db)
	reconciler, err := intervention.NewPolicyReconciler(intervention.PolicyReconcilerConfig{
		Repository: policies,
		Compiler:   compiler,
	})
	require.NoError(t, err)
	return interventionPolicyStoreFixture{
		db:          db,
		project:     uuid.NewString(),
		workstation: "policy-store-workstation",
		session:     "policy-store-session",
		principal:   "agent/policy-store",
		memories:    NewMemoryStore(&Store{DB: db}),
		policies:    policies,
		compiler:    compiler,
		reconciler:  reconciler,
		versions:    compiler.Versions(),
	}
}

func interventionPolicyStoreKeyMaterial(seed byte) [32]byte {
	var material [32]byte
	for index := range material {
		material[index] = seed
	}
	return material
}

func (fixture interventionPolicyStoreFixture) createMemory(
	t *testing.T,
	content string,
	tags []string,
	mutate func(*models.Memory),
) *models.Memory {
	t.Helper()
	memory := &models.Memory{
		Project:             fixture.project,
		Content:             content,
		Tags:                append([]string(nil), tags...),
		PrivacyScope:        "project",
		SourceWorkstationID: fixture.workstation,
		SourceSessions:      []string{fixture.session},
		OwnerPrincipal:      fixture.principal,
		OwnerPrincipalKind:  "agent",
		AgentVisibility:     models.AgentVisibilityShared,
	}
	if mutate != nil {
		mutate(memory)
	}
	created, err := fixture.memories.Create(context.Background(), memory)
	require.NoError(t, err)
	return created
}

func (fixture interventionPolicyStoreFixture) reconcile(t *testing.T, limit int) intervention.ReconcileResult {
	t.Helper()
	result, err := fixture.reconciler.Reconcile(context.Background(), limit)
	if err != nil {
		t.Fatalf("reconcile error %T: %q", err, err.Error())
	}
	return result
}

func (fixture interventionPolicyStoreFixture) authority(
	t *testing.T,
	workstation string,
	principal string,
	session string,
) taskmemory.AuthorizedTaskContext {
	t.Helper()
	caller, err := taskmemory.NewAuthenticatedCaller(
		"client",
		"read-write",
		workstation,
		principal,
		"agent",
		nil,
	)
	require.NoError(t, err)
	project, err := projectidentity.NewProjectKeyV3(fixture.project)
	require.NoError(t, err)
	correlation, err := projectidentity.NewCorrelationV3("policy-store-" + uuid.NewString())
	require.NoError(t, err)
	resolution, err := projectidentity.NewSuccessResultV3(
		projectidentity.ReadFilterIntentV3,
		projectidentity.ProjectResolvedOutcomeV3,
		project,
		projectidentity.RepositoryResolvedScopeV3,
		"",
		correlation,
	)
	require.NoError(t, err)
	authority, err := taskmemory.NewAuthorizedTaskContext(caller, resolution, session)
	require.NoError(t, err)
	return authority
}

func interventionPolicyStoreRef(t *testing.T, memory *models.Memory) taskmemory.AuthorizedCandidateRef {
	t.Helper()
	ref, err := taskmemory.NewAuthorizedCandidateRef(memory.ID, memory.Version, taskmemory.CandidateExact)
	require.NoError(t, err)
	return ref
}

func interventionPolicyStorePolicyCount(t *testing.T, db *gormlib.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("intervention_evidence_policies").Count(&count).Error)
	return count
}

func TestInterventionPolicyStore_ReconcileCreatesValidAndInsufficientPoliciesFromMemoryStore(t *testing.T) {
	fixture := newInterventionPolicyStoreFixture(t)
	valid := fixture.createMemory(t, "Focused policy source", []string{"focused"}, nil)
	insufficient := fixture.createMemory(t, "!!!", nil, nil)

	result := fixture.reconcile(t, interventionPolicyMaxCompileSources)
	require.Equal(t, 2, result.Scanned)
	require.Equal(t, 2, result.Inserted)
	require.Equal(t, 1, result.Insufficient)
	require.Zero(t, result.Existing)
	require.Zero(t, result.Stale)
	require.EqualValues(t, 2, interventionPolicyStorePolicyCount(t, fixture.db))

	authority := fixture.authority(t, fixture.workstation, fixture.principal, fixture.session)
	refs := []taskmemory.AuthorizedCandidateRef{
		interventionPolicyStoreRef(t, insufficient),
		interventionPolicyStoreRef(t, valid),
	}
	policies, err := fixture.policies.ReadCandidatePolicies(context.Background(), authority, refs, fixture.versions)
	require.NoError(t, err)
	require.Len(t, policies, len(refs))
	require.Equal(t, intervention.CandidatePolicyInsufficient, policies[0].State())
	require.Equal(t, intervention.CandidatePolicyValid, policies[1].State())
	for index, policy := range policies {
		require.Equal(t, refs[index].ID(), policy.Ref().ID())
		require.Equal(t, refs[index].Version(), policy.Ref().Version())
		require.NotEqual(t, intervention.Digest{}, policy.PolicyVersion())
		require.NotEqual(t, intervention.Digest{}, policy.ScopeCommitment())
		require.NotEqual(t, intervention.Digest{}, policy.DescriptorCommitment())
	}

	driftedVersions := fixture.versions
	driftedVersions.Parameter = "intervention-parameters/1:1111111111111111111111111111111111111111111111111111111111111111"
	require.True(t, driftedVersions.Valid())
	driftedSources, err := fixture.policies.ListCompileSources(context.Background(), driftedVersions, interventionPolicyMaxCompileSources)
	require.NoError(t, err)
	require.Len(t, driftedSources, 2, "a parameter-version rollout must requeue the raw current source versions")
	driftedPolicies, err := fixture.policies.ReadCandidatePolicies(context.Background(), authority, refs, driftedVersions)
	require.NoError(t, err)
	require.Equal(t, intervention.CandidatePolicyMissing, driftedPolicies[0].State())
	require.Equal(t, intervention.CandidatePolicyMissing, driftedPolicies[1].State())
}

func TestInterventionPolicyStore_ListCompileSourcesOrdersRawCurrentVersionsAndBoundsLimit(t *testing.T) {
	fixture := newInterventionPolicyStoreFixture(t)
	first := fixture.createMemory(t, "First ordered source", []string{"first"}, nil)
	second := fixture.createMemory(t, "Second ordered source", []string{"second"}, nil)
	_, err := fixture.memories.Create(context.Background(), &models.Memory{
		Project:      "legacy-project-slug",
		Content:      "Legacy selectors are not canonical HAP policy authority.",
		Tags:         []string{"legacy"},
		PrivacyScope: "project",
	})
	require.NoError(t, err)

	sources, err := fixture.policies.ListCompileSources(context.Background(), fixture.versions, interventionPolicyMaxCompileSources)
	require.NoError(t, err)
	require.Len(t, sources, 2)
	require.Equal(t, first.ID, sources[0].ID())
	require.Equal(t, first.Version, sources[0].Version())
	require.Equal(t, second.ID, sources[1].ID())
	require.Equal(t, second.Version, sources[1].Version())

	_, err = fixture.policies.ListCompileSources(context.Background(), fixture.versions, interventionPolicyMaxCompileSources+1)
	require.Error(t, err, "repository must not widen the reconciler's 128-source bound")
}

func TestInterventionPolicyStore_ConcurrentCommitReturnsOneImmutableWinner(t *testing.T) {
	fixture := newInterventionPolicyStoreFixture(t)
	fixture.createMemory(t, "Concurrent policy source", []string{"concurrent"}, nil)

	sources, err := fixture.policies.ListCompileSources(context.Background(), fixture.versions, 1)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	definition, err := fixture.compiler.Compile(sources[0])
	require.NoError(t, err)
	require.True(t, definition.CanCommit())

	type result struct {
		definition intervention.PolicyDefinition
		inserted   bool
		err        error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			winner, inserted, err := fixture.policies.CommitPolicy(context.Background(), definition)
			results <- result{definition: winner, inserted: inserted, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	insertions := 0
	var winner intervention.PolicyDefinition
	for result := range results {
		require.NoError(t, result.err)
		if result.inserted {
			insertions++
		}
		require.False(t, result.definition.CanCommit(), "persisted winners are intentionally restored and not authorable")
		if winner.PersistenceRecord().PolicyID == ([32]byte{}) {
			winner = result.definition
			continue
		}
		require.Equal(t, winner.PersistenceRecord(), result.definition.PersistenceRecord())
	}
	require.Equal(t, 1, insertions)
	require.EqualValues(t, 1, interventionPolicyStorePolicyCount(t, fixture.db))

	secondPass, err := fixture.policies.ListCompileSources(context.Background(), fixture.versions, 1)
	require.NoError(t, err)
	require.Empty(t, secondPass, "exact source plus semantic labels must not requeue an immutable winner")
}

func TestInterventionPolicyStore_CommitSkipsVersionChangedSource(t *testing.T) {
	fixture := newInterventionPolicyStoreFixture(t)
	memory := fixture.createMemory(t, "Versioned source", []string{"versioned"}, nil)

	sources, err := fixture.policies.ListCompileSources(context.Background(), fixture.versions, 1)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	definition, err := fixture.compiler.Compile(sources[0])
	require.NoError(t, err)

	updated, err := fixture.memories.Update(context.Background(), &models.Memory{
		ID:      memory.ID,
		Content: "Versioned source corrected",
		Tags:    []string{"versioned-corrected"},
	})
	require.NoError(t, err)
	require.Equal(t, memory.Version+1, updated.Version)

	committed, inserted, err := fixture.policies.CommitPolicy(context.Background(), definition)
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, [32]byte{}, committed.PersistenceRecord().PolicyID)
	require.Zero(t, interventionPolicyStorePolicyCount(t, fixture.db))
}

func TestInterventionPolicyStore_UpdateAndSuccessorLogicallyRetirePriorPolicies(t *testing.T) {
	fixture := newInterventionPolicyStoreFixture(t)
	original := fixture.createMemory(t, "Original source", []string{"original"}, nil)
	first := fixture.reconcile(t, interventionPolicyMaxCompileSources)
	require.Equal(t, 1, first.Inserted)

	authority := fixture.authority(t, fixture.workstation, fixture.principal, fixture.session)
	originalRef := interventionPolicyStoreRef(t, original)
	policies, err := fixture.policies.ReadCandidatePolicies(context.Background(), authority, []taskmemory.AuthorizedCandidateRef{originalRef}, fixture.versions)
	require.NoError(t, err)
	require.Equal(t, intervention.CandidatePolicyValid, policies[0].State())

	updated, err := fixture.memories.Update(context.Background(), &models.Memory{
		ID:      original.ID,
		Content: "Updated source",
		Tags:    []string{"updated"},
	})
	require.NoError(t, err)
	require.Equal(t, original.ID, updated.ID)
	require.Equal(t, original.Version+1, updated.Version)
	second := fixture.reconcile(t, interventionPolicyMaxCompileSources)
	require.Equal(t, 1, second.Inserted)
	require.EqualValues(t, 2, interventionPolicyStorePolicyCount(t, fixture.db))

	updatedRef := interventionPolicyStoreRef(t, updated)
	policies, err = fixture.policies.ReadCandidatePolicies(context.Background(), authority, []taskmemory.AuthorizedCandidateRef{originalRef, updatedRef}, fixture.versions)
	require.NoError(t, err)
	require.Equal(t, intervention.CandidatePolicySourceStale, policies[0].State())
	require.Equal(t, intervention.CandidatePolicyValid, policies[1].State())

	successor := fixture.createMemory(t, "Successor source", []string{"successor"}, nil)
	require.NoError(t, fixture.memories.MarkSuperseded(context.Background(), updated.ID, successor.ID))
	third := fixture.reconcile(t, interventionPolicyMaxCompileSources)
	require.Equal(t, 1, third.Inserted)
	require.EqualValues(t, 3, interventionPolicyStorePolicyCount(t, fixture.db))

	successorRef := interventionPolicyStoreRef(t, successor)
	policies, err = fixture.policies.ReadCandidatePolicies(context.Background(), authority, []taskmemory.AuthorizedCandidateRef{updatedRef, successorRef}, fixture.versions)
	require.NoError(t, err)
	require.Equal(t, intervention.CandidatePolicySourceStale, policies[0].State())
	require.Equal(t, intervention.CandidatePolicyValid, policies[1].State())
}

func TestInterventionPolicyStore_ReaderPreservesOrderAndClassifiesAccessAndCurrentness(t *testing.T) {
	fixture := newInterventionPolicyStoreFixture(t)
	visible := fixture.createMemory(t, "Visible source", []string{"visible"}, nil)
	insufficient := fixture.createMemory(t, "!!!", nil, nil)
	private := fixture.createMemory(t, "Private source", []string{"private"}, func(memory *models.Memory) {
		memory.PrivacyScope = "private"
		memory.SourceWorkstationID = "private-workstation"
		memory.SourceSessions = []string{"private-session"}
		memory.AgentVisibility = models.AgentVisibilityPrivate
	})
	principal := fixture.createMemory(t, "Principal source", []string{"principal"}, func(memory *models.Memory) {
		memory.AgentVisibility = models.AgentVisibilityPrivate
	})
	domain := fixture.createMemory(t, "Domain source", []string{"domain"}, func(memory *models.Memory) {
		memory.Domain = "policy-domain"
	})
	stale := fixture.createMemory(t, "Stale source", []string{"stale"}, nil)
	initial := fixture.reconcile(t, interventionPolicyMaxCompileSources)
	require.Equal(t, 6, initial.Inserted)

	staleRef := interventionPolicyStoreRef(t, stale)
	_, err := fixture.memories.Update(context.Background(), &models.Memory{
		ID:      stale.ID,
		Content: "Stale source corrected",
		Tags:    []string{"stale-corrected"},
	})
	require.NoError(t, err)
	missing := fixture.createMemory(t, "Missing policy source", []string{"missing"}, nil)

	matchingAuthority := fixture.authority(t, "private-workstation", fixture.principal, "private-session")
	refs := []taskmemory.AuthorizedCandidateRef{
		interventionPolicyStoreRef(t, visible),
		interventionPolicyStoreRef(t, insufficient),
		interventionPolicyStoreRef(t, missing),
		staleRef,
		interventionPolicyStoreRef(t, private),
		interventionPolicyStoreRef(t, principal),
		interventionPolicyStoreRef(t, domain),
	}
	policies, err := fixture.policies.ReadCandidatePolicies(context.Background(), matchingAuthority, refs, fixture.versions)
	require.NoError(t, err)
	require.Len(t, policies, len(refs))
	wantStates := []intervention.CandidatePolicyState{
		intervention.CandidatePolicyValid,
		intervention.CandidatePolicyInsufficient,
		intervention.CandidatePolicyMissing,
		intervention.CandidatePolicySourceStale,
		intervention.CandidatePolicyValid,
		intervention.CandidatePolicyValid,
		intervention.CandidatePolicyValid,
	}
	for index, want := range wantStates {
		require.Equal(t, want, policies[index].State(), "candidate %d state", index)
		require.Equal(t, refs[index].ID(), policies[index].Ref().ID(), "candidate %d ID order", index)
		require.Equal(t, refs[index].Version(), policies[index].Ref().Version(), "candidate %d version order", index)
	}

	wrongWorkstation := fixture.authority(t, "other-workstation", fixture.principal, "private-session")
	privatePolicies, err := fixture.policies.ReadCandidatePolicies(
		context.Background(),
		wrongWorkstation,
		[]taskmemory.AuthorizedCandidateRef{interventionPolicyStoreRef(t, private)},
		fixture.versions,
	)
	require.NoError(t, err)
	require.Equal(t, intervention.CandidatePolicySourceStale, privatePolicies[0].State(), "private source must not reveal whether it exists to another workstation")

	wrongPrincipal := fixture.authority(t, fixture.workstation, "agent/other", fixture.session)
	principalPolicies, err := fixture.policies.ReadCandidatePolicies(
		context.Background(),
		wrongPrincipal,
		[]taskmemory.AuthorizedCandidateRef{interventionPolicyStoreRef(t, principal), interventionPolicyStoreRef(t, domain)},
		fixture.versions,
	)
	require.NoError(t, err)
	require.Equal(t, intervention.CandidatePolicySourceStale, principalPolicies[0].State(), "private principal source must be hidden from a different principal")
	require.Equal(t, intervention.CandidatePolicySourceStale, principalPolicies[1].State(), "domain-owned source must be hidden from a different principal")
}

func TestInterventionPolicyStore_PurgeRetainsImmutablePolicyHistoryWithoutMemoryForeignKey(t *testing.T) {
	fixture := newInterventionPolicyStoreFixture(t)
	memory := fixture.createMemory(t, "Purge policy source", []string{"purge"}, nil)
	fixture.reconcile(t, interventionPolicyMaxCompileSources)
	require.EqualValues(t, 1, interventionPolicyStorePolicyCount(t, fixture.db))

	purge := NewPurgeStore(&Store{DB: fixture.db})
	_, err := purge.PurgeProject(context.Background(), fixture.project)
	require.NoError(t, err)
	require.EqualValues(t, 1, interventionPolicyStorePolicyCount(t, fixture.db), "hard memory purge must retain immutable policy evidence")

	authority := fixture.authority(t, fixture.workstation, fixture.principal, fixture.session)
	policies, err := fixture.policies.ReadCandidatePolicies(
		context.Background(),
		authority,
		[]taskmemory.AuthorizedCandidateRef{interventionPolicyStoreRef(t, memory)},
		fixture.versions,
	)
	require.NoError(t, err)
	require.Equal(t, intervention.CandidatePolicySourceStale, policies[0].State())
}
