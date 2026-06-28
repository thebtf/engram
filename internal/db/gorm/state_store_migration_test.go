package gorm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatePlaneMigrationCreatesDedicatedTables(t *testing.T) {
	schema := migrationSchema(t)

	sessionTable, ok := schema.Table("agent_session_state")
	require.True(t, ok, "agent_session_state must be created by the migration ledger")
	require.False(t, sessionTable.Dropped, "agent_session_state must be live")
	require.Equal(t, "152_agent_state_plane", sessionTable.CreatingMigrationID)
	require.Contains(t, sessionTable.CreateSQL, "focus")
	require.Contains(t, sessionTable.CreateSQL, "execution")
	require.Contains(t, sessionTable.CreateSQL, "horizons")
	require.Contains(t, strings.ToLower(sessionTable.CreateSQL), "jsonb")

	projectTable, ok := schema.Table("agent_project_state")
	require.True(t, ok, "agent_project_state must be created by the migration ledger")
	require.False(t, projectTable.Dropped, "agent_project_state must be live")
	require.Equal(t, "152_agent_state_plane", projectTable.CreatingMigrationID)
	require.Contains(t, projectTable.CreateSQL, "updated_by")
	require.Contains(t, projectTable.CreateSQL, "deadline_date")
}
