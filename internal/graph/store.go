package graph

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Edge is the GORM row struct for knowledge_edges.
type Edge struct {
	ID              int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	SourceID        int64      `gorm:"not null" json:"source_id"`
	TargetID        int64      `gorm:"not null" json:"target_id"`
	EdgeType        string     `gorm:"type:text;not null" json:"edge_type"`
	Weight          float64    `gorm:"type:real;not null;default:1.0" json:"weight"`
	Reasoning       string     `gorm:"type:text;not null;default:''" json:"reasoning"`
	SourceSessionID string     `gorm:"type:text;not null;default:''" json:"source_session_id"`
	ValidFrom       time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"valid_from"`
	ValidUntil      time.Time  `gorm:"type:timestamptz;not null;default:'infinity'" json:"valid_until"`
	CreatedAt       time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	SupersededAt    *time.Time `gorm:"type:timestamptz" json:"superseded_at,omitempty"`
}

func (Edge) TableName() string { return "knowledge_edges" }

// Store handles knowledge_edges CRUD.
type Store struct {
	db *gorm.DB
}

// NewStore creates a new graph Store.
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Create inserts a new edge.
func (s *Store) Create(ctx context.Context, e *Edge) (*Edge, error) {
	if e.SourceID == 0 || e.TargetID == 0 {
		return nil, fmt.Errorf("source_id and target_id required")
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
