package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

type continuitySlotTestEnv struct {
	srv     *Server
	store   *dbgorm.Store
	slots   *dbgorm.ContinuitySlotStore
	audits  *dbgorm.AuditStore
	project string
}

func newContinuitySlotTestEnv(t *testing.T) continuitySlotTestEnv {
	t.Helper()
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	project := "test-mcp-continuity-slot-" + uuid.NewString()
	base := newMemoryServerForT007(t, project)
	slots := dbgorm.NewContinuitySlotStore(base.store.GetDB())
	audits := dbgorm.NewAuditStore(base.store.GetDB())
	base.srv.SetContinuitySlotStore(slots)
	base.srv.SetAuditStore(audits)

	t.Cleanup(func() {
		require.NoError(t, base.store.DB.Where("project = ?", project).Delete(&dbgorm.ContinuitySlot{}).Error)
		require.NoError(t, base.store.DB.Exec(
			"DELETE FROM audit_log WHERE action IN (?, ?) AND reason LIKE ?",
			"continuity_slot_set", "continuity_slot_clear", "%project=\""+project+"\"%",
		).Error)
	})
	return continuitySlotTestEnv{srv: base.srv, store: base.store, slots: slots, audits: audits, project: project}
}

func (e continuitySlotTestEnv) createMemory(t *testing.T, project, owner, domain, privacyScope, workstation string) *models.Memory {
	t.Helper()
	memory, err := e.srv.memoryStore.Create(context.Background(), &models.Memory{
		Project:             project,
		Content:             "continuity memory content " + uuid.NewString(),
		Status:              "active",
		Domain:              domain,
		OwnerPrincipal:      owner,
		OwnerPrincipalKind:  "agent",
		AgentVisibility:     models.AgentVisibilityShared,
		PrivacyScope:        privacyScope,
		SourceWorkstationID: workstation,
		SourceSessions:      []string{"source-session"},
	})
	require.NoError(t, err)
	return memory
}

func continuitySlotContext(project, principal string) context.Context {
	ctx := ContextWithProject(ContextWithSession(context.Background(), "raw-session-42"), project)
	return auth.WithIdentity(ctx, auth.ClientWithPrincipal("read-write", "keycard-"+principal, principal, auth.PrincipalKindAgent))
}

func continuitySlotSetArgs(t *testing.T, project string, memoryID int64, expiresAt string) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"action":     "set",
		"project":    project,
		"memory_id":  memoryID,
		"expires_at": expiresAt,
	})
	require.NoError(t, err)
	return body
}

func continuitySlotClearArgs(t *testing.T, project string) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"action": "clear", "project": project})
	require.NoError(t, err)
	return body
}

type continuitySlotQueryProbeKey struct{}

func continuitySlotQueryProbe(t *testing.T, db *gormlib.DB, table string, ctx context.Context) (context.Context, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	marker := uuid.NewString()
	ctx = context.WithValue(ctx, continuitySlotQueryProbeKey{}, marker)
	started := make(chan struct{})
	returned := make(chan struct{})
	var startOnce, returnOnce sync.Once
	callbacks := db.Callback().Query()
	beforeName := "continuity_slot_query_before_" + marker
	afterName := "continuity_slot_query_after_" + marker
	require.NoError(t, callbacks.Before("gorm:query").Register(beforeName, func(tx *gormlib.DB) {
		if tx.Statement.Table == table && tx.Statement.Context.Value(continuitySlotQueryProbeKey{}) == marker {
			startOnce.Do(func() { close(started) })
		}
	}))
	require.NoError(t, callbacks.After("gorm:query").Register(afterName, func(tx *gormlib.DB) {
		if tx.Statement.Table == table && tx.Statement.Context.Value(continuitySlotQueryProbeKey{}) == marker {
			returnOnce.Do(func() { close(returned) })
		}
	}))
	t.Cleanup(func() {
		_ = callbacks.Remove(beforeName)
		_ = callbacks.Remove(afterName)
	})
	return ctx, started, returned
}

func TestContinuitySlot_SetReplacementClearAndAudit(t *testing.T) {
	env := newContinuitySlotTestEnv(t)
	ctx := continuitySlotContext(env.project, "agent/alice")
	first := env.createMemory(t, env.project, "agent/alice", "release", "project", "keycard-agent/alice")
	second := env.createMemory(t, env.project, "agent/alice", "release", "project", "keycard-agent/alice")
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	out, err := env.srv.callTool(ctx, "continuity_slot", continuitySlotSetArgs(t, env.project, first.ID, expiresAt))
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"set","status":"ok"}`, out)

	slot, err := env.slots.Get(ctx, env.project)
	require.NoError(t, err)
	assert.Equal(t, first.ID, slot.MemoryID)
	assert.Equal(t, "release", slot.AuthorityDomain)
	assert.Equal(t, "agent/alice", slot.AuthorityOwnerPrincipal)
	assert.Equal(t, "agent", slot.AuthorityOwnerPrincipalKind)
	assert.Equal(t, time.UTC, slot.ExpiresAt.Location())

	_, err = env.srv.callTool(ctx, "continuity_slot", continuitySlotSetArgs(t, env.project, second.ID, expiresAt))
	require.NoError(t, err)
	slot, err = env.slots.Get(ctx, env.project)
	require.NoError(t, err)
	assert.Equal(t, second.ID, slot.MemoryID, "set replaces the one project slot")

	out, err = env.srv.callTool(ctx, "continuity_slot", continuitySlotClearArgs(t, env.project))
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"clear","status":"ok"}`, out)
	_, err = env.slots.Get(ctx, env.project)
	require.Error(t, err)
	assert.ErrorIs(t, err, gormlib.ErrRecordNotFound)

	entries, err := env.audits.GetByMemory(ctx, second.ID, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	for _, entry := range entries {
		assert.Contains(t, []string{"continuity_slot_set", "continuity_slot_clear"}, entry.Action)
		assert.Equal(t, "principal:agent:agent/alice", entry.Actor)
		assert.Equal(t, "raw-session-42", entry.SourceSessionID)
		assert.Nil(t, entry.BeforeState)
		assert.Nil(t, entry.AfterState)
		assert.NotContains(t, entry.Reason, second.Content)
	}

	out, err = env.srv.callTool(ctx, "continuity_slot", continuitySlotClearArgs(t, env.project))
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"clear","status":"ok"}`, out)
}

func TestContinuitySlot_RejectsMalformedOrExpiredExpiry(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	const project = "canonical-project"
	ctx := continuitySlotContext(project, "agent/alice")
	srv := NewServer(ServerOptions{Version: "test"})

	for _, tc := range []struct {
		name      string
		expiresAt string
		want      string
	}{
		{name: "malformed", expiresAt: "tomorrow", want: "expires_at must be an RFC3339 UTC timestamp"},
		{name: "offset", expiresAt: time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05+00:00"), want: "expires_at must be an RFC3339 UTC timestamp"},
		{name: "expired", expiresAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), want: "expires_at must be in the future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.handleContinuitySlot(ctx, continuitySlotSetArgs(t, project, 1, tc.expiresAt))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestContinuitySlot_DeniesUnqualifiedCallers(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	const project = "canonical-project"
	args := continuitySlotSetArgs(t, project, 1, time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	srv := NewServer(ServerOptions{Version: "test"})

	projectCtx := func(ctx context.Context) context.Context { return ContextWithProject(ctx, project) }
	cases := map[string]context.Context{
		"no identity":   projectCtx(context.Background()),
		"master":        auth.WithIdentity(projectCtx(context.Background()), auth.Admin()),
		"auth disabled": auth.WithIdentity(projectCtx(context.Background()), auth.AuthDisabled()),
		"read only": auth.WithIdentity(projectCtx(context.Background()),
			auth.ClientWithPrincipal("read-only", "keycard-readonly", "agent/alice", auth.PrincipalKindAgent)),
		"missing principal": auth.WithIdentity(projectCtx(context.Background()),
			auth.Client("read-write", "keycard-no-principal")),
		"invalid principal kind": auth.WithIdentity(projectCtx(context.Background()), auth.Identity{
			Role: auth.RoleReadWrite, Source: auth.SourceClient, KeycardID: "keycard-invalid", Principal: "agent/alice", PrincipalKind: auth.PrincipalKind("invalid"),
		}),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := srv.handleContinuitySlot(ctx, args)
			require.EqualError(t, err, continuitySlotAuthorizationError)
		})
	}
}

func TestContinuitySlot_DeniesProjectOwnerAndVisibilityWithoutDisclosure(t *testing.T) {
	env := newContinuitySlotTestEnv(t)
	ctx := continuitySlotContext(env.project, "agent/alice")
	foreignOwner := env.createMemory(t, env.project, "agent/bob", "release", "project", "keycard-agent/bob")
	invisible := env.createMemory(t, env.project, "agent/alice", "release", "private", "keycard-other")
	otherProject := "test-mcp-continuity-slot-other-" + uuid.NewString()
	crossProject := env.createMemory(t, otherProject, "agent/alice", "release", "project", "keycard-agent/alice")
	t.Cleanup(func() {
		require.NoError(t, env.store.DB.Where("project = ?", otherProject).Delete(&dbgorm.Memory{}).Error)
	})

	argsFor := func(memoryID int64) json.RawMessage {
		return continuitySlotSetArgs(t, env.project, memoryID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	}
	for name, args := range map[string]json.RawMessage{
		"owner mismatch":     argsFor(foreignOwner.ID),
		"private visibility": argsFor(invisible.ID),
		"project mismatch":   argsFor(crossProject.ID),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.srv.handleContinuitySlot(ctx, args)
			require.EqualError(t, err, continuitySlotAuthorizationError)
		})
	}
}

func TestContinuitySlot_SetRejectsTemporallyIneligibleTargets(t *testing.T) {
	env := newContinuitySlotTestEnv(t)
	ctx := continuitySlotContext(env.project, "agent/alice")

	for _, tc := range []struct {
		name  string
		field string
		value time.Time
	}{
		{name: "valid from in future", field: "valid_from", value: time.Now().UTC().Add(time.Hour)},
		{name: "valid until in past", field: "valid_until", value: time.Now().UTC().Add(-time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memory := env.createMemory(t, env.project, "agent/alice", "release", "project", "keycard-agent/alice")
			require.NoError(t, env.srv.memoryStore.UpdateLifecycleFields(ctx, memory.ID, map[string]any{tc.field: tc.value}))

			_, err := env.srv.handleContinuitySlot(ctx, continuitySlotSetArgs(t, env.project, memory.ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)))
			require.EqualError(t, err, continuitySlotAuthorizationError)
			_, err = env.slots.Get(ctx, env.project)
			require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
		})
	}
}

func TestContinuitySlot_ClearExpiredSlotWithStoredAuthority(t *testing.T) {
	env := newContinuitySlotTestEnv(t)
	ctx := continuitySlotContext(env.project, "agent/alice")
	memory := env.createMemory(t, env.project, "agent/alice", "release", "project", "keycard-agent/alice")
	expiresAt := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, env.slots.Upsert(ctx, dbgorm.ContinuitySlot{
		Project:                     env.project,
		MemoryID:                    memory.ID,
		ExpiresAt:                   expiresAt,
		AuthorityDomain:             memory.Domain,
		AuthorityOwnerPrincipal:     memory.OwnerPrincipal,
		AuthorityOwnerPrincipalKind: memory.OwnerPrincipalKind,
	}))
	_, err := env.srv.handleContinuitySlot(continuitySlotContext(env.project, "agent/bob"), continuitySlotClearArgs(t, env.project))
	require.EqualError(t, err, continuitySlotAuthorizationError)

	out, err := env.srv.handleContinuitySlot(ctx, continuitySlotClearArgs(t, env.project))
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"clear","status":"ok"}`, out)
	_, err = env.slots.Get(ctx, env.project)
	require.ErrorIs(t, err, gormlib.ErrRecordNotFound)

	entries, err := env.audits.GetByMemory(ctx, memory.ID, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "continuity_slot_clear", entries[0].Action)
	assert.Equal(t, "principal:agent:agent/alice", entries[0].Actor)
}

func TestContinuitySlot_ClearRequiresStoredAuthority(t *testing.T) {
	env := newContinuitySlotTestEnv(t)
	ownerCtx := continuitySlotContext(env.project, "agent/alice")
	memory := env.createMemory(t, env.project, "agent/alice", "release", "project", "keycard-agent/alice")
	_, err := env.srv.handleContinuitySlot(ownerCtx, continuitySlotSetArgs(t, env.project, memory.ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)))
	require.NoError(t, err)

	_, err = env.srv.handleContinuitySlot(continuitySlotContext(env.project, "agent/bob"), continuitySlotClearArgs(t, env.project))
	require.EqualError(t, err, continuitySlotAuthorizationError)
	slot, err := env.slots.Get(ownerCtx, env.project)
	require.NoError(t, err)
	assert.Equal(t, memory.ID, slot.MemoryID)
}

func TestContinuitySlot_SetRevalidatesLockedTargetAfterInvalidation(t *testing.T) {
	env := newContinuitySlotTestEnv(t)
	memory := env.createMemory(t, env.project, "agent/alice", "release", "project", "keycard-agent/alice")
	ctx, cancel := context.WithTimeout(continuitySlotContext(env.project, "agent/alice"), 5*time.Second)
	defer cancel()
	ctx, queryStarted, queryReturned := continuitySlotQueryProbe(t, env.store.GetDB(), "memories", ctx)

	tx := env.store.GetDB().WithContext(ctx).Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback().Error }()
	require.NoError(t, tx.Exec("SELECT 1 FROM memories WHERE id = ? FOR UPDATE", memory.ID).Error)
	require.NoError(t, tx.Model(&dbgorm.Memory{}).Where("id = ?", memory.ID).Update("status", "superseded").Error)

	setArgs := continuitySlotSetArgs(t, env.project, memory.ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	setResult := make(chan error, 1)
	go func() {
		_, err := env.srv.handleContinuitySlot(ctx, setArgs)
		setResult <- err
	}()
	select {
	case <-queryStarted:
	case <-ctx.Done():
		t.Fatal("set target lookup did not start")
	}
	select {
	case <-queryReturned:
		t.Fatal("set target lookup completed despite the invalidation lock")
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit().Error)

	select {
	case err := <-setResult:
		require.EqualError(t, err, continuitySlotAuthorizationError)
	case <-ctx.Done():
		t.Fatal("set did not finish after invalidation committed")
	}
	_, err := env.slots.Get(ctx, env.project)
	require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
}

func TestContinuitySlot_ClearUsesLockedReplacementAuthority(t *testing.T) {
	env := newContinuitySlotTestEnv(t)
	alice := env.createMemory(t, env.project, "agent/alice", "release", "project", "keycard-agent/alice")
	bob := env.createMemory(t, env.project, "agent/bob", "release", "project", "keycard-agent/bob")
	aliceCtx := continuitySlotContext(env.project, "agent/alice")
	expiresAt := time.Now().UTC().Add(time.Hour)
	_, err := env.srv.handleContinuitySlot(aliceCtx, continuitySlotSetArgs(t, env.project, alice.ID, expiresAt.Format(time.RFC3339)))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(aliceCtx, 5*time.Second)
	defer cancel()
	ctx, queryStarted, queryReturned := continuitySlotQueryProbe(t, env.store.GetDB(), "project_continuity_slots", ctx)
	replacement := dbgorm.ContinuitySlot{
		Project:                     env.project,
		MemoryID:                    bob.ID,
		ExpiresAt:                   expiresAt,
		AuthorityDomain:             bob.Domain,
		AuthorityOwnerPrincipal:     bob.OwnerPrincipal,
		AuthorityOwnerPrincipalKind: bob.OwnerPrincipalKind,
	}
	tx := env.store.GetDB().WithContext(ctx).Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback().Error }()
	require.NoError(t, env.slots.UpsertTx(ctx, tx, replacement))

	clearArgs := continuitySlotClearArgs(t, env.project)
	clearResult := make(chan error, 1)
	go func() {
		_, err := env.srv.handleContinuitySlot(ctx, clearArgs)
		clearResult <- err
	}()
	select {
	case <-queryStarted:
	case <-ctx.Done():
		t.Fatal("clear slot lookup did not start")
	}
	select {
	case <-queryReturned:
		t.Fatal("clear slot lookup completed despite the replacement lock")
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit().Error)

	select {
	case err := <-clearResult:
		require.EqualError(t, err, continuitySlotAuthorizationError)
	case <-ctx.Done():
		t.Fatal("clear did not finish after replacement committed")
	}
	slot, err := env.slots.Get(ctx, env.project)
	require.NoError(t, err)
	assert.Equal(t, bob.ID, slot.MemoryID)
}

func TestContinuitySlot_HandlerFlagsAndDiscovery(t *testing.T) {
	ctx := ContextWithProject(context.Background(), "canonical-project")
	args := json.RawMessage(`{"action":"clear","project":"canonical-project"}`)

	t.Run("continuity flag off", func(t *testing.T) {
		t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
		t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "false")
		_, err := NewServer(ServerOptions{Version: "test"}).handleContinuitySlot(ctx, args)
		require.ErrorContains(t, err, "ENGRAM_CONTINUITY_SLOT_ENABLED=true")
	})
	t.Run("vnext flag off", func(t *testing.T) {
		t.Setenv("ENGRAM_VNEXT_ENABLED", "false")
		t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
		_, err := NewServer(ServerOptions{Version: "test"}).handleContinuitySlot(ctx, args)
		require.ErrorContains(t, err, "ENGRAM_VNEXT_ENABLED=true")
	})
	t.Run("discovery requires flags and stores", func(t *testing.T) {
		t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
		t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetMemoryStore(&dbgorm.MemoryStore{})
		srv.SetContinuitySlotStore(&dbgorm.ContinuitySlotStore{})
		srv.SetAuditStore(&dbgorm.AuditStore{})
		assert.True(t, hasContinuitySlotTool(srv.ListTools()))

		t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "false")
		assert.False(t, hasContinuitySlotTool(srv.ListTools()))
	})
}

func hasContinuitySlotTool(tools []Tool) bool {
	for _, tool := range tools {
		if tool.Name == "continuity_slot" {
			return true
		}
	}
	return false
}
