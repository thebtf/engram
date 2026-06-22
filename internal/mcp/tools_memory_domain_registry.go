package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/models"
)

type domainRegistryService interface {
	CheckWrite(ctx context.Context, req principalmemory.DomainWriteCheckRequest) (*principalmemory.DomainWriteDecision, error)
}

// SetDomainRegistryService wires the shared domain registry use case into MCP store_memory.
func (s *Server) SetDomainRegistryService(registryService domainRegistryService) {
	s.domainRegistryService = registryService
}

func (s *Server) checkDomainWriteMCP(ctx context.Context, mem *models.Memory, sourceSessionID string) (*principalmemory.DomainWriteDecision, error) {
	if mem == nil || strings.TrimSpace(mem.Domain) == "" {
		return nil, nil
	}
	if s.domainRegistryService == nil {
		return nil, nil
	}
	decision, err := s.domainRegistryService.CheckWrite(ctx, principalmemory.DomainWriteCheckRequest{
		Project:         mem.Project,
		Domain:          mem.Domain,
		Writer:          principalmemory.PrincipalRef{Principal: mem.OwnerPrincipal, PrincipalKind: mem.OwnerPrincipalKind},
		SourceSessionID: strings.TrimSpace(sourceSessionID),
	})
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
