package codeintel_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/handlers/codeintel"
	_ "modernc.org/sqlite"
)

const (
	uciLocalRootID       = "root:opaque:workstation-a"
	uciLocalSourceID     = "source:opaque:7d9b2e"
	uciLocalCheckoutID   = "checkout:opaque:primary"
	uciLocalCommonGitDir = "sha256:common-git-dir:97d1"
	uciLocalPrivateGit   = "sha256:private-git-dir:4a1c"
	uciLocalWorkstation  = "workstation:opaque:alpha"
	uciLocalClient       = "client-instance:opaque:alpha-1"
	uciLocalIncarnationA = "11111111-1111-4111-8111-111111111111"
	uciLocalIncarnationB = "22222222-2222-4222-8222-222222222222"
	uciLocalIncarnationC = "33333333-3333-4333-8333-333333333333"
)

func TestUCILocalRegistryRecordsOnlyOperationalIdentityFacts(t *testing.T) {
	ctx := context.Background()
	db, registry := newUCILocalRegistry(t)

	root := uciLocalApprovedRoot(t)
	require.NoError(t, registry.RecordApprovedRoot(ctx, root))

	persistedRoot, found, err := registry.ApprovedRoot(ctx, root.RootID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, root, persistedRoot)
	conflictingRoot := root
	conflictingRoot.SourceID = "source:opaque:conflict"
	require.Error(t, registry.RecordApprovedRoot(ctx, conflictingRoot), "an opaque root ID must not be rebound to another source")
	conflictingRoot = root
	conflictingRoot.CommonGitDirFingerprint = "sha256:common-git-dir:conflict"
	require.Error(t, registry.RecordApprovedRoot(ctx, conflictingRoot), "an opaque root ID must not be rebound to another common Git identity")
	persistedRoot, found, err = registry.ApprovedRoot(ctx, root.RootID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, root, persistedRoot)

	missingRoot, found, err := registry.ApprovedRoot(ctx, "root:opaque:missing")
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, missingRoot)

	checkout, err := registry.RegisterCheckout(ctx, uciLocalRegistration(uciLocalCheckoutID))
	require.NoError(t, err)
	require.Equal(t, uciLocalRootID, checkout.RootID)
	require.Equal(t, uciLocalSourceID, checkout.SourceID)
	require.Equal(t, uciLocalCheckoutID, checkout.CheckoutID)
	require.Equal(t, uciLocalCommonGitDir, checkout.CommonGitDirFingerprint)
	require.Equal(t, uciLocalPrivateGit, checkout.PrivateGitDirFingerprint)
	require.Equal(t, uciLocalWorkstation, checkout.WorkstationID)
	require.Equal(t, uciLocalClient, checkout.ClientInstanceID)
	require.Equal(t, uciLocalIncarnationA, checkout.IncarnationID)

	assertUCILocalRegistryNarrowSurface(t, db)
}

func TestUCILocalRegistryPreservesExactServerRegistration(t *testing.T) {
	ctx := context.Background()
	_, registry := newUCILocalRegistry(t)
	require.NoError(t, registry.RecordApprovedRoot(ctx, uciLocalApprovedRoot(t)))

	registration := uciLocalRegistration(uciLocalCheckoutID)
	first, err := registry.RegisterCheckout(ctx, registration)
	require.NoError(t, err)
	require.Equal(t, registration.IncarnationID, first.IncarnationID)

	state, err := registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   first.CheckoutID,
		RelativePath: "internal/changed.go",
		Sequence:     1,
	})
	require.NoError(t, err)
	recovery := codeintel.UCILocalOfflineRecovery{
		CheckoutID:         first.CheckoutID,
		FromSequence:       2,
		ThroughSequence:    2,
		MaxDirtyPaths:      2,
		ObservedDirtyPaths: 1,
	}
	state, err = registry.RecordOfflineRecovery(ctx, recovery)
	require.NoError(t, err)

	replayed, err := registry.RegisterCheckout(ctx, registration)
	require.NoError(t, err)
	require.Equal(t, registration.IncarnationID, replayed.IncarnationID)
	require.Equal(t, state.DirtySequence, replayed.DirtySequence)
	require.Equal(t, state.DirtyPaths, replayed.DirtyPaths)
	require.Equal(t, recovery, replayed.OfflineRecovery)
}

func TestUCILocalRegistryResetsStateForChangedServerIncarnation(t *testing.T) {
	ctx := context.Background()
	_, registry := newUCILocalRegistry(t)
	require.NoError(t, registry.RecordApprovedRoot(ctx, uciLocalApprovedRoot(t)))

	registration := uciLocalRegistration(uciLocalCheckoutID)
	first, err := registry.RegisterCheckout(ctx, registration)
	require.NoError(t, err)
	_, err = registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   first.CheckoutID,
		RelativePath: "internal/recreated.go",
		Sequence:     1,
	})
	require.NoError(t, err)
	_, err = registry.RecordOfflineRecovery(ctx, codeintel.UCILocalOfflineRecovery{
		CheckoutID:         first.CheckoutID,
		FromSequence:       2,
		ThroughSequence:    2,
		MaxDirtyPaths:      2,
		ObservedDirtyPaths: 1,
	})
	require.NoError(t, err)

	recreated := registration
	recreated.IncarnationID = uciLocalIncarnationB
	recreated.PrivateGitDirFingerprint = "sha256:private-git-dir:recreated"
	replaced, err := registry.RegisterCheckout(ctx, recreated)
	require.NoError(t, err)
	require.Equal(t, recreated.IncarnationID, replaced.IncarnationID)
	require.Zero(t, replaced.DirtySequence)
	require.Empty(t, replaced.DirtyPaths)
	require.True(t, replaced.RescanRequired)
	require.Empty(t, replaced.RescanCauses)
	require.Zero(t, replaced.OfflineRecovery)
}

func TestUCILocalRegistryStartsNewStateForCopy(t *testing.T) {
	ctx := context.Background()
	_, registry := newUCILocalRegistry(t)
	require.NoError(t, registry.RecordApprovedRoot(ctx, uciLocalApprovedRoot(t)))

	original, err := registry.RegisterCheckout(ctx, uciLocalRegistration(uciLocalCheckoutID))
	require.NoError(t, err)
	_, err = registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   original.CheckoutID,
		RelativePath: "internal/original.go",
		Sequence:     1,
	})
	require.NoError(t, err)

	copyRegistration := uciLocalRegistration("checkout:opaque:copy")
	copyRegistration.IncarnationID = uciLocalIncarnationC
	copyRecord, err := registry.RegisterCheckout(ctx, copyRegistration)
	require.NoError(t, err)
	require.Equal(t, copyRegistration.IncarnationID, copyRecord.IncarnationID)
	require.Zero(t, copyRecord.DirtySequence)
	require.Empty(t, copyRecord.DirtyPaths)
	require.True(t, copyRecord.RescanRequired)
}

func TestUCILocalRegistryUpdatesOnlyLocalRootPathForVerifiedMove(t *testing.T) {
	ctx := context.Background()
	_, registry := newUCILocalRegistry(t)
	root := uciLocalApprovedRoot(t)
	require.NoError(t, registry.RecordApprovedRoot(ctx, root))

	registration := uciLocalRegistration(uciLocalCheckoutID)
	checkout, err := registry.RegisterCheckout(ctx, registration)
	require.NoError(t, err)
	state, err := registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: "internal/moved.go",
		Sequence:     1,
	})
	require.NoError(t, err)

	movedRoot := root
	movedRoot.RootPath = t.TempDir()
	require.NoError(t, registry.RecordApprovedRoot(ctx, movedRoot))
	persistedRoot, found, err := registry.ApprovedRoot(ctx, root.RootID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, movedRoot, persistedRoot)

	replayed, err := registry.RegisterCheckout(ctx, registration)
	require.NoError(t, err)
	require.Equal(t, registration.IncarnationID, replayed.IncarnationID)
	require.Equal(t, state.DirtySequence, replayed.DirtySequence)
	require.Equal(t, state.DirtyPaths, replayed.DirtyPaths)
}

func TestUCILocalRegistryRefusesMismatchedEvidenceForSameServerIncarnation(t *testing.T) {
	ctx := context.Background()
	_, registry := newUCILocalRegistry(t)
	require.NoError(t, registry.RecordApprovedRoot(ctx, uciLocalApprovedRoot(t)))

	registration := uciLocalRegistration(uciLocalCheckoutID)
	checkout, err := registry.RegisterCheckout(ctx, registration)
	require.NoError(t, err)
	baseline, err := registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: "internal/retained.go",
		Sequence:     1,
	})
	require.NoError(t, err)

	mismatch := registration
	mismatch.PrivateGitDirFingerprint = "sha256:private-git-dir:mismatch"
	_, err = registry.RegisterCheckout(ctx, mismatch)
	require.Error(t, err)
	current, found, err := registry.Snapshot(ctx, checkout.CheckoutID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, baseline, current)

	invalid := registration
	invalid.IncarnationID = strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	_, err = registry.RegisterCheckout(ctx, invalid)
	require.Error(t, err)
}

func TestUCILocalRegistryMigratesV1DatabaseTransactionally(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "registry.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	seedUCILocalRegistryV1(t, db)

	registry, err := codeintel.NewUCILocalRegistry(db)
	require.NoError(t, err)

	var version int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT version FROM uci_local_registry_schema WHERE singleton = 1`).Scan(&version))
	require.Equal(t, 2, version)

	root, found, err := registry.ApprovedRoot(ctx, uciLocalRootID)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, root.RootPath)
	root.RootPath = t.TempDir()
	require.NoError(t, registry.RecordApprovedRoot(ctx, root))

	persistedRoot, found, err := registry.ApprovedRoot(ctx, uciLocalRootID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, root, persistedRoot)
	checkout, found, err := registry.Snapshot(ctx, uciLocalCheckoutID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uciLocalIncarnationA, checkout.IncarnationID)
	require.Equal(t, int64(4), checkout.DirtySequence)
	require.Equal(t, map[string]int64{"internal/legacy.go": 4}, checkout.DirtyPaths)
	require.Equal(t, codeintel.UCILocalOfflineRecovery{
		CheckoutID:         uciLocalCheckoutID,
		FromSequence:       5,
		ThroughSequence:    5,
		MaxDirtyPaths:      2,
		ObservedDirtyPaths: 1,
	}, checkout.OfflineRecovery)

	_, err = codeintel.NewUCILocalRegistry(db)
	require.NoError(t, err)
}

func TestUCILocalRegistryCoalescesDirtyStateAndTracksReconciliation(t *testing.T) {
	ctx := context.Background()
	_, registry := newUCILocalRegistry(t)
	checkout := registerUCILocalCheckout(t, registry)

	state, err := registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: "internal/a.go",
		Sequence:     1,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), state.DirtySequence)
	require.Equal(t, map[string]int64{"internal/a.go": 1}, state.DirtyPaths)

	replayed, err := registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: "internal/a.go",
		Sequence:     1,
	})
	require.NoError(t, err)
	require.Equal(t, state.DirtySequence, replayed.DirtySequence)
	require.Equal(t, state.DirtyPaths, replayed.DirtyPaths, "exact dirty-event replay must be idempotent")

	state, err = registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: "internal/a.go",
		Sequence:     2,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"internal/a.go": 2}, state.DirtyPaths, "a later event replaces the pending state for its path")

	state, err = registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: "internal/b.go",
		Sequence:     3,
	})
	require.NoError(t, err)
	require.Equal(t, int64(3), state.DirtySequence)
	require.Equal(t, map[string]int64{"internal/a.go": 2, "internal/b.go": 3}, state.DirtyPaths)

	state, err = registry.MarkReconciled(ctx, checkout.CheckoutID, 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), state.LastReconciledSequence)
	require.Equal(t, map[string]int64{"internal/b.go": 3}, state.DirtyPaths, "acknowledgement removes only paths at or below its accepted sequence")

	state, err = registry.MarkReconciled(ctx, checkout.CheckoutID, 3)
	require.NoError(t, err)
	require.Equal(t, int64(3), state.LastReconciledSequence)
	require.Empty(t, state.DirtyPaths)

	state, err = registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: "internal/stale.go",
		Sequence:     2,
	})
	require.NoError(t, err)
	require.Equal(t, int64(3), state.DirtySequence, "stale events may not move the checkout sequence backward")
	require.Equal(t, int64(2), state.DirtyPaths["internal/stale.go"], "a stale event still leaves a safe path hint for reconciliation")
	requireUCILocalRescanCauses(t, state.RescanCauses, codeintel.UCILocalRescanStaleSequence)

	state, err = registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: "internal/gap.go",
		Sequence:     5,
	})
	require.NoError(t, err)
	require.Equal(t, int64(5), state.DirtySequence)
	requireUCILocalRescanCauses(t, state.RescanCauses, codeintel.UCILocalRescanSequenceGap)

	for _, cause := range []codeintel.UCILocalRescanCause{
		codeintel.UCILocalRescanOverflow,
		codeintel.UCILocalRescanRestart,
		codeintel.UCILocalRescanMove,
		codeintel.UCILocalRescanGitTransition,
		codeintel.UCILocalRescanWatcherRegistrationFailure,
	} {
		state, err = registry.RequireRescan(ctx, checkout.CheckoutID, cause)
		require.NoError(t, err)
		require.True(t, state.RescanRequired)
		requireUCILocalRescanCauses(t, state.RescanCauses, cause)
	}

	recovery := codeintel.UCILocalOfflineRecovery{
		CheckoutID:         checkout.CheckoutID,
		FromSequence:       4,
		ThroughSequence:    9,
		MaxDirtyPaths:      2,
		ObservedDirtyPaths: 3,
	}
	state, err = registry.RecordOfflineRecovery(ctx, recovery)
	require.NoError(t, err)
	require.Equal(t, recovery, state.OfflineRecovery, "offline recovery keeps bounded metadata rather than an unbounded event archive")
	requireUCILocalRescanCauses(t, state.RescanCauses, codeintel.UCILocalRescanOfflineRecoveryBound)

	replayedRecovery, err := registry.RecordOfflineRecovery(ctx, recovery)
	require.NoError(t, err)
	require.Equal(t, state.OfflineRecovery, replayedRecovery.OfflineRecovery)
	require.Equal(t, state.RescanCauses, replayedRecovery.RescanCauses, "replaying the same bounded recovery report must be idempotent")
}

func TestUCILocalRegistryRecoversCurrentStateAcrossCrashWithoutEventRetention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "registry.sqlite")

	db, registry := openUCILocalRegistry(t, path)
	root := uciLocalApprovedRoot(t)
	require.NoError(t, registry.RecordApprovedRoot(ctx, root))
	checkout, err := registry.RegisterCheckout(ctx, uciLocalRegistration(uciLocalCheckoutID))
	require.NoError(t, err)

	_, err = registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{CheckoutID: checkout.CheckoutID, RelativePath: "internal/a.go", Sequence: 1})
	require.NoError(t, err)
	_, err = registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{CheckoutID: checkout.CheckoutID, RelativePath: "internal/a.go", Sequence: 2})
	require.NoError(t, err)
	beforeCrash, err := registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{CheckoutID: checkout.CheckoutID, RelativePath: "internal/b.go", Sequence: 3})
	require.NoError(t, err)

	recovery := codeintel.UCILocalOfflineRecovery{
		CheckoutID:         checkout.CheckoutID,
		FromSequence:       4,
		ThroughSequence:    4,
		MaxDirtyPaths:      2,
		ObservedDirtyPaths: 1,
	}
	beforeCrash, err = registry.RecordOfflineRecovery(ctx, recovery)
	require.NoError(t, err)
	require.NoError(t, db.Close(), "closing the SQLite handle simulates process loss without a registry shutdown path")

	reopenedDB, reopenedRegistry := openUCILocalRegistry(t, path)
	t.Cleanup(func() { _ = reopenedDB.Close() })
	persistedRoot, found, err := reopenedRegistry.ApprovedRoot(ctx, uciLocalRootID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, root, persistedRoot)

	afterCrash, found, err := reopenedRegistry.Snapshot(ctx, checkout.CheckoutID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, beforeCrash.IncarnationID, afterCrash.IncarnationID)
	require.Equal(t, int64(3), afterCrash.DirtySequence)
	require.Equal(t, map[string]int64{"internal/a.go": 2, "internal/b.go": 3}, afterCrash.DirtyPaths)
	require.Equal(t, recovery, afterCrash.OfflineRecovery)

	afterReconcile, err := reopenedRegistry.MarkReconciled(ctx, checkout.CheckoutID, 3)
	require.NoError(t, err)
	require.Empty(t, afterReconcile.DirtyPaths)
	require.Equal(t, int64(3), afterReconcile.LastReconciledSequence)
	require.NoError(t, reopenedDB.Close())

	finalDB, finalRegistry := openUCILocalRegistry(t, path)
	t.Cleanup(func() { _ = finalDB.Close() })
	finalState, found, err := finalRegistry.Snapshot(ctx, checkout.CheckoutID)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, finalState.DirtyPaths, "recovery persists the checkpoint, not a replayable history of prior dirty events")
	require.Equal(t, int64(3), finalState.LastReconciledSequence)
	require.NotContains(t, strings.ToLower(uciLocalRegistrySchema(t, finalDB)), "event", "the local registry may retain coalesced current state but not an event archive")
}

func TestUCILocalRegistryCoalescesConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	_, registry := newUCILocalRegistry(t)
	checkout := registerUCILocalCheckout(t, registry)

	const writers = 24
	start := make(chan struct{})
	errs := make(chan error, writers)
	var writersWG sync.WaitGroup
	writersWG.Add(writers)
	for i := 1; i <= writers; i++ {
		i := i
		go func() {
			defer writersWG.Done()
			<-start
			_, err := registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
				CheckoutID:   checkout.CheckoutID,
				RelativePath: fmt.Sprintf("concurrent/%02d.go", i),
				Sequence:     int64(i),
			})
			errs <- err
		}()
	}
	close(start)
	writersWG.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	state, found, err := registry.Snapshot(ctx, checkout.CheckoutID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(writers), state.DirtySequence)
	require.Len(t, state.DirtyPaths, writers, "concurrent writers must not lose distinct path state")
	for i := 1; i <= writers; i++ {
		require.Equal(t, int64(i), state.DirtyPaths[fmt.Sprintf("concurrent/%02d.go", i)])
	}

	replay, err := registry.RecordDirty(ctx, codeintel.UCILocalDirtyChange{
		CheckoutID:   checkout.CheckoutID,
		RelativePath: fmt.Sprintf("concurrent/%02d.go", writers),
		Sequence:     writers,
	})
	require.NoError(t, err)
	require.Equal(t, state.DirtyPaths, replay.DirtyPaths, "a replay after a concurrent burst must not create a duplicate path record")
}

func newUCILocalRegistry(t *testing.T) (*sql.DB, *codeintel.UCILocalRegistry) {
	t.Helper()
	db, registry := openUCILocalRegistry(t, filepath.Join(t.TempDir(), "registry.sqlite"))
	t.Cleanup(func() { _ = db.Close() })
	return db, registry
}

func openUCILocalRegistry(t *testing.T, path string) (*sql.DB, *codeintel.UCILocalRegistry) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)

	registry, err := codeintel.NewUCILocalRegistry(db)
	require.NoError(t, err)
	return db, registry
}

func registerUCILocalCheckout(t *testing.T, registry *codeintel.UCILocalRegistry) codeintel.UCILocalCheckoutRecord {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, registry.RecordApprovedRoot(ctx, uciLocalApprovedRoot(t)))
	checkout, err := registry.RegisterCheckout(ctx, uciLocalRegistration(uciLocalCheckoutID))
	require.NoError(t, err)
	return checkout
}

func uciLocalApprovedRoot(t *testing.T) codeintel.UCILocalApprovedRoot {
	t.Helper()
	return codeintel.UCILocalApprovedRoot{
		RootID:                  uciLocalRootID,
		SourceID:                uciLocalSourceID,
		CommonGitDirFingerprint: uciLocalCommonGitDir,
		RootPath:                t.TempDir(),
	}
}

func uciLocalRegistration(checkoutID string) codeintel.UCILocalCheckoutRegistration {
	return codeintel.UCILocalCheckoutRegistration{
		RootID:                   uciLocalRootID,
		SourceID:                 uciLocalSourceID,
		CheckoutID:               checkoutID,
		IncarnationID:            uciLocalIncarnationA,
		CommonGitDirFingerprint:  uciLocalCommonGitDir,
		PrivateGitDirFingerprint: uciLocalPrivateGit,
		WorkstationID:            uciLocalWorkstation,
		ClientInstanceID:         uciLocalClient,
	}
}

func requireUCILocalRescanCauses(t *testing.T, actual []codeintel.UCILocalRescanCause, expected ...codeintel.UCILocalRescanCause) {
	t.Helper()
	seen := make(map[codeintel.UCILocalRescanCause]struct{}, len(actual))
	for _, cause := range actual {
		_, duplicate := seen[cause]
		require.Falsef(t, duplicate, "rescan cause %q must be coalesced", cause)
		seen[cause] = struct{}{}
	}
	for _, cause := range expected {
		_, found := seen[cause]
		require.Truef(t, found, "missing rescan cause %q in %v", cause, actual)
	}
}

func assertUCILocalRegistryNarrowSurface(t *testing.T, db *sql.DB) {
	t.Helper()
	registryType := reflect.TypeOf((*codeintel.UCILocalRegistry)(nil))
	for _, forbiddenMethod := range []string{
		"Authorize",
		"Authorization",
		"Search",
		"Graph",
		"Source",
		"ReadSource",
		"Exposure",
		"Query",
	} {
		_, found := registryType.MethodByName(forbiddenMethod)
		require.Falsef(t, found, "local operational state must not expose %s", forbiddenMethod)
	}

	rootEvidence := reflect.TypeOf(codeintel.UCILocalApprovedRoot{})
	_, found := rootEvidence.FieldByName("RootPath")
	require.True(t, found, "only approved-root evidence may retain its local root path")
	for _, operationalType := range []reflect.Type{
		reflect.TypeOf(codeintel.UCILocalCheckoutRegistration{}),
		reflect.TypeOf(codeintel.UCILocalCheckoutRecord{}),
		reflect.TypeOf(codeintel.UCILocalDirtyChange{}),
		reflect.TypeOf(codeintel.UCILocalOfflineRecovery{}),
	} {
		_, found := operationalType.FieldByName("RootPath")
		require.Falsef(t, found, "%s must not expose its local root path", operationalType.Name())
		for _, forbiddenField := range []string{"Token", "AccessToken", "SourceBody", "PrivateSourceBody", "RawSource", "ContentBody"} {
			_, found := operationalType.FieldByName(forbiddenField)
			require.Falsef(t, found, "%s must not retain %s", operationalType.Name(), forbiddenField)
		}
	}

	schema := strings.ToLower(uciLocalRegistrySchema(t, db))
	for _, forbiddenStorage := range []string{"token", "source_body", "private_source_body", "raw_source", "content_body", "authorization", "exposure"} {
		require.NotContainsf(t, schema, forbiddenStorage, "local registry schema must not store %s", forbiddenStorage)
	}
}

func uciLocalRegistrySchema(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT COALESCE(sql, '') FROM sqlite_master WHERE type IN ('table', 'index', 'view', 'trigger')`)
	require.NoError(t, err)
	defer rows.Close()

	var statements []string
	for rows.Next() {
		var statement string
		require.NoError(t, rows.Scan(&statement))
		statements = append(statements, statement)
	}
	require.NoError(t, rows.Err())
	return strings.Join(statements, "\n")
}

func seedUCILocalRegistryV1(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE uci_local_registry_schema (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			version INTEGER NOT NULL
		)`,
		`CREATE TABLE uci_local_approved_roots (
			root_id TEXT PRIMARY KEY,
			server_source_id TEXT NOT NULL,
			common_git_dir_fingerprint TEXT NOT NULL
		)`,
		`CREATE TABLE uci_local_checkouts (
			server_checkout_id TEXT PRIMARY KEY,
			root_id TEXT NOT NULL,
			server_source_id TEXT NOT NULL,
			common_git_dir_fingerprint TEXT NOT NULL,
			private_git_dir_fingerprint TEXT NOT NULL,
			workstation_id TEXT NOT NULL,
			client_instance_id TEXT NOT NULL,
			incarnation_id TEXT NOT NULL,
			dirty_sequence INTEGER NOT NULL DEFAULT 0 CHECK (dirty_sequence >= 0),
			last_reconciled_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_reconciled_sequence >= 0),
			rescan_required INTEGER NOT NULL DEFAULT 0 CHECK (rescan_required IN (0, 1)),
			FOREIGN KEY (root_id) REFERENCES uci_local_approved_roots(root_id)
		)`,
		`CREATE TABLE uci_local_dirty_paths (
			server_checkout_id TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0),
			PRIMARY KEY (server_checkout_id, relative_path),
			FOREIGN KEY (server_checkout_id) REFERENCES uci_local_checkouts(server_checkout_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE uci_local_rescan_causes (
			server_checkout_id TEXT NOT NULL,
			cause TEXT NOT NULL,
			PRIMARY KEY (server_checkout_id, cause),
			FOREIGN KEY (server_checkout_id) REFERENCES uci_local_checkouts(server_checkout_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE uci_local_offline_recovery (
			server_checkout_id TEXT PRIMARY KEY,
			from_sequence INTEGER NOT NULL CHECK (from_sequence >= 0),
			through_sequence INTEGER NOT NULL CHECK (through_sequence >= from_sequence),
			max_dirty_paths INTEGER NOT NULL CHECK (max_dirty_paths > 0),
			observed_dirty_paths INTEGER NOT NULL CHECK (observed_dirty_paths >= 0),
			FOREIGN KEY (server_checkout_id) REFERENCES uci_local_checkouts(server_checkout_id) ON DELETE CASCADE
		)`,
	} {
		_, err := db.Exec(statement)
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO uci_local_registry_schema (singleton, version) VALUES (1, 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO uci_local_approved_roots (
			root_id, server_source_id, common_git_dir_fingerprint
		) VALUES (?, ?, ?)`,
		uciLocalRootID,
		uciLocalSourceID,
		uciLocalCommonGitDir,
	)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO uci_local_checkouts (
			server_checkout_id,
			root_id,
			server_source_id,
			common_git_dir_fingerprint,
			private_git_dir_fingerprint,
			workstation_id,
			client_instance_id,
			incarnation_id,
			dirty_sequence,
			last_reconciled_sequence,
			rescan_required
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 4, 2, 1)`,
		uciLocalCheckoutID,
		uciLocalRootID,
		uciLocalSourceID,
		uciLocalCommonGitDir,
		uciLocalPrivateGit,
		uciLocalWorkstation,
		uciLocalClient,
		uciLocalIncarnationA,
	)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO uci_local_dirty_paths (server_checkout_id, relative_path, sequence)
		VALUES (?, ?, ?)`, uciLocalCheckoutID, "internal/legacy.go", 4)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO uci_local_rescan_causes (server_checkout_id, cause)
		VALUES (?, ?)`, uciLocalCheckoutID, codeintel.UCILocalRescanRestart)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO uci_local_offline_recovery (
			server_checkout_id, from_sequence, through_sequence, max_dirty_paths, observed_dirty_paths
		) VALUES (?, ?, ?, ?, ?)`, uciLocalCheckoutID, 5, 5, 2, 1)
	require.NoError(t, err)
}
