package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	ucidomain "github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

type uciBackupRestoreBinding struct {
	SourceDSN, TargetDSN, SourceOID, TargetOID, SystemIdentifier, KeyPath string
}

// Only the explicitly bound, newly created synthetic databases may enter this test.
func uciBackupRestoreBindingFor(t *testing.T) uciBackupRestoreBinding {
	t.Helper()
	path := os.Getenv("ENGRAM_UCI_RESTORE_FIXTURE")
	if path == "" {
		t.Skip("dedicated two-new-database UCI restore fixture not assigned")
	}
	var binding uciBackupRestoreBinding
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &binding))
	require.NotEmpty(t, binding.SystemIdentifier)
	require.NotEqual(t, "7610612894341156899", binding.SystemIdentifier)
	require.NotEqual(t, binding.SourceOID, binding.TargetOID)
	ctx := context.Background()
	for _, endpoint := range []struct{ dsn, role, oid string }{
		{binding.SourceDSN, "source", binding.SourceOID},
		{binding.TargetDSN, "restore", binding.TargetOID},
	} {
		cfg := consolidationTestDatabaseConfig(t, endpoint.dsn, endpoint.role)
		require.Equal(t, "127.0.0.1", cfg.Host)
		require.Equal(t, "housekeeping_uci_restore", cfg.User)
		require.Equal(t, "engram_migration186_scope_test_hk_01a14587_"+endpoint.role, cfg.Database)
		require.Nil(t, cfg.TLSConfig)
		require.NotEmpty(t, endpoint.oid)
		require.NotEqual(t, "1162015", endpoint.oid)
		pool := stdlib.OpenDB(*cfg)
		pool.SetMaxOpenConns(1)
		var identity string
		queryErr := pool.QueryRowContext(ctx, `SELECT (SELECT system_identifier::text FROM pg_control_system())||'|'||oid::text||'|'||datname FROM pg_database WHERE datname=current_database()`).Scan(&identity)
		if queryErr != nil {
			_ = pool.Close()
			t.Fatal("assigned synthetic database identity unavailable")
		}
		require.Equal(t, binding.SystemIdentifier+"|"+endpoint.oid+"|"+cfg.Database, identity)
		var tables int64
		queryErr = pool.QueryRowContext(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname !~ '^pg_toast' AND c.relkind IN ('r','p','m')`).Scan(&tables)
		require.NoError(t, pool.Close())
		require.NoError(t, queryErr)
		require.Zero(t, tables, "fixture admission requires two new empty databases")
		t.Logf("admitted synthetic %s: %s", endpoint.role, identity)
	}
	return binding
}

func openUCIBackupRestoreStoreFixture(t *testing.T) *streamIntegrityFixture {
	t.Helper()
	binding := uciBackupRestoreBindingFor(t)
	schema := "uci_restore_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	source := openMigratedTestDBInSchema(t, binding.SourceDSN, schema, "uci_restore_source")
	target := openMigratedTestDBInSchema(t, binding.TargetDSN, schema, "uci_restore_target")
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKeyFile: binding.KeyPath})
	require.NoError(t, err)
	return &streamIntegrityFixture{store: NewProjectConsolidationStore(source, vault, target), source: source, target: target, ctx: context.Background(), directory: t.TempDir(), schema: schema}
}

func TestUCIBackupRestoresPublishedSealedCorpus(t *testing.T) {
	binding := uciBackupRestoreBindingFor(t)
	ctx := context.Background()

	t.Setenv("DATABASE_DSN", binding.SourceDSN)
	fixture := openUCIPublicationFixture(t)
	source := fixture.db
	var schema string
	require.NoError(t, source.Raw(`SELECT current_schema()`).Scan(&schema).Error)
	target := openMigratedTestDBInSchema(t, binding.TargetDSN, schema, "housekeeping_uci_restore_target")
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "restore-published", "func RestorePublished() {}\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("restore.go", artifact)}
	edges := []ucidomain.IndexEdgeReplacement{{SourcePath: "restore.go"}}
	draft := newUCIPublicationDraft([]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, edges)}, memberships, edges)
	_, view := fixture.publish(t, fixture.publisher, fixture.caller("restore-owner"), fixture.publishInput("restore-published", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, draft))
	require.NotEmpty(t, view.Context.ViewID)
	var publishedView UCIView
	require.NoError(t, source.Where("view_id=?", view.Context.ViewID).First(&publishedView).Error)
	evidenceFixture := &uciProjectionMigrationFixture{db: source, authRealm: fixture.realm, source: fixture.source, checkout: fixture.checkout, view: &publishedView}
	evidenceStore := NewUCIExposureStore(source)
	exposure, err := evidenceStore.AppendExposure(ctx, uciProjectionExposureRecord(t, evidenceFixture, time.Now().UTC().Truncate(time.Microsecond)))
	require.NoError(t, err)
	_, err = evidenceStore.AppendCompletion(uciCompletionFixtureContext(t, ctx, source, fixture.realm, fixture.checkout.OwnerPrincipal), uciProjectionCompletionEvidence(t, exposure.ExposureRef, time.Now().UTC().Truncate(time.Microsecond)))
	require.NoError(t, err)
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKeyFile: binding.KeyPath})
	require.NoError(t, err)
	plaintext := "synthetic housekeeping UCI restore credential"
	ciphertext, err := vault.Encrypt(plaintext)
	require.NoError(t, err)
	storedCredential := Credential{Project: sessionTestProject(t, source), Key: "restore-synthetic", Scope: "project", Version: 1, EncryptedSecret: ciphertext, EncryptionKeyFingerprint: vault.Fingerprint()}
	require.NoError(t, source.Create(&storedCredential).Error)
	credential, err := NewCredentialStore(&Store{DB: source}).GetByID(ctx, storedCredential.ID)
	require.NoError(t, err)
	decoded, err := vault.Decrypt(credential.EncryptedSecret)
	require.NoError(t, err)
	require.Equal(t, plaintext, decoded)

	history := uciMigrationRollbackTableRows(t, source, "migrations")
	require.NotEqual(t, "[]", history)
	var published, sealed int64
	require.NoError(t, source.Raw(`SELECT count(*) FROM ci_checkouts c JOIN ci_views v ON v.view_id=c.current_view_id AND v.checkout_id=c.checkout_id WHERE v.state='published'`).Scan(&published).Error)
	require.NoError(t, source.Raw(`SELECT count(*) FROM ci_parse_artifacts a WHERE a.sealed_at IS NOT NULL AND a.facts_digest IS NOT NULL AND EXISTS (SELECT 1 FROM ci_definitions d WHERE d.artifact_id=a.artifact_id) AND EXISTS (SELECT 1 FROM ci_chunks c WHERE c.artifact_id=a.artifact_id)`).Scan(&sealed).Error)
	require.Positive(t, published, "the source must contain a real published current View")
	require.Positive(t, sealed, "the source must contain sealed artifacts with nonempty facts")
	require.NoError(t, NewUCIExposureStore(source).VerifyIntegrity(ctx))
	t.Logf("published current views=%d; sealed artifacts with facts=%d", published, sealed)
	for _, db := range []*gormlib.DB{source, target} {
		require.NoError(t, db.Exec(`CREATE FUNCTION uci_restore_independent_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF current_setting('uci_restore.fail_pointer',true)='on' AND NEW.current_view_id IS NOT NULL THEN
				RAISE EXCEPTION 'synthetic deferred-pointer failure';
			END IF; RETURN NEW; END $$`).Error)
		require.NoError(t, db.Exec(`CREATE TRIGGER uci_restore_independent_guard AFTER UPDATE OF current_view_id ON ci_checkouts FOR EACH ROW EXECUTE FUNCTION uci_restore_independent_guard()`).Error)
	}

	rowsBefore, err := consolidationSnapshot(source)
	require.NoError(t, err)
	sequencesBefore, err := consolidationSequences(source)
	require.NoError(t, err)
	emptyTarget, err := consolidationSnapshot(target)
	require.NoError(t, err)
	guardModes := func(db *gormlib.DB) []string {
		var modes []string
		require.NoError(t, db.Raw(`SELECT c.relname||':'||t.tgname||':'||t.tgenabled::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal ORDER BY c.relname,t.tgname`).Scan(&modes).Error)
		require.NotEmpty(t, modes)
		return modes
	}
	guardsBefore := guardModes(target)
	store := NewProjectConsolidationStore(source, vault, target)
	census, err := store.Census(ctx, "synthetic-housekeeping-published-sealed-restore")
	require.NoError(t, err)
	plan, err := store.Preflight(ctx, ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint})
	require.NoError(t, err)
	var backup bytes.Buffer
	require.NoError(t, store.Backup(ctx, plan.Reference, plan.Fingerprint, &backup))
	require.Positive(t, backup.Len())
	targetBefore := streamIntegrityRead(t, target)
	tx := target.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, tx.Exec(`SET LOCAL uci_restore.fail_pointer='on'`).Error)
	_, err = NewProjectConsolidationStore(source, vault, tx).VerifyBackup(ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(backup.Bytes()))
	require.EqualError(t, err, "PROJECT_ADOPTION_RESTORE_CONSTRAINT_FAILED")
	require.NoError(t, tx.Rollback().Error)
	streamIntegrityUnchanged(t, targetBefore, target, "independent trigger after deferred seals and pointer must roll back all rows, frames, sequences and DDL modes")
	backupRef, restoreErr := store.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(backup.Bytes()))
	require.NoError(t, consolidationCompare(source, rowsBefore), "backup and verification must not rewrite source rows")
	require.Equal(t, guardsBefore, guardModes(target), "restore must retain independent trigger modes, including on rollback")
	if restoreErr != nil {
		require.NoError(t, consolidationCompare(target, emptyTarget), "failed restore must roll back every target row")
		for _, table := range []string{"project_consolidation_backups", "project_consolidation_frames"} {
			var count int64
			require.NoError(t, target.Table(table).Count(&count).Error)
			require.Zero(t, count, "failed restore must roll back recovery bookkeeping")
		}
		t.Log("failed restore retained exact empty-target rows and independent trigger modes; recovery frames rolled back")
	}
	require.NoError(t, restoreErr, "published/sealed current-schema backup must restore into an empty target")
	require.NotEmpty(t, backupRef)
	require.NoError(t, consolidationCompare(target, rowsBefore), "every non-bookkeeping table must restore exact rows")
	sequencesAfter, err := consolidationSequences(target)
	require.NoError(t, err)
	require.Equal(t, sequencesBefore, sequencesAfter)
	require.Equal(t, history, uciMigrationRollbackTableRows(t, target, "migrations"), "restore must retain complete migration history")
	require.NoError(t, NewUCIExposureStore(target).VerifyIntegrity(ctx))
	readback, err := NewCredentialStore(&Store{DB: target}).GetByID(ctx, credential.ID)
	require.NoError(t, err)
	require.Equal(t, credential, readback)
	decoded, err = vault.Decrypt(readback.EncryptedSecret)
	require.NoError(t, err)
	require.Equal(t, plaintext, decoded, "the fresh synthetic Vault must authenticate restored ciphertext")

	var backups, frames []string
	require.NoError(t, target.Raw(`SELECT backup_id FROM project_consolidation_backups ORDER BY backup_id`).Scan(&backups).Error)
	require.Equal(t, []string{backupRef}, backups)
	require.NoError(t, target.Raw(`SELECT encode(ciphertext,'hex') FROM project_consolidation_frames WHERE backup_id=? ORDER BY ordinal`, backupRef).Scan(&frames).Error)
	require.Greater(t, len(frames), 1)
	var sourceBackups int64
	require.NoError(t, source.Model(&consolidationBackupRow{}).Count(&sourceBackups).Error)
	require.Zero(t, sourceBackups)
	repeated, err := store.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(backup.Bytes()))
	require.NoError(t, err)
	require.Equal(t, backupRef, repeated)
	var replayFrames []string
	require.NoError(t, target.Raw(`SELECT encode(ciphertext,'hex') FROM project_consolidation_frames WHERE backup_id=? ORDER BY ordinal`, backupRef).Scan(&replayFrames).Error)
	require.Equal(t, frames, replayFrames, "authenticated replay must retain exact sealed frames")
	require.NoError(t, consolidationCompare(target, rowsBefore))
	require.Equal(t, guardsBefore, guardModes(target))
	sequencesReplay, err := consolidationSequences(target)
	require.NoError(t, err)
	require.Equal(t, sequencesBefore, sequencesReplay)
	t.Logf("exact rows/seals/pointers/history/Vault retained across restore and replay; %d sealed frames; independent deferred-pointer trigger rollback retained full target state", len(frames))
}
