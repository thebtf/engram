package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
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
