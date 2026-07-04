package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// TestMemoryStore_CreateGetUpdateListDelete exercises the full Create→Get→Update→List→Delete
// round-trip against a real PostgreSQL database.
// Anti-stub contract: if any method body is replaced with `return nil` this test fails.
func TestMemoryStore_CreateGetUpdateListDelete(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-memory-store'`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	const testProject = "test-memory-store"

	// --- Create ---
	mem := &models.Memory{
		Project:     testProject,
		Content:     "discovered that gRPC retry policy requires idempotency tokens",
		Tags:        []string{"grpc", "retry", "architecture"},
		SourceAgent: "smoke-agent",
		EditedBy:    "smoke-test",
	}
	created, err := ms.Create(ctx, mem)
	require.NoError(t, err, "Create should succeed")
	assert.Greater(t, created.ID, int64(0), "Create should return a populated ID")
	assert.False(t, created.CreatedAt.IsZero(), "Create should return a populated CreatedAt")
	assert.False(t, created.UpdatedAt.IsZero(), "Create should return a populated UpdatedAt")
	assert.Equal(t, testProject, created.Project)
	assert.Equal(t, mem.Content, created.Content)
	assert.Equal(t, []string{"grpc", "retry", "architecture"}, created.Tags)
	assert.Equal(t, "smoke-agent", created.SourceAgent)
	assert.Equal(t, 1, created.Version, "Version should be 1 on create")
	assert.Nil(t, created.DeletedAt, "active memory must have nil deleted_at")

	// Verify input was NOT mutated
	assert.Equal(t, int64(0), mem.ID, "Create must not mutate caller's input ID")

	// --- Get ---
	fetched, err := ms.Get(ctx, created.ID)
	require.NoError(t, err, "Get should return the created memory")
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, testProject, fetched.Project)
	assert.Equal(t, mem.Content, fetched.Content)
	assert.Equal(t, []string{"grpc", "retry", "architecture"}, fetched.Tags)

	// --- Update ---
	updated, err := ms.Update(ctx, &models.Memory{
		ID:       created.ID,
		Content:  "updated: grpc retry requires idempotency tokens AND exponential backoff",
		Tags:     []string{"grpc", "retry", "architecture", "backoff"},
		EditedBy: "update-test",
	})
	require.NoError(t, err, "Update should succeed")
	assert.Equal(t, created.ID, updated.ID, "Update should return same ID")
	assert.Equal(t, "updated: grpc retry requires idempotency tokens AND exponential backoff", updated.Content)
	assert.Equal(t, []string{"grpc", "retry", "architecture", "backoff"}, updated.Tags)
	assert.Equal(t, "update-test", updated.EditedBy)
	assert.Equal(t, 2, updated.Version, "Version should be bumped to 2 after update")

	// --- List ---
	list, err := ms.List(ctx, testProject, 10)
	require.NoError(t, err, "List should succeed")
	require.GreaterOrEqual(t, len(list), 1, "List should return at least one memory")
	found := false
	for _, m := range list {
		if m.ID == created.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "Created memory must appear in List")

	// --- Delete ---
	err = ms.Delete(ctx, created.ID)
	require.NoError(t, err, "Delete should succeed")

	// Verify hard-delete: Get should no longer find the row.
	_, err = ms.Get(ctx, created.ID)
	require.Error(t, err, "Get after Delete should return an error (row gone)")

	// List should not return the deleted row.
	listAfter, err := ms.List(ctx, testProject, 10)
	require.NoError(t, err)
	for _, m := range listAfter {
		assert.NotEqual(t, created.ID, m.ID, "Deleted memory must not appear in List")
	}

	// --- Delete non-existent ID ---
	err = ms.Delete(ctx, 99999999)
	require.Error(t, err, "Delete of non-existent ID should return an error")
}

// TestMemoryStore_Create_ValidationErrors verifies that Create rejects invalid input.
func TestMemoryStore_Create_ValidationErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	// No rows are inserted in this test (all creates fail), so no extra cleanup needed.

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	cases := []struct {
		name string
		mem  *models.Memory
	}{
		{"nil memory", nil},
		{"empty project", &models.Memory{Content: "some content"}},
		{"empty content", &models.Memory{Project: "proj"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ms.Create(ctx, tc.mem)
			require.Error(t, err, "Create with %q should fail", tc.name)
		})
	}
}

// TestMemoryStore_List_FiltersByProject inserts 3 memories across 2 projects and confirms
// List returns only the requested project's rows.
func TestMemoryStore_List_FiltersByProject(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project IN ('test-memory-list-proj1','test-memory-list-proj2')`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	const proj1 = "test-memory-list-proj1"
	const proj2 = "test-memory-list-proj2"

	// Insert 2 memories for proj1 and 1 for proj2.
	_, err := ms.Create(ctx, &models.Memory{Project: proj1, Content: "proj1 memory A"})
	require.NoError(t, err)
	_, err = ms.Create(ctx, &models.Memory{Project: proj1, Content: "proj1 memory B"})
	require.NoError(t, err)
	_, err = ms.Create(ctx, &models.Memory{Project: proj2, Content: "proj2 memory A"})
	require.NoError(t, err)

	// List proj1: must return exactly 2 rows.
	list1, err := ms.List(ctx, proj1, 100)
	require.NoError(t, err)
	assert.Len(t, list1, 2, "proj1 should have exactly 2 memories")
	for _, m := range list1 {
		assert.Equal(t, proj1, m.Project, "all rows must belong to proj1")
	}

	// List proj2: must return exactly 1 row.
	list2, err := ms.List(ctx, proj2, 100)
	require.NoError(t, err)
	assert.Len(t, list2, 1, "proj2 should have exactly 1 memory")
	assert.Equal(t, proj2, list2[0].Project)
	assert.Equal(t, "proj2 memory A", list2[0].Content)
}

// TestMemoryStore_SearchFTS_OrFallback is the regression test for issue #281:
// recall_memory (hybrid FTS leg) returned "No memories found" for an
// over-specified multi-word query because websearch_to_tsquery ANDs all terms,
// requiring every term in a single memory's content. The OR-fallback retries
// with the terms OR-combined when the AND pass is empty.
//
// Anti-stub contract: if SearchFTS drops the OR-fallback (reverts to AND-only),
// the over-specified-query subtest fails because no single memory contains all
// four query terms.
func TestMemoryStore_SearchFTS_OrFallback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-fts-orfallback'`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	const proj = "test-fts-orfallback"

	// Two memories, each holding only SOME of the eventual query's terms. No
	// single memory contains all of {updated, deferred, launcher, install}.
	_, err := ms.Create(ctx, &models.Memory{
		Project: proj,
		Content: "the mcp launcher install reconnect races on windows post-exit",
	})
	require.NoError(t, err)
	_, err = ms.Create(ctx, &models.Memory{
		Project: proj,
		Content: "upgrade apply uses updated deferred standard procedure",
	})
	require.NoError(t, err)

	// AND pass: every term in ONE memory's content. "launcher install" both
	// live in memory 1 → precise match returns it.
	andHits, err := ms.SearchFTS(ctx, proj, "launcher install", 10)
	require.NoError(t, err, "SearchFTS AND pass should not error")
	require.GreaterOrEqual(t, len(andHits), 1, "AND pass: both terms in one memory must match")

	// Over-specified query (issue #281 repro): all four terms span TWO memories,
	// so the AND pass yields zero. The OR-fallback must surface partial matches.
	orHits, err := ms.SearchFTS(ctx, proj, "updated deferred launcher install", 10)
	require.NoError(t, err, "SearchFTS OR fallback should not error")
	require.GreaterOrEqual(t, len(orHits), 1,
		"issue #281: over-specified multi-word query must return partial matches via OR-fallback, not empty")

	// Single-term query: no fallback path, must still match directly.
	oneHit, err := ms.SearchFTS(ctx, proj, "launcher", 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(oneHit), 1, "single-term query must match content directly")

	// Quoted-phrase over-specified query (PR #270 review): a quoted phrase plus a
	// loose term spans both memories. The AND pass yields zero (no memory has the
	// "mcp launcher" phrase AND "deferred"), so the OR-fallback runs. The phrase
	// must survive tokenization intact — a naive strings.Fields split would
	// shatter "mcp launcher" and corrupt the rebuilt OR query, returning empty.
	phraseHits, err := ms.SearchFTS(ctx, proj, `"mcp launcher" deferred`, 10)
	require.NoError(t, err, "SearchFTS quoted-phrase OR fallback should not error")
	require.GreaterOrEqual(t, len(phraseHits), 1,
		"PR #270: quoted phrase + loose term must survive OR-fallback tokenization, not return empty")
}

func TestMemoryStore_QueryMetaIndex_TagOnlyAndFTSOnlyHits(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-s2-meta-index-hits'`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	const project = "test-s2-meta-index-hits"
	tagOnly := insertMetaIndexMemory(t, db, ms, ctx, project, "tag-only memory title\nthis body deliberately omits the lexical query marker", []string{"s2:meta", "intent:handoff"}, "agent/alice", models.AgentVisibilityShared, time.Unix(1700000100, 0).UTC())
	ftsOnly := insertMetaIndexMemory(t, db, ms, ctx, project, "fts-only memory title\ncontains rarelexemetagless for retrieval without the requested tag", []string{"s2:meta"}, "agent/alice", models.AgentVisibilityShared, time.Unix(1700000200, 0).UTC())
	insertMetaIndexMemory(t, db, ms, ctx, project, "distractor memory\nno matching tag or rare term", []string{"s2:other"}, "agent/alice", models.AgentVisibilityShared, time.Unix(1700000300, 0).UTC())

	tagHits, err := ms.QueryMetaIndex(ctx, MetaIndexQuery{
		Project:            project,
		Tags:               []string{"intent:handoff"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Limit:              10,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{tagOnly}, metaIndexHitIDs(tagHits), "tag-only lookup must return rows even when there is no content query")

	ftsHits, err := ms.QueryMetaIndex(ctx, MetaIndexQuery{
		Project:            project,
		Query:              "rarelexemetagless",
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Limit:              10,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{ftsOnly}, metaIndexHitIDs(ftsHits), "FTS-only lookup must return rows even when no tag filter is supplied")
}

func TestMemoryStore_QueryMetaIndex_EmptyProjectRejected(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ms := NewMemoryStore(&Store{DB: db})
	_, err := ms.QueryMetaIndex(context.Background(), MetaIndexQuery{
		Tags:  []string{"s2:meta"},
		Limit: 10,
	})
	require.Error(t, err, "content-free S2 index queries must stay project-scoped")
	require.Contains(t, err.Error(), "project")
}

func TestMemoryStore_QueryMetaIndex_VisibilityBeforeLimit(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-s2-meta-index-visibility'`)

	ms := NewMemoryStore(&Store{DB: db})
	ctx := context.Background()
	const project = "test-s2-meta-index-visibility"

	newerPrivate := insertMetaIndexMemory(t, db, ms, ctx, project, "newer bob private handoff", []string{"s2:meta", "intent:handoff"}, "agent/bob", models.AgentVisibilityPrivate, time.Unix(1700000600, 0).UTC())
	olderVisible := insertMetaIndexMemory(t, db, ms, ctx, project, "older alice visible handoff", []string{"s2:meta", "intent:handoff"}, "agent/alice", models.AgentVisibilityShared, time.Unix(1700000500, 0).UTC())

	hits, err := ms.QueryMetaIndex(ctx, MetaIndexQuery{
		Project:            project,
		Tags:               []string{"intent:handoff"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Limit:              1,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{olderVisible}, metaIndexHitIDs(hits), "visibility must be applied before LIMIT so a newer private row cannot hide the older visible row")
	require.NotContains(t, metaIndexHitIDs(hits), newerPrivate)
}

func TestMemoryStore_QueryMetaIndex_LimitsTieOrderAndContentFreeOutput(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-s2-meta-index-limits'`)

	ms := NewMemoryStore(&Store{DB: db})
	ctx := context.Background()
	const project = "test-s2-meta-index-limits"
	base := time.Unix(1700001000, 0).UTC()
	var ids []int64
	for i := range 30 {
		createdAt := base.Add(time.Duration(i/3) * time.Minute)
		id := insertMetaIndexMemory(t, db, ms, ctx, project,
			fmt.Sprintf("meta index title %02d\nforbidden-body-token-%02d must never be serialized in S2 index hits", i, i),
			[]string{"s2:meta", "limit:case"},
			"agent/alice",
			models.AgentVisibilityShared,
			createdAt,
		)
		ids = append(ids, id)
	}

	defaultHits, err := ms.QueryMetaIndex(ctx, MetaIndexQuery{
		Project:            project,
		Tags:               []string{"limit:case"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
	})
	require.NoError(t, err)
	require.Len(t, defaultHits, 10, "unspecified limit must return the bounded ten newest meta-index hits")
	require.Equal(t, reverseInt64s(ids[20:30]), metaIndexHitIDs(defaultHits), "equal created_at rows must use id DESC as the deterministic tie-breaker")

	cappedHits, err := ms.QueryMetaIndex(ctx, MetaIndexQuery{
		Project:            project,
		Tags:               []string{"limit:case"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Limit:              100,
	})
	require.NoError(t, err)
	require.Len(t, cappedHits, 25, "oversized S2 index requests must cap at twenty-five hits")

	_, hasContentField := reflect.TypeOf(MetaIndexHit{}).FieldByName("Content")
	require.False(t, hasContentField, "MetaIndexHit must be content-free; callers expand by id when full memory content is needed")
	payload, err := json.Marshal(cappedHits)
	require.NoError(t, err)
	serialized := strings.ToLower(string(payload))
	require.NotContains(t, serialized, "content", "serialized meta-index hits must not expose a content field")
	require.NotContains(t, serialized, "forbidden-body-token", "serialized meta-index hits must not leak raw memory body text")
}

func insertMetaIndexMemory(t *testing.T, db *gorm.DB, ms *MemoryStore, ctx context.Context, project, content string, tags []string, owner, visibility string, createdAt time.Time) int64 {
	t.Helper()
	created, err := ms.Create(ctx, &models.Memory{
		Project:            project,
		Content:            content,
		Tags:               tags,
		OwnerPrincipal:     owner,
		OwnerPrincipalKind: "agent",
		AgentVisibility:    visibility,
		SourceAgent:        "s2-meta-index-test",
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&Memory{}).Where("id = ?", created.ID).Updates(map[string]interface{}{
		"created_at": createdAt,
		"updated_at": createdAt,
	}).Error)
	return created.ID
}

func metaIndexHitIDs(hits []MetaIndexHit) []int64 {
	ids := make([]int64, len(hits))
	for i, hit := range hits {
		ids[i] = hit.ID
	}
	return ids
}

func reverseInt64s(in []int64) []int64 {
	out := make([]int64, len(in))
	for i := range in {
		out[i] = in[len(in)-1-i]
	}
	return out
}

// TestTokenizeFTSTerms is a pure unit test (no DB) for the OR-fallback tokenizer
// added in PR #270. It locks the two behaviors the Gemini reviewer flagged:
// quoted phrases stay intact, and literal boolean operators are dropped.
func TestTokenizeFTSTerms(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"plain terms", "launcher install aimux", []string{"launcher", "install", "aimux"}},
		{"quoted phrase preserved", `"mcp launcher" install`, []string{`"mcp launcher"`, "install"}},
		{"literal OR dropped", "a OR b", []string{"a", "b"}},
		{"literal AND/NOT dropped", "a AND b NOT c", []string{"a", "b", "c"}},
		{"mixed quoted + operator", `"foo bar" OR baz`, []string{`"foo bar"`, "baz"}},
		{"collapses whitespace", "  a   b\t c ", []string{"a", "b", "c"}},
		{"single term", "launcher", []string{"launcher"}},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenizeFTSTerms(tc.query)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestHasNegationTerm locks the negation guard (PR #270 review): a query bearing
// a `-term` exclusion must disable the OR-fallback, because OR-rewriting inverts
// websearch NOT semantics (`postgres -sqlite` -> `'postgres' | !'sqlite'`).
func TestHasNegationTerm(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  bool
	}{
		{"plain multi-word", "postgres sqlite", false},
		{"explicit exclusion", "postgres -sqlite", true},
		{"exclusion first", "-sqlite postgres", true},
		{"lone dash not exclusion", "a - b", false},
		{"quoted phrase not exclusion", `"mcp launcher" deferred`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, hasNegationTerm(tokenizeFTSTerms(tc.query)))
		})
	}
}
