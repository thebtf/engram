package gorm

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
)

func TestCredentialDecryptRoundTripAfterMigration(t *testing.T) {
	db := openDockerMigrationDBTo(t, "089_behavioral_rules")
	ctx := db.Statement.Context
	var before int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM migrations WHERE id = ?`, "090_observations_to_static_entities").Scan(&before).Error)
	require.Zero(t, before, "fixture must precede migration 090")
	vault, err := crypto.NewVault(&config.Config{EncryptionKey: "aa110102030405060708090a0b0c0d0e1f20212223242526272829aabbccddee"})
	require.NoError(t, err)
	fingerprint := vault.Fingerprint()
	require.NotEmpty(t, fingerprint)

	cases := []struct {
		name, plaintext string
	}{
		{"short_ascii", "sk-proj-abc123XYZ"},
		{"empty_plaintext", ""},
		{"unicode_mixed", "Тайна-秘密-🔐-key-42"},
		{"long_plaintext_1kb", strings.Repeat("x1B", 350)},
		{"binary_like", string([]byte{0x00, 0xFF, 0x7E, 0x01, 0xFE, 0x80, 0x00, 0xAB, 0xCD, 0xEF})},
	}
	const project = "migration-090-credential-fixture"
	ciphertexts := make(map[string][]byte, len(cases))
	insert := func(name string, ciphertext []byte, fp string, suppressed bool, archived int, superseded bool) {
		t.Helper()
		now := time.Now().UTC()
		require.NoError(t, db.Exec(`INSERT INTO observations
			(project, sdk_session_id, type, title, encrypted_secret, encryption_key_fingerprint,
			 created_at, created_at_epoch, is_suppressed, is_archived, is_superseded)
			VALUES (?, ?, 'credential', ?, ?, ?, ?, ?, ?, ?, ?)`,
			project, "fixture-"+name, name, ciphertext, fp,
			now.Format(time.RFC3339Nano), now.UnixMilli(), suppressed, archived, superseded).Error)
	}
	for _, tc := range cases {
		ciphertext, encryptErr := vault.Encrypt(tc.plaintext)
		require.NoError(t, encryptErr)
		require.NotEmpty(t, ciphertext)
		ciphertexts[tc.name] = ciphertext
		insert(tc.name, ciphertext, fingerprint, false, 0, false)
	}
	for _, excluded := range []struct {
		name       string
		suppressed bool
		archived   int
		superseded bool
	}{
		{"excluded-suppressed", true, 0, false},
		{"excluded-archived", false, 1, false},
		{"excluded-superseded", false, 0, true},
	} {
		ciphertext, encryptErr := vault.Encrypt("excluded")
		require.NoError(t, encryptErr)
		insert(excluded.name, ciphertext, fingerprint, excluded.suppressed, excluded.archived, excluded.superseded)
	}
	insert("orphan-secret", []byte("synthetic-orphan-ciphertext"), "wrongfingerprint", false, 0, false)

	require.NoError(t, runMigrationsTo(db, "090_observations_to_static_entities"), "execute the actual 090 migration")
	var applied int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM migrations WHERE id = ?`, "090_observations_to_static_entities").Scan(&applied).Error)
	require.Equal(t, int64(1), applied, "the real 090 migration must be recorded")
	store := NewCredentialStore(&Store{DB: db})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, getErr := store.Get(ctx, project, tc.name)
			require.NoError(t, getErr)
			require.NotNil(t, got)
			assert.Equal(t, ciphertexts[tc.name], got.EncryptedSecret)
			assert.Equal(t, fingerprint, got.EncryptionKeyFingerprint)
			plaintext, decryptErr := vault.Decrypt(got.EncryptedSecret)
			require.NoError(t, decryptErr)
			assert.Equal(t, tc.plaintext, plaintext)
		})
	}
	rows, err := store.List(ctx, project)
	require.NoError(t, err)
	require.Len(t, rows, len(cases)+1)
	mismatched, err := store.CountWithDifferentFingerprint(ctx, fingerprint)
	require.NoError(t, err)
	assert.Equal(t, int64(1), mismatched)
	for _, key := range []string{"excluded-suppressed", "excluded-archived", "excluded-superseded"} {
		_, getErr := store.Get(ctx, project, key)
		require.Error(t, getErr, "%s must not migrate", key)
	}
	got, err := store.Get(ctx, project, cases[0].name)
	require.NoError(t, err)
	tampered := append([]byte(nil), got.EncryptedSecret...)
	require.Greater(t, len(tampered), 15, fmt.Sprintf("ciphertext for %s must include authenticated body", cases[0].name))
	tampered[15] ^= 0xff
	_, err = vault.Decrypt(tampered)
	require.Error(t, err, "tampering must fail authentication")
}
