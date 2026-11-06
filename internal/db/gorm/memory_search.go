package gorm

import (
	"context"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/db/projectscope"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
)

func (s *MemoryStore) memorySearchQuery(ctx context.Context, project string, access scope.MemoryReadAccess) (*gorm.DB, error) {
	if project == "" {
		return nil, fmt.Errorf("project: must not be empty")
	}
	predicate, args := projectscope.MemoryPredicate("m", project)
	q := s.db.WithContext(ctx).Model(&Memory{}).Table("memories AS m").
		Where(predicate, args...).Where("m.status = 'active' AND m.deleted_at IS NULL").
		Where("m.valid_from IS NULL OR m.valid_from <= NOW()").
		Where("m.valid_until IS NULL OR m.valid_until >= NOW()")
	return memoryReaderPrivacy(q, access.Caller, access.Visibility), nil
}

// CheckCanonicalProject applies the same selector validation as GetVisible.
func (s *MemoryStore) CheckCanonicalProject(ctx context.Context, project string) error {
	_, err := canonicalProjectRows(ctx, s.db.Model(&Memory{}), project, "memories", "m")
	return err
}

// ListMemoryPageForSearch uses the caller-aware SQL reader when available.
// Legacy injected pagers retain their existing post-filtering contract.
func ListMemoryPageForSearch(ctx context.Context, pager interface {
	ListWithOffset(context.Context, string, int, int) ([]*models.Memory, error)
}, project string, limit, offset int, access scope.MemoryReadAccess,
) ([]*models.Memory, error) {
	if authorized, ok := pager.(interface {
		ListWithFiltersForSearch(context.Context, string, ListOptions, scope.MemoryReadAccess) ([]*models.Memory, error)
	}); ok {
		return authorized.ListWithFiltersForSearch(ctx, project, ListOptions{Limit: limit, Offset: offset}, access)
	}
	return pager.ListWithOffset(ctx, project, limit, offset)
}

// ListForSearch applies caller policy before the bounded Tier0 candidate pool.
func (s *MemoryStore) ListForSearch(ctx context.Context, project string, limit int, access scope.MemoryReadAccess) ([]*models.Memory, error) {
	q, err := s.memorySearchQuery(ctx, project, access)
	if err != nil {
		return nil, err
	}
	rows, err := findRecoveryMemoryRows(q.Order("m.created_at DESC, m.id DESC").Limit(normalizeMemoryListLimit(limit)))
	return projectMemoryRows(rows, project), err
}

// SearchFTSForSearch keeps both passes inside caller policy. The returned set
// identifies OR-recovery matches, whose rank is weaker evidence than a strict hit.
func (s *MemoryStore) SearchFTSForSearch(ctx context.Context, project, query string, limit int, access scope.MemoryReadAccess) ([]*models.Memory, map[int64]bool, error) {
	rows, fallback, err := s.searchFTSWithFallback(ctx, project, query, limit, &access)
	if err != nil || !fallback || len(rows) == 0 {
		return rows, nil, err
	}
	partial := make(map[int64]bool, len(rows))
	for _, memory := range rows {
		partial[memory.ID] = true
	}
	return rows, partial, nil
}

// SearchLiteralForSearch preserves malformed-tag recovery without allowing
// inaccessible content or tags to consume the candidate page.
func (s *MemoryStore) SearchLiteralForSearch(ctx context.Context, project, query string, limit, offset int, access scope.MemoryReadAccess) ([]*models.Memory, error) {
	return s.searchLiteralWithAccess(ctx, project, query, limit, offset, &access)
}

// GetByRefsForSearch repeats project, lifecycle, ACL, version and body-digest
// checks in the body query. Reused version numbers cannot substitute new content.
func (s *MemoryStore) GetByRefsForSearch(ctx context.Context, project string, refs []scope.MemoryRef, access scope.MemoryReadAccess) ([]*models.Memory, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	q, err := s.memorySearchQuery(ctx, project, access)
	if err != nil {
		return nil, err
	}
	predicates := make([]string, 0, len(refs))
	args := make([]any, 0, 3*len(refs))
	for _, ref := range refs {
		if ref.ID <= 0 || ref.Version <= 0 || len(ref.ContentDigest) != 64 {
			return nil, fmt.Errorf("memory search: invalid candidate reference")
		}
		predicates = append(predicates, "(m.id = ? AND m.version = ? AND "+scope.MemoryContentDigestSQL+" = ?)")
		args = append(args, ref.ID, ref.Version, ref.ContentDigest)
	}
	rows, err := findRecoveryMemoryRows(q.Where("("+strings.Join(predicates, " OR ")+")", args...))
	if err != nil {
		return nil, fmt.Errorf("materialize memory search: %w", err)
	}
	byID := make(map[int64]*models.Memory, len(rows))
	for _, memory := range rows {
		memory.Project = project
		byID[memory.ID] = memory
	}
	result := make([]*models.Memory, 0, len(rows))
	for _, ref := range refs {
		if memory := byID[ref.ID]; ref.Matches(memory) {
			result = append(result, memory)
		}
	}
	return result, nil
}
