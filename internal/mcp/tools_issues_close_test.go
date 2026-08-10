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

func TestIssueMutationsUseCreatorKeycardNotClaimedProject(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	project := uniqueIssueToolProject(t, "claimed")
	owner := issueClientContext(project, "keycard-owner", auth.RoleReadWrite)
	foreign := issueClientContext(project, "keycard-second", auth.RoleReadWrite)

	for _, tc := range []struct {
		name   string
		status string
		args   func(int64) map[string]any
	}{
		{"update", "open", func(id int64) map[string]any {
			return map[string]any{"action": "update", "id": id, "status": "resolved", "project": project}
		}},
		{"comment", "open", func(id int64) map[string]any {
			return map[string]any{"action": "comment", "id": id, "body": "foreign", "project": project}
		}},
		{"reopen", "resolved", func(id int64) map[string]any { return map[string]any{"action": "reopen", "id": id, "project": project} }},
		{"close", "resolved", func(id int64) map[string]any { return map[string]any{"action": "close", "id": id, "project": project} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := createIssueToolFixture(t, store, issueStore, project, "keycard-owner", tc.status)
			_, err := callIssueAction(t, server, foreign, tc.args(id))
			require.ErrorContains(t, err, "issue mutation forbidden")
		})
	}

	id := createIssueToolFixture(t, store, issueStore, project, "keycard-owner", "open")
	_, err := callIssueAction(t, server, owner, map[string]any{"action": "update", "id": id, "status": "resolved", "project": project})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, owner, map[string]any{"action": "comment", "id": id, "body": "owner", "project": project})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, owner, map[string]any{"action": "reopen", "id": id, "project": project})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, owner, map[string]any{"action": "update", "id": id, "status": "resolved", "project": project})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, owner, map[string]any{"action": "close", "id": id, "project": project})
	require.NoError(t, err)
}

func TestLegacyIssueDeniesClientButAllowsNonClientAdmin(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	id := createIssueToolFixture(t, store, issueStore, "claimed-project", "", "open")
	args := map[string]any{"action": "close", "id": id, "project": "claimed-project"}

	_, err := callIssueAction(t, server, issueClientContext("claimed-project", "keycard-client-admin", auth.RoleAdmin), args)
	require.ErrorContains(t, err, "issue mutation forbidden")
	_, err = callIssueAction(t, server, auth.WithIdentity(context.Background(), auth.Admin()), args)
	require.NoError(t, err)
}
