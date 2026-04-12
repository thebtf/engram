// Package models contains domain models for engram.
package models

import (
	"database/sql"
	"encoding/json"
	"time"
)

// SessionStatus represents the status of an SDK session.
type SessionStatus string

const (
	SessionStatusActive    SessionStatus = "active"
	SessionStatusCompleted SessionStatus = "completed"
	SessionStatusFailed    SessionStatus = "failed"
)

// SDKSession represents a Claude Code session tracked by the memory system.
type SDKSession struct {
	ClaudeSessionID  string         `db:"claude_session_id" json:"claude_session_id"`
	Project          string         `db:"project" json:"project"`
	Status           SessionStatus  `db:"status" json:"status"`
	StartedAt        string         `db:"started_at" json:"started_at"`
	SDKSessionID     sql.NullString `db:"sdk_session_id" json:"sdk_session_id,omitempty"`
	UserPrompt       sql.NullString `db:"user_prompt" json:"user_prompt,omitempty"`
	CompletedAt      sql.NullString `db:"completed_at" json:"completed_at,omitempty"`
	WorkerPort          sql.NullInt64  `db:"worker_port" json:"worker_port,omitempty"`
	CompletedAtEpoch    sql.NullInt64  `db:"completed_at_epoch" json:"completed_at_epoch,omitempty"`
	Outcome             sql.NullString `db:"outcome" json:"outcome,omitempty"`
	OutcomeReason       sql.NullString `db:"outcome_reason" json:"outcome_reason,omitempty"`
	OutcomeRecordedAt   sql.NullString `db:"outcome_recorded_at" json:"outcome_recorded_at,omitempty"`
	UtilityPropagatedAt sql.NullTime   `db:"utility_propagated_at" json:"utility_propagated_at,omitempty"`
	InjectionStrategy   sql.NullString `db:"injection_strategy" json:"injection_strategy,omitempty"`
	ID                  int64          `db:"id" json:"id"`
	PromptCounter       int64          `db:"prompt_counter" json:"prompt_counter"`
	StartedAtEpoch      int64          `db:"started_at_epoch" json:"started_at_epoch"`
}

// MarshalJSON implements json.Marshaler for SDKSession.
// It serializes utility_propagated_at as an RFC3339 string or omits it when null,
// instead of the default sql.NullTime wire format {"Time":"...","Valid":true}.
func (s SDKSession) MarshalJSON() ([]byte, error) {
	// shadow mirrors SDKSession fields but replaces UtilityPropagatedAt with *string.
	type shadow struct {
		ID                  int64          `json:"id"`
		ClaudeSessionID     string         `json:"claude_session_id"`
		Project             string         `json:"project"`
		Status              SessionStatus  `json:"status"`
		StartedAt           string         `json:"started_at"`
		StartedAtEpoch      int64          `json:"started_at_epoch"`
		PromptCounter       int64          `json:"prompt_counter"`
		SDKSessionID        sql.NullString `json:"sdk_session_id,omitempty"`
		UserPrompt          sql.NullString `json:"user_prompt,omitempty"`
		CompletedAt         sql.NullString `json:"completed_at,omitempty"`
		CompletedAtEpoch    sql.NullInt64  `json:"completed_at_epoch,omitempty"`
		WorkerPort          sql.NullInt64  `json:"worker_port,omitempty"`
		Outcome             sql.NullString `json:"outcome,omitempty"`
		OutcomeReason       sql.NullString `json:"outcome_reason,omitempty"`
		OutcomeRecordedAt   sql.NullString `json:"outcome_recorded_at,omitempty"`
		InjectionStrategy   sql.NullString `json:"injection_strategy,omitempty"`
		UtilityPropagatedAt *string        `json:"utility_propagated_at,omitempty"`
	}

	sh := shadow{
		ID:                s.ID,
		ClaudeSessionID:   s.ClaudeSessionID,
		Project:           s.Project,
		Status:            s.Status,
		StartedAt:         s.StartedAt,
		StartedAtEpoch:    s.StartedAtEpoch,
		PromptCounter:     s.PromptCounter,
		SDKSessionID:      s.SDKSessionID,
		UserPrompt:        s.UserPrompt,
		CompletedAt:       s.CompletedAt,
		CompletedAtEpoch:  s.CompletedAtEpoch,
		WorkerPort:        s.WorkerPort,
		Outcome:           s.Outcome,
		OutcomeReason:     s.OutcomeReason,
		OutcomeRecordedAt: s.OutcomeRecordedAt,
		InjectionStrategy: s.InjectionStrategy,
	}
	if s.UtilityPropagatedAt.Valid {
		t := s.UtilityPropagatedAt.Time.UTC().Format(time.RFC3339)
		sh.UtilityPropagatedAt = &t
	}
	return json.Marshal(sh)
}

// ActiveSession represents an in-memory active session being processed.
type ActiveSession struct {
	StartTime              time.Time
	ClaudeSessionID        string
	SDKSessionID           string
	Project                string
	UserPrompt             string
	SessionDBID            int64
	LastPromptNumber       int
	CumulativeInputTokens  int64
	CumulativeOutputTokens int64
}
