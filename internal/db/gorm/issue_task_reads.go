package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	issues "github.com/thebtf/engram/internal/issues/contract"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const IssueTaskPageBytes = 4 << 20

type IssueTaskListParams struct {
	IssueListParams
	ReadProject    string
	Query          string
	OwnerLabel     *string
	RecipientLabel *string
}

// IssueTaskSummary is a SQL projection, not a truncated detail. Large private
// body/metadata and creator keycard fields are never selected into this page.
type IssueTaskSummary struct {
	ID                     int64                  `json:"id"`
	Revision               int64                  `json:"revision,string"`
	Title                  string                 `json:"title"`
	Status                 string                 `json:"status"`
	Priority               string                 `json:"priority"`
	Type                   string                 `json:"type"`
	SourceProject          string                 `json:"source_project"`
	TargetProject          string                 `json:"target_project"`
	SourceAgent            string                 `json:"source_agent"`
	CreatedBySession       string                 `json:"created_by_session"`
	CreatedBySessionSource string                 `json:"created_by_session_source"`
	SourceClient           string                 `json:"source_client"`
	TargetSession          string                 `json:"target_session"`
	Labels                 models.JSONStringArray `json:"labels"`
	OwnerLabel             *string                `json:"owner_label"`
	RecipientLabel         *string                `json:"recipient_label"`
	AcknowledgedAt         *time.Time             `json:"acknowledged_at"`
	ResolvedAt             *time.Time             `json:"resolved_at"`
	ReopenedAt             *time.Time             `json:"reopened_at"`
	ClosedAt               *time.Time             `json:"closed_at"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
	CommentCount           int64                  `json:"comment_count"`
	IssueProjectRoles
}

type IssueTaskList struct {
	Issues     []IssueTaskSummary `json:"issues"`
	Total      int64              `json:"total"`
	Limit      int                `json:"limit"`
	Offset     int                `json:"offset"`
	NextOffset *int               `json:"next_offset"`
}

func (s *IssueStore) ListTaskIssues(ctx context.Context, params IssueTaskListParams) (IssueTaskList, error) {
	result := IssueTaskList{Issues: []IssueTaskSummary{}, Limit: params.Limit, Offset: params.Offset}
	if params.Limit < 1 || params.Limit > 200 || params.Offset < 0 || !ValidIssueSessionSelector(params.SourceSession) || !ValidIssueSessionSelector(params.TargetSession) || !ValidIssueSessionSelector(params.ParticipantSession) {
		return result, issues.Failure("INVALID_ARGUMENT")
	}
	err := s.db.WithContext(ctx).Session(&gormlib.Session{Logger: logger.Discard}).Transaction(func(tx *gormlib.DB) error {
		for _, project := range []string{params.ReadProject, params.SourceProject, params.TargetProject} {
			if project == "" {
				continue
			}
			if !projectidentity.ValidProjectID(project) {
				return issues.Failure("INVALID_ARGUMENT")
			}
			exists, err := (&SessionStore{db: tx}).CanonicalProjectExists(ctx, project)
			if err != nil {
				return err
			}
			if !exists {
				return issues.Failure("ISSUE_NOT_FOUND")
			}
		}
		query := tx.Table("issues")
		if params.ReadProject != "" {
			predicate := canonicalProjectMembershipPredicate("issues", "source_project") + " OR " + canonicalProjectMembershipPredicate("issues", "target_project")
			query = query.Where("("+predicate+")", params.ReadProject, "issues", "issues", params.ReadProject, params.ReadProject, "issues", "issues", params.ReadProject)
		}
		if params.TargetProject != "" {
			query = query.Where(canonicalProjectMembershipPredicate("issues", "target_project"), params.TargetProject, "issues", "issues", params.TargetProject)
		}
		if params.SourceProject != "" {
			query = query.Where(canonicalProjectMembershipPredicate("issues", "source_project"), params.SourceProject, "issues", "issues", params.SourceProject)
		}
		if params.SourceAgent != "" {
			query = query.Where("source_agent=?", params.SourceAgent)
		}
		if params.SourceSession != "" {
			query = query.Where("issues.created_by_session = ?", params.SourceSession)
		}
		if params.TargetSession != "" {
			query = query.Where("issues.target_session = ?", params.TargetSession)
		}
		if params.ParticipantSession != "" {
			query = query.Where(issueParticipantSessionPredicate, params.ParticipantSession, params.ParticipantSession, params.ParticipantSession, params.ParticipantSession)
		}
		if params.Type != "" {
			query = query.Where("type=?", params.Type)
		}
		if len(params.Statuses) > 0 {
			query = query.Where("status IN ?", params.Statuses)
		}
		if params.ResolvedSince != nil {
			query = query.Where("resolved_at>=?", *params.ResolvedSince)
		}
		if params.OwnerLabel != nil {
			query = query.Where("owner_label=?", *params.OwnerLabel)
		}
		if params.RecipientLabel != nil {
			query = query.Where("recipient_label=?", *params.RecipientLabel)
		}
		if params.Query != "" {
			literal := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(params.Query)
			query = query.Where(`(title ILIKE ? ESCAPE '\' OR body ILIKE ? ESCAPE '\')`, "%"+literal+"%", "%"+literal+"%")
		}
		if err := query.Count(&result.Total).Error; err != nil {
			return err
		}
		columns := "issues.id,issues.revision,issues.title,issues.status,issues.priority,issues.type,issues.source_project,issues.target_project,issues.source_agent,issues.created_by_session,issues.created_by_session_source,issues.source_client,issues.target_session,issues.labels,issues.owner_label,issues.recipient_label,issues.acknowledged_at,issues.resolved_at,issues.reopened_at,issues.closed_at,issues.created_at,issues.updated_at"
		columns += ", (SELECT count(*) FROM issue_comments c WHERE c.issue_id=issues.id) AS comment_count, " + readIssueRoleSQL("source_project") + " AS source_project_id, " + readIssueRoleSQL("target_project") + " AS target_project_id"
		if err := query.Select(columns).Order("CASE priority WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 END,created_at DESC,issues.id DESC").Limit(params.Limit).Offset(params.Offset).Scan(&result.Issues).Error; err != nil {
			return err
		}
		end := params.Offset + len(result.Issues)
		if int64(end) < result.Total {
			result.NextOffset = &end
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return IssueTaskList{}, issues.SafeError(err)
	}
	return result, nil
}

type IssueTaskDetailParams struct {
	ReadProject    string
	Paged          bool
	CommentsOffset int
	CommentsLimit  int
	HistoryOffset  int
	HistoryAfterID int64
	HistoryLimit   int
}

type IssueTaskDetail struct {
	Issue                  Issue               `json:"issue"`
	Roles                  IssueProjectRoles   `json:"-"`
	Comments               []IssueComment      `json:"comments"`
	History                []IssueChange       `json:"history"`
	HistoryEvents          []IssueHistoryEvent `json:"history_events"`
	HistoryHasMore         bool                `json:"history_has_more"`
	HistoryNextAfterID     *int64              `json:"history_next_after_id"`
	HistoryEventsOversized bool                `json:"history_events_oversized"`
	CommentCount           int64               `json:"comment_count"`
	HistoryCount           int64               `json:"history_count"`
	CommentsOffset         int                 `json:"comments_offset"`
	CommentsLimit          int                 `json:"comments_limit"`
	HistoryOffset          int                 `json:"history_offset"`
	HistoryLimit           int                 `json:"history_limit"`
	CommentsNextOffset     *int                `json:"comments_next_offset"`
	HistoryNextOffset      *int                `json:"history_next_offset"`
	CommentsComplete       bool                `json:"comments_complete"`
	HistoryComplete        bool                `json:"history_complete"`
	CommentsOversized      bool                `json:"comments_oversized"`
	HistoryOversized       bool                `json:"history_oversized"`
}

// ReadTaskIssue establishes visibility before loading detail or collection
// counts. Both independent pages share one repeatable-read snapshot. Rows are
// streamed one at a time, so the page target never requires an unbounded drain.
func (s *IssueStore) ReadTaskIssue(ctx context.Context, id int64, params IssueTaskDetailParams) (IssueTaskDetail, error) {
	result := IssueTaskDetail{Comments: []IssueComment{}, History: []IssueChange{}, CommentsOffset: params.CommentsOffset, CommentsLimit: params.CommentsLimit, HistoryOffset: params.HistoryOffset, HistoryLimit: params.HistoryLimit}
	if id < 1 || params.CommentsOffset < 0 || params.HistoryOffset < 0 || params.HistoryAfterID < 0 || params.CommentsLimit < 1 || params.CommentsLimit > 200 || params.HistoryLimit < 1 || params.HistoryLimit > 200 {
		return result, issues.Failure("INVALID_ARGUMENT")
	}
	err := s.db.WithContext(ctx).Session(&gormlib.Session{Logger: logger.Discard}).Transaction(func(tx *gormlib.DB) error {
		query := tx.Model(&Issue{}).Where("issues.id=?", id)
		if params.ReadProject != "" {
			if !projectidentity.ValidProjectID(params.ReadProject) {
				return issues.Failure("INVALID_ARGUMENT")
			}
			exists, err := (&SessionStore{db: tx}).CanonicalProjectExists(ctx, params.ReadProject)
			if err != nil {
				return err
			}
			if !exists {
				return issues.Failure("ISSUE_NOT_FOUND")
			}
			predicate := canonicalProjectMembershipPredicate("issues", "source_project") + " OR " + canonicalProjectMembershipPredicate("issues", "target_project")
			query = query.Where("("+predicate+")", params.ReadProject, "issues", "issues", params.ReadProject, params.ReadProject, "issues", "issues", params.ReadProject)
		}
		if err := query.Take(&result.Issue).Error; err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				return issues.Failure("ISSUE_NOT_FOUND")
			}
			return err
		}
		roles, err := NewIssueStore(tx).ProjectRoles(ctx, []int64{id})
		if err != nil {
			return err
		}
		result.Roles = roles[id]
		eventLimit := params.HistoryLimit
		if eventLimit > 100 {
			eventLimit = 100
		}
		events, oversized, err := readIssueTaskPage[IssueHistoryEvent](tx, tx.Model(&IssueHistoryEvent{}).Where("issue_id = ? AND id > ?", id, params.HistoryAfterID).Order("id ASC").Limit(eventLimit))
		if err != nil {
			return err
		}
		result.HistoryEvents, result.HistoryEventsOversized = events, oversized
		if len(events) > 0 {
			next := events[len(events)-1].ID
			var remaining int64
			if err := tx.Model(&IssueHistoryEvent{}).Where("issue_id = ? AND id > ?", id, next).Limit(1).Count(&remaining).Error; err != nil {
				return err
			}
			if remaining > 0 {
				result.HistoryHasMore = true
				result.HistoryNextAfterID = &next
			}
		}
		if err := tx.Model(&IssueComment{}).Where("issue_id=?", id).Count(&result.CommentCount).Error; err != nil {
			return err
		}
		if err := tx.Model(&IssueChange{}).Where("issue_id=?", id).Count(&result.HistoryCount).Error; err != nil {
			return err
		}
		if !params.Paged && result.CommentCount > 200 {
			return issues.Failure("DETAIL_PAGING_REQUIRED")
		}
		commentLimit, commentOffset := params.CommentsLimit, params.CommentsOffset
		if !params.Paged {
			commentLimit, commentOffset = 200, 0
		}
		comments, oversized, err := readIssueTaskPage[IssueComment](tx, tx.Model(&IssueComment{}).Where("issue_id=?", id).Order("created_at ASC,id ASC").Limit(commentLimit).Offset(commentOffset))
		if err != nil {
			return err
		}
		if !params.Paged && (oversized || int64(len(comments)) != result.CommentCount) {
			return issues.Failure("DETAIL_PAGING_REQUIRED")
		}
		result.Comments, result.CommentsOversized = comments, oversized
		end := commentOffset + len(comments)
		if int64(end) < result.CommentCount {
			result.CommentsNextOffset = &end
		}
		result.CommentsComplete = result.CommentsNextOffset == nil
		history, oversized, err := readIssueTaskPage[IssueChange](tx, tx.Model(&IssueChange{}).Where("issue_id=?", id).Order("revision ASC").Limit(params.HistoryLimit).Offset(params.HistoryOffset))
		if err != nil {
			return err
		}
		result.History, result.HistoryOversized = history, oversized
		historyEnd := params.HistoryOffset + len(history)
		if int64(historyEnd) < result.HistoryCount {
			result.HistoryNextOffset = &historyEnd
		}
		result.HistoryComplete = result.HistoryNextOffset == nil
		if !params.Paged {
			result.CommentsOffset, result.CommentsLimit = 0, 200
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return IssueTaskDetail{}, issues.SafeError(err)
	}
	return result, nil
}

func readIssueTaskPage[T any](tx, query *gormlib.DB) ([]T, bool, error) {
	page := make([]T, 0)
	rows, err := query.Rows()
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	bytes := 2 // JSON array brackets, then one comma for every later item.
	oversized := false
	for rows.Next() {
		var record T
		if err := tx.ScanRows(rows, &record); err != nil {
			return nil, false, err
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, false, err
		}
		size := len(encoded)
		if len(page) > 0 {
			size++
		}
		if bytes+size > IssueTaskPageBytes {
			if len(page) == 0 {
				page = append(page, record)
				oversized = true
			}
			break
		}
		bytes += size
		page = append(page, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return page, oversized, nil
}

// IssueTaskProjectNames projects display names only for roles already released
// by an authorized issue page/detail. It never discovers additional projects.
func (s *IssueStore) IssueTaskProjectNames(ctx context.Context, roles []IssueProjectRoles) (map[string]string, error) {
	names := make(map[string]string)
	for _, role := range roles {
		for _, project := range []*string{role.SourceProjectID, role.TargetProjectID} {
			if project != nil && projectidentity.ValidProjectID(*project) {
				names[*project] = *project
			}
		}
	}
	if len(names) == 0 {
		return names, nil
	}
	ids := make([]string, 0, len(names))
	for id := range names {
		ids = append(ids, id)
	}
	var rows []Project
	err := s.db.WithContext(ctx).Session(&gormlib.Session{Logger: logger.Discard}).Select("id", "display_name").Where("id IN ? AND removed_at IS NULL", ids).Find(&rows).Error
	if err != nil {
		return nil, issues.SafeError(err)
	}
	for _, row := range rows {
		if row.DisplayName.Valid && row.DisplayName.String != "" {
			names[row.ID] = row.DisplayName.String
		}
	}
	return names, nil
}
