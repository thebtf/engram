package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openIssueToolTestDB(t *testing.T) (*gormdb.Store, *gormdb.IssueStore) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping issue tool integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store, gormdb.NewIssueStore(store.GetDB())
}

func uniqueIssueToolProject(t *testing.T, _ string) string {
	t.Helper()
	return uuid.NewString()
}

func issueClientContext(project, keycard string, role auth.Role) context.Context {
	return auth.WithIdentity(contextWithProject(context.Background(), project), auth.Identity{
		Role: role, Source: auth.SourceClient, KeycardID: keycard,
	})
}

func createIssueToolFixture(t *testing.T, store *gormdb.Store, issueStore *gormdb.IssueStore, project, keycard, status string) int64 {
	t.Helper()
	id, err := issueStore.CreateIssue(context.Background(), &gormdb.Issue{
		Title: "credential ownership regression", SourceProject: project, TargetProject: project,
		Status: status, Type: "bug", Priority: "medium", CreatorKeycardID: keycard,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		store.GetDB().Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
		store.GetDB().Exec(`DELETE FROM issues WHERE id = ?`, id)
	})
	return id
}

func callIssueAction(t *testing.T, server *Server, ctx context.Context, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return server.handleIssues(ctx, raw)
}

func TestIssueCreateBindsSourceClientKeycard(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	project := uniqueIssueToolProject(t, "claimed")
	ctx := issueClientContext(project, "keycard-creator", auth.RoleReadWrite)

	out, err := callIssueAction(t, server, ctx, map[string]any{
		"action": "create", "title": "bound creator", "target_project": project, "project": "spoofed-project-identity",
	})
	require.NoError(t, err)
	var id int64
	_, err = fmt.Sscanf(out, "Issue #%d created", &id)
	require.NoError(t, err)
	t.Cleanup(func() {
		store.GetDB().Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
		store.GetDB().Exec(`DELETE FROM issues WHERE id = ?`, id)
	})
	issue, _, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "keycard-creator", issue.CreatorKeycardID)
	require.Equal(t, project, issue.SourceProject)
}

func TestIssueCollaboratorProgressionProtectsSourceActions(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	project := uniqueIssueToolProject(t, "collaborator")
	owner := issueClientContext(project, "keycard-owner", auth.RoleReadWrite)
	foreign := issueClientContext(project, "keycard-second", auth.RoleReadWrite)

	progressID := createIssueToolFixture(t, store, issueStore, project, "keycard-owner", "open")
	_, err := callIssueAction(t, server, foreign, map[string]any{"action": "update", "id": progressID, "status": "resolved", "project": "spoofed-project"})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, foreign, map[string]any{"action": "comment", "id": progressID, "body": "foreign collaborator", "project": "spoofed-project"})
	require.NoError(t, err)
	issue, comments, err := issueStore.GetIssue(context.Background(), progressID)
	require.NoError(t, err)
	require.Equal(t, "resolved", issue.Status)
	require.Len(t, comments, 1)
	require.Equal(t, "foreign collaborator", comments[0].Body)

	for _, tc := range []struct {
		name   string
		action string
	}{
		{"reopen", "reopen"},
		{"close", "close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := createIssueToolFixture(t, store, issueStore, project, "keycard-owner", "resolved")
			_, err := callIssueAction(t, server, foreign, map[string]any{"action": tc.action, "id": id, "project": "spoofed-project"})
			require.ErrorContains(t, err, "issue mutation forbidden")
			issue, _, err := issueStore.GetIssue(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, "resolved", issue.Status)
		})
	}

	ownerID := createIssueToolFixture(t, store, issueStore, project, "keycard-owner", "resolved")
	_, err = callIssueAction(t, server, owner, map[string]any{"action": "reopen", "id": ownerID, "project": project})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, owner, map[string]any{"action": "update", "id": ownerID, "status": "resolved", "project": project})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, owner, map[string]any{"action": "close", "id": ownerID, "project": project})
	require.NoError(t, err)
}

func TestIssueProgressionRejectsMissingAndReadOnlyIdentity(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	project := uniqueIssueToolProject(t, "readonly")
	id := createIssueToolFixture(t, store, issueStore, project, "keycard-owner", "open")

	_, err := callIssueAction(t, server, context.Background(), map[string]any{"action": "update", "id": id, "status": "resolved", "project": project})
	require.ErrorContains(t, err, "authenticated identity is required")
	_, err = callIssueAction(t, server, issueClientContext(project, "keycard-read-only", auth.RoleReadOnly), map[string]any{"action": "comment", "id": id, "body": "denied", "project": project})
	require.ErrorContains(t, err, "read-write client keycard is required")
	issue, comments, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "open", issue.Status)
	require.Empty(t, comments)
}

func TestLegacyIssueAllowsProgressionButRestrictsSourceActions(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	project := uniqueIssueToolProject(t, "legacy")
	id := createIssueToolFixture(t, store, issueStore, project, "", "open")
	client := issueClientContext(project, "keycard-client", auth.RoleReadWrite)

	_, err := callIssueAction(t, server, client, map[string]any{"action": "update", "id": id, "status": "resolved", "project": project})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, client, map[string]any{"action": "comment", "id": id, "body": "allowed", "project": project})
	require.NoError(t, err)
	for _, action := range []string{"reopen", "close"} {
		_, err = callIssueAction(t, server, client, map[string]any{"action": action, "id": id, "project": project})
		require.ErrorContains(t, err, "issue mutation forbidden")
	}
	issue, _, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "resolved", issue.Status)
	_, err = callIssueAction(t, server, auth.WithIdentity(context.Background(), auth.Admin()), map[string]any{"action": "close", "id": id, "project": project})
	require.NoError(t, err)
}

func TestIssueUpdateRollsBackStatusWhenCommentInsertFails(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	project := uniqueIssueToolProject(t, "atomic-resolve")
	id := createIssueToolFixture(t, store, issueStore, project, "keycard-owner", "open")
	constraint := fmt.Sprintf("issue_comment_atomic_%d", time.Now().UnixNano())
	require.NoError(t, store.GetDB().Exec(fmt.Sprintf(`ALTER TABLE issue_comments ADD CONSTRAINT %s CHECK (body <> 'force-atomic-comment-failure')`, constraint)).Error)
	t.Cleanup(func() {
		_ = store.GetDB().Exec(fmt.Sprintf(`ALTER TABLE issue_comments DROP CONSTRAINT IF EXISTS %s`, constraint)).Error
	})

	_, err := callIssueAction(t, server, issueClientContext(project, "keycard-owner", auth.RoleReadWrite), map[string]any{
		"action": "update", "id": id, "status": "resolved", "comment": "force-atomic-comment-failure", "project": project,
	})
	require.Error(t, err)
	issue, comments, getErr := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, getErr)
	require.Equal(t, "open", issue.Status)
	require.Empty(t, comments)
}

func TestIssueCommentPreservedParentAllowsCanonicalAuthor(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping retained issue PostgreSQL regression")
	}
	admin, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	adminSQL.SetMaxOpenConns(1)
	adminSQL.SetMaxIdleConns(1)
	schema := "issue_comment_retained_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec(`CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`).Error)
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		require.NoError(t, adminSQL.Close())
	})
	fixtureDSN := dsn + " search_path=" + schema + ",public application_name=engram_issue_comment_retained"
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, parseErr := url.Parse(dsn)
		require.NoError(t, parseErr)
		query := parsed.Query()
		query.Set("search_path", schema+",public")
		query.Set("application_name", "engram_issue_comment_retained")
		parsed.RawQuery = query.Encode()
		fixtureDSN = parsed.String()
	}
	seed, err := gormlib.Open(postgres.Open(fixtureDSN), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	seedSQL, err := seed.DB()
	require.NoError(t, err)
	seedSQL.SetMaxOpenConns(1)
	seedSQL.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, seedSQL.Close()) })
	// Seed retained rows before the normal migration chain; never disable a guard.
	require.NoError(t, seed.AutoMigrate(&gormdb.Issue{}))
	retained := []gormdb.Issue{
		{Title: "retained unbound issue", SourceProject: "retained-source", TargetProject: "retained-target", SourceAgent: "retained-agent"},
		{Title: "retained session-bound issue", SourceProject: "retained-session-selector", TargetProject: "retained-session-selector", CreatedBySession: "retained-session", SourceAgent: "retained-agent"},
	}
	for i := range retained {
		retained[i].CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		retained[i].UpdatedAt = retained[i].CreatedAt
		require.NoError(t, seed.Create(&retained[i]).Error)
	}
	require.NoError(t, seedSQL.Close())
	store, err := gormdb.NewStore(gormdb.Config{DSN: fixtureDSN, MaxConns: 2, LogLevel: logger.Silent})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	db := store.GetDB()
	issueStore := gormdb.NewIssueStore(db)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	project, otherProject := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Create(&gormdb.Project{ID: project}).Error)
	require.NoError(t, db.Create(&gormdb.Project{ID: otherProject}).Error)
	for _, redirect := range []struct{ selector, session, project string }{
		{"retained-session-selector", "retained-session", project},
		{"conflicting-selector", "", project},
		{"conflicting-selector", "other-session", otherProject},
	} {
		require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects
			(selector, session_id, project_id, plan_fingerprint) VALUES (?, ?, ?, 'retained-issue-test')`,
			redirect.selector, redirect.session, redirect.project).Error)
	}
	rowJSON := func(id int64) string {
		var raw string
		require.NoError(t, db.Raw(`SELECT to_jsonb(i)::text FROM issues i WHERE id = ?`, id).Scan(&raw).Error)
		return raw
	}
	rollbackProbe := errors.New("rollback diagnostic probe")

	t.Run("unresolved_and_conflicting_selector_writes_stay_denied", func(t *testing.T) {
		id := createIssueToolFixture(t, store, issueStore, project, "retained-owner", "open")
		before := rowJSON(id)
		_, writeErr := issueStore.AddComment(context.Background(), id, &gormdb.IssueComment{
			AuthorProject: "unresolved-comment-selector", Body: "must not persist",
		})
		require.ErrorContains(t, writeErr, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
		for _, role := range []string{"source_project", "target_project"} {
			for _, selector := range []string{"unresolved-parent-selector", uuid.Nil.String()} {
				row := gormdb.Issue{Title: "denied project-role insert", SourceProject: project, TargetProject: project}
				if role == "source_project" {
					row.SourceProject = selector
				} else {
					row.TargetProject = selector
				}
				require.ErrorContains(t, db.Create(&row).Error, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", role+" INSERT")
				err := db.Model(&gormdb.Issue{}).Where("id = ?", id).Update(role, selector).Error
				require.ErrorContains(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", role+" UPDATE")
				require.JSONEq(t, before, rowJSON(id))
			}
		}
		var rejectedRows int64
		require.NoError(t, db.Model(&gormdb.Issue{}).Where("title = ?", "denied project-role insert").Count(&rejectedRows).Error)
		require.Zero(t, rejectedRows)
		var resolved string
		err := db.Raw(`SELECT engram_consolidation_scope('conflicting-selector', 'other-session')`).Row().Scan(&resolved)
		require.ErrorContains(t, err, "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP")
		conflictingMemory := gormdb.Memory{Project: "conflicting-selector", SourceSessions: pq.StringArray{"other-session"}, Content: "conflicting membership"}
		require.ErrorContains(t, db.Create(&conflictingMemory).Error, "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP")
		memory := gormdb.Memory{Project: project, Content: "preserved canonical memory"}
		require.NoError(t, db.Create(&memory).Error)
		require.ErrorContains(t, db.Model(&gormdb.Memory{}).Where("id = ?", memory.ID).Updates(map[string]any{
			"project": "conflicting-selector", "source_sessions": pq.StringArray{"other-session"},
		}).Error, "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP")
		var actualMemory gormdb.Memory
		require.NoError(t, db.First(&actualMemory, memory.ID).Error)
		require.Equal(t, memory.Project, actualMemory.Project)
		require.Empty(t, actualMemory.SourceSessions)
		_, comments, err := issueStore.GetIssue(context.Background(), id)
		require.NoError(t, err)
		require.Empty(t, comments)
	})

	t.Run("canonical_comment_rolls_back_when_parent_timestamp_write_fails", func(t *testing.T) {
		id := createIssueToolFixture(t, store, issueStore, project, "retained-owner", "open")
		before := rowJSON(id)
		require.NoError(t, db.Exec(fmt.Sprintf(`ALTER TABLE issues ADD CONSTRAINT issue_comment_parent_write_atomic
			CHECK (id <> %d OR updated_at = created_at)`, id)).Error)
		t.Cleanup(func() {
			require.NoError(t, db.Exec(`ALTER TABLE issues DROP CONSTRAINT issue_comment_parent_write_atomic`).Error)
		})
		response := server.HandleRequest(issueClientContext(project, "collaborator-keycard", auth.RoleReadWrite), &Request{
			JSONRPC: "2.0", ID: "atomic-comment-refusal", Method: "tools/call",
			Params: mustJSON(t, map[string]any{"name": "issues", "arguments": map[string]any{
				"action": "comment", "id": id, "body": "rolled-back canonical comment", "project": project,
			}}),
		})
		require.NotNil(t, response.Error)
		require.Contains(t, response.Error.Message, "issue_comment_parent_write_atomic")
		_, comments, err := issueStore.GetIssue(context.Background(), id)
		require.NoError(t, err)
		require.Empty(t, comments)
		require.JSONEq(t, before, rowJSON(id))
		t.Log("MCP atomicity: independent parent CHECK refusal rolled back canonical comment INSERT and parent timestamp")
	})

	for _, parent := range retained {
		t.Run(parent.Title, func(t *testing.T) {
			before := rowJSON(parent.ID)
			err := db.Transaction(func(tx *gormlib.DB) error {
				if insertErr := tx.Create(&gormdb.IssueComment{IssueID: parent.ID, AuthorProject: project, Body: "canonical insert probe"}).Error; insertErr != nil {
					return insertErr
				}
				return rollbackProbe
			})
			require.ErrorIs(t, err, rollbackProbe, "canonical comment INSERT must pass the real 186 guard")
			t.Log("SQL phase: canonical comment INSERT passed; diagnostic transaction rolled back")
			parentErr := db.Transaction(func(tx *gormlib.DB) error {
				if updateErr := tx.Model(&gormdb.Issue{}).Where("id = ?", parent.ID).Update("updated_at", time.Now()).Error; updateErr != nil {
					return updateErr
				}
				return rollbackProbe
			})
			require.ErrorIs(t, parentErr, rollbackProbe, "timestamp-only UPDATE must not rebind retained project roles")
			t.Log("SQL phase: timestamp-only UPDATE passed; diagnostic transaction rolled back")
			require.JSONEq(t, before, rowJSON(parent.ID), "diagnostic probes must leave the retained issue unchanged")
			var historyBefore string
			require.NoError(t, db.Raw(`SELECT (to_jsonb(i)-'updated_at')::text FROM issues i WHERE id = ?`, parent.ID).Scan(&historyBefore).Error)
			if parent.CreatedBySession != "" {
				var resolved string
				require.NoError(t, db.Raw(`SELECT engram_consolidation_scope(?, ?)`, parent.SourceProject, parent.CreatedBySession).Row().Scan(&resolved))
				require.Equal(t, project, resolved, "the session-scoped redirect itself is valid")
			}
			out, commentErr := callIssueAction(t, server, issueClientContext(project, "collaborator-keycard", auth.RoleReadWrite), map[string]any{
				"action": "comment", "id": parent.ID, "body": "ordinary retained issue comment", "project": "untrusted-argument",
			})
			actual, comments, getErr := issueStore.GetIssue(context.Background(), parent.ID)
			require.NoError(t, getErr)
			if commentErr != nil {
				require.Empty(t, comments, "parent UPDATE refusal must roll back the successful comment INSERT")
				require.JSONEq(t, before, rowJSON(parent.ID), "failed comment must leave every parent field unchanged")
				t.Log("MCP refusal: successful comment INSERT rolled back atomically with unchanged retained parent")
			}
			require.NoError(t, commentErr, "ordinary canonical-author comment must not rebind retained parent project roles")
			require.Contains(t, out, "Comment added to issue")
			require.Len(t, comments, 1)
			require.Equal(t, project, comments[0].AuthorProject)
			require.Equal(t, "ordinary retained issue comment", comments[0].Body)
			require.Equal(t, parent.SourceProject, actual.SourceProject)
			require.Equal(t, parent.TargetProject, actual.TargetProject)
			require.Equal(t, parent.SourceAgent, actual.SourceAgent)
			require.Equal(t, parent.CreatedBySession, actual.CreatedBySession)
			require.Equal(t, parent.CreatedAt, actual.CreatedAt)
			require.True(t, actual.UpdatedAt.After(parent.UpdatedAt))
			readback, err := callIssueAction(t, server, context.Background(), map[string]any{"action": "get", "id": parent.ID})
			require.NoError(t, err)
			require.Contains(t, readback, "ordinary retained issue comment")
			var historyAfter string
			require.NoError(t, db.Raw(`SELECT (to_jsonb(i)-'updated_at')::text FROM issues i WHERE id = ?`, parent.ID).Scan(&historyAfter).Error)
			require.JSONEq(t, historyBefore, historyAfter, "ordinary comment may change only the parent timestamp")
			for _, role := range []string{"source_project", "target_project"} {
				beforeAssignment := rowJSON(parent.ID)
				err := db.Model(&gormdb.Issue{}).Where("id = ?", parent.ID).Update(role, gormlib.Expr(role)).Error
				require.ErrorContains(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", "explicit no-op project-role assignment must still be validated")
				require.JSONEq(t, beforeAssignment, rowJSON(parent.ID))
			}
			ctx := issueClientContext(project, "collaborator-keycard", auth.RoleReadWrite)
			request := &Request{
				JSONRPC: "2.0", ID: fmt.Sprintf("public-comment-%d", parent.ID), Method: "tools/call",
				Params: mustJSON(t, map[string]any{"name": "issues", "arguments": map[string]any{
					"action": "comment", "id": parent.ID, "body": "public-entry retained issue comment", "project": project,
				}}),
			}
			for _, denied := range []context.Context{context.Background(), issueClientContext(project, "read-only-keycard", auth.RoleReadOnly)} {
				response := server.HandleRequest(denied, request)
				require.NotNil(t, response.Error, "public entry must retain caller authorization")
			}
			response := server.HandleRequest(ctx, request)
			require.Nil(t, response.Error)
			require.Equal(t, request.ID, response.ID)
			t.Logf("PUBLIC_ENTRY request=%s response=%s", mustJSON(t, request), mustJSON(t, response))
			getRequest := &Request{
				JSONRPC: "2.0", ID: fmt.Sprintf("public-get-%d", parent.ID), Method: "tools/call",
				Params: mustJSON(t, map[string]any{"name": "issues", "arguments": map[string]any{"action": "get", "id": parent.ID}}),
			}
			getResponse := server.HandleRequest(ctx, getRequest)
			require.Nil(t, getResponse.Error)
			require.Contains(t, string(mustJSON(t, getResponse.Result)), "public-entry retained issue comment")
			t.Logf("PUBLIC_ENTRY request=%s response=%s", mustJSON(t, getRequest), mustJSON(t, getResponse))
			_, allComments, err := issueStore.GetIssue(context.Background(), parent.ID)
			require.NoError(t, err)
			require.Len(t, allComments, 2, "denied callers must not append or duplicate comments")
			require.Equal(t, project, allComments[1].AuthorProject)
			require.NoError(t, db.Raw(`SELECT (to_jsonb(i)-'updated_at')::text FROM issues i WHERE id = ?`, parent.ID).Scan(&historyAfter).Error)
			require.JSONEq(t, historyBefore, historyAfter)
		})
	}
}
