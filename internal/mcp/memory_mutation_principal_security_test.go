package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/bulkops"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
)

// These are post-hoc MCP-boundary regressions, not the original lifecycle RED.
// ENGRAM_MEMORY_MUTATION_TEST_DSN must explicitly select disposable PostgreSQL with the normal Engram migrations.
func TestMemoryMutationPrincipalSecurityPostgres(t *testing.T) {
	useMutationPrincipalSecurityPostgres(t)
	cases := []struct {
		name           string
		caller         string
		visibility     string
		privacy        string
		domainOwned    bool
		unowned        bool
		foreignProject bool
		missingProject bool
		enforce        bool
		legacyFlags    bool
		allowed        bool
	}{
		{name: "foreign_private_empty_domain_flags_off", caller: "other", visibility: "private"},
		{name: "foreign_private_empty_domain_flags_on", caller: "other", visibility: "private", legacyFlags: true},
		{name: "owner_private_empty_domain", caller: "owner", visibility: "private", enforce: true, legacyFlags: true, allowed: true},
		{name: "same_principal_wrong_kind", caller: "wrong_kind", visibility: "private"},
		{name: "foreign_shared_empty_domain", caller: "other", visibility: "shared", enforce: true, allowed: true},
		{name: "foreign_shared_owned_domain", caller: "other", visibility: "shared", domainOwned: true},
		{name: "owner_shared_owned_domain", caller: "owner", visibility: "shared", domainOwned: true, allowed: true},
		{name: "enforced_foreign_shared_project", caller: "other", visibility: "shared", foreignProject: true, enforce: true},
		{name: "unenforced_foreign_owner_project", caller: "owner", visibility: "private", foreignProject: true, allowed: true},
		{name: "enforced_missing_trusted_project", caller: "owner", visibility: "private", missingProject: true, enforce: true},
		{name: "admin_not_principal_private_bypass", caller: "admin", visibility: "private"},
		{name: "unenforced_global_admin_compatibility", caller: "admin", privacy: "global", unowned: true, foreignProject: true, allowed: true},
		{name: "enforced_global_admin_project_boundary", caller: "admin", privacy: "global", unowned: true, foreignProject: true, enforce: true},
	}
	for _, operation := range []string{"suppress", "edit", "supersedes"} {
		for _, tc := range cases {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				configureMutationPrincipalSecurity(t, tc.enforce, tc.legacyFlags)
				f := newMutationPrincipalFixture(t, tc.foreignProject)
				identity := f.identity(tc.caller)
				privacy := tc.privacy
				if privacy == "" {
					privacy = "project"
				}
				domain := ""
				if tc.domainOwned {
					domain = f.domain + "-target"
				}
				f.seed(t, identity, tc.visibility, privacy, domain, tc.unowned)
				ctx := ContextWithSession(auth.WithIdentity(context.Background(), identity), f.session)
				if !tc.missingProject {
					ctx = ContextWithProject(ctx, f.project)
				}

				before := f.snapshot(t)
				tool, arguments := f.arguments(operation, f.target.ID, identity)
				response := mutationPrincipalToolCall(t, f.env.srv, ctx, tool, arguments)
				if !tc.allowed {
					requireMutationPrincipalNotFound(t, response, f.target)
					require.Equal(t, before, f.snapshot(t), "denial must preserve full rows, versions, slots, audit history and replacement absence")

					// A missing selector and an invisible selector have the same public error.
					var missingID int64
					require.NoError(t, f.env.store.DB.Raw("SELECT COALESCE(MAX(id), 0) + 1000000 FROM memories").Scan(&missingID).Error)
					_, missingArguments := f.arguments(operation, missingID, identity)
					missing := mutationPrincipalToolCall(t, f.env.srv, ctx, tool, missingArguments)
					requireMutationPrincipalNotFound(t, missing, f.target)
					require.Equal(t, mutationPrincipalErrorShape(t, missing.Error, missingID), mutationPrincipalErrorShape(t, response.Error, f.target.ID))
					if operation == "supersedes" {
						arguments["supersedes"] = []int64{f.target.ID, f.local.ID}
						reversed := mutationPrincipalToolCall(t, f.env.srv, ctx, tool, arguments)
						requireMutationPrincipalNotFound(t, reversed, f.target)
					}
					// Mutation-history audits are detached; observe a bounded absence window
					// as well as the synchronous domain-warning/lifecycle audit state.
					require.Never(t, func() bool {
						var count int64
						err := f.env.store.DB.Table("audit_log").Where("source_session_id = ? OR memory_id IN (SELECT id FROM memories WHERE project IN ?)",
							f.session, []string{f.project, f.targetProject}).Count(&count).Error
						if err != nil {
							t.Logf("checking denied-call audit state: %v", err)
						}
						return err != nil || count != 1
					}, 200*time.Millisecond, 10*time.Millisecond, "denied calls must not enqueue a delayed audit")
					require.Equal(t, before, f.snapshot(t))
					return
				}

				out := mutationPrincipalResult(t, response)
				f.requireSuccess(t, operation, out, identity, before)
			})
		}
	}
}

// Ordinary caller mutations are privacy-scoped; explicit admin maintenance
// retains its separate, snapshot-backed cross-principal authority.
func TestMemoryMutationPrincipalSecurityAdminMaintenancePostgres(t *testing.T) {
	useMutationPrincipalSecurityPostgres(t)
	for _, tool := range []string{"bulk_delete", "bulk_supersede"} {
		t.Run(tool, func(t *testing.T) {
			configureMutationPrincipalSecurity(t, true, true)
			f := newMutationPrincipalFixture(t, true)
			f.seed(t, f.identity("other"), "private", "project", "", false)
			snapshots := dbgorm.NewSnapshotStore(f.env.store.DB)
			f.env.srv.SetBulkFacade(bulkops.NewFacade(snapshots, nil, f.env.srv.memoryStore, dbgorm.NewAuditStore(f.env.store.DB)))
			ctx := ContextWithProject(ContextWithSession(context.Background(), f.session), f.project)
			arguments := map[string]any{"memory_ids": []int64{f.target.ID}, "dry_run": false}
			before := f.snapshot(t)

			denied := mutationPrincipalToolCall(t, f.env.srv, auth.WithIdentity(ctx, f.identity("other")), tool, arguments)
			require.NotNil(t, denied.Error)
			require.Contains(t, denied.Error.Message, "admin_required")
			requireMutationPrincipalNoLeak(t, denied, f.target)
			require.Equal(t, before, f.snapshot(t))

			arguments["dry_run"] = true
			preview := mutationPrincipalResult(t, mutationPrincipalToolCall(t, f.env.srv, auth.WithIdentity(ctx, auth.Admin()), tool, arguments))
			var previewBody struct {
				DryRun      bool   `json:"dry_run"`
				WouldAffect int    `json:"would_affect"`
				SnapshotID  string `json:"snapshot_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(preview), &previewBody))
			require.True(t, previewBody.DryRun)
			require.Equal(t, 1, previewBody.WouldAffect)
			require.Empty(t, previewBody.SnapshotID)
			require.Equal(t, before, f.snapshot(t), "real-store maintenance preview must not change memory, slot, audit or snapshot")

			arguments["dry_run"] = false
			out := mutationPrincipalResult(t, mutationPrincipalToolCall(t, f.env.srv, auth.WithIdentity(ctx, auth.Admin()), tool, arguments))
			var result struct {
				AffectedCount int      `json:"affected_count"`
				SnapshotID    string   `json:"snapshot_id"`
				Errors        []string `json:"errors"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &result))
			require.Equal(t, 1, result.AffectedCount)
			require.Empty(t, result.Errors)
			require.NotEmpty(t, result.SnapshotID)
			snapshot, err := snapshots.Get(ctx, result.SnapshotID)
			require.NoError(t, err)
			require.Equal(t, []int64{f.target.ID}, snapshot.AffectedMemoryIDs)
			row := f.row(t, f.target.ID)
			require.Equal(t, f.target.Content, row.Content)
			require.Equal(t, 2, row.Version)
			if tool == "bulk_delete" {
				require.NotNil(t, row.DeletedAt)
			} else {
				require.Equal(t, "superseded", row.Status)
				require.Nil(t, row.DeletedAt)
			}
			var slotCount, auditCount int64
			require.NoError(t, f.env.store.DB.Model(&dbgorm.ContinuitySlot{}).Where("project = ?", f.targetProject).Count(&slotCount).Error)
			require.Zero(t, slotCount)
			require.NoError(t, f.env.store.DB.Table("audit_log").Where("action = ? AND reason LIKE ?", tool, "%snapshot="+result.SnapshotID+"%").Count(&auditCount).Error)
			require.EqualValues(t, 1, auditCount)
			require.Equal(t, before.witness, f.rowJSON(t, f.witness.ID))
			require.Equal(t, before.local, f.rowJSON(t, f.local.ID))
		})
	}
}

type mutationPrincipalFixture struct {
	env           t007TestEnv
	project       string
	targetProject string
	session       string
	owner         string
	other         string
	domain        string
	local         *models.Memory
	target        *models.Memory
	witness       *models.Memory
}

type mutationPrincipalSnapshot struct {
	memories string
	slots    string
	audits   string
	bulk     string
	local    string
	witness  string
}

func useMutationPrincipalSecurityPostgres(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("ENGRAM_MEMORY_MUTATION_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_MEMORY_MUTATION_TEST_DSN not set; requires explicit disposable PostgreSQL")
	}
	t.Setenv("DATABASE_DSN", dsn)
}

func configureMutationPrincipalSecurity(t *testing.T, enforce, legacyFlags bool) {
	t.Helper()
	t.Cleanup(func() { reloadConfig(t) })
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", strconv.FormatBool(enforce))
	t.Setenv("ENGRAM_VNEXT_ENABLED", strconv.FormatBool(legacyFlags))
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", strconv.FormatBool(legacyFlags))
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", strconv.FormatBool(legacyFlags))
	reloadConfig(t)
}

func newMutationPrincipalFixture(t *testing.T, foreignProject bool) *mutationPrincipalFixture {
	t.Helper()
	project := uuid.NewString()
	f := &mutationPrincipalFixture{
		env: newMemoryServerForT007(t, project), project: project, targetProject: project,
		session: "mcp-mutation-security-" + uuid.NewString(),
		owner:   "agent/mcp-owner-" + uuid.NewString(), other: "agent/mcp-other-" + uuid.NewString(),
		domain: "mcp-security-warning-" + uuid.NewString(),
	}
	if foreignProject {
		f.targetProject = uuid.NewString()
	}
	audits := dbgorm.NewAuditStore(f.env.store.DB)
	f.env.srv.SetAuditStore(audits)
	f.env.srv.SetContinuitySlotStore(dbgorm.NewContinuitySlotStore(f.env.store.DB))
	domains := dbgorm.NewDomainOwnerStore(f.env.store)
	f.env.srv.SetDomainRegistryService(principalmemory.NewDomainRegistryService(domains, audits))
	t.Cleanup(func() {
		projects := []string{f.project, f.targetProject}
		require.NoError(t, f.env.store.DB.Exec(`DELETE FROM audit_log a WHERE source_session_id = ? OR actor = ?
			OR memory_id IN (SELECT id FROM memories WHERE project IN ?)
			OR EXISTS (SELECT 1 FROM bulk_op_snapshots s
				WHERE s.affected_memory_ids && ARRAY(SELECT id FROM memories WHERE project IN ?)
				AND a.reason LIKE '%' || s.snapshot_id || '%')`, f.session, f.session, projects, projects).Error)
		require.NoError(t, f.env.store.DB.Exec("DELETE FROM bulk_op_snapshots WHERE affected_memory_ids && ARRAY(SELECT id FROM memories WHERE project IN ?)", projects).Error)
		require.NoError(t, f.env.store.DB.Exec("DELETE FROM project_continuity_slots WHERE project IN ?", projects).Error)
		require.NoError(t, f.env.store.DB.Exec("DELETE FROM memory_domain_owners WHERE domain = ?", f.domain).Error)
		require.NoError(t, f.env.store.DB.Exec("DELETE FROM memories WHERE project IN ?", projects).Error)
		require.NoError(t, f.env.store.DB.Where("id IN ?", projects).Delete(&dbgorm.Project{}).Error)
	})
	require.NoError(t, f.env.store.DB.Create(&dbgorm.Project{ID: f.project}).Error)
	if f.targetProject != f.project {
		require.NoError(t, f.env.store.DB.Create(&dbgorm.Project{ID: f.targetProject}).Error)
	}
	_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
		Domain: f.domain, OwnerPrincipal: "agent/registry-" + uuid.NewString(),
		OwnerPrincipalKind: "agent", Mode: dbgorm.DomainOwnerModeWarn,
	})
	require.NoError(t, err)
	return f
}

func (f *mutationPrincipalFixture) identity(caller string) auth.Identity {
	switch caller {
	case "owner":
		return auth.ClientWithPrincipal("read-write", "keycard-"+f.owner, f.owner, auth.PrincipalKindAgent)
	case "wrong_kind":
		return auth.ClientWithPrincipal("read-write", "keycard-wrong-kind", f.owner, auth.PrincipalKind("human"))
	case "admin":
		return auth.Admin()
	default:
		return auth.ClientWithPrincipal("read-write", "keycard-"+f.other, f.other, auth.PrincipalKindAgent)
	}
}

func (f *mutationPrincipalFixture) seed(t *testing.T, caller auth.Identity, visibility, privacy, domain string, unowned bool) {
	t.Helper()
	ctx := context.Background()
	owner, kind, owned := caller.MemoryOwner()
	localVisibility := ""
	if owned {
		localVisibility = models.AgentVisibilityPrivate
	}
	// The allowed predecessor has the smaller ID: a mixed-target denial must
	// not partly supersede it before encountering the foreign target.
	var err error
	f.local, err = f.env.srv.memoryStore.Create(ctx, &models.Memory{
		Project: f.project, Content: "Synthetic local predecessor " + uuid.NewString(), Status: "active",
		PrivacyScope: "project", OwnerPrincipal: owner, OwnerPrincipalKind: kind, AgentVisibility: localVisibility,
	})
	require.NoError(t, err)
	targetOwner, targetKind := f.owner, "agent"
	if unowned {
		targetOwner, targetKind = "", ""
	}
	f.target, err = f.env.srv.memoryStore.Create(ctx, &models.Memory{
		Project: f.targetProject, Content: "Synthetic confidential target payload " + uuid.NewString(), Status: "active",
		Tags: []string{"security-fixture", "preserve-on-denial"}, PrivacyScope: privacy,
		OwnerPrincipal: targetOwner, OwnerPrincipalKind: targetKind, AgentVisibility: visibility, Domain: domain,
		SourceWorkstationID: "keycard-" + f.owner, SourceSessions: []string{f.session},
	})
	require.NoError(t, err)
	f.witness, err = f.env.srv.memoryStore.Create(ctx, &models.Memory{
		Project: f.project, Content: "Unrelated immutable witness " + uuid.NewString(), Status: "active", PrivacyScope: "project",
	})
	require.NoError(t, err)
	require.NoError(t, dbgorm.NewContinuitySlotStore(f.env.store.DB).Upsert(ctx, dbgorm.ContinuitySlot{
		Project: f.targetProject, MemoryID: f.target.ID, ExpiresAt: time.Now().UTC().Add(time.Hour),
		AuthorityDomain: domain, AuthorityOwnerPrincipal: targetOwner, AuthorityOwnerPrincipalKind: targetKind,
	}))
	require.NoError(t, dbgorm.NewAuditStore(f.env.store.DB).Log(ctx, dbgorm.AuditLogEntry{
		MemoryID: &f.target.ID, Action: "security_fixture", SourceSessionID: f.session, Reason: "existing audit must survive denial",
	}))
}

func (f *mutationPrincipalFixture) arguments(operation string, targetID int64, caller auth.Identity) (string, map[string]any) {
	switch operation {
	case "suppress":
		return "feedback", map[string]any{"action": "suppress", "id": targetID, "project": f.targetProject}
	case "edit":
		return "store", map[string]any{
			"action": "edit", "id": targetID, "narrative": "Authorized edited narrative " + f.session,
			"tags": []string{"edited-security-fixture"}, "project": f.targetProject,
		}
	default:
		arguments := map[string]any{
			"action": "create", "content": f.target.Content, "type": "decision", "session_id": f.session,
			"supersedes": []int64{f.local.ID, targetID}, "project": f.targetProject,
		}
		if _, _, owned := caller.MemoryOwner(); owned {
			arguments["domain"] = f.domain
		}
		return "store", arguments
	}
}

func mutationPrincipalToolCall(t *testing.T, server *Server, ctx context.Context, tool string, arguments map[string]any) *Response {
	t.Helper()
	response := server.HandleRequest(ctx, &Request{
		JSONRPC: "2.0", ID: 1, Method: "tools/call",
		Params: mustJSON(t, map[string]any{"name": tool, "arguments": arguments}),
	})
	require.NotNil(t, response)
	return response
}

func mutationPrincipalResult(t *testing.T, response *Response) string {
	t.Helper()
	require.Nil(t, response.Error, "actual registered MCP tool must succeed")
	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Content, 1)
	require.Equal(t, "text", result.Content[0].Type)
	return result.Content[0].Text
}

func requireMutationPrincipalNoLeak(t *testing.T, response *Response, target *models.Memory) {
	t.Helper()
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	for _, hidden := range []string{
		target.Content, "Synthetic confidential target payload", strings.TrimPrefix(target.Content, "Synthetic confidential target payload "),
		target.OwnerPrincipal, target.SourceWorkstationID, target.Domain,
	} {
		if hidden != "" {
			require.NotContains(t, string(raw), hidden, "MCP error/result/data must not disclose the target")
		}
	}
}

func requireMutationPrincipalNotFound(t *testing.T, response *Response, target *models.Memory) {
	t.Helper()
	require.NotNil(t, response.Error, "foreign or missing target must fail at tools/call")
	require.Equal(t, -32000, response.Error.Code)
	require.Contains(t, response.Error.Message, "not found")
	require.Nil(t, response.Result)
	requireMutationPrincipalNoLeak(t, response, target)
}

func mutationPrincipalErrorShape(t *testing.T, err *Error, id int64) string {
	t.Helper()
	normalized := *err
	selector := fmt.Sprintf("memory %d", id)
	normalized.Message = strings.ReplaceAll(normalized.Message, selector, "memory <memory-id>")
	if data, ok := normalized.Data.(string); ok {
		normalized.Data = strings.ReplaceAll(data, selector, "memory <memory-id>")
	}
	raw, marshalErr := json.Marshal(normalized)
	require.NoError(t, marshalErr)
	return string(raw)
}

func (f *mutationPrincipalFixture) snapshot(t *testing.T) mutationPrincipalSnapshot {
	t.Helper()
	projects := []string{f.project, f.targetProject}
	var result mutationPrincipalSnapshot
	queries := []struct {
		out   *string
		query string
		args  []any
	}{
		{&result.memories, `SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY m.id), '[]'::jsonb)::text FROM memories m WHERE project IN ?`, []any{projects}},
		{&result.slots, `SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.project), '[]'::jsonb)::text FROM project_continuity_slots s WHERE project IN ?`, []any{projects}},
		{&result.audits, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.id), '[]'::jsonb)::text FROM audit_log a
			WHERE source_session_id = ? OR actor = ? OR memory_id IN (SELECT id FROM memories WHERE project IN ?)
			OR EXISTS (SELECT 1 FROM bulk_op_snapshots s
				WHERE s.affected_memory_ids && ARRAY(SELECT id FROM memories WHERE project IN ?)
				AND a.reason LIKE '%' || s.snapshot_id || '%')`, []any{f.session, f.session, projects, projects}},
		{&result.bulk, `SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.id), '[]'::jsonb)::text FROM bulk_op_snapshots s
			WHERE affected_memory_ids && ARRAY(SELECT id FROM memories WHERE project IN ?)`, []any{projects}},
	}
	for _, query := range queries {
		require.NoError(t, f.env.store.DB.Raw(query.query, query.args...).Scan(query.out).Error)
	}
	result.local, result.witness = f.rowJSON(t, f.local.ID), f.rowJSON(t, f.witness.ID)
	return result
}

func (f *mutationPrincipalFixture) rowJSON(t *testing.T, id int64) string {
	t.Helper()
	var result string
	require.NoError(t, f.env.store.DB.Raw("SELECT to_jsonb(m)::text FROM memories m WHERE id = ?", id).Scan(&result).Error)
	require.NotEmpty(t, result)
	return result
}

func (f *mutationPrincipalFixture) row(t *testing.T, id int64) dbgorm.Memory {
	t.Helper()
	var row dbgorm.Memory
	require.NoError(t, f.env.store.DB.First(&row, id).Error)
	return row
}

func (f *mutationPrincipalFixture) requireSuccess(t *testing.T, operation, out string, caller auth.Identity, before mutationPrincipalSnapshot) {
	t.Helper()
	row := f.row(t, f.target.ID)
	require.Equal(t, 2, row.Version)
	require.Equal(t, f.target.OwnerPrincipal, row.OwnerPrincipal)
	require.Equal(t, f.target.OwnerPrincipalKind, row.OwnerPrincipalKind)
	require.Equal(t, f.target.AgentVisibility, row.AgentVisibility)
	require.Equal(t, f.target.Domain, row.Domain)
	expectedAudits := 1 // Existing fixture history.
	var slotCount int64
	require.NoError(t, f.env.store.DB.Model(&dbgorm.ContinuitySlot{}).Where("project = ?", f.targetProject).Count(&slotCount).Error)
	switch operation {
	case "suppress":
		require.Contains(t, out, fmt.Sprintf("Memory %d suppressed", f.target.ID))
		require.NotNil(t, row.DeletedAt)
		require.Equal(t, f.target.Content, row.Content)
		require.Zero(t, slotCount)
		expectedAudits += 2 // Slot lifecycle clear and detached delete history.
		require.Equal(t, before.local, f.rowJSON(t, f.local.ID))
	case "edit":
		require.Equal(t, "Authorized edited narrative "+f.session, row.Content)
		require.Equal(t, models.JSONStringArray{"edited-security-fixture"}, row.Tags)
		require.Equal(t, "active", row.Status)
		require.Nil(t, row.DeletedAt)
		require.EqualValues(t, 1, slotCount)
		require.Equal(t, before.slots, f.snapshot(t).slots)
		expectedAudits++
		require.Equal(t, before.local, f.rowJSON(t, f.local.ID))
	case "supersedes":
		require.Equal(t, "superseded", row.Status)
		require.Nil(t, row.DeletedAt)
		require.Zero(t, slotCount)
		local := f.row(t, f.local.ID)
		require.Equal(t, "superseded", local.Status)
		require.Equal(t, 2, local.Version)
		var result struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Positive(t, result.ID)
		created := f.row(t, result.ID)
		require.Equal(t, f.target.Content, created.Content)
		require.Equal(t, f.targetProject, created.Project)
		require.Equal(t, "active", created.Status, "a superseded identical predecessor must not novelty-flag its replacement")
		require.Equal(t, 1, created.Version)
		require.NotNil(t, created.SupersedesID)
		require.Equal(t, f.local.ID, *created.SupersedesID)
		require.Nil(t, created.DeletedAt)
		owner, kind, owned := caller.MemoryOwner()
		require.Equal(t, owner, created.OwnerPrincipal)
		require.Equal(t, kind, created.OwnerPrincipalKind)
		if owned {
			require.Equal(t, f.domain, created.Domain)
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &body))
			require.Contains(t, body, "domain_warning")
			require.Equal(t, principalmemory.AuditStatusWritten, body["domain_audit_status"])
			expectedAudits++
		}
		expectedAudits += 4 // One slot clear, two supersede events and one create.
	}
	var memoryCount int64
	require.NoError(t, f.env.store.DB.Model(&dbgorm.Memory{}).Where("project IN ?", []string{f.project, f.targetProject}).Count(&memoryCount).Error)
	wantCount := int64(3)
	if operation == "supersedes" {
		wantCount++
	}
	require.Equal(t, wantCount, memoryCount)
	require.Equal(t, before.witness, f.rowJSON(t, f.witness.ID))
	require.Eventually(t, func() bool {
		var count int64
		err := f.env.store.DB.Table("audit_log").Where("source_session_id = ? OR memory_id IN (SELECT id FROM memories WHERE project IN ?)",
			f.session, []string{f.project, f.targetProject}).Count(&count).Error
		return err == nil && count == int64(expectedAudits)
	}, 2*time.Second, 10*time.Millisecond, "successful mutation must persist its real audit history")
	var entries []dbgorm.AuditLogEntry
	require.NoError(t, f.env.store.DB.Where("source_session_id = ? OR memory_id IN (SELECT id FROM memories WHERE project IN ?)",
		f.session, []string{f.project, f.targetProject}).Find(&entries).Error)
	actions := map[string]int{}
	for _, entry := range entries {
		actions[entry.Action]++
	}
	wantActions := map[string]int{"security_fixture": 1}
	switch operation {
	case "suppress":
		wantActions["delete"], wantActions["continuity_slot_lifecycle_clear"] = 1, 1
	case "edit":
		wantActions["update"] = 1
	case "supersedes":
		wantActions["supersede"], wantActions["create"], wantActions["continuity_slot_lifecycle_clear"] = 2, 1, 1
		if _, _, owned := caller.MemoryOwner(); owned {
			wantActions[principalmemory.AuditActionDomainWriteWarn] = 1
		}
	}
	require.Equal(t, wantActions, actions)
}

// This post-hoc race is separate from the original OpenClaw lifecycle RED.
// Phase1 mints the real token; Phase2 must not publish a replacement or warning
// while an independent transaction owns the target's authorization row lock.
func TestMemoryMutationPrincipalSecurityWriteLintPhase2Postgres(t *testing.T) {
	useMutationPrincipalSecurityPostgres(t)
	for _, tc := range []struct {
		name       string
		caller     string
		visibility string
		revoke     string
	}{
		{name: "shared_target_becomes_principal_private", caller: "other", visibility: "shared", revoke: "visibility"},
		{name: "owner_target_deleted_before_locked_recheck", caller: "owner", visibility: "private", revoke: "delete"},
		{name: "owner_positive_control", caller: "owner", visibility: "private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureMutationPrincipalSecurity(t, true, true)
			f := newMutationPrincipalFixture(t, false)
			identity := f.identity(tc.caller)
			f.seed(t, identity, tc.visibility, "project", "", false)
			// Holder, blocked handler and observer require distinct connections.
			pool, err := f.env.store.DB.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(4)
			tokens := writelint.NewTokenStore(writelint.DefaultTokenStoreConfig())
			t.Cleanup(tokens.Close)
			f.env.srv.SetWriteLintOrchestrator(writelint.NewOrchestrator(writelint.OrchestratorConfig{
				MemoryStore: f.env.srv.memoryStore, AuditLogger: dbgorm.NewAuditStore(f.env.store.DB),
				TokenStore: tokens, DupThreshold: 0.85,
			}))
			base := ContextWithProject(ContextWithSession(auth.WithIdentity(context.Background(), identity), f.session), f.project)
			ctx, cancel := context.WithTimeout(base, 15*time.Second)
			t.Cleanup(cancel)
			phase1Out := mutationPrincipalResult(t, mutationPrincipalToolCall(t, f.env.srv, ctx, "store", map[string]any{
				"action": "create", "content": f.target.Content, "project": f.project, "session_id": f.session,
			}))
			var phase1 writelint.Phase1Response
			require.NoError(t, json.Unmarshal([]byte(phase1Out), &phase1))
			require.False(t, phase1.Stored)
			require.NotEmpty(t, phase1.LintSignals)
			require.NotEmpty(t, phase1.ResolutionToken, "use a token actually minted through tools/call")
			var offeredSupersede bool
			for _, option := range phase1.ResolutionOptions {
				offeredSupersede = offeredSupersede || option.Option == "supersede"
			}
			require.True(t, offeredSupersede)
			before := f.snapshot(t)
			holder := f.env.store.DB.WithContext(ContextWithSession(ctx, f.session+"-holder")).Begin()
			require.NoError(t, holder.Error)
			defer func() { _ = holder.Rollback().Error }()
			var holderPID int
			require.NoError(t, holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error)
			require.NoError(t, holder.Exec("SELECT id FROM memories WHERE id = ? FOR UPDATE", f.target.ID).Error)
			switch tc.revoke {
			case "visibility":
				require.NoError(t, holder.Exec(`UPDATE memories SET agent_visibility = ?, version = version + 1, updated_at = now() WHERE id = ?`,
					models.AgentVisibilityPrivate, f.target.ID).Error)
			case "delete":
				require.NoError(t, f.env.srv.memoryStore.DeleteTx(holder.Statement.Context, holder, f.target.ID))
			}
			// Capture only the holder's legitimate changes before Phase2 starts.
			holderView := *f
			holderView.env = t007TestEnv{srv: f.env.srv, store: &dbgorm.Store{DB: holder}}
			afterHolder := holderView.snapshot(t)
			request := &Request{
				JSONRPC: "2.0", ID: 2, Method: "tools/call",
				Params: mustJSON(t, map[string]any{"name": "store", "arguments": map[string]any{
					"action": "create", "content": f.target.Content, "project": f.project, "session_id": f.session,
					"resolution_token": phase1.ResolutionToken, "option": "supersede", "target_memory_id": f.target.ID,
					"domain": f.domain,
				}}),
			}
			result := make(chan *Response, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				result <- f.env.srv.HandleRequest(ctx, request)
			}()
			t.Cleanup(func() {
				cancel()
				_ = holder.Rollback().Error
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("Phase2 handler did not stop before fixture cleanup")
				}
			})
			waitMutationPrincipalTargetLock(t, f, ctx, holderPID, result)
			// Nonfatal so a vulnerable implementation still records the post-release
			// denial and orphan instead of stopping at its premature commit.
			assert.Equal(t, before, f.snapshot(t), "blocked Phase2 must not commit replacement, warning audit, target or slot effects")
			require.NoError(t, holder.Commit().Error)
			var response *Response
			select {
			case response = <-result:
			case <-ctx.Done():
				t.Fatal("Phase2 did not finish after the target holder committed")
			}
			require.NotNil(t, response)
			if tc.revoke != "" {
				requireMutationPrincipalNotFound(t, response, f.target)
				require.Equal(t, afterHolder, f.snapshot(t), "access revocation must leave only the holder's committed changes, no Phase2 replacement or audit")
				return
			}
			var committed writelint.Phase2Response
			out := mutationPrincipalResult(t, response)
			require.NoError(t, json.Unmarshal([]byte(out), &committed))
			require.True(t, committed.Stored)
			require.Equal(t, "supersede_with_candidate", committed.ActionTaken)
			require.Positive(t, committed.MemoryID)
			require.NotEqual(t, f.target.ID, committed.MemoryID)
			created, target := f.row(t, committed.MemoryID), f.row(t, f.target.ID)
			require.Equal(t, f.target.Content, created.Content)
			require.Equal(t, f.project, created.Project)
			require.Equal(t, f.owner, created.OwnerPrincipal)
			require.Equal(t, "agent", created.OwnerPrincipalKind)
			require.Equal(t, f.domain, created.Domain)
			require.Nil(t, created.DeletedAt)
			require.Equal(t, "superseded", target.Status)
			require.NotNil(t, target.SupersededBy)
			require.Equal(t, committed.MemoryID, *target.SupersededBy)
			require.Equal(t, f.target.Content, target.Content)
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &body))
			require.Contains(t, body, "domain_warning")
			require.Equal(t, principalmemory.AuditStatusWritten, body["domain_audit_status"])
			after := f.snapshot(t)
			require.Equal(t, "[]", after.slots)
			require.Equal(t, before.local, after.local)
			require.Equal(t, before.witness, after.witness)
			require.Equal(t, before.bulk, after.bulk)
			var rows []dbgorm.Memory
			require.NoError(t, json.Unmarshal([]byte(after.memories), &rows))
			require.Len(t, rows, 4, "owner success commits exactly one replacement")
			var entries []dbgorm.AuditLogEntry
			require.NoError(t, json.Unmarshal([]byte(after.audits), &entries))
			actions := map[string]int{}
			for _, entry := range entries {
				actions[entry.Action]++
			}
			require.Equal(t, map[string]int{
				"security_fixture": 1, "write_lint_signaled": 1, "continuity_slot_lifecycle_clear": 1,
				"supersede_with_candidate": 1, principalmemory.AuditActionDomainWriteWarn: 1,
			}, actions)
		})
	}
}

func waitMutationPrincipalTargetLock(t *testing.T, f *mutationPrincipalFixture, ctx context.Context, holderPID int, result <-chan *Response) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		require.NoError(t, f.env.store.DB.WithContext(ctx).Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND state = 'active' AND wait_event_type = 'Lock'
			AND wait_event = 'transactionid'
			AND ? = ANY(pg_blocking_pids(pid))
			AND query ILIKE 'SELECT%' AND query ILIKE '%memories%'
		)`, holderPID).Scan(&blocked).Error)
		if blocked {
			return
		}
		select {
		case response := <-result:
			if response == nil {
				t.Fatal("Phase2 returned nil before waiting on the held target lock")
			}
			t.Fatalf("Phase2 returned before waiting on the held target lock: error=%v", response.Error)
		case <-deadline.C:
			t.Fatal("no actual Phase2 target-row lock wait observed in PostgreSQL")
		case <-ctx.Done():
			t.Fatal("request context expired before the target-row lock wait")
		case <-ticker.C:
		}
	}
}
