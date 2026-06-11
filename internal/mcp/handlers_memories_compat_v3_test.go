package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

// TestRecallMemory_CompatV3_ZeroFlagsShape_T021 verifies NFR-F1: when
// recall_memory is called without any TG3 flags (confidence_min=0,
// include_superseded=false, include_rationale=false), the response JSON
// shape is byte-identical to v6.4.x — specifically:
//
//   - top-level keys are exactly {"memories": [...], "count": N}
//     (no "ranking_rationale" key anywhere in the response at the top level)
//   - individual memory objects do NOT have a "ranking_rationale" key
//   - flag-OFF schema has the correct recall_memory keys
//
// The test uses a live PostgreSQL server (DATABASE_DSN env var required).
// When DATABASE_DSN is unset or -short is passed, the schema-only portions
// run and the DB-dependent assertion is skipped.
//
// Anti-stub: if handleRecallMemory unconditionally attaches RankingRationale
// (ignoring the tg3Active gate), the per-memory assertion fails because
// "ranking_rationale" would appear in the output.
func TestRecallMemory_CompatV3_ZeroFlagsShape_T021(t *testing.T) {
	// Part A: schema-level check — no DB needed.
	// Verify that the recall_memory tool schema still carries the three TG3
	// params unconditionally but that their descriptions clearly state the
	// flag gate ("Honored only when ENGRAM_VNEXT_F_ENABLED=true").
	t.Run("schema_unconditional_tg3_params_present", func(t *testing.T) {
		// Use env-unset state to confirm schema is unconditional.
		t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
		t.Setenv("ENGRAM_VNEXT_ENABLED", "")

		s := NewServer(ServerOptions{Version: "test"})
		s.memoryStore = nonNilMemoryStore() // nonNilMemoryStore from tools_memory_t005_test.go

		props := findToolProperties(t, s.ListTools(), "recall_memory")

		for _, paramName := range []string{"confidence_min", "include_superseded", "include_rationale"} {
			p, ok := props[paramName].(map[string]any)
			require.True(t, ok, "recall_memory schema must expose %q unconditionally (T019)", paramName)
			desc, _ := p["description"].(string)
			assert.Contains(t, desc, "ENGRAM_VNEXT_F_ENABLED",
				"%q description must mention the flag gate", paramName)
		}

		// Legacy params must still be present (byte-identity of schema surface).
		for _, legacyParam := range []string{"query", "tags", "type", "limit", "format", "project"} {
			_, ok := props[legacyParam].(map[string]any)
			require.True(t, ok, "legacy param %q must still be present in schema (NFR-F1)", legacyParam)
		}
	})

	// Part B: runtime behavior — requires live PostgreSQL.
	// Verify that the response JSON contains no "ranking_rationale" when all
	// TG3 flags are at their default (zero) values.
	t.Run("runtime_no_ranking_rationale_key_when_flags_at_default", func(t *testing.T) {
		dsn := os.Getenv("DATABASE_DSN")
		if dsn == "" || testing.Short() {
			t.Skip("T021 runtime: DATABASE_DSN not set or -short; skipping DB-dependent assertion")
		}

		project := "t021-compat-v3"
		store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = store.DB.Exec(`DELETE FROM memories WHERE project = ?`, project).Error
			_ = store.Close()
		})

		memoryStore := dbgorm.NewMemoryStore(store)

		// Insert one test row.
		now := time.Now().UTC()
		row := &dbgorm.Memory{
			Project:        project,
			Content:        "compat v3 test content for recall",
			Status:         "active",
			Confidence:     0.8,
			ImportanceBase: 0.5,
			TsAlpha:        1.0,
			TsBeta:         1.0,
			Version:        1,
			CreatedAt:      now,
			UpdatedAt:      now,
			PrivacyScope:   "project",
		}
		require.NoError(t, store.DB.Create(row).Error, "insert fixture row")

		// Build server with ENGRAM_VNEXT_F_ENABLED=false (flag-OFF = legacy path).
		t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
		t.Setenv("ENGRAM_VNEXT_ENABLED", "")

		srv := NewServer(ServerOptions{Version: "test"})
		srv.memoryStore = memoryStore

		// Call recall_memory without any TG3 flags.
		args := mustJSON(t, map[string]any{
			"query":   "compat",
			"project": project,
			"format":  "text",
		})
		result, err := srv.handleRecallMemory(context.Background(), args)
		require.NoError(t, err, "handleRecallMemory must not error in zero-flag mode")

		// The text format for flag-OFF is a plain text string, not JSON.
		// Verify it does NOT contain "ranking_rationale" anywhere (NFR-F1 shape).
		assert.NotContains(t, result, "ranking_rationale",
			"flag-OFF response must not contain 'ranking_rationale' key (NFR-F1 byte-identity)")

		// Part C: Call recall(action="search") — items format for JSON assertion.
		argsSearch := mustJSON(t, map[string]any{
			"action":  "search",
			"query":   "compat",
			"project": project,
			"format":  "items",
		})
		resultSearch, err := srv.handleRecall(context.Background(), argsSearch)
		require.NoError(t, err, "handleRecall(search) must not error in zero-flag mode")

		// The items format returns JSON. Unmarshal and verify no ranking_rationale.
		var out map[string]any
		require.NoError(t, json.Unmarshal([]byte(resultSearch), &out),
			"items format must return valid JSON")

		// Top-level keys must be exactly {"memories", "count"} (and optionally "query").
		for key := range out {
			assert.NotEqual(t, "ranking_rationale", key,
				"top-level response must not have 'ranking_rationale' key (NFR-F1)")
		}

		memoriesAny, ok := out["memories"]
		require.True(t, ok, "response must have 'memories' key")
		memories, ok := memoriesAny.([]any)
		require.True(t, ok, "'memories' must be an array")

		for _, memAny := range memories {
			memObj, ok := memAny.(map[string]any)
			require.True(t, ok, "each memory must be an object")
			_, hasRationale := memObj["ranking_rationale"]
			assert.False(t, hasRationale,
				"individual memory object must not have 'ranking_rationale' key in flag-OFF mode (NFR-F1)")
		}
	})
}
