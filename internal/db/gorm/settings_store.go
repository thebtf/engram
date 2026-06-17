// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// SettingsStore provides CRUD over the model_settings table (migration 143) — the
// server-global settings-store for swappable model configuration (#259 foundation).
//
// It mirrors CredentialStore's shape, with two deliberate differences:
//   - Settings are server-global (no project dimension); the unique key is `key`.
//   - Set is an idempotent UPSERT (settings are edited in place and bump version),
//     whereas credentials rotate via delete+create. A setting's history is not kept;
//     the latest value wins.
//
// Encryption is the caller's responsibility (same contract as CredentialStore): the
// store reads and writes ciphertext bytes in EncryptedValue and never touches the Vault.
// For non-secret config the plaintext lives in Value with Encrypted=false.
type SettingsStore struct {
	db *gorm.DB
}

// NewSettingsStore creates a SettingsStore backed by the given Store.
func NewSettingsStore(store *Store) *SettingsStore {
	return &SettingsStore{db: store.DB}
}

// Set inserts or updates a setting by key (idempotent upsert). The caller's input is
// never mutated; EncryptedValue is copied. For a secret setting, set Encrypted=true,
// populate EncryptedValue + EncryptionKeyFingerprint, and leave Value empty. For plain
// config, set Value and leave Encrypted=false. Returns the stored row.
func (s *SettingsStore) Set(ctx context.Context, in *models.ModelSetting) (*models.ModelSetting, error) {
	if in == nil {
		return nil, fmt.Errorf("setting must not be nil")
	}
	if in.Key == "" {
		return nil, fmt.Errorf("setting.Key must not be empty")
	}
	if in.Encrypted {
		if len(in.EncryptedValue) == 0 {
			return nil, fmt.Errorf("setting %q: Encrypted=true requires a non-empty EncryptedValue", in.Key)
		}
		if in.EncryptionKeyFingerprint == "" {
			return nil, fmt.Errorf("setting %q: Encrypted=true requires EncryptionKeyFingerprint", in.Key)
		}
	} else if len(in.EncryptedValue) > 0 {
		return nil, fmt.Errorf("setting %q: EncryptedValue set but Encrypted=false (ambiguous)", in.Key)
	}

	now := time.Now().UTC()
	var encrypted []byte
	if len(in.EncryptedValue) > 0 {
		encrypted = append([]byte(nil), in.EncryptedValue...)
	}

	// Load any existing active row for this key to decide insert vs update.
	var existing ModelSetting
	err := s.db.WithContext(ctx).
		Where("key = ? AND deleted_at IS NULL", in.Key).
		First(&existing).Error
	switch {
	case err == gorm.ErrRecordNotFound:
		row := &ModelSetting{
			Key:                      in.Key,
			Value:                    in.Value,
			EncryptedValue:           encrypted,
			Encrypted:                in.Encrypted,
			EncryptionKeyFingerprint: in.EncryptionKeyFingerprint,
			Description:              in.Description,
			EditedBy:                 in.EditedBy,
			Version:                  1,
			CreatedAt:                now,
			UpdatedAt:                now,
		}
		if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
			return nil, fmt.Errorf("create setting %q: %w", in.Key, err)
		}
		return modelSettingRowToModel(row), nil
	case err != nil:
		return nil, fmt.Errorf("load setting %q: %w", in.Key, err)
	default:
		updates := map[string]any{
			"value":                      in.Value,
			"encrypted_value":            encrypted,
			"encrypted":                  in.Encrypted,
			"encryption_key_fingerprint": in.EncryptionKeyFingerprint,
			"description":                in.Description,
			"edited_by":                  in.EditedBy,
			"version":                    existing.Version + 1,
			"updated_at":                 now,
		}
		if err := s.db.WithContext(ctx).Model(&ModelSetting{}).
			Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("update setting %q: %w", in.Key, err)
		}
		var updated ModelSetting
		if err := s.db.WithContext(ctx).First(&updated, existing.ID).Error; err != nil {
			return nil, fmt.Errorf("reload setting %q: %w", in.Key, err)
		}
		return modelSettingRowToModel(&updated), nil
	}
}

// Get returns the active setting for the given key, or a wrapped gorm.ErrRecordNotFound.
func (s *SettingsStore) Get(ctx context.Context, key string) (*models.ModelSetting, error) {
	if key == "" {
		return nil, fmt.Errorf("key: must not be empty")
	}
	var row ModelSetting
	err := s.db.WithContext(ctx).
		Where("key = ? AND deleted_at IS NULL", key).
		First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get setting %q: %w", key, err)
	}
	return modelSettingRowToModel(&row), nil
}

// List returns all active settings ordered by key. Secret values are returned as
// ciphertext in EncryptedValue; the caller decrypts as needed (and typically redacts
// secret keys when surfacing to a client).
func (s *SettingsStore) List(ctx context.Context) ([]*models.ModelSetting, error) {
	var rows []ModelSetting
	err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Order("key ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	result := make([]*models.ModelSetting, len(rows))
	for i := range rows {
		result[i] = modelSettingRowToModel(&rows[i])
	}
	return result, nil
}

// Delete soft-deletes the active setting for the given key. The unique index is partial
// (WHERE deleted_at IS NULL), so a soft-deleted key can be re-created later without a
// constraint clash. Returns gorm.ErrRecordNotFound if no active row exists.
func (s *SettingsStore) Delete(ctx context.Context, key string) error {
	if key == "" {
		return fmt.Errorf("key: must not be empty")
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&ModelSetting{}).
		Where("key = ? AND deleted_at IS NULL", key).
		Update("deleted_at", now)
	if result.Error != nil {
		return fmt.Errorf("delete setting %q: %w", key, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete setting %q: %w", key, gorm.ErrRecordNotFound)
	}
	return nil
}

// modelSettingRowToModel converts an internal GORM ModelSetting row to the pkg/models type.
func modelSettingRowToModel(row *ModelSetting) *models.ModelSetting {
	return &models.ModelSetting{
		ID:                       row.ID,
		Key:                      row.Key,
		Value:                    row.Value,
		EncryptedValue:           row.EncryptedValue,
		Encrypted:                row.Encrypted,
		EncryptionKeyFingerprint: row.EncryptionKeyFingerprint,
		Description:              row.Description,
		EditedBy:                 row.EditedBy,
		Version:                  row.Version,
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
		DeletedAt:                row.DeletedAt,
	}
}
