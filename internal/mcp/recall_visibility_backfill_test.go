package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

// TestRecall_ScopeInvisibleNewestDoNotTruncate_CodexP1Cycle3 is the regression
// guard for the codex P1 cycle-3 finding on `4cb71be` (PR #221):
//
//	"Apply scope filtering before final recall limiting — when
//	 ENGRAM_VNEXT_F_ENABLED=true, handleRecallSearch applies scope.Resolve only
//	 after MemoryStore.List has already returned a bounded slice. If the newest
//	 candidates are mostly private rows the caller cannot see, this loop drops
//	 them and never backfills from older eligible rows, so callers can get far
//	 fewer (or zero) results even though visible matches exist in the same
//	 project."
//
// Setup:
//   - 3 newest rows: privacy_scope='private', source_workstation_id='other'
//     (invisible to the caller workstation)
//   - 2 older rows:  privacy_scope='project' (always visible)
//
// Recall as a SourceClient identity whose WorkstationID() returns 'self' (not
// 'other'), with limit=2 and ENGRAM_VNEXT_F_ENABLED=true. Without the
// batch-loop backfill the single-call List(limit=2) path returns the 2
// newest private rows, both fail scope.Resolve (workstation mismatch), and
// the response carries zero results. The fix in this PR adds a batch-loop
// via MemoryStore.ListWithOffset that keeps paging until limit visible
// results accumulate or the project's rows are exhausted.
//
// Anti-stub: replacing the batch-loop with a single ListWithOffset(limit) or
// reverting to List(fetchLimit=limit) breaks this test — the assertion of
// count==2 fails because the older project rows never enter the candidate
// pool.
func TestRecall_ScopeInvisibleNewestDoNotTruncate_CodexP1Cycle3(t *testing.T) {
	project := uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB
	require.NoError(t, db.Create(&dbgorm.Project{ID: project}).Error)

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	// Insert 2 older project-scoped rows first with explicit created_at
	// timestamps. CodeRabbit cycle-4 fix: previously the test relied on
	// implicit `now()` ordering between back-to-back INSERTs, which is
	// non-deterministic when multiple inserts complete within the same
	// timestamp tick. Explicit timestamps with multi-minute gaps make the
	// `ORDER BY created_at DESC, id DESC` recall pagination stable on any
	// PostgreSQL instance regardless of clock resolution. These rows are
	// always visible to anyone in the project.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '5 minutes')`,
		project, "older project row A", `[]`, "project", "",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '4 minutes')`,
		project, "older project row B", `[]`, "project", "",
	).Error)

	// Insert 3 newest private rows owned by a DIFFERENT workstation with
	// strictly later timestamps. These are the rows that would truncate the
	// recall under the old single-call path because they fail scope.Resolve
	// and are the freshest by created_at.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '3 minutes')`,
		project, "newest private row 1", `[]`, "private", "other-workstation",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '2 minutes')`,
		project, "newest private row 2", `[]`, "private", "other-workstation",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '1 minute')`,
		project, "newest private row 3", `[]`, "private", "other-workstation",
	).Error)

	// Build a context carrying a SourceClient identity whose WorkstationID()
	// returns 'self-workstation'. Per auth/identity.go:111-116, WorkstationID
	// returns KeycardID when Source == SourceClient.
	callerIdentity := auth.Identity{
		Role:      auth.Role("admin"),
		Source:    auth.SourceClient,
		KeycardID: "self-workstation",
	}
	ctx := auth.WithIdentity(context.Background(), callerIdentity)

	args, err := json.Marshal(map[string]any{
		"action":  "search",
		"project": project,
		"limit":   2,
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecall(ctx, args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	count, _ := resp["count"].(float64)
	require.Equal(t, 2, int(count),
		"recall must backfill from older visible rows when newest are scope-invisible — "+
			"got count=%v which means batch-loop is not finding the project rows",
		int(count))

	mems, _ := resp["memories"].([]any)
	require.Len(t, mems, 2, "memories slice must carry exactly the 2 visible rows")

	// Both visible rows must be the older project-scoped fixtures, NOT the
	// newest private rows (which fail scope.Resolve).
	visibleContents := map[string]bool{}
	for _, raw := range mems {
		m, ok := raw.(map[string]any)
		require.True(t, ok)
		c, _ := m["content"].(string)
		visibleContents[c] = true
		ps, _ := m["privacy_scope"].(string)
		require.Equal(t, "project", ps,
			"every returned row must be project-scoped (private rows belong to other workstation)")
	}
	require.True(t, visibleContents["older project row A"], "older project row A must be returned")
	require.True(t, visibleContents["older project row B"], "older project row B must be returned")
}

func TestRecall_PrincipalPrivateInvisibleNewestDoNotTruncate_FlagOff(t *testing.T) {
	project := uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB
	require.NoError(t, db.Create(&dbgorm.Project{ID: project}).Error)

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '5 minutes')`,
		project, "older visible row A", `[]`, "private", "keycard-alice",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, created_at)
			VALUES (?, ?, ?::jsonb, ?, now() - interval '4 minutes')`,
		project, "older visible row B", `[]`, "project",
	).Error)

	for i := 1; i <= 3; i++ {
		require.NoError(t, db.Exec(
			`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
				VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - (?::int * interval '1 minute'))`,
			project, "newest principal-private row "+string(rune('0'+i)), `[]`, "project", "agent/bob", "agent", "private", i,
		).Error)
	}

	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
	args, err := json.Marshal(map[string]any{
		"action":  "search",
		"project": project,
		"limit":   2,
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecall(ctx, args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.Equal(t, 2, int(resp["count"].(float64)))

	mems := resp["memories"].([]any)
	contents := map[string]bool{}
	for _, raw := range mems {
		row := raw.(map[string]any)
		content := row["content"].(string)
		require.False(t, strings.Contains(content, "principal-private"),
			"cross-principal private row leaked through recall(action=search)")
		contents[content] = true
	}
	require.True(t, contents["older visible row A"])
	require.True(t, contents["older visible row B"])
}

func TestRecallMemory_PrincipalPrivateInvisibleAndSharedAttributed_FlagOff(t *testing.T) {
	project := uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB
	require.NoError(t, db.Create(&dbgorm.Project{ID: project}).Error)

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "")

	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - interval '2 minutes')`,
		project, "shared team memory", `["type:fact"]`, "project", "agent/bob", "agent", "shared",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - interval '1 minute')`,
		project, "private bob memory", `["type:fact"]`, "project", "agent/bob", "agent", "private",
	).Error)

	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
	args, err := json.Marshal(map[string]any{
		"query":   "memory",
		"project": project,
		"format":  "items",
		"limit":   5,
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecallMemory(ctx, args)
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "shared team memory", rows[0]["content"])
	require.Equal(t, "agent/bob", rows[0]["owner_principal"])
	require.Equal(t, "agent", rows[0]["owner_principal_kind"])
	require.Equal(t, "shared", rows[0]["agent_visibility"])
}

func TestRecallMemory_DomainOwnedInvisibleNewestDoNotTruncate_FlagOff(t *testing.T) {
	project := uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB
	require.NoError(t, db.Create(&dbgorm.Project{ID: project}).Error)

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "")

	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, domain, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, ?, now() - interval '5 minutes')`,
		project, "older alice domain memory A", `["type:fact"]`, "project", "agent/alice", "agent", "shared", "memory-lab",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, domain, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, ?, now() - interval '4 minutes')`,
		project, "older alice domain memory B", `["type:fact"]`, "project", "agent/alice", "agent", "shared", "memory-lab",
	).Error)

	for i := 1; i <= 3; i++ {
		require.NoError(t, db.Exec(
			`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, domain, created_at)
				VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, ?, now() - (?::int * interval '1 minute'))`,
			project, "newest bob domain memory "+string(rune('0'+i)), `["type:fact"]`, "project", "agent/bob", "agent", "shared", "memory-lab", i,
		).Error)
	}

	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
	args, err := json.Marshal(map[string]any{
		"query":   "domain memory",
		"project": project,
		"format":  "items",
		"limit":   2,
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecallMemory(ctx, args)
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 2)

	contents := map[string]bool{}
	for _, row := range rows {
		content := row["content"].(string)
		require.False(t, strings.Contains(content, "bob domain memory"),
			"cross-principal domain-owned row leaked through recall_memory")
		require.Equal(t, "agent/alice", row["owner_principal"])
		require.Equal(t, "agent", row["owner_principal_kind"])
		require.Equal(t, "memory-lab", row["domain"])
		contents[content] = true
	}
	require.True(t, contents["older alice domain memory A"])
	require.True(t, contents["older alice domain memory B"])
}

func TestRecallMemory_IncludeSupersededRecoveryPreservesHistoryPrivacy(t *testing.T) {
	project := uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB
	require.NoError(t, db.Create(&dbgorm.Project{ID: project}).Error)
	queryMarker := "GO17_PRIVATE_QUERY_" + uuid.NewString()
	sessionID := "GO17_PRIVATE_SESSION_" + uuid.NewString()
	query := "contract memory " + queryMarker

	for _, row := range []struct {
		content, status, privacy, workstation, owner, ownerKind, visibility string
		confidence                                                          float64
		age                                                                 int
	}{
		{"active contract memory", "active", "project", "", "", "", "", 0.9, 6},
		{"superseded contract memory", "superseded", "private", "keycard-alice", "", "", "", 0.8, 5},
		{"low confidence contract memory", "superseded", "project", "", "", "", "", 0.2, 4},
		{"foreign principal private contract memory", "superseded", "project", "", "agent/bob", "agent", "private", 0.9, 3},
		{"foreign workstation contract memory", "superseded", "private", "keycard-bob", "", "", "", 0.9, 2},
		{"unknown workstation contract memory", "superseded", "private", "", "", "", "", 0.9, 1},
	} {
		require.NoError(t, db.Exec(
			`INSERT INTO memories (project, content, tags, status, confidence, privacy_scope,
			 source_workstation_id, source_sessions, owner_principal, owner_principal_kind, agent_visibility, created_at)
			 VALUES (?, ?, '["type:fact"]'::jsonb, ?, ?, ?, ?, ARRAY[?]::text[], ?, ?, ?, now() - (?::int * interval '1 minute'))`,
			project, row.content+" "+queryMarker, row.status, row.confidence, row.privacy, row.workstation, sessionID,
			row.owner, row.ownerKind, row.visibility, row.age,
		).Error)
	}
	var before, after []string
	require.NoError(t, db.Raw(`SELECT to_jsonb(m)::text FROM memories m WHERE project = ? ORDER BY id`, project).Scan(&before).Error)

	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-only", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
	request := map[string]any{
		"query":              query,
		"project":            project,
		"format":             "items",
		"limit":              2,
		"include_superseded": true,
		"confidence_min":     0.7,
		"session_id":         sessionID,
		"include_scopes":     []string{"project", "private"},
	}
	var logs bytes.Buffer
	originalLogger := log.Logger
	log.Logger = zerolog.New(&logs)
	t.Cleanup(func() { log.Logger = originalLogger })
	_, err := env.srv.callTool(ctx, "recall_memory", mustJSON(t, request))
	require.ErrorContains(t, err, "include_superseded")
	for _, payload := range []string{queryMarker, sessionID, project} {
		assert.NotContains(t, err.Error(), payload, "logged Go error must be payload-free")
	}
	failed := env.srv.HandleRequest(ctx, &Request{
		JSONRPC: "2.0", ID: 17, Method: "tools/call",
		Params: mustJSON(t, map[string]any{"name": "recall_memory", "arguments": request}),
	})
	require.NotNil(t, failed.Error, "unsupported historical hybrid filter must remain an error")
	require.Equal(t, -32000, failed.Error.Code)
	require.Nil(t, failed.Result, "recovery must not turn rejection into a successful tool response")
	require.Contains(t, failed.Error.Message, `recall(action="search")`)
	for _, payload := range []string{queryMarker, sessionID, project} {
		assert.NotContains(t, failed.Error.Message, payload)
		assert.NotContains(t, logs.String(), payload, "public MCP handler must not log private recovery arguments")
	}
	require.Contains(t, logs.String(), `"tool":"recall_memory"`)
	require.Contains(t, logs.String(), `"message":"Tool call failed"`)
	var recoveryData struct {
		Code     string `json:"code"`
		Recovery struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		} `json:"recovery"`
	}
	wireData, err := json.Marshal(failed.Error.Data)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(wireData, &recoveryData))
	require.Equal(t, "INCLUDE_SUPERSEDED_UNSUPPORTED", recoveryData.Code)
	require.Equal(t, "recall", recoveryData.Recovery.Tool)
	recovery := recoveryData.Recovery.Arguments
	recoveryJSON := mustJSON(t, recovery)
	want := make(map[string]any, len(request))
	for key, value := range request {
		if key != "format" {
			want[key] = value
		}
	}
	want["action"] = "search"
	require.JSONEq(t, string(mustJSON(t, want)), string(recoveryJSON))
	props := findToolProperties(t, env.srv.ListTools(), "recall")
	for key := range recovery {
		require.Contains(t, props, key, "every recovery argument must be in the advertised public schema")
	}

	out, err := projectContextToolCall(t, env.srv, ctx, recoveryData.Recovery.Tool, recovery)
	require.NoError(t, err)
	var response struct {
		Memories []struct {
			ID      int64  `json:"id"`
			Project string `json:"project"`
			Content string `json:"content"`
		} `json:"memories"`
		Count int `json:"count"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	require.Equal(t, 2, response.Count)
	require.Len(t, response.Memories, 2)
	contents := make([]string, 0, 2)
	for _, row := range response.Memories {
		require.Equal(t, project, row.Project)
		require.Positive(t, row.ID)
		contents = append(contents, row.Content)
	}
	require.ElementsMatch(t, []string{"active contract memory " + queryMarker, "superseded contract memory " + queryMarker}, contents)
	require.NoError(t, db.Raw(`SELECT to_jsonb(m)::text FROM memories m WHERE project = ? ORDER BY id`, project).Scan(&after).Error)
	require.Equal(t, before, after, "hybrid rejection and read-only historical recovery must not mutate stored memories")
}

func TestRecallHistoryRecoverySchema(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	for _, expected := range []struct{ property, typeName string }{
		{"include_superseded", "boolean"},
		{"confidence_min", "number"},
		{"session_id", "string"},
		{"include_scopes", "array"},
		{"include_rationale", "boolean"},
	} {
		requireMutationSchemaType(t, srv.ListTools(), "recall", expected.property, expected.typeName)
	}
	props := recallMemoryTool().InputSchema["properties"].(map[string]any)
	description := props["include_superseded"].(map[string]any)["description"].(string)
	require.Contains(t, description, `recall(action="search")`)
}
