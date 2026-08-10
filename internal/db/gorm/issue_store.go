package gorm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"
)

// projectBareName strips the "_<hash>" suffix from a canonical hashed project ID
// like "mcp-mux_e54050" → "mcp-mux". If the input has no recognizable hash suffix,
// it is returned unchanged. Hash suffixes are 6-8 hex characters following "_".
var projectHashSuffixRe = regexp.MustCompile(`_[0-9a-f]{6,8}$`)

func projectBareName(projectID string) string {
	return projectHashSuffixRe.ReplaceAllString(projectID, "")
}

var (
	// ErrIssueNotFound indicates that no issue exists for the requested ID.
	ErrIssueNotFound = errors.New("issue_not_found")
	// ErrIssueInvalidTransition indicates that the requested lifecycle change is not allowed.
	ErrIssueInvalidTransition = errors.New("issue_invalid_transition")
	// ErrIssueInvalidInput indicates that request data cannot be persisted.
	ErrIssueInvalidInput = errors.New("issue_invalid_input")
	// ErrIssueForbidden indicates that the authenticated actor cannot mutate the issue.
	ErrIssueForbidden = errors.New("issue_forbidden")
)

// IssueStore provides CRUD operations for issues and issue comments.
type IssueStore struct {
	db *gorm.DB
}

// NewIssueStore creates a new IssueStore.
func NewIssueStore(db *gorm.DB) *IssueStore {
	return &IssueStore{db: db}
}

// ResolveProject normalizes a project slug through the legacy_ids lookup.
// Returns the canonical project ID, or the input unchanged if no alias found.
func (s *IssueStore) ResolveProject(ctx context.Context, projectID string) string {
	return ResolveProjectID(ctx, s.db, projectID)
}

// IssueWithCount extends Issue with a computed comment count for list views.
type IssueWithCount struct {
	Issue
	CommentCount int64 `gorm:"column:comment_count" json:"comment_count"`
}

// validIssueTypes is the allowed set of issue type values.
var validIssueTypes = map[string]bool{"bug": true, "feature": true, "improvement": true, "task": true}

// CreateIssue inserts a new issue and returns its ID.
func (s *IssueStore) CreateIssue(ctx context.Context, issue *Issue) (int64, error) {
	now := time.Now()
	created := Issue{
		Title:            issue.Title,
		Body:             issue.Body,
		Status:           issue.Status,
		Priority:         issue.Priority,
		Type:             issue.Type,
		SourceProject:    issue.SourceProject,
		TargetProject:    issue.TargetProject,
		SourceAgent:      issue.SourceAgent,
		CreatedBySession: issue.CreatedBySession,
		CreatorKeycardID: issue.CreatorKeycardID,
		Labels:           issue.Labels,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if created.Status == "" {
		created.Status = "open"
	}
	if created.Priority == "" {
		created.Priority = "medium"
	}
	if created.Type == "" {
		created.Type = "task"
	}

	// Validate status and priority before INSERT (avoid cryptic CHECK constraint errors)
	validStatuses := map[string]bool{"open": true, "acknowledged": true, "resolved": true, "reopened": true, "closed": true, "rejected": true}
	if !validStatuses[created.Status] {
		return 0, fmt.Errorf("invalid status %q: must be one of open, acknowledged, resolved, reopened", created.Status)
	}
	validPriorities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	if !validPriorities[created.Priority] {
		return 0, fmt.Errorf("invalid priority %q: must be one of critical, high, medium, low", created.Priority)
	}
	if !validIssueTypes[created.Type] {
		return 0, fmt.Errorf("invalid type %q: must be one of bug, feature, improvement, task", created.Type)
	}

	if err := s.db.WithContext(ctx).Create(&created).Error; err != nil {
		return 0, fmt.Errorf("create issue: %w", err)
	}
	return created.ID, nil
}

// IssueListParams holds optional filters for ListIssues.
type IssueListParams struct {
	TargetProject string
	SourceProject string
	Statuses      []string
	ResolvedSince *time.Time
	Type          string
	Limit         int
	Offset        int
}

// ListIssues returns issues matching the filters with comment counts, stale_days, and total count.
// Ordered by priority (critical first) then newest first.
func (s *IssueStore) ListIssues(ctx context.Context, targetProject string, statuses []string, limit, offset int) ([]IssueWithCount, int64, error) {
	return s.ListIssuesEx(ctx, IssueListParams{
		TargetProject: targetProject,
		Statuses:      statuses,
		Limit:         limit,
		Offset:        offset,
	})
}

// ListIssuesEx returns issues with extended filtering (source_project, resolved_since, stale_days).
func (s *IssueStore) ListIssuesEx(ctx context.Context, params IssueListParams) ([]IssueWithCount, int64, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}

	query := s.db.WithContext(ctx).Table("issues")
	// Project matching accepts both canonical hashed IDs (dirname_hash) and bare
	// slug names, because hooks use hashed IDs but MCP callers use bare names.
	// Query "mcp-mux" matches issues with target_project="mcp-mux" AND "mcp-mux_e54050".
	// Query "mcp-mux_e54050" matches issues with target_project="mcp-mux_e54050" AND "mcp-mux".
	if params.TargetProject != "" {
		bare := projectBareName(params.TargetProject)
		query = query.Where(`target_project = ? OR target_project = ? OR target_project LIKE ? ESCAPE '\'`,
			params.TargetProject, bare, escapeSQLLike(bare)+`\_%`)
	}
	if params.SourceProject != "" {
		bare := projectBareName(params.SourceProject)
		query = query.Where(`source_project = ? OR source_project = ? OR source_project LIKE ? ESCAPE '\'`,
			params.SourceProject, bare, escapeSQLLike(bare)+`\_%`)
	}
	if len(params.Statuses) > 0 {
		query = query.Where("status IN ?", params.Statuses)
	}
	if params.ResolvedSince != nil {
		query = query.Where("resolved_at >= ?", *params.ResolvedSince)
	}
	if params.Type != "" {
		query = query.Where("type = ?", params.Type)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count issues: %w", err)
	}

	var issues []IssueWithCount
	err := query.
		Select("issues.*, (SELECT COUNT(*) FROM issue_comments WHERE issue_comments.issue_id = issues.id) AS comment_count").
		Order("CASE priority WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 WHEN 'low' THEN 4 END, created_at DESC").
		Limit(limit).
		Offset(params.Offset).
		Find(&issues).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list issues: %w", err)
	}

	return issues, total, nil
}

// GetIssue returns a single issue with its comments.
func (s *IssueStore) GetIssue(ctx context.Context, id int64) (*Issue, []IssueComment, error) {
	var issue Issue
	if err := s.db.WithContext(ctx).First(&issue, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil, fmt.Errorf("issue %d not found", id)
		}
		return nil, nil, fmt.Errorf("get issue: %w", err)
	}

	var comments []IssueComment
	if err := s.db.WithContext(ctx).
		Where("issue_id = ?", id).
		Order("created_at ASC").
		Find(&comments).Error; err != nil {
		return nil, nil, fmt.Errorf("get issue comments: %w", err)
	}

	return &issue, comments, nil
}

// UpdateIssueStatus transitions an issue to a new status with appropriate timestamps.
func (s *IssueStore) UpdateIssueStatus(ctx context.Context, id int64, status string) error {
	return s.UpdateIssueStatusWithComment(ctx, id, status, "", "", "")
}

// UpdateIssueStatusWithComment applies a status transition and its optional
// comment atomically so callers never observe a resolved issue without the
// comment that explained the transition.
func (s *IssueStore) UpdateIssueStatusWithComment(ctx context.Context, id int64, status, comment, authorProject, authorAgent string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := updateIssueStatusTx(tx, id, status, now); err != nil {
			return err
		}
		if comment == "" {
			return nil
		}
		_, err := addIssueCommentTx(tx, id, &IssueComment{
			AuthorProject: authorProject,
			AuthorAgent:   authorAgent,
			Body:          comment,
		}, now)
		return err
	})
}

// IssueUpdate is the complete mutation requested by a REST issue PATCH.
type IssueUpdate struct {
	Status        string
	Comment       string
	AuthorProject string
	AuthorAgent   string
	Title         string
	Body          string
	Priority      string
	Type          string
	Labels        []string
}

func (u IssueUpdate) hasFields() bool {
	return u.Title != "" || u.Body != "" || u.Priority != "" || u.Type != "" || u.Labels != nil
}

// UpdateIssueAtomically applies fields, a lifecycle transition, and an optional
// comment as a single transaction.
func (s *IssueStore) UpdateIssueAtomically(ctx context.Context, id int64, update IssueUpdate, isOperator bool) error {
	if update.Status != "" {
		switch update.Status {
		case "resolved", "reopened", "closed", "rejected", "open", "acknowledged":
		default:
			return fmt.Errorf("%w: invalid status %q", ErrIssueInvalidInput, update.Status)
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if update.hasFields() {
			if err := updateIssueFieldsTx(tx, id, update.Title, update.Body, update.Priority, update.Type, update.Labels, now); err != nil {
				return err
			}
		}
		switch update.Status {
		case "":
			if update.Comment == "" {
				return nil
			}
			_, err := addIssueCommentTx(tx, id, &IssueComment{AuthorProject: update.AuthorProject, AuthorAgent: update.AuthorAgent, Body: update.Comment}, now)
			return err
		case "resolved":
			if err := updateIssueStatusTx(tx, id, update.Status, now); err != nil {
				return err
			}
		case "open", "acknowledged":
			if !isOperator {
				return fmt.Errorf("%w: issue mutation forbidden: operator identity is required", ErrIssueForbidden)
			}
			if err := updateIssueStatusTx(tx, id, update.Status, now); err != nil {
				return err
			}
		case "reopened":
			if err := reopenIssueTx(tx, id, now); err != nil {
				return err
			}
		case "closed":
			if err := closeIssueTx(tx, id, isOperator, now); err != nil {
				return err
			}
		case "rejected":
			if !isOperator {
				return fmt.Errorf("%w: issue mutation forbidden: operator identity is required", ErrIssueForbidden)
			}
			if update.Comment == "" {
				return fmt.Errorf("%w: comment is required when rejecting an issue", ErrIssueInvalidInput)
			}
			return rejectIssueTx(tx, id, update.Comment, update.AuthorProject, update.AuthorAgent, now)
		}
		if update.Comment == "" {
			return nil
		}
		_, err := addIssueCommentTx(tx, id, &IssueComment{AuthorProject: update.AuthorProject, AuthorAgent: update.AuthorAgent, Body: update.Comment}, now)
		return err
	})
}

func updateIssueStatusTx(tx *gorm.DB, id int64, status string, now time.Time) error {
	updates := map[string]interface{}{
		"status":     status,
		"updated_at": now,
	}

	switch status {
	case "resolved":
		updates["resolved_at"] = now
	case "reopened":
		updates["reopened_at"] = now
	case "acknowledged":
		updates["acknowledged_at"] = now
	case "closed":
		updates["closed_at"] = now
	}

	result := tx.Model(&Issue{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update issue status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: issue %d", ErrIssueNotFound, id)
	}
	return nil
}

func reopenIssueTx(tx *gorm.DB, id int64, now time.Time) error {
	var issue Issue
	if err := tx.First(&issue, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("%w: issue %d", ErrIssueNotFound, id)
		}
		return err
	}
	if issue.Status != "resolved" {
		return fmt.Errorf("%w: issue %d is %s, not resolved — cannot reopen", ErrIssueInvalidTransition, id, issue.Status)
	}
	result := tx.Model(&Issue{}).Where("id = ? AND status = ?", id, "resolved").Updates(map[string]any{
		"status":      "reopened",
		"reopened_at": now,
		"updated_at":  now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: issue %d is no longer resolved (concurrent modification)", ErrIssueInvalidTransition, id)
	}
	return nil
}

func rejectIssueTx(tx *gorm.DB, id int64, comment, authorProject, authorAgent string, now time.Time) error {
	result := tx.Model(&Issue{}).Where("id = ?", id).Updates(map[string]any{
		"status":     "rejected",
		"closed_at":  now,
		"updated_at": now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: issue %d", ErrIssueNotFound, id)
	}
	return tx.Create(&IssueComment{
		IssueID:       id,
		AuthorProject: authorProject,
		AuthorAgent:   authorAgent,
		Body:          "Rejected: " + comment,
		CreatedAt:     now,
	}).Error
}

// AddComment adds a comment to an issue and updates the issue's updated_at.
func (s *IssueStore) AddComment(ctx context.Context, issueID int64, comment *IssueComment) (int64, error) {
	var commentID int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		commentID, err = addIssueCommentTx(tx, issueID, comment, time.Now())
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("add comment: %w", err)
	}
	return commentID, nil
}

func addIssueCommentTx(tx *gorm.DB, issueID int64, comment *IssueComment, now time.Time) (int64, error) {
	// Verify issue exists before inserting comment (prevents orphan rows).
	var count int64
	if err := tx.Model(&Issue{}).Where("id = ?", issueID).Count(&count).Error; err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("%w: issue %d", ErrIssueNotFound, issueID)
	}
	created := IssueComment{
		IssueID:       issueID,
		AuthorProject: comment.AuthorProject,
		AuthorAgent:   comment.AuthorAgent,
		Body:          comment.Body,
		CreatedAt:     now,
	}
	if err := tx.Create(&created).Error; err != nil {
		return 0, err
	}
	if err := tx.Model(&Issue{}).Where("id = ?", issueID).Update("updated_at", now).Error; err != nil {
		return 0, err
	}
	return created.ID, nil
}

// AcknowledgeIssues bulk-transitions issues from open to acknowledged.
// Only affects issues with status='open'. Returns count of updated rows.
func (s *IssueStore) AcknowledgeIssues(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	now := time.Now()
	result := s.db.WithContext(ctx).
		Model(&Issue{}).
		Where("id IN ? AND status = ?", ids, "open").
		Updates(map[string]interface{}{
			"status":          "acknowledged",
			"acknowledged_at": now,
			"updated_at":      now,
		})

	if result.Error != nil {
		return 0, fmt.Errorf("acknowledge issues: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// AcknowledgeIssuesAtomically requires every requested issue to exist and be
// open before transitioning the complete set in one transaction.
func (s *IssueStore) AcknowledgeIssuesAtomically(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, fmt.Errorf("%w: issue ids are required", ErrIssueInvalidInput)
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return 0, fmt.Errorf("%w: invalid issue id %d", ErrIssueInvalidInput, id)
		}
		if _, exists := seen[id]; exists {
			return 0, fmt.Errorf("%w: duplicate issue id %d", ErrIssueInvalidInput, id)
		}
		seen[id] = struct{}{}
	}

	var acknowledged int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&Issue{}).Where("id IN ? AND status = ?", ids, "open").Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return fmt.Errorf("%w: all requested issues must exist and be open", ErrIssueInvalidTransition)
		}
		now := time.Now()
		result := tx.Model(&Issue{}).Where("id IN ? AND status = ?", ids, "open").Updates(map[string]any{
			"status":          "acknowledged",
			"acknowledged_at": now,
			"updated_at":      now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return fmt.Errorf("%w: issue acknowledgement changed concurrently", ErrIssueInvalidTransition)
		}
		acknowledged = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("acknowledge issues: %w", err)
	}
	return acknowledged, nil
}

// ReopenIssue transitions a resolved issue back to reopened state.
// Returns error if issue is not in 'resolved' state.
// Optionally adds a comment explaining the reopen reason.
func (s *IssueStore) ReopenIssue(ctx context.Context, id int64, comment, authorProject, authorAgent string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Verify issue exists and is resolved.
		var issue Issue
		if err := tx.First(&issue, id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return fmt.Errorf("%w: issue %d", ErrIssueNotFound, id)
			}
			return err
		}
		if issue.Status != "resolved" {
			return fmt.Errorf("%w: issue %d is %s, not resolved — cannot reopen", ErrIssueInvalidTransition, id, issue.Status)
		}

		// Include the old state in the update to prevent a race.
		now := time.Now()
		result := tx.Model(&Issue{}).Where("id = ? AND status = ?", id, "resolved").Updates(map[string]interface{}{
			"status":      "reopened",
			"reopened_at": now,
			"updated_at":  now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("%w: issue %d is no longer resolved (concurrent modification)", ErrIssueInvalidTransition, id)
		}
		if comment == "" {
			return nil
		}
		_, err := addIssueCommentTx(tx, id, &IssueComment{
			AuthorProject: authorProject,
			AuthorAgent:   authorAgent,
			Body:          comment,
		}, now)
		return err
	})
}

// AuthorizeIssueMutation permits an authenticated operator or the SourceClient
// keycard that created the issue. Project metadata is never authorization data.
func (s *IssueStore) AuthorizeIssueMutation(ctx context.Context, id int64, keycardID string, isOperator bool) error {
	if isOperator {
		return nil
	}
	if keycardID == "" {
		return fmt.Errorf("%w: issue mutation forbidden: authenticated client keycard is required", ErrIssueForbidden)
	}

	var issue Issue
	if err := s.db.WithContext(ctx).Select("creator_keycard_id").First(&issue, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("%w: issue %d", ErrIssueNotFound, id)
		}
		return fmt.Errorf("authorize issue mutation: %w", err)
	}
	if issue.CreatorKeycardID == "" || issue.CreatorKeycardID != keycardID {
		return fmt.Errorf("%w: issue mutation forbidden", ErrIssueForbidden)
	}
	return nil
}

// CloseIssue transitions an issue to closed state. Authorization belongs to
// the transport boundary and must complete before this store mutation.
func (s *IssueStore) CloseIssue(ctx context.Context, id int64, isOperator bool) error {
	return s.CloseIssueWithComment(ctx, id, isOperator, "", "", "")
}

// CloseIssueWithComment atomically closes an issue and records an optional comment.
func (s *IssueStore) CloseIssueWithComment(ctx context.Context, id int64, isOperator bool, comment, authorProject, authorAgent string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := closeIssueTx(tx, id, isOperator, now); err != nil {
			return err
		}
		if comment == "" {
			return nil
		}
		_, err := addIssueCommentTx(tx, id, &IssueComment{
			AuthorProject: authorProject,
			AuthorAgent:   authorAgent,
			Body:          comment,
		}, now)
		return err
	})
}

func closeIssueTx(tx *gorm.DB, id int64, isOperator bool, now time.Time) error {
	var issue Issue
	if err := tx.First(&issue, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("%w: issue %d", ErrIssueNotFound, id)
		}
		return err
	}
	validFromStates := map[string]bool{"resolved": true, "reopened": true}
	if !isOperator && !validFromStates[issue.Status] {
		return fmt.Errorf("%w: issue %d is %s — can only close from resolved or reopened state", ErrIssueInvalidTransition, id, issue.Status)
	}
	if issue.Status == "closed" {
		return fmt.Errorf("%w: issue %d is already closed", ErrIssueInvalidTransition, id)
	}
	result := tx.Model(&Issue{}).Where("id = ? AND status = ?", id, issue.Status).Updates(map[string]any{
		"status":     "closed",
		"closed_at":  now,
		"updated_at": now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: issue %d state changed concurrently", ErrIssueInvalidTransition, id)
	}
	return nil
}

// RejectIssue transitions any issue to rejected state with a mandatory comment.
// Intended for human operators (dashboard). No lifecycle validation.
func (s *IssueStore) RejectIssue(ctx context.Context, id int64, comment, authorProject, authorAgent string) error {
	if comment == "" {
		return fmt.Errorf("%w: comment is required when rejecting an issue", ErrIssueInvalidInput)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		result := tx.Model(&Issue{}).Where("id = ?", id).Updates(map[string]any{
			"status":     "rejected",
			"closed_at":  now,
			"updated_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("%w: issue %d", ErrIssueNotFound, id)
		}
		return tx.Create(&IssueComment{
			IssueID:       id,
			AuthorProject: authorProject,
			AuthorAgent:   authorAgent,
			Body:          "Rejected: " + comment,
			CreatedAt:     now,
		}).Error
	})
}

// DeleteIssue hard-deletes an issue and all its comments.
func (s *IssueStore) DeleteIssue(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("issue_id = ?", id).Delete(&IssueComment{}).Error; err != nil {
			return fmt.Errorf("delete issue comments: %w", err)
		}
		result := tx.Delete(&Issue{}, id)
		if result.Error != nil {
			return fmt.Errorf("delete issue: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("issue %d not found", id)
		}
		return nil
	})
}

// UpdateIssueFields updates mutable fields (title, body, priority, labels, type) for dashboard editing.
// Only non-zero-value fields are updated.
func (s *IssueStore) UpdateIssueFields(ctx context.Context, id int64, title, body, priority, issueType string, labels []string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return updateIssueFieldsTx(tx, id, title, body, priority, issueType, labels, time.Now())
	})
}

func updateIssueFieldsTx(tx *gorm.DB, id int64, title, body, priority, issueType string, labels []string, now time.Time) error {
	updates := map[string]any{"updated_at": now}
	if title != "" {
		updates["title"] = title
	}
	if body != "" {
		updates["body"] = body
	}
	if priority != "" {
		validPriorities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
		if !validPriorities[priority] {
			return fmt.Errorf("%w: invalid priority %q", ErrIssueInvalidInput, priority)
		}
		updates["priority"] = priority
	}
	if issueType != "" {
		if !validIssueTypes[issueType] {
			return fmt.Errorf("%w: invalid type %q: must be one of bug, feature, improvement, task", ErrIssueInvalidInput, issueType)
		}
		updates["type"] = issueType
	}
	if labels != nil {
		updates["labels"] = labels
	}
	result := tx.Model(&Issue{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update issue fields: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: issue %d", ErrIssueNotFound, id)
	}
	return nil
}

// GetTrackedProjects returns the set of projects that have at least one issue
// (as source or target). Used to determine which projects are reachable via engram.
func (s *IssueStore) GetTrackedProjects(ctx context.Context) ([]string, error) {
	var projects []string
	err := s.db.WithContext(ctx).Raw(`
		SELECT DISTINCT project FROM (
			SELECT target_project AS project FROM issues WHERE target_project != ''
			UNION
			SELECT source_project AS project FROM issues WHERE source_project != ''
		) AS p
		WHERE project != ''
		ORDER BY project
	`).Scan(&projects).Error
	if err != nil {
		return nil, fmt.Errorf("get tracked projects: %w", err)
	}
	return projects, nil
}
