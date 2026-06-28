package gorm

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thebtf/engram/pkg/cognitive"
)

// JSONObjectRaw stores one JSON object in a PostgreSQL JSONB column.
type JSONObjectRaw []byte

func (j JSONObjectRaw) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "{}", nil
	}
	return string(j), nil
}

func (j *JSONObjectRaw) Scan(src interface{}) error {
	if src == nil {
		*j = []byte("{}")
		return nil
	}
	switch v := src.(type) {
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = []byte(v)
	default:
		return fmt.Errorf("json_object_raw: unsupported Scan source type %T", src)
	}
	if len(*j) == 0 {
		*j = []byte("{}")
	}
	return nil
}

type sessionStateRow struct {
	CreatedAt time.Time     `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time     `gorm:"column:updated_at;autoUpdateTime"`
	SessionID string        `gorm:"column:session_id;not null;uniqueIndex"`
	Focus     JSONObjectRaw `gorm:"column:focus;type:jsonb;not null;default:'{}'"`
	Execution JSONObjectRaw `gorm:"column:execution;type:jsonb;not null;default:'{}'"`
	Horizons  JSONObjectRaw `gorm:"column:horizons;type:jsonb;not null;default:'{}'"`
	ID        int64         `gorm:"primaryKey;autoIncrement"`
}

func (sessionStateRow) TableName() string { return "agent_session_state" }

type projectStateRow struct {
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;autoUpdateTime"`
	DeadlineDate *time.Time `gorm:"column:deadline_date"`
	Project      string     `gorm:"column:project;not null;uniqueIndex"`
	Phase        string     `gorm:"column:phase;not null;default:''"`
	Pressure     string     `gorm:"column:pressure;not null;default:''"`
	UpdatedBy    string     `gorm:"column:updated_by;not null;default:'agent'"`
	ID           int64      `gorm:"primaryKey;autoIncrement"`
}

func (projectStateRow) TableName() string { return "agent_project_state" }

// StateStore persists Engram-native handoff state in dedicated state-plane
// tables. It is intentionally separate from MemoryStore: state rows are not
// ordinary recall memories and should not enter hot-memory retrieval ranking.
type StateStore struct {
	db         *gorm.DB
	auditStore *AuditStore
}

// NewStateStore creates a PostgreSQL-backed native state store.
func NewStateStore(db *gorm.DB, auditStore *AuditStore) *StateStore {
	return &StateStore{db: db, auditStore: auditStore}
}

// WriteSessionState upserts the structured handoff slots for a session.
func (s *StateStore) WriteSessionState(ctx context.Context, sessionID string, state cognitive.SessionStateSlots) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if sessionID == "" {
		return fmt.Errorf("state_store write_session: session_id is required")
	}
	focus, err := marshalJSONObject(state.Focus)
	if err != nil {
		return fmt.Errorf("state_store write_session focus: %w", err)
	}
	execution, err := marshalJSONObject(state.Execution)
	if err != nil {
		return fmt.Errorf("state_store write_session execution: %w", err)
	}
	horizons, err := marshalJSONObject(state.Horizons)
	if err != nil {
		return fmt.Errorf("state_store write_session horizons: %w", err)
	}

	now := time.Now().UTC()
	row := &sessionStateRow{
		SessionID: sessionID,
		Focus:     focus,
		Execution: execution,
		Horizons:  horizons,
		UpdatedAt: now,
	}
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "session_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"focus":      focus,
			"execution":  execution,
			"horizons":   horizons,
			"updated_at": now,
		}),
	}).Create(row).Error
	if err != nil {
		return fmt.Errorf("state_store write_session %q: %w", sessionID, err)
	}

	s.logAuditAsync("write_session_state", "agent", sessionID, "session state updated", state)
	return nil
}

// ReadSessionState returns the latest native handoff slots for a session.
func (s *StateStore) ReadSessionState(ctx context.Context, sessionID string) (cognitive.SessionStateSlots, error) {
	if err := s.requireDB(); err != nil {
		return cognitive.SessionStateSlots{}, err
	}
	if sessionID == "" {
		return cognitive.SessionStateSlots{}, fmt.Errorf("state_store read_session: session_id is required")
	}
	var row sessionStateRow
	if err := s.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&row).Error; err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("state_store read_session %q: %w", sessionID, err)
	}
	focus, err := unmarshalJSONObject(row.Focus)
	if err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("state_store read_session focus %q: %w", sessionID, err)
	}
	execution, err := unmarshalJSONObject(row.Execution)
	if err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("state_store read_session execution %q: %w", sessionID, err)
	}
	horizons, err := unmarshalJSONObject(row.Horizons)
	if err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("state_store read_session horizons %q: %w", sessionID, err)
	}
	return cognitive.SessionStateSlots{
		Focus:     focus,
		Execution: execution,
		Horizons:  horizons,
	}, nil
}

// WriteProjectState upserts the canonical native state for a project.
func (s *StateStore) WriteProjectState(ctx context.Context, project string, state cognitive.ProjectStateRecord) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if project == "" {
		return fmt.Errorf("state_store write_project: project is required")
	}
	updatedBy := state.UpdatedBy
	if updatedBy == "" {
		updatedBy = "agent"
	}
	now := time.Now().UTC()
	row := &projectStateRow{
		Project:      project,
		Phase:        state.Phase,
		DeadlineDate: state.DeadlineDate,
		Pressure:     state.Pressure,
		UpdatedBy:    updatedBy,
		UpdatedAt:    now,
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"phase":         state.Phase,
			"deadline_date": state.DeadlineDate,
			"pressure":      state.Pressure,
			"updated_by":    updatedBy,
			"updated_at":    now,
		}),
	}).Create(row).Error
	if err != nil {
		return fmt.Errorf("state_store write_project %q: %w", project, err)
	}

	auditState := state
	auditState.UpdatedBy = updatedBy
	s.logAuditAsync("write_project_state", updatedBy, "", fmt.Sprintf("project state updated for %s", project), auditState)
	return nil
}

// ReadProjectState returns the latest native handoff state for a project.
func (s *StateStore) ReadProjectState(ctx context.Context, project string) (cognitive.ProjectStateRecord, error) {
	if err := s.requireDB(); err != nil {
		return cognitive.ProjectStateRecord{}, err
	}
	if project == "" {
		return cognitive.ProjectStateRecord{}, fmt.Errorf("state_store read_project: project is required")
	}
	var row projectStateRow
	if err := s.db.WithContext(ctx).Where("project = ?", project).First(&row).Error; err != nil {
		return cognitive.ProjectStateRecord{}, fmt.Errorf("state_store read_project %q: %w", project, err)
	}
	return cognitive.ProjectStateRecord{
		Phase:        row.Phase,
		DeadlineDate: row.DeadlineDate,
		Pressure:     row.Pressure,
		UpdatedBy:    row.UpdatedBy,
	}, nil
}

func (s *StateStore) requireDB() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("state_store: db is not initialized")
	}
	return nil
}

func marshalJSONObject(value map[string]interface{}) (JSONObjectRaw, error) {
	if value == nil {
		value = map[string]interface{}{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(data) == "null" {
		return JSONObjectRaw(`{}`), nil
	}
	return JSONObjectRaw(data), nil
}

func unmarshalJSONObject(raw JSONObjectRaw) (map[string]interface{}, error) {
	if len(raw) == 0 {
		return map[string]interface{}{}, nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	return out, nil
}

func (s *StateStore) logAuditAsync(action, actor, sourceSessionID, reason string, afterState interface{}) {
	if s == nil || s.auditStore == nil {
		return
	}
	if actor == "" {
		actor = "agent"
	}
	var after *json.RawMessage
	if afterState != nil {
		if data, err := json.Marshal(afterState); err == nil {
			msg := json.RawMessage(data)
			after = &msg
		}
	}
	entry := AuditLogEntry{
		Action:          action,
		Actor:           actor,
		SourceSessionID: sourceSessionID,
		AfterState:      after,
		Reason:          reason,
	}
	auditStore := s.auditStore
	go func() {
		defer func() { _ = recover() }()
		auditCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = auditStore.Log(auditCtx, entry)
	}()
}
