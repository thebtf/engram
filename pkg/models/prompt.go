// Package models contains domain models for engram.
package models

// UserPrompt represents a user prompt captured during a session.
// The user_prompts table was dropped in v5 (US3); this type is retained
// for test hook injection and in-memory pipeline usage only.
// Do not add persistence logic here — the table no longer exists.
type UserPrompt struct {
	ClaudeSessionID     string `db:"claude_session_id" json:"claude_session_id"`
	PromptText          string `db:"prompt_text" json:"prompt_text"`
	CreatedAt           string `db:"created_at" json:"created_at"`
	ID                  int64  `db:"id" json:"id"`
	PromptNumber        int    `db:"prompt_number" json:"prompt_number"`
	MatchedObservations int    `db:"matched_observations" json:"matched_observations"`
	CreatedAtEpoch      int64  `db:"created_at_epoch" json:"created_at_epoch"`
}

// UserPromptWithSession extends UserPrompt with the project and SDK session context
// needed when surfacing prompts in multi-project search results.
// Retained for the same reason as UserPrompt — in-memory pipeline only.
type UserPromptWithSession struct {
	Project      string `db:"project" json:"project"`
	SDKSessionID string `db:"sdk_session_id" json:"sdk_session_id"`
	UserPrompt
}
