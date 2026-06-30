package gorm

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	state, err := sessionStateFromRow(row)
	if err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("state_store read_session %q: %w", sessionID, err)
	}
	return state, nil
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
	if updatedBy != "agent" {
		return fmt.Errorf("state_store write_project: updated_by must be agent")
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

// ReadResumePacket builds one bounded native resume packet from persisted state.
func (s *StateStore) ReadResumePacket(ctx context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	request.Principal = strings.TrimSpace(request.Principal)
	request.SessionID = strings.TrimSpace(request.SessionID)
	if request.Principal == "" {
		return cognitive.ResumePacket{}, fmt.Errorf("state_store read_resume: principal is required")
	}
	if request.SessionID == "" {
		return cognitive.ResumePacket{}, fmt.Errorf("state_store read_resume: session_id is required")
	}
	if err := s.requireDB(); err != nil {
		return cognitive.ResumePacket{}, err
	}

	var row sessionStateRow
	if err := s.db.WithContext(ctx).Where("session_id = ?", request.SessionID).First(&row).Error; err != nil {
		return cognitive.ResumePacket{}, fmt.Errorf("state_store read_session %q: %w", request.SessionID, err)
	}
	sessionState, err := sessionStateFromRow(row)
	if err != nil {
		return cognitive.ResumePacket{}, fmt.Errorf("state_store read_resume session %q: %w", request.SessionID, err)
	}
	nextAction, err := stateActionFromSlots(sessionState)
	if err != nil {
		return cognitive.ResumePacket{}, err
	}
	nextVerification, err := stateVerificationFromSlots(sessionState)
	if err != nil {
		return cognitive.ResumePacket{}, err
	}

	scopes := []cognitive.StateScopeKind{cognitive.StateScopeSession}
	if request.Project != "" {
		if _, err := s.ReadProjectState(ctx, request.Project); err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return cognitive.ResumePacket{}, err
			}
		} else {
			scopes = append(scopes, cognitive.StateScopeProject)
		}
	}

	now := time.Now().UTC()
	stateVersion := stateVersionFromTime(row.UpdatedAt)
	evidenceRefs := stateEvidenceRefsFromSlots(sessionState, request.SessionID, stateVersion)
	return cognitive.ResumePacket{
		PacketID:         resumePacketID(request, stateVersion),
		Project:          request.Project,
		Principal:        request.Principal,
		SessionID:        request.SessionID,
		StateVersion:     stateVersion,
		Source:           cognitive.StatePacketSourceNative,
		FallbackUsed:     false,
		Freshness:        cognitive.StateFreshnessFresh,
		Drift:            cognitive.StateDrift{Kind: cognitive.StateDriftNone, CheckedAt: now},
		NextAction:       nextAction,
		NextVerification: nextVerification,
		GeneratedAt:      now,
		EvidenceRefs:     evidenceRefs,
		GoalID:           request.GoalID,
		TaskID:           request.TaskID,
		Scopes:           scopes,
	}, nil
}

func sessionStateFromRow(row sessionStateRow) (cognitive.SessionStateSlots, error) {
	focus, err := unmarshalJSONObject(row.Focus)
	if err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("focus: %w", err)
	}
	execution, err := unmarshalJSONObject(row.Execution)
	if err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("execution: %w", err)
	}
	horizons, err := unmarshalJSONObject(row.Horizons)
	if err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("horizons: %w", err)
	}
	return cognitive.SessionStateSlots{
		Focus:     focus,
		Execution: execution,
		Horizons:  horizons,
	}, nil
}

func resumePacketID(request cognitive.ResumePacketRequest, stateVersion string) string {
	parts := []string{"resume", request.Project, request.Principal, request.SessionID, stateVersion}
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return strings.Join(parts, ":")
}

func stateVersionFromTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func stateEvidenceRefsFromSlots(state cognitive.SessionStateSlots, sessionID, stateVersion string) []string {
	refs := make([]string, 0, 8)
	seen := make(map[string]bool, 8)
	appendRef := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	appendRef(fmt.Sprintf("agent_session_state:%s@%s", strings.TrimSpace(sessionID), strings.TrimSpace(stateVersion)))
	for _, values := range []map[string]interface{}{state.Horizons, state.Execution, state.Focus} {
		for _, ref := range parseStateStringSlice(values["evidence_refs"]) {
			appendRef(ref)
		}
	}
	return refs
}

func parseStateStringSlice(value interface{}) []string {
	switch v := value.(type) {
	case []string:
		return cleanStateStrings(v)
	case []interface{}:
		items := make([]string, 0, len(v))
		for _, item := range v {
			items = append(items, fmt.Sprint(item))
		}
		return cleanStateStrings(items)
	case string:
		return cleanStateStrings([]string{v})
	default:
		return []string{}
	}
}

func cleanStateStrings(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func (s *StateStore) requireDB() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("state_store: db is not initialized")
	}
	return nil
}

func stateActionFromSlots(state cognitive.SessionStateSlots) (cognitive.StateAction, error) {
	value, ok := state.Execution["next_action"]
	if !ok {
		return cognitive.StateAction{}, fmt.Errorf("state_store read_resume: execution.next_action is required")
	}
	return parseStateAction(value)
}

func stateVerificationFromSlots(state cognitive.SessionStateSlots) (cognitive.StateVerification, error) {
	value, ok := state.Horizons["next_verification"]
	if !ok {
		return cognitive.StateVerification{}, fmt.Errorf("state_store read_resume: horizons.next_verification is required")
	}
	return parseStateVerification(value)
}

func parseStateAction(value interface{}) (cognitive.StateAction, error) {
	switch v := value.(type) {
	case cognitive.StateAction:
		if strings.TrimSpace(v.Description) == "" {
			return cognitive.StateAction{}, fmt.Errorf("state action description is required")
		}
		if v.Kind == "" {
			v.Kind = inferActionKind(v.Command)
		}
		if !validActionKind(v.Kind) {
			return cognitive.StateAction{}, fmt.Errorf("state action kind %q is invalid", v.Kind)
		}
		return v, nil
	case map[string]interface{}:
		action := cognitive.StateAction{
			Kind:        inferActionKind(mapString(v, "command")),
			Description: mapString(v, "description"),
			Command:     mapString(v, "command"),
		}
		if kind := mapString(v, "kind"); kind != "" {
			action.Kind = cognitive.StateActionKind(kind)
		}
		if strings.TrimSpace(action.Description) == "" {
			return cognitive.StateAction{}, fmt.Errorf("state action description is required")
		}
		if !validActionKind(action.Kind) {
			return cognitive.StateAction{}, fmt.Errorf("state action kind %q is invalid", action.Kind)
		}
		return action, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return cognitive.StateAction{}, fmt.Errorf("state action description is required")
		}
		return cognitive.StateAction{Kind: cognitive.StateActionInstruction, Description: v}, nil
	default:
		return cognitive.StateAction{}, fmt.Errorf("state action has unsupported type %T", value)
	}
}

func parseStateVerification(value interface{}) (cognitive.StateVerification, error) {
	switch v := value.(type) {
	case cognitive.StateVerification:
		if strings.TrimSpace(v.Description) == "" {
			return cognitive.StateVerification{}, fmt.Errorf("state verification description is required")
		}
		if v.Kind == "" {
			v.Kind = inferVerificationKind(v.Command)
		}
		if !validVerificationKind(v.Kind) {
			return cognitive.StateVerification{}, fmt.Errorf("state verification kind %q is invalid", v.Kind)
		}
		return v, nil
	case map[string]interface{}:
		verification := cognitive.StateVerification{
			Kind:        inferVerificationKind(mapString(v, "command")),
			Description: mapString(v, "description"),
			Command:     mapString(v, "command"),
		}
		if kind := mapString(v, "kind"); kind != "" {
			verification.Kind = cognitive.StateVerificationKind(kind)
		}
		if strings.TrimSpace(verification.Description) == "" {
			return cognitive.StateVerification{}, fmt.Errorf("state verification description is required")
		}
		if !validVerificationKind(verification.Kind) {
			return cognitive.StateVerification{}, fmt.Errorf("state verification kind %q is invalid", verification.Kind)
		}
		return verification, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return cognitive.StateVerification{}, fmt.Errorf("state verification description is required")
		}
		return cognitive.StateVerification{Kind: cognitive.StateVerificationManual, Description: v}, nil
	default:
		return cognitive.StateVerification{}, fmt.Errorf("state verification has unsupported type %T", value)
	}
}

func mapString(values map[string]interface{}, key string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func inferActionKind(command string) cognitive.StateActionKind {
	if strings.TrimSpace(command) != "" {
		return cognitive.StateActionCommand
	}
	return cognitive.StateActionInstruction
}

func inferVerificationKind(command string) cognitive.StateVerificationKind {
	if strings.TrimSpace(command) != "" {
		return cognitive.StateVerificationCommand
	}
	return cognitive.StateVerificationManual
}

func validActionKind(kind cognitive.StateActionKind) bool {
	switch kind {
	case cognitive.StateActionCommand, cognitive.StateActionInstruction, cognitive.StateActionReviewGate:
		return true
	default:
		return false
	}
}

func validVerificationKind(kind cognitive.StateVerificationKind) bool {
	switch kind {
	case cognitive.StateVerificationCommand, cognitive.StateVerificationArtifact, cognitive.StateVerificationManual:
		return true
	default:
		return false
	}
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
