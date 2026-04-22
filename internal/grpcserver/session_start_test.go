package grpcserver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	localgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gormpostgres "gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openSessionStartTestDB(t *testing.T) (*gormlib.DB, func()) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping session-start gRPC integration test")
	}
	lowerDSN := strings.ToLower(dsn)
	if !strings.Contains(lowerDSN, "test") || strings.Contains(lowerDSN, "prod") || strings.Contains(lowerDSN, "production") || strings.Contains(lowerDSN, "staging") {
		t.Skip("DATABASE_DSN does not look like a dedicated test database")
	}

	store, err := localgorm.NewStore(localgorm.Config{
		DSN:      dsn,
		LogLevel: logger.Silent,
	})
	require.NoError(t, err, "NewStore (applies migrations)")

	db, err := gormlib.Open(gormpostgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "open postgres db handle")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())

	cleanup := func() {
		sqlDB.Close()
		store.Close()
	}
	return db, cleanup
}

func TestGetSessionStartContext_InvalidArgument(t *testing.T) {
	t.Parallel()

	srv := &Server{}
	_, err := srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{Project: "proj", MemoriesLimit: -1})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{Project: "proj", IssuesLimit: -1})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{Project: "proj", MemoriesLimit: maxSessionStartMemoriesLimit + 1})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{Project: "proj", IssuesLimit: maxSessionStartIssuesLimit + 1})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetSessionStartContext_HappyPath(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	ctx := context.Background()
	project := fmt.Sprintf("grpc-session-start-%d", time.Now().UnixNano())
	otherProject := project + "-other"

	defer db.Exec(`DELETE FROM issue_comments WHERE issue_id IN (SELECT id FROM issues WHERE target_project IN (?, ?))`, project, otherProject)
	defer db.Exec(`DELETE FROM issues WHERE target_project IN (?, ?)`, project, otherProject)
	defer db.Exec(`DELETE FROM behavioral_rules WHERE project = ? OR project = ? OR project IS NULL AND edited_by = ?`, project, otherProject, project)
	defer db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, project, otherProject)

	issueStore := localgorm.NewIssueStore(db)
	memoryStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})
	ruleStore := localgorm.NewBehavioralRulesStore(&localgorm.Store{DB: db})

	globalRule, err := ruleStore.Create(ctx, &models.BehavioralRule{
		Content:  "global rule content",
		Priority: 100,
		EditedBy: project,
	})
	require.NoError(t, err)
	projectRule, err := ruleStore.Create(ctx, &models.BehavioralRule{
		Project:  &project,
		Content:  "project rule content",
		Priority: 50,
		EditedBy: project,
	})
	require.NoError(t, err)
	_, err = ruleStore.Create(ctx, &models.BehavioralRule{
		Project:  &otherProject,
		Content:  "other project rule content",
		Priority: 999,
		EditedBy: project,
	})
	require.NoError(t, err)

	olderMemory, err := memoryStore.Create(ctx, &models.Memory{
		Project:     project,
		Content:     "older memory",
		Tags:        []string{"older"},
		SourceAgent: "test-agent",
		EditedBy:    project,
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	newerMemory, err := memoryStore.Create(ctx, &models.Memory{
		Project:     project,
		Content:     "newer memory",
		Tags:        []string{"newer"},
		SourceAgent: "test-agent",
		EditedBy:    project,
	})
	require.NoError(t, err)
	_, err = memoryStore.Create(ctx, &models.Memory{
		Project:     otherProject,
		Content:     "other memory",
		Tags:        []string{"other"},
		SourceAgent: "test-agent",
		EditedBy:    project,
	})
	require.NoError(t, err)

	highIssueID, err := issueStore.CreateIssue(ctx, &localgorm.Issue{
		Title:         "high issue",
		Body:          "body-high",
		Status:        "open",
		Priority:      "high",
		Type:          "bug",
		SourceProject: "source-a",
		TargetProject: project,
		SourceAgent:   "agent-a",
		Labels:        []string{"bug"},
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	criticalIssueID, err := issueStore.CreateIssue(ctx, &localgorm.Issue{
		Title:         "critical issue",
		Body:          "body-critical",
		Status:        "reopened",
		Priority:      "critical",
		Type:          "task",
		SourceProject: "source-b",
		TargetProject: project,
		SourceAgent:   "agent-b",
		Labels:        []string{"task"},
	})
	require.NoError(t, err)
	_, err = issueStore.CreateIssue(ctx, &localgorm.Issue{
		Title:         "resolved issue",
		Body:          "ignore me",
		Status:        "resolved",
		Priority:      "critical",
		Type:          "bug",
		SourceProject: "source-c",
		TargetProject: project,
		SourceAgent:   "agent-c",
	})
	require.NoError(t, err)
	_, err = issueStore.CreateIssue(ctx, &localgorm.Issue{
		Title:         "other project issue",
		Body:          "ignore me too",
		Status:        "open",
		Priority:      "critical",
		Type:          "bug",
		SourceProject: "source-d",
		TargetProject: otherProject,
		SourceAgent:   "agent-d",
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO issue_comments (issue_id, author_project, author_agent, body) VALUES (?, ?, ?, ?)`, criticalIssueID, project, "agent-b", "first comment").Error)

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(ctx, &pb.GetSessionStartContextRequest{
		Project:       project,
		MemoriesLimit: 1,
		IssuesLimit:   2,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GeneratedAt)

	require.Len(t, resp.Memories, 1)
	assert.Equal(t, newerMemory.ID, resp.Memories[0].Id)
	assert.Equal(t, "newer memory", resp.Memories[0].Content)
	assert.Equal(t, []string{"newer"}, resp.Memories[0].Tags)
	assert.NotEqual(t, olderMemory.ID, resp.Memories[0].Id)

	require.Len(t, resp.Issues, 2)
	assert.Equal(t, criticalIssueID, resp.Issues[0].Id)
	assert.Equal(t, "critical", resp.Issues[0].Priority)
	assert.Equal(t, int64(1), resp.Issues[0].CommentCount)
	assert.Equal(t, highIssueID, resp.Issues[1].Id)
	assert.Equal(t, "high", resp.Issues[1].Priority)

	require.Len(t, resp.Rules, 2)
	assert.Equal(t, globalRule.ID, resp.Rules[0].Id)
	assert.Equal(t, "", resp.Rules[0].Project)
	assert.Equal(t, projectRule.ID, resp.Rules[1].Id)
	assert.Equal(t, project, resp.Rules[1].Project)
}

func TestGetSessionStartContext_DefaultLimits(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	ctx := context.Background()
	project := fmt.Sprintf("grpc-session-start-defaults-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM issues WHERE target_project = ?`, project)
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)

	issueStore := localgorm.NewIssueStore(db)
	memoryStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})

	for i := 0; i < 3; i++ {
		_, err := memoryStore.Create(ctx, &models.Memory{
			Project:     project,
			Content:     fmt.Sprintf("memory-%d", i),
			Tags:        []string{"default"},
			SourceAgent: "test-agent",
			EditedBy:    project,
		})
		require.NoError(t, err)

		_, err = issueStore.CreateIssue(ctx, &localgorm.Issue{
			Title:         fmt.Sprintf("issue-%d", i),
			Status:        "open",
			Priority:      "medium",
			Type:          "task",
			SourceProject: "source",
			TargetProject: project,
			SourceAgent:   "agent",
		})
		require.NoError(t, err)
	}

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(ctx, &pb.GetSessionStartContextRequest{Project: project})
	require.NoError(t, err)
	assert.Len(t, resp.Memories, 3)
	assert.Len(t, resp.Issues, 3)
}
