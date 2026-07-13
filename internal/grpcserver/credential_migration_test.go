//go:build legacyupgrade

package grpcserver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	enggorm "gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	localgorm "github.com/thebtf/engram/internal/db/gorm"
)

// TestCredentialDecryptRoundTripAfterMigration is the F-1 HARD GATE for US3
// (PR-v5-3, observations split). It verifies that credentials migrated from
// the legacy `observations` table into the dedicated `credentials` table via
// migration 090 preserve the AES-256-GCM ciphertext byte-for-byte, and that
// the vault can still decrypt every migrated secret back to its original
// plaintext with 100% fidelity.
//
// Why this test exists:
//   - Migration 090 copies encrypted_secret from observations into credentials
//     via a SQL SELECT into a BYTEA column. If either end silently mutated the
//     bytes (wrong encoding, trimming, pad handling), AES-GCM decrypt would
//     fail with authentication-tag mismatch and the production vault would
//     become unusable. That would lose all 13 prod credentials irrecoverably.
//   - This test simulates the migration path using a real Postgres instance
//     and a real crypto.Vault, with diverse plaintexts designed to exercise
//     byte-preservation corner cases: empty, short ASCII, long ASCII, unicode
//     (mixed scripts + emoji), and binary-like data (NUL + high bytes).
//   - Failure of this test BLOCKS Commit G (drop observations migration).
//     Commit G must not land until F-1 is green in CI.
//
// Prerequisite: DATABASE_DSN must point at a disposable database restored from
// the pinned v4.5.0 fixture. The dedicated legacyupgrade gate fails rather than
// skipping when that contract is not met.
func TestCredentialDecryptRoundTripAfterMigration(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	require.NotEmpty(t, dsn, "legacyupgrade gate requires DATABASE_DSN restored from tests/fixtures/pre-v5/engram-v4.5.0.sql")

	db, err := enggorm.Open(postgres.Open(dsn), &enggorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open test db")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	defer sqlDB.Close()

	// This tagged gate starts from the checksum-pinned v4.5.0 fixture. Ordinary
	// suites never recreate this demolished table and contain no legacy skip.
	var obsExists bool
	if err := db.Raw(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='observations')`,
	).Scan(&obsExists).Error; err != nil {
		t.Fatalf("check observations table: %v", err)
	}
	require.True(t, obsExists, "legacy fixture must contain observations before candidate migration")
	var migrationCount int64
	require.NoError(t, db.Table("migrations").Count(&migrationCount).Error)
	require.Equal(t, int64(82), migrationCount, "fixture must be the exact v4.5.0 migration state")

	// Deterministic 32-byte test key so the test is reproducible and does not
	// depend on the Docker vault.key or the production fingerprint.
	// This is a TEST KEY, not a secret — it encrypts only synthetic test rows
	// scoped to a unique test project slug.
	const testHexKey = "aa110102030405060708090a0b0c0d0e1f20212223242526272829aabbccddee"
	cfg := &config.Config{EncryptionKey: testHexKey}
	vault, err := crypto.NewVault(cfg)
	require.NoError(t, err, "NewVault with test key")
	goodFP := vault.Fingerprint()
	require.NotEmpty(t, goodFP, "vault fingerprint must not be empty")

	// Unique project slug prevents collisions with production data and other
	// tests that may share the same DB.
	testProject := fmt.Sprintf("f1-decrypt-roundtrip-%d", time.Now().UnixNano())
	const badFP = "wrongfingerprint" // 16 hex chars, mismatches goodFP

	if os.Getenv("ENGRAM_PRE_V5_TEST_MODE") == "interrupted" {
		ciphertext, encErr := vault.Encrypt("interrupted-upgrade-secret")
		require.NoError(t, encErr)
		require.NoError(t, db.Exec(`
			INSERT INTO observations (project, sdk_session_id, type, title, encrypted_secret,
				encryption_key_fingerprint, created_at, created_at_epoch, is_suppressed, is_archived, is_superseded)
			VALUES (?, 'interrupted', 'credential', 'timestamp-overflow', ?, ?, ?, 9223372036854775807, false, 0, 0)`,
			testProject, ciphertext, goodFP, time.Now().UTC().Format(time.RFC3339Nano)).Error)
		failedStore, migrateErr := localgorm.NewStore(localgorm.Config{DSN: dsn, LogLevel: logger.Silent})
		if failedStore != nil {
			_ = failedStore.Close()
		}
		require.Error(t, migrateErr, "an invalid legacy timestamp must fail the upgrade")
		require.True(t, db.Migrator().HasTable("observations"), "failed migration 090 must preserve the legacy source table")
		var applied090 int64
		require.NoError(t, db.Table("migrations").Where("id = ?", "090_observations_to_static_entities").Count(&applied090).Error)
		require.Zero(t, applied090, "failed migration 090 must not advance its ledger entry")
		var copied int64
		require.NoError(t, db.Table("credentials").Where("project = ?", testProject).Count(&copied).Error)
		require.Zero(t, copied, "failed migration 090 must roll back partial credential copies")
		return
	}

	// --- Diverse plaintexts to exercise byte-preservation corner cases. ---
	// Each entry produces one observation row with type='credential'.
	// After migration 090 runs, each must round-trip via vault.Decrypt to the
	// exact original plaintext.
	cases := []struct {
		name      string
		key       string // observation.title; becomes credentials.key
		plaintext string
	}{
		{
			name:      "short_ascii",
			key:       "api-key-smoke",
			plaintext: "sk-proj-abc123XYZ",
		},
		{
			name:      "empty_plaintext",
			key:       "empty-secret",
			plaintext: "",
		},
		{
			name: "unicode_mixed",
			key:  "unicode-secret",
			// Cyrillic + CJK + emoji — each is multi-byte UTF-8. The migration
			// touches encrypted_secret only (raw bytes), but the plaintext
			// variety guards against any future regression that would read
			// the secret as text instead of bytes.
			plaintext: "Тайна-秘密-🔐-key-42",
		},
		{
			name:      "long_plaintext_1kb",
			key:       "long-secret",
			plaintext: strings.Repeat("x1B", 350), // ~1050 bytes
		},
		{
			name: "binary_like_with_nul_and_high_bytes",
			key:  "binary-secret",
			// Embedded NUL and high-bit bytes — encoded into the plaintext
			// string. vault.Encrypt operates on []byte(plaintext), so these
			// bytes go through AES-GCM and must survive the BYTEA round-trip.
			plaintext: string([]byte{0x00, 0xFF, 0x7E, 0x01, 0xFE, 0x80, 0x00, 0xAB, 0xCD, 0xEF}),
		},
	}

	ctx := context.Background()
	ciphertexts := make([][]byte, len(cases))

	// --- Phase 1: encrypt + seed observations with ciphertext + goodFP ---
	//   These rows represent pre-migration state. encrypted_secret is stored
	//   in the observations BYTEA column; the migration copies it verbatim
	//   into credentials.encrypted_secret.
	for i, tc := range cases {
		ct, encErr := vault.Encrypt(tc.plaintext)
		require.NoError(t, encErr, "Encrypt case %q", tc.name)
		require.NotEmpty(t, ct, "ciphertext must not be empty for case %q", tc.name)
		ciphertexts[i] = ct

		// Insert an observation row with the minimum columns migration 090
		// reads, plus the NOT NULL columns required by the observations schema
		// (project, sdk_session_id, type, created_at, created_at_epoch).
		insertSQL := `
			INSERT INTO observations (
				project, sdk_session_id, type, title,
				encrypted_secret, encryption_key_fingerprint,
				created_at, created_at_epoch,
				is_suppressed, is_archived, is_superseded
			) VALUES (
				?, ?, 'credential', ?,
				?, ?,
				?, ?,
				false, 0, 0
			)
		`
		now := time.Now().UTC()
		require.NoError(t,
			db.Exec(insertSQL,
				testProject,
				"f1-test-session-"+tc.name,
				tc.key,
				ct,
				goodFP,
				now.Format(time.RFC3339Nano),
				now.UnixMilli(),
			).Error,
			"insert observation for case %q", tc.name,
		)
	}

	// Adversarial EXCLUDED rows — migration 090 must NOT migrate these.
	// If the WHERE clause (is_suppressed/is_archived/is_superseded filters)
	// were accidentally loosened, these rows would appear in credentials and
	// the assertions in Phase 5 (adversarial_excluded_rows) would fire.
	excludedCases := []struct {
		keyName  string
		column   string // column to flip non-default
		boolFlag bool   // true for is_suppressed (bool), false for is_archived / is_superseded (int)
	}{
		{"excluded-suppressed", "is_suppressed", true},
		{"excluded-archived", "is_archived", false},
		{"excluded-superseded", "is_superseded", false},
	}
	for _, ec := range excludedCases {
		// Build the INSERT with one non-default lifecycle flag. Using string
		// concatenation for the column name is safe because it is a hardcoded
		// literal from this closed set; the value is still parameterized.
		var sqlStmt string
		if ec.boolFlag {
			// is_suppressed is bool — seed with true, others default.
			sqlStmt = `
				INSERT INTO observations (
					project, sdk_session_id, type, title,
					encrypted_secret, encryption_key_fingerprint,
					created_at, created_at_epoch,
					is_suppressed, is_archived, is_superseded
				) VALUES (
					?, ?, 'credential', ?,
					?, ?,
					?, ?,
					true, 0, 0
				)`
		} else if ec.column == "is_archived" {
			sqlStmt = `
				INSERT INTO observations (
					project, sdk_session_id, type, title,
					encrypted_secret, encryption_key_fingerprint,
					created_at, created_at_epoch,
					is_suppressed, is_archived, is_superseded
				) VALUES (
					?, ?, 'credential', ?,
					?, ?,
					?, ?,
					false, 1, 0
				)`
		} else { // is_superseded
			sqlStmt = `
				INSERT INTO observations (
					project, sdk_session_id, type, title,
					encrypted_secret, encryption_key_fingerprint,
					created_at, created_at_epoch,
					is_suppressed, is_archived, is_superseded
				) VALUES (
					?, ?, 'credential', ?,
					?, ?,
					?, ?,
					false, 0, 1
				)`
		}
		ct, encErr := vault.Encrypt("should-not-migrate-" + ec.keyName)
		require.NoError(t, encErr)
		require.NoError(t,
			db.Exec(sqlStmt,
				testProject,
				"f1-test-session-"+ec.keyName,
				ec.keyName,
				ct,
				goodFP,
				time.Now().UTC().Format(time.RFC3339Nano),
				time.Now().UnixMilli(),
			).Error,
			"insert excluded observation %q", ec.keyName,
		)
	}

	// Additional "orphaned" observation encrypted with badFP — must NOT be
	// decryptable with goodFP and must be flagged by CountWithDifferentFingerprint.
	// We use a throw-away ciphertext value; the plaintext is unused because
	// this row exists only to exercise the fingerprint-mismatch path.
	require.NoError(t,
		db.Exec(`
			INSERT INTO observations (
				project, sdk_session_id, type, title,
				encrypted_secret, encryption_key_fingerprint,
				created_at, created_at_epoch,
				is_suppressed, is_archived, is_superseded
			) VALUES (
				?, ?, 'credential', 'orphan-secret',
				?, ?,
				?, ?,
				false, 0, 0
			)
		`,
			testProject,
			"f1-test-session-orphan",
			[]byte("orphaned-ciphertext-not-decryptable-with-good-key-placeholder"),
			badFP,
			time.Now().UTC().Format(time.RFC3339Nano),
			time.Now().UnixMilli(),
		).Error,
		"insert orphan observation with bad fingerprint",
	)

	for _, legacy := range []struct {
		title     string
		narrative string
		concepts  string
	}{
		{title: "legacy-memory", narrative: "memory preserved across pre-v5 upgrade", concepts: `["upgrade"]`},
		{title: "legacy-rule", narrative: "rule preserved across pre-v5 upgrade", concepts: `["always-inject"]`},
	} {
		require.NoError(t, db.Exec(`
			INSERT INTO observations (project, sdk_session_id, type, title, narrative, concepts,
				created_at, created_at_epoch, is_suppressed, is_archived, is_superseded)
			VALUES (?, ?, 'guidance', ?, ?, ?::jsonb, ?, ?, false, 0, 0)`,
			testProject, "legacy-"+legacy.title, legacy.title, legacy.narrative, legacy.concepts,
			time.Now().UTC().Format(time.RFC3339Nano), time.Now().UnixMilli()).Error)
	}

	// --- Phase 2: run the real current migration chain over the historical fixture. ---
	gormStore, err := localgorm.NewStore(localgorm.Config{DSN: dsn, LogLevel: logger.Silent})
	require.NoError(t, err, "upgrade v4.5.0 fixture with current migrations")
	defer gormStore.Close()
	require.False(t, db.Migrator().HasTable("observations"), "migration 099 must drop observations only after migration 090 commits")

	var migratedMemories int64
	require.NoError(t, db.Table("memories").Where("project = ?", testProject).Count(&migratedMemories).Error)
	require.Equal(t, int64(1), migratedMemories, "the ordinary legacy row must survive as a memory")
	var migratedRules int64
	require.NoError(t, db.Table("behavioral_rules").Where("project = ?", testProject).Count(&migratedRules).Error)
	require.Equal(t, int64(1), migratedRules, "the always-inject legacy row must also survive as a behavioral rule")

	// --- Phase 3: read via CredentialStore and decrypt via vault. ---
	store := &localgorm.Store{DB: db}
	credStore := localgorm.NewCredentialStore(store)

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := credStore.Get(ctx, testProject, tc.key)
			require.NoError(t, err, "CredentialStore.Get for %q", tc.key)
			require.NotNil(t, got)

			// Byte-for-byte ciphertext preservation check.
			// This is the invariant the migration must preserve.
			assert.Equal(t, ciphertexts[i], got.EncryptedSecret,
				"encrypted_secret must be preserved byte-for-byte through observation→credentials migration (case %q)",
				tc.name,
			)
			assert.Equal(t, goodFP, got.EncryptionKeyFingerprint,
				"encryption_key_fingerprint must be preserved exactly (case %q)", tc.name,
			)

			// The actual round-trip: decrypt via the same vault that encrypted.
			decrypted, err := vault.Decrypt(got.EncryptedSecret)
			require.NoError(t, err, "vault.Decrypt post-migration for %q", tc.name)
			assert.Equal(t, tc.plaintext, decrypted,
				"decrypted plaintext must equal original pre-migration plaintext (case %q)",
				tc.name,
			)
		})
	}

	// --- Phase 4: fingerprint mismatch + orphan visibility checks. ---
	t.Run("orphan_credential_visible_via_fingerprint_check", func(t *testing.T) {
		// List returns only this project's rows.
		list, err := credStore.List(ctx, testProject)
		require.NoError(t, err)
		require.Len(t, list, len(cases)+1,
			"migrated rows must include all %d good-fp cases + 1 orphan", len(cases),
		)

		// Count per-project how many rows have a fingerprint != goodFP. We do
		// this manually because CountWithDifferentFingerprint is global; the
		// spec invariant "mismatch_count: 0" is verified against the
		// production fingerprint in a staging dry-run — here we prove the
		// primitive works at the store level.
		var mismatch int
		for _, c := range list {
			if c.EncryptionKeyFingerprint != goodFP {
				mismatch++
			}
		}
		assert.Equal(t, 1, mismatch, "exactly one orphan with badFP must be visible")

		// CountWithDifferentFingerprint(goodFP) counts ALL rows globally with
		// a different fingerprint. Since we inserted exactly one orphan in
		// this test, the count must be >= 1. It may be higher if prior data
		// exists, which is fine — the contract is "non-zero ⇒ something is
		// orphaned". We verify the direction, not the absolute count.
		globalMismatch, err := credStore.CountWithDifferentFingerprint(ctx, goodFP)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, globalMismatch, int64(1),
			"global mismatch count must include our orphan",
		)
	})

	t.Run("adversarial_excluded_rows_not_migrated", func(t *testing.T) {
		// Migration 090 filters by is_suppressed=false AND is_archived=0 AND
		// is_superseded=0. Rows violating any of these predicates MUST NOT
		// appear in credentials after the migration. This sub-test proves the
		// WHERE clause is load-bearing: if any filter were accidentally
		// dropped or loosened, one of these three Get calls would succeed
		// instead of returning ErrRecordNotFound.
		for _, keyName := range []string{
			"excluded-suppressed",
			"excluded-archived",
			"excluded-superseded",
		} {
			_, err := credStore.Get(ctx, testProject, keyName)
			require.Error(t, err,
				"excluded row %q must NOT have been migrated into credentials",
				keyName,
			)
		}
	})

	t.Run("tampered_ciphertext_fails_decrypt", func(t *testing.T) {
		// Prove the decrypt gate is real: flip one byte of the ciphertext
		// and confirm AES-GCM authentication catches it. This guards against
		// the scenario "decrypt accepts anything" — which would make the
		// round-trip gate a stub.
		got, err := credStore.Get(ctx, testProject, cases[0].key)
		require.NoError(t, err)
		tampered := make([]byte, len(got.EncryptedSecret))
		copy(tampered, got.EncryptedSecret)
		// Flip a byte in the GCM ciphertext body (after the 12-byte nonce).
		tamperIdx := 15
		if tamperIdx >= len(tampered) {
			tamperIdx = len(tampered) - 1
		}
		tampered[tamperIdx] ^= 0xFF
		_, err = vault.Decrypt(tampered)
		require.Error(t, err, "decrypt of tampered ciphertext must fail (GCM auth)")
	})

	t.Run("wrong_key_fails_decrypt", func(t *testing.T) {
		wrongVault, err := crypto.NewVault(&config.Config{EncryptionKey: "bb110102030405060708090a0b0c0d0e1f20212223242526272829aabbccddee"})
		require.NoError(t, err)
		got, err := credStore.Get(ctx, testProject, cases[0].key)
		require.NoError(t, err)
		_, err = wrongVault.Decrypt(got.EncryptedSecret)
		require.Error(t, err, "a different vault key must not decrypt migrated ciphertext")
	})

	restarted, err := localgorm.NewStore(localgorm.Config{DSN: dsn, LogLevel: logger.Silent})
	require.NoError(t, err, "current schema must reopen cleanly after the upgrade")
	restartedCredential, err := localgorm.NewCredentialStore(restarted).Get(ctx, testProject, cases[0].key)
	require.NoError(t, err, "current credential read must survive restart")
	require.Equal(t, ciphertexts[0], restartedCredential.EncryptedSecret)
	require.NoError(t, restarted.Close())
}
