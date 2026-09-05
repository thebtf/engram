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
)

func TestUCILocalRegistryRecordsOnlyOperationalIdentityFacts(t *testing.T) {
	ctx := context.Background()
	db, registry := newUCILocalRegistry(t)

	root := uciLocalApprovedRoot()
	require.NoError(t, registry.RecordApprovedRoot(ctx, root))

	persistedRoot, found, err := registry.ApprovedRoot(ctx, root.RootID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, root, persistedRoot)
	conflictingRoot := root
	conflictingRoot.SourceID = "source:opaque:conflict"
	require.Error(t, registry.RecordApprovedRoot(ctx, conflictingRoot), "an opaque root ID must not be rebound to another source")
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
	require.NotEmpty(t, checkout.IncarnationID)

	assertUCILocalRegistryNarrowSurface(t, db)
}

func TestUCILocalRegistryIssuesAndPreservesIncarnationsOnlyWithContinuity(t *testing.T) {
	ctx := context.Background()
	_, registry := newUCILocalRegistry(t)
	require.NoError(t, registry.RecordApprovedRoot(ctx, uciLocalApprovedRoot()))

	initialRegistration := uciLocalRegistration(uciLocalCheckoutID)
	first, err := registry.RegisterCheckout(ctx, initialRegistration)
	require.NoError(t, err)
	require.NotEmpty(t, first.IncarnationID)

	exactReplay, err := registry.RegisterCheckout(ctx, initialRegistration)
	require.NoError(t, err)
	require.Equal(t, first.IncarnationID, exactReplay.IncarnationID, "an exact registration replay must preserve the local incarnation")

	moveRegistration := uciLocalRegistration("checkout:opaque:moved")
	moveRegistration.Continuity = &codeintel.UCILocalContinuityEvidence{
		PreviousCheckoutID:       first.CheckoutID,
		PreviousIncarnationID:    first.IncarnationID,
		RootID:                   first.RootID,
		SourceID:                 first.SourceID,
		CommonGitDirFingerprint:  first.CommonGitDirFingerprint,
		PrivateGitDirFingerprint: first.PrivateGitDirFingerprint,
		WorkstationID:            first.WorkstationID,
		ClientInstanceID:         first.ClientInstanceID,
	}
	moved, err := registry.RegisterCheckout(ctx, moveRegistration)
	require.NoError(t, err)
	require.Equal(t, first.IncarnationID, moved.IncarnationID, "only exact local continuity evidence may preserve a moved checkout incarnation")
	require.Equal(t, moveRegistration.CheckoutID, moved.CheckoutID)

	copyRegistration := uciLocalRegistration("checkout:opaque:copy")
	copyRecord, err := registry.RegisterCheckout(ctx, copyRegistration)
	require.NoError(t, err)
	require.NotEqual(t, first.IncarnationID, copyRecord.IncarnationID, "a copy without continuity evidence is a new local incarnation")

	recreatedRegistration := initialRegistration
	recreatedRegistration.PrivateGitDirFingerprint = "sha256:private-git-dir:recreated"
	recreated, err := registry.RegisterCheckout(ctx, recreatedRegistration)
	require.NoError(t, err)
	require.NotEqual(t, first.IncarnationID, recreated.IncarnationID, "a recreated checkout at the same opaque ID must not inherit its incarnation")

	mismatchedMove := moveRegistration
	mismatchedMove.CheckoutID = "checkout:opaque:mismatched-move"
	mismatchedMove.PrivateGitDirFingerprint = "sha256:private-git-dir:not-continuous"
	mismatched, err := registry.RegisterCheckout(ctx, mismatchedMove)
	require.NoError(t, err)
	require.NotEqual(t, first.IncarnationID, mismatched.IncarnationID, "partial continuity evidence must not preserve an incarnation")

	otherWorkstationMove := moveRegistration
	otherWorkstationMove.CheckoutID = "checkout:opaque:other-workstation"
	otherWorkstationMove.WorkstationID = "workstation:opaque:beta"
	otherWorkstation, err := registry.RegisterCheckout(ctx, otherWorkstationMove)
	require.NoError(t, err)
	require.NotEqual(t, first.IncarnationID, otherWorkstation.IncarnationID, "a workstation change is not a continuous local checkout")

	lostDBPath := filepath.Join(t.TempDir(), "lost-registry.sqlite")
	lostDB, lostRegistry := openUCILocalRegistry(t, lostDBPath)
	t.Cleanup(func() { _ = lostDB.Close() })
	require.NoError(t, lostRegistry.RecordApprovedRoot(ctx, uciLocalApprovedRoot()))
	afterLostDB, err := lostRegistry.RegisterCheckout(ctx, initialRegistration)
	require.NoError(t, err)
	require.NotEqual(t, first.IncarnationID, afterLostDB.IncarnationID, "a lost local database has no continuity evidence and must issue a new incarnation")
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
	require.NoError(t, registry.RecordApprovedRoot(ctx, uciLocalApprovedRoot()))
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
	require.Equal(t, uciLocalApprovedRoot(), persistedRoot)

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
	require.NoError(t, registry.RecordApprovedRoot(ctx, uciLocalApprovedRoot()))
	checkout, err := registry.RegisterCheckout(ctx, uciLocalRegistration(uciLocalCheckoutID))
	require.NoError(t, err)
	return checkout
}

func uciLocalApprovedRoot() codeintel.UCILocalApprovedRoot {
	return codeintel.UCILocalApprovedRoot{
		RootID:                  uciLocalRootID,
		SourceID:                uciLocalSourceID,
		CommonGitDirFingerprint: uciLocalCommonGitDir,
	}
}

func uciLocalRegistration(checkoutID string) codeintel.UCILocalCheckoutRegistration {
	return codeintel.UCILocalCheckoutRegistration{
		RootID:                   uciLocalRootID,
		SourceID:                 uciLocalSourceID,
		CheckoutID:               checkoutID,
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

	for _, operationalType := range []reflect.Type{
		reflect.TypeOf(codeintel.UCILocalApprovedRoot{}),
		reflect.TypeOf(codeintel.UCILocalCheckoutRegistration{}),
		reflect.TypeOf(codeintel.UCILocalContinuityEvidence{}),
		reflect.TypeOf(codeintel.UCILocalCheckoutRecord{}),
		reflect.TypeOf(codeintel.UCILocalDirtyChange{}),
		reflect.TypeOf(codeintel.UCILocalOfflineRecovery{}),
	} {
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
