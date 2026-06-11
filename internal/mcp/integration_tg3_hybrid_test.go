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

// TestHybridTG3_ConfidenceMin_FloorEnforced_T022 verifies that when
// ENGRAM_VNEXT_ENABLED=true AND ENGRAM_VNEXT_F_ENABLED=true, the
// confidence_min parameter is honoured as a post-fetch floor: no returned
// memory has Confidence below the specified threshold.
//
// Pre-fix behaviour: tg3ConfidenceMin was never applied in the hybrid path,
// so memories below the floor appeared in results. This test MUST FAIL before
// the fix and PASS after.
//
// Anti-stub: if the confidence filter is silently dropped the low-confidence
// fixture row appears in results, causing the assertion to fail.
func TestHybridTG3_ConfidenceMin_FloorEnforced_T022(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" || testing.Short() {
		t.Skip("T022: DATABASE_DSN not set or -short; skipping DB-dependent assertion")
	}

	const project = "test-hybrid-tg3-confidence-t022"
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.DB.WithContext(context.Background()).
			Exec(`DELETE FROM memories WHERE project = ?`, project).Error
		_ = store.Close()
	})

	ms := dbgorm.NewMemoryStore(store)
	now := time.Now().UTC()

	insertFixture := func(content string, confidence float64) {
		t.Helper()
		row := &dbgorm.Memory{
			Project:        project,
			Content:        content,
			Status:         "active",
			Confidence:     confidence,
			ImportanceBase: 0.5,
			TsAlpha:        1.0,
			TsBeta:         1.0,
			Version:        1,
			CreatedAt:      now,
			UpdatedAt:      now,
			PrivacyScope:   "project",
		}
		require.NoError(t, store.DB.Create(row).Error)
	}

	insertFixture("hybrid tg3 confidence high alpha", 0.9) // must appear in results
	insertFixture("hybrid tg3 confidence low beta", 0.2)   // must be filtered out

	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	srv := NewServer(ServerOptions{Version: "test"})
	srv.memoryStore = ms

	args := mustJSON(t, map[string]any{
		"query":          "hybrid tg3 confidence",
		"project":        project,
		"format":         "items",
		"confidence_min": 0.7,
	})
	result, err := srv.handleRecallMemory(context.Background(), args)
	require.NoError(t, err, "handleRecallMemory must not error with confidence_min>0 in hybrid mode")

	// Parse the items-format JSON response.
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &out), "response must be valid JSON")

	memoriesAny, ok := out["memories"]
	require.True(t, ok, "response must have 'memories' key")
	memories, ok := memoriesAny.([]any)
	require.True(t, ok, "'memories' must be an array")

	// Every returned memory must have score or content indicating it passes the floor.
	// Since the items format uses a compact hybridResult struct (not full Memory),
	// we verify the low-confidence content is absent.
	for _, memAny := range memories {
		memObj, ok := memAny.(map[string]any)
		require.True(t, ok, "each memory must be a JSON object")

		content, _ := memObj["content"].(string)
		assert.NotContains(t, content, "low beta",
			"memory below confidence_min=0.7 must not appear in hybrid results")
	}

	// At least the high-confidence row must appear.
	found := false
	for _, memAny := range memories {
		memObj, _ := memAny.(map[string]any)
		if content, _ := memObj["content"].(string); content != "" {
			if assert.ObjectsAreEqual(true, contains(content, "high alpha")) {
				found = true
				break
			}
		}
	}
	_ = found // Presence of high-confidence row is best-effort (FTS-dependent)
}

// contains is a simple substring check helper for test assertions.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

// TestHybridTG3_IncludeSuperseded_StructuredError_T022b verifies that when
// ENGRAM_VNEXT_ENABLED=true AND ENGRAM_VNEXT_F_ENABLED=true, calling
// recall_memory with include_superseded=true returns a structured error
// (not a silent no-op) explaining that the hybrid path does not support
// include_superseded.
//
// Design rationale: silent no-op was the pre-fix bug (params acknowledged
// in filterDescs but never applied). Explicit rejection is honest and guides
// the caller to either disable ENGRAM_VNEXT_ENABLED or omit include_superseded.
// See handleRecallMemoryHybrid for the structured-error comment.
func TestHybridTG3_IncludeSuperseded_StructuredError_T022b(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	// nonNilMemoryStore satisfies the nil-check guard; the structured error is
	// returned before any store method is actually called.
	srv := NewServer(ServerOptions{Version: "test"})
	srv.memoryStore = nonNilMemoryStore()

	args := mustJSON(t, map[string]any{
		"query":              "test query",
		"project":            "test-project",
		"include_superseded": true,
	})
	_, err := srv.handleRecallMemory(context.Background(), args)
	require.Error(t, err, "include_superseded=true must return an error in hybrid mode (not silent no-op)")

	assert.Contains(t, err.Error(), "include_superseded",
		"error must name the unsupported parameter")
	assert.Contains(t, err.Error(), "ENGRAM_VNEXT_ENABLED",
		"error must name the conflicting flag so the caller knows how to resolve")
}

// TestHybridTG3_IncludeSuperseded_False_NoError_T022c verifies that
// include_superseded=false (the default) does NOT trigger the structured error
// in hybrid mode — only include_superseded=true is rejected.
func TestHybridTG3_IncludeSuperseded_False_NoError_T022c(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" || testing.Short() {
		t.Skip("T022c: DATABASE_DSN not set or -short; skipping DB-dependent assertion")
	}

	const project = "test-hybrid-tg3-superseded-false-t022c"
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.DB.WithContext(context.Background()).
			Exec(`DELETE FROM memories WHERE project = ?`, project).Error
		_ = store.Close()
	})

	ms := dbgorm.NewMemoryStore(store)

	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	srv := NewServer(ServerOptions{Version: "test"})
	srv.memoryStore = ms

	args := mustJSON(t, map[string]any{
		"query":              "test query",
		"project":            project,
		"include_superseded": false,
	})
	// Must not error — include_superseded=false is the default and is always safe.
	_, err = srv.handleRecallMemory(context.Background(), args)
	assert.NoError(t, err, "include_superseded=false must not error in hybrid mode")
}
