package gorm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	gormlib "gorm.io/gorm"
)

type ConsolidationError struct{ Code string }

func (e *ConsolidationError) Error() string { return e.Code }
func consolidationHold(code string) error {
	return &ConsolidationError{Code: "PROJECT_ADOPTION_" + code}
}
func quoted(value string) string { return pq.QuoteIdentifier(value) }
func structuralDigest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type consolidationRole struct{ Table, Column, Kind string }

// Scalar addressing is mutable; historical serialized evidence is not. The live
// schema census closes roles not present in this source registry explicitly.
var consolidationRoles = []consolidationRole{
	{"sdk_sessions", "project", "tenant"},
	{"telemetry_snapshots", "project", "tenant"},
	{"search_query_log", "project", "tenant"},
	{"retrieval_stats_log", "project", "tenant"},
	{"versioned_documents", "project", "tenant"},
	{"issues", "source_project", "tenant"},
	{"issues", "target_project", "tenant"},
	{"issue_comments", "author_project", "tenant"},
	{"credentials", "project", "tenant"},
	{"memories", "project", "tenant"},
	{"behavioral_rules", "project", "tenant"},
	{"injection_log", "project", "tenant"},
	{"session_segments", "project", "tenant"},
	{"knowledge_nodes", "project", "tenant"},
	{"crystallization_candidates", "affected_projects", "array"},
	{"session_transcripts", "project", "tenant"},
	{"rule_candidates", "source_project", "tenant"},
	{"rule_injection_events", "project", "tenant"},
	{"agent_project_state", "project", "tenant"},
	{"temporal_truth_records", "project", "tenant"},
	{"attention_events", "project", "tenant"},
	{"project_continuity_slots", "project", "tenant"},
	{"code_chunks", "project_id", "derived_membership"},
	{"code_index_sessions", "project_id", "derived_membership"},
	{"sources", "project_id", "source_membership"},
	{"projects", "id", "registry"},
	{"projects", "project_key", "historical_identity"},
	{"projects", "anchor_project_id", "historical_identity"},
	{"projects", "legacy_ids", "historical_identity"},
	{"projects", "relative_path", "locator"},
	{"project_identifiers", "project_key", "historical_identity"},
	{"project_merge_audits", "target_project_key", "immutable_audit"},
	{"project_merge_audit_sources", "source_project_key", "immutable_audit"},
	{"project_resolution_attempts", "anchor_project_id", "immutable_audit"},
	{"task_memory_intervention_receipts", "canonical_project", "immutable_audit"},
	{"task_memory_intervention_receipts", "selected_source_project", "immutable_audit"},
	{"intervention_evidence_policies", "canonical_project", "immutable_audit"},
}

type ConsolidationRoleCount struct {
	Family      string `json:"object_family"`
	Role        string `json:"relationship_role"`
	Before      int64  `json:"before_count"`
	After       int64  `json:"after_count"`
	Disposition string `json:"disposition"`
}

type ConsolidationEvidence struct {
	Reference string   `json:"reference"`
	Kind      string   `json:"kind"`
	Group     string   `json:"group"`
	ProjectID string   `json:"project_id,omitempty"`
	Rows      []string `json:"rows"`
}

type ConsolidationPreflightRequest struct {
	CensusReference   string                  `json:"census_reference"`
	CensusFingerprint string                  `json:"census_fingerprint"`
	Evidence          []ConsolidationEvidence `json:"evidence"`
}

type ConsolidationMapping struct {
	Reference           string   `json:"row_reference"`
	Family              string   `json:"object_family"`
	Role                string   `json:"relationship_role"`
	SelectorReference   string   `json:"selector_reference,omitempty"`
	ProjectID           string   `json:"project_id,omitempty"`
	UnresolvedReference string   `json:"unresolved_reference,omitempty"`
	Reason              string   `json:"reason,omitempty"`
	Evidence            []string `json:"evidence,omitempty"`
}

type ConsolidationPlan struct {
	Reference     string                 `json:"plan_reference"`
	Fingerprint   string                 `json:"plan_fingerprint"`
	SourceVersion string                 `json:"source_version"`
	Snapshot      string                 `json:"snapshot_identity"`
	Schema        string                 `json:"schema_identity"`
	Mapping       []ConsolidationMapping `json:"mapping"`
	Gaps          []string               `json:"gaps"`
}

type ConsolidationApplyRequest struct {
	PlanReference   string `json:"plan_reference"`
	PlanFingerprint string `json:"plan_fingerprint"`
	BackupReference string `json:"backup_reference"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type ConsolidationReceipt struct {
	OperationID      string                   `json:"operation_id"`
	Reference        string                   `json:"receipt_reference"`
	ProjectIDs       []string                 `json:"project_ids"`
	Roles            []ConsolidationRoleCount `json:"roles"`
	Snapshot         string                   `json:"after_snapshot_identity"`
	RollbackBoundary string                   `json:"rollback_boundary"`
}

type consolidationPlanState struct {
	ID          string
	Fingerprint string
	Census      consolidationCensusState
	Mapping     []ConsolidationMapping
}

type consolidationBackupState struct {
	PlanID, Fingerprint, Schema string
	Tables                      map[string]json.RawMessage
	Sequences                   []consolidationSequence
}

type consolidationBackupRow struct {
	BackupID          string `gorm:"column:backup_id;primaryKey"`
	PlanID            string
	PlanFingerprint   string
	Ciphertext        []byte
	RestoreIdentity   string
	RestoreSnapshot   string
	RestoreVerifiedAt time.Time
}

func (consolidationBackupRow) TableName() string { return "project_consolidation_backups" }

type consolidationOperation struct {
	IdempotencyKey  string `gorm:"primaryKey"`
	OperationID     string
	PlanID          string
	PlanFingerprint string
	Receipt         JSONRaw `gorm:"type:jsonb"`
	CompletedAt     time.Time
}

func (consolidationOperation) TableName() string { return "project_consolidation_operations" }

// Membership keys are internal reader inputs, never transport DTOs. Callers must
// keep ordinary authentication, per-record privacy and Vault policy in their readers.
type ConsolidationMembership struct {
	Reference           string `gorm:"primaryKey"`
	UnresolvedReference string
	Table               string  `gorm:"column:table_name"`
	RowKey              JSONRaw `gorm:"type:jsonb"`
	FieldPath           JSONRaw `gorm:"type:jsonb"`
	SelectorReference   string
	ProjectID           *string
	Reason              string
	PlanFingerprint     string
}

func (ConsolidationMembership) TableName() string { return "project_consolidation_memberships" }

// The supplied Vault must already exist in authorized private storage. Only the
// supplied isolated restore target is mutated; these seams never apply to source.
type ProjectConsolidationStore struct {
	db, restoreDB *gormlib.DB
	vault         *vaultcrypto.Vault
}

func NewProjectConsolidationStore(db *gormlib.DB, vault *vaultcrypto.Vault, restoreDB *gormlib.DB) *ProjectConsolidationStore {
	return &ProjectConsolidationStore{db: db, vault: vault, restoreDB: restoreDB}
}

func (s *ProjectConsolidationStore) available() error {
	if s == nil || s.db == nil {
		return consolidationHold("INVENTORY_INCOMPLETE")
	}
	if s.vault == nil {
		return consolidationHold("BACKUP_MISSING")
	}
	return nil
}

func (s *ProjectConsolidationStore) opaque(kind string, value any) string {
	key := s.vault.DeriveKey("project-consolidation-reference/v1")
	mac := hmac.New(sha256.New, key[:])
	raw, _ := json.Marshal(value)
	_, _ = mac.Write([]byte(kind + "\x00"))
	_, _ = mac.Write(raw)
	return "urn:engram:migration:" + kind + ":" + hex.EncodeToString(mac.Sum(nil))
}

func (s *ProjectConsolidationStore) seal(kind string, value any) (string, error) {
	raw, err := json.Marshal(struct {
		Kind  string
		Value any
	}{kind, value})
	if err != nil {
		return "", err
	}
	encrypted, err := s.vault.Encrypt(string(raw))
	if err != nil {
		return "", consolidationHold("BACKUP_MISSING")
	}
	return hex.EncodeToString(encrypted), nil
}

func (s *ProjectConsolidationStore) open(token, kind string, destination any) error {
	if len(token) == 0 || len(token) > 64<<20 {
		return consolidationHold("STALE_PLAN")
	}
	encrypted, err := hex.DecodeString(token)
	if err != nil {
		return consolidationHold("STALE_PLAN")
	}
	raw, err := s.vault.Decrypt(encrypted)
	if err != nil {
		return consolidationHold("STALE_PLAN")
	}
	var envelope struct {
		Kind  string
		Value json.RawMessage
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil || envelope.Kind != kind || json.Unmarshal(envelope.Value, destination) != nil {
		return consolidationHold("STALE_PLAN")
	}
	return nil
}

func validConsolidationReference(value string) bool {
	return strings.HasPrefix(value, "urn:engram:") && len(value) <= 256 && !strings.ContainsAny(value, " \r\n\\@")
}

func consolidationPlanFingerprint(state consolidationPlanState) string {
	return structuralDigest(struct {
		Census  string
		Mapping []ConsolidationMapping
	}{state.Census.Fingerprint, state.Mapping})
}

func (s *ProjectConsolidationStore) plan(token, fingerprint string) (consolidationPlanState, error) {
	var state consolidationPlanState
	if err := s.open(token, "plan", &state); err != nil {
		return state, err
	}
	if state.Fingerprint != fingerprint || consolidationPlanFingerprint(state) != fingerprint {
		return state, consolidationHold("STALE_PLAN")
	}
	return state, nil
}

// Preflight is deterministic for a census and evidence set. Evidence references
// must point to retained structural proof; names, paths, upstream and similarity
// are deliberately not recognized evidence classes.
func (s *ProjectConsolidationStore) Preflight(ctx context.Context, request ConsolidationPreflightRequest) (ConsolidationPlan, error) {
	if err := s.available(); err != nil {
		return ConsolidationPlan{}, err
	}
	var census consolidationCensusState
	if err := s.open(request.CensusReference, "census", &census); err != nil {
		return ConsolidationPlan{}, err
	}
	if census.Fingerprint != request.CensusFingerprint {
		return ConsolidationPlan{}, consolidationHold("STALE_PLAN")
	}
	state := consolidationPlanState{Census: census}
	mapping, err := s.buildMapping(census.Rows, request.Evidence)
	if err != nil {
		return ConsolidationPlan{}, err
	}
	state.Mapping = mapping
	state.Fingerprint = consolidationPlanFingerprint(state)
	state.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String()
	err = s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error { return s.fresh(tx, census) }, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ConsolidationPlan{}, err
	}
	reference, err := s.seal("plan", state)
	return ConsolidationPlan{Reference: reference, Fingerprint: state.Fingerprint, SourceVersion: census.SourceVersion, Snapshot: census.Snapshot, Schema: census.Schema, Mapping: mapping, Gaps: census.Gaps}, err
}

func (s *ProjectConsolidationStore) buildMapping(rows []consolidationRow, evidence []ConsolidationEvidence) ([]ConsolidationMapping, error) {
	known := make(map[string]bool, len(rows))
	for _, row := range rows {
		known[row.Reference] = true
	}
	groups := map[string]map[string]bool{}
	for _, e := range evidence {
		if !validConsolidationReference(e.Reference) || !validConsolidationReference(e.Group) {
			return nil, consolidationHold("INVALID_EVIDENCE")
		}
		switch e.Kind {
		case "portable_marker", "common_dir_binding", "established_alias", "session_provenance", "source_provenance":
		default:
			return nil, consolidationHold("INVALID_EVIDENCE")
		}
		if e.ProjectID != "" {
			id, err := uuid.Parse(e.ProjectID)
			if err != nil || id == uuid.Nil || id.String() != e.ProjectID {
				return nil, consolidationHold("INVALID_EVIDENCE")
			}
		}
		if e.Kind == "portable_marker" && e.ProjectID == "" {
			return nil, consolidationHold("INVALID_EVIDENCE")
		}
		if groups[e.Group] == nil {
			groups[e.Group] = map[string]bool{}
		}
		if e.ProjectID != "" {
			groups[e.Group][e.ProjectID] = true
		}
		for _, ref := range e.Rows {
			if !known[ref] {
				return nil, consolidationHold("STALE_PLAN")
			}
		}
	}
	claims := map[string][]ConsolidationEvidence{}
	for _, e := range evidence {
		for _, ref := range e.Rows {
			claims[ref] = append(claims[ref], e)
		}
	}
	result := make([]ConsolidationMapping, 0, len(rows))
	resolved := map[string]string{}
	for _, row := range rows {
		m := ConsolidationMapping{Reference: row.Reference, Family: row.Table, Role: row.Column, SelectorReference: row.SelectorReference}
		targets := map[string]bool{}
		conflicts := false
		for _, e := range claims[row.Reference] {
			ids := groups[e.Group]
			if len(ids) > 1 {
				conflicts = true
				continue
			}
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("engram.logical-project/v1\x00"+e.Group)).String()
			for retained := range ids {
				id = retained
			}
			targets[id] = true
			m.Evidence = append(m.Evidence, e.Reference)
		}
		sort.Strings(m.Evidence)
		m.Evidence = compactStrings(m.Evidence)
		if len(targets) == 1 && !conflicts && row.Kind != "unknown" && row.Kind != "locator" && !row.Global {
			for id := range targets {
				m.ProjectID = id
				resolved[row.Reference] = id
			}
		} else {
			m.Reason = "missing_membership_evidence"
			if conflicts || len(targets) > 1 {
				m.Reason = "conflicting_membership_evidence"
			}
			if row.Kind == "unknown" {
				m.Reason = "unknown_field_role"
			}
			if row.Kind == "locator" {
				m.Reason = "locator_is_not_identity"
			}
			if row.Global {
				m.Reason = "native_global_scope"
			}
		}
		result = append(result, m)
	}
	// Only actual FK edges inherit membership. Multiple parents must agree;
	// registry metadata and labels never propagate an ownership guess.
	for range len(rows) {
		changed := false
		for i, row := range rows {
			if row.Kind != "inherited" || result[i].ProjectID != "" || len(row.Parents) == 0 {
				continue
			}
			id := ""
			complete := true
			for _, parent := range row.Parents {
				candidate := resolved[parent]
				if candidate == "" || (id != "" && id != candidate) {
					complete = false
					break
				}
				id = candidate
			}
			if complete {
				result[i].ProjectID = id
				result[i].Reason = ""
				resolved[row.Reference] = id
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	for i, row := range rows {
		if result[i].ProjectID == "" && !row.Global {
			result[i].UnresolvedReference = s.opaque("unresolved", row.SelectorReference)
			if row.SelectorReference == "" {
				result[i].UnresolvedReference = s.opaque("unresolved", row.Reference)
			}
		}
	}
	return result, nil
}

func compactStrings(keys []string) []string {
	out := keys[:0]
	for _, key := range keys {
		if key != "" && (len(out) == 0 || out[len(out)-1] != key) {
			out = append(out, key)
		}
	}
	return out
}

func (s *ProjectConsolidationStore) Backup(ctx context.Context, token, fingerprint string) ([]byte, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	state, err := s.plan(token, fingerprint)
	if err != nil {
		return nil, err
	}
	var encrypted []byte
	err = s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := s.fresh(tx, state.Census); err != nil {
			return err
		}
		tables, err := consolidationSnapshot(tx)
		if err != nil {
			return err
		}
		sequences, err := consolidationSequences(tx)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(consolidationBackupState{state.ID, fingerprint, state.Census.Schema, tables, sequences})
		if err != nil {
			return err
		}
		encrypted, err = s.vault.Encrypt(string(raw))
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return encrypted, err
}

// DryRun executes the real row updates and constraints, then rolls back. A live
// source dry-run is intentionally not offered: only the supplied isolated target.
func (s *ProjectConsolidationStore) DryRun(ctx context.Context, request ConsolidationApplyRequest) (ConsolidationReceipt, error) {
	return s.apply(ctx, request, true)
}

func (s *ProjectConsolidationStore) Apply(ctx context.Context, request ConsolidationApplyRequest) (ConsolidationReceipt, error) {
	return s.apply(ctx, request, false)
}

func (s *ProjectConsolidationStore) apply(ctx context.Context, request ConsolidationApplyRequest, dryRun bool) (ConsolidationReceipt, error) {
	if err := s.available(); err != nil {
		return ConsolidationReceipt{}, err
	}
	if s.restoreDB == nil {
		return ConsolidationReceipt{}, consolidationHold("ISOLATED_TARGET_MISSING")
	}
	id, err := uuid.Parse(request.IdempotencyKey)
	if err != nil || id == uuid.Nil {
		return ConsolidationReceipt{}, consolidationHold("REPLAY_CONFLICT")
	}
	state, err := s.plan(request.PlanReference, request.PlanFingerprint)
	if err != nil {
		return ConsolidationReceipt{}, err
	}
	if err := s.distinctTarget(ctx); err != nil {
		return ConsolidationReceipt{}, err
	}
	tx := s.restoreDB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return ConsolidationReceipt{}, tx.Error
	}
	defer tx.Rollback()
	if err := consolidationLock(tx); err != nil {
		return ConsolidationReceipt{}, err
	}
	var prior consolidationOperation
	err = tx.Where("idempotency_key = ?", request.IdempotencyKey).First(&prior).Error
	var receipt ConsolidationReceipt
	if err == nil {
		if prior.PlanID != state.ID || prior.PlanFingerprint != state.Fingerprint {
			return receipt, consolidationHold("REPLAY_CONFLICT")
		}
		if json.Unmarshal(prior.Receipt, &receipt) != nil {
			return receipt, consolidationHold("REPLAY_CONFLICT")
		}
		current, err := consolidationRevision(tx)
		if err != nil {
			return receipt, err
		}
		if current != receipt.Snapshot {
			return receipt, consolidationHold("STALE_PLAN")
		}
		return receipt, nil
	}
	if !errors.Is(err, gormlib.ErrRecordNotFound) {
		return receipt, err
	}
	var backup consolidationBackupRow
	if err := tx.Where("backup_id = ? AND plan_id = ? AND plan_fingerprint = ?", request.BackupReference, state.ID, state.Fingerprint).First(&backup).Error; err != nil {
		return receipt, consolidationHold("BACKUP_MISSING")
	}
	identity, err := consolidationDatabaseIdentity(tx)
	if err != nil {
		return receipt, err
	}
	current, err := consolidationRevision(tx)
	if err != nil {
		return receipt, err
	}
	if backup.RestoreVerifiedAt.IsZero() || backup.RestoreIdentity != identity || backup.RestoreSnapshot != current {
		return receipt, consolidationHold("STALE_PLAN")
	}
	raw, err := s.vault.Decrypt(backup.Ciphertext)
	if err != nil {
		return receipt, consolidationHold("BACKUP_MISSING")
	}
	var before consolidationBackupState
	if json.Unmarshal([]byte(raw), &before) != nil || before.PlanID != state.ID || before.Fingerprint != state.Fingerprint {
		return receipt, consolidationHold("BACKUP_MISSING")
	}
	schema, _, _, err := consolidationSchema(tx)
	if err != nil {
		return receipt, err
	}
	if schema != before.Schema {
		return receipt, consolidationHold("STALE_PLAN")
	}
	if err := consolidationCompare(tx, before.Tables); err != nil {
		return receipt, err
	}
	receipt, err = s.execute(tx, state, before.Tables)
	if err != nil {
		return ConsolidationReceipt{}, err
	}
	if dryRun {
		return receipt, nil
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return receipt, err
	}
	if err := tx.Create(&consolidationOperation{IdempotencyKey: request.IdempotencyKey, OperationID: receipt.OperationID, PlanID: state.ID, PlanFingerprint: state.Fingerprint, Receipt: JSONRaw(encoded), CompletedAt: time.Now().UTC()}).Error; err != nil {
		return receipt, err
	}
	if err := tx.Commit().Error; err != nil {
		return ConsolidationReceipt{}, err
	}
	return receipt, nil
}

func (s *ProjectConsolidationStore) UnresolvedMembership(ctx context.Context, reference, kind string) ([]ConsolidationMembership, error) {
	if s == nil || s.db == nil || s.vault == nil {
		return nil, consolidationHold("UNKNOWN_REFERENCE")
	}
	if !strings.HasPrefix(reference, "urn:engram:migration:unresolved:") {
		return nil, consolidationHold("UNKNOWN_REFERENCE")
	}
	var table string
	switch kind {
	case "memory":
		table = "memories"
	case "document":
		table = "versioned_documents"
	case "issue":
		table = "issues"
	case "code":
		table = "sources"
	default:
		return nil, consolidationHold("UNKNOWN_REFERENCE")
	}
	var rows []ConsolidationMembership
	if err := s.db.WithContext(ctx).Where("unresolved_reference = ? AND table_name = ? AND project_id IS NULL", reference, table).Order("reference").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, consolidationHold("UNKNOWN_REFERENCE")
	}
	if err := s.validateUnresolvedMembership(ctx, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// CanonicalMembership includes immutable history and independent code scopes
// without treating their retained historical fields as current project choices.
func (s *ProjectConsolidationStore) CanonicalMembership(ctx context.Context, projectID, table string) ([]ConsolidationMembership, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return nil, consolidationHold("UNKNOWN_REFERENCE")
	}
	var rows []ConsolidationMembership
	err := s.db.WithContext(ctx).Where("project_id = ? AND table_name = ?", projectID, table).Order("reference").Find(&rows).Error
	return rows, err
}

func (s *ProjectConsolidationStore) ResolveLegacy(ctx context.Context, selector, session string) (string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Raw(`SELECT project_id::text FROM project_consolidation_redirects WHERE selector = ? AND session_id IN ('', ?) ORDER BY session_id DESC`, selector, session).Scan(&ids).Error
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", consolidationHold("UNRESOLVED_MEMBERSHIP")
	}
	for _, id := range ids {
		if id != ids[0] {
			return "", consolidationHold("CONFLICTING_MEMBERSHIP")
		}
	}
	return ids[0], nil
}

// References select retained membership only; data readers still enforce their
// ordinary record privacy and code authorization independently.
func (s *ProjectConsolidationStore) validateUnresolvedMembership(ctx context.Context, rows []ConsolidationMembership) error {
	if s == nil || s.db == nil || s.vault == nil {
		return consolidationHold("UNKNOWN_REFERENCE")
	}
	for _, row := range rows {
		var path []string
		var key map[string]json.RawMessage
		if json.Unmarshal(row.FieldPath, &path) != nil || len(path) == 0 || json.Unmarshal(row.RowKey, &key) != nil || len(key) == 0 || row.PlanFingerprint == "" {
			return consolidationHold("STALE_REFERENCE")
		}
		keyJSON, _ := json.Marshal(key)
		var nested []string
		if len(path) > 1 {
			nested = path[1:]
		}
		ref := s.opaque("row", struct {
			Table, Column string
			Key           JSONRaw
			Path          []string
		}{row.Table, path[0], JSONRaw(keyJSON), nested})
		group := row.SelectorReference
		if group == "" {
			group = ref
		}
		if ref != row.Reference || s.opaque("unresolved", group) != row.UnresolvedReference {
			return consolidationHold("UNKNOWN_REFERENCE")
		}
		if row.Table == "sources" {
			var sourceID string
			if len(key) != 1 || json.Unmarshal(key["source_id"], &sourceID) != nil || sourceID == "" || len(path) != 1 || (path[0] != "" && path[0] != "project_id") || (path[0] == "" && row.SelectorReference != "") {
				return consolidationHold("STALE_REFERENCE")
			}
			var count int64
			if err := s.db.WithContext(ctx).Table("sources").Where("source_id = ?", sourceID).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return consolidationHold("STALE_REFERENCE")
			}
			if path[0] == "project_id" {
				var values []struct{ Selector string }
				if err := s.db.WithContext(ctx).Raw("SELECT COALESCE(project_id::text,'') AS selector FROM sources WHERE source_id = ?", sourceID).Scan(&values).Error; err != nil {
					return err
				}
				if len(values) != 1 || (row.SelectorReference == "" && values[0].Selector != "") || (row.SelectorReference != "" && s.opaque("selector", values[0].Selector) != row.SelectorReference) {
					return consolidationHold("STALE_REFERENCE")
				}
			}
			continue
		}
		var values []struct{ Selector string }
		if err := s.db.WithContext(ctx).Raw("SELECT to_jsonb(t) #>> ?::text[] AS selector FROM "+quoted(row.Table)+" t WHERE to_jsonb(t) @> ?::jsonb", pq.Array(path), string(row.RowKey)).Scan(&values).Error; err != nil {
			return err
		}
		if len(values) != 1 || (row.SelectorReference != "" && s.opaque("selector", values[0].Selector) != row.SelectorReference) {
			return consolidationHold("STALE_REFERENCE")
		}
	}
	return nil
}

func (s *ProjectConsolidationStore) LegacyUnresolved(ctx context.Context, selector string) (string, error) {
	if s == nil || s.db == nil || s.vault == nil || selector == "" {
		return "", consolidationHold("UNKNOWN_REFERENCE")
	}
	var rows []ConsolidationMembership
	if err := s.db.WithContext(ctx).Where("selector_reference = ? AND project_id IS NULL AND unresolved_reference <> ''", s.opaque("selector", selector)).Order("reference").Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", consolidationHold("UNKNOWN_REFERENCE")
	}
	if err := s.validateUnresolvedMembership(ctx, rows); err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.UnresolvedReference != rows[0].UnresolvedReference {
			return "", consolidationHold("CONFLICTING_MEMBERSHIP")
		}
	}
	return rows[0].UnresolvedReference, nil
}
