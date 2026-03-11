package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/pkg/models"
)

// vaultOnce guards lazy vault initialization.
var (
	sharedVault  *crypto.Vault
	vaultInitErr error
	vaultOnce    sync.Once
)

// getVault returns the shared Vault, initializing it lazily on first call.
func getVault() (*crypto.Vault, error) {
	vaultOnce.Do(func() {
		cfg := config.Get()
		sharedVault, vaultInitErr = crypto.NewVault(cfg)
		if vaultInitErr != nil {
			log.Error().Err(vaultInitErr).Msg("vault: failed to initialize")
		}
	})
	return sharedVault, vaultInitErr
}

// handleStoreCredential encrypts and stores a credential observation.
func (s *Server) handleStoreCredential(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	var params struct {
		Tags    []string `json:"tags"`
		Name    string   `json:"name"`
		Value   string   `json:"value"`
		Scope   string   `json:"scope"`
		Project string   `json:"project"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if params.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if params.Value == "" {
		return "", fmt.Errorf("value is required")
	}
	if params.Scope == "" {
		params.Scope = "project"
	}

	v, err := getVault()
	if err != nil {
		return "", fmt.Errorf("vault not available: %w", err)
	}

	ciphertext, err := v.Encrypt(params.Value)
	if err != nil {
		return "", fmt.Errorf("encrypt credential: %w", err)
	}

	// Expand hierarchical tags: "lang:go" → ["lang", "lang:go"]
	seen := make(map[string]bool)
	var concepts []string
	for _, tag := range params.Tags {
		parts := expandTagHierarchy(tag)
		for _, p := range parts {
			if !seen[p] {
				seen[p] = true
				concepts = append(concepts, p)
			}
		}
	}

	scope := models.ObservationScope(params.Scope)
	obs := &models.ParsedObservation{
		Type:                     models.ObsTypeCredential,
		SourceType:               models.SourceManual,
		Title:                    params.Name,
		Narrative:                params.Name,
		Concepts:                 concepts,
		Scope:                    scope,
		EncryptedSecret:          ciphertext,
		EncryptionKeyFingerprint: v.Fingerprint(),
	}

	id, _, err := s.observationStore.StoreObservation(ctx, "", params.Project, obs, 0, 0)
	if err != nil {
		return "", fmt.Errorf("store credential observation: %w", err)
	}

	result := map[string]any{
		"id":      id,
		"name":    params.Name,
		"scope":   string(scope),
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
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	var params struct {
		Name    string `json:"name"`
		Project string `json:"project"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if params.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	v, err := getVault()
	if err != nil {
		return "", fmt.Errorf("vault not available — configure ENGRAM_ENCRYPTION_KEY or ENGRAM_ENCRYPTION_KEY_FILE: %w", err)
	}

	cred, err := s.observationStore.GetCredential(ctx, params.Name, params.Project)
	if err != nil {
		return "", fmt.Errorf("get credential: %w", err)
	}
	if cred == nil {
		return "", fmt.Errorf("credential %q not found", params.Name)
	}

	// Verify key fingerprint before decryption to detect key mismatch early.
	if cred.EncryptionKeyFingerprint.Valid && cred.EncryptionKeyFingerprint.String != "" {
		if !v.MatchesFingerprint(cred.EncryptionKeyFingerprint.String) {
			return "", fmt.Errorf(
				"encryption key mismatch: credential %q was encrypted with key fingerprint %q, current key has fingerprint %q — restore the original key to decrypt",
				params.Name, cred.EncryptionKeyFingerprint.String, v.Fingerprint(),
			)
		}
	}

	plaintext, err := v.Decrypt(cred.EncryptedSecret)
	if err != nil {
		return "", fmt.Errorf("decrypt credential %q: %w", params.Name, err)
	}

	result := map[string]any{
		"name":  params.Name,
		"value": plaintext,
		"scope": string(cred.Scope),
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleListCredentials lists credential names and metadata (no values).
func (s *Server) handleListCredentials(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	var params struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	creds, err := s.observationStore.ListCredentials(ctx, params.Project)
	if err != nil {
		return "", fmt.Errorf("list credentials: %w", err)
	}

	type credItem struct {
		Concepts []string `json:"concepts,omitempty"`
		Name     string   `json:"name"`
		Scope    string   `json:"scope"`
		ID       int64    `json:"id"`
	}
	items := make([]credItem, 0, len(creds))
	for _, c := range creds {
		name := ""
		if c.Title.Valid {
			name = c.Title.String
		} else if c.Narrative.Valid {
			name = c.Narrative.String
		}
		items = append(items, credItem{
			ID:       c.ID,
			Name:     name,
			Scope:    string(c.Scope),
			Concepts: []string(c.Concepts),
		})
	}

	out, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleDeleteCredential removes a credential by name.
func (s *Server) handleDeleteCredential(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	var params struct {
		Name    string `json:"name"`
		Project string `json:"project"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if params.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	if err := s.observationStore.DeleteCredential(ctx, params.Name, params.Project); err != nil {
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
func (s *Server) handleVaultStatus(ctx context.Context, _ json.RawMessage) (string, error) {
	v, vErr := getVault()

	keyConfigured := vErr == nil && v != nil
	fingerprint := ""
	if keyConfigured {
		fingerprint = v.Fingerprint()
	}

	count := 0
	if s.observationStore != nil {
		if n, err := s.observationStore.CountCredentials(ctx); err == nil {
			count = int(n)
		}
	}

	result := map[string]any{
		"key_configured":   keyConfigured,
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
