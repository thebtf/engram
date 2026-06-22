package principalmemory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
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
	AgentVisibility    string
	IncludePrivate     bool
	Domain             string
	Limit              int
	Offset             int
	SourceSessionID    string
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
	OwnerPrincipal     string    `json:"owner_principal"`
	OwnerPrincipalKind string    `json:"owner_principal_kind"`
	AgentVisibility    string    `json:"agent_visibility"`
	Domain             string    `json:"domain"`
	Confidence         float64   `json:"confidence"`
	CreatedAt          time.Time `json:"created_at"`
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

	rows, err := s.store.ListPrincipalMemory(ctx, strings.TrimSpace(req.Project), gormdb.ListOptions{
		OwnerPrincipal:     strings.TrimSpace(req.OwnerPrincipal),
		OwnerPrincipalKind: strings.TrimSpace(strings.ToLower(req.OwnerPrincipalKind)),
		ContentContains:    strings.TrimSpace(req.Query),
		AgentVisibility:    strings.TrimSpace(req.AgentVisibility),
		Domain:             strings.TrimSpace(req.Domain),
		Limit:              principalQueryFetchLimit(limit),
		Offset:             req.Offset,
	})
	if err != nil {
		return nil, err
	}

	result := principalMemoryQueryResult(req, limit)
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
	return result, nil
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
		OwnerPrincipal:     mem.OwnerPrincipal,
		OwnerPrincipalKind: mem.OwnerPrincipalKind,
		AgentVisibility:    mem.AgentVisibility,
		Domain:             mem.Domain,
		Confidence:         mem.Confidence,
		CreatedAt:          mem.CreatedAt,
	}
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
