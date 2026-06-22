package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/models"
)

const principalMemoryQueryMaxLimit = principalmemory.MaxPrincipalQueryLimit

type principalMemoryQueryService interface {
	Query(ctx context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error)
}

type principalMemoryQueryResponse struct {
	Principal     string                                    `json:"principal"`
	PrincipalKind string                                    `json:"principal_kind"`
	Project       string                                    `json:"project,omitempty"`
	Domain        string                                    `json:"domain,omitempty"`
	Items         []principalMemoryQueryItem                `json:"items"`
	HiddenCount   int                                       `json:"hidden_count"`
	Audit         principalmemory.PrincipalMemoryQueryAudit `json:"audit"`
	AuditStatus   string                                    `json:"audit_status"`
}

type principalMemoryQueryItem struct {
	ID                 int64     `json:"id"`
	Project            string    `json:"project"`
	Content            string    `json:"content"`
	Tags               []string  `json:"tags"`
	OwnerPrincipal     string    `json:"owner_principal"`
	OwnerPrincipalKind string    `json:"owner_principal_kind"`
	AgentVisibility    string    `json:"agent_visibility"`
	Domain             string    `json:"domain"`
	Confidence         float64   `json:"confidence"`
	CreatedAt          time.Time `json:"created_at"`
}

func (s *Service) handlePrincipalMemoryQuery(w http.ResponseWriter, r *http.Request) {
	queryService := s.currentPrincipalMemoryQueryService()
	if queryService == nil {
		http.Error(w, "principal memory query service not available", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	ownerPrincipal := strings.TrimSpace(q.Get("principal"))
	if ownerPrincipal == "" {
		http.Error(w, "principal is required", http.StatusBadRequest)
		return
	}

	ownerPrincipalKind := strings.TrimSpace(strings.ToLower(q.Get("principal_kind")))
	if ownerPrincipalKind == "" {
		ownerPrincipalKind = "human"
	}
	if !auth.IsValidPrincipalKind(auth.PrincipalKind(ownerPrincipalKind)) {
		http.Error(w, "principal_kind must be one of human, agent, service", http.StatusBadRequest)
		return
	}

	visibility, err := parsePrincipalQueryVisibility(q.Get("visibility"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit, err := parsePrincipalQueryLimit(q.Get("limit"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	offset, err := parsePrincipalQueryOffset(q.Get("offset"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	includePrivate, err := parseOptionalBool(q.Get("include_private"))
	if err != nil {
		http.Error(w, "include_private must be a boolean", http.StatusBadRequest)
		return
	}

	caller, callerIsAdmin := principalQueryCaller(r.Context())
	result, err := queryService.Query(r.Context(), principalmemory.PrincipalMemoryQueryRequest{
		Project:            strings.TrimSpace(q.Get("project")),
		Caller:             caller,
		CallerIsAdmin:      callerIsAdmin,
		OwnerPrincipal:     ownerPrincipal,
		OwnerPrincipalKind: ownerPrincipalKind,
		Query:              strings.TrimSpace(q.Get("q")),
		AgentVisibility:    visibility,
		IncludePrivate:     includePrivate,
		Domain:             strings.TrimSpace(q.Get("domain")),
		Limit:              limit,
		Offset:             offset,
		SourceSessionID:    strings.TrimSpace(q.Get("session_id")),
	})
	if err != nil {
		if errors.Is(err, principalmemory.ErrCrossPrincipalPrivateDenied) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		http.Error(w, "principal memory query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, principalMemoryQueryHTTPResponse(result))
}

func (s *Service) currentPrincipalMemoryQueryService() principalMemoryQueryService {
	s.initMu.RLock()
	queryService := s.principalMemoryQueryService
	s.initMu.RUnlock()
	return queryService
}

func principalQueryCaller(ctx context.Context) (principalmemory.PrincipalRef, bool) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return principalmemory.PrincipalRef{}, false
	}
	principal, kind, hasOwner := id.MemoryOwner()
	if !hasOwner {
		return principalmemory.PrincipalRef{}, id.IsAdmin()
	}
	return principalmemory.PrincipalRef{Principal: principal, PrincipalKind: kind}, id.IsAdmin()
}

func parsePrincipalQueryLimit(raw string) (int, error) {
	if raw == "" {
		return principalmemory.DefaultPrincipalQueryLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if n > principalMemoryQueryMaxLimit {
		return principalMemoryQueryMaxLimit, nil
	}
	return n, nil
}

func parsePrincipalQueryOffset(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("offset must be a non-negative integer")
	}
	return n, nil
}

func parseOptionalBool(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	return strconv.ParseBool(raw)
}

func parsePrincipalQueryVisibility(raw string) (string, error) {
	visibility := strings.TrimSpace(strings.ToLower(raw))
	switch visibility {
	case "", "all":
		return "", nil
	case models.AgentVisibilityPrivate, models.AgentVisibilityShared:
		return visibility, nil
	default:
		return "", fmt.Errorf("visibility must be one of private, shared, all")
	}
}

func principalMemoryQueryHTTPResponse(result *principalmemory.PrincipalMemoryQueryResult) principalMemoryQueryResponse {
	resp := principalMemoryQueryResponse{
		Items:       make([]principalMemoryQueryItem, 0),
		AuditStatus: principalmemory.AuditStatusNotRequired,
		Audit: principalmemory.PrincipalMemoryQueryAudit{
			Action: principalmemory.AuditActionQuery,
		},
	}
	if result == nil {
		return resp
	}
	resp.Principal = result.Principal
	resp.PrincipalKind = result.PrincipalKind
	resp.Project = result.Project
	resp.Domain = result.Domain
	resp.HiddenCount = result.HiddenCount
	resp.AuditStatus = result.AuditStatus
	resp.Audit = result.Audit
	if resp.Audit.Action == "" {
		resp.Audit.Action = principalmemory.AuditActionQuery
	}
	resp.Items = make([]principalMemoryQueryItem, 0, len(result.Items))
	for _, item := range result.Items {
		resp.Items = append(resp.Items, principalMemoryQueryItem{
			ID:                 item.ID,
			Project:            item.Project,
			Content:            item.Content,
			Tags:               append([]string(nil), item.Tags...),
			OwnerPrincipal:     item.OwnerPrincipal,
			OwnerPrincipalKind: item.OwnerPrincipalKind,
			AgentVisibility:    item.AgentVisibility,
			Domain:             item.Domain,
			Confidence:         item.Confidence,
			CreatedAt:          item.CreatedAt,
		})
	}
	return resp
}
