package grpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	localgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
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

type fakeSessionStartMemoryPager struct {
	rows []*models.Memory
}

func (f fakeSessionStartMemoryPager) ListWithOffset(_ context.Context, _ string, limit int, offset int) ([]*models.Memory, error) {
	if offset >= len(f.rows) {
		return nil, nil
	}
	end := offset + limit
	if end > len(f.rows) {
		end = len(f.rows)
	}
	return f.rows[offset:end], nil
}

func TestListVisibleSessionStartMemories_BackfillsBeforeScoring(t *testing.T) {
	t.Parallel()

	pager := fakeSessionStartMemoryPager{rows: []*models.Memory{
		{ID: 1, Project: "project", Content: "private bob newest", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityPrivate},
		{ID: 2, Project: "project", Content: "private bob second", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityPrivate},
		{ID: 3, Project: "project", Content: "shared visible", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityShared},
		{ID: 4, Project: "project", Content: "alice private visible", OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityPrivate},
	}}
	caller := scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"}

	visible, err := listVisibleSessionStartMemories(context.Background(), pager, "project", 2, caller, scope.MemoryVisibilityOptions{}, 10)
	require.NoError(t, err)
	require.Len(t, visible, 2)
	assert.Equal(t, int64(3), visible[0].ID)
	assert.Equal(t, int64(4), visible[1].ID)
}

func TestListVisibleSessionStartMemories_DomainOwnedCrossPrincipalInvisible(t *testing.T) {
	t.Parallel()

	pager := fakeSessionStartMemoryPager{rows: []*models.Memory{
		{ID: 11, Project: "project", Content: "bob domain newest", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityShared, Domain: "memory-lab"},
		{ID: 12, Project: "project", Content: "bob domain second", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityShared, Domain: "memory-lab"},
		{ID: 13, Project: "project", Content: "alice domain visible", OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityShared, Domain: "memory-lab"},
	}}
	caller := scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"}

	visible, err := listVisibleSessionStartMemories(context.Background(), pager, "project", 1, caller, scope.MemoryVisibilityOptions{}, 10)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	assert.Equal(t, int64(13), visible[0].ID)
}

func TestListVisibleSessionStartMemories_ReadBudgetUnderfills(t *testing.T) {
	t.Parallel()

	pager := fakeSessionStartMemoryPager{rows: []*models.Memory{
		{ID: 1, Project: "project", Content: "private bob newest", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityPrivate},
		{ID: 2, Project: "project", Content: "private bob second", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityPrivate},
		{ID: 3, Project: "project", Content: "shared visible", AgentVisibility: models.AgentVisibilityShared},
	}}
	caller := scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"}

	visible, err := listVisibleSessionStartMemories(context.Background(), pager, "project", 1, caller, scope.MemoryVisibilityOptions{}, 2)
	require.NoError(t, err)
	require.Empty(t, visible, "explicit read budget must stop the scan instead of walking the whole project")
}

func TestBuildSessionStartMetaSummary_ScanBudgetCapsLandscape(t *testing.T) {
	t.Parallel()

	oldest := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	middle := oldest.Add(time.Minute)
	newest := middle.Add(time.Minute)
	pager := fakeSessionStartMemoryPager{rows: []*models.Memory{
		{ID: 1, Project: "project", Content: "visible newest", Tags: []string{"alpha"}, AgentVisibility: models.AgentVisibilityShared, CreatedAt: newest},
		{ID: 2, Project: "project", Content: "visible middle", Tags: []string{"alpha", "beta"}, AgentVisibility: models.AgentVisibilityShared, CreatedAt: middle},
		{ID: 3, Project: "project", Content: "visible oldest", Tags: []string{"gamma"}, AgentVisibility: models.AgentVisibilityShared, CreatedAt: oldest},
	}}
	caller := scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"}

	summary, err := buildSessionStartMetaSummary(context.Background(), pager, "project", caller, scope.MemoryVisibilityOptions{}, newest, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), requireMetaInt(t, summary.ProtoReflect(), "total_count"), "scan budget must cap the landscape summary instead of walking the entire project")
	assert.Equal(t, []metaTagCount{{Tag: "alpha", Count: 2}, {Tag: "beta", Count: 1}}, requireMetaTopTags(t, summary.ProtoReflect()))
	assert.Equal(t, middle, requireMetaTimestamp(t, summary.ProtoReflect(), "oldest_created_at"))
	assert.Equal(t, newest, requireMetaTimestamp(t, summary.ProtoReflect(), "newest_created_at"))
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
	disabledRule, err := ruleStore.Create(ctx, &models.BehavioralRule{
		Project:  &project,
		Content:  "disabled project rule content",
		Priority: 999,
		EditedBy: project,
	})
	require.NoError(t, err)
	_, err = ruleStore.SetEnabled(ctx, disabledRule.ID, false, &project)
	require.NoError(t, err)
	otherProjectRule, err := ruleStore.Create(ctx, &models.BehavioralRule{
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
	olderCreatedAt := time.Now().UTC().Add(-2 * time.Second)
	require.NoError(t, db.Exec(`UPDATE memories SET created_at = ?, updated_at = ? WHERE id = ?`, olderCreatedAt, olderCreatedAt, olderMemory.ID).Error)
	olderMemory.CreatedAt = olderCreatedAt
	olderMemory.UpdatedAt = olderCreatedAt
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
	highCreatedAt := time.Now().UTC().Add(-2 * time.Second)
	require.NoError(t, db.Exec(`UPDATE issues SET created_at = ?, updated_at = ? WHERE id = ?`, highCreatedAt, highCreatedAt, highIssueID).Error)
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
	require.Nil(t, resp.RuleRouter, "router metadata must stay absent while ENGRAM_RULE_ROUTER_ENABLED is off")

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

	rulesByID := make(map[int64]*pb.SessionStartRule, len(resp.Rules))
	for _, rule := range resp.Rules {
		rulesByID[rule.Id] = rule
	}
	require.Contains(t, rulesByID, globalRule.ID)
	assert.Equal(t, "", rulesByID[globalRule.ID].Project)
	require.Contains(t, rulesByID, projectRule.ID)
	assert.Equal(t, project, rulesByID[projectRule.ID].Project)
	require.NotContains(t, rulesByID, disabledRule.ID, "disabled behavioral rules must not render into session-start rules")
	require.NotContains(t, rulesByID, otherProjectRule.ID, "sibling project rules must not render into session-start rules")
}

func TestGetSessionStartContext_PrincipalPrivateCrossPrincipalInvisible_FlagOff(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	project := fmt.Sprintf("grpc-session-start-principal-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)
	t.Setenv("ENGRAM_VNEXT_ENABLED", "")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	memoryStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})
	_, err := memoryStore.Create(context.Background(), &models.Memory{
		Project:            project,
		Content:            "private bob startup memory",
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityPrivate,
	})
	require.NoError(t, err)
	_, err = memoryStore.Create(context.Background(), &models.Memory{
		Project:            project,
		Content:            "shared bob startup memory",
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
	})
	require.NoError(t, err)

	srv := &Server{db: db}
	caller := auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent)
	resp, err := srv.GetSessionStartContext(
		auth.WithIdentity(context.Background(), caller),
		&pb.GetSessionStartContextRequest{Project: project, MemoriesLimit: 10},
	)
	require.NoError(t, err)

	require.Len(t, resp.GetMemories(), 1)
	assert.Equal(t, "shared bob startup memory", resp.GetMemories()[0].GetContent())
}

func TestGetSessionStartContext_MetaSummaryFlagOnDescribesMemoryLandscape(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	project := fmt.Sprintf("grpc-session-start-meta-summary-%d", time.Now().UnixNano())
	otherProject := project + "-other"
	defer db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, project, otherProject)
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true")

	ctx := context.Background()
	memoryStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})
	oldest := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	newest := oldest.Add(7 * time.Hour)
	fixtures := []struct {
		content   string
		tags      []string
		createdAt time.Time
	}{
		{"raw alpha beta gamma delta epsilon zeta eta body must not leak", []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta"}, oldest},
		{"raw alpha beta gamma delta epsilon zeta body must not leak", []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}, oldest.Add(1 * time.Hour)},
		{"raw alpha beta gamma delta epsilon body must not leak", []string{"alpha", "beta", "gamma", "delta", "epsilon"}, oldest.Add(2 * time.Hour)},
		{"raw alpha beta gamma delta body must not leak", []string{"alpha", "beta", "gamma", "delta"}, oldest.Add(3 * time.Hour)},
		{"raw alpha beta gamma body must not leak", []string{"alpha", "beta", "gamma"}, oldest.Add(4 * time.Hour)},
		{"raw alpha beta body must not leak", []string{"alpha", "beta"}, oldest.Add(5 * time.Hour)},
		{"raw alpha body must not leak", []string{"alpha"}, oldest.Add(6 * time.Hour)},
		{"raw theta newest body must not leak", []string{"theta"}, newest},
	}
	for _, fixture := range fixtures {
		insertSessionStartMetaSummaryMemory(t, db, memoryStore, project, fixture.content, fixture.tags, fixture.createdAt)
	}
	insertSessionStartMetaSummaryMemory(t, db, memoryStore, otherProject, "other project raw body must not leak", []string{"other"}, newest.Add(24*time.Hour))

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(ctx, &pb.GetSessionStartContextRequest{Project: project, MemoriesLimit: 1})
	require.NoError(t, err)
	require.Len(t, resp.GetMemories(), 1, "memory list remains independently bounded while meta_summary describes the wider project landscape")

	summary := requireSessionStartMetaSummary(t, resp)
	assert.Equal(t, int64(len(fixtures)), requireMetaInt(t, summary, "total_count"), "total_count must derive from all visible project memories, not the bounded memories response")
	assert.Equal(t, []metaTagCount{
		{Tag: "alpha", Count: 7},
		{Tag: "beta", Count: 6},
		{Tag: "gamma", Count: 5},
		{Tag: "delta", Count: 4},
		{Tag: "epsilon", Count: 3},
		{Tag: "zeta", Count: 2},
	}, requireMetaTopTags(t, summary), "top_tags must be the top six project tag counts and must exclude lower-ranked tags")
	assert.Equal(t, oldest, requireMetaTimestamp(t, summary, "oldest_created_at"))
	assert.Equal(t, newest, requireMetaTimestamp(t, summary, "newest_created_at"))
	assertMetaSummaryContentFree(t, summary, "raw alpha", "raw theta", "other project raw body", "must not leak")
}

func TestGetSessionStartContext_MetaSummaryCountsBeyondResponseCap(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	project := fmt.Sprintf("grpc-session-start-meta-summary-capped-%d", time.Now().UnixNano())
	otherProject := project + "-other"
	defer db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, project, otherProject)
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true")

	ctx := context.Background()
	memoryStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})
	oldest := time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC)
	newest := oldest.Add(204 * time.Minute)
	for i := range 205 {
		tags := []string{"alpha"}
		if i < 3 {
			tags = append(tags, "beta")
		}
		insertSessionStartMetaSummaryMemory(t, db, memoryStore, project, fmt.Sprintf("visible summary row %03d body must not leak", i), tags, oldest.Add(time.Duration(i)*time.Minute))
	}
	insertSessionStartMetaSummaryMemory(t, db, memoryStore, otherProject, "other project body must not leak", []string{"other"}, newest.Add(time.Hour))

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(ctx, &pb.GetSessionStartContextRequest{Project: project, MemoriesLimit: 1})
	require.NoError(t, err)
	require.Len(t, resp.GetMemories(), 1, "response memories remain bounded while meta_summary scans the full visible project set")

	summary := requireSessionStartMetaSummary(t, resp)
	assert.Equal(t, int64(205), requireMetaInt(t, summary, "total_count"), "total_count must stay truthful beyond the 200-memory response cap")
	assert.Equal(t, []metaTagCount{{Tag: "alpha", Count: 205}, {Tag: "beta", Count: 3}}, requireMetaTopTags(t, summary))
	assert.Equal(t, oldest, requireMetaTimestamp(t, summary, "oldest_created_at"))
	assert.Equal(t, newest, requireMetaTimestamp(t, summary, "newest_created_at"))
	assertMetaSummaryContentFree(t, summary, "visible summary row", "other project body", "must not leak")
}

func TestGetSessionStartContext_MetaSummaryFlagOffOmitted(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	project := fmt.Sprintf("grpc-session-start-meta-summary-off-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "")

	memoryStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})
	insertSessionStartMetaSummaryMemory(t, db, memoryStore, project, "baseline memory remains visible", []string{"baseline"}, time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC))

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{Project: project, MemoriesLimit: 10})
	require.NoError(t, err)
	require.Len(t, resp.GetMemories(), 1)
	assert.Equal(t, "baseline memory remains visible", resp.GetMemories()[0].GetContent())
	assertSessionStartMetaSummaryAbsent(t, resp)
}

func TestGetSessionStartContext_MetaSummaryFlagOnEmptyProjectIsBoundedAndContentFree(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	project := fmt.Sprintf("grpc-session-start-meta-summary-empty-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true")

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{Project: project, MemoriesLimit: 10})
	require.NoError(t, err)
	require.Empty(t, resp.GetMemories())

	summary := requireSessionStartMetaSummary(t, resp)
	assert.Equal(t, int64(0), requireMetaInt(t, summary, "total_count"))
	assert.Empty(t, requireMetaTopTags(t, summary))
	assertMetaTimestampAbsent(t, summary, "oldest_created_at")
	assertMetaTimestampAbsent(t, summary, "newest_created_at")
	assertMetaSummaryContentFree(t, summary, "content", "snippet")
}

func TestGetSessionStartContext_T014_MetaSummaryRequiresMasterAndS2Flags(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	projectPrefix := fmt.Sprintf("grpc-session-start-t014-toggle-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM memories WHERE project LIKE ?`, projectPrefix+"%")
	memoryStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})

	tests := []struct {
		name        string
		masterFlag  string
		s2Flag      string
		wantSummary bool
	}{
		{name: "master and s2 enabled includes meta_summary", masterFlag: "true", s2Flag: "true", wantSummary: true},
		{name: "master disabled omits meta_summary even when s2 flag is set", masterFlag: "false", s2Flag: "true", wantSummary: false},
		{name: "s2 disabled omits meta_summary even when master is set", masterFlag: "true", s2Flag: "false", wantSummary: false},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_V7_PLUG_ENABLED", tt.masterFlag)
			t.Setenv("ENGRAM_V7_S2_METAMEM", tt.s2Flag)

			project := fmt.Sprintf("%s-%d", projectPrefix, i)
			content := fmt.Sprintf("baseline memory remains visible for T014 case %d", i)
			insertSessionStartMetaSummaryMemory(t, db, memoryStore, project, content, []string{"t014", "s2-toggle"}, time.Date(2026, 7, 4, 8+i, 0, 0, 0, time.UTC))

			srv := &Server{db: db}
			resp, err := srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{Project: project, MemoriesLimit: 10})
			require.NoError(t, err)
			require.Len(t, resp.GetMemories(), 1, "S2 flag toggling must not hide baseline session-start memories")
			assert.Equal(t, content, resp.GetMemories()[0].GetContent())

			if tt.wantSummary {
				summary := requireSessionStartMetaSummary(t, resp)
				assert.Equal(t, int64(1), requireMetaInt(t, summary, "total_count"), "enabled meta_summary must derive from visible project memories")
				assertMetaSummaryContentFree(t, summary, content)
			} else {
				assertSessionStartMetaSummaryAbsent(t, resp)
			}
		})
	}
}

type metaTagCount struct {
	Tag   string
	Count int64
}

func insertSessionStartMetaSummaryMemory(t *testing.T, db *gormlib.DB, store *localgorm.MemoryStore, project string, content string, tags []string, createdAt time.Time) *models.Memory {
	t.Helper()

	mem, err := store.Create(context.Background(), &models.Memory{
		Project:     project,
		Content:     content,
		Tags:        tags,
		SourceAgent: "test-agent",
		EditedBy:    project,
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE memories SET created_at = ?, updated_at = ? WHERE id = ?`, createdAt, createdAt, mem.ID).Error)
	mem.CreatedAt = createdAt
	mem.UpdatedAt = createdAt
	return mem
}

func requireSessionStartMetaSummary(t *testing.T, resp *pb.GetSessionStartContextResponse) protoreflect.Message {
	t.Helper()
	require.NotNil(t, resp)
	field := resp.ProtoReflect().Descriptor().Fields().ByName("meta_summary")
	require.NotNil(t, field, "GetSessionStartContextResponse must declare meta_summary for S2 startup landscape")
	require.True(t, resp.ProtoReflect().Has(field), "S2-enabled session-start response must include a derived meta_summary")
	summary := resp.ProtoReflect().Get(field).Message()
	require.True(t, summary.IsValid(), "S2-enabled session-start meta_summary must not be nil or empty")
	return summary
}

func assertSessionStartMetaSummaryAbsent(t *testing.T, resp *pb.GetSessionStartContextResponse) {
	t.Helper()
	require.NotNil(t, resp)
	field := resp.ProtoReflect().Descriptor().Fields().ByName("meta_summary")
	if field == nil {
		return
	}
	assert.False(t, resp.ProtoReflect().Has(field), "flag-off session-start response must remain baseline-compatible by omitting meta_summary")
}

func requireMetaInt(t *testing.T, msg protoreflect.Message, name protoreflect.Name) int64 {
	t.Helper()
	field := msg.Descriptor().Fields().ByName(name)
	require.NotNil(t, field, "meta_summary must declare %s", name)
	require.Contains(t, []protoreflect.Kind{protoreflect.Int32Kind, protoreflect.Int64Kind}, field.Kind(), "meta_summary.%s must be an integer", name)
	return msg.Get(field).Int()
}

func requireMetaTopTags(t *testing.T, msg protoreflect.Message) []metaTagCount {
	t.Helper()
	field := msg.Descriptor().Fields().ByName("top_tags")
	require.NotNil(t, field, "meta_summary must declare top_tags")
	require.True(t, field.IsList(), "meta_summary.top_tags must be a repeated field")
	list := msg.Get(field).List()
	out := make([]metaTagCount, 0, list.Len())
	for i := range list.Len() {
		item := list.Get(i).Message()
		tagField := item.Descriptor().Fields().ByName("tag")
		countField := item.Descriptor().Fields().ByName("count")
		require.NotNil(t, tagField, "meta_summary.top_tags entries must declare tag")
		require.NotNil(t, countField, "meta_summary.top_tags entries must declare count")
		out = append(out, metaTagCount{Tag: item.Get(tagField).String(), Count: item.Get(countField).Int()})
	}
	return out
}

func requireMetaTimestamp(t *testing.T, msg protoreflect.Message, name protoreflect.Name) time.Time {
	t.Helper()
	field := msg.Descriptor().Fields().ByName(name)
	require.NotNil(t, field, "meta_summary must declare %s", name)
	require.True(t, msg.Has(field), "meta_summary.%s must be set when memories exist", name)
	return metaTimestampValue(t, msg.Get(field).Message(), name)
}

func assertMetaTimestampAbsent(t *testing.T, msg protoreflect.Message, name protoreflect.Name) {
	t.Helper()
	field := msg.Descriptor().Fields().ByName(name)
	require.NotNil(t, field, "meta_summary must declare %s", name)
	assert.False(t, msg.Has(field), "meta_summary.%s must be absent for an empty project", name)
}

func metaTimestampValue(t *testing.T, msg protoreflect.Message, name protoreflect.Name) time.Time {
	t.Helper()
	require.True(t, msg.IsValid(), "meta_summary.%s must be a valid timestamp", name)
	secondsField := msg.Descriptor().Fields().ByName("seconds")
	nanosField := msg.Descriptor().Fields().ByName("nanos")
	require.NotNil(t, secondsField, "meta_summary.%s must be a google.protobuf.Timestamp", name)
	require.NotNil(t, nanosField, "meta_summary.%s must be a google.protobuf.Timestamp", name)
	return time.Unix(msg.Get(secondsField).Int(), msg.Get(nanosField).Int()).UTC()
}

func assertMetaSummaryContentFree(t *testing.T, msg protoreflect.Message, forbidden ...string) {
	t.Helper()
	raw, err := protojson.Marshal(msg.Interface())
	require.NoError(t, err)
	payload := string(raw)
	assert.NotContains(t, payload, "content", "meta_summary must stay content-free")
	assert.NotContains(t, payload, "snippet", "meta_summary must not expose raw snippets")
	for _, value := range forbidden {
		assert.NotContains(t, payload, value)
	}
}

func TestGetSessionStartContext_RuleRouterEnabledPacketShape(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	t.Setenv("ENGRAM_RULE_ROUTER_ENABLED", "true")
	t.Setenv("ENGRAM_RULE_ROUTER_KERNEL_MAX", "1")
	t.Setenv("ENGRAM_RULE_ROUTER_CONTEXTUAL_MAX", "2")
	t.Setenv("ENGRAM_RULE_ROUTER_MAX_RENDERED_CHARS", "12000")

	ctx := context.Background()
	project := fmt.Sprintf("grpc-session-start-router-%d", time.Now().UnixNano())
	otherProject := project + "-other"
	defer db.Exec(`DELETE FROM behavioral_rules WHERE edited_by = ?`, project)
	defer db.Exec(`DELETE FROM rule_versions WHERE content LIKE ?`, "RG-2 session-start router fixture "+project+"%")
	defer db.Exec(`DELETE FROM rule_families WHERE family_key LIKE ?`, "rg2-session-start-"+project+"%")

	kernelID := insertSessionStartRuleVersion(t, db, project+"-kernel", models.RuleStateKernel, "developer", 1000003, map[string]any{})
	activeProjectID := insertSessionStartRuleVersion(t, db, project+"-active", models.RuleStateActiveProject, "developer", 1000002, map[string]any{"project": project})
	suppressedID := insertSessionStartRuleVersion(t, db, project+"-other", models.RuleStateActiveProject, "developer", 1000001, map[string]any{"project": otherProject})

	ruleStore := localgorm.NewBehavioralRulesStore(&localgorm.Store{DB: db})
	legacyRule, err := ruleStore.Create(ctx, &models.BehavioralRule{
		Project:  &project,
		Content:  "RG-2 session-start legacy fallback " + project,
		Priority: 1000000,
		EditedBy: project,
	})
	require.NoError(t, err)
	disabledLegacyRule, err := ruleStore.Create(ctx, &models.BehavioralRule{
		Project:  &project,
		Content:  "RG-2 session-start disabled legacy fallback " + project,
		Priority: 1000004,
		EditedBy: project,
	})
	require.NoError(t, err)
	_, err = ruleStore.SetEnabled(ctx, disabledLegacyRule.ID, false, &project)
	require.NoError(t, err)

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(ctx, &pb.GetSessionStartContextRequest{Project: project})
	require.NoError(t, err)
	require.NotNil(t, resp.RuleRouter)
	require.True(t, resp.RuleRouter.Enabled)
	require.Equal(t, "router", resp.RuleRouter.Mode)
	require.Equal(t, int32(1), resp.RuleRouter.KernelCount)
	require.Equal(t, int32(2), resp.RuleRouter.ContextualCount)
	require.Equal(t, int32(len(resp.RuleRouter.Suppressed)), resp.RuleRouter.SuppressedCount)
	suppressedByVersion := map[int64]*pb.SessionStartRulePacket{}
	for _, packet := range resp.RuleRouter.Suppressed {
		suppressedByVersion[packet.RuleVersionId] = packet
	}
	require.Contains(t, suppressedByVersion, suppressedID)
	deferredByBudget := false
	for _, packet := range resp.RuleRouter.Suppressed {
		if packet.SuppressionReason == "deferred_budget" {
			deferredByBudget = true
			break
		}
	}
	if deferredByBudget {
		require.Equal(t, "truncated", resp.RuleRouter.BudgetOutcome)
	} else {
		require.Equal(t, "within_budget", resp.RuleRouter.BudgetOutcome)
	}

	require.Len(t, resp.RuleRouter.Kernel, 1)
	require.Equal(t, kernelID, resp.RuleRouter.Kernel[0].RuleVersionId)
	require.Equal(t, "kernel", resp.RuleRouter.Kernel[0].Bucket)

	contextualByVersion := map[int64]*pb.SessionStartRulePacket{}
	contextualByLegacy := map[int64]*pb.SessionStartRulePacket{}
	for _, packet := range resp.RuleRouter.Contextual {
		contextualByVersion[packet.RuleVersionId] = packet
		contextualByLegacy[packet.LegacyBehavioralRuleId] = packet
	}
	require.Contains(t, contextualByVersion, activeProjectID)
	require.Contains(t, contextualByLegacy, legacyRule.ID)
	require.NotContains(t, contextualByLegacy, disabledLegacyRule.ID)
	require.Equal(t, "contextual", contextualByVersion[activeProjectID].Bucket)
	require.Equal(t, "legacy", contextualByLegacy[legacyRule.ID].BudgetClass)

	require.Len(t, resp.Rules, 3, "legacy compatibility rules should include kernel plus contextual packets")
	ruleContents := map[string]bool{}
	for _, rule := range resp.Rules {
		ruleContents[rule.Content] = true
	}
	require.True(t, ruleContents["RG-2 session-start router fixture "+project+"-kernel"])
	require.True(t, ruleContents["RG-2 session-start router fixture "+project+"-active"])
	require.True(t, ruleContents[legacyRule.Content])
	require.False(t, ruleContents[disabledLegacyRule.Content])
}

func TestRuleRouterLegacyFallbackShape(t *testing.T) {
	cfg := configForSessionStartRouterTest()
	project := "router-fallback-project"
	legacyID := int64(42)
	result := selectSessionStartRouterPackets(nil, []*models.BehavioralRule{{
		ID:       legacyID,
		Project:  &project,
		Content:  "legacy fallback rule",
		Priority: 7,
	}}, project, cfg)
	router := mapSessionStartRuleRouter(result)
	router.FallbackReason = "rule_router_unavailable"

	require.Len(t, result.Contextual, 1)
	require.Equal(t, legacyID, *result.Contextual[0].LegacyBehavioralRuleID)
	require.Empty(t, result.Kernel, "legacy fallback rows must never become kernel")
	require.Equal(t, "rule_router_unavailable", router.FallbackReason)
	require.Len(t, router.Contextual, 1)
	require.Equal(t, legacyID, router.Contextual[0].LegacyBehavioralRuleId)
	require.Equal(t, "legacy", router.Contextual[0].BudgetClass)
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

func configForSessionStartRouterTest() *config.Config {
	cfg := config.Default()
	cfg.RuleRouterEnabled = true
	return cfg
}

func insertSessionStartRuleVersion(t *testing.T, db *gormlib.DB, suffix string, state models.RuleVersionState, audience string, priority int, predicate map[string]any) int64 {
	t.Helper()
	predicateJSON, err := json.Marshal(predicate)
	require.NoError(t, err)

	var familyID int64
	require.NoError(t, db.Raw(`INSERT INTO rule_families (family_key) VALUES (?) RETURNING id`,
		"rg2-session-start-"+suffix,
	).Scan(&familyID).Error)

	var versionID int64
	require.NoError(t, db.Raw(`INSERT INTO rule_versions (
		family_id,
		content,
		scope,
		owner,
		audience,
		activation_predicate_json,
		evidence_handles_json,
		state,
		budget_class,
		anti_capture_status,
		conflict_status,
		decay_policy,
		priority
	) VALUES (?, ?, ?, ?, ?, CAST(? AS jsonb), '[]'::jsonb, ?, ?, ?, ?, ?, ?) RETURNING id`,
		familyID,
		"RG-2 session-start router fixture "+suffix,
		"project",
		"codex",
		audience,
		string(predicateJSON),
		string(state),
		"contextual",
		"passed",
		"none",
		"NO DATA",
		priority,
	).Scan(&versionID).Error)
	return versionID
}
