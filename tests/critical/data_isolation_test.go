//go:build critical

package critical_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	localgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"gorm.io/gorm/logger"
)

// @critical
// @category: data-consistency
// @features: [session-start-isolation, document-isolation]
// @dev_stand: required
func TestSessionStartAndDocumentQueries_DoNotExposeSiblingProjectsOrPaths(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	lowerDSN := strings.ToLower(dsn)
	if dsn == "" || !strings.Contains(lowerDSN, "test") || strings.Contains(lowerDSN, "prod") || strings.Contains(lowerDSN, "staging") {
		t.Fatal("DATABASE_DSN must identify a dedicated non-production test database")
	}

	store, err := localgorm.NewStore(localgorm.Config{DSN: dsn, LogLevel: logger.Silent})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	db := store.GetDB()
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	bare := `literal_project%` + suffix + `\root`
	project := bare + "_a1b2c3"
	sibling := `literalXprojectZ` + suffix + `\root_a1b2c3`

	issueStore := localgorm.NewIssueStore(db)
	exactIssueID, err := issueStore.CreateIssue(ctx, &localgorm.Issue{Title: "exact-session-start", TargetProject: project, SourceProject: "critical", Type: "task"})
	require.NoError(t, err)
	siblingIssueID, err := issueStore.CreateIssue(ctx, &localgorm.Issue{Title: "sibling-must-not-leak", TargetProject: sibling, SourceProject: "critical", Type: "task"})
	require.NoError(t, err)

	memoryStore := localgorm.NewMemoryStore(store)
	for _, fixture := range []struct{ project, content string }{
		{project, "exact-session-memory"},
		{sibling, "sibling-memory-must-not-leak"},
	} {
		_, createErr := memoryStore.CreateWithLifecycle(ctx, &models.Memory{
			Project: fixture.project, Content: fixture.content, EpistemicType: "observation", Tier: "episodic", SourceAgent: "critical-isolation",
		})
		require.NoError(t, createErr)
	}

	docStore := localgorm.NewVersionedDocumentStore(store)
	paths := []string{
		`notes_1/exact.md`, `notesX1/sibling.md`,
		`notes%literal/exact.md`, `notesZliteral/sibling.md`,
		`notes\root/exact.md`, `notesXroot/sibling.md`,
	}
	for _, path := range paths {
		_, err = docStore.Create(ctx, path, project, path, "markdown", "{}", "critical-isolation")
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM versioned_documents WHERE project = ?", project).Error
		_ = db.Exec("DELETE FROM issues WHERE id IN ?", []int64{exactIssueID, siblingIssueID}).Error
		_ = db.Exec("DELETE FROM memories WHERE project IN ?", []string{project, sibling}).Error
	})

	_, sessionServer := grpcserver.New(stubMCPHandler{}, nil)
	sessionServer.SetDB(db)
	resp, err := sessionServer.GetSessionStartContext(ctx, &pb.GetSessionStartContextRequest{Project: project, MemoriesLimit: 20, IssuesLimit: 20})
	require.NoError(t, err)
	require.Len(t, resp.Issues, 1)
	require.Equal(t, "exact-session-start", resp.Issues[0].Title)
	require.Len(t, resp.Memories, 1)
	require.Equal(t, "exact-session-memory", resp.Memories[0].Content)

	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "critical"})
	mcpServer.SetVersionedDocumentStore(docStore)
	for _, tc := range []struct{ prefix, want string }{
		{`notes_1/`, `notes_1/exact.md`},
		{`notes%literal/`, `notes%literal/exact.md`},
		{`notes\root/`, `notes\root/exact.md`},
	} {
		args, marshalErr := json.Marshal(map[string]any{"action": "list", "project": project, "path_prefix": tc.prefix, "limit": 20})
		require.NoError(t, marshalErr)
		params, marshalErr := json.Marshal(map[string]any{"name": "docs", "arguments": json.RawMessage(args)})
		require.NoError(t, marshalErr)
		toolResp := mcpServer.HandleRequest(ctx, &mcp.Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
		require.Nil(t, toolResp.Error)
		result := toolResp.Result.(map[string]any)
		content := result["content"].([]map[string]any)
		var docs []struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.Unmarshal([]byte(content[0]["text"].(string)), &docs))
		require.Equal(t, []struct {
			Path string `json:"path"`
		}{{Path: tc.want}}, docs)
	}
}
