package worker

import (
	"context"
	"errors"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	enggorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// settingsReader is the minimal read seam the resolver needs from the settings-store:
// fetch one setting by key. *enggorm.SettingsStore already satisfies it. The seam is
// explicit (rather than a direct concrete dependency) because CR-3 will extend this
// adapter to vault-decrypt encrypted rows, so it is a re-touched boundary worth keeping
// testable without a live DB.
type settingsReader interface {
	Get(ctx context.Context, key string) (*models.ModelSetting, error)
}

// settingsResolver adapts a settingsReader to the minimal surface the reranking and
// embedding clients need (their identical SettingsResolver interface: Get(ctx, key)
// (string, bool)). It lives in the worker package — not the low-level client packages —
// so those stay storage-agnostic and free of an import cycle.
//
// CR-2 scope (#259): it serves ONLY non-secret config (URLs, model names). An encrypted
// (secret) row is reported absent so the caller falls through to its env/default path;
// secret resolution requires vault decryption and is CR-3 work, not CR-2.
type settingsResolver struct {
	reader settingsReader
}

// newSettingsResolver wraps a SettingsStore. A nil store yields a resolver that always
// reports absence, which is the correct env-first fallback when the store is unavailable.
func newSettingsResolver(store *enggorm.SettingsStore) *settingsResolver {
	if store == nil {
		return &settingsResolver{}
	}
	return &settingsResolver{reader: store}
}

// Get returns (value, true) for a present non-secret setting, ("", false) otherwise:
// missing key, an encrypted/secret row, or a read error. Read errors other than
// record-not-found are logged; the caller treats every absence identically as
// "use env or default", so a transient settings-store fault never breaks client init.
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
	if row == nil || row.Encrypted {
		// A nil row (a settingsReader returning (nil, nil) — defensive against custom or
		// test implementations) or a secret row (ciphertext only, not served on the CR-2
		// plaintext read path) both resolve to absent, so the caller uses env/default.
		return "", false
	}
	return row.Value, true
}
