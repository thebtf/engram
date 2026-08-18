package grpcserver

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/injection"
	"github.com/thebtf/engram/internal/ruleinjection"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

const (
	defaultSessionStartMemoriesLimit = 20
	defaultSessionStartIssuesLimit   = 20
	defaultSessionStartRulesLimit    = 20
	maxSessionStartMemoriesLimit     = 200
	maxSessionStartIssuesLimit       = 200
	maxSessionStartRulesLimit        = 200
	sessionStartMemoryBatchSize      = 500
	sessionStartVisibilityScanBudget = 5000
)

type sessionStartMemoryPager interface {
	ListWithOffset(ctx context.Context, project string, limit int, offset int) ([]*models.Memory, error)
}

// GetSessionStartContext returns static session-start entities for a project.
// The payload is SQL-backed only: active issues, behavioral rules, recent memories,
// plus the timestamp when the response was generated.
func (s *Server) GetSessionStartContext(ctx context.Context, req *pb.GetSessionStartContextRequest) (*pb.GetSessionStartContextResponse, error) {
	project := req.GetProject()
	if project == "" {
		return nil, status.Error(codes.InvalidArgument, "project must not be empty")
	}
	if req.GetMemoriesLimit() < 0 {
		return nil, status.Error(codes.InvalidArgument, "memories_limit must be >= 0")
	}
	if req.GetMemoriesLimit() > maxSessionStartMemoriesLimit {
		return nil, status.Errorf(codes.InvalidArgument, "memories_limit must be <= %d", maxSessionStartMemoriesLimit)
	}
	if req.GetIssuesLimit() < 0 {
		return nil, status.Error(codes.InvalidArgument, "issues_limit must be >= 0")
	}
	if req.GetIssuesLimit() > maxSessionStartIssuesLimit {
		return nil, status.Errorf(codes.InvalidArgument, "issues_limit must be <= %d", maxSessionStartIssuesLimit)
	}
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "database not ready")
	}

	memoriesLimit := int(req.GetMemoriesLimit())
	if memoriesLimit == 0 {
		memoriesLimit = defaultSessionStartMemoriesLimit
	}
	issuesLimit := int(req.GetIssuesLimit())
	if issuesLimit == 0 {
		issuesLimit = defaultSessionStartIssuesLimit
	}
	rulesLimit := defaultSessionStartRulesLimit

	issueStore := dbgorm.NewIssueStore(s.db)
	issueRows, _, err := issueStore.ListIssuesEx(ctx, dbgorm.IssueListParams{
		TargetProject: project,
		Statuses:      []string{"open", "acknowledged", "reopened"},
		Limit:         issuesLimit,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list session-start issues")
	}

	memoryStore := dbgorm.NewMemoryStore(&dbgorm.Store{DB: s.db})
	var memoryRows []*models.Memory
	metaSummaryEnabled := os.Getenv("ENGRAM_V7_PLUG_ENABLED") == "true" && os.Getenv("ENGRAM_V7_S2_METAMEM") == "true"

	// Build scope.KeycardContext once for both branches. ENGRAM_VNEXT_F_ENABLED
	// gates only legacy privacy_scope; principal-private rows are filtered
	// fail-safe before startup memory scoring or serialization.
	var callerCtx scope.KeycardContext
	if id, ok := auth.IdentityFrom(ctx); ok {
		callerCtx.WorkstationID = id.WorkstationID()
		callerCtx.Principal = id.Principal
		if _, principalKind, hasOwner := id.MemoryOwner(); hasOwner {
			callerCtx.PrincipalKind = principalKind
		} else {
			callerCtx.PrincipalKind = string(id.PrincipalKind)
		}
	}
	visibilityOpts := scope.MemoryVisibilityOptions{
		ApplyPrivacyScope: os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true",
	}

	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
		allMemories, listErr := listVisibleSessionStartMemories(ctx, memoryStore, project, maxSessionStartMemoriesLimit, callerCtx, visibilityOpts, sessionStartVisibilityScanBudget)
		if listErr != nil {
			return nil, status.Error(codes.Internal, "failed to list session-start memories")
		}
		candidateRows := allMemories
		if os.Getenv(config.EnvContinuitySlotEnabled) == "true" {
			slot, slotErr := dbgorm.NewContinuitySlotStore(s.db).Get(ctx, project)
			if slotErr != nil && !errors.Is(slotErr, gorm.ErrRecordNotFound) {
				return nil, status.Error(codes.Internal, "failed to get session-start continuity slot")
			}
			if slotErr == nil && slot.ExpiresAt.After(time.Now().UTC()) {
				slotRows, getErr := memoryStore.GetByIDs(ctx, project, []int64{slot.MemoryID})
				if getErr != nil {
					return nil, status.Error(codes.Internal, "failed to get session-start continuity memory")
				}
				if len(slotRows) == 1 && scope.ResolveMemory(callerCtx, slotRows[0], visibilityOpts) {
					memoryRows = append(memoryRows, slotRows[0])
					candidateRows = make([]*models.Memory, 0, len(allMemories))
					for _, memory := range allMemories {
						if memory.ID != slotRows[0].ID {
							candidateRows = append(candidateRows, memory)
						}
					}
				}
			}
		}
		if remaining := memoriesLimit - len(memoryRows); remaining > 0 {
			for _, scoredMemory := range injection.Score(candidateRows, remaining) {
				if !scoredMemory.Selected {
					break
				}
				memoryRows = append(memoryRows, scoredMemory.Memory)
			}
		}
	} else {
		var listErr error
		memoryRows, listErr = listVisibleSessionStartMemories(ctx, memoryStore, project, memoriesLimit, callerCtx, visibilityOpts, sessionStartVisibilityScanBudget)
		if listErr != nil {
			return nil, status.Error(codes.Internal, "failed to list session-start memories")
		}
		if metaSummaryEnabled {
			// Meta summary is built from a full visibility-aware scan after the main response rows are selected.
		}
	}

	var rules []*pb.SessionStartRule
	var ruleRouter *pb.SessionStartRuleRouter
	routerCfg := loadSessionStartRuleRouterConfig()
	if routerCfg.RuleRouterEnabled {
		var routerErr error
		rules, ruleRouter, routerErr = s.getRouterSessionStartRules(ctx, project, rulesLimit, routerCfg)
		if routerErr != nil {
			return nil, routerErr
		}
	} else {
		var ruleRows []dbgorm.BehavioralRule
		if err := s.db.WithContext(ctx).
			Where("deleted_at IS NULL").
			Where("enabled = ?", true).
			Where("project = ? OR project IS NULL", project).
			Order("priority DESC, created_at DESC").
			Limit(rulesLimit).
			Find(&ruleRows).Error; err != nil {
			return nil, status.Error(codes.Internal, "failed to list session-start rules")
		}
		rules = mapSessionStartRules(ruleRows)
	}

	generatedAt := time.Now().UTC()
	response := &pb.GetSessionStartContextResponse{
		Issues:      mapSessionStartIssues(issueRows),
		Rules:       rules,
		Memories:    mapSessionStartMemories(memoryRows),
		GeneratedAt: timestamppb.New(generatedAt),
		RuleRouter:  ruleRouter,
	}
	if metaSummaryEnabled {
		summary, summaryErr := buildSessionStartMetaSummary(ctx, memoryStore, project, callerCtx, visibilityOpts, generatedAt, sessionStartVisibilityScanBudget)
		if summaryErr != nil {
			return nil, status.Error(codes.Internal, "failed to summarize session-start memories")
		}
		response.MetaSummary = summary
	}
	return response, nil
}

func listVisibleSessionStartMemories(
	ctx context.Context,
	store sessionStartMemoryPager,
	project string,
	visibleLimit int,
	caller scope.KeycardContext,
	opts scope.MemoryVisibilityOptions,
	scanBudget int,
) ([]*models.Memory, error) {
	if visibleLimit <= 0 {
		return nil, nil
	}
	if scanBudget <= 0 {
		scanBudget = sessionStartVisibilityScanBudget
	}
	if scanBudget < visibleLimit {
		scanBudget = visibleLimit
	}

	visible := make([]*models.Memory, 0, visibleLimit)
	for offset := 0; len(visible) < visibleLimit && offset < scanBudget; {
		batchLimit := sessionStartMemoryBatchSize
		if remaining := scanBudget - offset; remaining < batchLimit {
			batchLimit = remaining
		}
		if batchLimit <= 0 {
			break
		}
		batch, err := store.ListWithOffset(ctx, project, batchLimit, offset)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, mem := range batch {
			if scope.ResolveMemory(caller, mem, opts) {
				visible = append(visible, mem)
				if len(visible) == visibleLimit {
					break
				}
			}
		}
		offset += len(batch)
		if len(batch) < batchLimit {
			break
		}
	}
	return visible, nil
}

func loadSessionStartRuleRouterConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return config.Default()
	}
	return cfg
}

func (s *Server) getRouterSessionStartRules(ctx context.Context, project string, legacyLimit int, cfg *config.Config) ([]*pb.SessionStartRule, *pb.SessionStartRuleRouter, error) {
	store := dbgorm.NewRuleGovernanceStore(s.db)
	versionLimit := cfg.RuleRouterKernelMax + cfg.RuleRouterContextualMax + 50
	if versionLimit < 50 {
		versionLimit = 50
	}
	if versionLimit > maxSessionStartRulesLimit {
		versionLimit = maxSessionStartRulesLimit
	}

	projectPtr := project
	versions, err := store.ListRenderableRuleVersions(ctx, dbgorm.RuleVersionRenderListParams{Limit: versionLimit})
	if err != nil {
		legacyRules, legacyErr := store.ListLegacyBehavioralRuleFallback(ctx, &projectPtr, legacyLimit)
		if legacyErr != nil {
			return nil, nil, status.Error(codes.Internal, "failed to list session-start rules")
		}
		result := selectSessionStartRouterPackets(nil, legacyRules, project, cfg)
		router := mapSessionStartRuleRouter(result)
		router.FallbackReason = "rule_router_unavailable"
		return mapRouterSelectionRules(result, project), router, nil
	}
	legacyRules, err := store.ListLegacyBehavioralRuleFallback(ctx, &projectPtr, legacyLimit)
	fallbackReason := ""
	if err != nil {
		legacyRules = nil
		fallbackReason = "legacy_rule_fallback_unavailable"
	}

	result := selectSessionStartRouterPackets(versions, legacyRules, project, cfg)
	router := mapSessionStartRuleRouter(result)
	router.FallbackReason = fallbackReason
	return mapRouterSelectionRules(result, project), router, nil
}

func selectSessionStartRouterPackets(versions []*models.RuleVersion, legacyRules []*models.BehavioralRule, project string, cfg *config.Config) ruleinjection.SelectionResult {
	packets := make([]ruleinjection.RulePacket, 0, len(versions)+len(legacyRules))
	for _, version := range versions {
		packets = append(packets, ruleinjection.PacketFromRuleVersion(version))
	}
	for _, rule := range legacyRules {
		packets = append(packets, ruleinjection.PacketFromBehavioralRule(rule))
	}

	result := ruleinjection.SelectPackets(packets, ruleinjection.Metadata{
		Project:  project,
		Scope:    "project",
		Audience: "developer",
		Surface:  "session-start",
	}, ruleinjection.Caps{
		MaxKernel:        cfg.RuleRouterKernelMax,
		MaxContextual:    cfg.RuleRouterContextualMax,
		MaxRenderedChars: cfg.RuleRouterMaxRenderedChars,
	})
	return result
}

func mapRouterSelectionRules(result ruleinjection.SelectionResult, project string) []*pb.SessionStartRule {
	all := append([]ruleinjection.RulePacket{}, result.Kernel...)
	all = append(all, result.Contextual...)
	rules := make([]*pb.SessionStartRule, 0, len(all))
	for _, packet := range all {
		id := packet.ID
		if packet.LegacyBehavioralRuleID != nil {
			id = *packet.LegacyBehavioralRuleID
		}
		ruleProject := ""
		if packet.State == models.RuleStateActiveProject || (packet.LegacyBehavioralRuleID != nil && packet.Scope != "global") {
			ruleProject = project
		}
		rules = append(rules, &pb.SessionStartRule{
			Id:       id,
			Project:  ruleProject,
			Content:  packet.Content,
			Priority: int32(packet.Priority),
			Version:  1,
		})
	}
	return rules
}

func mapSessionStartRuleRouter(result ruleinjection.SelectionResult) *pb.SessionStartRuleRouter {
	return &pb.SessionStartRuleRouter{
		Enabled:         true,
		Mode:            "router",
		KernelCount:     int32(len(result.Kernel)),
		ContextualCount: int32(len(result.Contextual)),
		SuppressedCount: int32(len(result.Suppressed)),
		BudgetOutcome:   result.BudgetOutcome,
		Kernel:          mapRulePackets("kernel", result.Kernel),
		Contextual:      mapRulePackets("contextual", result.Contextual),
		Suppressed:      mapSuppressedRulePackets(result.Suppressed),
	}
}

func mapRulePackets(bucket string, packets []ruleinjection.RulePacket) []*pb.SessionStartRulePacket {
	out := make([]*pb.SessionStartRulePacket, 0, len(packets))
	for _, packet := range packets {
		legacyID := int64(0)
		if packet.LegacyBehavioralRuleID != nil {
			legacyID = *packet.LegacyBehavioralRuleID
		}
		out = append(out, &pb.SessionStartRulePacket{
			RuleVersionId:          packet.ID,
			LegacyBehavioralRuleId: legacyID,
			Bucket:                 bucket,
			Scope:                  packet.Scope,
			Audience:               packet.Audience,
			Content:                packet.Content,
			Summary:                packet.Summary,
			EvidenceHandles:        append([]string(nil), packet.EvidenceHandles...),
			State:                  string(packet.State),
			BudgetClass:            packet.BudgetClass,
			Priority:               int32(packet.Priority),
		})
	}
	return out
}

func mapSuppressedRulePackets(packets []ruleinjection.SuppressedPacket) []*pb.SessionStartRulePacket {
	out := make([]*pb.SessionStartRulePacket, 0, len(packets))
	for _, packet := range packets {
		legacyID := int64(0)
		if packet.LegacyBehavioralRuleID != nil {
			legacyID = *packet.LegacyBehavioralRuleID
		}
		out = append(out, &pb.SessionStartRulePacket{
			RuleVersionId:          packet.ID,
			LegacyBehavioralRuleId: legacyID,
			Bucket:                 "suppressed",
			SuppressionReason:      packet.Reason,
		})
	}
	return out
}

func mapSessionStartIssues(rows []dbgorm.IssueWithCount) []*pb.SessionStartIssue {
	issues := make([]*pb.SessionStartIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, &pb.SessionStartIssue{
			Id:             row.ID,
			Title:          row.Title,
			Body:           row.Body,
			Status:         row.Status,
			Priority:       row.Priority,
			Type:           row.Type,
			SourceProject:  row.SourceProject,
			TargetProject:  row.TargetProject,
			SourceAgent:    row.SourceAgent,
			Labels:         append([]string(nil), row.Labels...),
			CommentCount:   row.CommentCount,
			AcknowledgedAt: timestampProto(row.AcknowledgedAt),
			ResolvedAt:     timestampProto(row.ResolvedAt),
			ReopenedAt:     timestampProto(row.ReopenedAt),
			ClosedAt:       timestampProto(row.ClosedAt),
			CreatedAt:      timestamppb.New(row.CreatedAt),
			UpdatedAt:      timestamppb.New(row.UpdatedAt),
		})
	}
	return issues
}

func mapSessionStartRules(rows []dbgorm.BehavioralRule) []*pb.SessionStartRule {
	rules := make([]*pb.SessionStartRule, 0, len(rows))
	for _, row := range rows {
		project := ""
		if row.Project != nil {
			project = *row.Project
		}
		rules = append(rules, &pb.SessionStartRule{
			Id:        row.ID,
			Project:   project,
			Content:   row.Content,
			EditedBy:  row.EditedBy,
			Priority:  int32(row.Priority),
			Version:   int32(row.Version),
			CreatedAt: timestamppb.New(row.CreatedAt),
			UpdatedAt: timestamppb.New(row.UpdatedAt),
		})
	}
	return rules
}

func mapSessionStartMemories(rows []*models.Memory) []*pb.SessionStartMemory {
	memories := make([]*pb.SessionStartMemory, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		memories = append(memories, &pb.SessionStartMemory{
			Id:          row.ID,
			Project:     row.Project,
			Content:     row.Content,
			Tags:        append([]string(nil), row.Tags...),
			SourceAgent: row.SourceAgent,
			EditedBy:    row.EditedBy,
			Version:     int32(row.Version),
			CreatedAt:   timestamppb.New(row.CreatedAt),
			UpdatedAt:   timestamppb.New(row.UpdatedAt),
		})
	}
	return memories
}

func buildSessionStartMetaSummary(ctx context.Context, store sessionStartMemoryPager, project string, caller scope.KeycardContext, opts scope.MemoryVisibilityOptions, generatedAt time.Time, scanBudget int) (*pb.SessionStartMetaSummary, error) {
	summary := &pb.SessionStartMetaSummary{
		Project:     strings.TrimSpace(project),
		GeneratedAt: timestamppb.New(generatedAt),
	}
	counts := map[string]int64{}
	var total int64
	var oldest time.Time
	var newest time.Time
	haveVisible := false
	if scanBudget <= 0 {
		scanBudget = sessionStartVisibilityScanBudget
	}
	for offset := 0; offset < scanBudget; {
		batchLimit := sessionStartMemoryBatchSize
		if remaining := scanBudget - offset; remaining < batchLimit {
			batchLimit = remaining
		}
		if batchLimit <= 0 {
			break
		}
		batch, err := store.ListWithOffset(ctx, project, batchLimit, offset)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			if row == nil || !scope.ResolveMemory(caller, row, opts) {
				continue
			}
			total++
			if !haveVisible {
				oldest = row.CreatedAt
				newest = row.CreatedAt
				haveVisible = true
			} else {
				if row.CreatedAt.Before(oldest) {
					oldest = row.CreatedAt
				}
				if row.CreatedAt.After(newest) {
					newest = row.CreatedAt
				}
			}
			for _, tag := range row.Tags {
				tag = strings.TrimSpace(tag)
				if tag == "" {
					continue
				}
				counts[tag]++
			}
		}
		offset += len(batch)
		if len(batch) < batchLimit {
			break
		}
	}
	summary.TotalCount = total
	summary.TopTags = summarizeSessionStartTopTagsFromCounts(counts, 6)
	if !haveVisible {
		return summary, nil
	}
	summary.OldestCreatedAt = timestamppb.New(oldest)
	summary.NewestCreatedAt = timestamppb.New(newest)
	return summary, nil
}

func summarizeSessionStartTopTagsFromCounts(counts map[string]int64, max int) []*pb.SessionStartMetaTagCount {
	items := make([]*pb.SessionStartMetaTagCount, 0, len(counts))
	for tag, count := range counts {
		items = append(items, &pb.SessionStartMetaTagCount{Tag: tag, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Tag < items[j].Tag
	})
	if max <= 0 || max > len(items) {
		max = len(items)
	}
	return items[:max]
}

func summarizeSessionStartTopTags(rows []*models.Memory, max int) []*pb.SessionStartMetaTagCount {
	counts := map[string]int64{}
	for _, row := range rows {
		if row == nil {
			continue
		}
		for _, tag := range row.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			counts[tag]++
		}
	}
	return summarizeSessionStartTopTagsFromCounts(counts, max)
}

func timestampProto(ts *time.Time) *timestamppb.Timestamp {
	if ts == nil || ts.IsZero() {
		return nil
	}
	return timestamppb.New(*ts)
}
