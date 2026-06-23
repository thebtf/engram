package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	gormlib "gorm.io/gorm"

	authpkg "github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
)

type domainOwnerStore interface {
	List(ctx context.Context, opts gormdb.DomainOwnerListOptions) ([]*gormdb.DomainOwner, error)
	Upsert(ctx context.Context, in *gormdb.DomainOwner) (*gormdb.DomainOwner, error)
	Delete(ctx context.Context, domain string) error
}

type domainRegistryService interface {
	CheckWrite(ctx context.Context, req principalmemory.DomainWriteCheckRequest) (*principalmemory.DomainWriteDecision, error)
}

type memoryDomainUpsertRequest struct {
	OwnerPrincipal     string `json:"owner_principal"`
	OwnerPrincipalKind string `json:"owner_principal_kind"`
	Mode               string `json:"mode"`
}

type memoryDomainResponse struct {
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Domain             string    `json:"domain"`
	OwnerPrincipal     string    `json:"owner_principal"`
	OwnerPrincipalKind string    `json:"owner_principal_kind"`
	Mode               string    `json:"mode"`
}

type memoryDomainsListResponse struct {
	Domains []memoryDomainResponse `json:"domains"`
}

func (s *Service) handleListMemoryDomains(w http.ResponseWriter, r *http.Request) {
	if rejectNonAdmin(w, r) {
		return
	}

	store := s.currentDomainOwnerStore()
	if store == nil {
		http.Error(w, "domain owner store not available", http.StatusServiceUnavailable)
		return
	}

	opts, err := parseMemoryDomainListOptions(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := store.List(r.Context(), opts)
	if err != nil {
		http.Error(w, "list memory domains failed", http.StatusInternalServerError)
		return
	}
	resp := memoryDomainsListResponse{Domains: make([]memoryDomainResponse, 0, len(rows))}
	for _, row := range rows {
		resp.Domains = append(resp.Domains, memoryDomainHTTPResponse(row))
	}
	writeJSON(w, resp)
}

func (s *Service) handleUpsertMemoryDomain(w http.ResponseWriter, r *http.Request) {
	if rejectNonAdmin(w, r) {
		return
	}

	store := s.currentDomainOwnerStore()
	if store == nil {
		http.Error(w, "domain owner store not available", http.StatusServiceUnavailable)
		return
	}

	domain := strings.TrimSpace(chi.URLParam(r, "domain"))
	var req memoryDomainUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	row, err := store.Upsert(r.Context(), &gormdb.DomainOwner{
		Domain:             domain,
		OwnerPrincipal:     strings.TrimSpace(req.OwnerPrincipal),
		OwnerPrincipalKind: strings.TrimSpace(strings.ToLower(req.OwnerPrincipalKind)),
		Mode:               strings.TrimSpace(strings.ToLower(req.Mode)),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, memoryDomainHTTPResponse(row))
}

func (s *Service) handleDeleteMemoryDomain(w http.ResponseWriter, r *http.Request) {
	if rejectNonAdmin(w, r) {
		return
	}

	store := s.currentDomainOwnerStore()
	if store == nil {
		http.Error(w, "domain owner store not available", http.StatusServiceUnavailable)
		return
	}

	domain := strings.TrimSpace(chi.URLParam(r, "domain"))
	if domain == "" {
		http.Error(w, "domain must not be empty", http.StatusBadRequest)
		return
	}
	if err := store.Delete(r.Context(), domain); err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "domain owner not found", http.StatusNotFound)
			return
		}
		http.Error(w, "delete memory domain failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"deleted": true,
		"domain":  domain,
	})
}

func (s *Service) currentDomainOwnerStore() domainOwnerStore {
	s.initMu.RLock()
	store := s.domainOwnerStore
	s.initMu.RUnlock()
	return store
}

func rejectNonAdmin(w http.ResponseWriter, r *http.Request) bool {
	id, ok := authpkg.IdentityFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	}
	if !id.IsAdmin() {
		http.Error(w, "admin authorization required", http.StatusForbidden)
		return true
	}
	return false
}

func parseMemoryDomainListOptions(r *http.Request) (gormdb.DomainOwnerListOptions, error) {
	q := r.URL.Query()
	limit, err := parseOptionalPositiveInt(q.Get("limit"), 100)
	if err != nil {
		return gormdb.DomainOwnerListOptions{}, fmt.Errorf("limit must be a positive integer")
	}
	offset, err := parseOptionalNonNegativeInt(q.Get("offset"), 0)
	if err != nil {
		return gormdb.DomainOwnerListOptions{}, fmt.Errorf("offset must be a non-negative integer")
	}
	return gormdb.DomainOwnerListOptions{
		Domain:             strings.TrimSpace(q.Get("domain")),
		OwnerPrincipal:     strings.TrimSpace(q.Get("owner_principal")),
		OwnerPrincipalKind: strings.TrimSpace(strings.ToLower(q.Get("owner_principal_kind"))),
		Mode:               strings.TrimSpace(strings.ToLower(q.Get("mode"))),
		Limit:              limit,
		Offset:             offset,
	}, nil
}

func parseOptionalPositiveInt(raw string, fallback int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid positive integer")
	}
	return n, nil
}

func parseOptionalNonNegativeInt(raw string, fallback int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid non-negative integer")
	}
	return n, nil
}

func memoryDomainHTTPResponse(row *gormdb.DomainOwner) memoryDomainResponse {
	if row == nil {
		return memoryDomainResponse{}
	}
	return memoryDomainResponse{
		Domain:             row.Domain,
		OwnerPrincipal:     row.OwnerPrincipal,
		OwnerPrincipalKind: row.OwnerPrincipalKind,
		Mode:               row.Mode,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}
