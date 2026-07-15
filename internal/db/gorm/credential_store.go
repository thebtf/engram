// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// CredentialStore provides credential-related database operations using GORM.
// It targets the dedicated credentials table created by migration 087.
//
// Method signatures mirror the vault helpers on ObservationStore
// (CountCredentials / CountCredentialsWithDifferentFingerprint / DeleteOrphanedCredentials)
// so callers in internal/worker/handlers_vault.go can swap without signature changes
// beyond the receiver type.
type CredentialStore struct {
	db *gorm.DB
}

// NewCredentialStore creates a new CredentialStore backed by the given Store.
func NewCredentialStore(store *Store) *CredentialStore {
	return &CredentialStore{db: store.DB}
}

// Create inserts a new credential row. Returns a new *models.Credential populated with
// the database-assigned ID and timestamps. The caller's input is never mutated.
// Project credentials require a non-empty project; global credentials use project="".
// The UNIQUE(project, key) constraint keeps those namespaces independent.
// The caller is responsible for encrypting the secret before passing it in.
func (s *CredentialStore) Create(ctx context.Context, cred *models.Credential) (*models.Credential, error) {
	if cred == nil {
		return nil, fmt.Errorf("credential must not be nil")
	}
	scope := cred.Scope
	if scope == "" {
		scope = "project"
	}
	switch scope {
	case "project":
		if cred.Project == "" {
			return nil, fmt.Errorf("credential.Project must not be empty for project scope")
		}
	case "global":
		if cred.Project != "" {
			return nil, fmt.Errorf("credential.Project must be empty for global scope")
		}
	default:
		return nil, fmt.Errorf("credential.Scope must be \"project\" or \"global\"")
	}
	if cred.Key == "" {
		return nil, fmt.Errorf("credential.Key must not be empty")
	}
	if len(cred.EncryptedSecret) == 0 {
		return nil, fmt.Errorf("credential.EncryptedSecret must not be empty")
	}
	if cred.EncryptionKeyFingerprint == "" {
		return nil, fmt.Errorf("credential.EncryptionKeyFingerprint must not be empty")
	}

	// Use a single timestamp for both created_at and updated_at — two calls to time.Now()
	// could produce different values under load and should always match on create.
	// Copy the encrypted secret to prevent the caller's slice from aliasing the stored row —
	// immutability contract: the caller's input is never mutated.
	now := time.Now().UTC()
	secret := append([]byte(nil), cred.EncryptedSecret...)

	row := &Credential{
		Project:                  cred.Project,
		Key:                      cred.Key,
		EncryptedSecret:          secret,
		EncryptionKeyFingerprint: cred.EncryptionKeyFingerprint,
		Scope:                    scope,
		EditedBy:                 cred.EditedBy,
		Version:                  1,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if cred.Version > 0 {
		row.Version = cred.Version
	}

	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("create credential %q/%q: %w", cred.Project, cred.Key, err)
	}
	return credentialRowToModel(row), nil
}

// Get returns the active credential matching the given project and key.
// An empty project selects the exact global namespace; non-empty projects keep
// the existing project-scoped lookup semantics.
// Returns a wrapped gorm.ErrRecordNotFound if no active row exists.
func (s *CredentialStore) Get(ctx context.Context, project, key string) (*models.Credential, error) {
	if key == "" {
		return nil, fmt.Errorf("key: must not be empty")
	}
	var row Credential
	err := s.db.WithContext(ctx).
		Where("project = ? AND key = ? AND deleted_at IS NULL", project, key).
		First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get credential %q/%q: %w", project, key, err)
	}
	return credentialRowToModel(&row), nil
}

// GetByName returns the first active credential matching the given key across all projects.
// Used by the dashboard admin view where the caller doesn't know the project.
// Global credentials are preferred, then project rows are ordered deterministically.
// Returns a wrapped gorm.ErrRecordNotFound if no active row exists.
func (s *CredentialStore) GetByName(ctx context.Context, key string) (*models.Credential, error) {
	if key == "" {
		return nil, fmt.Errorf("key: must not be empty")
	}
	var row Credential
	err := s.db.WithContext(ctx).
		Where("key = ? AND deleted_at IS NULL", key).
		Order("CASE WHEN project = '' THEN 0 ELSE 1 END").
		Order("project ASC").
		First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get credential by name %q: %w", key, err)
	}
	return credentialRowToModel(&row), nil
}

// List returns all active credentials for the exact project namespace, ordered
// by key ascending. An empty project lists global credentials only.
func (s *CredentialStore) List(ctx context.Context, project string) ([]*models.Credential, error) {
	var rows []Credential
	err := s.db.WithContext(ctx).
		Where("project = ? AND deleted_at IS NULL", project).
		Order("key ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list credentials for project %q: %w", project, err)
	}
	result := make([]*models.Credential, len(rows))
	for i := range rows {
		result[i] = credentialRowToModel(&rows[i])
	}
	return result, nil
}

// ListAll returns all active (non-soft-deleted) credentials across all projects,
// ordered by project then key. Used by the dashboard admin view.
func (s *CredentialStore) ListAll(ctx context.Context) ([]*models.Credential, error) {
	var rows []Credential
	err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Order("project ASC, key ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list all credentials: %w", err)
	}
	result := make([]*models.Credential, len(rows))
	for i := range rows {
		result[i] = credentialRowToModel(&rows[i])
	}
	return result, nil
}

// Delete permanently removes the credential matching the exact (project, key)
// namespace. An empty project selects the global namespace.
// Returns gorm.ErrRecordNotFound if no row exists.
//
// Design note — hard-delete vs soft-delete: credentials use hard-delete
// intentionally. The primary use-case is key rotation: delete the old
// credential, then create a new one with the same (project, key). The
// credentials table has UNIQUE(project, key) without a partial index, so a
// soft-deleted row with deleted_at IS NOT NULL would block re-creation with a
// unique constraint violation. The deleted_at column is present in the schema
// and GORM model for future auditing / cascaded-delete use (e.g. if a project
// is removed), but it is NOT used by this Delete method.
// See: TestCredentialStore_CreateGetCountDelete "rotation scenario" test case.
func (s *CredentialStore) Delete(ctx context.Context, project, key string) error {
	if key == "" {
		return fmt.Errorf("key: must not be empty")
	}
	result := s.db.WithContext(ctx).
		Where("project = ? AND key = ? AND deleted_at IS NULL", project, key).
		Delete(&Credential{})
	if result.Error != nil {
		return fmt.Errorf("delete credential %q/%q: %w", project, key, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete credential %q/%q: %w", project, key, gorm.ErrRecordNotFound)
	}
	return nil
}

// DeleteByName hard-deletes the first active credential matching the given key across
// all projects, preferring the exact global row. Used by the dashboard admin view
// where the caller doesn't know the project. Returns gorm.ErrRecordNotFound if absent.
func (s *CredentialStore) DeleteByName(ctx context.Context, key string) error {
	if key == "" {
		return fmt.Errorf("key: must not be empty")
	}
	// Find the exact row first; a global credential wins when names coexist.
	var row Credential
	if err := s.db.WithContext(ctx).
		Where("key = ? AND deleted_at IS NULL", key).
		Order("CASE WHEN project = '' THEN 0 ELSE 1 END").
		Order("project ASC").
		First(&row).Error; err != nil {
		return fmt.Errorf("delete credential by name %q: %w", key, err)
	}
	result := s.db.WithContext(ctx).Delete(&Credential{}, row.ID)
	if result.Error != nil {
		return fmt.Errorf("delete credential by name %q: %w", key, result.Error)
	}
	return nil
}

// CountCredentials returns the total number of active (non-soft-deleted) credentials
// across all projects. Mirrors ObservationStore.CountCredentials signature.
func (s *CredentialStore) CountCredentials(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&Credential{}).
		Where("deleted_at IS NULL").
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count credentials: %w", err)
	}
	return count, nil
}

// CountWithDifferentFingerprint counts active credentials whose
// encryption_key_fingerprint differs from currentFingerprint.
// A non-zero result means some credentials were encrypted with a different key
// (key rotation happened or the wrong key is in use).
// Mirrors ObservationStore.CountCredentialsWithDifferentFingerprint signature.
func (s *CredentialStore) CountWithDifferentFingerprint(ctx context.Context, currentFingerprint string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&Credential{}).
		Where("deleted_at IS NULL").
		Where("encryption_key_fingerprint != '' AND encryption_key_fingerprint != ?", currentFingerprint).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count mismatched credentials: %w", err)
	}
	return count, nil
}

// DeleteOrphanedByFingerprint hard-deletes (permanent DELETE) all active credentials
// whose encryption_key_fingerprint differs from currentFingerprint.
// These rows cannot be decrypted with the current key and are irrecoverable.
// Returns the number of rows deleted.
// Mirrors ObservationStore.DeleteOrphanedCredentials signature.
func (s *CredentialStore) DeleteOrphanedByFingerprint(ctx context.Context, currentFingerprint string) (int64, error) {
	if currentFingerprint == "" {
		return 0, fmt.Errorf("currentFingerprint must not be empty")
	}
	result := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("encryption_key_fingerprint != '' AND encryption_key_fingerprint != ?", currentFingerprint).
		Delete(&Credential{})
	if result.Error != nil {
		return 0, fmt.Errorf("delete orphaned credentials (fingerprint %q): %w", currentFingerprint, result.Error)
	}
	return result.RowsAffected, nil
}

// credentialRowToModel converts an internal GORM Credential row to the pkg/models.Credential type.
func credentialRowToModel(row *Credential) *models.Credential {
	return &models.Credential{
		ID:                       row.ID,
		Project:                  row.Project,
		Key:                      row.Key,
		EncryptedSecret:          row.EncryptedSecret,
		EncryptionKeyFingerprint: row.EncryptionKeyFingerprint,
		Scope:                    row.Scope,
		EditedBy:                 row.EditedBy,
		Version:                  row.Version,
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
		DeletedAt:                row.DeletedAt,
	}
}
