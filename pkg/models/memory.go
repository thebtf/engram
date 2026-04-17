// Package models contains domain models for engram.
package models

import "time"

// Memory represents a user-facing persistent note stored in the memories table.
// Memories are project-scoped and support full-text search via a GENERATED tsvector column
// (search_vector) that is NOT exposed here — it is a read-only computed column managed by
// the database and not part of the domain interface.
//
// Migration 088 creates this table; migration 080 populates it from observations.
type Memory struct {
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
	Project     string     `json:"project"`
	Content     string     `json:"content"`
	SourceAgent string     `json:"source_agent,omitempty"`
	EditedBy    string     `json:"edited_by,omitempty"`
	Tags        []string   `json:"tags"`
	ID          int64      `json:"id"`
	Version     int        `json:"version"`
}
