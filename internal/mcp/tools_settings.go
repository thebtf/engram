package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	gogorm "gorm.io/gorm"
)

// handleSettingsConsolidated routes settings tool actions. The settings-store holds
// server-global model configuration (reranker/embedder URL, model, API key) read by the
// server on the recall/init path (#259). Writes (set, delete) are admin-gated and change
// behavior for every consumer; reads (get, list) are open to any authenticated identity but
// secret values are NEVER returned in plaintext — the server decrypts them in-process.
func (s *Server) handleSettingsConsolidated(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	action, present, err := optionalStringArg(m, "action")
	if err != nil {
		return "", err
	}
	if !present || action == "" {
		return "", fmt.Errorf("action required for settings tool (valid: set, get, list, delete)")
	}

	switch action {
	case "set":
		return s.handleSetSetting(ctx, m)
	case "get":
		return s.handleGetSetting(ctx, m)
	case "list":
		return s.handleListSettings(ctx, m)
	case "delete":
		return s.handleDeleteSetting(ctx, m)
	default:
		return "", fmt.Errorf("unknown settings action: %q (valid: set, get, list, delete)", action)
	}
}

// SetSettingsStore wires the SettingsStore built from the worker's already-open *gorm.Store
// (#259 CR-3). This reuses the single process-wide connection pool instead of opening a new
// one per tool call — NewStore opens a pool, runs migrations, and warms connections, so
// calling it per-invocation would leak pools and exhaust file descriptors under load.
func (s *Server) SetSettingsStore(store *gormstore.SettingsStore) {
	s.settingsStoreWired = store
}

// settingsStore returns the wired SettingsStore. It is wired once at server init from the
// worker's open store (SetSettingsStore); the settings tool never opens its own pool.
func (s *Server) settingsStore() (*gormstore.SettingsStore, error) {
	if s.settingsStoreWired == nil {
		return nil, fmt.Errorf("settings store not available: not wired (server started without a database)")
	}
	return s.settingsStoreWired, nil
}

// isSecretSettingKey reports whether a key holds a secret that must be stored encrypted.
// The convention is a ".api_key" suffix (reranker.api_key, embedder.api_key). A name rule
// (rather than a caller flag) means a secret can never be accidentally stored in plaintext.
func isSecretSettingKey(key string) bool {
	return strings.HasSuffix(key, ".api_key")
}

// requireAdmin enforces admin identity for server-global writes, failing closed when no
// identity is present (e.g. auth disabled) — mirrors handlePurgeProject. Settings writes
// change behavior for every consumer, so they require the operator/admin role.
func requireAdmin(ctx context.Context, action string) error {
	id, ok := auth.IdentityFrom(ctx)
	if !ok || !id.IsAdmin() {
		return fmt.Errorf("admin authorization required for settings %s (server-global config)", action)
	}
	return nil
}

// handleSetSetting stores or updates a setting (idempotent upsert). Secret keys (by name
// convention, or an explicit encrypt=true) are vault-encrypted; plain keys store the value
// as-is. Admin-gated.
func (s *Server) handleSetSetting(ctx context.Context, m map[string]any) (string, error) {
	if err := requireAdmin(ctx, "set"); err != nil {
		return "", err
	}

	keyValue, keyPresent, err := optionalStringArg(m, "key")
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(keyValue)
	if !keyPresent || key == "" {
		return "", fmt.Errorf("key is required")
	}
	value, valuePresent, err := optionalStringArg(m, "value")
	if err != nil {
		return "", err
	}
	if !valuePresent || value == "" {
		return "", fmt.Errorf("value is required")
	}
	// A key is secret if its name marks it (.api_key) OR the caller explicitly asks. The
	// name rule is the safety net; the explicit flag is forward-compat for future secret keys.
	encrypt, _, err := optionalBoolArg(m, "encrypt")
	if err != nil {
		return "", err
	}
	secret := isSecretSettingKey(key) || encrypt

	store, err := s.settingsStore()
	if err != nil {
		return "", err
	}

	in := &models.ModelSetting{
		Key:      key,
		EditedBy: "mcp",
	}
	description, _, err := optionalStringArg(m, "description")
	if err != nil {
		return "", err
	}
	in.Description = description

	if secret {
		v, vErr := s.getVault()
		if vErr != nil {
			return "", fmt.Errorf("vault not available for secret setting %q — configure ENGRAM_ENCRYPTION_KEY or ENGRAM_ENCRYPTION_KEY_FILE: %w", key, vErr)
		}
		ciphertext, encErr := v.Encrypt(value)
		if encErr != nil {
			return "", fmt.Errorf("encrypt setting %q: %w", key, encErr)
		}
		in.Encrypted = true
		in.EncryptedValue = ciphertext
		in.EncryptionKeyFingerprint = v.Fingerprint()
	} else {
		in.Value = value
	}

	stored, err := store.Set(ctx, in)
	if err != nil {
		return "", fmt.Errorf("store setting %q: %w", key, err)
	}

	return marshalJSON(map[string]any{
		"key":       stored.Key,
		"encrypted": stored.Encrypted,
		"version":   stored.Version,
		"message":   "Setting stored successfully",
	})
}

// handleGetSetting returns one setting. A plain setting returns its value; a secret setting
// is REDACTED — value is never returned, only {key, encrypted, value_set}. Secrets reach the
// server's reranker/embedder via in-process decryption, not through this management tool.
func (s *Server) handleGetSetting(ctx context.Context, m map[string]any) (string, error) {
	key := strings.TrimSpace(coerceString(m["key"], ""))
	if key == "" {
		return "", fmt.Errorf("key is required")
	}

	store, err := s.settingsStore()
	if err != nil {
		return "", err
	}

	row, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, gogorm.ErrRecordNotFound) {
			return "", fmt.Errorf("setting %q not found", key)
		}
		return "", fmt.Errorf("get setting %q: %w", key, err)
	}

	result := map[string]any{
		"key":         row.Key,
		"encrypted":   row.Encrypted,
		"description": row.Description,
		"version":     row.Version,
	}
	if row.Encrypted {
		// Redacted: never return secret plaintext through the management tool.
		result["value_set"] = len(row.EncryptedValue) > 0
	} else {
		result["value"] = row.Value
	}
	return marshalJSON(result)
}

// handleListSettings returns metadata for all settings (key/encrypted/description/version/
// updated_at), never values or ciphertext — mirrors handleListCredentials.
func (s *Server) handleListSettings(ctx context.Context, _ map[string]any) (string, error) {
	store, err := s.settingsStore()
	if err != nil {
		return "", err
	}

	rows, err := store.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list settings: %w", err)
	}

	type settingItem struct {
		Key         string `json:"key"`
		Description string `json:"description,omitempty"`
		UpdatedAt   string `json:"updated_at,omitempty"`
		Version     int    `json:"version"`
		Encrypted   bool   `json:"encrypted"`
	}
	items := make([]settingItem, 0, len(rows))
	for _, r := range rows {
		item := settingItem{
			Key:         r.Key,
			Description: r.Description,
			Version:     r.Version,
			Encrypted:   r.Encrypted,
		}
		if !r.UpdatedAt.IsZero() {
			item.UpdatedAt = r.UpdatedAt.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	return marshalJSON(items)
}

// handleDeleteSetting soft-deletes a setting by key. Admin-gated.
func (s *Server) handleDeleteSetting(ctx context.Context, m map[string]any) (string, error) {
	if err := requireAdmin(ctx, "delete"); err != nil {
		return "", err
	}

	keyValue, keyPresent, err := optionalStringArg(m, "key")
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(keyValue)
	if !keyPresent || key == "" {
		return "", fmt.Errorf("key is required")
	}

	store, err := s.settingsStore()
	if err != nil {
		return "", err
	}

	if err := store.Delete(ctx, key); err != nil {
		if errors.Is(err, gogorm.ErrRecordNotFound) {
			return "", fmt.Errorf("setting %q not found", key)
		}
		return "", fmt.Errorf("delete setting %q: %w", key, err)
	}

	return marshalJSON(map[string]any{
		"deleted": true,
		"key":     key,
	})
}
