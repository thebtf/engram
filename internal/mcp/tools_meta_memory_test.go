package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func TestKnowAbout_T005_PopulatedTopicReturnsContentFreeIndexHits(t *testing.T) {
	idx := &fakeT005MetaMemoryIndex{
		hits: []gormdb.MetaIndexHit{
			{
				ID:        101,
				Project:   "engram",
				Title:     "handoff protocol summary",
				Tags:      []string{"intent:handoff", "s2:meta"},
				CreatedAt: time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 7, 4, 11, 0, 0, 0, time.UTC),
				Score:     0.91,
				Source:    "meta_index",
				Reason:    "topic match",
			},
			{
				ID:        102,
				Project:   "engram",
				Title:     "agent discovery boundary",
				Tags:      []string{"s2:meta"},
				CreatedAt: time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 7, 4, 9, 30, 0, 0, time.UTC),
				Score:     0.78,
				Source:    "meta_index",
				Reason:    "tag match",
			},
		},
	}
	srv := newT005KnowAboutServer(t, idx, true)

	tool := findT005KnowAboutTool(t, srv.ListTools())
	props := tool.InputSchema["properties"].(map[string]any)
	require.Contains(t, props, "topic", "know_about must be discoverable as a top-level topic query")
	require.Contains(t, props, "project", "know_about must allow explicit project scoping")
	require.Contains(t, props, "limit", "know_about must advertise bounded index reads")
	require.NotContains(t, props, "content", "know_about is discovery-only and must not accept memory bodies")

	payload := callT005KnowAbout(t, srv, context.Background(), map[string]any{
		"topic":   "handoff protocol",
		"project": "engram",
		"limit":   2,
	})

	require.Equal(t, "engram", payload["project"])
	require.Equal(t, "handoff protocol", payload["topic"])
	require.Equal(t, float64(2), payload["count"])
	require.Equal(t, float64(2), payload["total_candidates"])

	memories := requireT005Memories(t, payload)
	require.Len(t, memories, 2, "populated topics must return the canonical content-free memories list; nil/empty stubs must fail here")
	require.Equal(t, float64(101), memories[0]["id"])
	require.Equal(t, "handoff protocol summary", memories[0]["title"])
	require.Equal(t, []any{"intent:handoff", "s2:meta"}, memories[0]["tags"])
	require.Equal(t, float64(102), memories[1]["id"])

	require.Len(t, idx.queries, 1)
	require.Equal(t, "engram", idx.queries[0].Project)
	require.Equal(t, "handoff protocol", idx.queries[0].Query)
	require.Equal(t, 2, idx.queries[0].Limit)
	assertT005NoContentKeys(t, payload)
}

func TestKnowAbout_T005_MissingTopicReturnsEmptyIndexPacket(t *testing.T) {
	idx := &fakeT005MetaMemoryIndex{hits: []gormdb.MetaIndexHit{}}
	srv := newT005KnowAboutServer(t, idx, true)

	payload := callT005KnowAbout(t, srv, context.Background(), map[string]any{
		"topic":   "topic-with-no-index-hits",
		"project": "engram",
		"limit":   3,
	})

	require.Equal(t, "engram", payload["project"])
	require.Equal(t, "topic-with-no-index-hits", payload["topic"])
	require.Equal(t, float64(0), payload["count"])
	require.Equal(t, float64(0), payload["total_candidates"])
	require.Empty(t, requireT005Memories(t, payload), "missing topics must be an explicit empty result, not nil or a tool error")
	require.Len(t, idx.queries, 1, "missing topics must still query the content-free S2 index")
	assertT005NoContentKeys(t, payload)
}

func TestKnowAbout_T005_ProjectFallbackFailureRequiresProjectScope(t *testing.T) {
	srv := newT005KnowAboutServer(t, &fakeT005MetaMemoryIndex{}, true)

	_, err := srv.callTool(context.Background(), "know_about", mustT005KnowAboutJSON(t, map[string]any{
		"topic": "handoff protocol",
		"limit": 5,
	}))

	require.Error(t, err, "know_about must fail closed when neither arguments nor context provide a project")
	require.ErrorContains(t, err, "project")
}

func TestKnowAbout_T005_ContextProjectFallbackAndLimitClamp(t *testing.T) {
	idx := &fakeT005MetaMemoryIndex{generateFromLimit: true}
	srv := newT005KnowAboutServer(t, idx, true)
	ctx := contextWithProject(context.Background(), "context-project")

	payload := callT005KnowAbout(t, srv, ctx, map[string]any{
		"topic": "bounded discovery",
		"limit": 99,
	})

	require.Len(t, idx.queries, 1)
	require.Equal(t, "context-project", idx.queries[0].Project, "project must fall back to MCP context before querying S2")
	require.Equal(t, 25, idx.queries[0].Limit, "oversized know_about limits must clamp to the S2 meta-index maximum")
	require.Equal(t, float64(25), payload["count"])
	require.Equal(t, float64(25), payload["total_candidates"])
	require.Len(t, requireT005Memories(t, payload), 25, "response size must match the clamped S2 index limit")
	assertT005NoContentKeys(t, payload)
}

func TestKnowAbout_T005_RealStoreCanonicalShapeAndMissingTopicEmptyPacket(t *testing.T) {
	const project = "test-s2-meta-index-know-about-real"
	idx := openT005RealMetaMemoryIndex(t, project)
	insertT005RealMetaMemory(t, idx, project, "handoff memory title\nthis body omits the lexical query marker", []string{"intent:handoff", "s2:meta"})

	srv := newT005KnowAboutServer(t, idx, true)

	populated := callT005KnowAbout(t, srv, context.Background(), map[string]any{
		"topic":   "intent:handoff",
		"project": project,
		"limit":   5,
	})
	require.Equal(t, project, populated["project"])
	require.Equal(t, "intent:handoff", populated["topic"])
	require.Equal(t, float64(1), populated["count"])
	require.Equal(t, float64(1), populated["total_candidates"])
	memories := requireT005Memories(t, populated)
	require.Len(t, memories, 1)
	require.Equal(t, "handoff memory title", memories[0]["title"])
	assertT005NoContentKeys(t, populated)

	empty := callT005KnowAbout(t, srv, context.Background(), map[string]any{
		"topic":   "topic-with-no-index-hits",
		"project": project,
		"limit":   5,
	})
	require.Equal(t, project, empty["project"])
	require.Equal(t, "topic-with-no-index-hits", empty["topic"])
	require.Equal(t, float64(0), empty["count"])
	require.Equal(t, float64(0), empty["total_candidates"])
	require.Empty(t, requireT005Memories(t, empty), "real-store no-match queries must return an empty canonical packet, not a tool error")
	assertT005NoContentKeys(t, empty)
}

func TestKnowAbout_T005_DisabledS2NotAdvertised(t *testing.T) {
	idx := &fakeT005MetaMemoryIndex{}
	srv := newT005KnowAboutServer(t, idx, false)

	for _, tool := range srv.ListTools() {
		require.NotEqual(t, "know_about", tool.Name, "know_about must be absent unless ENGRAM_V7_PLUG_ENABLED and ENGRAM_V7_S2_METAMEM are both enabled")
	}
}

func TestKnowAbout_T014_ToolListRequiresMasterAndS2Flags(t *testing.T) {
	tests := []struct {
		name       string
		masterFlag string
		s2Flag     string
		wantTool   bool
	}{
		{name: "master and s2 enabled advertises know_about", masterFlag: "true", s2Flag: "true", wantTool: true},
		{name: "master disabled suppresses know_about even when s2 flag is set", masterFlag: "false", s2Flag: "true", wantTool: false},
		{name: "s2 disabled suppresses know_about even when master is set", masterFlag: "true", s2Flag: "false", wantTool: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_V7_PLUG_ENABLED", tt.masterFlag)
			t.Setenv("ENGRAM_V7_S2_METAMEM", tt.s2Flag)

			srv := NewServer(ServerOptions{Version: "test"})
			srv.SetMetaMemoryIndex(&fakeT005MetaMemoryIndex{})

			gotTool := false
			for _, tool := range srv.ListTools() {
				if tool.Name == "know_about" {
					gotTool = true
					props := tool.InputSchema["properties"].(map[string]any)
					require.Contains(t, props, "topic", "enabled know_about must expose the topic query contract")
					require.NotContains(t, props, "content", "know_about must stay content-free when advertised")
				}
			}
			require.Equal(t, tt.wantTool, gotTool, "know_about advertisement must require both ENGRAM_V7_PLUG_ENABLED and ENGRAM_V7_S2_METAMEM")
		})
	}
}

func TestKnowAbout_T005_IndexErrorsSurfaceAsToolErrors(t *testing.T) {
	idx := &fakeT005MetaMemoryIndex{err: errors.New("meta index offline")}
	srv := newT005KnowAboutServer(t, idx, true)

	_, err := srv.callTool(context.Background(), "know_about", mustT005KnowAboutJSON(t, map[string]any{
		"topic":   "handoff protocol",
		"project": "engram",
		"limit":   4,
	}))

	require.Error(t, err)
	require.ErrorContains(t, err, "meta index offline")
}

func TestKnowAbout_T005_JSONNeverContainsContentKeysOrMemoryBodies(t *testing.T) {
	idx := &fakeT005MetaMemoryIndex{
		hits: []gormdb.MetaIndexHit{
			{
				ID:        301,
				Project:   "engram",
				Title:     "safe title only",
				Tags:      []string{"s2:meta"},
				CreatedAt: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 7, 4, 12, 5, 0, 0, time.UTC),
				Score:     0.5,
				Source:    "meta_index",
			},
		},
	}
	srv := newT005KnowAboutServer(t, idx, true)

	result, err := srv.callTool(context.Background(), "know_about", mustT005KnowAboutJSON(t, map[string]any{
		"topic":   "safe title",
		"project": "engram",
		"limit":   1,
	}))
	require.NoError(t, err)

	lower := strings.ToLower(result)
	require.NotContains(t, lower, "\"content\"", "know_about JSON must not expose memory body fields")
	require.NotContains(t, lower, "\"body\"", "know_about JSON must not expose body aliases")
	require.NotContains(t, lower, "\"narrative\"", "know_about JSON must not expose legacy observation narrative")
	require.NotContains(t, lower, "\"raw_content\"", "know_about JSON must not expose raw memory bodies")
	require.NotContains(t, lower, "forbidden-body-token", "know_about must never serialize raw memory body text")

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &payload))
	assertT005NoContentKeys(t, payload)
}

type fakeT005MetaMemoryIndex struct {
	hits              []gormdb.MetaIndexHit
	err               error
	generateFromLimit bool
	queries           []gormdb.MetaIndexQuery
}

func (f *fakeT005MetaMemoryIndex) QueryMetaIndex(_ context.Context, query gormdb.MetaIndexQuery) ([]gormdb.MetaIndexHit, error) {
	f.queries = append(f.queries, query)
	if f.err != nil {
		return nil, f.err
	}
	if f.generateFromLimit {
		hits := make([]gormdb.MetaIndexHit, query.Limit)
		for i := range hits {
			hits[i] = gormdb.MetaIndexHit{
				ID:        int64(7000 + i),
				Project:   query.Project,
				Title:     fmt.Sprintf("generated index hit %02d", i),
				Tags:      []string{"s2:meta"},
				CreatedAt: time.Date(2026, 7, 4, 13, i%60, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 7, 4, 13, i%60, 30, 0, time.UTC),
				Score:     1,
				Source:    "meta_index",
			}
		}
		return hits, nil
	}
	return append([]gormdb.MetaIndexHit(nil), f.hits...), nil
}

func newT005KnowAboutServer(t *testing.T, idx metaMemoryIndex, s2Enabled bool) *Server {
	t.Helper()
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	if s2Enabled {
		t.Setenv("ENGRAM_V7_S2_METAMEM", "true")
	} else {
		t.Setenv("ENGRAM_V7_S2_METAMEM", "")
	}
	srv := NewServer(ServerOptions{Version: "test"})
	if idx != nil {
		srv.SetMetaMemoryIndex(idx)
	}
	return srv
}

func findT005KnowAboutTool(t *testing.T, tools []Tool) *Tool {
	t.Helper()
	for i := range tools {
		if tools[i].Name == "know_about" {
			return &tools[i]
		}
	}
	t.Fatalf("know_about must be advertised when S2 is enabled and a meta index is wired; tools=%v", toolNamesT005(tools))
	return nil
}

func toolNamesT005(tools []Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func callT005KnowAbout(t *testing.T, srv *Server, ctx context.Context, args map[string]any) map[string]any {
	t.Helper()
	result, err := srv.callTool(ctx, "know_about", mustT005KnowAboutJSON(t, args))
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &payload), "know_about must return JSON")
	return payload
}

func requireT005Memories(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, ok := payload["memories"].([]any)
	require.True(t, ok, "know_about response must expose index entries under memories")
	memories := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		memory, ok := item.(map[string]any)
		require.True(t, ok, "each memory must be a JSON object")
		memories = append(memories, memory)
	}
	return memories
}

func mustT005KnowAboutJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func assertT005NoContentKeys(t *testing.T, v any) {
	t.Helper()
	forbidden := map[string]bool{
		"content":     true,
		"body":        true,
		"narrative":   true,
		"raw_content": true,
		"rawcontent":  true,
	}
	var walk func(any, string)
	walk = func(value any, path string) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				canonical := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
				require.False(t, forbidden[canonical], "know_about must not serialize content-bearing key %q at %s", key, path)
				walk(child, path+"."+key)
			}
		case []any:
			for i, child := range typed {
				walk(child, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(v, "$USER")
}

func openT005RealMetaMemoryIndex(t *testing.T, project string) *gormdb.MemoryStore {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" || testing.Short() {
		t.Skip("T005 real-store: DATABASE_DSN not set or -short; skipping DB-dependent assertion")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.DB.WithContext(context.Background()).Exec(`DELETE FROM memories WHERE project = ?`, project).Error
		_ = store.Close()
	})
	return gormdb.NewMemoryStore(store)
}

func insertT005RealMetaMemory(t *testing.T, ms *gormdb.MemoryStore, project, content string, tags []string) {
	t.Helper()
	_, err := ms.Create(context.Background(), &models.Memory{
		Project:            project,
		Content:            content,
		Tags:               tags,
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		SourceAgent:        "t005-real-store",
	})
	require.NoError(t, err)
}
