package worker

import (
	"context"
	"fmt"
	"testing"

	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/pkg/models"
)

// testHexKey is a deterministic 32-byte AES key (hex) for vault-backed decrypt tests.
const testHexKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// newTestVault builds a real crypto.Vault from a fixed key so encrypt/decrypt round-trips
// are exercised end-to-end (no mock crypto).
func newTestVault(t *testing.T) *crypto.Vault {
	t.Helper()
	v, err := crypto.NewVault(&config.Config{EncryptionKey: testHexKey})
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	return v
}

// fakeSettingsReader is an in-memory settingsReader for adapter unit tests (no DB).
// A key mapped to a nil row + non-nil err simulates a store error; a missing key
// returns gorm.ErrRecordNotFound to mirror SettingsStore.Get's not-found behavior.
type fakeSettingsReader struct {
	rows map[string]*models.ModelSetting
	err  error // when non-nil, every Get returns this error
}

func (f fakeSettingsReader) Get(_ context.Context, key string) (*models.ModelSetting, error) {
	if f.err != nil {
		return nil, f.err
	}
	row, ok := f.rows[key]
	if !ok {
		return nil, fmt.Errorf("get setting %q: %w", key, gorm.ErrRecordNotFound)
	}
	return row, nil
}

// TestSettingsResolver_Get pins the CR-2 (#259) worker-adapter contract: a plain row
// resolves to (value,true); an encrypted row, a missing key, and a store error all
// resolve to ("",false) so the client falls through to env/default.
func TestSettingsResolver_Get(t *testing.T) {
	ctx := context.Background()

	t.Run("plain row resolves to value", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
			"reranker.url": {Key: "reranker.url", Value: "https://store.example.test", Encrypted: false},
		}}}
		got, ok := r.Get(ctx, "reranker.url")
		if !ok || got != "https://store.example.test" {
			t.Fatalf("Get = (%q, %v), want (%q, true)", got, ok, "https://store.example.test")
		}
	})

	t.Run("encrypted row with no vault wired reports absent", func(t *testing.T) {
		// vaultProvider nil → cannot decrypt → fall through to env/default.
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
			"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: []byte{0x01, 0x02}},
		}}}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(encrypted, no vault) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("missing key reports absent", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{}}}
		got, ok := r.Get(ctx, "absent.key")
		if ok || got != "" {
			t.Fatalf("Get(missing) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("store error reports absent (never breaks client init)", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{err: fmt.Errorf("connection refused")}}
		got, ok := r.Get(ctx, "reranker.url")
		if ok || got != "" {
			t.Fatalf("Get(error) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("nil reader reports absent", func(t *testing.T) {
		r := newSettingsResolver(nil, nil)
		got, ok := r.Get(ctx, "reranker.url")
		if ok || got != "" {
			t.Fatalf("Get(nil reader) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("nil row with nil error does not panic (gemini #304)", func(t *testing.T) {
		// A settingsReader returning (nil, nil) — possible with a custom or future
		// implementation — must resolve to absent, never panic on row.Encrypted.
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
			"reranker.url": nil, // present key, nil row, nil error
		}}}
		got, ok := r.Get(ctx, "reranker.url")
		if ok || got != "" {
			t.Fatalf("Get(nil row) = (%q, %v), want (\"\", false)", got, ok)
		}
	})
}

// TestSettingsResolver_DecryptPath pins the CR-3 (#259) secret read path: an encrypted row
// is decrypted in-process when a matching vault is wired, and EVERY decrypt failure mode
// (fingerprint mismatch, corrupt ciphertext, vault-provider error) is fail-SOFT — it resolves
// to absent so the reranker/embedder falls through to env/default and init never breaks.
func TestSettingsResolver_DecryptPath(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	// Encrypt a known secret with the test vault to seed the rows.
	ciphertext, err := v.Encrypt("sk-secret-123")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	t.Run("encrypted row decrypts to plaintext when vault matches", func(t *testing.T) {
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: ciphertext, EncryptionKeyFingerprint: v.Fingerprint()},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return v, nil },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if !ok || got != "sk-secret-123" {
			t.Fatalf("Get(encrypted, vault) = (%q, %v), want (%q, true)", got, ok, "sk-secret-123")
		}
	})

	t.Run("fingerprint mismatch is fail-soft (absent)", func(t *testing.T) {
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: ciphertext, EncryptionKeyFingerprint: "deadbeefdeadbeef"},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return v, nil },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(fingerprint mismatch) = (%q, %v), want (\"\", false) — must not break init", got, ok)
		}
	})

	t.Run("corrupt ciphertext is fail-soft (absent)", func(t *testing.T) {
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: []byte{0x00, 0x01, 0x02}, EncryptionKeyFingerprint: v.Fingerprint()},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return v, nil },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(corrupt ciphertext) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("vault-provider error is fail-soft (absent)", func(t *testing.T) {
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: ciphertext, EncryptionKeyFingerprint: v.Fingerprint()},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return nil, fmt.Errorf("vault init failed") },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(vault error) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("empty stored fingerprint still decrypts (back-compat)", func(t *testing.T) {
		// A row with no fingerprint stamped skips the match check and attempts decrypt directly.
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: ciphertext},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return v, nil },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if !ok || got != "sk-secret-123" {
			t.Fatalf("Get(no fingerprint) = (%q, %v), want (%q, true)", got, ok, "sk-secret-123")
		}
	})
}
