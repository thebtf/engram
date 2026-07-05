package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s4directives"
)

type directiveCaptureService interface {
	RememberDirective(ctx context.Context, project, sessionID string, req s4directives.RememberDirectiveRequest) (*s4directives.StoredAttentionEvent, error)
}

func directivesCaptureEnabledFromEnv() bool {
	return cognitivecore.LoadFlagConfigFromEnv().IsSubsystemEnabled("s4a")
}

func rememberDirectiveTool() Tool {
	return Tool{
		Name:        "remember_directive",
		Description: "CR-001: capture a distilled creator directive without storing raw prompt text. Only available when master v7 and S4a flags are enabled.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"text"},
			"properties": map[string]any{
				"text":          map[string]any{"type": "string", "description": "Directive text to distill and capture. Raw text is validated, distilled, then discarded."},
				"source_turn":   map[string]any{"type": "string", "description": "Optional source-turn material; hashed before storage and never persisted raw."},
				"horizon":       map[string]any{"type": "string", "enum": []string{"session", "project", "permanent"}, "description": "Optional bounded horizon. Defaults to project."},
				"privacy_class": map[string]any{"type": "string", "enum": []string{"public", "internal", "secret"}, "description": "Optional privacy class. Defaults to internal."},
			},
		},
	}
}

func (s *Server) currentDirectiveCaptureService() (directiveCaptureService, error) {
	if !directivesCaptureEnabledFromEnv() {
		return nil, fmt.Errorf("remember_directive feature flag required")
	}
	if s == nil || s.directiveCaptureService == nil {
		return nil, fmt.Errorf("remember_directive service not configured")
	}
	return s.directiveCaptureService, nil
}

func (s *Server) handleRememberDirective(ctx context.Context, args json.RawMessage) (string, error) {
	service, err := s.currentDirectiveCaptureService()
	if err != nil {
		return "", err
	}
	request, err := parseRememberDirectiveArgs(args)
	if err != nil {
		return "", err
	}
	project := projectFromContext(ctx)
	if project == "" {
		return "", fmt.Errorf("project is required")
	}
	sessionID := sessionFromContext(ctx)
	if sessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}
	stored, err := service.RememberDirective(ctx, project, sessionID, request)
	if err != nil {
		return "", err
	}
	return marshalJSON(stored)
}

func parseRememberDirectiveArgs(args json.RawMessage) (s4directives.RememberDirectiveRequest, error) {
	m, err := parseArgs(args)
	if err != nil {
		return s4directives.RememberDirectiveRequest{}, err
	}
	return s4directives.RememberDirectiveRequest{
		Text:         coerceString(m["text"], ""),
		SourceTurn:   coerceString(m["source_turn"], ""),
		Horizon:      coerceString(m["horizon"], ""),
		PrivacyClass: coerceString(m["privacy_class"], ""),
	}, nil
}
