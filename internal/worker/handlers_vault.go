// Package worker provides vault credential REST handlers for the dashboard.
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/pkg/models"
)

// storeCredentialRequest is the JSON body for POST /api/vault/credentials.
type storeCredentialRequest struct {
	Name    string   `json:"name"`
	Value   string   `json:"value"`
	Scope   string   `json:"scope"`
	Project string   `json:"project"`
	Tags    []string `json:"tags,omitempty"`
}

// handleListCredentials godoc
// @Summary List vault credentials
// @Description Returns all stored credentials with metadata (name, scope, created) but NOT decrypted values.
// @Tags Vault
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Success 200 {array} object
// @Failure 500 {string} string "internal error"
// @Router /api/vault/credentials [get]
func (s *Service) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if s.credentialStore == nil {
		http.Error(w, "credential store not available", http.StatusServiceUnavailable)
		return
	}

	project := r.URL.Query().Get("project")

	type credItem struct {
		Name      string `json:"name"`
		Scope     string `json:"scope"`
		Project   string `json:"project,omitempty"`
		CreatedAt string `json:"created_at,omitempty"`
		ID        int64  `json:"id"`
	}

	// When no project specified, list ALL credentials (dashboard admin view).
	if project == "" {
		allCreds, err := s.credentialStore.ListAll(r.Context())
		if err != nil {
			log.Error().Err(err).Msg("list all credentials failed")
			http.Error(w, "failed to list credentials", http.StatusInternalServerError)
			return
		}
		items := make([]credItem, 0, len(allCreds))
		for _, c := range allCreds {
			items = append(items, credItem{
				Name:      c.Key,
				Scope:     c.Scope,
				Project:   c.Project,
				ID:        c.ID,
				CreatedAt: c.CreatedAt.Format(time.RFC3339),
			})
		}
		writeJSON(w, items)
		return
	}

	creds, err := s.credentialStore.List(r.Context(), project)
	if err != nil {
		log.Error().Err(err).Msg("list credentials failed")
		http.Error(w, "failed to list credentials", http.StatusInternalServerError)
		return
	}
	globalCreds, err := s.credentialStore.List(r.Context(), "")
	if err != nil {
		log.Error().Err(err).Msg("list global credentials failed")
		http.Error(w, "failed to list credentials", http.StatusInternalServerError)
		return
	}
	creds = append(creds, globalCreds...)

	items := make([]credItem, 0, len(creds))
	for _, c := range creds {
		items = append(items, credItem{
			ID:        c.ID,
			Name:      c.Key,
			Scope:     c.Scope,
			Project:   c.Project,
			CreatedAt: c.CreatedAt.Format(time.RFC3339),
		})
	}

	writeJSON(w, items)
}

// handleGetCredential godoc
// @Summary Get credential with decrypted value
// @Description Retrieves and decrypts a credential by name. Verifies key fingerprint before decryption.
// @Tags Vault
// @Produce json
// @Security ApiKeyAuth
// @Param name path string true "Credential name"
// @Param project query string false "Project scope"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 404 {string} string "not found"
// @Failure 500 {string} string "internal error"
// @Router /api/vault/credentials/{name} [get]
func (s *Service) handleGetCredential(w http.ResponseWriter, r *http.Request) {
	if s.credentialStore == nil {
		http.Error(w, "credential store not available", http.StatusServiceUnavailable)
		return
	}

	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "credential name is required", http.StatusBadRequest)
		return
	}
	project := r.URL.Query().Get("project")

	v, err := s.getVault()
	if err != nil {
		log.Error().Err(err).Msg("vault not available")
		http.Error(w, "vault not available", http.StatusInternalServerError)
		return
	}

	var cred *models.Credential
	if project != "" {
		cred, err = s.credentialStore.Get(r.Context(), project, name)
	} else {
		// Dashboard admin view: caller doesn't know the project — find by name.
		cred, err = s.credentialStore.GetByName(r.Context(), name)
	}
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "credential not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("name", name).Msg("get credential failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Verify key fingerprint before decryption to detect key mismatch early.
	if cred.EncryptionKeyFingerprint != "" {
		if !v.MatchesFingerprint(cred.EncryptionKeyFingerprint) {
			http.Error(w, "encryption key mismatch: credential was encrypted with a different key", http.StatusConflict)
			return
		}
	}

	plaintext, err := v.Decrypt(cred.EncryptedSecret)
	if err != nil {
		log.Error().Err(err).Str("name", name).Msg("decrypt credential failed")
		http.Error(w, "failed to decrypt credential", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"name":  name,
		"value": plaintext,
		"scope": cred.Scope,
	})
}

// handleStoreCredential godoc
// @Summary Store a new credential
// @Description Encrypts and stores a credential using AES-256-GCM vault encryption.
// @Tags Vault
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body storeCredentialRequest true "Credential to store"
// @Success 201 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/vault/credentials [post]
func (s *Service) handleStoreCredential(w http.ResponseWriter, r *http.Request) {
	if s.credentialStore == nil {
		http.Error(w, "credential store not available", http.StatusServiceUnavailable)
		return
	}

	var req storeCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if req.Value == "" {
		http.Error(w, "value is required", http.StatusBadRequest)
		return
	}
	if req.Scope == "" {
		req.Scope = "project"
	}
	switch req.Scope {
	case "project", "global":
		// valid
	default:
		http.Error(w, "invalid scope: must be \"project\" or \"global\"", http.StatusBadRequest)
		return
	}
	if req.Scope == "global" {
		req.Project = ""
	} else if req.Project == "" {
		http.Error(w, "project is required for project-scoped credentials", http.StatusBadRequest)
		return
	}

	v, err := s.getVault()
	if err != nil {
		log.Error().Err(err).Msg("vault not available")
		http.Error(w, "vault not available", http.StatusInternalServerError)
		return
	}

	ciphertext, err := v.Encrypt(req.Value)
	if err != nil {
		log.Error().Err(err).Msg("encrypt credential failed")
		http.Error(w, "failed to encrypt credential", http.StatusInternalServerError)
		return
	}

	cred := &models.Credential{
		Project:                  req.Project,
		Key:                      req.Name,
		EncryptedSecret:          ciphertext,
		EncryptionKeyFingerprint: v.Fingerprint(),
		Scope:                    req.Scope,
		EditedBy:                 "api",
	}
	created, err := s.credentialStore.Create(r.Context(), cred)
	if err != nil {
		log.Error().Err(err).Msg("store credential failed")
		http.Error(w, "failed to store credential", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{
		"id":      created.ID,
		"name":    req.Name,
		"scope":   req.Scope,
		"message": "Credential stored successfully",
	})
}

// handleDeleteCredential godoc
// @Summary Delete a credential
// @Description Removes a credential by name and optional project/scope filter.
// @Tags Vault
// @Produce json
// @Security ApiKeyAuth
// @Param name path string true "Credential name"
// @Param project query string false "Project scope"
// @Param scope query string false "Scope filter (project or global)" default(project)
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 404 {string} string "not found"
// @Failure 500 {string} string "internal error"
// @Router /api/vault/credentials/{name} [delete]
func (s *Service) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if s.credentialStore == nil {
		http.Error(w, "credential store not available", http.StatusServiceUnavailable)
		return
	}

	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "credential name is required", http.StatusBadRequest)
		return
	}

	project := r.URL.Query().Get("project")

	var err error
	if project != "" {
		err = s.credentialStore.Delete(r.Context(), project, name)
	} else {
		// Dashboard admin view: caller doesn't know the project — find and delete by name.
		err = s.credentialStore.DeleteByName(r.Context(), name)
	}
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "credential not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("name", name).Msg("delete credential failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"deleted": true,
		"name":    name,
	})
}

// handleDeleteOrphanedCredentials godoc
// @Summary Delete orphaned credentials
// @Description Removes credentials encrypted with a key that no longer matches the current vault key fingerprint.
// @Tags Vault
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "vault not configured"
// @Router /api/vault/orphaned-credentials [delete]
func (s *Service) handleDeleteOrphanedCredentials(w http.ResponseWriter, r *http.Request) {
	v, err := s.getVault()
	if err != nil || v == nil {
		http.Error(w, "vault not configured", http.StatusServiceUnavailable)
		return
	}

	fingerprint := v.Fingerprint()
	if fingerprint == "" {
		http.Error(w, "vault key fingerprint not available", http.StatusServiceUnavailable)
		return
	}

	if s.credentialStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	deleted, err := s.credentialStore.DeleteOrphanedByFingerprint(r.Context(), fingerprint)
	if err != nil {
		http.Error(w, "failed to delete orphaned credentials", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"status":  "ok",
		"deleted": deleted,
	})
}

// handleVaultStatus godoc
// @Summary Get vault encryption status
// @Description Returns vault key configuration status, fingerprint, and credential count.
// @Tags Vault
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} object
// @Failure 500 {string} string "internal error"
// @Router /api/vault/status [get]
func (s *Service) handleVaultStatus(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	keyConfigured := crypto.VaultExists(cfg)
	fingerprint := ""
	keySource := ""

	// Safe to call getVault() when key IS configured (env var or file exists) —
	// it will load the explicit key, not auto-generate.
	// Only skip when !keyConfigured to prevent auto-generation side effect.
	if keyConfigured {
		if v, err := s.getVault(); err == nil && v != nil {
			fingerprint = v.Fingerprint()
			keySource = v.KeySource()
		}
	}

	count := 0
	if s.credentialStore != nil {
		if n, err := s.credentialStore.CountCredentials(r.Context()); err == nil {
			count = int(n)
		}
	}

	// Check for fingerprint mismatch: credentials encrypted with a different key.
	mismatchCount := 0
	if fingerprint != "" && s.credentialStore != nil {
		if n, err := s.credentialStore.CountWithDifferentFingerprint(r.Context(), fingerprint); err == nil {
			mismatchCount = int(n)
		}
	}

	resp := map[string]any{
		"key_configured":   keyConfigured,
		"key_source":       keySource,
		"fingerprint":      fingerprint,
		"credential_count": count,
		"backup_reminder":  "Back up vault.key (or set ENGRAM_VAULT_KEY) — losing this key makes stored credentials unrecoverable",
	}
	if mismatchCount > 0 {
		resp["mismatch_warning"] = fmt.Sprintf("%d credential(s) encrypted with a different key — they cannot be decrypted with the current key", mismatchCount)
		resp["mismatch_count"] = mismatchCount
	}
	writeJSON(w, resp)
}
