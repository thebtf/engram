package gorm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/cognitive/s4directives"
	"github.com/thebtf/engram/pkg/cognitive"
)

type attentionEventRow struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Project        string    `gorm:"column:project;type:text;not null;index:idx_attention_events_project_created,priority:1"`
	SessionID      string    `gorm:"column:session_id;type:text;not null;index:idx_attention_events_session_created,priority:1"`
	SourceTurnHash string    `gorm:"column:source_turn_hash;type:text;not null"`
	DerivedIntent  string    `gorm:"column:derived_intent;type:text;not null"`
	AgentConfirmed bool      `gorm:"column:agent_confirmed;not null;default:true"`
	Horizon        string    `gorm:"column:horizon;type:text;not null"`
	PrivacyClass   string    `gorm:"column:privacy_class;type:text;not null"`
	CreatedAt      time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now();index:idx_attention_events_project_created,priority:2,sort:desc;index:idx_attention_events_session_created,priority:2,sort:desc"`
	UpdatedAt      time.Time `gorm:"column:updated_at;type:timestamptz;not null;default:now()"`
}

func (attentionEventRow) TableName() string { return "attention_events" }

type AttentionEventStore struct {
	db *gorm.DB
}

func NewAttentionEventStore(db *gorm.DB) *AttentionEventStore {
	return &AttentionEventStore{db: db}
}

func (s *AttentionEventStore) Create(ctx context.Context, event cognitive.AttentionEventRecord) (*s4directives.StoredAttentionEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("attention_event_store create: db not configured")
	}
	row, err := attentionEventRowFromRecord(event)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("attention_event_store create: %w", err)
	}
	return attentionEventRowToStored(row), nil
}

func (s *AttentionEventStore) Get(ctx context.Context, id int64) (*s4directives.StoredAttentionEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("attention_event_store get: db not configured")
	}
	if id <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var row attentionEventRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, fmt.Errorf("attention_event_store get %d: %w", id, err)
	}
	return attentionEventRowToStored(&row), nil
}

func (s *AttentionEventStore) ListByProject(ctx context.Context, project string, limit int) ([]s4directives.StoredAttentionEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("attention_event_store list_by_project: db not configured")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("attention_event_store list_by_project: project is required")
	}
	if limit <= 0 {
		limit = 50
	}
	var rows []attentionEventRow
	if err := s.db.WithContext(ctx).
		Where("project = ?", project).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("attention_event_store list_by_project %q: %w", project, err)
	}
	results := make([]s4directives.StoredAttentionEvent, 0, len(rows))
	for i := range rows {
		results = append(results, *attentionEventRowToStored(&rows[i]))
	}
	return results, nil
}

func attentionEventRowFromRecord(event cognitive.AttentionEventRecord) (*attentionEventRow, error) {
	project := strings.TrimSpace(event.Project)
	if project == "" {
		return nil, fmt.Errorf("attention_event_store create: project is required")
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("attention_event_store create: session_id is required")
	}
	sourceTurnHash := strings.TrimSpace(event.SourceTurnHash)
	if sourceTurnHash == "" {
		return nil, fmt.Errorf("attention_event_store create: source_turn_hash is required")
	}
	if !s4directives.IsCanonicalSourceTurnHash(sourceTurnHash) {
		return nil, fmt.Errorf("attention_event_store create: invalid source_turn_hash")
	}
	derivedIntent := strings.TrimSpace(event.DerivedIntent)
	if derivedIntent == "" {
		return nil, fmt.Errorf("attention_event_store create: derived_intent is required")
	}
	if !event.AgentConfirmed {
		return nil, fmt.Errorf("attention_event_store create: agent_confirmed must be true")
	}
	horizon := strings.TrimSpace(event.Horizon)
	if !validAttentionEventHorizon(horizon) {
		return nil, fmt.Errorf("attention_event_store create: invalid horizon %q", horizon)
	}
	privacyClass := strings.TrimSpace(event.PrivacyClass)
	if !validAttentionEventPrivacyClass(privacyClass) {
		return nil, fmt.Errorf("attention_event_store create: invalid privacy_class %q", privacyClass)
	}
	now := time.Now().UTC()
	return &attentionEventRow{
		Project:        project,
		SessionID:      sessionID,
		SourceTurnHash: sourceTurnHash,
		DerivedIntent:  derivedIntent,
		AgentConfirmed: true,
		Horizon:        horizon,
		PrivacyClass:   privacyClass,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func attentionEventRowToStored(row *attentionEventRow) *s4directives.StoredAttentionEvent {
	if row == nil {
		return nil
	}
	return &s4directives.StoredAttentionEvent{
		ID:             row.ID,
		Project:        row.Project,
		SessionID:      row.SessionID,
		SourceTurnHash: row.SourceTurnHash,
		DerivedIntent:  row.DerivedIntent,
		AgentConfirmed: row.AgentConfirmed,
		Horizon:        row.Horizon,
		PrivacyClass:   row.PrivacyClass,
		CreatedAt:      row.CreatedAt.UTC(),
	}
}

func validAttentionEventHorizon(value string) bool {
	switch value {
	case "session", "project", "permanent":
		return true
	default:
		return false
	}
}

func validAttentionEventPrivacyClass(value string) bool {
	switch value {
	case "public", "internal", "secret":
		return true
	default:
		return false
	}
}
