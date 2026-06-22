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

	agentVisibility, ok := props["agent_visibility"].(map[string]any)
	require.True(t, ok, "store_memory schema must expose agent_visibility property")
	require.Equal(t, "string", agentVisibility["type"])
	require.ElementsMatch(t,
		[]string{"private", "shared"},
		toStringSlice(agentVisibility["enum"]),
		"agent_visibility enum must mirror migration 149 CHECK constraint")

	domain, ok := props["domain"].(map[string]any)
	require.True(t, ok, "store_memory schema must expose domain property")
	require.Equal(t, "string", domain["type"])
	require.NotContains(t, props, "owner_principal", "owner_principal must be server-derived, not a tool argument")

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
	require.Contains(t, props, "agent_visibility",
		"schema must advertise agent_visibility unconditionally; runtime decides owner derivation")
	require.Contains(t, props, "domain",
		"schema must advertise domain unconditionally; runtime decides owner derivation")

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

// ---- B4 tier_filter tests -------------------------------------------------------

// TestRecallMemoryToolSchema_B4_HasTierFilter verifies that recall_memory InputSchema
// advertises the tier_filter property when ENGRAM_VNEXT_ENABLED=true.
//
// Merge note (W3+W4): W3 adopted the recallMemoryTool() function which gates
// tier_filter (retrieval tiers: tier0_exact/tier1_fts/tier1_vector/tier2_graph)
// behind ENGRAM_VNEXT_ENABLED. The lifecycle cognitive-tier filter (FR-B2: working/
// episodic/semantic/procedural) is still applied in handleRecallMemory when
// ENGRAM_LIFECYCLE_ENABLED=true, but is not schema-advertised in the base schema
// (it is a hidden-but-functional parameter to avoid conflicting tier_filter enums).
// This test is updated to reflect the merged schema contract.
func TestRecallMemoryToolSchema_B4_HasTierFilter(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	props := findToolProperties(t, s.ListTools(), "recall_memory")
	tierFilter, ok := props["tier_filter"]
	require.True(t, ok, "recall_memory schema must expose tier_filter property when ENGRAM_VNEXT_ENABLED=true")

	tfMap, ok := tierFilter.(map[string]any)
	require.True(t, ok, "tier_filter must be an object")
	require.Equal(t, "array", tfMap["type"], "tier_filter must be type=array")

	items, ok := tfMap["items"].(map[string]any)
	require.True(t, ok, "tier_filter items must be an object")
	enum := toStringSlice(items["enum"])
	// W3 hybrid tier_filter enum (retrieval tiers, not lifecycle cognitive tiers).
	require.ElementsMatch(t, []string{"tier0_exact", "tier1_fts", "tier1_vector", "tier2_graph"}, enum,
		"tier_filter enum must cover retrieval tiers when ENGRAM_VNEXT_ENABLED=true")
}

// TestRecallMemoryTierFilter_InvalidTier_B4 verifies that recall_memory returns
// an 'invalid_tier_filter:' structured error for unknown tier values when
// ENGRAM_LIFECYCLE_ENABLED=true.
func TestRecallMemoryTierFilter_InvalidTier_B4(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "true")

	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	args := mustJSON(t, map[string]any{
		"query":       "test query",
		"project":     "test-project",
		"tier_filter": []string{"INVALID_TIER"},
	})

	_, err := s.handleRecallMemory(context.Background(), args)
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "invalid_tier_filter:"),
		"error must start with 'invalid_tier_filter:' structured prefix; got %q", err.Error())
	require.Contains(t, err.Error(), `"INVALID_TIER"`, "error must echo offending value")
}

// TestRecallMemoryTierFilter_FlagOff_SchemaAbsent_B4 verifies the merged W3+W4 schema
// contract: tier_filter is NOT present in the base schema when ENGRAM_VNEXT_ENABLED=false,
// consistent with W3's recallMemoryTool() which gates tier_filter behind that flag.
//
// The lifecycle cognitive-tier filter (FR-B2) is still honored at runtime when
// ENGRAM_LIFECYCLE_ENABLED=true — it reads from the tier_filter input key and applies
// the working/episodic/semantic/procedural filter in handleRecallMemory. The field is
// functional but not schema-advertised in the base schema to avoid conflicting enums
// with the W3 hybrid retrieval tier_filter (tier0_exact/tier1_fts/…).
func TestRecallMemoryTierFilter_FlagOff_SchemaAbsent_B4(t *testing.T) {
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "")

	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	// With ENGRAM_VNEXT_ENABLED=false, tier_filter is not in the schema (W3 behavior).
	// The lifecycle tier filter (FR-B2) is a runtime-only parameter in this flag state.
	props := findToolProperties(t, s.ListTools(), "recall_memory")
	_, present := props["tier_filter"]
	require.False(t, present,
		"recall_memory schema must NOT advertise tier_filter when ENGRAM_VNEXT_ENABLED=false (W3+W4 merged contract: tier_filter is a hybrid retrieval param, not the base schema)")
}
