package principalmemory

import (
	"context"
	"fmt"
	"strings"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

const (
	AuditStatusNotRequired = "not_required"
	AuditStatusWritten     = "written"
)

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
	AgentVisibility    string
	Domain             string
	Limit              int
	Offset             int
	SourceSessionID    string
}

type PrincipalMemoryQueryResult struct {
	Items       []PrincipalMemoryQueryItem
	HiddenCount int
	AuditStatus string
}

type PrincipalMemoryQueryItem struct {
	ID                 int64
	Project            string
	Content            string
	OwnerPrincipal     string
	OwnerPrincipalKind string
	AgentVisibility    string
	Domain             string
}

func (s *PrincipalMemoryQueryService) Query(ctx context.Context, req PrincipalMemoryQueryRequest) (*PrincipalMemoryQueryResult, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("principal memory query store is not configured")
	}

	limit := normalizePrincipalQueryLimit(req.Limit)
	rows, err := s.store.ListPrincipalMemory(ctx, strings.TrimSpace(req.Project), gormdb.ListOptions{
		OwnerPrincipal:     strings.TrimSpace(req.OwnerPrincipal),
		OwnerPrincipalKind: strings.TrimSpace(strings.ToLower(req.OwnerPrincipalKind)),
		AgentVisibility:    strings.TrimSpace(req.AgentVisibility),
		Domain:             strings.TrimSpace(req.Domain),
		Limit:              principalQueryFetchLimit(limit),
		Offset:             req.Offset,
	})
	if err != nil {
		return nil, err
	}

	result := &PrincipalMemoryQueryResult{
		Items:       make([]PrincipalMemoryQueryItem, 0, limit),
		AuditStatus: AuditStatusNotRequired,
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
		if decision.AuditRequired {
			if err := s.logPrivateRead(ctx, req, mem); err != nil {
				return nil, err
			}
			result.AuditStatus = AuditStatusWritten
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
		OwnerPrincipal:     mem.OwnerPrincipal,
		OwnerPrincipalKind: mem.OwnerPrincipalKind,
		AgentVisibility:    mem.AgentVisibility,
		Domain:             mem.Domain,
	}
}

func normalizePrincipalQueryLimit(limit int) int {
	const (
		defaultPrincipalQueryLimit = 10
		maxPrincipalQueryLimit     = 100
	)
	if limit <= 0 {
		return defaultPrincipalQueryLimit
	}
	if limit > maxPrincipalQueryLimit {
		return maxPrincipalQueryLimit
	}
	return limit
}

func principalQueryFetchLimit(visibleLimit int) int {
	const hiddenProbeBudget = 50
	return visibleLimit + hiddenProbeBudget
}
