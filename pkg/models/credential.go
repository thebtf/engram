// Package models contains domain models for engram.
package models

import "time"

// Credential represents a vault-stored encrypted credential in the credentials table.
// Credentials were previously stored as rows in the observations table (type='credential').
// Migration 087 creates this dedicated table; migration 088+ will migrate data and drop observations.
type Credential struct {
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	DeletedAt                *time.Time `json:"deleted_at,omitempty"`
	Project                  string     `json:"project"`
	Key                      string     `json:"key"`
	EncryptionKeyFingerprint string     `json:"encryption_key_fingerprint"`
	Scope                    string     `json:"scope,omitempty"`
	EditedBy                 string     `json:"edited_by,omitempty"`
	EncryptedSecret          []byte     `json:"encrypted_secret"`
	ID                       int64      `json:"id"`
	Version                  int        `json:"version"`
}
