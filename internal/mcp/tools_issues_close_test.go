package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
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

func forceIssueToolSourceProjectEnforcement(t *testing.T) {
	t.Helper()
	cfg := config.Get()
	previous := cfg.EnforceSourceProject
	cfg.EnforceSourceProject = true
	t.Cleanup(func() { cfg.EnforceSourceProject = previous })
}

func TestHandleIssueCloseAcceptsExplicitLegacySourceProject(t *testing.T) {
	forceIssueToolSourceProjectEnforcement(t)
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)

	ctx := context.Background()
	legacySource := uniqueIssueToolProject(t, "legacy-source")
	currentContextSource := uniqueIssueToolProject(t, "current-source")

	id, err := issueStore.CreateIssue(ctx, &gormdb.Issue{
		Title:         "legacy source close regression",
		SourceProject: legacySource,
		TargetProject: "zz-test-target",
		Status:        "resolved",
		Type:          "bug",
		Priority:      "medium",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		store.GetDB().Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
		store.GetDB().Exec(`DELETE FROM issues WHERE id = ?`, id)
	})

	args, err := json.Marshal(map[string]any{
		"action":  "close",
		"id":      id,
		"project": legacySource,
	})
	require.NoError(t, err)

	result, err := server.handleIssues(contextWithProject(ctx, currentContextSource), args)
	require.NoError(t, err)
	require.Contains(t, result, fmt.Sprintf("Issue #%d closed", id))

	issue, _, err := issueStore.GetIssue(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "closed", issue.Status)
}

func TestHandleIssueCloseDoesNotLetExplicitDashboardBypassContext(t *testing.T) {
	forceIssueToolSourceProjectEnforcement(t)
	store, issueStore := openIssueToolTestDB(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)

	ctx := context.Background()
	owner := uniqueIssueToolProject(t, "owner")
	foreignContext := uniqueIssueToolProject(t, "foreign")

	id, err := issueStore.CreateIssue(ctx, &gormdb.Issue{
		Title:         "dashboard spoof close regression",
		SourceProject: owner,
		TargetProject: "zz-test-target",
		Status:        "resolved",
		Type:          "bug",
		Priority:      "medium",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		store.GetDB().Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
		store.GetDB().Exec(`DELETE FROM issues WHERE id = ?`, id)
	})

	args, err := json.Marshal(map[string]any{
		"action":  "close",
		"id":      id,
		"project": "dashboard",
	})
	require.NoError(t, err)

	_, err = server.handleIssues(contextWithProject(ctx, foreignContext), args)
	require.Error(t, err)
	require.ErrorContains(t, err, "close rejected")

	issue, _, err := issueStore.GetIssue(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "resolved", issue.Status)
}
