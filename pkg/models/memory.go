// Package models contains domain models for engram.
package models

import "time"

// Memory represents a user-facing persistent note stored in the memories table.
// Memories are project-scoped and support full-text search via a GENERATED tsvector column
// (search_vector) that is NOT exposed here — it is a read-only computed column managed by
// the database and not part of the domain interface.
//
// Migration 088 creates this table; migration 105 adds lifecycle columns for vNext Phase A.
type Memory struct {
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	LastRetrievedAt *time.Time `json:"last_retrieved_at,omitempty"`
	ValidFrom       *time.Time `json:"valid_from,omitempty"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
	Project         string     `json:"project"`
	Content         string     `json:"content"`
	SourceAgent     string     `json:"source_agent,omitempty"`
	EditedBy        string     `json:"edited_by,omitempty"`
	Status          string     `json:"status,omitempty"`
	Tags            []string   `json:"tags"`
	ID              int64      `json:"id"`
	SupersedesID    *int64     `json:"supersedes_id,omitempty"`
	ImportanceBase  float64    `json:"importance_base,omitempty"`
	TsAlpha         float64    `json:"ts_alpha,omitempty"`
	TsBeta          float64    `json:"ts_beta,omitempty"`
	Version         int        `json:"version"`
	CitationCount   int        `json:"citation_count,omitempty"`
	InjectionCount  int        `json:"injection_count,omitempty"`
	AccessCount     int        `json:"access_count,omitempty"`
}
