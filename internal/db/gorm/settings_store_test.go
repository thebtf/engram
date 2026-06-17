package gorm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// uniqueSettingKey returns a test-only settings key that cannot collide with real
// model_settings rows on a shared/dev database. Tests clean up by these exact keys.
func uniqueSettingKey(t *testing.T, base string) string {
	t.Helper()
	return fmt.Sprintf("zz-test.%s.%d", base, time.Now().UnixNano())
}

// TestSettingsStore_SetGetListDelete exercises the full lifecycle of the settings store
// against a real PostgreSQL (migration 143 model_settings table). DATABASE_DSN-gated.
func TestSettingsStore_SetGetListDelete(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()
	store := NewSettingsStore(&Store{DB: db})

	plainKey := uniqueSettingKey(t, "reranker.url")
	secretKey := uniqueSettingKey(t, "reranker.api_key")
	defer db.Exec(`DELETE FROM model_settings WHERE key IN (?, ?)`, plainKey, secretKey)

	// --- Set plain (non-secret) config ---
	created, err := store.Set(ctx, &models.ModelSetting{
		Key:         plainKey,
		Value:       "http://litellm.lan:4000/rerank",
		Description: "reranker base url",
		EditedBy:    "settings-test",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), int64(created.Version), "first write is version 1")
	require.False(t, created.Encrypted)
	require.Equal(t, "http://litellm.lan:4000/rerank", created.Value)
	require.Greater(t, created.ID, int64(0))

	// --- Get it back ---
	got, err := store.Get(ctx, plainKey)
	require.NoError(t, err)
	require.Equal(t, created.Value, got.Value)
	require.False(t, got.Encrypted)

	// --- Set is an idempotent upsert: same key updates in place, bumps version ---
	updated, err := store.Set(ctx, &models.ModelSetting{
		Key:      plainKey,
		Value:    "http://litellm.lan:4000/v2/rerank",
		EditedBy: "settings-test",
	})
	require.NoError(t, err)
	require.Equal(t, created.ID, updated.ID, "upsert must reuse the same row")
	require.Equal(t, 2, updated.Version, "second write bumps version to 2")
	require.Equal(t, "http://litellm.lan:4000/v2/rerank", updated.Value)

	// --- Set a secret (encrypted) value ---
	secret, err := store.Set(ctx, &models.ModelSetting{
		Key:                      secretKey,
		Encrypted:                true,
		EncryptedValue:           []byte{0x01, 0x02, 0x03, 0x04},
		EncryptionKeyFingerprint: "fp-test-abc",
		Description:              "reranker api key (encrypted)",
	})
	require.NoError(t, err)
	require.True(t, secret.Encrypted)
	require.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, secret.EncryptedValue)
	require.Empty(t, secret.Value)

	// --- List returns both, ordered by key ---
	all, err := store.List(ctx)
	require.NoError(t, err)
	var sawPlain, sawSecret bool
	for _, s := range all {
		if s.Key == plainKey {
			sawPlain = true
		}
		if s.Key == secretKey {
			sawSecret = true
			require.True(t, s.Encrypted)
			require.NotEmpty(t, s.EncryptedValue)
		}
	}
	require.True(t, sawPlain && sawSecret, "List must return both test settings")

	// --- Delete soft-deletes; Get then misses; re-create works (partial unique index) ---
	require.NoError(t, store.Delete(ctx, plainKey))
	_, err = store.Get(ctx, plainKey)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound), "deleted setting must not be found")

	recreated, err := store.Set(ctx, &models.ModelSetting{Key: plainKey, Value: "v3", EditedBy: "settings-test"})
	require.NoError(t, err, "re-creating a soft-deleted key must succeed (partial unique index)")
	require.Equal(t, int64(1), int64(recreated.Version), "re-created key starts fresh at version 1")
}

// TestSettingsStore_Validation pins the input guards.
func TestSettingsStore_Validation(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	store := NewSettingsStore(&Store{DB: db})

	_, err := store.Set(ctx, nil)
	require.Error(t, err)
	_, err = store.Set(ctx, &models.ModelSetting{Key: ""})
	require.Error(t, err, "empty key must be rejected")

	// Encrypted=true but no ciphertext.
	_, err = store.Set(ctx, &models.ModelSetting{Key: uniqueSettingKey(t, "bad1"), Encrypted: true})
	require.Error(t, err, "Encrypted=true requires EncryptedValue")

	// EncryptedValue present but Encrypted=false (ambiguous).
	_, err = store.Set(ctx, &models.ModelSetting{Key: uniqueSettingKey(t, "bad2"), EncryptedValue: []byte{0x01}})
	require.Error(t, err, "EncryptedValue without Encrypted=true must be rejected")

	// Encrypted=true AND plaintext Value set violates exactly-one-payload (PR #303 review).
	_, err = store.Set(ctx, &models.ModelSetting{
		Key: uniqueSettingKey(t, "bad3"), Encrypted: true,
		EncryptedValue: []byte{0x01}, EncryptionKeyFingerprint: "fp", Value: "leak",
	})
	require.Error(t, err, "Value must be empty when Encrypted=true")
}

// TestSettingsStore_DeleteClearsPayload confirms soft-delete wipes the payload columns
// (a deleted secret must not leave ciphertext resident) — PR #303 review.
func TestSettingsStore_DeleteClearsPayload(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	store := NewSettingsStore(&Store{DB: db})

	key := uniqueSettingKey(t, "secret.to.delete")
	defer db.Exec(`DELETE FROM model_settings WHERE key = ?`, key)

	_, err := store.Set(ctx, &models.ModelSetting{
		Key: key, Encrypted: true, EncryptedValue: []byte{0xDE, 0xAD, 0xBE, 0xEF}, EncryptionKeyFingerprint: "fp",
	})
	require.NoError(t, err)
	require.NoError(t, store.Delete(ctx, key))

	// Read the soft-deleted row directly (bypassing the deleted_at filter) and confirm
	// the ciphertext is gone.
	var row ModelSetting
	require.NoError(t, db.WithContext(ctx).Where("key = ?", key).First(&row).Error)
	require.NotNil(t, row.DeletedAt, "row must be soft-deleted")
	require.Empty(t, row.EncryptedValue, "soft-deleted secret must not retain ciphertext")
	require.Empty(t, row.Value, "soft-deleted setting must not retain value")
}
