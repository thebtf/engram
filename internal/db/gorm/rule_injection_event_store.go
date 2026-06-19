package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

type ruleInjectionEventRow struct {
	CreatedAt              time.Time     `gorm:"column:created_at;autoCreateTime"`
	SessionID              string        `gorm:"column:session_id;not null"`
	Project                string        `gorm:"column:project;not null"`
	Surface                string        `gorm:"column:surface;not null"`
	EventType              string        `gorm:"column:event_type;not null"`
	Reason                 string        `gorm:"column:reason;not null;default:''"`
	RuleVersionID          sql.NullInt64 `gorm:"column:rule_version_id"`
	LegacyBehavioralRuleID sql.NullInt64 `gorm:"column:legacy_behavioral_rule_id"`
	ID                     int64         `gorm:"primaryKey;autoIncrement"`
	BudgetPosition         int           `gorm:"column:budget_position;not null;default:0"`
}

func (ruleInjectionEventRow) TableName() string { return "rule_injection_events" }

type RuleInjectionEventStore struct {
	db *gorm.DB
}

type RuleInjectionTelemetryParams struct {
	Project       string
	Since         time.Time
	RuleVersionID int64
	Limit         int
}

type RuleInjectionTelemetryAggregate struct {
	Project       string
	RuleVersionID int64
	Buckets       []RuleInjectionTelemetryBucket
	NoData        bool
}

type RuleInjectionTelemetryBucket struct {
	EventType  models.RuleInjectionEventType
	Reasons    []string
	LastSeenAt time.Time
	Count      int
}

func NewRuleInjectionEventStore(db *gorm.DB) *RuleInjectionEventStore {
	return &RuleInjectionEventStore{db: db}
}

func (s *RuleInjectionEventStore) RecordEvents(ctx context.Context, events []*models.RuleInjectionEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("rule_injection_events: store is not initialized")
	}
	if len(events) == 0 {
		return nil
	}
	rows := make([]ruleInjectionEventRow, 0, len(events))
	for _, event := range events {
		row, err := fromRuleInjectionEvent(event)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	if err := s.db.WithContext(ctx).Create(&rows).Error; err != nil {
		return fmt.Errorf("rule_injection_events record: %w", err)
	}
	return nil
}

func (s *RuleInjectionEventStore) ListBySession(ctx context.Context, sessionID string, limit int) ([]*models.RuleInjectionEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("rule_injection_events: store is not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("rule_injection_events: session_id must not be empty")
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []ruleInjectionEventRow
	if err := s.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at ASC, id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("rule_injection_events list_by_session: %w", err)
	}
	out := make([]*models.RuleInjectionEvent, 0, len(rows))
	for i := range rows {
		out = append(out, toRuleInjectionEvent(&rows[i]))
	}
	return out, nil
}

func (s *RuleInjectionEventStore) AggregateByProjectRuleAndEventType(ctx context.Context, params RuleInjectionTelemetryParams) (RuleInjectionTelemetryAggregate, error) {
	if s == nil || s.db == nil {
		return RuleInjectionTelemetryAggregate{}, fmt.Errorf("rule_injection_events: store is not initialized")
	}
	project := strings.TrimSpace(params.Project)
	if project == "" {
		return RuleInjectionTelemetryAggregate{}, fmt.Errorf("rule_injection_events: project must not be empty")
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 100
	}
	aggregate := RuleInjectionTelemetryAggregate{
		Project:       project,
		RuleVersionID: params.RuleVersionID,
	}

	base := s.telemetryQuery(ctx, project, params.RuleVersionID, params.Since)
	type bucketRow struct {
		EventType  string
		LastSeenAt time.Time
		Count      int64
	}
	var rows []bucketRow
	if err := base.
		Select("event_type, COUNT(*) AS count, MAX(created_at) AS last_seen_at").
		Group("event_type").
		Order("count DESC, event_type ASC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return RuleInjectionTelemetryAggregate{}, fmt.Errorf("rule_injection_events aggregate: %w", err)
	}
	if len(rows) == 0 {
		aggregate.NoData = true
		return aggregate, nil
	}

	aggregate.Buckets = make([]RuleInjectionTelemetryBucket, 0, len(rows))
	for _, row := range rows {
		eventType := models.RuleInjectionEventType(row.EventType)
		reasons, err := s.telemetryReasons(ctx, project, params.RuleVersionID, params.Since, eventType)
		if err != nil {
			return RuleInjectionTelemetryAggregate{}, err
		}
		aggregate.Buckets = append(aggregate.Buckets, RuleInjectionTelemetryBucket{
			EventType:  eventType,
			Count:      int(row.Count),
			LastSeenAt: row.LastSeenAt,
			Reasons:    reasons,
		})
	}
	return aggregate, nil
}

func (s *RuleInjectionEventStore) telemetryQuery(ctx context.Context, project string, ruleVersionID int64, since time.Time) *gorm.DB {
	query := s.db.WithContext(ctx).Model(&ruleInjectionEventRow{}).Where("project = ?", project)
	if ruleVersionID > 0 {
		query = query.Where("rule_version_id = ?", ruleVersionID)
	}
	if !since.IsZero() {
		query = query.Where("created_at >= ?", since)
	}
	return query
}

func (s *RuleInjectionEventStore) telemetryReasons(ctx context.Context, project string, ruleVersionID int64, since time.Time, eventType models.RuleInjectionEventType) ([]string, error) {
	var reasons []string
	if err := s.telemetryQuery(ctx, project, ruleVersionID, since).
		Where("event_type = ? AND reason <> ''", string(eventType)).
		Distinct("reason").
		Order("reason ASC").
		Pluck("reason", &reasons).Error; err != nil {
		return nil, fmt.Errorf("rule_injection_events aggregate reasons: %w", err)
	}
	return reasons, nil
}

func fromRuleInjectionEvent(event *models.RuleInjectionEvent) (ruleInjectionEventRow, error) {
	if event == nil {
		return ruleInjectionEventRow{}, fmt.Errorf("rule_injection_events: event must not be nil")
	}
	sessionID := strings.TrimSpace(event.SessionID)
	project := strings.TrimSpace(event.Project)
	surface := strings.TrimSpace(event.Surface)
	if sessionID == "" || project == "" || surface == "" || !event.EventType.IsValid() {
		return ruleInjectionEventRow{}, models.ErrRuleRequiredFieldMissing
	}
	row := ruleInjectionEventRow{
		SessionID:      sessionID,
		Project:        project,
		Surface:        surface,
		EventType:      string(event.EventType),
		Reason:         strings.TrimSpace(event.Reason),
		BudgetPosition: event.BudgetPosition,
	}
	if !event.CreatedAt.IsZero() {
		row.CreatedAt = event.CreatedAt
	}
	if event.RuleVersionID != nil && *event.RuleVersionID > 0 {
		row.RuleVersionID = sql.NullInt64{Int64: *event.RuleVersionID, Valid: true}
	}
	if event.LegacyBehavioralRuleID != nil && *event.LegacyBehavioralRuleID > 0 {
		row.LegacyBehavioralRuleID = sql.NullInt64{Int64: *event.LegacyBehavioralRuleID, Valid: true}
	}
	return row, nil
}

func toRuleInjectionEvent(row *ruleInjectionEventRow) *models.RuleInjectionEvent {
	event := &models.RuleInjectionEvent{
		ID:             row.ID,
		CreatedAt:      row.CreatedAt,
		SessionID:      row.SessionID,
		Project:        row.Project,
		Surface:        row.Surface,
		EventType:      models.RuleInjectionEventType(row.EventType),
		Reason:         row.Reason,
		BudgetPosition: row.BudgetPosition,
	}
	if row.RuleVersionID.Valid {
		id := row.RuleVersionID.Int64
		event.RuleVersionID = &id
	}
	if row.LegacyBehavioralRuleID.Valid {
		id := row.LegacyBehavioralRuleID.Int64
		event.LegacyBehavioralRuleID = &id
	}
	return event
}
