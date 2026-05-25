package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/db/gorm"
)

// nonNilMemoryStore returns a zero-value *gorm.MemoryStore — the only
// requirement for ListTools() to advertise the memory tools is a non-nil
// pointer (server.go:798). T005 schema tests never reach the store;
// runtime tests for invalid_privacy_scope / invalid_include_scopes abort
// during parameter validation BEFORE any store method is called.
func nonNilMemoryStore() *gorm.MemoryStore {
	return &gorm.MemoryStore{}
}

// TestStoreMemoryToolSchema_T005 verifies the MCP store_memory tool schema
// exposes the privacy_scope + session_id properties added by engram vNext
// Milestone F TG1/T005 (schema-level completion of T004 wire-up).
//
// Asserts:
//   - properties.privacy_scope.type == "string"
//   - properties.privacy_scope.enum == ["private","project","shared","global"]
//   - properties.session_id.type == "string"
//   - the legacy `scope` enum still lists ["project","global"] (RI-F2)
//
// Anti-stub: a server that returns the schema without the privacy_scope
// property fails the first assertion.
func TestStoreMemoryToolSchema_T005(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	props := findToolProperties(t, s.ListTools(), "store_memory")

	privacyScope, ok := props["privacy_scope"].(map[string]any)
	require.True(t, ok, "store_memory schema must expose privacy_scope property")
	require.Equal(t, "string", privacyScope["type"])
	require.ElementsMatch(t,
		[]string{"private", "project", "shared", "global"},
		toStringSlice(privacyScope["enum"]),
		"privacy_scope enum must mirror migration 125 CHECK constraint")

	sessionID, ok := props["session_id"].(map[string]any)
	require.True(t, ok, "store_memory schema must expose session_id property")
	require.Equal(t, "string", sessionID["type"])

	// Legacy scope still present + correct enum (RI-F2).
	legacy, ok := props["scope"].(map[string]any)
	require.True(t, ok, "store_memory schema must still expose legacy `scope` property (RI-F2)")
	require.ElementsMatch(t,
		[]string{"project", "global"},
		toStringSlice(legacy["enum"]),
		"legacy scope enum must remain 2-tier (RI-F2)")
}

// TestRecallMemoryToolSchema_T005 verifies the MCP recall_memory tool schema
// exposes the session_id + include_scopes properties.
func TestRecallMemoryToolSchema_T005(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	props := findToolProperties(t, s.ListTools(), "recall_memory")

	sessionID, ok := props["session_id"].(map[string]any)
	require.True(t, ok, "recall_memory schema must expose session_id property")
	require.Equal(t, "string", sessionID["type"])

	includeScopes, ok := props["include_scopes"].(map[string]any)
	require.True(t, ok, "recall_memory schema must expose include_scopes property")
	require.Equal(t, "array", includeScopes["type"])

	itemsAny, ok := includeScopes["items"]
	require.True(t, ok, "include_scopes must declare items schema")
	items, ok := itemsAny.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "string", items["type"])
	require.ElementsMatch(t,
		[]string{"private", "project", "shared", "global"},
		toStringSlice(items["enum"]),
		"include_scopes items enum must mirror migration 125 CHECK constraint")
}

// TestStoreMemory_InvalidPrivacyScope_StructuredError verifies that
// handleStoreMemory rejects unknown privacy_scope values with the structured
// "invalid_privacy_scope:" error prefix per spec FR-F1 AMEND (T005).
//
// Asserts under ENGRAM_VNEXT_F_ENABLED=true:
//   - error returned
//   - error.Error() starts with "invalid_privacy_scope:"
//   - the offending value is mentioned in the message
func TestStoreMemory_InvalidPrivacyScope_StructuredError(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore() // never actually written to — validation aborts first

	args := mustJSON(t, map[string]any{
		"content":       "fixture content for invalid_privacy_scope rejection",
		"project":       "t005-test",
		"privacy_scope": "ADMIN", // not in the 4-tier enum
	})

	_, err := s.handleStoreMemory(context.Background(), args)
	require.Error(t, err, "invalid privacy_scope must reject")
	require.True(t,
		strings.HasPrefix(err.Error(), "invalid_privacy_scope:"),
		"error must start with 'invalid_privacy_scope:' structured prefix; got %q", err.Error())
	require.Contains(t, err.Error(), `"ADMIN"`, "error must echo offending value")
}

// TestRecallMemory_InvalidIncludeScopes_StructuredError verifies the symmetric
// rejection on the recall side: include_scopes entries outside the 4-tier
// enum produce "invalid_include_scopes:".
func TestRecallMemory_InvalidIncludeScopes_StructuredError(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	args := mustJSON(t, map[string]any{
		"action":         "search",
		"query":          "anything",
		"project":        "t005-test",
		"include_scopes": []string{"project", "ADMIN"}, // ADMIN is invalid
	})

	_, err := s.handleRecall(context.Background(), args)
	require.Error(t, err, "invalid include_scopes value must reject")
	require.True(t,
		strings.HasPrefix(err.Error(), "invalid_include_scopes:"),
		"error must start with 'invalid_include_scopes:' structured prefix; got %q", err.Error())
	require.Contains(t, err.Error(), `"ADMIN"`, "error must echo offending value")
}

// TestStoreMemoryToolSchema_FlagOff_HasNewProperties_ButRuntimeIgnores verifies
// that the schema advertises the new properties unconditionally (clients
// always know they exist) while the runtime ignores them when the flag is OFF
// — preserving RI-F1 byte-identical v6.4.x behavior.
//
// This guards against an over-eager future refactor that would conditionally
// hide schema properties based on env state at ListTools call time, which
// would make schema discovery non-deterministic.
func TestStoreMemoryToolSchema_FlagOff_HasNewProperties_ButRuntimeIgnores(t *testing.T) {
	// Explicitly clear the flag for this subprocess.
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	require.Equal(t, "", os.Getenv("ENGRAM_VNEXT_F_ENABLED"))

	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	props := findToolProperties(t, s.ListTools(), "store_memory")
	require.Contains(t, props, "privacy_scope",
		"schema must advertise privacy_scope unconditionally; runtime decides honor")
	require.Contains(t, props, "session_id",
		"schema must advertise session_id unconditionally; runtime decides honor")

	// Sanity: vnextFEnabled() is the runtime gate; the schema test cannot
	// observe it but the helper must report false here.
	require.False(t, vnextFEnabled(),
		"flag must be off; runtime store_memory handler must skip new-field population per RI-F1")
}

// ---- helpers -----------------------------------------------------------------

func findToolProperties(t *testing.T, tools []Tool, name string) map[string]any {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		props, ok := tool.InputSchema["properties"].(map[string]any)
		require.True(t, ok, "tool %q has no properties map in InputSchema", name)
		return props
	}
	t.Fatalf("tool %q not found in ListTools output", name)
	return nil
}

func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	if ss, ok := v.([]string); ok {
		return ss
	}
	if anyList, ok := v.([]any); ok {
		out := make([]string, 0, len(anyList))
		for _, x := range anyList {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
