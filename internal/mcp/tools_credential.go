package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	gogorm "gorm.io/gorm"
)

// getVault returns the Server's Vault, initializing it lazily on first call.
// The vault is a singleton within the server instance; initialization errors
// are permanent — restart the server after fixing key configuration to retry.
func (s *Server) getVault() (*crypto.Vault, error) {
	s.vaultOnce.Do(func() {
		cfg := config.Get()
		s.vault, s.vaultInitErr = crypto.NewVault(cfg)
		if s.vaultInitErr != nil {
			log.Error().Err(s.vaultInitErr).Msg("vault: failed to initialize")
		}
	})
	return s.vault, s.vaultInitErr
}

// credentialStore derives a dedicated CredentialStore from the server's configured
// database DSN using only public constructors. This removes the prior reflect/unsafe
// field access while keeping the change local to MCP wiring.
func (s *Server) credentialStore() (*gormstore.CredentialStore, error) {
	dsn := config.GetDatabaseDSN()
	if dsn == "" {
		return nil, fmt.Errorf("credential store not available: database DSN not configured")
	}

	store, err := gormstore.NewStore(gormstore.Config{DSN: dsn})
	if err != nil {
		return nil, fmt.Errorf("credential store not available: %w", err)
	}
	return gormstore.NewCredentialStore(store), nil
}

// handleStoreCredential encrypts and stores a credential in the dedicated credentials table.
func (s *Server) handleStoreCredential(ctx context.Context, args json.RawMessage) (string, error) {
	store, err := s.credentialStore()
	if err != nil {
		return "", err
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Tags    []string
		Name    string
		Value   string
		Scope   string
		Project string
	}
	params.Tags = coerceStringSlice(m["tags"])
	params.Name = coerceString(m["name"], "")
	params.Value = coerceString(m["value"], "")
	params.Scope = coerceString(m["scope"], "")
	params.Project = coerceString(m["project"], "")
	if params.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if params.Value == "" {
		return "", fmt.Errorf("value is required")
	}
	if params.Scope == "" {
		params.Scope = "project"
	}
	switch params.Scope {
	case "project", "global":
		// valid
	default:
		return "", fmt.Errorf("invalid scope %q: must be \"project\" or \"global\"", params.Scope)
	}
	if params.Scope == "global" {
		return "", fmt.Errorf("global-scope credentials are not yet supported; use scope \"project\" and provide a project name")
	}
	if params.Project == "" {
		return "", fmt.Errorf("project is required for project-scoped credentials")
	}

	v, err := s.getVault()
	if err != nil {
		return "", fmt.Errorf("vault not available: %w", err)
	}

	ciphertext, err := v.Encrypt(params.Value)
	if err != nil {
		return "", fmt.Errorf("encrypt credential: %w", err)
	}

	created, err := store.Create(ctx, &models.Credential{
		Project:                  params.Project,
		Key:                      params.Name,
		EncryptedSecret:          ciphertext,
		EncryptionKeyFingerprint: v.Fingerprint(),
		Scope:                    params.Scope,
		EditedBy:                 "mcp",
	})
	if err != nil {
		return "", fmt.Errorf("store credential: %w", err)
	}

	result := map[string]any{
		"id":      created.ID,
		"name":    params.Name,
		"scope":   params.Scope,
		"message": "Credential stored successfully",
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleGetCredential retrieves and decrypts a credential by name.
func (s *Server) handleGetCredential(ctx context.Context, args json.RawMessage) (string, error) {
	store, err := s.credentialStore()
	if err != nil {
		return "", err
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Name    string
		Project string
	}
	params.Name = coerceString(m["name"], "")
	params.Project = coerceString(m["project"], "")
	if params.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if params.Project == "" {
		return "", fmt.Errorf("project is required")
	}

	v, err := s.getVault()
	if err != nil {
		return "", fmt.Errorf("vault not available — configure ENGRAM_ENCRYPTION_KEY or ENGRAM_ENCRYPTION_KEY_FILE: %w", err)
	}

	cred, err := store.Get(ctx, params.Project, params.Name)
	if err != nil {
		if errors.Is(err, gogorm.ErrRecordNotFound) {
			return "", fmt.Errorf("credential %q not found", params.Name)
		}
		return "", fmt.Errorf("get credential: %w", err)
	}

	if cred.EncryptionKeyFingerprint != "" && !v.MatchesFingerprint(cred.EncryptionKeyFingerprint) {
		return "", fmt.Errorf(
			"encryption key mismatch: credential %q was encrypted with key fingerprint %q, current key has fingerprint %q — restore the original key to decrypt",
			params.Name, cred.EncryptionKeyFingerprint, v.Fingerprint(),
		)
	}

	plaintext, err := v.Decrypt(cred.EncryptedSecret)
	if err != nil {
		return "", fmt.Errorf("decrypt credential %q: %w", params.Name, err)
	}

	result := map[string]any{
		"name":  params.Name,
		"value": plaintext,
		"scope": cred.Scope,
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleListCredentials lists credential names and metadata (no values).
func (s *Server) handleListCredentials(ctx context.Context, args json.RawMessage) (string, error) {
	store, err := s.credentialStore()
	if err != nil {
		return "", err
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Project string
	}
	params.Project = coerceString(m["project"], "")
	if params.Project == "" {
		return "[]", nil
	}

	creds, err := store.List(ctx, params.Project)
	if err != nil {
		return "", fmt.Errorf("list credentials: %w", err)
	}

	type credItem struct {
		Name      string `json:"name"`
		Scope     string `json:"scope"`
		CreatedAt string `json:"created_at,omitempty"`
		ID        int64  `json:"id"`
	}
	items := make([]credItem, 0, len(creds))
	for _, c := range creds {
		item := credItem{
			ID:    c.ID,
			Name:  c.Key,
			Scope: c.Scope,
		}
		if !c.CreatedAt.IsZero() {
			item.CreatedAt = c.CreatedAt.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}

	out, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleDeleteCredential removes a credential by name.
func (s *Server) handleDeleteCredential(ctx context.Context, args json.RawMessage) (string, error) {
	store, err := s.credentialStore()
	if err != nil {
		return "", err
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Name    string
		Scope   string
		Project string
	}
	params.Name = coerceString(m["name"], "")
	params.Scope = coerceString(m["scope"], "")
	params.Project = coerceString(m["project"], "")
	if params.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if params.Scope == "" {
		params.Scope = "project"
	}
	switch params.Scope {
	case "project", "global":
		// valid
	default:
		return "", fmt.Errorf("invalid scope %q: must be \"project\" or \"global\"", params.Scope)
	}
	if params.Scope == "global" {
		return "", fmt.Errorf("global-scope credentials are not yet supported; use scope \"project\" and provide a project name")
	}
	if params.Project == "" {
		return "", fmt.Errorf("project is required for project-scoped credentials")
	}

	if err := store.Delete(ctx, params.Project, params.Name); err != nil {
		if errors.Is(err, gogorm.ErrRecordNotFound) {
			return "", fmt.Errorf("credential %q not found", params.Name)
		}
		return "", fmt.Errorf("delete credential: %w", err)
	}

	result := map[string]any{
		"deleted": true,
		"name":    params.Name,
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleVaultStatus returns vault key status and credential count.
// This is a read-only status check: it does NOT initialize the vault or create
// any key files. It uses a passive existence check to determine key_configured.
func (s *Server) handleVaultStatus(ctx context.Context, _ json.RawMessage) (string, error) {
	cfg := config.Get()
	keyConfigured := crypto.VaultExists(cfg)
	fingerprint := ""

	// Only load fingerprint and key source when vault is already configured (read existing key).
	keySource := ""
	if keyConfigured {
		if v, err := s.getVault(); err == nil && v != nil {
			fingerprint = v.Fingerprint()
			keySource = v.KeySource()
		}
	}

	count := 0
	if store, err := s.credentialStore(); err == nil {
		if n, countErr := store.CountCredentials(ctx); countErr == nil {
			count = int(n)
		}
	}

	result := map[string]any{
		"key_configured":   keyConfigured,
		"key_source":       keySource,
		"fingerprint":      fingerprint,
		"credential_count": count,
		"backup_reminder":  "Back up vault.key (or set ENGRAM_ENCRYPTION_KEY) — losing this key makes stored credentials unrecoverable",
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// expandTagHierarchy expands "lang:go:concurrency" into ["lang", "lang:go", "lang:go:concurrency"].
func expandTagHierarchy(tag string) []string {
	var parts []string
	start := 0
	segments := make([]string, 0, 4)
	for i := 0; i < len(tag); i++ {
		if tag[i] == ':' {
			segments = append(segments, tag[start:i])
			start = i + 1
		}
	}
	segments = append(segments, tag[start:])

	for i := range segments {
		p := ""
		for j := 0; j <= i; j++ {
			if j > 0 {
				p += ":"
			}
			p += segments[j]
		}
		parts = append(parts, p)
	}
	return parts
}
