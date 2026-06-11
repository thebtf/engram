package graph

import (
	"context"
	"fmt"
	"time"

	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
)

// nodeRow is the GORM row struct for knowledge_nodes (migration 126).
// Mirrors the table schema; the privacy_scope column uses NOT NULL DEFAULT 'project'.
type nodeRow struct {
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
	DeletedAt    *time.Time `gorm:"column:deleted_at"`
	NodeType     string     `gorm:"column:node_type;not null"`
	ExternalRef  string     `gorm:"column:external_ref;not null"`
	Project      string     `gorm:"column:project"`
	PrivacyScope string     `gorm:"column:privacy_scope;not null;default:project"`
	Metadata     []byte     `gorm:"column:metadata;type:jsonb"`
	ID           int64      `gorm:"column:id;primaryKey;autoIncrement"`
}

func (nodeRow) TableName() string { return "knowledge_nodes" }

func rowToNode(r nodeRow) models.KnowledgeNode {
	return models.KnowledgeNode{
		ID:           r.ID,
		NodeType:     r.NodeType,
		ExternalRef:  r.ExternalRef,
		Project:      r.Project,
		PrivacyScope: r.PrivacyScope,
		Metadata:     r.Metadata,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		DeletedAt:    r.DeletedAt,
	}
}

func nodeToRow(n *models.KnowledgeNode) nodeRow {
	ps := n.PrivacyScope
	if ps == "" {
		ps = scope.ScopeProject
	}
	return nodeRow{
		ID:           n.ID,
		NodeType:     n.NodeType,
		ExternalRef:  n.ExternalRef,
		Project:      n.Project,
		PrivacyScope: ps,
		Metadata:     n.Metadata,
		CreatedAt:    n.CreatedAt,
		UpdatedAt:    n.UpdatedAt,
		DeletedAt:    n.DeletedAt,
	}
}

// NodesStore handles knowledge_nodes CRUD with scope-based visibility filtering.
//
// Visibility contract (T012 AC):
//   - ListByType: returns rows with deleted_at IS NULL; when includePrivate is
//     false, rows with privacy_scope='private' are excluded from listing.
//     Project filter is applied upstream (WHERE project = ?).
//   - Create/Get/Update/SoftDelete: operate on any scope; callers enforce
//     visibility before calling.
//
// Anti-stub: removing privacy_scope filter from ListByType causes
// TestNodesStore_T012_ValidateCreate to pass but integration tests (T015)
// that expect scope isolation to fail.
type NodesStore struct {
	db *gorm.DB
}

// NewNodesStore creates a new NodesStore backed by db.
func NewNodesStore(db *gorm.DB) *NodesStore {
	return &NodesStore{db: db}
}

// Create inserts a new knowledge_node row. Validates NodeType, ExternalRef, and
// Project before writing. Sets PrivacyScope to 'project' if empty.
func (s *NodesStore) Create(ctx context.Context, node *models.KnowledgeNode) (*models.KnowledgeNode, error) {
	if node == nil {
		return nil, fmt.Errorf("node is required")
	}
	if !models.ValidNodeType(node.NodeType) {
		return nil, fmt.Errorf("invalid node_type %q", node.NodeType)
	}
	if node.ExternalRef == "" {
		return nil, fmt.Errorf("external_ref is required")
	}
	if node.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	now := time.Now().UTC()
	if node.CreatedAt.IsZero() {
		node.CreatedAt = now
	}
	node.UpdatedAt = now
	if node.PrivacyScope == "" {
		node.PrivacyScope = scope.ScopeProject
	}

	row := nodeToRow(node)
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create knowledge_node: %w", err)
	}
	node.ID = row.ID
	node.CreatedAt = row.CreatedAt
	node.UpdatedAt = row.UpdatedAt
	return node, nil
}

// Get retrieves an active (deleted_at IS NULL) knowledge_node by ID.
// When includePrivate is false, rows with privacy_scope='private' are excluded,
// matching the ListByType visibility contract (T012 AC / 7th-bypass pattern fix).
// Callers that need full access (administrative paths) may pass includePrivate=true.
func (s *NodesStore) Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error) {
	q := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", id)
	if !includePrivate {
		q = q.Where("privacy_scope != ?", scope.ScopePrivate)
	}
	var row nodeRow
	err := q.First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get knowledge_node %d: %w", id, err)
	}
	n := rowToNode(row)
	return &n, nil
}

// ListByType returns active knowledge_nodes of the given type in the given project.
// When nodeType is empty, all types are returned. When includePrivate is false,
// rows with privacy_scope='private' are excluded.
//
// The project parameter is required; an empty project returns no rows and an
// error to prevent accidental cross-project leaks.
func (s *NodesStore) ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required for ListByType")
	}

	// Validate nodeType before touching the DB.
	if nodeType != "" && !models.ValidNodeType(nodeType) {
		return nil, fmt.Errorf("invalid node_type %q", nodeType)
	}

	q := s.db.WithContext(ctx).
		Where("project = ? AND deleted_at IS NULL", project)

	if nodeType != "" {
		q = q.Where("node_type = ?", nodeType)
	}

	if !includePrivate {
		q = q.Where("privacy_scope != ?", scope.ScopePrivate)
	}

	var rows []nodeRow
	if err := q.Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list knowledge_nodes: %w", err)
	}

	nodes := make([]models.KnowledgeNode, len(rows))
	for i, r := range rows {
		nodes[i] = rowToNode(r)
	}
	return nodes, nil
}

// Update replaces mutable fields (ExternalRef, Metadata, PrivacyScope) for an
// active knowledge_node. NodeType and Project are immutable after creation.
func (s *NodesStore) Update(ctx context.Context, node *models.KnowledgeNode) (*models.KnowledgeNode, error) {
	if node == nil {
		return nil, fmt.Errorf("node is required")
	}
	if node.ID == 0 {
		return nil, fmt.Errorf("node.ID is required for Update")
	}
	node.UpdatedAt = time.Now().UTC()
	updates := map[string]interface{}{
		"external_ref":  node.ExternalRef,
		"metadata":      node.Metadata,
		"privacy_scope": node.PrivacyScope,
		"updated_at":    node.UpdatedAt,
	}
	result := s.db.WithContext(ctx).Model(&nodeRow{}).
		Where("id = ? AND deleted_at IS NULL", node.ID).
		Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("update knowledge_node %d: %w", node.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("knowledge_node %d not found or already deleted", node.ID)
	}
	return node, nil
}

// SoftDelete sets deleted_at = now() on the given knowledge_node.
func (s *NodesStore) SoftDelete(ctx context.Context, id int64) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&nodeRow{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Update("deleted_at", now)
	if result.Error != nil {
		return fmt.Errorf("soft delete knowledge_node %d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("knowledge_node %d not found or already deleted", id)
	}
	return nil
}
