// Package models contains domain models for engram.
package models

import "time"

// Memory represents a user-facing persistent note stored in the memories table.
// Memories are project-scoped and support full-text search via a GENERATED tsvector column
// (search_vector) that is NOT exposed here — it is a read-only computed column managed by
// the database and not part of the domain interface.
//
// Migration 088 creates this table; migration 105 adds lifecycle columns for vNext Milestone A;
// migration 110 adds lifecycle metadata for Milestone B.
type Memory struct {
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	LastRetrievedAt *time.Time `json:"last_retrieved_at,omitempty"`
	LastConfirmed   *time.Time `json:"last_confirmed,omitempty"`
	ReviewAfter     *time.Time `json:"review_after,omitempty"`
	ValidFrom       *time.Time `json:"valid_from,omitempty"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
	Project         string     `json:"project"`
	Content         string     `json:"content"`
	SourceAgent     string     `json:"source_agent,omitempty"`
	EditedBy        string     `json:"edited_by,omitempty"`
	Status          string     `json:"status,omitempty"`
	Tier            string     `json:"tier,omitempty"`
	EpistemicType   string     `json:"epistemic_type,omitempty"`
	Defeasibility   string     `json:"defeasibility,omitempty"`
	PromotionTarget string     `json:"promotion_target,omitempty"`
	Tags            []string   `json:"tags"`
	ID              int64      `json:"id"`
	SupersedesID    *int64     `json:"supersedes_id,omitempty"`
	SupersededBy    *int64     `json:"superseded_by,omitempty"`
	ImportanceBase  float64    `json:"importance_base,omitempty"`
	TsAlpha         float64    `json:"ts_alpha,omitempty"`
	TsBeta          float64    `json:"ts_beta,omitempty"`
	Confidence      float64    `json:"confidence,omitempty"`
	Stability       float64    `json:"stability,omitempty"`
	Retrievability  float64    `json:"retrievability,omitempty"`
	Version         int        `json:"version"`
	CitationCount   int        `json:"citation_count,omitempty"`
	InjectionCount  int        `json:"injection_count,omitempty"`
	AccessCount     int        `json:"access_count,omitempty"`
	RecurrenceCount int        `json:"recurrence_count,omitempty"`
}
