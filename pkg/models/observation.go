// Package models contains domain models for engram.
package models

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ObservationType classifies the nature of a learning captured from a session.
// Values are stored in PostgreSQL and must match the CHECK constraint in migrations.
type ObservationType string

const (
	ObsTypeDecision    ObservationType = "decision"
	ObsTypeBugfix      ObservationType = "bugfix"
	ObsTypeFeature     ObservationType = "feature"
	ObsTypeRefactor    ObservationType = "refactor"
	ObsTypeDiscovery   ObservationType = "discovery"
	ObsTypeChange      ObservationType = "change"
	ObsTypeGuidance    ObservationType = "guidance"
	ObsTypeCredential  ObservationType = "credential"
	ObsTypeEntity      ObservationType = "entity"
	ObsTypeWiki        ObservationType = "wiki"
	ObsTypePitfall     ObservationType = "pitfall"
	ObsTypeOperational ObservationType = "operational"
	ObsTypeTimeline    ObservationType = "timeline"
)

// MemoryType classifies an observation for memory storage and retrieval routing.
// The retrieval layer uses this to bucket memories and weight them during injection.
type MemoryType string

const (
	MemTypeDecision   MemoryType = "decision"
	MemTypePattern    MemoryType = "pattern"
	MemTypePreference MemoryType = "preference"
	MemTypeStyle      MemoryType = "style"
	MemTypeHabit      MemoryType = "habit"
	MemTypeInsight    MemoryType = "insight"
	MemTypeContext    MemoryType = "context"
	MemTypeGuidance   MemoryType = "guidance"
)

var AllMemoryTypes = []MemoryType{
	MemTypeDecision,
	MemTypePattern,
	MemTypePreference,
	MemTypeStyle,
	MemTypeHabit,
	MemTypeInsight,
	MemTypeContext,
	MemTypeGuidance,
}

// SourceType records the provenance of an observation — which Claude Code tool
// produced or verified the data.  This lets the retrieval layer apply
// source-quality weighting (tool-verified > llm-derived, etc.).
type SourceType string

const (
	SourceToolVerified   SourceType = "tool_verified"
	SourceToolRead       SourceType = "tool_read"
	SourceWebFetch       SourceType = "web_fetch"
	SourceTodoWrite      SourceType = "todo_write"
	SourceLLMDerived     SourceType = "llm_derived"
	SourceInstinctImport SourceType = "instinct_import"
	SourceBackfill       SourceType = "backfill"
	SourceUnknown        SourceType = "unknown"
	SourceManual         SourceType = "manual"
	SourceCrossModel     SourceType = "cross_model"
)

// AgentSource records which AI tool created the observation.
// Stored so multi-model workstations can filter by the originating agent.
type AgentSource string

const (
	AgentClaude  AgentSource = "claude-code"
	AgentCodex   AgentSource = "codex"
	AgentGemini  AgentSource = "gemini"
	AgentOther   AgentSource = "other"
	AgentUnknown AgentSource = "unknown"
)

// ValidAgentSources is the authoritative list for validation — adding a new agent
// here is all that is required; no other file needs to change.
var ValidAgentSources = []AgentSource{
	AgentClaude,
	AgentCodex,
	AgentGemini,
	AgentOther,
	AgentUnknown,
}

// IsValidAgentSource returns true if s is a recognized AgentSource value.
func IsValidAgentSource(s string) bool {
	for _, v := range ValidAgentSources {
		if string(v) == s {
			return true
		}
	}
	return false
}

// ClassifySourceType maps a Claude Code tool name to its SourceType.
// Write/Edit tools provide the strongest provenance signal (tool_verified)
// because the agent confirmed the change was applied.
func ClassifySourceType(toolName string) SourceType {
	switch toolName {
	case "Edit", "Write", "Bash", "NotebookEdit":
		return SourceToolVerified
	case "Read", "Grep", "Glob", "LSP":
		return SourceToolRead
	case "WebFetch", "WebSearch":
		return SourceWebFetch
	case "TodoWrite", "TodoRead":
		return SourceTodoWrite
	default:
		return SourceUnknown
	}
}

// ObservationScope controls the visibility of an observation across projects.
type ObservationScope string

const (
	// ScopeProject: observation is only visible within the same project.
	ScopeProject ObservationScope = "project"
	// ScopeGlobal: observation is visible across all projects.
	// Used for best practices, advanced patterns, and generalizable knowledge.
	ScopeGlobal ObservationScope = "global"
	// ScopeAgent: observation is only visible to the specific agent that created it.
	// Used for per-agent private memory (e.g., Neuromancer, Jeeves).
	ScopeAgent ObservationScope = "agent"
)

// GlobalizableConcepts are the concept tags that trigger automatic global scope assignment
// in NewObservation.  ORDER IS LOAD-BEARING: NewObservation's DetermineScope call iterates
// this slice and returns on first match — do not reorder.
var GlobalizableConcepts = []string{
	"best-practice",
	"pattern",
	"anti-pattern",
	"architecture",
	"security",
	"performance",
	"testing",
	"debugging",
	"workflow",
	"tooling",
}

// JSONStringArray handles PostgreSQL jsonb columns that store string arrays.
// Both storage (Value) and retrieval (Scan) go through JSON encoding so the
// wire format is always a JSON array regardless of the PostgreSQL column type.
type JSONStringArray []string

// Scan implements sql.Scanner for JSONStringArray.
func (j *JSONStringArray) Scan(src interface{}) error {
	if src == nil {
		*j = nil
		return nil
	}

	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("JSONStringArray: unsupported type %T", src)
	}

	if len(data) == 0 {
		*j = nil
		return nil
	}

	return json.Unmarshal(data, j)
}

// Value implements driver.Valuer for JSONStringArray.
func (j JSONStringArray) Value() (driver.Value, error) {
	if j == nil {
		return json.Marshal([]string{})
	}
	return json.Marshal(j)
}

// JSONInt64Map handles PostgreSQL jsonb columns that store string→int64 maps.
// Used for file modification timestamps (FileMtimes field).
type JSONInt64Map map[string]int64

// Scan implements sql.Scanner for JSONInt64Map.
func (j *JSONInt64Map) Scan(src interface{}) error {
	if src == nil {
		*j = nil
		return nil
	}

	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("JSONInt64Map: unsupported type %T", src)
	}

	if len(data) == 0 {
		*j = nil
		return nil
	}

	return json.Unmarshal(data, j)
}

// Value implements driver.Valuer for JSONInt64Map.
func (j JSONInt64Map) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// JSONInt64Array handles PostgreSQL columns that store int64 arrays.
// Supports both JSON array format ([1,2,3]) and PostgreSQL array format ({1,2,3}).
type JSONInt64Array []int64

// Scan implements sql.Scanner for JSONInt64Array.
func (j *JSONInt64Array) Scan(src interface{}) error {
	if src == nil {
		*j = nil
		return nil
	}

	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("JSONInt64Array.Scan: unsupported type %T", src)
	}

	if len(data) == 0 {
		*j = nil
		return nil
	}

	// Convert PostgreSQL array format {1,2,3} to JSON array format [1,2,3].
	s := string(data)
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		s = "[" + s[1:len(s)-1] + "]"
		data = []byte(s)
	}

	return json.Unmarshal(data, j)
}

// Value implements driver.Valuer for JSONInt64Array.
func (j JSONInt64Array) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// Observation represents a learning extracted from a Claude Code session.
//
// Deprecated: the observations table was dropped in v5 (migration 099) and
// Memory is now the primary unit of storage and retrieval. This struct is
// retained only as a transitional response/transport shape for the few HTTP
// surfaces still being migrated to Memory JSON (provenance-cleanup CR-4
// reshapes the inject/dashboard responses; full struct removal is the CR-6
// precondition once no caller depends on the Observation shape). The ObsType*
// and related enum values remain in active use and are NOT deprecated. Do not
// introduce new code that depends on Observation — use models.Memory.
type Observation struct {
	FileMtimes              JSONInt64Map     `db:"file_mtimes" gorm:"type:jsonb" json:"file_mtimes,omitempty"`
	SDKSessionID            string           `db:"sdk_session_id" json:"sdk_session_id"`
	Project                 string           `db:"project" json:"project"`
	Scope                   ObservationScope `db:"scope" json:"scope"`
	AgentID                 string           `db:"agent_id" json:"agent_id,omitempty"`
	AgentSource             AgentSource      `db:"agent_source" json:"agent_source,omitempty"`
	Type                    ObservationType  `db:"type" json:"type"`
	MemoryType              MemoryType       `db:"memory_type" json:"memory_type"`
	SourceType              SourceType       `db:"source_type" json:"source_type,omitempty"`
	CreatedAt               string           `db:"created_at" json:"created_at"`
	Subtitle                sql.NullString   `db:"subtitle" json:"subtitle,omitempty"`
	Title                   sql.NullString   `db:"title" json:"title,omitempty"`
	Narrative               sql.NullString   `db:"narrative" json:"narrative,omitempty"`
	Concepts                JSONStringArray  `db:"concepts" gorm:"type:jsonb" json:"concepts,omitempty"`
	FilesRead               JSONStringArray  `db:"files_read" gorm:"type:jsonb" json:"files_read,omitempty"`
	FilesModified           JSONStringArray  `db:"files_modified" gorm:"type:jsonb" json:"files_modified,omitempty"`
	CommandsRun             JSONStringArray  `db:"commands_run" gorm:"type:jsonb" json:"commands_run,omitempty"`
	Facts                   JSONStringArray  `db:"facts" gorm:"type:jsonb" json:"facts,omitempty"`
	Rejected                JSONStringArray  `db:"rejected" gorm:"type:jsonb" json:"rejected,omitempty"`
	PromptNumber            sql.NullInt64    `db:"prompt_number" json:"prompt_number,omitempty"`
	LastRetrievedAt         sql.NullInt64    `db:"last_retrieved_at_epoch" json:"last_retrieved_at_epoch,omitempty"`
	ScoreUpdatedAt          sql.NullInt64    `db:"score_updated_at_epoch" json:"score_updated_at_epoch,omitempty"`
	DiscoveryTokens         int64            `db:"discovery_tokens" json:"discovery_tokens"`
	ID                      int64            `db:"id" json:"id"`
	CreatedAtEpoch          int64            `db:"created_at_epoch" json:"created_at_epoch"`
	ImportanceScore         float64          `db:"importance_score" json:"importance_score"`
	UtilityScore            float64          `db:"utility_score" json:"utility_score"`
	UserFeedback            int              `db:"user_feedback" json:"user_feedback"`
	RetrievalCount          int              `db:"retrieval_count" json:"retrieval_count"`
	InjectionCount          int              `db:"injection_count" json:"injection_count"`
	IsStale                 bool             `db:"-" json:"is_stale,omitempty"`
	IsSuperseded            bool             `db:"is_superseded" json:"is_superseded,omitempty"`
	EnrichmentLevel         int              `db:"enrichment_level" json:"enrichment_level"`
	SourceEventIDs          JSONInt64Array   `db:"source_event_ids" gorm:"type:jsonb" json:"source_event_ids,omitempty"`
	RawContent              sql.NullString   `db:"raw_content" json:"raw_content,omitempty"`
	ExpiresAt               sql.NullTime     `db:"expires_at" json:"expires_at,omitempty"`
	TtlDays                 sql.NullInt32    `db:"ttl_days" json:"ttl_days,omitempty"`
	IsExpired               bool             `db:"-" json:"is_expired,omitempty"`
	Status                  string           `db:"status" json:"status,omitempty"`
	StatusReason            sql.NullString   `db:"status_reason" json:"status_reason,omitempty"`
	EffectivenessScore      float64          `db:"effectiveness_score" json:"effectiveness_score"`
	EffectivenessInjections int              `db:"effectiveness_injections" json:"effectiveness_injections"`
	EffectivenessSuccesses  int              `db:"effectiveness_successes" json:"effectiveness_successes"`
}

// ParsedObservation is the intermediate representation of an observation parsed
// from SDK response XML before it is converted to the stored Observation format.
type ParsedObservation struct {
	FileMtimes               map[string]int64
	Type                     ObservationType
	MemoryType               MemoryType
	SourceType               SourceType
	Title                    string
	Subtitle                 string
	Narrative                string
	Scope                    ObservationScope
	AgentID                  string
	AgentSource              AgentSource
	Facts                    []string
	Concepts                 []string
	FilesRead                []string
	FilesModified            []string
	CommandsRun              []string
	Rejected                 []string // Alternatives considered and dismissed (for decisions)
	EncryptedSecret          []byte   // set for credential observations
	EncryptionKeyFingerprint string   // SHA-256(key)[:16] hex
}

// ToStoredObservation converts a ParsedObservation to a partial Observation for
// similarity comparison before storage.  Fields added during persistence
// (ID, timestamps, project, session) are intentionally left at zero value.
func (p *ParsedObservation) ToStoredObservation() *Observation {
	return &Observation{
		Type:          p.Type,
		MemoryType:    p.MemoryType,
		SourceType:    p.SourceType,
		Title:         sql.NullString{String: p.Title, Valid: p.Title != ""},
		Subtitle:      sql.NullString{String: p.Subtitle, Valid: p.Subtitle != ""},
		Facts:         p.Facts,
		Rejected:      p.Rejected,
		Narrative:     sql.NullString{String: p.Narrative, Valid: p.Narrative != ""},
		Concepts:      p.Concepts,
		FilesRead:     p.FilesRead,
		FilesModified: p.FilesModified,
		CommandsRun:   p.CommandsRun,
		FileMtimes:    p.FileMtimes,
		AgentID:       p.AgentID,
		AgentSource:   p.AgentSource,
	}
}

// DetermineScope returns ScopeGlobal if any concept in the list matches a
// GlobalizableConcept, otherwise ScopeProject.
// The caller is responsible for passing a non-nil slice; nil is safe but returns ScopeProject.
func DetermineScope(concepts []string) ObservationScope {
	for _, concept := range concepts {
		for _, globalConcept := range GlobalizableConcepts {
			if concept == globalConcept {
				return ScopeGlobal
			}
		}
	}
	return ScopeProject
}

// scopePatterns maps file path regexes to scope tags injected into observations.
// These tags let the retrieval layer filter observations by code area
// (frontend, backend, tests, etc.) without requiring manual concept tagging.
var scopePatterns = []struct {
	pattern *regexp.Regexp
	scope   string
}{
	{regexp.MustCompile(`(?i)\.(tsx|jsx|vue|svelte|css|scss|less)$`), "scope:frontend"},
	{regexp.MustCompile(`(?i)^(internal|cmd|pkg)/`), "scope:backend"},
	{regexp.MustCompile(`(?i)(prompt|generation)`), "scope:prompts"},
	{regexp.MustCompile(`(?i)(_test\.go|\.test\.[jt]sx?|_test\.py)$`), "scope:tests"},
	{regexp.MustCompile(`(?i)(\.md$|^docs/)`), "scope:docs"},
	{regexp.MustCompile(`(?i)\.(yaml|yml|toml)$`), "scope:config"},
	{regexp.MustCompile(`(?i)(migration|migrate)`), "scope:migrations"},
	{regexp.MustCompile(`(?i)(/api/|[/_]api[/_.]|handler|route)`), "scope:api"},
	{regexp.MustCompile(`(?i)(/auth/|[/_]auth[/_.]|jwt|oauth)`), "scope:auth"},
}

// classifyFileScopes returns the unique scope tags matching any of the given file paths.
// Empty paths are skipped; the result is deduplicated and ordered by first match.
func classifyFileScopes(filePaths []string) []string {
	if len(filePaths) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var scopes []string
	for _, fp := range filePaths {
		if fp == "" {
			continue
		}
		for _, sp := range scopePatterns {
			if sp.pattern.MatchString(fp) {
				if _, ok := seen[sp.scope]; !ok {
					seen[sp.scope] = struct{}{}
					scopes = append(scopes, sp.scope)
				}
			}
		}
	}
	return scopes
}

// ClassifyMemoryType maps a ParsedObservation to the most specific MemoryType bucket.
// Concept keywords take priority over the observation type so that a feature observation
// tagged "best-practice" is stored as a pattern rather than context.
func ClassifyMemoryType(obs *ParsedObservation) MemoryType {
	if obs.Type == ObsTypeGuidance {
		return MemTypeGuidance
	}
	for _, c := range obs.Concepts {
		cl := strings.ToLower(c)
		switch {
		case strings.Contains(cl, "architecture") || strings.Contains(cl, "design") || strings.Contains(cl, "choice"):
			return MemTypeDecision
		case strings.Contains(cl, "pattern") || strings.Contains(cl, "best-practice") || strings.Contains(cl, "anti-pattern"):
			return MemTypePattern
		case strings.Contains(cl, "preference") || strings.Contains(cl, "config") || strings.Contains(cl, "setting"):
			return MemTypePreference
		case strings.Contains(cl, "style") || strings.Contains(cl, "naming") || strings.Contains(cl, "format"):
			return MemTypeStyle
		case strings.Contains(cl, "workflow") || strings.Contains(cl, "habit") || strings.Contains(cl, "routine"):
			return MemTypeHabit
		case strings.Contains(cl, "insight") || strings.Contains(cl, "discovery") || strings.Contains(cl, "gotcha"):
			return MemTypeInsight
		}
	}
	return MemTypeContext
}

// ObservationJSON is the JSON-serializable view of an Observation.
// sql.NullString fields are unwrapped to plain strings so API consumers do not
// need to handle the sql.NullString envelope.
type ObservationJSON struct {
	FileMtimes              map[string]int64 `json:"file_mtimes,omitempty"`
	Subtitle                string           `json:"subtitle,omitempty"`
	SDKSessionID            string           `json:"sdk_session_id"`
	Scope                   ObservationScope `json:"scope"`
	AgentID                 string           `json:"agent_id,omitempty"`
	AgentSource             string           `json:"agent_source,omitempty"`
	Type                    ObservationType  `json:"type"`
	MemoryType              string           `json:"memory_type"`
	SourceType              string           `json:"source_type,omitempty"`
	Title                   string           `json:"title,omitempty"`
	CreatedAt               string           `json:"created_at"`
	Narrative               string           `json:"narrative,omitempty"`
	Project                 string           `json:"project"`
	Concepts                []string         `json:"concepts,omitempty"`
	Facts                   []string         `json:"facts,omitempty"`
	Rejected                []string         `json:"rejected,omitempty"`
	FilesRead               []string         `json:"files_read,omitempty"`
	FilesModified           []string         `json:"files_modified,omitempty"`
	CommandsRun             []string         `json:"commands_run,omitempty"`
	CreatedAtEpoch          int64            `json:"created_at_epoch"`
	DiscoveryTokens         int64            `json:"discovery_tokens"`
	ID                      int64            `json:"id"`
	PromptNumber            int64            `json:"prompt_number,omitempty"`
	ImportanceScore         float64          `json:"importance_score"`
	UtilityScore            float64          `json:"utility_score"`
	UserFeedback            int              `json:"user_feedback"`
	RetrievalCount          int              `json:"retrieval_count"`
	InjectionCount          int              `json:"injection_count"`
	LastRetrievedAt         int64            `json:"last_retrieved_at_epoch,omitempty"`
	ScoreUpdatedAt          int64            `json:"score_updated_at_epoch,omitempty"`
	IsStale                 bool             `json:"is_stale,omitempty"`
	IsSuperseded            bool             `json:"is_superseded,omitempty"`
	Status                  string           `json:"status,omitempty"`
	StatusReason            string           `json:"status_reason,omitempty"`
	EffectivenessScore      float64          `json:"effectiveness_score"`
	EffectivenessInjections int              `json:"effectiveness_injections"`
	EffectivenessSuccesses  int              `json:"effectiveness_successes"`
	ExpiresAt               *time.Time       `json:"expires_at,omitempty"`
	TtlDays                 *int32           `json:"ttl_days,omitempty"`
	IsExpired               bool             `json:"is_expired,omitempty"`
}

// MarshalJSON implements json.Marshaler for Observation.
// Routes through ObservationJSON to unwrap sql.Null* fields.
func (o *Observation) MarshalJSON() ([]byte, error) {
	j := ObservationJSON{
		ID:              o.ID,
		SDKSessionID:    o.SDKSessionID,
		Project:         o.Project,
		Scope:           o.Scope,
		AgentID:         o.AgentID,
		AgentSource:     string(o.AgentSource),
		Type:            o.Type,
		MemoryType:      string(o.MemoryType),
		SourceType:      string(o.SourceType),
		Facts:           o.Facts,
		Rejected:        o.Rejected,
		Concepts:        o.Concepts,
		FilesRead:       o.FilesRead,
		FilesModified:   o.FilesModified,
		CommandsRun:     o.CommandsRun,
		FileMtimes:      o.FileMtimes,
		DiscoveryTokens: o.DiscoveryTokens,
		CreatedAt:       o.CreatedAt,
		CreatedAtEpoch:  o.CreatedAtEpoch,
		IsStale:         o.IsStale,
		// Importance scoring fields
		ImportanceScore: o.ImportanceScore,
		UtilityScore:    o.UtilityScore,
		UserFeedback:    o.UserFeedback,
		RetrievalCount:  o.RetrievalCount,
		InjectionCount:  o.InjectionCount,
		// Conflict detection fields
		IsSuperseded: o.IsSuperseded,
		// Status lifecycle
		Status:                  o.Status,
		EffectivenessScore:      o.EffectivenessScore,
		EffectivenessInjections: o.EffectivenessInjections,
		EffectivenessSuccesses:  o.EffectivenessSuccesses,
		// TTL fields
		IsExpired: o.IsExpired,
	}

	// Unwrap optional sql.Null* fields only when valid.
	if o.ExpiresAt.Valid {
		t := o.ExpiresAt.Time.UTC()
		j.ExpiresAt = &t
	}
	if o.TtlDays.Valid {
		d := o.TtlDays.Int32
		j.TtlDays = &d
	}
	if o.Title.Valid {
		j.Title = o.Title.String
	}
	if o.Subtitle.Valid {
		j.Subtitle = o.Subtitle.String
	}
	if o.Narrative.Valid {
		j.Narrative = o.Narrative.String
	}
	if o.PromptNumber.Valid {
		j.PromptNumber = o.PromptNumber.Int64
	}
	if o.LastRetrievedAt.Valid {
		j.LastRetrievedAt = o.LastRetrievedAt.Int64
	}
	if o.StatusReason.Valid {
		j.StatusReason = o.StatusReason.String
	}
	if o.ScoreUpdatedAt.Valid {
		j.ScoreUpdatedAt = o.ScoreUpdatedAt.Int64
	}

	return json.Marshal(j)
}

// NewObservation constructs a fully-populated Observation from parsed SDK data.
// Scope is determined from the parsed scope (explicit) or auto-derived from concepts.
// File paths from both FilesRead and FilesModified are classified into scope tags that
// are merged into the Concepts field (gstack-insights FR-7).
func NewObservation(sdkSessionID, project string, parsed *ParsedObservation, promptNumber int, discoveryTokens int64) *Observation {
	now := time.Now()

	// Determine scope: use parsed scope if set, otherwise auto-determine from concepts.
	scope := parsed.Scope
	if scope == "" {
		scope = DetermineScope(parsed.Concepts)
	}

	// Auto-add diff-scope tags based on file paths (gstack-insights FR-7).
	concepts := parsed.Concepts
	allFiles := append(append([]string{}, parsed.FilesRead...), parsed.FilesModified...)
	if scopeTags := classifyFileScopes(allFiles); len(scopeTags) > 0 {
		seen := make(map[string]struct{}, len(concepts))
		for _, c := range concepts {
			seen[c] = struct{}{}
		}
		for _, tag := range scopeTags {
			if _, exists := seen[tag]; !exists {
				concepts = append(concepts, tag)
			}
		}
	}

	return &Observation{
		SDKSessionID:    sdkSessionID,
		Project:         project,
		Scope:           scope,
		AgentID:         parsed.AgentID,
		AgentSource:     parsed.AgentSource,
		Type:            parsed.Type,
		MemoryType:      ClassifyMemoryType(parsed),
		SourceType:      parsed.SourceType,
		Title:           sql.NullString{String: parsed.Title, Valid: parsed.Title != ""},
		Subtitle:        sql.NullString{String: parsed.Subtitle, Valid: parsed.Subtitle != ""},
		Facts:           parsed.Facts,
		Rejected:        parsed.Rejected,
		Narrative:       sql.NullString{String: parsed.Narrative, Valid: parsed.Narrative != ""},
		Concepts:        concepts,
		FilesRead:       parsed.FilesRead,
		FilesModified:   parsed.FilesModified,
		CommandsRun:     parsed.CommandsRun,
		FileMtimes:      parsed.FileMtimes,
		PromptNumber:    sql.NullInt64{Int64: int64(promptNumber), Valid: promptNumber > 0},
		DiscoveryTokens: discoveryTokens,
		CreatedAt:       now.Format(time.RFC3339),
		CreatedAtEpoch:  now.UnixMilli(),
		// Importance scoring: new observations start with score 1.0
		ImportanceScore: 1.0,
		UtilityScore:    0.5, // Neutral prior
		UserFeedback:    0,
		RetrievalCount:  0,
		InjectionCount:  0,
	}
}

// ToMap converts the observation to a map for JSON response building.
// Routes through MarshalJSON so that sql.Null* unwrapping and custom tags are applied,
// then allows callers to inject extra fields (e.g., similarity scores) before serializing.
func (o *Observation) ToMap() map[string]interface{} {
	data, err := json.Marshal(o)
	if err != nil {
		return map[string]interface{}{"id": o.ID, "error": err.Error()}
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]interface{}{"id": o.ID, "error": err.Error()}
	}
	return result
}

// CheckStaleness returns true if any file tracked in FileMtimes has been modified
// since the observation was created.  Observations with no tracked files are assumed
// fresh — callers may override this with domain logic if needed.
func (o *Observation) CheckStaleness(currentMtimes map[string]int64) bool {
	if len(o.FileMtimes) == 0 {
		return false // No file tracking, assume fresh
	}

	for path, recordedMtime := range o.FileMtimes {
		if currentMtime, exists := currentMtimes[path]; exists {
			if currentMtime > recordedMtime {
				return true // File was modified since observation was created
			}
		}
		// If file doesn't exist in currentMtimes, it may have been deleted.
		// We don't mark as stale for missing files — they might just not be checked.
	}
	return false
}
