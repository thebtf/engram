package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
)

func TestRecallMemoryPrincipalDefault_OwnSharedLegacyVisibleOtherPrivateHidden(t *testing.T) {
	project := "pmq-recall-default-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "")

	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, created_at)
			VALUES (?, ?, ?::jsonb, ?, now() - interval '6 minutes')`,
		project, "legacy recall marker unowned row", `["type:fact"]`, "private",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - interval '5 minutes')`,
		project, "alice recall marker private row", `["type:fact"]`, "project", "agent/alice", "agent", "private",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - interval '4 minutes')`,
		project, "bob recall marker shared row", `["type:fact"]`, "project", "agent/bob", "agent", "shared",
	).Error)

	for i := 1; i <= 3; i++ {
		require.NoError(t, db.Exec(
			`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
				VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - (?::int * interval '1 minute'))`,
			project, "bob recall marker private newest "+string(rune('0'+i)), `["type:fact"]`, "project", "agent/bob", "agent", "private", i,
		).Error)
	}

	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
	args, err := json.Marshal(map[string]any{
		"query":   "recall marker",
		"project": project,
		"format":  "items",
		"limit":   3,
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecallMemory(ctx, args)
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 3, "recall must backfill older visible rows when newest rows are another principal's private memory")

	byContent := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		content := row["content"].(string)
		require.False(t, strings.Contains(content, "private newest"),
			"other-principal private row leaked through default recall")
		byContent[content] = row
	}

	legacy := byContent["legacy recall marker unowned row"]
	require.NotNil(t, legacy, "legacy unowned rows must remain eligible")
	require.Empty(t, legacy["owner_principal"])

	ownPrivate := byContent["alice recall marker private row"]
	require.NotNil(t, ownPrivate, "caller must see its own private principal memory")
	require.Equal(t, "agent/alice", ownPrivate["owner_principal"])
	require.Equal(t, "agent", ownPrivate["owner_principal_kind"])
	require.Equal(t, "private", ownPrivate["agent_visibility"])

	shared := byContent["bob recall marker shared row"]
	require.NotNil(t, shared, "shared rows from other principals must remain visible and attributed")
	require.Equal(t, "agent/bob", shared["owner_principal"])
	require.Equal(t, "agent", shared["owner_principal_kind"])
	require.Equal(t, "shared", shared["agent_visibility"])
}

func TestRecallMemoryIncludePrincipals_SchemaAdvertised(t *testing.T) {
	tool := recallMemoryTool()
	props := tool.InputSchema["properties"].(map[string]any)
	raw, ok := props["include_principals"]
	require.True(t, ok, "recall_memory schema must advertise explicit widening control")

	includeSchema := raw.(map[string]any)
	require.Equal(t, "array", includeSchema["type"])
	itemSchema := includeSchema["items"].(map[string]any)
	itemProps := itemSchema["properties"].(map[string]any)
	require.Contains(t, itemProps, "principal")
	require.Contains(t, itemProps, "principal_kind")
	require.ElementsMatch(t, []string{"principal", "principal_kind"}, itemSchema["required"])
}

func TestRecallMemoryIncludePrincipals_ValidationAndPrivacy(t *testing.T) {
	t.Run("rejects duplicate principals", func(t *testing.T) {
		env := newPrincipalRecallEnv(t, "pmq-recall-include-dup-"+uuid.NewString())
		ctx := auth.WithIdentity(context.Background(),
			auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))

		_, err := env.srv.handleRecallMemory(ctx, mustRecallJSON(t, map[string]any{
			"query":   "widen marker",
			"project": env.project,
			"format":  "items",
			"include_principals": []map[string]any{
				{"principal": "agent/bob", "principal_kind": "agent"},
				{"principal": " agent/bob ", "principal_kind": "AGENT"},
			},
		}))

		require.Error(t, err)
		require.Contains(t, err.Error(), "duplicate include_principals")
	})

	t.Run("rejects blank and invalid principals clearly", func(t *testing.T) {
		env := newPrincipalRecallEnv(t, "pmq-recall-include-invalid-"+uuid.NewString())
		ctx := auth.WithIdentity(context.Background(),
			auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))

		_, blankErr := env.srv.handleRecallMemory(ctx, mustRecallJSON(t, map[string]any{
			"query":   "widen marker",
			"project": env.project,
			"format":  "items",
			"include_principals": []map[string]any{
				{"principal": " ", "principal_kind": "agent"},
			},
		}))
		require.Error(t, blankErr)
		require.Contains(t, blankErr.Error(), "principal is required")

		_, invalidKindErr := env.srv.handleRecallMemory(ctx, mustRecallJSON(t, map[string]any{
			"query":   "widen marker",
			"project": env.project,
			"format":  "items",
			"include_principals": []map[string]any{
				{"principal": "agent/bob", "principal_kind": "robot"},
			},
		}))
		require.Error(t, invalidKindErr)
		require.Contains(t, invalidKindErr.Error(), "principal_kind must be one of")
	})

	t.Run("self include is allowed and deduplicated", func(t *testing.T) {
		env := newPrincipalRecallEnv(t, "pmq-recall-include-self-"+uuid.NewString())
		insertPrincipalMemory(t, env, "alice widen marker private self", "agent/alice", "agent", "private")
		ctx := auth.WithIdentity(context.Background(),
			auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))

		out, err := env.srv.handleRecallMemory(ctx, mustRecallJSON(t, map[string]any{
			"query":   "widen marker",
			"project": env.project,
			"format":  "items",
			"include_principals": []map[string]any{
				{"principal": "agent/alice", "principal_kind": "agent"},
			},
		}))
		require.NoError(t, err)

		rows := decodeRecallItems(t, out)
		require.Len(t, rows, 1)
		require.Equal(t, "agent/alice", rows[0]["owner_principal"])
		require.Equal(t, "private", rows[0]["agent_visibility"])
	})

	t.Run("non-admin cross-private include is denied", func(t *testing.T) {
		env := newPrincipalRecallEnv(t, "pmq-recall-include-nonadmin-"+uuid.NewString())
		insertPrincipalMemory(t, env, "bob widen marker private denied", "agent/bob", "agent", "private")
		ctx := auth.WithIdentity(context.Background(),
			auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))

		_, err := env.srv.handleRecallMemory(ctx, mustRecallJSON(t, map[string]any{
			"query":   "widen marker",
			"project": env.project,
			"format":  "items",
			"include_principals": []map[string]any{
				{"principal": "agent/bob", "principal_kind": "agent"},
			},
		}))

		require.Error(t, err)
		require.Contains(t, err.Error(), "include_private for another principal requires admin")
	})

	t.Run("admin cross-private include writes durable audit before returning private row", func(t *testing.T) {
		env := newPrincipalRecallEnv(t, "pmq-recall-include-admin-"+uuid.NewString())
		bobID := insertPrincipalMemory(t, env, "bob widen marker private audited", "agent/bob", "agent", "private")
		var before int64
		require.NoError(t, env.store.DB.Model(&dbgorm.AuditLogEntry{}).
			Where("memory_id = ? AND action = ?", bobID, "principal_memory_private_read").
			Count(&before).Error)
		ctx := auth.WithIdentity(context.Background(), auth.Admin())

		out, err := env.srv.handleRecallMemory(ctx, mustRecallJSON(t, map[string]any{
			"query":      "widen marker",
			"project":    env.project,
			"format":     "items",
			"limit":      5,
			"session_id": "operator-session-1",
			"include_principals": []map[string]any{
				{"principal": "agent/bob", "principal_kind": "agent"},
			},
		}))
		require.NoError(t, err)

		rows := decodeRecallItems(t, out)
		require.Len(t, rows, 1)
		require.Equal(t, "bob widen marker private audited", rows[0]["content"])
		require.Equal(t, "agent/bob", rows[0]["owner_principal"])
		require.Equal(t, "private", rows[0]["agent_visibility"])

		var after int64
		require.NoError(t, env.store.DB.Model(&dbgorm.AuditLogEntry{}).
			Where("memory_id = ? AND action = ?", bobID, "principal_memory_private_read").
			Count(&after).Error)
		require.Greater(t, after, before, "admin widening must write durable audit evidence before returning private data")
	})
}

type principalRecallEnv struct {
	t007TestEnv
	project string
}

func newPrincipalRecallEnv(t *testing.T, project string) principalRecallEnv {
	t.Helper()
	env := newMemoryServerForT007(t, project)
	env.srv.SetPrincipalMemoryQueryService(principalmemory.NewPrincipalMemoryQueryService(
		dbgorm.NewMemoryStore(env.store),
		dbgorm.NewAuditStore(env.store.DB),
	))
	t.Cleanup(func() {
		_ = env.store.DB.WithContext(context.Background()).
			Exec(`DELETE FROM audit_log WHERE action = 'principal_memory_private_read' AND reason = 'admin_private_widening'`).Error
	})
	return principalRecallEnv{t007TestEnv: env, project: project}
}

func insertPrincipalMemory(t *testing.T, env principalRecallEnv, content, principal, principalKind, visibility string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, env.store.DB.Raw(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now())
			RETURNING id`,
		env.project, content, `["type:fact"]`, "project", principal, principalKind, visibility,
	).Scan(&id).Error)
	require.NotZero(t, id)
	return id
}

func mustRecallJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func decodeRecallItems(t *testing.T, out string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	return rows
}
