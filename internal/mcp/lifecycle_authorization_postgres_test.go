package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

func TestLifecycleAuthorizationTransactionPostgres(t *testing.T) {
	useMutationPrincipalSecurityPostgres(t)
	configureMutationPrincipalSecurity(t, true, false)
	f := newMutationPrincipalFixture(t, false)
	owner := f.identity("owner")
	f.seed(t, owner, "private", "private", "", false)
	f.env.srv.SetPromotionStore(dbgorm.NewPromotionStore(f.env.store.DB))
	t.Cleanup(func() {
		require.NoError(t, f.env.store.DB.Exec("DELETE FROM promotion_log WHERE memory_id = ?", f.target.ID).Error)
	})
	pool, err := f.env.store.DB.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4)
	base := ContextWithProject(auth.WithIdentity(t.Context(), owner), f.project)
	ctx := ContextWithSession(base, f.session)
	call := func(t *testing.T, caller context.Context, action string, args map[string]any) *Response {
		t.Helper()
		if args == nil {
			args = map[string]any{}
		}
		args["action"], args["memory_id"] = action, f.target.ID
		return mutationPrincipalToolCall(t, f.env.srv, caller, "lifecycle", args)
	}
	history := func(t *testing.T) int64 {
		t.Helper()
		var total int64
		require.NoError(t, f.env.store.DB.Table("promotion_log").Where("memory_id = ?", f.target.ID).Count(&total).Error)
		return total
	}
	t.Run("owner_readonly_and_workstation_only", func(t *testing.T) {
		readonly := owner
		readonly.Role = auth.RoleReadOnly
		readctx := ContextWithSession(ContextWithProject(auth.WithIdentity(t.Context(), readonly), f.project), f.session)
		for _, action := range []string{"info", "decay_preview"} {
			_ = mutationPrincipalResult(t, call(t, readctx, action, nil))
			_ = mutationPrincipalResult(t, call(t, base, action, nil))
			_ = mutationPrincipalResult(t, call(t, ContextWithSession(readctx, f.session+"-other"), action, nil))
		}
		_ = mutationPrincipalResult(t, call(t, base, "set_confidence", map[string]any{"confidence": 0.42, "reason": "synthetic workstation-only control"}))
	})
	t.Run("wrong_session_no_mutation_effects_or_history", func(t *testing.T) {
		wrong := ContextWithSession(base, f.session+"-other")
		before, err := f.env.srv.memoryStore.Get(t.Context(), f.target.ID)
		require.NoError(t, err)
		count := history(t)
		for _, action := range []string{"promote", "demote", "set_confidence", "set_defeasibility"} {
			response := call(t, wrong, action, map[string]any{"target_tier": "semantic", "confidence": 0.13, "reason": "synthetic denied override", "defeasibility": "rapid"})
			requireMutationPrincipalNotFound(t, response, f.target)
		}
		after, err := f.env.srv.memoryStore.Get(t.Context(), f.target.ID)
		require.NoError(t, err)
		require.Equal(t, before, after)
		require.Equal(t, count, history(t))
	})
	t.Run("tier_history_atomic_success_and_failure", func(t *testing.T) {
		require.NoError(t, f.env.srv.memoryStore.UpdateLifecycleFields(t.Context(), f.target.ID, map[string]any{"tier": "episodic"}))
		start := history(t)
		_ = mutationPrincipalResult(t, call(t, ctx, "promote", map[string]any{"target_tier": "semantic"}))
		require.Equal(t, start+1, history(t))
		_ = mutationPrincipalResult(t, call(t, ctx, "demote", map[string]any{"target_tier": "episodic"}))
		require.Equal(t, start+2, history(t))
		require.NoError(t, f.env.store.DB.Exec(`ALTER TABLE promotion_log ADD CONSTRAINT lifecycle_fixture_history_denied CHECK (reason <> 'manual promotion') NOT VALID`).Error)
		t.Cleanup(func() {
			require.NoError(t, f.env.store.DB.Exec(`ALTER TABLE promotion_log DROP CONSTRAINT lifecycle_fixture_history_denied`).Error)
		})
		before, err := f.env.srv.memoryStore.Get(t.Context(), f.target.ID)
		require.NoError(t, err)
		response := call(t, ctx, "promote", map[string]any{"target_tier": "semantic"})
		require.NotNil(t, response.Error, "history persistence failure must reject the mutation")
		after, err := f.env.srv.memoryStore.Get(t.Context(), f.target.ID)
		require.NoError(t, err)
		require.Equal(t, before, after, "history failure must roll back tier and updated_at")
		require.Equal(t, start+2, history(t))
	})
	t.Run("scope_rechecked_after_row_lock_wait", func(t *testing.T) {
		require.NoError(t, f.env.store.DB.Model(&dbgorm.Memory{}).Where("id = ?", f.target.ID).Updates(map[string]any{"privacy_scope": "project", "agent_visibility": "shared", "tier": "episodic"}).Error)
		caller := ContextWithProject(ContextWithSession(auth.WithIdentity(t.Context(), f.identity("other")), f.session), f.project)
		requestCtx, cancel := context.WithTimeout(caller, 15*time.Second)
		defer cancel()
		holder := f.env.store.DB.WithContext(requestCtx).Begin()
		require.NoError(t, holder.Error)
		defer holder.Rollback()
		var holderPID int
		require.NoError(t, holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error)
		require.NoError(t, holder.Exec("UPDATE memories SET agent_visibility = 'private' WHERE id = ?", f.target.ID).Error)
		count := history(t)
		result, finished := make(chan *Response, 1), make(chan struct{})
		go func() {
			defer close(finished)
			result <- call(t, requestCtx, "promote", map[string]any{"target_tier": "semantic"})
		}()
		t.Cleanup(func() {
			cancel()
			_ = holder.Rollback().Error
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Error("lifecycle handler did not stop before fixture cleanup")
			}
		})
		waitMutationPrincipalTargetLock(t, f, requestCtx, holderPID, result)
		require.NoError(t, holder.Commit().Error)
		var response *Response
		select {
		case response = <-result:
		case <-requestCtx.Done():
			t.Fatal("lifecycle did not finish after holder commit")
		}
		requireMutationPrincipalNotFound(t, response, f.target)
		after, err := f.env.srv.memoryStore.Get(t.Context(), f.target.ID)
		require.NoError(t, err)
		require.Equal(t, "episodic", after.Tier)
		require.Equal(t, "private", after.AgentVisibility)
		require.Equal(t, count, history(t), "revoked scope must not append lifecycle history")
	})
}
