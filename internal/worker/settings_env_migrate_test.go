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

// recordingSettingsWriter is an in-memory settingsWriter for CR-4 migration tests (no DB).
// It records every Set so a test can assert exactly what was (and was not) written.
type recordingSettingsWriter struct {
	rows     map[string]*models.ModelSetting
	getErr   error                  // when set, Get returns this for ANY key (store-fault simulation)
	setCalls []*models.ModelSetting // every Set payload, in order
}

func newRecordingWriter() *recordingSettingsWriter {
	return &recordingSettingsWriter{rows: map[string]*models.ModelSetting{}}
}

func (w *recordingSettingsWriter) Get(_ context.Context, key string) (*models.ModelSetting, error) {
	if w.getErr != nil {
		return nil, w.getErr
	}
	row, ok := w.rows[key]
	if !ok {
		return nil, fmt.Errorf("get setting %q: %w", key, gorm.ErrRecordNotFound)
	}
	return row, nil
}

func (w *recordingSettingsWriter) Set(_ context.Context, in *models.ModelSetting) (*models.ModelSetting, error) {
	w.setCalls = append(w.setCalls, in)
	if w.rows == nil {
		w.rows = map[string]*models.ModelSetting{}
	}
	w.rows[in.Key] = in
	return in, nil
}

func (w *recordingSettingsWriter) setKeysWritten() map[string]*models.ModelSetting {
	out := map[string]*models.ModelSetting{}
	for _, c := range w.setCalls {
		out[c.Key] = c
	}
	return out
}

// TestMigrateEnvToSettings_WritesAbsentSkipsPresent pins the CR-4 (#259) idempotent contract:
// an env var that is set AND absent in the store is written; an env var whose key already exists
// is left untouched; an unset env var is a no-op.
func TestMigrateEnvToSettings_WritesAbsentSkipsPresent(t *testing.T) {
	ctx := context.Background()

	// reranker.url already in the store (operator-set) → must NOT be clobbered.
	w := newRecordingWriter()
	w.rows["reranker.url"] = &models.ModelSetting{Key: "reranker.url", Value: "https://operator-set.example"}

	t.Setenv("ENGRAM_RERANK_URL", "https://from-env.example")    // present in store → skip
	t.Setenv("ENGRAM_RERANK_MODEL", "env-model")                 // absent → write
	t.Setenv("ENGRAM_EMBEDDING_URL", "https://embed-env.example") // absent → write
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")                       // unset → no-op
	t.Setenv("ENGRAM_RERANK_API_KEY", "")                        // unset → no-op
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")                     // unset → no-op

	migrateEnvToSettings(ctx, w, nil) // nil vault: no secret env set, so never needed

	written := w.setKeysWritten()
	if _, ok := written["reranker.url"]; ok {
		t.Error("reranker.url was overwritten — must skip a key already present in the store")
	}
	if got := written["reranker.model"]; got == nil || got.Value != "env-model" {
		t.Errorf("reranker.model = %+v, want written with value env-model", got)
	}
	if got := written["embedder.url"]; got == nil || got.Value != "https://embed-env.example" {
		t.Errorf("embedder.url = %+v, want written from env", got)
	}
	if _, ok := written["embedder.model"]; ok {
		t.Error("embedder.model written despite unset env var")
	}
	if len(w.setCalls) != 2 {
		t.Errorf("got %d Set calls, want 2 (reranker.model + embedder.url)", len(w.setCalls))
	}
}

// TestMigrateEnvToSettings_EncryptsSecret confirms a secret env var (.api_key) is vault-encrypted
// on migration: the written row carries ciphertext + fingerprint, never the plaintext.
func TestMigrateEnvToSettings_EncryptsSecret(t *testing.T) {
	ctx := context.Background()
	v, err := crypto.NewVault(&config.Config{EncryptionKey: testHexKey})
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	w := newRecordingWriter()
	t.Setenv("ENGRAM_RERANK_API_KEY", "sk-secret-xyz")
	// Clear the others so only the secret migrates.
	for _, e := range []string{"ENGRAM_RERANK_URL", "ENGRAM_RERANK_MODEL", "ENGRAM_EMBEDDING_URL", "ENGRAM_EMBEDDING_MODEL", "ENGRAM_EMBEDDING_API_KEY"} {
		t.Setenv(e, "")
	}

	migrateEnvToSettings(ctx, w, func() (*crypto.Vault, error) { return v, nil })

	got := w.setKeysWritten()["reranker.api_key"]
	if got == nil {
		t.Fatal("reranker.api_key not written")
	}
	if !got.Encrypted {
		t.Error("secret row not marked Encrypted")
	}
	if got.Value != "" {
		t.Errorf("secret row carries plaintext Value %q, want empty", got.Value)
	}
	if len(got.EncryptedValue) == 0 {
		t.Error("secret row has no ciphertext")
	}
	if got.EncryptionKeyFingerprint != v.Fingerprint() {
		t.Errorf("fingerprint = %q, want %q", got.EncryptionKeyFingerprint, v.Fingerprint())
	}
	// And the ciphertext must round-trip back to the original secret.
	if pt, dErr := v.Decrypt(got.EncryptedValue); dErr != nil || pt != "sk-secret-xyz" {
		t.Errorf("decrypt = (%q, %v), want (sk-secret-xyz, nil)", pt, dErr)
	}
}

// TestMigrateEnvToSettings_SecretSkippedWhenNoVault confirms a secret env var is fail-soft when
// no vault is wired: it is skipped (env still works at runtime), not written in plaintext.
func TestMigrateEnvToSettings_SecretSkippedWhenNoVault(t *testing.T) {
	ctx := context.Background()
	w := newRecordingWriter()
	t.Setenv("ENGRAM_RERANK_API_KEY", "sk-secret-xyz")
	for _, e := range []string{"ENGRAM_RERANK_URL", "ENGRAM_RERANK_MODEL", "ENGRAM_EMBEDDING_URL", "ENGRAM_EMBEDDING_MODEL", "ENGRAM_EMBEDDING_API_KEY"} {
		t.Setenv(e, "")
	}

	migrateEnvToSettings(ctx, w, nil) // no vault

	if len(w.setCalls) != 0 {
		t.Errorf("got %d Set calls, want 0 — secret must be skipped (never stored plaintext) when no vault", len(w.setCalls))
	}
}

// TestMigrateEnvToSettings_ReadErrorSkips confirms a store read fault on a key is fail-soft:
// that key is skipped, never written, and startup is not blocked.
func TestMigrateEnvToSettings_ReadErrorSkips(t *testing.T) {
	ctx := context.Background()
	w := newRecordingWriter()
	w.getErr = fmt.Errorf("connection refused")
	t.Setenv("ENGRAM_RERANK_MODEL", "env-model")
	for _, e := range []string{"ENGRAM_RERANK_URL", "ENGRAM_RERANK_API_KEY", "ENGRAM_EMBEDDING_URL", "ENGRAM_EMBEDDING_MODEL", "ENGRAM_EMBEDDING_API_KEY"} {
		t.Setenv(e, "")
	}

	migrateEnvToSettings(ctx, w, nil)

	if len(w.setCalls) != 0 {
		t.Errorf("got %d Set calls, want 0 — a store read error must skip the key", len(w.setCalls))
	}
}

// TestMigrateEnvToSettings_NilStoreNoop confirms a nil store is a safe no-op (does not panic).
func TestMigrateEnvToSettings_NilStoreNoop(t *testing.T) {
	migrateEnvToSettings(context.Background(), nil, nil)
}

// TestMigrateEnvToSettings_CancelledContextAborts confirms a cancelled context (service
// shutdown) aborts the backfill before any write — no Set calls, no panic (gemini #306).
func TestMigrateEnvToSettings_CancelledContextAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the loop runs

	w := newRecordingWriter()
	t.Setenv("ENGRAM_RERANK_MODEL", "env-model") // would otherwise be written
	for _, e := range []string{"ENGRAM_RERANK_URL", "ENGRAM_RERANK_API_KEY", "ENGRAM_EMBEDDING_URL", "ENGRAM_EMBEDDING_MODEL", "ENGRAM_EMBEDDING_API_KEY"} {
		t.Setenv(e, "")
	}

	migrateEnvToSettings(ctx, w, nil)

	if len(w.setCalls) != 0 {
		t.Errorf("got %d Set calls, want 0 — cancelled context must abort before writing", len(w.setCalls))
	}
}
