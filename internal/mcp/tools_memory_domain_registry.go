package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

type domainRegistryService interface {
	CheckWrite(ctx context.Context, req principalmemory.DomainWriteCheckRequest) (*principalmemory.DomainWriteDecision, error)
}

// SetDomainRegistryService wires the shared domain registry use case into MCP store_memory.
func (s *Server) SetDomainRegistryService(registryService domainRegistryService) {
	s.domainRegistryService = registryService
}

func (s *Server) checkDomainWriteMCP(ctx context.Context, mem *models.Memory, sourceSessionID string) (*principalmemory.DomainWriteDecision, error) {
	return s.checkDomainWriteMCPTx(ctx, mem, sourceSessionID, nil)
}

func (s *Server) checkDomainWriteMCPTx(ctx context.Context, mem *models.Memory, sourceSessionID string, tx *gormlib.DB) (*principalmemory.DomainWriteDecision, error) {
	if mem == nil || strings.TrimSpace(mem.Domain) == "" {
		return nil, nil
	}
	if s.domainRegistryService == nil {
		return nil, nil
	}
	req := principalmemory.DomainWriteCheckRequest{
		Project:         mem.Project,
		Domain:          mem.Domain,
		Writer:          principalmemory.PrincipalRef{Principal: mem.OwnerPrincipal, PrincipalKind: mem.OwnerPrincipalKind},
		SourceSessionID: strings.TrimSpace(sourceSessionID),
	}
	var decision *principalmemory.DomainWriteDecision
	var err error
	if registry, ok := s.domainRegistryService.(interface {
		CheckWriteTx(context.Context, *gormlib.DB, principalmemory.DomainWriteCheckRequest) (*principalmemory.DomainWriteDecision, error)
	}); ok && tx != nil {
		decision, err = registry.CheckWriteTx(ctx, tx, req)
	} else {
		decision, err = s.domainRegistryService.CheckWrite(ctx, req)
	}
	if err != nil {
		return decision, err
	}
	if decision != nil && !decision.Allowed {
		return decision, principalmemory.ErrDomainWriteRejected
	}
	return decision, nil
}

func addDomainWriteDecisionFields(result map[string]any, decision *principalmemory.DomainWriteDecision) {
	if decision == nil || decision.Warning == nil {
		return
	}
	result["domain_warning"] = decision.Warning
	result["domain_audit_status"] = decision.AuditStatus
}

func marshalStoreMemoryAugmented(value any, decision *principalmemory.DomainWriteDecision, staleTerms []string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	addDomainWriteDecisionFields(result, decision)
	if len(staleTerms) > 0 {
		result["staleness_advisory"] = staleAdvisory(staleTerms)
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
