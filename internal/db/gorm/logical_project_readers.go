package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

// Source membership never changes a checkout, grant, or published snapshot.
const browserCodeProjectMembershipJoin = `LEFT JOIN LATERAL (
 SELECT CASE
   WHEN source.project_id IS NOT NULL AND EXISTS (SELECT 1 FROM projects p
     WHERE p.id = source.project_id AND p.removed_at IS NULL
       AND (p.identity_status IS NULL OR p.identity_status = 'active')) THEN source.project_id
   WHEN source.project_id IS NULL AND COUNT(DISTINCT m.project_id) = 1
     AND BOOL_AND(m.project_id IS NOT NULL AND EXISTS (SELECT 1 FROM projects p
       WHERE p.id = m.project_id AND p.removed_at IS NULL
         AND (p.identity_status IS NULL OR p.identity_status = 'active')))
     THEN MIN(m.project_id) END AS project_id,
   CASE WHEN source.project_id IS NULL AND BOOL_AND(m.project_id IS NULL)
     THEN MIN(NULLIF(m.unresolved_reference, '')) END AS unresolved_reference
 FROM project_consolidation_memberships m
 WHERE m.table_name = 'sources' AND m.row_key->>'source_id' = source.source_id::text
) membership ON TRUE`

// An inventoried occurrence overrides its historical selector. Uninventoried
// current rows belong only to their exact UUID, never to an alias bucket.
func canonicalProjectRows(ctx context.Context, db *gormlib.DB, project, table, alias string) (*gormlib.DB, error) {
	if !projectidentity.ValidProjectID(project) {
		return nil, ErrKnowledgeProjectForbidden
	}
	exists, err := (&SessionStore{db: db}).CanonicalProjectExists(ctx, project)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrKnowledgeProjectForbidden
	}
	if table != "memories" && table != "versioned_documents" {
		return nil, fmt.Errorf("unsupported content table")
	}
	q := db.WithContext(ctx).Table(table + " AS " + alias)
	return q.Where(canonicalProjectMembershipPredicate(alias, "project"), project, table, table, project), nil
}

func canonicalProjectMembershipPredicate(alias, field string) string {
	return `((` + alias + `.` + field + ` = ? AND NOT EXISTS (SELECT 1 FROM project_consolidation_memberships membership
 WHERE membership.table_name = ? AND membership.field_path = '["` + field + `"]' AND membership.row_key->>'id' = CAST(` + alias + `.id AS text))) OR EXISTS
 (SELECT 1 FROM project_consolidation_memberships membership WHERE membership.table_name = ? AND membership.field_path = '["` + field + `"]'
 AND membership.row_key->>'id' = CAST(` + alias + `.id AS text) AND membership.project_id = ?
 AND NOT EXISTS (SELECT 1 FROM project_consolidation_memberships conflict
 WHERE conflict.table_name = membership.table_name AND conflict.row_key = membership.row_key AND conflict.field_path = membership.field_path
 AND (conflict.project_id IS NULL OR conflict.project_id <> membership.project_id))))`
}

func RetainedContentIDs(rows []ConsolidationMembership) ([]int64, error) {
	ids := make([]int64, 0, len(rows))
	seen := make(map[int64]bool, len(rows))
	for _, row := range rows {
		var key struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(row.RowKey, &key) != nil || key.ID < 1 {
			return nil, &ConsolidationError{Code: "PROJECT_ADOPTION_STALE_REFERENCE"}
		}
		if !seen[key.ID] {
			ids = append(ids, key.ID)
			seen[key.ID] = true
		}
	}
	return ids, nil
}

func memoryReaderPrivacy(q *gormlib.DB, caller scope.KeycardContext, opts scope.MemoryVisibilityOptions) *gormlib.DB {
	principal, kind := strings.TrimSpace(caller.Principal), strings.TrimSpace(caller.PrincipalKind)
	q = q.Where(memoryPrincipalAccessSQL, principal, kind, principal, kind, principal, kind, principal, kind)
	if opts.ApplyPrivacyScope {
		q = q.Where(memoryPrivacyAccessSQL, caller.WorkstationID, caller.WorkstationID, caller.SessionID, caller.SessionID)
	}
	return q
}

func (s *SessionStore) RetainedMemories(ctx context.Context, ids []int64, query string, caller scope.KeycardContext, opts scope.MemoryVisibilityOptions, limit, offset int) ([]*models.Memory, int64, error) {
	q := memoryReaderPrivacy(s.db.WithContext(ctx).Model(&Memory{}).Table("memories AS m").Where("m.id IN ? AND m.deleted_at IS NULL", ids), caller, opts)
	if query != "" {
		q = q.Where("LOWER(m.content) LIKE ? ESCAPE '\\'", "%"+escapeSQLLike(strings.ToLower(query))+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Memory
	if err := q.Order("m.created_at DESC, m.id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]*models.Memory, len(rows))
	for i := range rows {
		items[i] = memoryRowToModel(&rows[i])
	}
	return items, total, nil
}

func (s *SessionStore) RetainedDocuments(ctx context.Context, ids []int64, query string, limit, offset int) ([]VersionedDocument, int64, error) {
	q := s.db.WithContext(ctx).Model(&VersionedDocument{}).Where("id IN ?", ids)
	if query != "" {
		literal := "%" + escapeSQLLike(strings.ToLower(query)) + "%"
		q = q.Where("LOWER(path) LIKE ? ESCAPE '\\' OR LOWER(content) LIKE ? ESCAPE '\\'", literal, literal)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]VersionedDocument, 0)
	err := q.Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *SessionStore) RetainedIssues(ctx context.Context, ids []int64, query string, limit, offset int) ([]Issue, int64, error) {
	q := s.db.WithContext(ctx).Model(&Issue{}).Where("id IN ?", ids)
	if query != "" {
		literal := "%" + escapeSQLLike(strings.ToLower(query)) + "%"
		q = q.Where("LOWER(title) LIKE ? ESCAPE '\\' OR LOWER(body) LIKE ? ESCAPE '\\'", literal, literal)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]Issue, 0)
	err := q.Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *SessionStore) RetainedMemoryAudit(ctx context.Context, id int64, limit, offset int) ([]AuditLogEntry, int64, error) {
	q := s.db.WithContext(ctx).Model(&AuditLogEntry{}).Where("memory_id = ?", id)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]AuditLogEntry, 0)
	err := q.Select("id, memory_id, action, actor, source_session_id, reason, created_at, CASE WHEN before_state IS NOT NULL THEN '{}'::jsonb END AS before_state, CASE WHEN after_state IS NOT NULL THEN '{}'::jsonb END AS after_state").Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *SessionStore) RetainedIssueHistory(ctx context.Context, id int64, limit, offset int) ([]IssueComment, int64, error) {
	q := s.db.WithContext(ctx).Model(&IssueComment{}).Where("issue_id = ?", id)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]IssueComment, 0)
	err := q.Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *SessionStore) RetainedDocumentHistory(ctx context.Context, ids []int64, document VersionedDocument, limit, offset int) ([]VersionedDocument, int64, error) {
	q := s.db.WithContext(ctx).Model(&VersionedDocument{}).Where("id IN ? AND path = ? AND project = ?", ids, document.Path, document.Project)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]VersionedDocument, 0)
	err := q.Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}
