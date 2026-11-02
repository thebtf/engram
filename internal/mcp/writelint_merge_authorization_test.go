package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/writelint"
)

func TestWriteLintMergeAuthorizationPostgres(t *testing.T) {
	useMutationPrincipalSecurityPostgres(t)
	for _, tc := range []struct {
		name       string
		caller     string
		allowed    bool
		concurrent bool
	}{
		{name: "foreign_private_target_denied_without_audit", caller: "other"},
		{name: "legitimate_owner_positive_control", caller: "owner", allowed: true},
		{name: "owner_merge_preserves_concurrent_target_content", caller: "owner", allowed: true, concurrent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureMutationPrincipalSecurity(t, true, true)
			f := newMutationPrincipalFixture(t, false)
			identity := f.identity(tc.caller)
			f.seed(t, identity, "private", "project", "", false)
			tokens := writelint.NewTokenStore(writelint.DefaultTokenStoreConfig())
			t.Cleanup(tokens.Close)
			f.env.srv.SetWriteLintOrchestrator(writelint.NewOrchestrator(writelint.OrchestratorConfig{
				MemoryStore: f.env.srv.memoryStore, AuditLogger: dbgorm.NewAuditStore(f.env.store.DB),
				TokenStore: tokens, DupThreshold: 0.85,
			}))
			ctx := ContextWithProject(ContextWithSession(auth.WithIdentity(context.Background(), identity), f.session), f.project)
			beforePhase1 := f.snapshot(t)
			phase1Response := mutationPrincipalToolCall(t, f.env.srv, ctx, "store", map[string]any{
				"action": "create", "content": f.local.Content, "project": f.project,
				"session_id": f.session, "domain": f.domain,
			})
			phase1Out := mutationPrincipalResult(t, phase1Response)
			var phase1 writelint.Phase1Response
			require.NoError(t, json.Unmarshal([]byte(phase1Out), &phase1))
			require.False(t, phase1.Stored)
			require.NotEmpty(t, phase1.LintSignals)
			require.NotEmpty(t, phase1.ResolutionToken, "Phase1 must mint the token through actual MCP tools/call")
			var localMergeOffered bool
			for _, option := range phase1.ResolutionOptions {
				if option.Option == "merge_with" && option.MemoryID != nil && *option.MemoryID == f.local.ID {
					localMergeOffered = true
				}
			}
			require.True(t, localMergeOffered, "the legitimate token must come from the caller's own visible memory")
			if !tc.allowed {
				requireMutationPrincipalNoLeak(t, phase1Response, f.target)
			}

			// Phase1's real warning and signal audit are legitimate baseline effects.
			before := f.snapshot(t)
			require.Equal(t, beforePhase1.memories, before.memories)
			require.Equal(t, beforePhase1.slots, before.slots)
			require.Equal(t, beforePhase1.bulk, before.bulk)
			auditActions := func(snapshot mutationPrincipalSnapshot) map[string]int {
				var entries []dbgorm.AuditLogEntry
				require.NoError(t, json.Unmarshal([]byte(snapshot.audits), &entries))
				actions := map[string]int{}
				for _, entry := range entries {
					actions[entry.Action]++
				}
				return actions
			}
			require.Equal(t, map[string]int{
				"security_fixture": 1, "write_lint_signaled": 1, principalmemory.AuditActionDomainWriteWarn: 1,
			}, auditActions(before), "the real warn-mode registry must be active before Phase2")

			arguments := map[string]any{
				"action": "create", "content": f.local.Content, "project": f.project,
				"session_id": f.session, "domain": f.domain,
				"resolution_token": phase1.ResolutionToken, "option": "merge_with", "target_memory_id": f.target.ID,
			}
			wantContent, wantVersion := f.target.Content+"\n\n[merged] "+f.local.Content, 2
			var response *Response
			if tc.concurrent {
				pool, err := f.env.store.DB.DB()
				require.NoError(t, err)
				pool.SetMaxOpenConns(4)
				requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				holder := f.env.store.DB.WithContext(requestCtx).Begin()
				require.NoError(t, holder.Error)
				var holderPID int
				require.NoError(t, holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error)
				freshContent := f.target.Content + "\nConcurrent owner fragment " + f.session
				require.NoError(t, holder.Exec("UPDATE memories SET content = ?, version = version + 1, updated_at = now() WHERE id = ?", freshContent, f.target.ID).Error)
				request := &Request{
					JSONRPC: "2.0", ID: 2, Method: "tools/call",
					Params: mustJSON(t, map[string]any{"name": "store", "arguments": arguments}),
				}
				result := make(chan *Response, 1)
				finished := make(chan struct{})
				t.Cleanup(func() {
					cancel()
					_ = holder.Rollback().Error
					select {
					case <-finished:
					case <-time.After(3 * time.Second):
						t.Error("merge handler did not stop before fixture cleanup")
					}
				})
				go func() {
					defer close(finished)
					result <- f.env.srv.HandleRequest(requestCtx, request)
				}()
				require.Eventually(t, func() bool {
					select {
					case <-finished:
						t.Fatal("merge returned before waiting on the concurrent target writer")
					default:
					}
					var blocked bool
					require.NoError(t, f.env.store.DB.WithContext(requestCtx).Raw(`SELECT EXISTS (
						SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
						AND state = 'active' AND wait_event_type = 'Lock'
						AND ? = ANY(pg_blocking_pids(pid)) AND query ILIKE '%memories%'
					)`, holderPID).Scan(&blocked).Error)
					return blocked
				}, 3*time.Second, 10*time.Millisecond, "observe the real merge backend blocked by the exact holder PID")
				t.Logf("Observed merge blocked by PostgreSQL holder PID %d", holderPID)
				require.NoError(t, holder.Commit().Error)
				select {
				case response = <-result:
				case <-requestCtx.Done():
					t.Fatal("merge did not complete after the concurrent writer committed")
				}
				wantContent, wantVersion = freshContent+"\n\n[merged] "+f.local.Content, 3
			} else {
				response = mutationPrincipalToolCall(t, f.env.srv, ctx, "store", arguments)
			}
			if !tc.allowed {
				requireMutationPrincipalNotFound(t, response, f.target)
				after := f.snapshot(t)
				assert.Equal(t, before.memories, after.memories, "denial must preserve all target/replacement rows and versions")
				assert.Equal(t, before.slots, after.slots, "denial must preserve the target continuity slot")
				assert.Equal(t, before.bulk, after.bulk, "denial must not create a maintenance snapshot")
				assert.Equal(t, before.local, after.local)
				assert.Equal(t, before.witness, after.witness)
				t.Logf("Phase2 denied: audit actions before=%v after=%v", auditActions(before), auditActions(after))
				assert.Never(t, func() bool {
					return f.snapshot(t).audits != before.audits
				}, 200*time.Millisecond, 10*time.Millisecond, "denied merge_with must preserve the exact post-Phase1 audit snapshot, including delayed audit effects")
				return
			}

			out := mutationPrincipalResult(t, response)
			var committed writelint.Phase2Response
			require.NoError(t, json.Unmarshal([]byte(out), &committed))
			require.True(t, committed.Stored)
			require.Equal(t, "merge", committed.ActionTaken)
			require.Equal(t, f.target.ID, committed.MemoryID)
			target := f.row(t, f.target.ID)
			assert.Equal(t, wantContent, target.Content, "merge must append to the latest authorized target content")
			require.Equal(t, f.owner, target.OwnerPrincipal)
			require.Equal(t, "agent", target.OwnerPrincipalKind)
			require.Equal(t, "private", target.AgentVisibility)
			require.Equal(t, f.target.Domain, target.Domain)
			require.Equal(t, wantVersion, target.Version)
			require.Nil(t, target.DeletedAt)
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &body))
			require.Contains(t, body, "domain_warning")
			require.Equal(t, principalmemory.AuditStatusWritten, body["domain_audit_status"])
			require.Eventually(t, func() bool {
				return auditActions(f.snapshot(t))["merge"] == 1
			}, 2*time.Second, 10*time.Millisecond, "owner merge must persist its real audit")
			after := f.snapshot(t)
			require.Equal(t, before.slots, after.slots)
			require.Equal(t, before.bulk, after.bulk)
			require.Equal(t, before.local, after.local)
			require.Equal(t, before.witness, after.witness)
			var rows []dbgorm.Memory
			require.NoError(t, json.Unmarshal([]byte(after.memories), &rows))
			require.Len(t, rows, 3, "owner merge updates the target without creating a replacement")
			require.Equal(t, map[string]int{
				"security_fixture": 1, "write_lint_signaled": 1, "merge": 1,
				principalmemory.AuditActionDomainWriteWarn: 2,
			}, auditActions(after))
		})
	}
}
