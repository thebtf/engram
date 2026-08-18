// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"database/sql"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// GORM Models

// Note: JSON types (JSONStringArray, JSONInt64Map) are imported from pkg/models
// and already implement sql.Scanner and driver.Valuer interfaces.

// SDKSession represents a Claude Code session.
// sdk_sessions is the primary lifecycle table: one row per Claude Code process
// invocation. ClaudeSessionID and SDKSessionID each have unique indexes; the
// former is set immediately on session-start, the latter arrives with the first
// hook event and may be null for short-lived sessions.
type SDKSession struct {
	ClaudeSessionID     string         `gorm:"uniqueIndex;not null"`
	Project             string         `gorm:"index;not null"`
	Status              string         `gorm:"type:text;check:status IN ('active', 'completed', 'failed');default:'active';index"`
	StartedAt           string         `gorm:"not null"`
	SDKSessionID        sql.NullString `gorm:"uniqueIndex"`
	UserPrompt          sql.NullString
	CompletedAt         sql.NullString
	WorkerPort          sql.NullInt64
	CompletedAtEpoch    sql.NullInt64
	Outcome             sql.NullString `gorm:"type:text"`
	OutcomeReason       sql.NullString `gorm:"type:text"`
	OutcomeRecordedAt   sql.NullString `gorm:"type:timestamptz"`
	UtilityPropagatedAt sql.NullTime   `gorm:"type:timestamptz"`
	InjectionStrategy   sql.NullString `gorm:"type:text"`
	ID                  int64          `gorm:"primaryKey;autoIncrement"`
	PromptCounter       int            `gorm:"default:0"`
	StartedAtEpoch      int64          `gorm:"index:idx_sessions_started,sort:desc;not null"`
}

func (SDKSession) TableName() string { return "sdk_sessions" }

// BeforeCreate hook to ensure timestamps are set.
func (s *SDKSession) BeforeCreate(tx *gorm.DB) error {
	if s.StartedAtEpoch == 0 {
		s.StartedAtEpoch = time.Now().UnixMilli()
	}
	if s.StartedAt == "" {
		s.StartedAt = time.Now().Format(time.RFC3339)
	}
	return nil
}

// ObservationConflict, ObservationRelation, and ConceptWeight structs were
// removed from this package in CR-2b of provenance-cleanup; their tables
// (observation_conflicts, observation_relations, concept_weights) are dropped
// by migration 137. The pkg/models counterparts (ObservationConflict,
// ObservationRelation) were subsequently removed in FR-4 (contract honesty)
// as orphaned structs with no production callers.
// ReasoningTrace and ObservationVersion were similarly removed in CR-2a.

// Content holds deduplicated document bodies keyed by SHA-256 hash.
// The hash is computed by the ingestion layer; GORM does not generate it.
// Deduplication: two documents with identical content share one Content row.
type Content struct {
	Hash      string    `gorm:"primaryKey;type:text" json:"hash"`
	Doc       string    `gorm:"type:text;not null" json:"doc"`
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// TableName returns the table name for Content.
func (Content) TableName() string { return "content" }

// Document represents an ingested file in a collection.
// The (collection, path) pair is unique: re-ingesting the same file updates
// the Hash field and triggers re-embedding downstream.
type Document struct {
	ID         int64          `gorm:"primaryKey;autoIncrement" json:"id"`
	Collection string         `gorm:"type:text;not null;uniqueIndex:idx_doc_collection_path" json:"collection"`
	Path       string         `gorm:"type:text;not null;uniqueIndex:idx_doc_collection_path" json:"path"`
	Title      sql.NullString `gorm:"type:text" json:"title"`
	Hash       sql.NullString `gorm:"type:text" json:"hash"`
	Active     bool           `gorm:"default:true" json:"active"`
	CreatedAt  time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt  time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName returns the table name for Document.
func (Document) TableName() string { return "documents" }

// ContentChunk is the legacy hash-based chunk shell. The original content_chunks
// table (hash+seq) was dropped in migration 085; migration 108 restored a
// content_chunks table with a DIFFERENT schema (memory_id FK, not hash), so this
// hash-based type no longer mirrors the live table. It is retained only so
// document_store.go compiles; new code uses the mig-108 schema.
type ContentChunk struct {
	Hash      string    `gorm:"type:text;not null;primaryKey" json:"hash"`
	Seq       int       `gorm:"primaryKey" json:"seq"`
	Text      string    `gorm:"type:text;not null;default:''" json:"text"`
	Pos       int       `gorm:"not null" json:"pos"`
	Model     string    `gorm:"type:text;not null" json:"model"`
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// TableName returns the table name for ContentChunk.
func (ContentChunk) TableName() string { return "content_chunks" }

// TelemetrySnapshot stores periodic telemetry measurements.
// idx_telemetry_type_time is a composite index on (snapshot_type, created_at_epoch DESC)
// enabling efficient "latest N snapshots of type X" range scans.
type TelemetrySnapshot struct {
	ID             int64  `gorm:"primaryKey;autoIncrement"`
	SnapshotType   string `gorm:"type:text;not null;index:idx_telemetry_type_time,priority:1"`
	Project        string `gorm:"type:text;not null;default:''"`
	Data           string `gorm:"type:jsonb;not null"`
	CreatedAtEpoch int64  `gorm:"not null;index:idx_telemetry_type_time,priority:2,sort:desc"`
}

func (TelemetrySnapshot) TableName() string { return "telemetry_snapshots" }

// Project represents a repository's stable identity record for cross-platform project ID resolution.
// Maps a canonical git-remote-based project ID to optional legacy path-based aliases,
// enabling zero-downtime migration when clients upgrade to git-remote IDs.
type Project struct {
	GitRemote    sql.NullString `gorm:"column:git_remote;index"`
	RelativePath sql.NullString `gorm:"column:relative_path"`
	DisplayName  sql.NullString `gorm:"column:display_name"`
	// RemovedAt marks the project as soft-deleted. NULL means live. Set by DELETE /api/projects/{id}.
	RemovedAt *time.Time `gorm:"column:removed_at;default:null"`
	// LastHeartbeat records the last SyncProjectState call from a daemon tracking this project.
	LastHeartbeat *time.Time     `gorm:"column:last_heartbeat"`
	LegacyIDs     pq.StringArray `gorm:"column:legacy_ids;type:text[]"`
	ID            string         `gorm:"primaryKey"`
	CreatedAt     time.Time      `gorm:"autoCreateTime"`
}

func (Project) TableName() string { return "projects" }

// APIToken represents a client API token for agent authentication.
// Tokens are stored as bcrypt hashes with a prefix for fast lookup.
type APIToken struct {
	ID            string     `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name          string     `gorm:"type:text;not null;uniqueIndex"`
	TokenHash     string     `gorm:"type:text;not null"`
	TokenPrefix   string     `gorm:"type:text;not null;index"`
	Scope         string     `gorm:"type:text;not null;default:read-write"`
	Principal     string     `gorm:"type:text;not null;default:''" json:"principal"`
	PrincipalKind string     `gorm:"type:text;not null;default:'human'" json:"principal_kind"`
	CreatedAt     time.Time  `gorm:"not null;default:now()"`
	LastUsedAt    *time.Time `gorm:"column:last_used_at"`
	RequestCount  int64      `gorm:"not null;default:0"`
	ErrorCount    int64      `gorm:"not null;default:0"`
	Revoked       bool       `gorm:"not null;default:false"`
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
}

func (APIToken) TableName() string { return "api_tokens" }

// (ReasoningTrace struct removed in CR-2a of provenance-cleanup: its store and all
// readers/writers are gone. The reasoning_traces table — created by migration 065 via
// raw SQL, not AutoMigrate — stays live until its CR-3 DROP migration.)

// Issue represents a cross-project issue filed by an agent.
// Lifecycle: open → acknowledged → resolved ⟲ reopened
type Issue struct {
	ID               int64                  `gorm:"primaryKey;autoIncrement" json:"id"`
	Title            string                 `gorm:"type:text;not null" json:"title"`
	Body             string                 `gorm:"type:text" json:"body"`
	Status           string                 `gorm:"type:text;not null;default:'open';check:status IN ('open','acknowledged','resolved','reopened');index:idx_issues_target_status,priority:2" json:"status"`
	Priority         string                 `gorm:"type:text;not null;default:'medium';check:priority IN ('critical','high','medium','low')" json:"priority"`
	Type             string                 `gorm:"type:text;not null;default:task" json:"type"`
	SourceProject    string                 `gorm:"type:text;not null;index:idx_issues_source_project" json:"source_project"`
	TargetProject    string                 `gorm:"type:text;not null;index:idx_issues_target_status,priority:1" json:"target_project"`
	SourceAgent      string                 `gorm:"type:text" json:"source_agent"`
	CreatedBySession string                 `gorm:"type:text" json:"created_by_session"`
	CreatorKeycardID string                 `gorm:"type:text" json:"-"`
	Labels           models.JSONStringArray `gorm:"type:jsonb;default:'[]'" json:"labels"`
	AcknowledgedAt   *time.Time             `gorm:"type:timestamptz" json:"acknowledged_at"`
	ResolvedAt       *time.Time             `gorm:"type:timestamptz" json:"resolved_at"`
	ReopenedAt       *time.Time             `gorm:"type:timestamptz" json:"reopened_at"`
	ClosedAt         *time.Time             `gorm:"type:timestamptz" json:"closed_at"`
	CreatedAt        time.Time              `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt        time.Time              `gorm:"type:timestamptz;not null;default:now()" json:"updated_at"`
}

func (Issue) TableName() string { return "issues" }

// IssueComment represents a comment on an issue, enabling dialogue between agents.
type IssueComment struct {
	ID            int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	IssueID       int64     `gorm:"not null;index:idx_issue_comments_issue_created,priority:1" json:"issue_id"`
	AuthorProject string    `gorm:"type:text;not null" json:"author_project"`
	AuthorAgent   string    `gorm:"type:text" json:"author_agent"`
	Body          string    `gorm:"type:text;not null" json:"body"`
	CreatedAt     time.Time `gorm:"type:timestamptz;not null;default:now();index:idx_issue_comments_issue_created,priority:2" json:"created_at"`
}

func (IssueComment) TableName() string { return "issue_comments" }

// Credential represents a vault-stored encrypted credential.
// Created by migration 087 as a dedicated static-entity table.
// Pre-v5: credentials lived as rows in observations (type='credential').
// Post-v5 (US3): credentials are migrated here and observations is dropped.
type Credential struct {
	Project                  string     `gorm:"type:text;not null;index:idx_credentials_project,where:deleted_at IS NULL;uniqueIndex:idx_credentials_project_key,priority:1" json:"project"`
	Key                      string     `gorm:"type:text;not null;uniqueIndex:idx_credentials_project_key,priority:2" json:"key"`
	EncryptedSecret          []byte     `gorm:"type:bytea;not null" json:"encrypted_secret"`
	EncryptionKeyFingerprint string     `gorm:"type:text;not null;index:idx_credentials_fingerprint,where:deleted_at IS NULL" json:"encryption_key_fingerprint"`
	Scope                    string     `gorm:"type:text" json:"scope,omitempty"`
	EditedBy                 string     `gorm:"type:text" json:"edited_by,omitempty"`
	CreatedAt                time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt                time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"updated_at"`
	DeletedAt                *time.Time `gorm:"type:timestamptz" json:"deleted_at,omitempty"`
	ID                       int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	Version                  int        `gorm:"not null;default:1" json:"version"`
}

func (Credential) TableName() string { return "credentials" }

// ModelSetting is the GORM row struct for the model_settings table (migration 143).
// It is a server-global key/value store for swappable model configuration (reranker /
// embedder URLs, model names, and API keys) — the #259 settings-store foundation.
//
// Deliberate differences from Credential (the mirrored pattern):
//   - No `project` column: model config is server-global, not per-project. The unique key
//     is `key` alone.
//   - `value` is plain text for non-secret config (URLs, model names); secret values
//     (API keys) are stored in `encrypted_value` (AES-256-GCM via the existing Vault) with
//     a non-empty `encryption_key_fingerprint`. Exactly one of value / encrypted_value is set,
//     indicated by the `encrypted` flag. This keeps non-secret config readable without the
//     vault key while protecting secrets with the same crypto as credentials.
type ModelSetting struct {
	Key                      string     `gorm:"type:text;not null;uniqueIndex:idx_model_settings_key,where:deleted_at IS NULL" json:"key"`
	Value                    string     `gorm:"type:text" json:"value,omitempty"`
	EncryptedValue           []byte     `gorm:"type:bytea" json:"encrypted_value,omitempty"`
	Encrypted                bool       `gorm:"not null;default:false" json:"encrypted"`
	EncryptionKeyFingerprint string     `gorm:"type:text" json:"encryption_key_fingerprint,omitempty"`
	Description              string     `gorm:"type:text" json:"description,omitempty"`
	EditedBy                 string     `gorm:"type:text" json:"edited_by,omitempty"`
	CreatedAt                time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt                time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"updated_at"`
	DeletedAt                *time.Time `gorm:"type:timestamptz" json:"deleted_at,omitempty"`
	ID                       int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	Version                  int        `gorm:"not null;default:1" json:"version"`
}

func (ModelSetting) TableName() string { return "model_settings" }

// Memory is the GORM row struct for the memories table (migration 088 + 105 lifecycle).
// Tags are stored as JSONB using models.JSONStringArray.
// search_vector is a GENERATED ALWAYS AS STORED column — it must NOT appear in INSERT/UPDATE
// statements.  GORM will only write columns that are present in the struct, so omitting the
// search_vector field here is the correct approach.
type Memory struct {
	Project         string                 `gorm:"type:text;not null;index:idx_memories_project_created,priority:1,where:deleted_at IS NULL" json:"project"`
	Content         string                 `gorm:"type:text;not null" json:"content"`
	Tags            models.JSONStringArray `gorm:"type:jsonb;not null;default:'[]'" json:"tags"`
	SourceAgent     string                 `gorm:"type:text" json:"source_agent,omitempty"`
	EditedBy        string                 `gorm:"type:text" json:"edited_by,omitempty"`
	Status          string                 `gorm:"type:text;default:'active'" json:"status"`
	Tier            string                 `gorm:"type:text;default:'episodic'" json:"tier"`
	EpistemicType   string                 `gorm:"type:text;default:'observation'" json:"epistemic_type"`
	Defeasibility   string                 `gorm:"type:text;default:'slow'" json:"defeasibility"`
	PromotionTarget string                 `gorm:"type:text;default:'none'" json:"promotion_target"`
	// T002 + T001b + T003b (engram vNext Milestone F TG1): privacy metadata persisted
	// to migration-125 (privacy_scope + source_sessions[]) and migration-130
	// (source_workstation_id) columns. Without these fields, values set by the MCP
	// layer (tools_memory.go) are silently dropped on INSERT and rows come back with
	// empty privacy metadata on SELECT — codex P1 fix-forward on 3e4a4b1.
	PrivacyScope             string         `gorm:"type:text;not null;default:'project'" json:"privacy_scope"`
	SourceWorkstationID      string         `gorm:"type:text;not null;default:''" json:"source_workstation_id"`
	SourceSessions           pq.StringArray `gorm:"type:text[];not null;default:'{}'" json:"source_sessions"`
	OwnerPrincipal           string         `gorm:"type:text;not null;default:''" json:"owner_principal"`
	OwnerPrincipalKind       string         `gorm:"type:text;not null;default:''" json:"owner_principal_kind"`
	AgentVisibility          string         `gorm:"type:text;not null;default:''" json:"agent_visibility"`
	Domain                   string         `gorm:"type:text;not null;default:''" json:"domain"`
	CreatedAt                time.Time      `gorm:"type:timestamptz;not null;default:now();index:idx_memories_project_created,priority:2,sort:desc" json:"created_at"`
	UpdatedAt                time.Time      `gorm:"type:timestamptz;not null;default:now()" json:"updated_at"`
	DeletedAt                *time.Time     `gorm:"type:timestamptz" json:"deleted_at,omitempty"`
	LastRetrievedAt          *time.Time     `gorm:"type:timestamptz" json:"last_retrieved_at,omitempty"`
	LastConfirmed            *time.Time     `gorm:"type:timestamptz" json:"last_confirmed,omitempty"`
	ReviewAfter              *time.Time     `gorm:"type:timestamptz" json:"review_after,omitempty"`
	ValidFrom                *time.Time     `gorm:"type:timestamptz;default:now()" json:"valid_from,omitempty"`
	ValidUntil               *time.Time     `gorm:"type:timestamptz;default:'9999-12-31T23:59:59Z'" json:"valid_until,omitempty"`
	ID                       int64          `gorm:"primaryKey;autoIncrement" json:"id"`
	SupersedesID             *int64         `gorm:"type:bigint" json:"supersedes_id,omitempty"`
	SupersededBy             *int64         `gorm:"type:bigint" json:"superseded_by,omitempty"`
	ImportanceBase           float64        `gorm:"type:real;default:0.5" json:"importance_base"`
	TsAlpha                  float64        `gorm:"type:real;default:1.0" json:"ts_alpha"`
	TsBeta                   float64        `gorm:"type:real;default:1.0" json:"ts_beta"`
	Confidence               float64        `gorm:"type:real;default:0.5" json:"confidence"`
	Stability                float64        `gorm:"type:real;default:30.0" json:"stability"`
	Retrievability           float64        `gorm:"type:real;default:1.0" json:"retrievability"`
	Version                  int            `gorm:"not null;default:1" json:"version"`
	CitationCount            int            `gorm:"default:0" json:"citation_count"`
	InjectionCount           int            `gorm:"default:0" json:"injection_count"`
	AccessCount              int            `gorm:"default:0" json:"access_count"`
	RecurrenceCount          int            `gorm:"default:0" json:"recurrence_count"`
	ConsecutiveCitationCount int            `gorm:"default:0" json:"consecutive_citation_count"`
}

func (Memory) TableName() string { return "memories" }

// ContinuitySlot is the separately owned, one-per-project designation state.
// Authority fields are server-derived snapshots used for later clear authorization.
type ContinuitySlot struct {
	CreatedAt                   time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt                   time.Time `gorm:"column:updated_at;type:timestamptz;not null;default:now()" json:"updated_at"`
	ExpiresAt                   time.Time `gorm:"column:expires_at;type:timestamptz;not null;index:idx_project_continuity_slots_expires_at" json:"expires_at"`
	Project                     string    `gorm:"column:project;primaryKey;type:text" json:"project"`
	AuthorityDomain             string    `gorm:"column:authority_domain;type:text;not null" json:"authority_domain"`
	AuthorityOwnerPrincipal     string    `gorm:"column:authority_owner_principal;type:text;not null" json:"authority_owner_principal"`
	AuthorityOwnerPrincipalKind string    `gorm:"column:authority_owner_principal_kind;type:text;not null" json:"authority_owner_principal_kind"`
	MemoryID                    int64     `gorm:"column:memory_id;type:bigint;not null;index:idx_project_continuity_slots_memory_id" json:"memory_id"`
}

func (ContinuitySlot) TableName() string { return "project_continuity_slots" }

// DomainOwner is the operator-managed registry row that decides who owns a
// memory domain and whether cross-owner writes are allowed, warned, or rejected.
type DomainOwner struct {
	CreatedAt          time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at;type:timestamptz;not null;default:now()" json:"updated_at"`
	Domain             string    `gorm:"column:domain;primaryKey;type:text" json:"domain"`
	OwnerPrincipal     string    `gorm:"column:owner_principal;type:text;not null" json:"owner_principal"`
	OwnerPrincipalKind string    `gorm:"column:owner_principal_kind;type:text;not null;default:'human'" json:"owner_principal_kind"`
	Mode               string    `gorm:"column:mode;type:text;not null;default:'warn'" json:"mode"`
}

func (DomainOwner) TableName() string { return "memory_domain_owners" }

// BehavioralRule is the GORM row struct for the behavioral_rules table (migration 089).
// Project is a pointer because the column is NULLable: NULL = global rule.
type BehavioralRule struct {
	Project   *string    `gorm:"type:text" json:"project,omitempty"`
	Content   string     `gorm:"type:text;not null" json:"content"`
	EditedBy  string     `gorm:"type:text" json:"edited_by,omitempty"`
	CreatedAt time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
	UpdatedAt time.Time  `gorm:"type:timestamptz;not null;default:now()" json:"updated_at"`
	DeletedAt *time.Time `gorm:"type:timestamptz" json:"deleted_at,omitempty"`
	ID        int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	Priority  int        `gorm:"not null;default:0" json:"priority"`
	Version   int        `gorm:"not null;default:1" json:"version"`
	Enabled   bool       `gorm:"not null;default:true" json:"enabled"`
}

func (BehavioralRule) TableName() string { return "behavioral_rules" }
