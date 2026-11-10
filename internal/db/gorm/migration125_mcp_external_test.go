package gorm_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
)

// TestMigration125_TagDerivedBackfillMCPRecall seeds legacy rows before the
// actual migration, then exercises list and MCP recall against the same rows.
func TestMigration125_TagDerivedBackfillMCPRecall(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	fixture := dbgorm.OpenMigration125Fixture(t)
	db := fixture.DB()
	project := uuid.NewString()

	var before int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM migrations WHERE id = ?`, "125_privacy_scope_addition").Scan(&before).Error)
	require.Zero(t, before, "fixture must precede migration 125")
	require.NoError(t, db.Exec(`INSERT INTO projects (id) VALUES (?)`, project).Error)
	for _, row := range []struct {
		content string
		tags    string
	}{
		{"T007 fixture global-tagged", `["scope:global"]`},
		{"T007 fixture project-tagged", `["scope:project"]`},
		{"T007 fixture untagged", `[]`},
	} {
		require.NoError(t, db.Exec(`INSERT INTO memories (project, content, tags) VALUES (?, ?, ?::jsonb)`, project, row.content, row.tags).Error)
	}

	require.NoError(t, fixture.MigrateRemaining(), "run the actual migration chain including 125")
	var applied int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM migrations WHERE id = ?`, "125_privacy_scope_addition").Scan(&applied).Error)
	require.Equal(t, int64(1), applied)

	var rows []struct {
		ID           int64
		Content      string
		PrivacyScope string
		Tags         string
	}
	require.NoError(t, db.Raw(`SELECT id, content, privacy_scope, tags::text AS tags FROM memories WHERE project = ? ORDER BY id`, project).Scan(&rows).Error)
	require.Len(t, rows, 3)
	require.NotZero(t, rows[0].ID)
	require.Equal(t, "global", rows[0].PrivacyScope)
	require.Contains(t, rows[0].Tags, "scope:global")
	require.Equal(t, "project", rows[1].PrivacyScope)
	require.Contains(t, rows[1].Tags, "scope:project")
	require.Equal(t, "project", rows[2].PrivacyScope)
	require.NotContains(t, rows[2].Tags, "scope:")

	store := dbgorm.NewMemoryStore(&dbgorm.Store{DB: db})
	memories, err := store.List(db.Statement.Context, project, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(memories), 3)
	found := false
	for _, memory := range memories {
		if memory.ID == rows[0].ID {
			require.Equal(t, rows[0].Content, memory.Content)
			found = true
		}
	}
	require.True(t, found, "MemoryStore.List must retain the migrated global fixture")

	server := mcp.NewServer(mcp.ServerOptions{Version: "test"})
	server.SetMemoryStore(store)
	params, err := json.Marshal(map[string]any{
		"name": "recall", "arguments": map[string]any{"action": "search", "project": project},
	})
	require.NoError(t, err)
	response := server.HandleRequest(db.Statement.Context, &mcp.Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
	require.NotNil(t, response)
	require.Nil(t, response.Error)
	encoded, err := json.Marshal(response.Result)
	require.NoError(t, err)
	var envelope struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	require.False(t, envelope.IsError)
	require.Len(t, envelope.Content, 1)
	var result struct {
		Memories []struct {
			ID           int64  `json:"id"`
			Content      string `json:"content"`
			PrivacyScope string `json:"privacy_scope"`
		} `json:"memories"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &result))
	require.Len(t, result.Memories, 3)
	for _, memory := range result.Memories {
		if memory.ID == rows[0].ID {
			require.Equal(t, rows[0].Content, memory.Content)
			require.Equal(t, "global", memory.PrivacyScope)
			return
		}
	}
	t.Fatal("MCP recall omitted the migrated global fixture")
}
