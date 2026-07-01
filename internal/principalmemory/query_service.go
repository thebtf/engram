package principalmemory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

const (
	AuditStatusNotRequired = "not_required"
	AuditStatusWritten     = "written"
	AuditActionQuery       = "principal_memory_query"

	DefaultPrincipalQueryLimit = 50
	MaxPrincipalQueryLimit     = 500
)

var ErrCrossPrincipalPrivateDenied = errors.New("include_private for another principal requires admin")

// PrincipalMemoryStore is the storage seam used by the query service.
type PrincipalMemoryStore interface {
	ListPrincipalMemory(ctx context.Context, project string, opts gormdb.ListOptions) ([]*models.Memory, error)
}

// AuditLogger is intentionally tiny so tests and future stores can enforce the same fail-closed contract.
type AuditLogger interface {
	Log(ctx context.Context, entry gormdb.AuditLogEntry) error
}

type PrincipalMemoryQueryService struct {
	store  PrincipalMemoryStore
	audit  AuditLogger
	policy PrincipalAccessPolicy
}

func NewPrincipalMemoryQueryService(store PrincipalMemoryStore, audit AuditLogger) *PrincipalMemoryQueryService {
	return &PrincipalMemoryQueryService{
		store:  store,
		audit:  audit,
		policy: PrincipalAccessPolicy{},
	}
}

type PrincipalMemoryQueryRequest struct {
	Project            string
	Caller             PrincipalRef
	CallerIsAdmin      bool
	OwnerPrincipal     string
	OwnerPrincipalKind string
	Query              string
	// QueryTerms, when non-empty, are ORed as case-insensitive content-substring
	// predicates at the SQL layer (relevance narrowing without the full-phrase
	// Query cliff or an unfiltered newest-N recency cliff). ADDITIVE to the
	// access-policy filters. When empty the fetch stays bounded by Limit only.
	QueryTerms []string
	// IDs, when non-empty, restricts the projection to the given memory IDs via an
	// additive WHERE id IN (...). Used by the experience detail-by-id path so a
	// specific memory:<id> lookup fetches that exact row under the same
	// owner/kind/visibility/domain access-policy gating (NFR-1 preserved).
	IDs                []int64
	AgentVisibility    string
	IncludePrivate     bool
	Domain             string
	Limit              int
	Offset             int
	SourceSessionID    string
}

// normalizeQueryTerms trims, lower-cases, and de-duplicates OR-narrowing content
// terms, dropping empties. Empty input (or all-empty terms) yields nil so the
// store falls back to the bounded newest-N fetch instead of an empty OR clause.
func normalizeQueryTerms(terms []string) []string {
	if len(terms) == 0 {
		return nil
	}
	out := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		t := strings.ToLower(strings.TrimSpace(term))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type PrincipalMemoryQueryResult struct {
	Principal     string                     `json:"principal"`
	PrincipalKind string                     `json:"principal_kind"`
	Project       string                     `json:"project,omitempty"`
	Domain        string                     `json:"domain,omitempty"`
	Items         []PrincipalMemoryQueryItem `json:"items"`
	HiddenCount   int                        `json:"hidden_count"`
	Audit         PrincipalMemoryQueryAudit  `json:"audit"`
	AuditStatus   string                     `json:"audit_status"`
}

type PrincipalMemoryQueryAudit struct {
	Durable bool   `json:"durable"`
	Action  string `json:"action"`
}

type PrincipalMemoryQueryItem struct {
	ID                 int64     `json:"id"`
	Project            string    `json:"project"`
	Content            string    `json:"content"`
	Tags               []string  `json:"tags"`
	Status             string    `json:"status,omitempty"`
	Tier               string    `json:"tier,omitempty"`
	SourceAgent        string    `json:"source_agent,omitempty"`
	OwnerPrincipal     string    `json:"owner_principal"`
	OwnerPrincipalKind string    `json:"owner_principal_kind"`
	AgentVisibility    string    `json:"agent_visibility"`
	Domain             string    `json:"domain"`
	Confidence         float64   `json:"confidence"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Version            int       `json:"version"`
	memory             *models.Memory
}

func (item PrincipalMemoryQueryItem) Memory() *models.Memory {
	if item.memory != nil {
		return cloneMemory(item.memory)
	}
	return &models.Memory{
		ID:                 item.ID,
		Project:            item.Project,
		Content:            item.Content,
		Tags:               models.JSONStringArray(append([]string(nil), item.Tags...)),
		Status:             item.Status,
		Tier:               item.Tier,
		SourceAgent:        item.SourceAgent,
		OwnerPrincipal:     item.OwnerPrincipal,
		OwnerPrincipalKind: item.OwnerPrincipalKind,
		AgentVisibility:    item.AgentVisibility,
		Domain:             item.Domain,
		Confidence:         item.Confidence,
		CreatedAt:          item.CreatedAt,
		UpdatedAt:          item.UpdatedAt,
		Version:            item.Version,
	}
}

func (s *PrincipalMemoryQueryService) Query(ctx context.Context, req PrincipalMemoryQueryRequest) (*PrincipalMemoryQueryResult, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("principal memory query store is not configured")
	}

	limit := normalizePrincipalQueryLimit(req.Limit)
	if req.IncludePrivate {
		decision := s.policy.Decide(PrincipalAccessRequest{
			Caller:        req.Caller,
			Target:        PrincipalRef{Principal: req.OwnerPrincipal, PrincipalKind: req.OwnerPrincipalKind},
			Visibility:    models.AgentVisibilityPrivate,
			CallerIsAdmin: req.CallerIsAdmin,
		})
		if !decision.Allowed {
			if decision.Reason == ReasonCrossPrincipalPrivateDenied {
				return nil, ErrCrossPrincipalPrivateDenied
			}
			return nil, fmt.Errorf("include_private denied: %s", decision.Reason)
		}
	}

	queryOpts := gormdb.ListOptions{
		OwnerPrincipal:     strings.TrimSpace(req.OwnerPrincipal),
		OwnerPrincipalKind: strings.TrimSpace(strings.ToLower(req.OwnerPrincipalKind)),
		ContentContains:    strings.TrimSpace(req.Query),
		ContentContainsAny: normalizeQueryTerms(req.QueryTerms),
		IDs:                req.IDs,
		AgentVisibility:    strings.TrimSpace(req.AgentVisibility),
		Domain:             strings.TrimSpace(req.Domain),
		Limit:              principalQueryFetchLimit(limit),
	}
	result := principalMemoryQueryResult(req, limit)
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	for len(result.Items) < limit {
		queryOpts.Offset = offset
		rows, err := s.store.ListPrincipalMemory(ctx, strings.TrimSpace(req.Project), queryOpts)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, mem := range rows {
			if mem == nil {
				continue
			}
			decision := s.policy.Decide(PrincipalAccessRequest{
				Caller:        req.Caller,
				Target:        PrincipalRef{Principal: mem.OwnerPrincipal, PrincipalKind: mem.OwnerPrincipalKind},
				Visibility:    mem.AgentVisibility,
				CallerIsAdmin: req.CallerIsAdmin,
			})
			if !decision.Allowed {
				result.HiddenCount++
				continue
			}
			if !s.domainReadAllowed(req, mem) {
				result.HiddenCount++
				continue
			}
			if decision.AuditRequired {
				if !req.IncludePrivate {
					result.HiddenCount++
					continue
				}
				if err := s.logPrivateRead(ctx, req, mem); err != nil {
					return nil, err
				}
				result.AuditStatus = AuditStatusWritten
				result.Audit.Durable = true
			}
			result.Items = append(result.Items, principalMemoryQueryItem(mem))
			if len(result.Items) >= limit {
				break
			}
		}
		offset += len(rows)
		if len(rows) < queryOpts.Limit {
			break
		}
	}
	return result, nil
}

func (s *PrincipalMemoryQueryService) domainReadAllowed(req PrincipalMemoryQueryRequest, mem *models.Memory) bool {
	if mem == nil {
		return false
	}
	caller := normalizePrincipalRef(req.Caller)
	return scope.DomainOwnershipPolicy{}.Decide(scope.KeycardContext{
		Principal:     caller.Principal,
		PrincipalKind: caller.PrincipalKind,
	}, scope.DomainPolicyRequest{
		Operation:          scope.DomainOperationRead,
		Domain:             mem.Domain,
		OwnerPrincipal:     mem.OwnerPrincipal,
		OwnerPrincipalKind: mem.OwnerPrincipalKind,
	}).Allowed
}

func (s *PrincipalMemoryQueryService) logPrivateRead(ctx context.Context, req PrincipalMemoryQueryRequest, mem *models.Memory) error {
	if s.audit == nil {
		return fmt.Errorf("principal private widening requires audit logger")
	}
	memoryID := mem.ID
	entry := gormdb.AuditLogEntry{
		MemoryID:        &memoryID,
		Action:          "principal_memory_private_read",
		Actor:           strings.TrimSpace(req.Caller.Principal),
		SourceSessionID: strings.TrimSpace(req.SourceSessionID),
		Reason:          "admin_private_widening",
	}
	if entry.Actor == "" {
		entry.Actor = "system"
	}
	if err := s.audit.Log(ctx, entry); err != nil {
		return fmt.Errorf("principal private widening audit: %w", err)
	}
	return nil
}

func principalMemoryQueryItem(mem *models.Memory) PrincipalMemoryQueryItem {
	return PrincipalMemoryQueryItem{
		ID:                 mem.ID,
		Project:            mem.Project,
		Content:            mem.Content,
		Tags:               append([]string(nil), mem.Tags...),
		Status:             mem.Status,
		Tier:               mem.Tier,
		SourceAgent:        mem.SourceAgent,
		OwnerPrincipal:     mem.OwnerPrincipal,
		OwnerPrincipalKind: mem.OwnerPrincipalKind,
		AgentVisibility:    mem.AgentVisibility,
		Domain:             mem.Domain,
		Confidence:         mem.Confidence,
		CreatedAt:          mem.CreatedAt,
		UpdatedAt:          mem.UpdatedAt,
		Version:            mem.Version,
		memory:             cloneMemory(mem),
	}
}

func cloneMemory(mem *models.Memory) *models.Memory {
	if mem == nil {
		return nil
	}
	clone := *mem
	clone.Tags = append(mem.Tags[:0:0], mem.Tags...)
	clone.SourceSessions = append(mem.SourceSessions[:0:0], mem.SourceSessions...)
	sanitizeTimePtr := func(value **time.Time) {
		if *value == nil {
			return
		}
		year := (*value).Year()
		if year < 0 || year > 9999 {
			*value = nil
		}
	}
	sanitizeTimePtr(&clone.DeletedAt)
	sanitizeTimePtr(&clone.LastRetrievedAt)
	sanitizeTimePtr(&clone.LastConfirmed)
	sanitizeTimePtr(&clone.ReviewAfter)
	sanitizeTimePtr(&clone.ValidFrom)
	sanitizeTimePtr(&clone.ValidUntil)
	return &clone
}

func principalMemoryQueryResult(req PrincipalMemoryQueryRequest, limit int) *PrincipalMemoryQueryResult {
	return &PrincipalMemoryQueryResult{
		Principal:     strings.TrimSpace(req.OwnerPrincipal),
		PrincipalKind: strings.TrimSpace(strings.ToLower(req.OwnerPrincipalKind)),
		Project:       strings.TrimSpace(req.Project),
		Domain:        strings.TrimSpace(req.Domain),
		Items:         make([]PrincipalMemoryQueryItem, 0, limit),
		AuditStatus:   AuditStatusNotRequired,
		Audit: PrincipalMemoryQueryAudit{
			Action: AuditActionQuery,
		},
	}
}

func normalizePrincipalQueryLimit(limit int) int {
	if limit <= 0 {
		return DefaultPrincipalQueryLimit
	}
	if limit > MaxPrincipalQueryLimit {
		return MaxPrincipalQueryLimit
	}
	return limit
}

func principalQueryFetchLimit(visibleLimit int) int {
	const hiddenProbeBudget = 50
	return visibleLimit + hiddenProbeBudget
}
