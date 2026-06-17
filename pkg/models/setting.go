// Package models contains domain models for engram.
package models

import "time"

// ModelSetting represents a server-global model-configuration entry in the
// model_settings table (migration 143) — the #259 settings-store foundation.
//
// Exactly one of Value (plain config: URLs, model names) or EncryptedValue (secret:
// API keys, AES-256-GCM ciphertext) carries the payload, indicated by Encrypted.
// Encryption/decryption is performed by the handler layer via the existing Vault,
// mirroring the Credential contract; the store layer only moves ciphertext bytes.
type ModelSetting struct {
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	DeletedAt                *time.Time `json:"deleted_at,omitempty"`
	Key                      string     `json:"key"`
	Value                    string     `json:"value,omitempty"`
	EncryptionKeyFingerprint string     `json:"encryption_key_fingerprint,omitempty"`
	Description              string     `json:"description,omitempty"`
	EditedBy                 string     `json:"edited_by,omitempty"`
	EncryptedValue           []byte     `json:"encrypted_value,omitempty"`
	Encrypted                bool       `json:"encrypted"`
	ID                       int64      `json:"id"`
	Version                  int        `json:"version"`
}
