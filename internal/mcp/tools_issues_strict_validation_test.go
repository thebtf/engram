package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issues"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"
)

func TestIssuesRejectWrongStructuredFieldTypesBeforeMutation(t *testing.T) {
	server := NewServer(ServerOptions{Version: "strict-issues"})
	// A zero-value store makes an accidental durable call fail the test instead of
	// accepting a coercion. Every malformed request must fail during validation.
	server.SetIssueStore(&gormdb.IssueStore{})

	for _, tc := range []struct {
		name  string
		raw   string
		field string
	}{
		{"create title", `{"action":"create","project":"source","title":1,"target_project":"target"}`, "title"},
		{"create body", `{"action":"create","project":"source","title":"title","body":1,"target_project":"target"}`, "body"},
		{"create priority", `{"action":"create","project":"source","title":"title","target_project":"target","priority":1}`, "priority"},
		{"create type", `{"action":"create","project":"source","title":"title","target_project":"target","type":1}`, "type"},
		{"create agent source", `{"action":"create","project":"source","title":"title","target_project":"target","agent_source":1}`, "agent_source"},
		{"create labels", `{"action":"create","project":"source","title":"title","target_project":"target","labels":"valid"}`, "labels"},
		{"create label item", `{"action":"create","project":"source","title":"title","target_project":"target","labels":["valid",1]}`, "labels[1]"},
		{"update status", `{"action":"update","project":"source","id":1,"status":1}`, "status"},
		{"update comment", `{"action":"update","project":"source","id":1,"status":"resolved","comment":1}`, "comment"},
		{"update agent source", `{"action":"update","project":"source","id":1,"status":"resolved","agent_source":1}`, "agent_source"},
		{"comment body", `{"action":"comment","project":"source","id":1,"body":1}`, "body"},
		{"comment agent source", `{"action":"comment","project":"source","id":1,"body":"comment","agent_source":1}`, "agent_source"},
		{"reopen comment", `{"action":"reopen","project":"source","id":1,"comment":1}`, "comment"},
		{"reopen agent source", `{"action":"reopen","project":"source","id":1,"agent_source":1}`, "agent_source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := server.handleIssues(context.Background(), json.RawMessage(tc.raw))
			require.ErrorContains(t, err, tc.field)
			require.Empty(t, out)
		})
	}
}

func TestIssuesGetSelectorIsStrictAndLossless(t *testing.T) {
	server := NewServer(ServerOptions{Version: "strict-issues"})
	server.SetIssueStore(&gormdb.IssueStore{})

	valid, err := parseArgs(json.RawMessage(`{"action":"get","id":9007199254740993}`))
	require.NoError(t, err)
	require.NoError(t, validateIssueActionParams("get", valid))
	id, err := requireInt64Arg(valid, "id")
	require.NoError(t, err)
	require.EqualValues(t, 9007199254740993, id)

	for _, raw := range []string{
		`{"action":"get","id":"1"}`,
		`{"action":"get","id":1.5}`,
		`{"action":"get","id":1e3}`,
		`{"action":"get","id":-1}`,
		`{"action":"get","id":9223372036854775808}`,
		`{"action":"get","id":0}`,
	} {
		out, err := server.handleIssues(context.Background(), json.RawMessage(raw))
		require.Error(t, err, raw)
		require.Empty(t, out, raw)
	}
}

func TestIssueCreateRecoveryIdentifiesKnownRolesAndPreservesAmbiguity(t *testing.T) {
	const canonical = "b42c9854-bf02-4d1b-867d-a2a096f9e4d0"
	const refusal = "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP"
	for _, tc := range []struct {
		name, source, target string
		want, avoid          []string
		wantInsert           bool
	}{
		{"target", canonical, "private-target", []string{"target_project", "canonical", "authorized", "legacy_id", "name", "adopt"}, []string{"project_context", "private-target"}, false},
		{"source", "private-source", canonical, []string{"source_project", "project_context", "repair", "marker", "hash"}, []string{"private-source"}, false},
		{"store", canonical, canonical, []string{"source_project", "target_project", "does not identify", "unknown", "replay"}, []string{"private-source", "private-target"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
			require.NoError(t, err)
			require.NoError(t, db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY, removed_at TEXT, identity_status TEXT)`).Error)
			require.NoError(t, db.Exec(`INSERT INTO projects (id, identity_status) VALUES (?, 'active')`, canonical).Error)
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:sqlite-admission-lock", func(tx *gorm.DB) {
				delete(tx.Statement.Clauses, "FOR")
			}))
			require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:empty-redirects", func(tx *gorm.DB) {
				tx.Statement.SQL.Reset()
				tx.Statement.SQL.WriteString("SELECT '' WHERE 0")
				tx.Statement.Vars = nil
			}))
			inserts := 0
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:refuse-insert", func(tx *gorm.DB) {
				inserts++
				tx.AddError(&pgconn.PgError{Code: "P0001", Message: refusal})
			}))
			server := NewServer(ServerOptions{})
			server.SetIssueStore(gormdb.NewIssueStore(db))
			ctx := auth.WithIdentity(contextWithProject(context.Background(), tc.source), auth.Admin())
			out, err := callIssueAction(t, server, ctx, map[string]any{"action": "create", "title": "recovery", "target_project": tc.target})
			require.Empty(t, out)
			require.ErrorContains(t, err, refusal)
			for _, part := range tc.want {
				require.Contains(t, strings.ToLower(err.Error()), strings.ToLower(part))
			}
			for _, part := range tc.avoid {
				require.NotContains(t, err.Error(), part)
			}
			if tc.wantInsert {
				require.Equal(t, 1, inserts)
				var safe *issues.Error
				require.ErrorAs(t, err, &safe)
				require.Equal(t, refusal, safe.Code)
			} else {
				require.Zero(t, inserts)
				var hold *gormdb.ConsolidationError
				require.ErrorAs(t, err, &hold)
				require.Equal(t, refusal, hold.Code)
			}
			inserts = 0
			params, err := json.Marshal(map[string]any{"name": "issues", "arguments": map[string]any{"action": "create", "title": "recovery", "target_project": tc.target}})
			require.NoError(t, err)
			response := server.HandleRequest(ctx, &Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
			encoded, err := json.Marshal(response)
			require.NoError(t, err)
			var wire struct {
				Error struct {
					Data    string `json:"data"`
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(encoded, &wire))
			require.Equal(t, refusal, wire.Error.Data)
			for _, part := range tc.want {
				require.Contains(t, strings.ToLower(wire.Error.Message), strings.ToLower(part))
			}
			for _, part := range tc.avoid {
				require.NotContains(t, wire.Error.Message, part)
			}
			if tc.wantInsert {
				require.Equal(t, 1, inserts)
			} else {
				require.Zero(t, inserts)
			}
		})
	}
}

func TestIssueSchemaExplainsExactProjectFilters(t *testing.T) {
	properties := issuesToolSchema()["properties"].(map[string]any)
	for _, field := range []string{"project", "source_project"} {
		description := strings.ToLower(properties[field].(map[string]any)["description"].(string))
		require.Contains(t, description, "exact")
		require.Contains(t, description, "not name resolution")
		require.Contains(t, description, "do not prove no canonical issues")
	}
	description := properties["target_project"].(map[string]any)["description"].(string)
	require.Contains(t, description, "canonical project UUID")
	require.Contains(t, description, "authorized target")
}

func TestIssuesPublicListBoundsAndGetAuthPrecedence(t *testing.T) {
	server := NewServer(ServerOptions{Version: "issue-bounds"})
	server.SetIssueStore(&gormdb.IssueStore{})
	ctx := auth.WithIdentity(context.Background(), auth.Client("read-only", "keycard-read-only"))

	for _, tc := range []struct {
		name  string
		limit any
	}{
		{"above maximum", 201},
		{"fraction", 1.5},
		{"string", "1"},
		{"boolean", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := callIssues513Native(t, server, ctx, map[string]any{"action": "list", "limit": tc.limit})
			text := issues513NativeText(t, response)
			require.Contains(t, text, "INVALID_ARGUMENT")
			require.Contains(t, text, "limit must be between 1 and 200")
		})
	}

	withoutStore := NewServer(ServerOptions{Version: "issue-bounds"})
	_, err := withoutStore.handleIssues(context.Background(), json.RawMessage(`{"action":"get","id":1}`))
	require.ErrorContains(t, err, "UNAUTHENTICATED")
	_, err = withoutStore.handleIssues(ctx, json.RawMessage(`{"action":"get","id":1}`))
	require.ErrorContains(t, err, "UNAVAILABLE")
	_, err = server.handleIssues(ctx, json.RawMessage(`{"action":"get","id":1.5}`))
	require.ErrorContains(t, err, "INVALID_ARGUMENT")
}

func TestIssuesTargetSessionSchemaAndAdmission(t *testing.T) {
	server := NewServer(ServerOptions{Version: "issue-session"})
	properties := findToolProperties(t, server.primaryTools(), "issues")
	field := properties["target_session"].(map[string]any)
	require.Equal(t, []string{"string", "null"}, field["type"])
	description := field["description"].(string)
	for _, phrase := range []string{"Create/update", "update may use null", "List", "null is invalid for create and list", "provenance", "not authentication or authority"} {
		require.Contains(t, description, phrase)
	}

	ctx := auth.WithIdentity(context.Background(), auth.Client("read-write", "keycard-read-write"))
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"create string", `{"action":"create","title":"task","target_session":"session"}`, "UNAVAILABLE"},
		{"update string", `{"action":"update","id":1,"target_session":"session"}`, "UNAVAILABLE"},
		{"update clear", `{"action":"update","id":1,"target_session":null}`, "UNAVAILABLE"},
		{"list string", `{"action":"list","target_session":"session"}`, "UNAVAILABLE"},
		{"create null", `{"action":"create","title":"task","target_session":null}`, "INVALID_ARGUMENT"},
		{"list null", `{"action":"list","target_session":null}`, "INVALID_ARGUMENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.handleIssues(ctx, json.RawMessage(tc.raw))
			require.ErrorContains(t, err, tc.want)
		})
	}
}
