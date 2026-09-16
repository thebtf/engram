package gorm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/hostadvisor"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
)

func TestInterventionReceiptStoreGuardsNilDatabaseAndInput(t *testing.T) {
	var nilStore *InterventionReceiptStore
	_, _, err := nilStore.Lookup(context.Background(), intervention.ReceiptAxis{})
	require.Error(t, err)
	_, _, err = nilStore.Commit(context.Background(), intervention.Receipt{})
	require.Error(t, err)

	store := NewInterventionReceiptStore(nil)
	_, _, err = store.Lookup(context.Background(), intervention.ReceiptAxis{})
	require.Error(t, err)
	_, _, err = store.Lookup(nil, intervention.ReceiptAxis{})
	require.Error(t, err)
	_, _, err = store.Commit(context.Background(), intervention.Receipt{})
	require.Error(t, err)
	_, _, err = store.Commit(nil, intervention.Receipt{})
	require.Error(t, err)
}

func TestInterventionReceiptStoreLookupAndCommit(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	store := NewInterventionReceiptStore(db)
	fixture := newInterventionReceiptStoreFixture(t)
	receipt := newInterventionReceiptStoreReceipt(t, fixture, uuid.NewString())

	missing, found, err := store.Lookup(context.Background(), receipt.Axis())
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, intervention.Receipt{}, missing)

	committed, inserted, err := store.Commit(context.Background(), receipt)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, receipt.PersistenceRecord(), committed.PersistenceRecord())
	require.True(t, committed.Verify(fixture.epoch))
	require.False(t, committed.CanCommit())
	trustedCommitted := requireInterventionReceiptStoreTrusted(t, committed, fixture.epoch)

	foundReceipt, found, err := store.Lookup(context.Background(), receipt.Axis())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, committed.PersistenceRecord(), foundReceipt.PersistenceRecord())
	require.True(t, foundReceipt.Verify(fixture.epoch))
	require.False(t, foundReceipt.CanCommit())
	trustedFound := requireInterventionReceiptStoreTrusted(t, foundReceipt, fixture.epoch)
	require.Equal(t, trustedCommitted.PersistenceRecord(), trustedFound.PersistenceRecord())
}

func TestInterventionReceiptStoreRoundTripsContextReferenceSelection(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	store := NewInterventionReceiptStore(db)
	fixture := newInterventionReceiptStoreFixture(t)
	axis := fixture.axis(t)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	ctx, cancel := context.WithDeadline(context.Background(), createdAt.Add(time.Minute))
	defer cancel()
	reference, err := taskmemory.NewAuthorizedCandidateRef(91, 3, taskmemory.CandidateExact)
	require.NoError(t, err)
	materialized, err := taskmemory.NewMaterializedCandidate(reference, fixture.canonicalProject, "Use the immutable retry receipt.")
	require.NoError(t, err)
	receipt, err := intervention.NewContextReferenceReceipt(ctx, fixture.epoch, axis, intervention.ReceiptCreation{
		ReceiptID:   uuid.NewString(),
		OperationID: uuid.NewString(),
		CreatedAt:   createdAt,
	}, materialized, 1)
	require.NoError(t, err)

	committed, inserted, err := store.Commit(context.Background(), receipt)
	require.NoError(t, err)
	require.True(t, inserted)
	require.True(t, committed.Verify(fixture.epoch))
	record := committed.PersistenceRecord()
	require.Equal(t, intervention.ReceiptDecisionModeContextReference, record.DecisionMode)
	require.EqualValues(t, 0, record.EligibleCount)
	require.NotNil(t, record.Selection)
	memoryID, memoryVersion, sourceProject, sourceTier, textDigest, ok := record.Selection.ContextReference()
	require.True(t, ok)
	require.EqualValues(t, reference.ID(), memoryID)
	require.EqualValues(t, reference.Version(), memoryVersion)
	require.Equal(t, fixture.canonicalProject, sourceProject)
	require.Equal(t, intervention.CandidateTierExact, sourceTier)
	require.Equal(t, intervention.Digest(materialized.TextDigest()), textDigest)
	_, _, _, _, _, _, learned := record.Selection.LearnedIntervention()
	require.False(t, learned, "context receipts must not rehydrate learned-policy fields")

	found, exists, err := store.Lookup(context.Background(), axis)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, record, found.PersistenceRecord())
	require.True(t, found.Verify(fixture.epoch))
}

func TestInterventionReceiptStoreRejectsRestoredReceiptsBeforeSQL(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	store := NewInterventionReceiptStore(db)
	fixture := newInterventionReceiptStoreFixture(t)
	signed := newInterventionReceiptStoreReceipt(t, fixture, uuid.NewString())

	restored, err := intervention.RestoreUnverifiedReceipt(signed.PersistenceRecord())
	require.NoError(t, err)
	require.False(t, restored.CanCommit())
	trustedRestored := requireInterventionReceiptStoreTrusted(t, restored, fixture.epoch)
	_, inserted, err := store.Commit(context.Background(), trustedRestored)
	require.Error(t, err)
	require.False(t, inserted)

	var count int64
	require.NoError(t, db.Table("task_memory_intervention_receipts").Count(&count).Error)
	require.Zero(t, count, "verified restored receipts must not be recommitted")

	forgedRecord := signed.PersistenceRecord()
	forgedRecord.IntegrityDigest[0] ^= 0x80
	forged, err := intervention.RestoreUnverifiedReceipt(forgedRecord)
	require.NoError(t, err)
	require.False(t, forged.Verify(fixture.epoch))
	require.False(t, forged.CanCommit())
	_, inserted, err = store.Commit(context.Background(), forged)
	require.Error(t, err)
	require.False(t, inserted)

	require.NoError(t, db.Table("task_memory_intervention_receipts").Count(&count).Error)
	require.Zero(t, count, "forged restored receipts must be rejected before SQL")
}

func TestInterventionReceiptStoreConcurrentSameAxisReturnsFirstWinner(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)

	store := NewInterventionReceiptStore(db)
	fixture := newInterventionReceiptStoreFixture(t)
	candidates := []intervention.Receipt{
		newInterventionReceiptStoreReceipt(t, fixture, uuid.NewString()),
		newInterventionReceiptStoreReceipt(t, fixture, uuid.NewString()),
	}

	type result struct {
		receipt  intervention.Receipt
		inserted bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, len(candidates))
	var workers sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			committed, inserted, err := store.Commit(context.Background(), candidate)
			results <- result{receipt: committed, inserted: inserted, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var committed []intervention.Receipt
	insertions := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.inserted {
			insertions++
		}
		require.True(t, result.receipt.Verify(fixture.epoch))
		require.False(t, result.receipt.CanCommit())
		requireInterventionReceiptStoreTrusted(t, result.receipt, fixture.epoch)
		committed = append(committed, result.receipt)
	}
	require.Len(t, committed, 2)
	require.Equal(t, 1, insertions, "exactly one transaction must win the occurrence insert")
	require.Equal(t, committed[0].PersistenceRecord(), committed[1].PersistenceRecord(), "loser must return the committed immutable winner")

	var count int64
	require.NoError(t, db.Table("task_memory_intervention_receipts").Count(&count).Error)
	require.EqualValues(t, 1, count, "same-axis concurrency must retain one row")
}

func TestInterventionReceiptStoreDistinctAxisDimensionsDoNotCollide(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	store := NewInterventionReceiptStore(db)
	base := newInterventionReceiptStoreFixture(t)

	fixtures := make([]interventionReceiptStoreFixture, 0, 6)
	channel := base
	channel.runtimeInstance = "receipt-store-runtime-2"
	fixtures = append(fixtures, channel)
	project := base
	project.canonicalProject = uuid.NewString()
	fixtures = append(fixtures, project)
	principal := base
	principal.actorPrincipal = "receipt-store-agent-2"
	fixtures = append(fixtures, principal)
	kind := base
	kind.actorKind = "human"
	fixtures = append(fixtures, kind)
	workstation := base
	workstation.workstation = "receipt-store-workstation-2"
	fixtures = append(fixtures, workstation)
	occurrence := base
	occurrence.phaseAnchor = "receipt-store-turn-2"
	fixtures = append(fixtures, occurrence)

	for _, fixture := range fixtures {
		receipt := newInterventionReceiptStoreReceipt(t, fixture, uuid.NewString())
		_, inserted, err := store.Commit(context.Background(), receipt)
		require.NoError(t, err)
		require.True(t, inserted, "a distinct six-axis dimension must not collide")
	}

	var count int64
	require.NoError(t, db.Table("task_memory_intervention_receipts").Count(&count).Error)
	require.EqualValues(t, len(fixtures), count)
}

func TestInterventionReceiptStoreOperationIDIsAuditOnly(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	store := NewInterventionReceiptStore(db)
	fixture := newInterventionReceiptStoreFixture(t)
	operationID := uuid.NewString()

	first := newInterventionReceiptStoreReceipt(t, fixture, operationID)
	_, inserted, err := store.Commit(context.Background(), first)
	require.NoError(t, err)
	require.True(t, inserted)

	secondFixture := fixture
	secondFixture.phaseAnchor = "receipt-store-turn-2"
	second := newInterventionReceiptStoreReceipt(t, secondFixture, operationID)
	_, inserted, err = store.Commit(context.Background(), second)
	require.Error(t, err, "operation-id uniqueness must not return an unrelated replay winner")
	require.False(t, inserted)

	var count int64
	require.NoError(t, db.Table("task_memory_intervention_receipts").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInterventionReceiptStoreDistinctEpochsZeroOneAndMalformedWidth(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	store := NewInterventionReceiptStore(db)

	epochs, err := store.DistinctInterventionReceiptEpochs(context.Background())
	require.NoError(t, err)
	require.Empty(t, epochs)

	fixture := newInterventionReceiptStoreFixture(t)
	first := newInterventionReceiptStoreReceipt(t, fixture, uuid.NewString())
	_, inserted, err := store.Commit(context.Background(), first)
	require.NoError(t, err)
	require.True(t, inserted)

	secondFixture := fixture
	secondFixture.phaseAnchor = "receipt-store-turn-2"
	second := newInterventionReceiptStoreReceipt(t, secondFixture, uuid.NewString())
	_, inserted, err = store.Commit(context.Background(), second)
	require.NoError(t, err)
	require.True(t, inserted)

	epochs, err = store.DistinctInterventionReceiptEpochs(context.Background())
	require.NoError(t, err)
	require.Equal(t, [][32]byte{fixture.epoch.EpochCommitment()}, epochs)

	// Isolated-schema corruption proves the reader refuses to truncate a bad
	// historical width. The test schema is dropped rather than deleting any row.
	require.NoError(t, db.Exec(`ALTER TABLE task_memory_intervention_receipts DROP CONSTRAINT task_memory_intervention_receipts_commitment_width`).Error)
	malformedFixture := fixture
	malformedFixture.phaseAnchor = "receipt-store-turn-3"
	malformedReceipt := newInterventionReceiptStoreReceipt(t, malformedFixture, uuid.NewString())
	malformed, err := interventionReceiptRowFromRecord(malformedReceipt.PersistenceRecord())
	require.NoError(t, err)
	malformed.KeyEpochCommitment = []byte{1}
	_, inserted, err = insertInterventionReceiptRow(context.Background(), db, malformed)
	require.NoError(t, err)
	require.True(t, inserted)

	_, err = store.DistinctInterventionReceiptEpochs(context.Background())
	require.ErrorContains(t, err, "exactly 32 bytes")
}

type interventionReceiptStoreFixture struct {
	epoch            intervention.KeyEpoch
	runtimeInstance  string
	canonicalProject string
	actorPrincipal   string
	actorKind        string
	workstation      string
	phaseAnchor      string
}

func newInterventionReceiptStoreFixture(t *testing.T) interventionReceiptStoreFixture {
	t.Helper()
	epoch, err := intervention.NewKeyEpoch(
		interventionReceiptStoreBytes(1),
		interventionReceiptStoreBytes(2),
		interventionReceiptStoreBytes(3),
		interventionReceiptStoreBytes(4),
		interventionReceiptStoreBytes(5),
	)
	require.NoError(t, err)
	return interventionReceiptStoreFixture{
		epoch:            epoch,
		runtimeInstance:  "receipt-store-runtime-1",
		canonicalProject: uuid.NewString(),
		actorPrincipal:   "receipt-store-agent",
		actorKind:        "agent",
		workstation:      "receipt-store-workstation",
		phaseAnchor:      "receipt-store-turn-1",
	}
}

func newInterventionReceiptStoreReceipt(t *testing.T, fixture interventionReceiptStoreFixture, operationID string) intervention.Receipt {
	t.Helper()
	axis := fixture.axis(t)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	ctx, cancel := context.WithDeadline(context.Background(), createdAt.Add(time.Minute))
	defer cancel()
	receipt, err := intervention.NewNoCandidatesReceipt(ctx, fixture.epoch, axis, uuid.NewString(), operationID, createdAt)
	require.NoError(t, err)
	require.True(t, receipt.CanCommit())
	require.True(t, receipt.Verify(fixture.epoch))
	return receipt
}

func (fixture interventionReceiptStoreFixture) axis(t *testing.T) intervention.ReceiptAxis {
	t.Helper()
	facts, err := intervention.NewBeforeAgentStartFacts("receipt-store-task", nil)
	require.NoError(t, err)
	occurrence, err := intervention.NewBeforeAgentStartOccurrence("receipt-store-session", fixture.phaseAnchor, facts)
	require.NoError(t, err)
	axis, err := intervention.NewReceiptAxis(fixture.epoch, fixture.binding(t), fixture.authority(t), occurrence)
	require.NoError(t, err)
	return axis
}

func (fixture interventionReceiptStoreFixture) binding(t *testing.T) intervention.BindingFacts {
	t.Helper()
	profile, err := hostadvisor.NewOMPAdvisor1Profile(hostadvisor.OMPAdvisor1ProfileSpec{
		HostVersion:             "1.0.0",
		AdapterID:               "receipt-store-adapter",
		AdapterVersion:          "1.0.0",
		InstalledArtifactDigest: hostadvisor.Digest(interventionReceiptStoreBytes(18)),
		RuntimeProbeReceiptID:   "receipt-store-probe",
		SnapshotID:              "receipt-store-snapshot",
		SnapshotRevision:        1,
		CallbackDeadline:        time.Second,
		BindingTTL:              time.Hour,
	})
	require.NoError(t, err)
	registry, err := hostadvisor.NewRegistry(hostadvisor.RegistryConfig{
		Profiles:    []hostadvisor.AcceptedProfile{profile},
		MaxBindings: 1,
		Now: func() time.Time {
			return time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
		},
		NewBindingID: func() (hostadvisor.BindingID, error) {
			return hostadvisor.BindingID("receipt-store-binding"), nil
		},
	})
	require.NoError(t, err)
	subject, err := hostadvisor.NewAuthenticatedSubject(hostadvisor.Digest(interventionReceiptStoreBytes(19)))
	require.NoError(t, err)
	binding, err := registry.Bind(subject, hostadvisor.HostHello{
		Protocol: profile.Protocol,
		Host: hostadvisor.HostIdentity{
			Family:             hostadvisor.HostFamilyOMP,
			HostVersion:        "1.0.0",
			AdapterID:          "receipt-store-adapter",
			AdapterVersion:     "1.0.0",
			RuntimeInstanceRef: fixture.runtimeInstance,
		},
		Requested: profile.Capabilities,
		Evidence:  profile.Evidence,
	})
	require.NoError(t, err)
	facts, err := intervention.NewBindingFacts(binding)
	require.NoError(t, err)
	return facts
}

func (fixture interventionReceiptStoreFixture) authority(t *testing.T) taskmemory.AuthorizedTaskContext {
	t.Helper()
	caller, err := taskmemory.NewAuthenticatedCaller(
		"client",
		"read-write",
		fixture.workstation,
		fixture.actorPrincipal,
		fixture.actorKind,
		nil,
	)
	require.NoError(t, err)
	project, err := projectidentity.NewProjectKeyV3(fixture.canonicalProject)
	require.NoError(t, err)
	correlation, err := projectidentity.NewCorrelationV3("receipt-store-correlation")
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
	authority, err := taskmemory.NewAuthorizedTaskContext(caller, resolution, "receipt-store-authority-session")
	require.NoError(t, err)
	return authority
}

func requireInterventionReceiptStoreTrusted(t *testing.T, receipt intervention.Receipt, epoch intervention.KeyEpoch) intervention.Receipt {
	t.Helper()
	trusted, verified := receipt.VerifyTrusted(epoch)
	require.True(t, verified)
	require.False(t, trusted.CanCommit())
	return trusted
}

func interventionReceiptStoreBytes(seed byte) [32]byte {
	var value [32]byte
	for index := range value {
		value[index] = seed
	}
	return value
}

// Compile-time assertion preserves the only shared runtime persistence port.
var _ intervention.ReceiptStore = (*InterventionReceiptStore)(nil)
