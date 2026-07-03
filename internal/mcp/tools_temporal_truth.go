package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/cognitive"
)

type temporalTruthProvider interface {
	QueryTemporalTruth(ctx context.Context, request cognitive.TemporalTruthQueryRequest) (cognitive.TemporalTruthResponse, error)
	RefreshProject(ctx context.Context, project string) (gormdb.TemporalTruthAdmissionResult, error)
}

// SetTemporalTruthProvider wires the CR-011 selected temporal truth read seam.
func (s *Server) SetTemporalTruthProvider(provider temporalTruthProvider) {
	s.temporalTruthProvider = provider
}

func temporalTruthEnabledFromEnv() bool {
	value := strings.TrimSpace(os.Getenv("ENGRAM_TEMPORAL_TRUTH_ENABLED"))
	return value == "true" || value == "1"
}

func temporalTruthTool() Tool {
	return Tool{
		Name:        "temporal_truth",
		Description: "CR-011: read bounded true-now-vs-then temporal truth for admitted fact classes only.",
		tier:        tierCore,
		InputSchema: temporalTruthSchema(),
	}
}

func temporalTruthRefreshTool() Tool {
	return Tool{
		Name:        "temporal_truth_refresh",
		Description: "CR-011: explicitly rebuild admitted temporal truth rows for one project.",
		tier:        tierCore,
		InputSchema: temporalTruthRefreshSchema(),
	}
}

func temporalTruthRefreshSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"project"},
		"properties": map[string]any{
			"project": map[string]any{"type": "string", "description": "Project whose admitted temporal truth rows should be rebuilt."},
		},
	}
}

func temporalTruthSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"project", "fact_id"},
		"properties": map[string]any{
			"project":    map[string]any{"type": "string", "description": "Project scope for the temporal truth read."},
			"fact_id":    map[string]any{"type": "string", "description": "Selected fact identifier (root memory id string for the additive store path)."},
			"fact_class": map[string]any{"type": "string", "description": "Optional admitted fact class filter."},
			"as_of":      map[string]any{"type": "string", "description": "Optional RFC3339 anchor for true-then lookup."},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "description": "Bounded history limit. Defaults to 5, max 10."},
		},
	}
}

func (s *Server) currentTemporalTruthProvider() (temporalTruthProvider, error) {
	if !temporalTruthEnabledFromEnv() {
		return nil, fmt.Errorf("temporal truth feature flag required")
	}
	if s == nil || s.temporalTruthProvider == nil {
		return nil, fmt.Errorf("temporal truth provider not configured")
	}
	return s.temporalTruthProvider, nil
}

func (s *Server) handleTemporalTruth(ctx context.Context, args json.RawMessage) (string, error) {
	provider, err := s.currentTemporalTruthProvider()
	if err != nil {
		return "", err
	}
	request, err := parseTemporalTruthArgs(args)
	if err != nil {
		return "", err
	}
	response, err := provider.QueryTemporalTruth(ctx, request)
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return "", fmt.Errorf("temporal_truth marshal response: %w", err)
	}
	return string(out), nil
}

func (s *Server) handleTemporalTruthRefresh(ctx context.Context, args json.RawMessage) (string, error) {
	provider, err := s.currentTemporalTruthProvider()
	if err != nil {
		return "", err
	}
	project, err := parseTemporalTruthRefreshProject(args)
	if err != nil {
		return "", err
	}
	result, err := provider.RefreshProject(ctx, project)
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("temporal_truth_refresh marshal response: %w", err)
	}
	return string(out), nil
}

func parseTemporalTruthArgs(args json.RawMessage) (cognitive.TemporalTruthQueryRequest, error) {
	m, err := parseArgs(args)
	if err != nil {
		return cognitive.TemporalTruthQueryRequest{}, err
	}
	project := strings.TrimSpace(coerceString(m["project"], ""))
	if project == "" {
		return cognitive.TemporalTruthQueryRequest{}, fmt.Errorf("project is required")
	}
	factID := strings.TrimSpace(coerceString(m["fact_id"], ""))
	if factID == "" {
		return cognitive.TemporalTruthQueryRequest{}, fmt.Errorf("fact_id is required")
	}
	request := cognitive.TemporalTruthQueryRequest{
		Project:   project,
		FactID:    factID,
		FactClass: strings.TrimSpace(coerceString(m["fact_class"], "")),
		Limit:     coerceInt(m["limit"], 0),
	}
	if rawAsOf := strings.TrimSpace(coerceString(m["as_of"], "")); rawAsOf != "" {
		parsed, err := time.Parse(time.RFC3339, rawAsOf)
		if err != nil {
			return cognitive.TemporalTruthQueryRequest{}, fmt.Errorf("as_of must be RFC3339")
		}
		request.AsOf = &parsed
	}
	return request, nil
}

func parseTemporalTruthRefreshProject(args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	project := strings.TrimSpace(coerceString(m["project"], ""))
	if project == "" {
		return "", fmt.Errorf("project is required")
	}
	return project, nil
}
