package mcp

// tools_admin_purge_test.go — T008 unit tests for purge_project admin action.
//
// These tests cover the MCP validation layer (double-entry confirmation, nil-store
// guard, admin authz, vnext gate, wiring) without a live database. The full
// integration test (T009) lives in internal/db/gorm/purge_store_test.go alongside
// the other store integration tests.
//
// NOTE: DB tests that require live Postgres (DSN-gated) are in purge_store_test.go.
// CI has no Postgres; those tests skip automatically when DATABASE_DSN is absent.
// See docs/PRODUCTION-TESTING-PLAYBOOK.md for the live-DB test workflow.

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// adminCtx returns a context carrying an admin identity.
func adminCtx() context.Context {
	return auth.WithIdentity(context.Background(), auth.Admin())
}

// readWriteCtx returns a context carrying a read-write (non-admin) identity.
func readWriteCtx() context.Context {
	return auth.WithIdentity(context.Background(), auth.Client("read-write", "test-keycard-id"))
}

// TestAdminPurge_MissingProject verifies error when project is absent (admin ctx).
func TestAdminPurge_MissingProject(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})

	args := mustMarshalAdmin(map[string]any{"action": "purge_project"})
	_, err := srv.handleAdmin(adminCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project required")
}

// TestAdminPurge_MissingConfirm verifies error when confirm is absent (admin ctx).
func TestAdminPurge_MissingConfirm(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})

	args := mustMarshalAdmin(map[string]any{"action": "purge_project", "project": "my-proj"})
	_, err := srv.handleAdmin(adminCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation required")
}

// TestAdminPurge_MismatchedConfirm verifies error when confirm != project (admin ctx).
func TestAdminPurge_MismatchedConfirm(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "my-proj",
		"confirm": "wrong-name",
	})
	_, err := srv.handleAdmin(adminCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation required")
	assert.Contains(t, err.Error(), "wrong-name")
}

// TestAdminPurge_NilStore verifies error when purgeStore is not wired (admin ctx).
func TestAdminPurge_NilStore(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})
	// Do NOT call SetPurgeStore — store remains nil.

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "my-proj",
		"confirm": "my-proj",
	})
	_, err := srv.handleAdmin(adminCtx(), args)
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

// TestAdminPurge_ActionInAdminActions verifies purge_project is in adminActionsVnext.
func TestAdminPurge_ActionInAdminActions(t *testing.T) {
	found := false
	for _, a := range adminActionsVnext {
		if a == "purge_project" {
			found = true
			break
		}
	}
	assert.True(t, found, "purge_project must be listed in adminActionsVnext")
}

// ---------------------------------------------------------------------------
// Finding 1: Admin authorization gate
// ---------------------------------------------------------------------------

// TestAdminPurge_NonAdminDenied verifies that a non-admin identity (read-write keycard)
// is denied with "admin authorization required".
func TestAdminPurge_NonAdminDenied(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetPurgeStore(&gormdb.PurgeStore{}) // wired so authz gate fires, not nil-guard

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "my-proj",
		"confirm": "my-proj",
	})
	_, err := srv.handleAdmin(readWriteCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin authorization required for purge_project",
		"non-admin identity must be denied with structured error")
}

// TestAdminPurge_NoIdentityDenied verifies that a context with no identity
// (auth disabled path) is denied for purge_project.
func TestAdminPurge_NoIdentityDenied(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetPurgeStore(&gormdb.PurgeStore{})

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "my-proj",
		"confirm": "my-proj",
	})
	_, err := srv.handleAdmin(context.Background(), args) // no identity in ctx
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin authorization required for purge_project",
		"missing identity must be denied — fail closed for destructive action")
}

// TestAdminPurge_AdminAllowed verifies that an admin identity passes the authz gate
// and reaches the purge store (where nil DB causes a panic — proving gate was passed).
func TestAdminPurge_AdminAllowed(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetPurgeStore(&gormdb.PurgeStore{}) // nil DB — panics past authz gate

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "my-proj",
		"confirm": "my-proj",
	})
	// The admin identity passes authz; execution reaches PurgeProject which panics
	// on the nil gorm.DB — this proves the authz gate was passed.
	assert.Panics(t, func() {
		_, _ = srv.handleAdmin(adminCtx(), args)
	}, "admin identity must pass the authz gate and reach PurgeProject (nil DB panics)")
}

// ---------------------------------------------------------------------------
// Finding 6: vnext flag gate
// ---------------------------------------------------------------------------

// TestAdminPurge_FlagOff_RejectsAsUnknown verifies that purge_project returns
// "unknown admin action" when ENGRAM_VNEXT_ENABLED != "true", byte-identical to
// the pre-branch behaviour for truly unknown actions.
func TestAdminPurge_FlagOff_RejectsAsUnknown(t *testing.T) {
	os.Unsetenv("ENGRAM_VNEXT_ENABLED")
	srv := NewServer(ServerOptions{Version: "test"})

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "my-proj",
		"confirm": "my-proj",
	})
	_, err := srv.handleAdmin(adminCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown admin action",
		"flag-off: purge_project must be rejected as unknown action")
	assert.Contains(t, err.Error(), `"purge_project"`,
		"unknown action error must include the action name")
}

// TestAdminPurge_FlagOff_SchemaLacksConfirm verifies that when ENGRAM_VNEXT_ENABLED
// is not "true", the admin tool schema does not contain purge_project or confirm.
func TestAdminPurge_FlagOff_SchemaLacksConfirm(t *testing.T) {
	os.Unsetenv("ENGRAM_VNEXT_ENABLED")
	tool := buildAdminTool()

	// Description must not mention purge_project.
	assert.NotContains(t, tool.Description, "purge_project",
		"flag-off: admin tool description must not mention purge_project")

	// Schema properties must not contain confirm field.
	props, _ := tool.InputSchema["properties"].(map[string]any)
	require.NotNil(t, props, "InputSchema must have properties")
	_, hasConfirm := props["confirm"]
	assert.False(t, hasConfirm,
		"flag-off: admin tool schema must not have confirm field")

	// project description must not mention purge_project.
	if projectProp, ok := props["project"].(map[string]any); ok {
		desc, _ := projectProp["description"].(string)
		assert.NotContains(t, desc, "purge_project",
			"flag-off: project field description must not mention purge_project")
	}
}

// TestAdminPurge_FlagOn_SchemaHasConfirm verifies that when ENGRAM_VNEXT_ENABLED=true,
// the admin tool schema contains confirm and purge_project references.
func TestAdminPurge_FlagOn_SchemaHasConfirm(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	tool := buildAdminTool()

	assert.Contains(t, tool.Description, "purge_project",
		"flag-on: admin tool description must mention purge_project")

	props, _ := tool.InputSchema["properties"].(map[string]any)
	require.NotNil(t, props)
	_, hasConfirm := props["confirm"]
	assert.True(t, hasConfirm,
		"flag-on: admin tool schema must have confirm field")
}

// ---------------------------------------------------------------------------
// Finding 7: whitespace project
// ---------------------------------------------------------------------------

// TestAdminPurge_WhitespaceProject verifies that a whitespace-only project name
// is rejected before reaching the purge store.
func TestAdminPurge_WhitespaceProject(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetPurgeStore(&gormdb.PurgeStore{}) // wired so whitespace guard fires, not nil-guard

	args := mustMarshalAdmin(map[string]any{
		"action":  "purge_project",
		"project": "   ",
		"confirm": "   ",
	})
	_, err := srv.handleAdmin(adminCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project required",
		"whitespace-only project must be rejected as empty")
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
