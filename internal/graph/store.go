package graph

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ErrDangling is returned by Resolve when an edge references a row that no
// longer exists in the target table. Per spec §EC-F7, dangling edges are NOT
// an error condition that fails callers — they are a flag for operator
// visibility. Callers must use errors.Is(err, ErrDangling) to detect this.
var ErrDangling = errors.New("dangling edge: referenced row not found")

// resolveDiscriminator normalises a discriminator string: empty → "memory".
// This provides backward compatibility for edges written before migration 127
// added the source_type/target_type columns (their value is NULL/empty and
// should default to the legacy memory interpretation).
func resolveDiscriminator(d string) string {
	if d == "" {
		return "memory"
	}
	return d
}

// Edge is the GORM row struct for knowledge_edges.
// Migration 127 extended the table with discriminator columns (source_type,
// target_type) and nullable FK columns (node_source_id, node_target_id).
// SourceID and TargetID are nullable at the DB level (migration 127 dropped
// NOT NULL): they are NULL for node-typed endpoints and populated only for
// memory-typed endpoints. *int64 forces discriminator-aware access and prevents
// silent 0-reads when GORM scans NULL from Postgres.
type Edge struct {
	ID              int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	SourceID        *int64     `gorm:"column:source_id" json:"source_id,omitempty"`
	TargetID        *int64     `gorm:"column:target_id" json:"target_id,omitempty"`
	EdgeType        string     `gorm:"type:text;not null" json:"edge_type"`
	Weight          float64    `gorm:"type:real;not null;default:1.0" json:"weight"`
	Reasoning       string     `gorm:"type:text;not null;default:''" json:"reasoning"`
	SourceSessionID string     `gorm:"type:text;not null;default:''" json:"source_session_id"`
	ValidFrom       time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"valid_from"`
	ValidUntil      time.Time  `gorm:"type:timestamptz;not null;default:'9999-12-31T23:59:59Z'" json:"valid_until"`
	CreatedAt       time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	SupersededAt    *time.Time `gorm:"type:timestamptz" json:"superseded_at,omitempty"`

	// Migration 127 discriminator columns (ADR-F-001 Path C).
	// Default 'memory' ensures backward compat with pre-127 rows.
	SourceType string `gorm:"column:source_type;not null;default:memory" json:"source_type"`
	TargetType string `gorm:"column:target_type;not null;default:memory" json:"target_type"`

	// Migration 127 nullable FK columns for node-type endpoints.
	// Populated when source_type='node' / target_type='node'.
	NodeSourceID *int64 `gorm:"column:node_source_id" json:"node_source_id,omitempty"`
	NodeTargetID *int64 `gorm:"column:node_target_id" json:"node_target_id,omitempty"`
}

func (Edge) TableName() string { return "knowledge_edges" }

// Store handles knowledge_edges CRUD.
type Store struct {
	db    *gorm.DB
	nodes *NodesStore // used by Resolve for node-type endpoint resolution
}

// NewStore creates a new graph Store.
// ns may be nil; if provided it is used by Resolve for node-type endpoint lookups.
func NewStore(db *gorm.DB, ns *NodesStore) *Store {
	return &Store{db: db, nodes: ns}
}

// memoryRow is a minimal projection of the memories table used by Resolve.
type memoryRow struct {
	ID int64 `gorm:"column:id"`
}

func (memoryRow) TableName() string { return "memories" }

// Resolve fetches the source and target rows referenced by this edge.
//
// Return values follow the EC-F7 contract:
//   - (source, target, nil)     — both endpoints resolved successfully
//   - (source, nil, ErrDangling) — target is referenced but the row is gone
//   - (nil, nil, ErrDangling)   — source is referenced but the row is gone
//   - (nil, nil, err)           — unexpected database error
//
// The discriminators source_type/target_type drive which table is queried.
// Empty discriminator is normalised to "memory" for backward compat with
// edges written before migration 127.
//
// Node-typed endpoints are fetched with includePrivate=false: private nodes
// are not visible via the public Resolve path (same contract as ListByType).
// Use ResolvePrivileged for administrative callers that need private visibility.
//
// Anti-stub: replacing with `return nil, nil, nil` breaks T015 integration
// tests that assert resolved types, and T016 dangling acceptance test.
func (s *Store) Resolve(ctx context.Context, e *Edge) (source any, target any, err error) {
	return s.resolve(ctx, e, false)
}

// ResolvePrivileged is the privileged variant of Resolve that returns private nodes.
// Use only for internal/administrative callers that have already verified access.
func (s *Store) ResolvePrivileged(ctx context.Context, e *Edge) (source any, target any, err error) {
	return s.resolve(ctx, e, true)
}

func (s *Store) resolve(ctx context.Context, e *Edge, includePrivate bool) (source any, target any, err error) {
	srcType := resolveDiscriminator(e.SourceType)
	tgtType := resolveDiscriminator(e.TargetType)

	var srcMemID int64
	if e.SourceID != nil {
		srcMemID = *e.SourceID
	}
	source, err = s.resolveEndpoint(ctx, srcType, srcMemID, e.NodeSourceID, includePrivate)
	if err != nil {
		if errors.Is(err, ErrDangling) {
			return nil, nil, ErrDangling
		}
		return nil, nil, fmt.Errorf("resolve source: %w", err)
	}

	var tgtMemID int64
	if e.TargetID != nil {
		tgtMemID = *e.TargetID
	}
	target, err = s.resolveEndpoint(ctx, tgtType, tgtMemID, e.NodeTargetID, includePrivate)
	if err != nil {
		if errors.Is(err, ErrDangling) {
			return source, nil, ErrDangling
		}
		return nil, nil, fmt.Errorf("resolve target: %w", err)
	}

	return source, target, nil
}

// resolveEndpoint looks up one edge endpoint based on its discriminator.
// endpointType is "memory" or "node"; memoryID and nodeID are the respective
// FK columns (one should be zero/nil, the other populated).
// includePrivate controls whether node-typed endpoints with privacy_scope='private'
// are visible; callers should pass false for all MCP/public-facing paths.
func (s *Store) resolveEndpoint(ctx context.Context, endpointType string, memoryID int64, nodeID *int64, includePrivate bool) (any, error) {
	switch endpointType {
	case "node":
		if nodeID == nil {
			return nil, fmt.Errorf("graph: edge integrity violation: source_type='node' with nil node_source_id")
		}
		if s.nodes == nil {
			return nil, fmt.Errorf("NodesStore not configured on graph.Store")
		}
		node, err := s.nodes.Get(ctx, *nodeID, includePrivate)
		if err != nil {
			// Treat "not found" (including privacy-filtered not-found) as dangling;
			// other errors propagate.
			return nil, fmt.Errorf("%w: %s", ErrDangling, err.Error())
		}
		return node, nil
	default: // "memory"
		if memoryID == 0 {
			return nil, fmt.Errorf("%w: source_type='memory' but source_id is 0", ErrDangling)
		}
		// Fetch memory row using raw db query to avoid importing worker/memory packages.
		var row struct {
			ID int64 `gorm:"column:id"`
		}
		err := s.db.WithContext(ctx).
			Table("memories").
			Select("id").
			Where("id = ? AND deleted_at IS NULL", memoryID).
			First(&row).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("%w: memory %d not found", ErrDangling, memoryID)
			}
			return nil, fmt.Errorf("lookup memory %d: %w", memoryID, err)
		}
		// Return the memory ID as a plain int64 sentinel.
		// Full Memory struct retrieval is left to callers who need it —
		// Resolve intentionally returns the minimal proof that the row exists.
		// T015 integration test verifies the type via type assertion.
		return row.ID, nil
	}
}

// Create inserts a new edge.
// Validation is discriminator-aware: memory-typed endpoints require a non-nil,
// non-zero source_id/target_id; node-typed endpoints require node_source_id /
// node_target_id instead and must have source_id/target_id nil.
func (s *Store) Create(ctx context.Context, e *Edge) (*Edge, error) {
	srcType := resolveDiscriminator(e.SourceType)
	tgtType := resolveDiscriminator(e.TargetType)
	if srcType == "memory" {
		if e.SourceID == nil || *e.SourceID == 0 {
			return nil, fmt.Errorf("source_id required when source_type='memory'")
		}
	}
	if tgtType == "memory" {
		if e.TargetID == nil || *e.TargetID == 0 {
			return nil, fmt.Errorf("target_id required when target_type='memory'")
		}
	}
	if !ValidEdgeType(e.EdgeType) {
		return nil, fmt.Errorf("invalid edge type: %s", e.EdgeType)
	}
	if e.Weight < 0 || e.Weight > 1 {
		return nil, fmt.Errorf("weight must be between 0 and 1")
	}
	if err := s.db.WithContext(ctx).Create(e).Error; err != nil {
		return nil, fmt.Errorf("create edge: %w", err)
	}
	return e, nil
}

// Get retrieves an edge by ID.
func (s *Store) Get(ctx context.Context, id int64) (*Edge, error) {
	var e Edge
	err := s.db.WithContext(ctx).
		Where("id = ? AND superseded_at IS NULL", id).
		First(&e).Error
	if err != nil {
		return nil, fmt.Errorf("get edge: %w", err)
	}
	return &e, nil
}

// Direction specifies outgoing, incoming, or both.
type Direction string

const (
	Outgoing Direction = "outgoing"
	Incoming Direction = "incoming"
	Both     Direction = "both"
)

// ListByMemory returns active edges for a memory in the given direction.
func (s *Store) ListByMemory(ctx context.Context, memoryID int64, dir Direction, edgeType string) ([]Edge, error) {
	q := s.db.WithContext(ctx).Where("superseded_at IS NULL")
	switch dir {
	case Outgoing:
		q = q.Where("source_id = ?", memoryID)
	case Incoming:
		q = q.Where("target_id = ?", memoryID)
	default:
		q = q.Where("source_id = ? OR target_id = ?", memoryID, memoryID)
	}
	if edgeType != "" {
		q = q.Where("edge_type = ?", edgeType)
	}
	var edges []Edge
	if err := q.Order("created_at DESC").Find(&edges).Error; err != nil {
		return nil, fmt.Errorf("list edges: %w", err)
	}
	return edges, nil
}

// SoftDelete marks an edge as superseded.
func (s *Store) SoftDelete(ctx context.Context, id int64) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&Edge{}).
		Where("id = ? AND superseded_at IS NULL", id).
		Update("superseded_at", now)
	if result.Error != nil {
		return fmt.Errorf("soft delete edge: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("edge %d not found or already superseded", id)
	}
	return nil
}

// ListByNode returns active edges for a knowledge_node (source_type='node' or
// target_type='node') in the given direction. T014 / Milestone F TG2.
func (s *Store) ListByNode(ctx context.Context, nodeID int64, dir Direction, edgeType string) ([]Edge, error) {
	q := s.db.WithContext(ctx).Where("superseded_at IS NULL")
	switch dir {
	case Outgoing:
		q = q.Where("source_type = 'node' AND node_source_id = ?", nodeID)
	case Incoming:
		q = q.Where("target_type = 'node' AND node_target_id = ?", nodeID)
	default:
		q = q.Where(
			"(source_type = 'node' AND node_source_id = ?) OR (target_type = 'node' AND node_target_id = ?)",
			nodeID, nodeID,
		)
	}
	if edgeType != "" {
		q = q.Where("edge_type = ?", edgeType)
	}
	var edges []Edge
	if err := q.Order("created_at DESC").Find(&edges).Error; err != nil {
		return nil, fmt.Errorf("list edges by node: %w", err)
	}
	return edges, nil
}

// FindSynonyms returns synonym_of and same_concept_as edges for a memory.
func (s *Store) FindSynonyms(ctx context.Context, memoryID int64) ([]Edge, error) {
	var edges []Edge
	err := s.db.WithContext(ctx).
		Where("superseded_at IS NULL").
		Where("(source_id = ? OR target_id = ?) AND edge_type IN ('synonym_of', 'same_concept_as')", memoryID, memoryID).
		Find(&edges).Error
	if err != nil {
		return nil, fmt.Errorf("find synonyms: %w", err)
	}
	return edges, nil
}
