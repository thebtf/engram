package gorm

import (
	"context"
	"database/sql"

	"github.com/thebtf/engram/internal/projectidentity"
	gormlib "gorm.io/gorm"
)

type IssueProjectRoles struct {
	ID              int64   `json:"-"`
	SourceProjectID *string `json:"source_project_id"`
	TargetProjectID *string `json:"target_project_id"`
}

func canonicalIssueRoleSQL(field string) string {
	return `(SELECT CASE WHEN COUNT(m.reference)>0 THEN
	 CASE WHEN COUNT(m.reference)=COUNT(m.project_id) AND COUNT(DISTINCT m.project_id)=1
	 AND BOOL_AND(EXISTS(SELECT 1 FROM projects p WHERE p.id=m.project_id
	 AND p.id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
	 AND p.id<>'00000000-0000-0000-0000-000000000000' AND p.removed_at IS NULL
	 AND (p.identity_status IS NULL OR p.identity_status='active'))) THEN MIN(m.project_id) END
	 ELSE (SELECT p.id FROM projects p WHERE p.id=issues.` + field + `
	 AND p.id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
	 AND p.id<>'00000000-0000-0000-0000-000000000000' AND p.removed_at IS NULL
	 AND (p.identity_status IS NULL OR p.identity_status='active')) END
	 FROM project_consolidation_memberships m WHERE m.table_name='issues'
	 AND m.field_path='["` + field + `"]'::jsonb AND m.row_key->>'id'=issues.id::text)`
}

func (s *IssueStore) ProjectRoles(ctx context.Context, ids []int64) (map[int64]IssueProjectRoles, error) {
	result := make(map[int64]IssueProjectRoles, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []IssueProjectRoles
	err := s.db.WithContext(ctx).Table("issues").Select("issues.id,"+canonicalIssueRoleSQL("source_project")+" AS source_project_id,"+canonicalIssueRoleSQL("target_project")+" AS target_project_id").Where("issues.id IN ?", ids).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

// Scope is checked before either the issue body or comments are loaded.
func (s *IssueStore) GetIssueForProject(ctx context.Context, id int64, project string) (*Issue, []IssueComment, error) {
	if !projectidentity.ValidProjectID(project) {
		return nil, nil, ErrIssueForbidden
	}
	var issue *Issue
	var comments []IssueComment
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		exists, err := (&SessionStore{db: tx}).CanonicalProjectExists(ctx, project)
		if err != nil {
			return err
		}
		if !exists {
			return ErrIssueNotFound
		}
		var count int64
		if err := tx.Table("issues").Where("id=? AND ("+canonicalIssueRoleSQL("source_project")+"=? OR "+canonicalIssueRoleSQL("target_project")+"=?)", id, project, project).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrIssueNotFound
		}
		issue, comments, err = NewIssueStore(tx).GetIssue(ctx, id)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return issue, comments, err
}
