package worker

import (
	"context"
	"errors"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/crypto"
	enggorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// settingsReader is the minimal read seam the resolver needs from the settings-store:
// fetch one setting by key. *enggorm.SettingsStore already satisfies it. The seam is
// explicit (rather than a direct concrete dependency) so the resolver's decrypt and
// fall-through behavior is testable without a live DB.
type settingsReader interface {
	Get(ctx context.Context, key string) (*models.ModelSetting, error)
}

// settingsResolver adapts a settingsReader to the minimal surface the reranking and
// embedding clients need (their identical SettingsResolver interface: Get(ctx, key)
// (string, bool)). It lives in the worker package — not the low-level client packages —
// so those stay storage-agnostic and free of an import cycle.
//
// Non-secret rows resolve to their plaintext Value. Secret (encrypted) rows are decrypted
// in-process via the vault (CR-3) so a stored API key reaches the client as plaintext
// without ever leaving the server. Decryption is fail-SOFT: any vault/fingerprint/decrypt
// problem resolves to absent, so the caller falls through to env/default and a settings
// fault never breaks client init (the CR-2 contract, extended to the secret path).
type settingsResolver struct {
	reader settingsReader
	// vaultProvider lazily supplies the decryption vault. It is only called when an
	// encrypted row is actually encountered, so a deployment with no secret settings
	// never forces vault initialization (which would auto-generate a key file). nil means
	// "no vault wired" → encrypted rows resolve to absent.
	vaultProvider func() (*crypto.Vault, error)
}

// newSettingsResolver wraps a SettingsStore with an optional vault provider for decrypting
// secret rows. A nil store yields a resolver that always reports absence (the correct
// env-first fallback when the store is unavailable). A nil vaultProvider disables secret
// resolution: encrypted rows report absent, so the caller uses env/default.
func newSettingsResolver(store *enggorm.SettingsStore, vaultProvider func() (*crypto.Vault, error)) *settingsResolver {
	if store == nil {
		return &settingsResolver{vaultProvider: vaultProvider}
	}
	return &settingsResolver{reader: store, vaultProvider: vaultProvider}
}

// Get returns (value, true) for a present setting whose value can be resolved, ("", false)
// otherwise. For a plain row it returns Value. For an encrypted row it decrypts via the
// vault (fail-soft: missing vault, fingerprint mismatch, or decrypt error → absent). Every
// absence — missing key, read error, nil row, undecryptable secret — is treated identically
// by the caller as "use env or default", so a settings-store fault never breaks client init.
func (r *settingsResolver) Get(ctx context.Context, key string) (string, bool) {
	if r == nil || r.reader == nil {
		return "", false
	}
	row, err := r.reader.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Warn().Err(err).Str("key", key).Msg("settings-store: read failed; falling back to env/default")
		}
		return "", false
	}
	if row == nil {
		// A settingsReader returning (nil, nil) — defensive against custom or test
		// implementations — resolves to absent, like a missing key.
		return "", false
	}
	if row.Encrypted {
		return r.decryptSecret(key, row)
	}
	return row.Value, true
}

// decryptSecret resolves an encrypted row to its plaintext, fail-soft. Any failure
// (no vault wired, vault init error, key-fingerprint mismatch, decrypt error) logs a
// warning and returns absent so the caller falls through to env/default — a secret that
// cannot be decrypted must NEVER break the reranker/embedder init path.
func (r *settingsResolver) decryptSecret(key string, row *models.ModelSetting) (string, bool) {
	if r.vaultProvider == nil {
		log.Warn().Str("key", key).Msg("settings-store: encrypted setting present but no vault wired; using env/default")
		return "", false
	}
	v, err := r.vaultProvider()
	if err != nil || v == nil {
		log.Warn().Err(err).Str("key", key).Msg("settings-store: vault unavailable for encrypted setting; using env/default")
		return "", false
	}
	if row.EncryptionKeyFingerprint != "" && !v.MatchesFingerprint(row.EncryptionKeyFingerprint) {
		log.Warn().Str("key", key).Str("stored_fingerprint", row.EncryptionKeyFingerprint).Str("current_fingerprint", v.Fingerprint()).
			Msg("settings-store: encrypted setting key fingerprint mismatch; using env/default (restore original key to decrypt)")
		return "", false
	}
	plaintext, err := v.Decrypt(row.EncryptedValue)
	if err != nil {
		log.Warn().Err(err).Str("key", key).Msg("settings-store: decrypt of encrypted setting failed; using env/default")
		return "", false
	}
	return plaintext, true
}
