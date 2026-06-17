package worker

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/crypto"
	enggorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// envSettingPair maps a legacy environment variable to its settings-store key (#259 CR-4).
type envSettingPair struct {
	envVar     string
	settingKey string
}

// envSettingMigrations is the set of model-config env vars that migrate into the settings-store.
// Keys ending ".api_key" are secrets (vault-encrypted on write); the rest are plaintext config.
// The embedding DIMENSION is deliberately absent — it is schema-bound, never a runtime setting.
var envSettingMigrations = []envSettingPair{
	{"ENGRAM_RERANK_URL", "reranker.url"},
	{"ENGRAM_RERANK_MODEL", "reranker.model"},
	{"ENGRAM_RERANK_API_KEY", "reranker.api_key"},
	{"ENGRAM_EMBEDDING_URL", "embedder.url"},
	{"ENGRAM_EMBEDDING_MODEL", "embedder.model"},
	{"ENGRAM_EMBEDDING_API_KEY", "embedder.api_key"},
}

// settingsWriter is the minimal write seam migrateEnvToSettings needs from the settings-store
// (*enggorm.SettingsStore satisfies it). Explicit so the migration is unit-testable without a DB.
type settingsWriter interface {
	Get(ctx context.Context, key string) (*models.ModelSetting, error)
	Set(ctx context.Context, in *models.ModelSetting) (*models.ModelSetting, error)
}

// migrateEnvToSettings performs a one-time, idempotent backfill of legacy model-config env vars
// into the settings-store (#259 CR-4). For each pair: if the env var is set AND the settings key
// is ABSENT, the env value is written to the store (secrets vault-encrypted) and a deprecation is
// logged once. It NEVER overwrites an existing store value — an operator who already set a value
// via the settings tool keeps it (env-first precedence at read time still lets env override for the
// running process; this migration only seeds the durable store so the operator can later drop env).
//
// This is a boot-time DATA backfill, not a DB schema migration. It is fail-soft: a per-key error is
// logged and skipped so a single bad key never blocks startup. vaultProvider is lazy (only called
// when a secret key actually needs encrypting) so a deployment with no secret env vars never forces
// vault-key initialization.
func migrateEnvToSettings(ctx context.Context, store settingsWriter, vaultProvider func() (*crypto.Vault, error)) {
	if store == nil {
		return
	}
	migrated := 0
	for _, p := range envSettingMigrations {
		envVal := os.Getenv(p.envVar)
		if envVal == "" {
			continue // env not set → nothing to migrate for this key
		}

		// Skip when the store already holds this key (active row) — never clobber an
		// operator-set value. Only a genuine "not found" is the signal to backfill.
		_, err := store.Get(ctx, p.settingKey)
		if err == nil {
			continue // already present → leave it
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Warn().Err(err).Str("key", p.settingKey).Msg("env-migrate: store read failed; skipping this key")
			continue
		}

		in := &models.ModelSetting{
			Key:         p.settingKey,
			Description: "migrated from " + p.envVar + " (CR-4); remove the env var to let this stored value take over",
			EditedBy:    "env-migrate",
		}

		if strings.HasSuffix(p.settingKey, ".api_key") {
			// Secret: encrypt via vault. A vault failure skips this one key (fail-soft) — the
			// env var still works at runtime, so nothing breaks; the operator just can't drop it yet.
			if vaultProvider == nil {
				log.Warn().Str("key", p.settingKey).Msg("env-migrate: secret env var set but no vault wired; skipping (env still used at runtime)")
				continue
			}
			v, vErr := vaultProvider()
			if vErr != nil || v == nil {
				log.Warn().Err(vErr).Str("key", p.settingKey).Msg("env-migrate: vault unavailable; skipping secret (env still used at runtime)")
				continue
			}
			ciphertext, encErr := v.Encrypt(envVal)
			if encErr != nil {
				log.Warn().Err(encErr).Str("key", p.settingKey).Msg("env-migrate: encrypt failed; skipping secret")
				continue
			}
			in.Encrypted = true
			in.EncryptedValue = ciphertext
			in.EncryptionKeyFingerprint = v.Fingerprint()
		} else {
			in.Value = envVal
		}

		if _, err := store.Set(ctx, in); err != nil {
			log.Warn().Err(err).Str("key", p.settingKey).Msg("env-migrate: store write failed; skipping")
			continue
		}
		migrated++
		log.Info().Str("env", p.envVar).Str("key", p.settingKey).Bool("secret", in.Encrypted).
			Msg("env-migrate: seeded settings-store from env var (#259 CR-4); you may remove the env var to let the stored value take over")
	}
	if migrated > 0 {
		log.Info().Int("count", migrated).Msg("env-migrate: settings-store backfill complete")
	}
}

// migrateEnvToSettingsStore adapts the concrete *enggorm.SettingsStore to migrateEnvToSettings.
// Separate thin wrapper so service.go calls a concrete-typed helper while the core logic stays
// testable against the settingsWriter seam.
func migrateEnvToSettingsStore(ctx context.Context, store *enggorm.SettingsStore, vaultProvider func() (*crypto.Vault, error)) {
	if store == nil {
		return
	}
	migrateEnvToSettings(ctx, store, vaultProvider)
}
