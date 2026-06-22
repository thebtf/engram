package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/models"
)

const principalMemoryQueryMaxLimit = 100

type principalMemoryQueryService interface {
	Query(ctx context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error)
}

// SetPrincipalMemoryQueryService wires the shared principal-memory query use case into MCP.
func (s *Server) SetPrincipalMemoryQueryService(queryService principalMemoryQueryService) {
	s.principalMemoryQuerySvc = queryService
}

func principalMemoryQueryTool() Tool {
	return Tool{
		Name:        "query_principal_memory",
		Description: "Query memories owned by a principal with bounded, owner-attributed, privacy-safe results.",
		tier:        tierUseful,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"principal", "principal_kind"},
			"properties": map[string]any{
				"principal":       map[string]any{"type": "string", "description": "Principal identifier to inspect, e.g. agent/alice or a human account id."},
				"principal_kind":  map[string]any{"type": "string", "enum": []string{"human", "agent", "service"}, "description": "Principal kind."},
				"project":         map[string]any{"type": "string", "description": "Optional project filter."},
				"domain":          map[string]any{"type": "string", "description": "Optional memory domain filter."},
				"q":               map[string]any{"type": "string", "description": "Optional substring filter over memory content."},
				"visibility":      map[string]any{"type": "string", "enum": []string{"private", "shared"}, "description": "Optional principal visibility filter."},
				"include_private": map[string]any{"type": "boolean", "description": "Request private rows. Cross-principal private widening requires admin identity and durable audit."},
				"limit":           map[string]any{"type": "integer", "default": 10, "minimum": 1, "maximum": principalMemoryQueryMaxLimit},
				"offset":          map[string]any{"type": "integer", "default": 0, "minimum": 0},
				"session_id":      map[string]any{"type": "string", "description": "Optional session id for audit provenance."},
			},
		},
	}
}

func (s *Server) handleQueryPrincipalMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.principalMemoryQuerySvc == nil {
		return "", fmt.Errorf("principal memory query service not available")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	ownerPrincipal := strings.TrimSpace(coerceString(m["principal"], ""))
	if ownerPrincipal == "" {
		return "", fmt.Errorf("principal is required")
	}
	ownerPrincipalKind := strings.TrimSpace(strings.ToLower(coerceString(m["principal_kind"], "")))
	if ownerPrincipalKind == "" {
		return "", fmt.Errorf("principal_kind is required")
	}
	if !auth.IsValidPrincipalKind(auth.PrincipalKind(ownerPrincipalKind)) {
		return "", fmt.Errorf("principal_kind must be one of human, agent, service")
	}

	visibility := strings.TrimSpace(coerceString(m["visibility"], ""))
	if visibility != "" && !models.IsValidAgentVisibility(visibility) {
		return "", fmt.Errorf("visibility must be one of private, shared")
	}
	limit, err := parsePrincipalMemoryQueryLimit(m["limit"])
	if err != nil {
		return "", err
	}
	offset, err := parsePrincipalMemoryQueryOffset(m["offset"])
	if err != nil {
		return "", err
	}
	includePrivate, err := parsePrincipalMemoryQueryBool(m["include_private"])
	if err != nil {
		return "", fmt.Errorf("include_private must be a boolean")
	}

	caller, callerIsAdmin := principalMemoryQueryCaller(ctx)
	result, err := s.principalMemoryQuerySvc.Query(ctx, principalmemory.PrincipalMemoryQueryRequest{
		Project:            strings.TrimSpace(coerceString(m["project"], "")),
		Caller:             caller,
		CallerIsAdmin:      callerIsAdmin,
		OwnerPrincipal:     ownerPrincipal,
		OwnerPrincipalKind: ownerPrincipalKind,
		Query:              strings.TrimSpace(coerceString(m["q"], "")),
		AgentVisibility:    visibility,
		IncludePrivate:     includePrivate,
		Domain:             strings.TrimSpace(coerceString(m["domain"], "")),
		Limit:              limit,
		Offset:             offset,
		SourceSessionID:    strings.TrimSpace(coerceString(m["session_id"], "")),
	})
	if err != nil {
		return "", fmt.Errorf("principal memory query failed: %w", err)
	}

	if result == nil {
		result = &principalmemory.PrincipalMemoryQueryResult{
			Items:       []principalmemory.PrincipalMemoryQueryItem{},
			AuditStatus: principalmemory.AuditStatusNotRequired,
		}
	}
	if result.Items == nil {
		result.Items = []principalmemory.PrincipalMemoryQueryItem{}
	}
	if result.AuditStatus == "" {
		result.AuditStatus = principalmemory.AuditStatusNotRequired
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal principal memory query response: %w", err)
	}
	return string(out), nil
}

func principalMemoryQueryCaller(ctx context.Context) (principalmemory.PrincipalRef, bool) {
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

func parsePrincipalMemoryQueryLimit(raw any) (int, error) {
	if raw == nil {
		return 10, nil
	}
	n, err := parsePrincipalMemoryQueryInt(raw)
	if err != nil || n < 1 || n > principalMemoryQueryMaxLimit {
		return 0, fmt.Errorf("limit must be between 1 and 100")
	}
	return n, nil
}

func parsePrincipalMemoryQueryOffset(raw any) (int, error) {
	if raw == nil {
		return 0, nil
	}
	n, err := parsePrincipalMemoryQueryInt(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("offset must be a non-negative integer")
	}
	return n, nil
}

func parsePrincipalMemoryQueryInt(raw any) (int, error) {
	switch v := raw.(type) {
	case float64:
		if math.Trunc(v) != v || v > float64(math.MaxInt) || v < float64(math.MinInt) {
			return 0, fmt.Errorf("not an integer")
		}
		return int(v), nil
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, err
		}
		if i > int64(math.MaxInt) || i < int64(math.MinInt) {
			return 0, fmt.Errorf("integer out of range")
		}
		return int(i), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(v))
	case int:
		return v, nil
	default:
		return 0, fmt.Errorf("not an integer")
	}
}

func parsePrincipalMemoryQueryBool(raw any) (bool, error) {
	if raw == nil {
		return false, nil
	}
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		return strconv.ParseBool(strings.TrimSpace(v))
	default:
		return false, fmt.Errorf("not a boolean")
	}
}
