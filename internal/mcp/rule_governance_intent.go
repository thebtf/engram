package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/pkg/models"
)

const explicitActiveRuleIntentSignal = "explicit_active_rule_intent"

type ruleIntentCapture struct {
	Content     string
	Project     string
	Scope       string
	Audience    string
	SessionID   string
	Actor       string
	SourceTool  string
	EvidenceTag string
}

func ruleGovernanceCaptureEnabled() bool {
	if v, ok := os.LookupEnv("ENGRAM_RULE_GOVERNANCE_ENABLED"); ok {
		v = strings.TrimSpace(v)
		return v == "true" || v == "1"
	}
	if config.Get().RuleGovernanceEnabled {
		return true
	}
	return false
}

func (s *Server) captureActiveRuleIntent(ctx context.Context, intent ruleIntentCapture) (*models.RuleCandidate, error) {
	if !ruleGovernanceCaptureEnabled() || s.ruleGovernanceStore == nil {
		return nil, nil
	}
	content := strings.TrimSpace(intent.Content)
	if content == "" {
		return nil, fmt.Errorf("rule_governance: content is required")
	}
	scope := strings.TrimSpace(intent.Scope)
	if scope == "" {
		scope = string(models.ScopeProject)
	}
	project := strings.TrimSpace(intent.Project)
	if project == "" {
		project = projectFromContext(ctx)
	}
	audience := strings.TrimSpace(intent.Audience)
	if audience == "" {
		audience = "developer"
	}
	actor := strings.TrimSpace(intent.Actor)
	if actor == "" {
		actor = actorFromContext(ctx)
	}
	sessionID := strings.TrimSpace(intent.SessionID)
	if sessionID == "" {
		sessionID = sessionFromContext(ctx)
	}
	sourceTool := strings.TrimSpace(intent.SourceTool)
	if sourceTool == "" {
		sourceTool = "mcp"
	}
	evidenceTag := strings.TrimSpace(intent.EvidenceTag)
	if evidenceTag == "" {
		evidenceTag = "explicit-active-rule-intent"
	}

	candidate := &models.RuleCandidate{
		SourceSignalType: explicitActiveRuleIntentSignal,
		SourceSessionID:  sessionID,
		SourceProject:    project,
		SourceActor:      actor,
		ProposedContent:  content,
		ProposedScope:    scope,
		ProposedAudience: audience,
		ActivationPredicate: map[string]any{
			"scope":  scope,
			"source": sourceTool,
		},
		EvidenceHandles: []string{
			"mcp:" + sourceTool,
			"signal:" + evidenceTag,
		},
		AntiCaptureStatus: models.RuleEscapeNoData,
		ConflictStatus:    models.RuleEscapeNoData,
		Status:            models.RuleCandidatePending,
		Fingerprint:       ruleIntentFingerprint(sourceTool, project, scope, audience, content),
		DecayPolicy:       models.RuleEscapeNoData,
	}
	if project != "" {
		candidate.ActivationPredicate["project"] = project
	}
	return s.ruleGovernanceStore.CreateRuleCandidate(ctx, candidate)
}

func ruleIntentFingerprint(parts ...string) string {
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		normalized = append(normalized, strings.TrimSpace(strings.ToLower(part)))
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\x00")))
	return fmt.Sprintf("rule-intent:%x", sum[:])
}

func marshalRuleCandidateIntentResponse(candidate *models.RuleCandidate, fields map[string]any) (string, error) {
	result := map[string]any{
		"id":              candidate.ID,
		"storage":         "rule_candidates",
		"rule_governance": true,
		"active":          false,
		"status":          string(candidate.Status),
		"candidate_id":    candidate.ID,
		"message":         "Active rule intent captured as a pending rule candidate",
	}
	for k, v := range fields {
		result[k] = v
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal rule governance response: %w", err)
	}
	return string(out), nil
}
