// Package models contains domain models for engram.
package models

import "time"

// BehavioralRule represents operator-controlled guidance stored in the behavioral_rules table.
// Enabled rules are injected at session-start; disabled rules remain visible to the operator
// so they can be reviewed and re-enabled without losing audit history.
//
// Project field is a pointer: nil means the rule is global (applies to every session regardless
// of project); non-nil means the rule applies only to the named project.
//
// Migration 089 creates this table; migration 080 populates it from observations with
// always_inject=true and type != 'credential'.
type BehavioralRule struct {
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	Project   *string    `json:"project,omitempty"`
	Content   string     `json:"content"`
	EditedBy  string     `json:"edited_by,omitempty"`
	ID        int64      `json:"id"`
	Priority  int        `json:"priority"`
	Version   int        `json:"version"`
	Enabled   bool       `json:"enabled"`
}
