package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/pkg/models"
)

type fakeRuleGovernanceCandidateWriter struct {
	created []*models.RuleCandidate
	nextID  int64
}

func (f *fakeRuleGovernanceCandidateWriter) CreateRuleCandidate(_ context.Context, c *models.RuleCandidate) (*models.RuleCandidate, error) {
	f.nextID++
	cp := *c
	cp.ID = f.nextID
	f.created = append(f.created, &cp)
	return &cp, nil
}

func TestStoreMemoryAlwaysInject_GovernanceFlagCreatesRuleCandidate(t *testing.T) {
	t.Setenv("ENGRAM_RULE_GOVERNANCE_ENABLED", "true")

	writer := &fakeRuleGovernanceCandidateWriter{}
	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = &gorm.MemoryStore{}
	s.ruleGovernanceStore = writer

	args := mustJSON(t, map[string]any{
		"content":       "Agents must verify migration gates before release.",
		"project":       "rg1-project",
		"scope":         "project",
		"always_inject": true,
		"session_id":    "rg1-session",
		"agent_source":  "codex",
	})

	out, err := s.handleStoreMemory(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, writer.created, 1)
	require.Equal(t, "explicit_active_rule_intent", writer.created[0].SourceSignalType)
	require.Equal(t, "rg1-project", writer.created[0].SourceProject)
	require.Equal(t, "project", writer.created[0].ProposedScope)
	require.Equal(t, models.RuleCandidatePending, writer.created[0].Status)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.Equal(t, "rule_candidates", resp["storage"])
	require.Equal(t, true, resp["rule_governance"])
}

func TestStoreMemoryAlwaysInject_FlagOffDoesNotUseRuleGovernance(t *testing.T) {
	t.Setenv("ENGRAM_RULE_GOVERNANCE_ENABLED", "")

	writer := &fakeRuleGovernanceCandidateWriter{}
	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = &gorm.MemoryStore{}
	s.ruleGovernanceStore = writer

	args := mustJSON(t, map[string]any{
		"content":       "Legacy active rule path remains active when governance is off.",
		"project":       "rg1-project",
		"scope":         "project",
		"always_inject": true,
	})

	_, err := s.handleStoreMemory(context.Background(), args)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "always_inject=true requires behavioral rules store"))
	require.Empty(t, writer.created, "flag-off path must not create rule candidates")
}

func TestStoreRule_GovernanceFlagCreatesRuleCandidate(t *testing.T) {
	t.Setenv("ENGRAM_RULE_GOVERNANCE_ENABLED", "true")

	writer := &fakeRuleGovernanceCandidateWriter{}
	s := NewServer(ServerOptions{Version: "test"})
	s.ruleGovernanceStore = writer

	args := mustJSON(t, map[string]any{
		"content":  "Agents must keep release evidence before publishing.",
		"project":  "rg1-project",
		"priority": 3,
	})

	out, err := s.handleStoreRule(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, writer.created, 1)
	require.Equal(t, "explicit_active_rule_intent", writer.created[0].SourceSignalType)
	require.Equal(t, "rg1-project", writer.created[0].SourceProject)
	require.Equal(t, "project", writer.created[0].ProposedScope)
	require.Equal(t, models.RuleCandidatePending, writer.created[0].Status)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.Equal(t, "rule_candidates", resp["storage"])
	require.Equal(t, true, resp["rule_governance"])
	require.Equal(t, false, resp["active"])
}

func TestStoreRule_GovernanceFlagPreservesGlobalIntentWithContextProject(t *testing.T) {
	t.Setenv("ENGRAM_RULE_GOVERNANCE_ENABLED", "true")

	writer := &fakeRuleGovernanceCandidateWriter{}
	s := NewServer(ServerOptions{Version: "test"})
	s.ruleGovernanceStore = writer
	ctx := contextWithProject(context.Background(), "git-derived-project")

	args := mustJSON(t, map[string]any{
		"content": "Agents must preserve global rule intent when no project is provided.",
	})

	out, err := s.handleStoreRule(ctx, args)
	require.NoError(t, err)
	require.Len(t, writer.created, 1)
	require.Equal(t, "", writer.created[0].SourceProject)
	require.Equal(t, "global", writer.created[0].ProposedScope)
	require.NotContains(t, writer.created[0].ActivationPredicate, "project")

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.Equal(t, nil, resp["project"])
	require.Equal(t, "global", resp["scope"])
}

func TestStoreRule_GovernanceFlagRedactsCandidateContent(t *testing.T) {
	t.Setenv("ENGRAM_RULE_GOVERNANCE_ENABLED", "true")

	rules, err := redaction.CompileRules([]redaction.Rule{{
		ID:          "secret-token",
		Pattern:     `SECRET-[0-9]+`,
		Replacement: "[REDACTED]",
	}})
	require.NoError(t, err)

	writer := &fakeRuleGovernanceCandidateWriter{}
	s := NewServer(ServerOptions{Version: "test"})
	s.ruleGovernanceStore = writer
	s.redactionRules = rules

	args := mustJSON(t, map[string]any{
		"content": "Never expose SECRET-12345 in prompts.",
		"project": "rg1-project",
	})

	_, err = s.handleStoreRule(context.Background(), args)
	require.NoError(t, err)
	require.Len(t, writer.created, 1)
	require.Equal(t, "Never expose [REDACTED] in prompts.", writer.created[0].ProposedContent)
}

func TestStoreRule_FlagOffDoesNotUseRuleGovernance(t *testing.T) {
	t.Setenv("ENGRAM_RULE_GOVERNANCE_ENABLED", "")

	writer := &fakeRuleGovernanceCandidateWriter{}
	s := NewServer(ServerOptions{Version: "test"})
	s.ruleGovernanceStore = writer

	args := mustJSON(t, map[string]any{
		"content": "Legacy store_rule still requires behavioral rules store when governance is off.",
		"project": "rg1-project",
	})

	_, err := s.handleStoreRule(context.Background(), args)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "behavioral rules store not initialised"))
	require.Empty(t, writer.created, "flag-off store_rule must not create rule candidates")
}
