package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/models"
)

func TestProjectFromContext_Empty(t *testing.T) {
	got := ProjectFromContext(context.Background())
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func projectContextToolCall(t *testing.T, server *Server, ctx context.Context, name string, arguments map[string]any) (string, error) {
	t.Helper()
	response := server.HandleRequest(ctx, &Request{
		JSONRPC: "2.0", ID: 1, Method: "tools/call",
		Params: mustJSON(t, map[string]any{"name": name, "arguments": arguments}),
	})
	require.NotNil(t, response)
	if response.Error != nil {
		return "", fmt.Errorf("%s", response.Error.Message)
	}
	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Content, 1)
	return result.Content[0].Text, nil
}

func TestProjectContextStoreDefaultsPostgres(t *testing.T) {
	t.Cleanup(func() { reloadConfig(t) })
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "false")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	reloadConfig(t)
	project, explicit := uuid.NewString(), uuid.NewString()
	env := newMemoryServerForT007(t, project)
	t.Cleanup(func() {
		require.NoError(t, env.store.DB.Exec("DELETE FROM memories WHERE project = ?", explicit).Error)
	})
	ctx := ContextWithProject(context.Background(), project)
	for _, selected := range []string{"", project, explicit} {
		t.Run("project="+selected, func(t *testing.T) {
			arguments := map[string]any{"action": "create", "content": "project default " + uuid.NewString(), "type": "decision"}
			if selected != "" {
				arguments["project"] = selected
			}
			out, err := projectContextToolCall(t, env.srv, ctx, "store", arguments)
			require.NoError(t, err)
			id := storedMemoryIDFromMCPResult(t, out)
			row, err := env.srv.memoryStore.Get(ctx, id)
			require.NoError(t, err)
			want := selected
			if want == "" {
				want = project
			}
			require.Equal(t, want, row.Project)
			require.Equal(t, arguments["content"], row.Content)
			require.Equal(t, id, row.ID)
		})
	}
}

func TestProjectContextRuleActivationPostgres(t *testing.T) {
	t.Cleanup(func() { reloadConfig(t) })
	t.Setenv("ENGRAM_RULE_GOVERNANCE_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	project, session := uuid.NewString(), "project-rules-"+uuid.NewString()
	env := newMemoryServerForT007(t, project)
	governance := dbgorm.NewRuleGovernanceStore(env.store.DB)
	env.srv.SetRuleGovernanceStore(governance)
	t.Cleanup(func() {
		require.NoError(t, env.store.DB.Exec("DELETE FROM rule_candidates WHERE source_session_id = ?", session).Error)
	})
	ctx := ContextWithProject(ContextWithSession(auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "project-rule-keycard", "agent/project-rule-owner", auth.PrincipalKindAgent)), session), project)
	for _, tc := range []struct {
		name, scope, privacy, explicit, source, activation string
		enforce                                          bool
	}{
		{name: "project default", scope: "project", source: project, activation: project},
		{name: "legacy global", scope: "global"},
		{name: "privacy global", scope: "project", privacy: "global"},
		{name: "privacy shared", scope: "project", privacy: "shared"},
		{name: "privacy project overrides global", scope: "global", privacy: "project", source: project, activation: project},
		{name: "privacy private overrides global", scope: "global", privacy: "private", source: project, activation: project},
		{name: "explicit global provenance", scope: "global", explicit: project, source: project},
		{name: "enforced global provenance", scope: "global", enforce: true, source: project},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { reloadConfig(t) })
			t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", fmt.Sprint(tc.enforce))
			reloadConfig(t)
			arguments := map[string]any{
				"action": "create", "content": "rule activation " + uuid.NewString(),
				"always_inject": true, "scope": tc.scope,
			}
			if tc.privacy != "" {
				arguments["privacy_scope"] = tc.privacy
			}
			if tc.explicit != "" {
				arguments["project"] = tc.explicit
			}
			out, err := projectContextToolCall(t, env.srv, ctx, "store", arguments)
			require.NoError(t, err)
			candidate, err := governance.GetRuleCandidate(ctx, storedMemoryIDFromMCPResult(t, out))
			require.NoError(t, err)
			require.Equal(t, arguments["content"], candidate.ProposedContent)
			require.Equal(t, tc.source, candidate.SourceProject)
			require.Equal(t, session, candidate.SourceSessionID)
			require.Equal(t, models.RuleCandidatePending, candidate.Status)
			if tc.activation == "" {
				require.Equal(t, "global", candidate.ProposedScope)
				require.NotContains(t, candidate.ActivationPredicate, "project")
			} else {
				require.Equal(t, "project", candidate.ProposedScope)
				require.Equal(t, tc.activation, candidate.ActivationPredicate["project"])
			}
		})
	}
}

func TestProjectContextMemoryMutationsPostgres(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		enforce, same, global, admin, allowed bool
	}{
		{name: "enforced foreign target", enforce: true},
		{name: "unenforced foreign target", allowed: true},
		{name: "enforced same project", enforce: true, same: true, allowed: true},
		{name: "enforced global admin target", enforce: true, global: true, admin: true},
		{name: "unenforced global admin target", global: true, admin: true, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { reloadConfig(t) })
			t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", fmt.Sprint(tc.enforce))
			t.Setenv("ENGRAM_VNEXT_ENABLED", fmt.Sprint(!tc.allowed))
			t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
			t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
			reloadConfig(t)
			project, foreign := uuid.NewString(), uuid.NewString()
			session, domain := "project-mutation-"+uuid.NewString(), "project-warn-"+uuid.NewString()
			env := newMemoryServerForT007(t, project)
			audits := dbgorm.NewAuditStore(env.store.DB)
			env.srv.SetAuditStore(audits)
			domains := dbgorm.NewDomainOwnerStore(env.store)
			env.srv.SetDomainRegistryService(principalmemory.NewDomainRegistryService(domains, audits))
			t.Cleanup(func() {
				require.NoError(t, env.store.DB.Exec("DELETE FROM audit_log WHERE source_session_id = ?", session).Error)
				require.NoError(t, env.store.DB.Exec("DELETE FROM memory_domain_owners WHERE domain = ?", domain).Error)
				require.NoError(t, env.store.DB.Exec("DELETE FROM memories WHERE project IN ?", []string{project, foreign}).Error)
			})
			_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
				Domain: domain, OwnerPrincipal: "agent/domain-owner", OwnerPrincipalKind: "agent", Mode: dbgorm.DomainOwnerModeWarn,
			})
			require.NoError(t, err)
			identity := auth.ClientWithPrincipal("read-write", "project-mutation-keycard", "agent/mutation-writer", auth.PrincipalKindAgent)
			if tc.admin {
				identity = auth.Admin()
			}
			ctx := ContextWithProject(ContextWithSession(auth.WithIdentity(context.Background(), identity), session), project)
			targetProject := foreign
			if tc.same {
				targetProject = project
			}
			privacy := "project"
			if tc.global {
				privacy = "global"
			}
			local, err := env.srv.memoryStore.Create(ctx, &models.Memory{Project: project, Content: session + " local", Status: "active"})
			require.NoError(t, err)
			target, err := env.srv.memoryStore.Create(ctx, &models.Memory{Project: targetProject, Content: session + " target", Status: "active", PrivacyScope: privacy})
			require.NoError(t, err)
			suppressed, err := env.srv.memoryStore.Create(ctx, &models.Memory{Project: targetProject, Content: session + " suppress", Status: "active", PrivacyScope: privacy})
			require.NoError(t, err)
			rows := func() []dbgorm.Memory {
				var result []dbgorm.Memory
				require.NoError(t, env.store.DB.Where("project IN ?", []string{project, foreign}).Order("id").Find(&result).Error)
				return result
			}
			before := rows()
			require.Len(t, before, 3)
			_, err = projectContextToolCall(t, env.srv, ctx, "feedback", map[string]any{"action": "suppress", "id": suppressed.ID})
			if tc.allowed {
				require.NoError(t, err)
				var deleted dbgorm.Memory
				require.NoError(t, env.store.DB.First(&deleted, suppressed.ID).Error)
				require.NotNil(t, deleted.DeletedAt)
			} else {
				require.ErrorContains(t, err, "not found")
				require.Equal(t, before, rows())
			}
			arguments := map[string]any{
				"action": "create", "content": session + " replacement", "session_id": session,
				"supersedes": []int64{local.ID, target.ID},
			}
			if !tc.admin {
				arguments["domain"] = domain
			}
			out, err := projectContextToolCall(t, env.srv, ctx, "store", arguments)
			if !tc.allowed {
				require.ErrorContains(t, err, "not found")
				require.Equal(t, before, rows(), "denied mixed targets must preserve IDs, content, versions, status and timestamps without a replacement")
				var auditCount int64
				require.NoError(t, env.store.DB.Table("audit_log").Where("source_session_id = ?", session).Count(&auditCount).Error)
				require.Zero(t, auditCount, "target preflight must precede domain-write warnings and mutation-history audits")
				return
			}
			require.NoError(t, err)
			require.Len(t, rows(), 4)
			created, err := env.srv.memoryStore.Get(ctx, storedMemoryIDFromMCPResult(t, out))
			require.NoError(t, err)
			require.Equal(t, project, created.Project)
			require.Equal(t, arguments["content"], created.Content)
			require.NotNil(t, created.SupersedesID)
			require.Equal(t, local.ID, *created.SupersedesID)
			for _, id := range []int64{local.ID, target.ID} {
				var changed dbgorm.Memory
				require.NoError(t, env.store.DB.First(&changed, id).Error)
				require.Equal(t, "superseded", changed.Status)
				require.Equal(t, 2, changed.Version)
			}
			if !tc.admin {
				var warnings int64
				require.NoError(t, env.store.DB.Table("audit_log").Where("source_session_id = ? AND action = ?", session, principalmemory.AuditActionDomainWriteWarn).Count(&warnings).Error)
				require.EqualValues(t, 1, warnings)
			}
		})
	}
}
