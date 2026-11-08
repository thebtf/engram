package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issuefixture"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This opt-in target is separate from ambient DATABASE_DSN. The caller must
// supply its own disposable loopback PostgreSQL/pgvector database, never an app DB.
func openIssues513NativeStore(t *testing.T) (*gormdb.Store, *gormdb.IssueStore, string) {
	t.Helper()
	dsn := os.Getenv("ISSUE513_NATIVE_TEST_DSN")
	if dsn == "" {
		t.Skip("ISSUE513_NATIVE_TEST_DSN not set; requires an owned disposable local PostgreSQL fixture")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, parsed.Scheme)
	require.Equal(t, "127.0.0.1", parsed.Hostname())
	require.NotEmpty(t, parsed.Port())
	require.Equal(t, "/issue513_native_synthetic", parsed.Path)
	require.NotNil(t, parsed.User)
	require.Equal(t, "issue513_native", parsed.User.Username())
	require.Empty(t, parsed.RawQuery, "fixture adds its own search path and connection options")
	query := parsed.Query()
	query.Set("sslmode", "disable")
	query.Set("connect_timeout", "5")
	query.Set("application_name", "issue513_native_contract_test")
	parsed.RawQuery = query.Encode()
	admin, err := gormlib.Open(postgres.Open(parsed.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	adminSQL.SetMaxOpenConns(1)
	adminSQL.SetMaxIdleConns(1)
	t.Cleanup(func() { assert.NoError(t, adminSQL.Close()) })
	var target struct {
		Database string
		Username string
	}
	require.NoError(t, admin.Raw(`SELECT current_database() AS database, current_user AS username`).Scan(&target).Error)
	require.Equal(t, "issue513_native_synthetic", target.Database)
	require.Equal(t, "issue513_native", target.Username)
	require.NoError(t, admin.Exec(`CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`).Error)
	schema := "issue513_native_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { assert.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	store, err := gormdb.NewStore(gormdb.Config{DSN: parsed.String(), MaxConns: 2, LogLevel: logger.Silent})
	require.NoError(t, err, "normal migrations must succeed before any contract assertion")
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	var actualSchema string
	require.NoError(t, store.GetDB().Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
	require.Equal(t, schema, actualSchema)
	for _, table := range []string{"projects", "issues", "issue_comments"} {
		var owner string
		require.NoError(t, store.GetDB().Raw(`SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass(?)`, table).Scan(&owner).Error)
		require.Equal(t, schema, owner, "fixture table must belong to the isolated schema")
	}
	project := uuid.NewString()
	require.NoError(t, store.GetDB().Create(&gormdb.Project{
		ID: project, ProjectKey: sql.NullString{String: project, Valid: true},
		DisplayName:    sql.NullString{String: "Synthetic native issue 513", Valid: true},
		IdentityStatus: sql.NullString{String: "active", Valid: true}, LegacyIDs: []string{},
	}).Error)
	sessions := gormdb.NewSessionStore(store)
	exists, err := sessions.CanonicalProjectExists(t.Context(), project)
	require.NoError(t, err)
	require.True(t, exists)
	issues := gormdb.NewIssueStore(store.GetDB())
	resolved, err := issues.ResolveProject(t.Context(), project)
	require.NoError(t, err)
	require.Equal(t, project, resolved)
	t.Logf("owned synthetic target database=%s schema=%s; normal migrations and canonical admission succeeded", target.Database, schema)
	return store, issues, project
}

func callIssues513Native(t *testing.T, server *Server, ctx context.Context, args map[string]any) *Response {
	t.Helper()
	response := server.HandleRequest(ctx, &Request{
		JSONRPC: "2.0", ID: t.Name(), Method: "tools/call",
		Params: mustJSON(t, ToolCallParams{Name: "issues", Arguments: mustJSON(t, args)}),
	})
	require.NotNil(t, response)
	var wire bytes.Buffer
	previous := server.stdout
	server.stdout = &wire
	server.sendResponse(response)
	server.stdout = previous
	var decoded Response
	require.NoError(t, json.Unmarshal(wire.Bytes(), &decoded), "inspect the actual terminal MCP JSON serialization")
	require.Equal(t, "2.0", decoded.JSONRPC)
	require.Equal(t, t.Name(), decoded.ID)
	return &decoded
}

func issues513NativeText(t *testing.T, response *Response) string {
	t.Helper()
	if response.Error != nil {
		return response.Error.Message
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(mustJSON(t, response.Result), &result))
	require.Len(t, result.Content, 1, "the native result remains one text-content envelope")
	require.Equal(t, "text", result.Content[0].Type)
	return result.Content[0].Text
}

func issues513NativeJSON(t *testing.T, response *Response) map[string]json.RawMessage {
	t.Helper()
	var raw []byte
	if response.Error != nil {
		// Typed Go errors may be mapped to structured JSON-RPC error data above
		// the issue handler. Never require private err.Error() to contain JSON.
		raw = mustJSON(t, response.Error.Data)
	} else {
		raw = []byte(issues513NativeText(t, response))
	}
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &result), "format=json must expose structured issue data at the public MCP boundary")
	require.NotNil(t, result)
	if response.Error != nil {
		if _, wrapped := result["error"]; !wrapped {
			return map[string]json.RawMessage{"error": raw}
		}
	}
	return result
}

func issues513NoCredentialFields(t *testing.T, fields map[string]json.RawMessage) {
	t.Helper()
	for _, key := range []string{"CreatorKeycardID", "creator_keycard_id", "api_key", "access_token", "refresh_token", "authorization", "password", "private_key"} {
		assert.NotContains(t, fields, key, "internal ownership and credential fields are not public issue data; safe actor references remain allowed")
	}
}

// These assertions exercise public MCP dispatch and terminal result marshalling
// against the concrete migrated store. They do not model server CAS/replay,
// future history/metadata persistence, or the ordinary muxcore/gRPC envelope.
func TestIssues513NativeContract(t *testing.T) {
	store, issues, project := openIssues513NativeStore(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issues)
	ctx := issueClientContext(project, "synthetic-513-owner", auth.RoleReadWrite)
	var issueID int64
	err := issuefixture.SeedHistorical(store.GetDB(), func(tx *gormlib.DB) error {
		legacy := gormdb.NewIssueStore(tx)
		var err error
		issueID, err = legacy.CreateIssue(ctx, &gormdb.Issue{
			Title: "Synthetic resumable native detail", Body: "Exact body: Синтетический результат.\n<quoted-data> & \"text\"",
			SourceProject: project, TargetProject: project, SourceAgent: "synthetic-agent",
			CreatorKeycardID: "synthetic-513-owner", Type: "task", Priority: "medium",
		})
		if err != nil {
			return err
		}
		for i := range 52 {
			if _, err := legacy.AddComment(ctx, issueID, &gormdb.IssueComment{
				AuthorProject: project, AuthorAgent: "synthetic-commenter", Body: fmt.Sprintf("synthetic-comment-%03d", i),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	before, originalComments, err := issues.GetIssue(ctx, issueID)
	require.NoError(t, err)
	require.Len(t, originalComments, 52)
	t.Cleanup(func() {
		after, comments, readErr := issues.GetIssue(context.Background(), issueID)
		require.NoError(t, readErr)
		assert.Equal(t, before, after, "reads and denied mutations must preserve every original issue field")
		assert.Equal(t, originalComments, comments, "reads and denied mutations must preserve comment IDs, bytes, order and attribution")
	})

	t.Run("default_text_is_bounded_and_names_the_next_call", func(t *testing.T) {
		response := callIssues513Native(t, server, ctx, map[string]any{"action": "get", "id": issueID})
		require.Nil(t, response.Error)
		out := issues513NativeText(t, response)
		assert.Contains(t, out, originalComments[0].Body)
		assert.Contains(t, out, originalComments[49].Body)
		assert.NotContains(t, out, originalComments[50].Body, "default text must not silently drain beyond its 50-record page")
		assert.NotContains(t, out, originalComments[51].Body)
		assert.Contains(t, out, "52", "show the authorized total, not just the returned count")
		assert.Contains(t, out, "detail_mode", "recovery must name explicit paged detail")
		assert.Contains(t, out, "paged")
		assert.Contains(t, out, "comments_offset", "show an actionable next call rather than pretending the page is complete")
		assert.Contains(t, out, "50")
	})

	t.Run("json_later_and_final_pages_preserve_real_record_identity", func(t *testing.T) {
		for _, offset := range []int{50, 51} {
			t.Run(fmt.Sprintf("offset_%d", offset), func(t *testing.T) {
				response := callIssues513Native(t, server, ctx, map[string]any{
					"action": "get", "id": issueID, "format": "json", "detail_mode": "paged",
					"comments_offset": offset, "comments_limit": 1,
				})
				require.Nil(t, response.Error)
				out := issues513NativeText(t, response)
				result := issues513NativeJSON(t, response)
				for _, key := range []string{"issue", "comments", "comment_count", "comments_offset", "comments_limit", "comments_next_offset", "comments_complete", "comments_oversized"} {
					require.Contains(t, result, key)
				}
				var page struct {
					Issue     gormdb.Issue          `json:"issue"`
					Comments  []gormdb.IssueComment `json:"comments"`
					Count     int                   `json:"comment_count"`
					Offset    int                   `json:"comments_offset"`
					Limit     int                   `json:"comments_limit"`
					Next      *int                  `json:"comments_next_offset"`
					Complete  bool                  `json:"comments_complete"`
					Oversized bool                  `json:"comments_oversized"`
				}
				require.NoError(t, json.Unmarshal([]byte(out), &page))
				assert.Equal(t, issueID, page.Issue.ID)
				assert.Equal(t, before.Body, page.Issue.Body)
				assert.Equal(t, project, page.Issue.SourceProject)
				assert.Equal(t, project, page.Issue.TargetProject)
				require.Len(t, page.Comments, 1, "a later-page request must not return earlier or later records")
				assert.Equal(t, originalComments[offset].ID, page.Comments[0].ID)
				assert.Equal(t, originalComments[offset].IssueID, page.Comments[0].IssueID)
				assert.Equal(t, originalComments[offset].AuthorProject, page.Comments[0].AuthorProject)
				assert.Equal(t, originalComments[offset].AuthorAgent, page.Comments[0].AuthorAgent)
				assert.Equal(t, originalComments[offset].Body, page.Comments[0].Body)
				assert.True(t, originalComments[offset].CreatedAt.Equal(page.Comments[0].CreatedAt))
				assert.Equal(t, 52, page.Count)
				assert.Equal(t, offset, page.Offset)
				assert.Equal(t, 1, page.Limit)
				assert.False(t, page.Oversized)
				assert.Equal(t, offset == 51, page.Complete, "complete means no later records, not that earlier pages were loaded")
				if offset == 51 {
					assert.Nil(t, page.Next)
				} else {
					require.NotNil(t, page.Next)
					assert.Equal(t, 51, *page.Next, "continuation must point at the first unreturned record")
				}
				var issueFields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(result["issue"], &issueFields))
				issues513NoCredentialFields(t, issueFields)
			})
		}
	})

	t.Run("oversized_historical_comment_is_intact_singleton_with_continuation", func(t *testing.T) {
		var largeID, firstID int64
		body := strings.Repeat("x", 4*1024*1024+1) + "\nsynthetic-intact-tail"
		insertErr := issuefixture.SeedHistorical(store.GetDB(), func(tx *gormlib.DB) error {
			legacy := gormdb.NewIssueStore(tx)
			var err error
			largeID, err = legacy.CreateIssue(ctx, &gormdb.Issue{
				Title: "Synthetic historical oversized comment", SourceProject: project, TargetProject: project, CreatorKeycardID: "synthetic-513-owner",
			})
			if err != nil {
				return err
			}
			firstID, err = legacy.AddComment(ctx, largeID, &gormdb.IssueComment{AuthorProject: project, Body: body})
			if err != nil {
				return err
			}
			_, err = legacy.AddComment(ctx, largeID, &gormdb.IssueComment{AuthorProject: project, Body: "synthetic-after-oversized"})
			return err
		})
		require.NoError(t, insertErr)
		response := callIssues513Native(t, server, ctx, map[string]any{
			"action": "get", "id": largeID, "format": "json", "detail_mode": "paged", "comments_limit": 200,
		})
		require.Nil(t, response.Error)
		out := issues513NativeText(t, response)
		result := issues513NativeJSON(t, response)
		for _, key := range []string{"comments", "comment_count", "comments_limit", "comments_oversized", "comments_next_offset", "comments_complete"} {
			require.Contains(t, result, key)
		}
		var page struct {
			Comments  []gormdb.IssueComment `json:"comments"`
			Count     int                   `json:"comment_count"`
			Limit     int                   `json:"comments_limit"`
			Oversized bool                  `json:"comments_oversized"`
			Next      *int                  `json:"comments_next_offset"`
			Complete  bool                  `json:"comments_complete"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &page))
		require.Len(t, page.Comments, 1, "over-target record must be an intact singleton, not a drained page")
		assert.Equal(t, firstID, page.Comments[0].ID)
		assert.Equal(t, sha256.Sum256([]byte(body)), sha256.Sum256([]byte(page.Comments[0].Body)), "oversized bytes must not be trimmed")
		assert.Equal(t, 2, page.Count)
		assert.Equal(t, 200, page.Limit, "effective limit is the admitted limit, not returned length")
		assert.True(t, page.Oversized)
		assert.False(t, page.Complete)
		require.NotNil(t, page.Next)
		assert.Equal(t, 1, *page.Next, "do not skip the record after the oversized singleton")
	})

	t.Run("denied_json_comment_retains_typed_error_without_content_or_effects", func(t *testing.T) {
		response := callIssues513Native(t, server, issueClientContext(project, "synthetic-513-read-only", auth.RoleReadOnly), map[string]any{
			"action": "comment", "id": issueID, "body": "synthetic-denied-draft", "format": "json",
		})
		public := string(mustJSON(t, response))
		assert.NotContains(t, public, strings.SplitN(before.Body, "\n", 2)[0], "body sentinel must remain absent even after JSON escaping")
		assert.NotContains(t, public, "synthetic-denied-draft")
		assert.NotContains(t, public, originalComments[0].Body)
		_, comments, readErr := issues.GetIssue(ctx, issueID)
		require.NoError(t, readErr)
		assert.Equal(t, originalComments, comments, "denial must not append the draft")
		result := issues513NativeJSON(t, response)
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		require.Contains(t, result, "error")
		require.NoError(t, json.Unmarshal(result["error"], &failure))
		assert.Equal(t, "FORBIDDEN", failure.Code)
		assert.NotEmpty(t, failure.Message)
		var details map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(result["error"], &details))
		issues513NoCredentialFields(t, result)
		issues513NoCredentialFields(t, details)
		for _, key := range []string{"revision", "current_revision", "replayed", "issue", "body", "comments", "history", "comment_count", "history_count"} {
			assert.NotContains(t, result, key, "denied result must not disclose protected state or look committed")
			assert.NotContains(t, details, key, "typed denial details must not disclose state either")
		}
	})

	t.Run("missing_text_read_has_stable_code_and_safe_recovery", func(t *testing.T) {
		response := callIssues513Native(t, server, ctx, map[string]any{"action": "get", "id": issueID + 1000000})
		combined := issues513NativeText(t, response)
		assert.Contains(t, combined, "ISSUE_NOT_FOUND")
		assert.Contains(t, strings.ToLower(combined), "list", "name the project-free authorized discovery action rather than substituting identity")
		assert.NotContains(t, combined, before.Body)
		assert.NotContains(t, combined, originalComments[0].Body)
	})
}
