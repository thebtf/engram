package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
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

	// Decode the items response and verify both sides of the confidence floor.
	var memories []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &memories), "response must be valid items JSON")

	contents := make([]string, 0, len(memories))
	for _, memory := range memories {
		content, ok := memory["content"].(string)
		require.True(t, ok, "returned memory must have string content")
		assert.NotContains(t, content, "low beta",
			"memory below confidence_min=0.7 must not appear in hybrid results")
		contents = append(contents, content)
	}
	assert.Contains(t, contents, "hybrid tg3 confidence high alpha",
		"matching high-confidence fixture must appear in hybrid results")
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

func TestRecallMemoryHybrid_EmbeddingBudget(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" || testing.Short() {
		t.Skip("DATABASE_DSN not set or -short; requires isolated PostgreSQL with vector")
	}
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "offline-fixture")
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "")
	vector := make([]float32, embedding.EmbeddingDim)
	vector[0] = 1

	for _, tc := range []struct {
		name       string
		mode       string
		tiers      []string
		format     string
		wantTier   string
		wantError  error
		noDeadline bool
		rerun      bool
		rationale  bool
	}{
		{name: "stalled_default_fts", mode: "stall", format: "items", wantTier: "tier1_fts"},
		{name: "stalled_explicit_fts", mode: "stall", tiers: []string{"tier1_fts"}, format: "items", wantTier: "tier1_fts"},
		{name: "filtered_tier0_rerun", mode: "stall", format: "items", wantTier: "tier1_fts", rerun: true},
		{name: "healthy_vector", mode: "reply", format: "items", wantTier: "tier1_vector"},
		{name: "vector_only_late", mode: "late", tiers: []string{"tier1_vector"}, format: "items", wantTier: "tier1_vector"},
		{name: "no_parent_deadline", mode: "reply", format: "items", wantTier: "tier1_vector", noDeadline: true},
		{name: "fts_excluded", mode: "stall", tiers: []string{"tier0_exact"}, wantError: context.DeadlineExceeded},
		{name: "parent_already_cancelled", mode: "pre_cancel", wantError: context.Canceled},
		{name: "parent_already_expired", mode: "pre_expire", wantError: context.DeadlineExceeded},
		{name: "parent_cancelled_during_embedding", mode: "cancel", wantError: context.Canceled},
		{name: "parent_cancelled_during_retrieval", mode: "retrieval_cancel", wantError: context.Canceled},
		{name: "detailed_format", mode: "stall", format: "detailed", wantTier: "tier1_fts"},
		{name: "text_format", mode: "stall", format: "text", wantTier: "tier1_fts"},
		{name: "rationale_format", mode: "stall", format: "items", wantTier: "tier1_fts", rationale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := "test-hybrid-budget-" + tc.name
			const query = "budgetstarvation"
			insert := func(content, rowProject, visibility, privacy string, confidence float64, tags []string) *dbgorm.Memory {
				t.Helper()
				row := &dbgorm.Memory{
					Project: rowProject, Content: content, Status: "active",
					PrivacyScope: privacy, AgentVisibility: visibility,
					OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent",
					Confidence: confidence, Tags: models.JSONStringArray(tags),
				}
				require.NoError(t, store.DB.Create(row).Error)
				return row
			}
			goodTags := []string{"type:fact", "budget-fixture"}
			visible := insert(query+" authorized FTS content", project, "shared", "project", 0.9, goodTags)
			vectorOnly := insert("semantic authorized content", project, "shared", "project", 0.9, goodTags)
			insert(query+" private principal", project, "private", "project", 0.9, goodTags)
			insert(query+" private workstation", project, "shared", "private", 0.9, goodTags)
			insert(query+" other project", project+"-other", "shared", "project", 0.9, goodTags)
			insert(query+" low confidence", project, "shared", "project", 0.2, goodTags)
			insert(query+" wrong type", project, "shared", "project", 0.9, []string{"type:decision", "budget-fixture"})
			insert(query+" wrong tag", project, "shared", "project", 0.9, []string{"type:fact", "other-tag"})
			if tc.rerun {
				insert(query, project, "private", "project", 0.9, goodTags)
			}
			t.Cleanup(func() {
				require.NoError(t, store.DB.Exec(`DELETE FROM content_chunks WHERE memory_id IN (SELECT id FROM memories WHERE project IN (?, ?))`, project, project+"-other").Error)
				require.NoError(t, store.DB.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, project, project+"-other").Error)
			})
			embStore := embedding.NewStore(store.DB)
			require.NoError(t, embStore.StoreChunks(context.Background(), []embedding.Chunk{
				{MemoryID: vectorOnly.ID, Text: vectorOnly.Content, Embedding: pgvector.NewVector(vector), Model: "offline-fixture"},
			}))
			caller := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
			var ctx context.Context
			var cancel context.CancelFunc
			if tc.noDeadline || tc.mode == "pre_cancel" {
				ctx, cancel = context.WithCancel(caller)
			} else if tc.mode == "pre_expire" {
				ctx, cancel = context.WithDeadline(caller, time.Now().Add(-time.Second))
			} else {
				ctx, cancel = context.WithTimeout(caller, 4*time.Second)
			}
			defer cancel()
			if tc.mode == "pre_cancel" {
				cancel()
			}
			var requests atomic.Int32
			finished := make(chan bool, 1)
			release := make(chan struct{})
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				_ = r.Body.Close()
				if tc.mode == "cancel" {
					cancel()
				}
				if tc.mode == "stall" || tc.mode == "cancel" {
					select {
					case <-r.Context().Done():
						finished <- true
					case <-release:
						finished <- false
					}
					return
				}
				if tc.mode == "late" {
					deadline, _ := ctx.Deadline()
					timer := time.NewTimer(time.Until(deadline) * 3 / 4)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-r.Context().Done():
						finished <- true
						return
					case <-release:
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": vector}}})
				finished <- false
			}))
			defer endpoint.Close()
			defer close(release)
			t.Setenv("ENGRAM_EMBEDDING_URL", endpoint.URL)
			client, err := embedding.NewClient()
			require.NoError(t, err)
			srv := NewServer(ServerOptions{Version: "offline-budget-fixture"})
			srv.memoryStore = dbgorm.NewMemoryStore(store)
			controlArgs := mustJSON(t, map[string]any{
				"query": query, "project": project, "format": "items", "limit": 5,
				"explain": true, "type": "fact", "tags": []string{"budget-fixture"},
				"confidence_min": 0.7, "tier_filter": []string{"tier1_fts"},
			})
			control, err := srv.handleRecallMemory(caller, controlArgs)
			require.NoError(t, err, "matched no-embedding FTS control")
			var controlRows []map[string]any
			require.NoError(t, json.Unmarshal([]byte(control), &controlRows))
			require.Len(t, controlRows, 1, "fixture must fit the existing candidate overfetch ceiling")
			require.EqualValues(t, visible.ID, controlRows[0]["id"])
			require.Equal(t, "tier1_fts", controlRows[0]["ranking_explanation"].(map[string]any)["source_tier"])
			srv.embeddingClient = client
			srv.embeddingStore = embStore
			if tc.mode == "retrieval_cancel" {
				const callback = "test:hybrid_budget_cancel"
				require.NoError(t, store.DB.Callback().Query().Before("gorm:query").Register(callback, func(db *gormlib.DB) {
					if db.Statement.Table == "memories" {
						cancel()
					}
				}))
				defer store.DB.Callback().Query().Remove(callback)
			}
			args := mustJSON(t, map[string]any{
				"query": query, "project": project, "format": tc.format,
				"limit": 5, "explain": true, "type": "fact", "tags": []string{"budget-fixture"},
				"confidence_min": 0.7, "tier_filter": tc.tiers, "include_rationale": tc.rationale,
			})
			if tc.wantError != nil {
				out, err := srv.handleRecallMemory(ctx, args)
				require.ErrorIs(t, err, tc.wantError, "must not turn parent cancellation into successful degradation: %s", out)
				require.Empty(t, out)
			} else {
				response := srv.HandleRequest(ctx, &Request{
					JSONRPC: "2.0", ID: "offline-budget", Method: "tools/call",
					Params: mustJSON(t, ToolCallParams{Name: "recall_memory", Arguments: args}),
				})
				require.Nil(t, response.Error, "actual MCP tools/call failed: %+v", response.Error)
				require.NoError(t, ctx.Err(), "original caller budget must remain live")
				require.Equal(t, "offline-budget", response.ID)
				out := response.Result.(map[string]any)["content"].([]map[string]any)[0]["text"].(string)
				if tc.format == "text" {
					require.Contains(t, out, visible.Content)
					require.Contains(t, out, "tier=tier1_fts")
					require.NotContains(t, out, vectorOnly.Content)
				} else {
					var rows []map[string]any
					if tc.rationale {
						var body struct {
							Memories []map[string]any `json:"memories"`
						}
						require.NoError(t, json.Unmarshal([]byte(out), &body))
						rows = body.Memories
					} else {
						require.NoError(t, json.Unmarshal([]byte(out), &rows))
					}
					wantCount := 1
					if tc.wantTier == "tier1_vector" && len(tc.tiers) == 0 {
						wantCount = 2
					}
					require.Len(t, rows, wantCount, "exact authorized fixture IDs after all filters")
					want := visible
					if tc.wantTier == "tier1_vector" {
						want = vectorOnly
					}
					require.EqualValues(t, want.ID, rows[0]["id"])
					require.Equal(t, want.Content, rows[0]["content"])
					require.Equal(t, project, rows[0]["project"])
					require.Equal(t, "agent/bob", rows[0]["owner_principal"])
					require.Equal(t, "shared", rows[0]["agent_visibility"])
					require.Equal(t, tc.wantTier, rows[0]["ranking_explanation"].(map[string]any)["source_tier"])
					if tc.rationale {
						require.NotNil(t, rows[0]["ranking_rationale"])
					}
					t.Logf("MCP recall_memory fixture id=%d content=%q project=%s tier=%s parent_alive=true", want.ID, want.Content, project, tc.wantTier)
					if wantCount == 2 {
						require.EqualValues(t, visible.ID, rows[1]["id"])
						require.Equal(t, visible.Content, rows[1]["content"])
						require.Equal(t, "tier1_fts", rows[1]["ranking_explanation"].(map[string]any)["source_tier"])
					}
				}
			}
			if tc.mode == "pre_cancel" || tc.mode == "pre_expire" {
				require.Zero(t, requests.Load(), "already cancelled caller must not launch embedding")
			} else {
				select {
				case cancelled := <-finished:
					require.Equal(t, tc.mode == "stall" || tc.mode == "cancel", cancelled, "HTTP fixture must exit through the expected cancellation/response")
				case <-time.After(time.Second):
					t.Fatal("local embedding HTTP request did not finish")
				}
				require.EqualValues(t, 1, requests.Load(), "no retry after child cancellation")
			}
		})
	}
}
