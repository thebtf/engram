package principalmemory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	gormlib "gorm.io/gorm"
)

const (
	AuditActionDomainWriteWarn   = "principal_memory_domain_write_warn"
	AuditActionDomainWriteReject = "principal_memory_domain_write_reject"

	AuditReasonDomainCrossOwnerWarn   = "domain_owner_cross_write_warn"
	AuditReasonDomainCrossOwnerReject = "domain_owner_cross_write_reject"

	DomainWriteWarningCrossOwner        = "domain_owner_cross_write"
	DomainWriteReasonCrossOwnerWarn     = "domain_owner_cross_write_warn"
	DomainWriteReasonCrossOwnerRejected = "domain_owner_cross_write_rejected"
)

var ErrDomainWriteRejected = errors.New("domain owner rejects cross-principal write")

// DomainOwnerReader is the storage seam used by DomainRegistryService.
type DomainOwnerReader interface {
	Get(ctx context.Context, domain string) (*gormdb.DomainOwner, error)
}

type DomainRegistryService struct {
	store DomainOwnerReader
	audit AuditLogger
}

func NewDomainRegistryService(store DomainOwnerReader, audit AuditLogger) *DomainRegistryService {
	return &DomainRegistryService{store: store, audit: audit}
}

// DomainWriteCheckRequest asks whether a principal may write to a memory domain.
type DomainWriteCheckRequest struct {
	Project         string
	Domain          string
	Writer          PrincipalRef
	SourceSessionID string
}

// DomainWriteDecision is protocol-neutral so REST and MCP adapters can expose
// the same allow/warn/reject result without duplicating policy.
type DomainWriteDecision struct {
	Domain      string              `json:"domain"`
	Mode        string              `json:"mode,omitempty"`
	Owner       PrincipalRef        `json:"owner"`
	Writer      PrincipalRef        `json:"writer"`
	Warning     *DomainWriteWarning `json:"warning,omitempty"`
	Reason      string              `json:"reason,omitempty"`
	AuditStatus string              `json:"audit_status"`
	Allowed     bool                `json:"allowed"`
}

type DomainWriteWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *DomainRegistryService) CheckWrite(ctx context.Context, req DomainWriteCheckRequest) (*DomainWriteDecision, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("domain registry store is not configured")
	}

	domain := strings.TrimSpace(req.Domain)
	writer := normalizePrincipalRef(req.Writer)
	if writer.PrincipalKind != "" && !isValidPrincipalKind(writer.PrincipalKind) {
		return nil, fmt.Errorf("writer principal kind %q: %s", writer.PrincipalKind, ReasonInvalidPrincipalKind)
	}
	if domain == "" {
		return allowDomainWriteDecision(domain, "", PrincipalRef{}, writer), nil
	}

	row, err := s.store.Get(ctx, domain)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			return allowDomainWriteDecision(domain, "", PrincipalRef{}, writer), nil
		}
		return nil, fmt.Errorf("get domain owner %q: %w", domain, err)
	}

	owner := normalizePrincipalRef(PrincipalRef{
		Principal:     row.OwnerPrincipal,
		PrincipalKind: row.OwnerPrincipalKind,
	})
	if owner.PrincipalKind != "" && !isValidPrincipalKind(owner.PrincipalKind) {
		return nil, fmt.Errorf("domain owner principal kind %q: %s", owner.PrincipalKind, ReasonInvalidPrincipalKind)
	}
	mode := strings.TrimSpace(strings.ToLower(row.Mode))

	decision := allowDomainWriteDecision(domain, mode, owner, writer)
	if mode == gormdb.DomainOwnerModeOff || writer.matches(owner) {
		return decision, nil
	}

	switch mode {
	case gormdb.DomainOwnerModeWarn:
		decision.Warning = &DomainWriteWarning{
			Code:    DomainWriteWarningCrossOwner,
			Message: fmt.Sprintf("domain %q is owned by %s; cross-owner write from %s allowed with warning", domain, owner.Principal, writer.Principal),
		}
		decision.Reason = DomainWriteReasonCrossOwnerWarn
		if err := s.logDomainWrite(ctx, req, AuditActionDomainWriteWarn, AuditReasonDomainCrossOwnerWarn); err != nil {
			return nil, err
		}
		decision.AuditStatus = AuditStatusWritten
		return decision, nil
	case gormdb.DomainOwnerModeReject:
		decision.Allowed = false
		decision.Reason = DomainWriteReasonCrossOwnerRejected
		if err := s.logDomainWrite(ctx, req, AuditActionDomainWriteReject, AuditReasonDomainCrossOwnerReject); err != nil {
			return nil, err
		}
		decision.AuditStatus = AuditStatusWritten
		return decision, ErrDomainWriteRejected
	default:
		return nil, fmt.Errorf("domain owner mode %q must be one of off, warn, reject", mode)
	}
}

func (s *DomainRegistryService) logDomainWrite(ctx context.Context, req DomainWriteCheckRequest, action, reason string) error {
	if s.audit == nil {
		return fmt.Errorf("domain write %s requires audit logger", reason)
	}
	actor := strings.TrimSpace(req.Writer.Principal)
	if actor == "" {
		actor = "system"
	}
	entry := gormdb.AuditLogEntry{
		Action:          action,
		Actor:           actor,
		SourceSessionID: strings.TrimSpace(req.SourceSessionID),
		Reason:          reason,
	}
	if err := s.audit.Log(ctx, entry); err != nil {
		return fmt.Errorf("domain write audit: %w", err)
	}
	return nil
}

func allowDomainWriteDecision(domain, mode string, owner, writer PrincipalRef) *DomainWriteDecision {
	return &DomainWriteDecision{
		Domain:      domain,
		Mode:        mode,
		Owner:       owner,
		Writer:      writer,
		AuditStatus: AuditStatusNotRequired,
		Allowed:     true,
	}
}
