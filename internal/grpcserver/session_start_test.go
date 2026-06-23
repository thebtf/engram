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

	require.Len(t, resp.Rules, 2)
	assert.Equal(t, globalRule.ID, resp.Rules[0].Id)
	assert.Equal(t, "", resp.Rules[0].Project)
	assert.Equal(t, projectRule.ID, resp.Rules[1].Id)
	assert.Equal(t, project, resp.Rules[1].Project)
	for _, rule := range resp.Rules {
		assert.NotEqual(t, disabledRule.ID, rule.Id, "disabled behavioral rules must not render into session-start rules")
	}
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
	_ = suppressedID

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
	require.Equal(t, int32(1), resp.RuleRouter.SuppressedCount)
	require.Equal(t, "within_budget", resp.RuleRouter.BudgetOutcome)

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
