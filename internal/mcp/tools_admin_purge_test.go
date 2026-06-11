package mcp

// tools_admin_purge_test.go — T008 unit tests for purge_project admin action.
//
// These tests cover the MCP validation layer (double-entry confirmation, nil-store
// guard, wiring) without a live database. The full integration test (T009) lives in
// internal/db/gorm/purge_store_test.go alongside the other store integration tests.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// TestAdminPurge_MissingProject verifies error when project is absent.
func TestAdminPurge_MissingProject(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})

	args := mustMarshalAdmin(map[string]any{"action": "purge_project"})
	_, err := srv.handleAdmin(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project required")
}

// TestAdminPurge_MissingConfirm verifies error when confirm is absent.
func TestAdminPurge_MissingConfirm(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})

	args := mustMarshalAdmin(map[string]any{"action": "purge_project", "project": "my-proj"})
	_, err := srv.handleAdmin(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation required")
}

// TestAdminPurge_MismatchedConfirm verifies error when confirm != project.
func TestAdminPurge_MismatchedConfirm(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "my-proj",
		"confirm": "wrong-name",
	})
	_, err := srv.handleAdmin(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation required")
	assert.Contains(t, err.Error(), "wrong-name")
}

// TestAdminPurge_NilStore verifies error when purgeStore is not wired.
func TestAdminPurge_NilStore(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	// Do NOT call SetPurgeStore — store remains nil.

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "my-proj",
		"confirm": "my-proj",
	})
	_, err := srv.handleAdmin(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "purge store not available")
}

// TestAdminPurge_SetPurgeStore_Wiring verifies that SetPurgeStore assigns the field.
func TestAdminPurge_SetPurgeStore_Wiring(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	ps := &gormdb.PurgeStore{}
	srv.SetPurgeStore(ps)
	require.Same(t, ps, srv.purgeStore, "SetPurgeStore must assign the purgeStore field")
}

// TestAdminPurge_ActionInAdminActions verifies purge_project is in the adminActions list.
func TestAdminPurge_ActionInAdminActions(t *testing.T) {
	found := false
	for _, a := range adminActions {
		if a == "purge_project" {
			found = true
			break
		}
	}
	assert.True(t, found, "purge_project must be listed in adminActions")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustMarshalAdmin(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
