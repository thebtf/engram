package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
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

func uniqueIssueToolProject(t *testing.T, base string) string {
	t.Helper()
	return fmt.Sprintf("zz-test-%s-%d", base, time.Now().UnixNano())
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
