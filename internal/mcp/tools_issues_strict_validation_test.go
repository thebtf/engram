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
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr)
				require.Equal(t, refusal, pgErr.Message)
			} else {
				require.Zero(t, inserts)
				var hold *gormdb.ConsolidationError
				require.ErrorAs(t, err, &hold)
				require.Equal(t, refusal, hold.Code)
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
