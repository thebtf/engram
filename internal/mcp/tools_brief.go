package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/injection"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

type memoryBriefArgs struct {
	Topic          string `json:"topic"`
	Project        string `json:"project"`
	Principal      string `json:"principal"`
	PrincipalKind  string `json:"principal_kind"`
	Domain         string `json:"domain"`
	Visibility     string `json:"visibility"`
	IncludePrivate bool   `json:"include_private"`
	SessionID      string `json:"session_id"`
	Limit          int    `json:"limit"`
}

// handleGetMemoryBrief returns a compact memory context for sub-agent delegation briefs.
func (s *Server) handleGetMemoryBrief(ctx context.Context, args json.RawMessage) (string, error) {
	if os.Getenv("ENGRAM_ADAPTIVE_ENABLED") != "true" {
		return marshalJSON(map[string]string{"status": "disabled", "message": "set ENGRAM_ADAPTIVE_ENABLED=true to enable"})
	}
	if s.memoryStore == nil {
		return "", fmt.Errorf("memory store not available")
	}

	var a memoryBriefArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if a.Project == "" {
		a.Project = projectFromContext(ctx)
	}
	if a.Project == "" {
		return "", fmt.Errorf("project required")
	}
	if a.Limit <= 0 {
		a.Limit = 5
	}
	if a.Limit > 10 {
		a.Limit = 10
	}

	if memoryBriefUsesPrincipalScope(a) {
		return s.handlePrincipalMemoryBrief(ctx, a)
	}

	raw, err := s.memoryStore.ListForInjection(ctx, a.Project, a.Limit*3)
	if err != nil {
		return "", fmt.Errorf("list memories: %w", err)
	}

	// T004 (codex P1 PR #221): apply scope.Resolve filter when
	// ENGRAM_VNEXT_F_ENABLED=true so private memories are not included in
	// briefs for callers that cannot see them.
	candidates := filterInjectionByScope(ctx, raw)

	if len(candidates) == 0 {
		return marshalJSON(map[string]any{
			"project":  a.Project,
			"topic":    a.Topic,
			"memories": []any{},
			"message":  "no memories found for this project",
		})
	}

	var scoreOpts injection.ScoreOpts
	citRate, crErr := s.memoryStore.GetProjectCitationRate(ctx, a.Project, 10)
	if crErr == nil && citRate != 0.5 {
		scoreOpts.DynamicPrior = true
		scoreOpts.ProjectCitationRate = citRate
	}

	scored := injection.Score(candidates, a.Limit, scoreOpts)

	var memories []map[string]any
	for _, sm := range scored {
		if !sm.Selected || sm.Memory == nil {
			break
		}
		memories = append(memories, map[string]any{
			"id":      sm.Memory.ID,
			"content": truncateBriefContent(sm.Memory.Content),
			"tags":    sm.Memory.Tags,
		})
	}

	return marshalJSON(map[string]any{
		"project":  a.Project,
		"topic":    a.Topic,
		"memories": memories,
	})
}

func memoryBriefUsesPrincipalScope(a memoryBriefArgs) bool {
	return strings.TrimSpace(a.Principal) != "" ||
		strings.TrimSpace(a.PrincipalKind) != "" ||
		strings.TrimSpace(a.Domain) != "" ||
		strings.TrimSpace(a.Visibility) != "" ||
		a.IncludePrivate
}

func (s *Server) handlePrincipalMemoryBrief(ctx context.Context, a memoryBriefArgs) (string, error) {
	if s.principalMemoryQuerySvc == nil {
		return "", fmt.Errorf("principal memory query service not available")
	}

	principal := strings.TrimSpace(a.Principal)
	principalKind := strings.TrimSpace(strings.ToLower(a.PrincipalKind))
	if principal == "" {
		if principalKind != "" {
			return "", fmt.Errorf("principal is required when principal_kind is set")
		}
		if a.IncludePrivate {
			return "", fmt.Errorf("principal is required when include_private is true")
		}
	} else {
		if principalKind == "" {
			principalKind = "human"
		}
		if !auth.IsValidPrincipalKind(auth.PrincipalKind(principalKind)) {
			return "", fmt.Errorf("principal_kind must be one of human, agent, service")
		}
	}
	requestPrincipalKind := principalKind

	visibility := strings.TrimSpace(strings.ToLower(a.Visibility))
	switch visibility {
	case "", "all":
		visibility = ""
	case models.AgentVisibilityPrivate, models.AgentVisibilityShared:
	default:
		return "", fmt.Errorf("visibility must be one of private, shared, all")
	}

	caller, callerIsAdmin := principalMemoryQueryCaller(ctx)
	result, err := s.principalMemoryQuerySvc.Query(ctx, principalmemory.PrincipalMemoryQueryRequest{
		Project:            strings.TrimSpace(a.Project),
		Caller:             caller,
		CallerIsAdmin:      callerIsAdmin,
		OwnerPrincipal:     principal,
		OwnerPrincipalKind: principalKind,
		Query:              strings.TrimSpace(a.Topic),
		AgentVisibility:    visibility,
		IncludePrivate:     a.IncludePrivate,
		Domain:             strings.TrimSpace(a.Domain),
		Limit:              a.Limit,
		SourceSessionID:    strings.TrimSpace(a.SessionID),
	})
	if err != nil {
		return "", fmt.Errorf("principal memory brief failed: %w", err)
	}
	if result == nil {
		result = &principalmemory.PrincipalMemoryQueryResult{
			Items:       []principalmemory.PrincipalMemoryQueryItem{},
			AuditStatus: principalmemory.AuditStatusNotRequired,
			Audit: principalmemory.PrincipalMemoryQueryAudit{
				Action: principalmemory.AuditActionQuery,
			},
		}
	}
	if result.AuditStatus == "" {
		result.AuditStatus = principalmemory.AuditStatusNotRequired
	}
	if result.Audit.Action == "" {
		result.Audit.Action = principalmemory.AuditActionQuery
	}

	memories := make([]map[string]any, 0, len(result.Items))
	for _, item := range result.Items {
		memories = append(memories, map[string]any{
			"id":                   item.ID,
			"project":              item.Project,
			"content":              truncateBriefContent(item.Content),
			"tags":                 item.Tags,
			"source_agent":         item.SourceAgent,
			"owner_principal":      item.OwnerPrincipal,
			"owner_principal_kind": item.OwnerPrincipalKind,
			"agent_visibility":     item.AgentVisibility,
			"domain":               item.Domain,
			"confidence":           item.Confidence,
			"created_at":           item.CreatedAt,
		})
	}

	project := strings.TrimSpace(result.Project)
	if project == "" {
		project = strings.TrimSpace(a.Project)
	}
	domain := strings.TrimSpace(result.Domain)
	if domain == "" {
		domain = strings.TrimSpace(a.Domain)
	}
	principal = strings.TrimSpace(result.Principal)
	if principal == "" {
		principal = strings.TrimSpace(a.Principal)
	}
	principalKind = strings.TrimSpace(result.PrincipalKind)
	if principalKind == "" {
		principalKind = requestPrincipalKind
	}
	generatedAt := time.Now().UTC()
	scopeEvidence := map[string]any{
		"project":         project,
		"source":          "principal_query",
		"freshness":       "live",
		"generated_at":    generatedAt,
		"hidden_count":    result.HiddenCount,
		"audit_status":    result.AuditStatus,
		"audit":           result.Audit,
		"include_private": a.IncludePrivate,
	}
	if principal != "" {
		scopeEvidence["principal"] = principal
	}
	if principalKind != "" {
		scopeEvidence["principal_kind"] = principalKind
	}
	if domain != "" {
		scopeEvidence["domain"] = domain
	}

	response := map[string]any{
		"project":      project,
		"topic":        a.Topic,
		"source":       "principal_query",
		"freshness":    "live",
		"generated_at": generatedAt,
		"scope":        scopeEvidence,
		"memories":     memories,
	}
	if principal != "" {
		response["principal"] = principal
	}
	if principalKind != "" {
		response["principal_kind"] = principalKind
	}
	if domain != "" {
		response["domain"] = domain
	}
	if len(memories) == 0 {
		response["message"] = "no memories found for this principal scope"
	}
	return marshalJSON(response)
}

func truncateBriefContent(content string) string {
	runes := []rune(content)
	if len(runes) > 200 {
		return string(runes[:200]) + "..."
	}
	return content
}

// filterInjectionByScope applies the shared memory visibility predicate to a
// slice of injection candidates. ENGRAM_VNEXT_F_ENABLED gates only the legacy
// privacy_scope layer; principal-private rows are filtered fail-safe.
func filterInjectionByScope(ctx context.Context, mems []*models.Memory) []*models.Memory {
	caller := scope.KeycardContext{}
	if id, ok := auth.IdentityFrom(ctx); ok {
		caller.WorkstationID = id.WorkstationID()
		caller.Principal = id.Principal
		caller.PrincipalKind = string(id.PrincipalKind)
	}
	opts := scope.MemoryVisibilityOptions{
		ApplyPrivacyScope: os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true",
	}
	visible := make([]*models.Memory, 0, len(mems))
	for _, mem := range mems {
		if scope.ResolveMemory(caller, mem, opts) {
			visible = append(visible, mem)
		}
	}
	return visible
}
