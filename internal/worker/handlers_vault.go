// Package worker provides vault credential REST handlers for the dashboard.
package worker

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
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
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

	project := r.URL.Query().Get("project")

	creds, err := s.observationStore.ListCredentials(r.Context(), project)
	if err != nil {
		log.Error().Err(err).Msg("list credentials failed")
		http.Error(w, "list credentials: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type credItem struct {
		Concepts  []string `json:"concepts,omitempty"`
		Name      string   `json:"name"`
		Scope     string   `json:"scope"`
		CreatedAt string   `json:"created_at"`
		ID        int64    `json:"id"`
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
			ID:        c.ID,
			Name:      name,
			Scope:     string(c.Scope),
			CreatedAt: c.CreatedAt,
			Concepts:  []string(c.Concepts),
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
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
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
		http.Error(w, "vault not available: "+err.Error(), http.StatusInternalServerError)
		return
	}

	cred, err := s.observationStore.GetCredential(r.Context(), name, project)
	if err != nil {
		log.Error().Err(err).Str("name", name).Msg("get credential failed")
		http.Error(w, "get credential: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if cred == nil {
		http.Error(w, "credential not found", http.StatusNotFound)
		return
	}

	// Verify key fingerprint before decryption to detect key mismatch early.
	if cred.EncryptionKeyFingerprint.Valid && cred.EncryptionKeyFingerprint.String != "" {
		if !v.MatchesFingerprint(cred.EncryptionKeyFingerprint.String) {
			http.Error(w, "encryption key mismatch: credential was encrypted with a different key", http.StatusConflict)
			return
		}
	}

	plaintext, err := v.Decrypt(cred.EncryptedSecret)
	if err != nil {
		log.Error().Err(err).Str("name", name).Msg("decrypt credential failed")
		http.Error(w, "decrypt credential: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"name":  name,
		"value": plaintext,
		"scope": string(cred.Scope),
	})
}

// handleStoreCredential godoc
// @Summary Store a new credential
// @Description Encrypts and stores a credential observation using AES-256-GCM vault encryption.
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
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
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
	if req.Scope == "project" && req.Project == "" {
		http.Error(w, "project is required for project-scoped credentials", http.StatusBadRequest)
		return
	}

	v, err := s.getVault()
	if err != nil {
		http.Error(w, "vault not available: "+err.Error(), http.StatusInternalServerError)
		return
	}

	ciphertext, err := v.Encrypt(req.Value)
	if err != nil {
		log.Error().Err(err).Msg("encrypt credential failed")
		http.Error(w, "encrypt credential: "+err.Error(), http.StatusInternalServerError)
		return
	}

	scope := models.ObservationScope(req.Scope)
	obs := &models.ParsedObservation{
		Type:                     models.ObsTypeCredential,
		SourceType:               models.SourceManual,
		Title:                    req.Name,
		Narrative:                req.Name,
		Concepts:                 req.Tags,
		Scope:                    scope,
		EncryptedSecret:          ciphertext,
		EncryptionKeyFingerprint: v.Fingerprint(),
	}

	const vaultSessionID = "credential:vault"
	id, _, err := s.observationStore.StoreObservation(r.Context(), vaultSessionID, req.Project, obs, 0, 0)
	if err != nil {
		log.Error().Err(err).Msg("store credential observation failed")
		http.Error(w, "store credential: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{
		"id":      id,
		"name":    req.Name,
		"scope":   string(scope),
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
// @Failure 500 {string} string "internal error"
// @Router /api/vault/credentials/{name} [delete]
func (s *Service) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "credential name is required", http.StatusBadRequest)
		return
	}

	project := r.URL.Query().Get("project")
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "project"
	}
	switch scope {
	case "project", "global":
		// valid
	default:
		http.Error(w, "invalid scope: must be \"project\" or \"global\"", http.StatusBadRequest)
		return
	}
	if scope == "project" && project == "" {
		http.Error(w, "project is required for project-scoped credentials", http.StatusBadRequest)
		return
	}

	if err := s.observationStore.DeleteCredential(r.Context(), name, project, scope); err != nil {
		log.Error().Err(err).Str("name", name).Msg("delete credential failed")
		http.Error(w, "delete credential: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"deleted": true,
		"name":    name,
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
	if s.observationStore != nil {
		if n, err := s.observationStore.CountCredentials(r.Context()); err == nil {
			count = int(n)
		}
	}

	writeJSON(w, map[string]any{
		"key_configured":   keyConfigured,
		"key_source":       keySource,
		"fingerprint":      fingerprint,
		"credential_count": count,
		"backup_reminder":  "Back up vault.key (or set ENGRAM_ENCRYPTION_KEY) — losing this key makes stored credentials unrecoverable",
	})
}
