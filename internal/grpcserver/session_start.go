package grpcserver

import (
	"context"
	"os"
	"time"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/injection"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultSessionStartMemoriesLimit = 20
	defaultSessionStartIssuesLimit   = 20
	defaultSessionStartRulesLimit    = 20
	maxSessionStartMemoriesLimit     = 200
	maxSessionStartIssuesLimit       = 200
	maxSessionStartRulesLimit        = 200
)

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

	// W4 P1 (CRIT): build scope.KeycardContext once for both branches.
	// When ENGRAM_VNEXT_F_ENABLED=true, private-scope rows written by a
	// different workstation are removed before the response is assembled.
	// Flag-OFF: callerCtx is built but never consumed (scope.FilterMemories
	// is not called), so the path is byte-identical to the pre-fix behavior.
	var callerCtx scope.KeycardContext
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
		if id, ok := auth.IdentityFrom(ctx); ok {
			callerCtx.WorkstationID = id.WorkstationID()
		}
	}

	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
		allMemories, listErr := memoryStore.List(ctx, project, maxSessionStartMemoriesLimit)
		if listErr != nil {
			return nil, status.Error(codes.Internal, "failed to list session-start memories")
		}
		// Apply privacy-scope filter before Thompson scoring so private rows
		// from other workstations are excluded from the candidate pool.
		if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
			allMemories = scope.FilterMemories(callerCtx, allMemories)
		}
		scored := injection.Score(allMemories, memoriesLimit)
		for _, sm := range scored {
			if !sm.Selected {
				break
			}
			memoryRows = append(memoryRows, sm.Memory)
		}
	} else {
		raw, listErr := memoryStore.List(ctx, project, memoriesLimit)
		if listErr != nil {
			return nil, status.Error(codes.Internal, "failed to list session-start memories")
		}
		if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
			raw = scope.FilterMemories(callerCtx, raw)
		}
		memoryRows = raw
	}

	var ruleRows []dbgorm.BehavioralRule
	if err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("project = ? OR project IS NULL", project).
		Order("priority DESC, created_at DESC").
		Limit(rulesLimit).
		Find(&ruleRows).Error; err != nil {
		return nil, status.Error(codes.Internal, "failed to list session-start rules")
	}

	generatedAt := timestamppb.Now()
	return &pb.GetSessionStartContextResponse{
		Issues:      mapSessionStartIssues(issueRows),
		Rules:       mapSessionStartRules(ruleRows),
		Memories:    mapSessionStartMemories(memoryRows),
		GeneratedAt: generatedAt,
	}, nil
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

func timestampProto(ts *time.Time) *timestamppb.Timestamp {
	if ts == nil || ts.IsZero() {
		return nil
	}
	return timestamppb.New(*ts)
}
