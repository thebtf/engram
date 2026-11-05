package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/projectidentity"
	gormlib "gorm.io/gorm"
)

type ProjectAliasExpected struct {
	Source         string `json:"source"`
	ExpectedTarget string `json:"expected_target"`
}
type ProjectAliasAttachRequest struct {
	Target  string                 `json:"target"`
	Aliases []ProjectAliasExpected `json:"aliases"`
	Reason  string                 `json:"reason"`
}
type ProjectAliasPlan struct {
	Reference   string                 `json:"plan_reference"`
	Fingerprint string                 `json:"plan_fingerprint"`
	Target      string                 `json:"target"`
	Aliases     []ProjectAliasExpected `json:"aliases"`
}
type ProjectAliasApplyRequest struct {
	PlanReference   string `json:"plan_reference"`
	PlanFingerprint string `json:"plan_fingerprint"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type ProjectAliasRollbackRequest struct {
	ApplyKey       string `json:"apply_key"`
	IdempotencyKey string `json:"idempotency_key"`
}
type ProjectAliasReceipt struct {
	Kind            string                 `json:"kind"`
	Status          string                 `json:"status"`
	IdempotencyKey  string                 `json:"idempotency_key"`
	ApplyKey        string                 `json:"apply_key,omitempty"`
	PlanFingerprint string                 `json:"plan_fingerprint"`
	Target          string                 `json:"target"`
	Aliases         []ProjectAliasExpected `json:"aliases"`
	ProjectSnapshot string                 `json:"project_snapshot"`
	Actor           string                 `json:"actor"`
}
type projectAliasPlanState struct {
	Request          ProjectAliasAttachRequest
	ProjectSnapshot  string
	RedirectSnapshot string
	Fingerprint      string
	ID               string
}

func normalizeAliasRequest(req ProjectAliasAttachRequest) (ProjectAliasAttachRequest, error) {
	if !projectidentity.ValidProjectID(req.Target) || len(req.Aliases) == 0 || len(req.Aliases) > 64 || strings.TrimSpace(req.Reason) != req.Reason || len(req.Reason) == 0 || len(req.Reason) > 1024 {
		return req, consolidationHold("UNKNOWN_REFERENCE")
	}
	req.Aliases = append([]ProjectAliasExpected(nil), req.Aliases...)
	sort.Slice(req.Aliases, func(i, j int) bool { return req.Aliases[i].Source < req.Aliases[j].Source })
	for i, a := range req.Aliases {
		if !projectidentity.ValidProjectID(a.Source) || a.Source == req.Target || a.ExpectedTarget != "" || i > 0 && req.Aliases[i-1].Source == a.Source {
			return req, consolidationHold("CONFLICTING_MEMBERSHIP")
		}
	}
	return req, nil
}

func validAliasKey(key string) bool {
	u, err := uuid.Parse(key)
	return err == nil && u != uuid.Nil && u.String() == key
}

func aliasSources(req ProjectAliasAttachRequest) []string {
	ids := make([]string, 0, len(req.Aliases))
	for _, a := range req.Aliases {
		ids = append(ids, a.Source)
	}
	return ids
}

// aliasSnapshots binds project identity and every related redirect, not content rows.
func aliasSnapshots(tx *gormlib.DB, req ProjectAliasAttachRequest) (string, string, error) {
	sources := aliasSources(req)
	ids := append([]string{req.Target}, sources...)
	var count int64
	if err := tx.Model(&Project{}).Where("id IN ? AND removed_at IS NULL AND (identity_status IS NULL OR identity_status = 'active')", ids).Count(&count).Error; err != nil {
		return "", "", err
	}
	if count != int64(len(ids)) {
		return "", "", consolidationHold("STALE_REFERENCE")
	}
	var projects, redirects string
	if err := tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(p)-'last_heartbeat' ORDER BY p.id),'[]'::jsonb)::text FROM projects p WHERE p.id IN ?`, ids).Scan(&projects).Error; err != nil {
		return "", "", err
	}
	if err := tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.selector,r.session_id),'[]'::jsonb)::text FROM project_consolidation_redirects r WHERE r.selector IN ? OR r.project_id IN ?`, ids, sources).Scan(&redirects).Error; err != nil {
		return "", "", err
	}
	for _, a := range req.Aliases {
		if err := tx.Raw(`SELECT count(*) FROM projects WHERE removed_at IS NULL AND id <> ? AND COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, a.Source, a.Source).Scan(&count).Error; err != nil {
			return "", "", err
		}
		if count != 0 {
			return "", "", consolidationHold("CONFLICTING_MEMBERSHIP")
		}
	}
	return structuralDigest(projects), structuralDigest(redirects), nil
}

func aliasPlanFingerprint(state projectAliasPlanState) string {
	return structuralDigest(struct {
		Request             ProjectAliasAttachRequest
		Projects, Redirects string
	}{state.Request, state.ProjectSnapshot, state.RedirectSnapshot})
}

// PrepareProjectAliases is read-only; an empty redirect set is part of the sealed expected state.
func (s *ProjectConsolidationStore) PrepareProjectAliases(ctx context.Context, request ProjectAliasAttachRequest) (ProjectAliasPlan, error) {
	if err := s.available(); err != nil {
		return ProjectAliasPlan{}, err
	}
	req, err := normalizeAliasRequest(request)
	if err != nil {
		return ProjectAliasPlan{}, err
	}
	state := projectAliasPlanState{Request: req}
	err = s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		var e error
		state.ProjectSnapshot, state.RedirectSnapshot, e = aliasSnapshots(tx, req)
		if e != nil {
			return e
		}
		if state.RedirectSnapshot != structuralDigest("[]") {
			return consolidationHold("CONFLICTING_MEMBERSHIP")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ProjectAliasPlan{}, err
	}
	state.Fingerprint = aliasPlanFingerprint(state)
	state.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String()
	token, err := s.seal("project-alias-plan", state)
	if err != nil {
		return ProjectAliasPlan{}, err
	}
	return ProjectAliasPlan{Reference: token, Fingerprint: state.Fingerprint, Target: req.Target, Aliases: req.Aliases}, nil
}

func (s *ProjectConsolidationStore) openAliasPlan(ref, fp string) (projectAliasPlanState, error) {
	var state projectAliasPlanState
	if err := s.open(ref, "project-alias-plan", &state); err != nil {
		return state, err
	}
	req, err := normalizeAliasRequest(state.Request)
	if err != nil {
		return state, consolidationHold("STALE_PLAN")
	}
	state.Request = req
	if fp != state.Fingerprint || fp != aliasPlanFingerprint(state) || state.ID != uuid.NewSHA1(uuid.NameSpaceOID, []byte(fp)).String() {
		return state, consolidationHold("STALE_PLAN")
	}
	return state, nil
}

func aliasLock(tx *gormlib.DB) error {
	if err := tx.Exec(`SET LOCAL lock_timeout='5s'`).Error; err != nil {
		return err
	}
	return tx.Exec(`LOCK TABLE audit_log, project_consolidation_operations, project_consolidation_redirects, projects IN SHARE ROW EXCLUSIVE MODE`).Error
}

func aliasOperation(tx *gormlib.DB, key string) (consolidationOperation, ProjectAliasReceipt, error) {
	var op consolidationOperation
	var receipt ProjectAliasReceipt
	err := tx.Where("idempotency_key = ?", key).First(&op).Error
	if err != nil {
		return op, receipt, err
	}
	if json.Unmarshal(op.Receipt, &receipt) != nil || receipt.Kind != "project-alias-v1" || receipt.IdempotencyKey != key || receipt.PlanFingerprint != op.PlanFingerprint {
		return op, receipt, consolidationHold("REPLAY_CONFLICT")
	}
	return op, receipt, nil
}

func aliasRecord(tx *gormlib.DB, key, planID, fingerprint string, receipt ProjectAliasReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return tx.Create(&consolidationOperation{IdempotencyKey: key, OperationID: uuid.NewString(), PlanID: planID, PlanFingerprint: fingerprint, Receipt: JSONRaw(raw), CompletedAt: time.Now().UTC()}).Error
}

func aliasAudit(tx *gormlib.DB, action, actor, reason string, receipt ProjectAliasReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	state := json.RawMessage(raw)
	return tx.Create(&AuditLogEntry{Action: action, Actor: actor, Reason: reason, AfterState: &state}).Error
}

func aliasAppliedStillCurrent(tx *gormlib.DB, receipt ProjectAliasReceipt) error {
	req := ProjectAliasAttachRequest{Target: receipt.Target, Aliases: receipt.Aliases, Reason: "reconcile"}
	projects, _, err := aliasSnapshots(tx, req)
	if err != nil {
		return err
	}
	if projects != receipt.ProjectSnapshot {
		return consolidationHold("STALE_PLAN")
	}
	sources := aliasSources(req)
	var all, exact int64
	if err := tx.Raw(`SELECT count(*) FROM project_consolidation_redirects WHERE selector IN ? OR selector = ? OR project_id IN ?`, sources, req.Target, sources).Scan(&all).Error; err != nil {
		return err
	}
	if err := tx.Raw(`SELECT count(*) FROM project_consolidation_redirects WHERE selector IN ? AND session_id = '' AND project_id = ? AND plan_fingerprint = ?`, sources, req.Target, receipt.PlanFingerprint).Scan(&exact).Error; err != nil {
		return err
	}
	if all != int64(len(sources)) || exact != all {
		return consolidationHold("STALE_PLAN")
	}
	return nil
}

func (s *ProjectConsolidationStore) ApplyProjectAliases(ctx context.Context, request ProjectAliasApplyRequest, actor string) (ProjectAliasReceipt, error) {
	if err := s.available(); err != nil {
		return ProjectAliasReceipt{}, err
	}
	if !validAliasKey(request.IdempotencyKey) || strings.TrimSpace(actor) == "" {
		return ProjectAliasReceipt{}, consolidationHold("UNKNOWN_REFERENCE")
	}
	state, err := s.openAliasPlan(request.PlanReference, request.PlanFingerprint)
	if err != nil {
		return ProjectAliasReceipt{}, err
	}
	var result ProjectAliasReceipt
	err = s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := aliasLock(tx); err != nil {
			return err
		}
		op, prior, err := aliasOperation(tx, request.IdempotencyKey)
		if err == nil {
			if prior.Status != "applied" || op.PlanID != state.ID || op.PlanFingerprint != state.Fingerprint {
				return consolidationHold("REPLAY_CONFLICT")
			}
			var rolled int64
			if err := tx.Model(&consolidationOperation{}).Where("receipt->>'kind' = ? AND receipt->>'status' = ? AND receipt->>'apply_key' = ?", "project-alias-v1", "rolled_back", request.IdempotencyKey).Count(&rolled).Error; err != nil {
				return err
			}
			if rolled != 0 {
				return consolidationHold("REPLAY_CONFLICT")
			}
			if err := aliasAppliedStillCurrent(tx, prior); err != nil {
				return err
			}
			result = prior
			return nil
		}
		if !errors.Is(err, gormlib.ErrRecordNotFound) {
			return err
		}
		projects, redirects, err := aliasSnapshots(tx, state.Request)
		if err != nil {
			return err
		}
		if projects != state.ProjectSnapshot || redirects != state.RedirectSnapshot {
			return consolidationHold("STALE_PLAN")
		}
		for _, alias := range state.Request.Aliases {
			if err := tx.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, ?)`, alias.Source, state.Request.Target, state.Fingerprint).Error; err != nil {
				return err
			}
		}
		result = ProjectAliasReceipt{Kind: "project-alias-v1", Status: "applied", IdempotencyKey: request.IdempotencyKey, PlanFingerprint: state.Fingerprint, Target: state.Request.Target, Aliases: state.Request.Aliases, ProjectSnapshot: state.ProjectSnapshot, Actor: actor}
		if err := aliasRecord(tx, request.IdempotencyKey, state.ID, state.Fingerprint, result); err != nil {
			return err
		}
		return aliasAudit(tx, "project_alias_attach", actor, state.Request.Reason, result)
	})
	return result, err
}

func (s *ProjectConsolidationStore) ProjectAliasStatus(ctx context.Context, key string) (ProjectAliasReceipt, error) {
	if err := s.available(); err != nil {
		return ProjectAliasReceipt{}, err
	}
	if !validAliasKey(key) {
		return ProjectAliasReceipt{}, consolidationHold("UNKNOWN_REFERENCE")
	}
	var receipt ProjectAliasReceipt
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		var err error
		_, receipt, err = aliasOperation(tx, key)
		if err != nil {
			return err
		}
		if receipt.Status == "applied" {
			var count int64
			if err := tx.Model(&consolidationOperation{}).Where("receipt->>'kind' = ? AND receipt->>'status' = ? AND receipt->>'apply_key' = ?", "project-alias-v1", "rolled_back", key).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				receipt.Status = "rolled_back"
			} else if err := aliasAppliedStillCurrent(tx, receipt); err != nil {
				return err
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return receipt, err
}

func (s *ProjectConsolidationStore) RollbackProjectAliases(ctx context.Context, request ProjectAliasRollbackRequest, actor string) (ProjectAliasReceipt, error) {
	if err := s.available(); err != nil {
		return ProjectAliasReceipt{}, err
	}
	if !validAliasKey(request.ApplyKey) || !validAliasKey(request.IdempotencyKey) || request.ApplyKey == request.IdempotencyKey || strings.TrimSpace(actor) == "" {
		return ProjectAliasReceipt{}, consolidationHold("UNKNOWN_REFERENCE")
	}
	var result ProjectAliasReceipt
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := aliasLock(tx); err != nil {
			return err
		}
		_, prior, err := aliasOperation(tx, request.IdempotencyKey)
		if err == nil {
			if prior.Status != "rolled_back" || prior.ApplyKey != request.ApplyKey {
				return consolidationHold("REPLAY_CONFLICT")
			}
			result = prior
			return nil
		}
		if !errors.Is(err, gormlib.ErrRecordNotFound) {
			return err
		}
		appliedOp, applied, err := aliasOperation(tx, request.ApplyKey)
		if err != nil {
			return err
		}
		if applied.Status != "applied" {
			return consolidationHold("REPLAY_CONFLICT")
		}
		var rolled int64
		if err := tx.Model(&consolidationOperation{}).Where("receipt->>'kind' = ? AND receipt->>'status' = ? AND receipt->>'apply_key' = ?", "project-alias-v1", "rolled_back", request.ApplyKey).Count(&rolled).Error; err != nil {
			return err
		}
		if rolled != 0 {
			return consolidationHold("REPLAY_CONFLICT")
		}
		req := ProjectAliasAttachRequest{Target: applied.Target, Aliases: applied.Aliases, Reason: "rollback"}
		projects, _, err := aliasSnapshots(tx, req)
		if err != nil {
			return err
		}
		if projects != applied.ProjectSnapshot {
			return consolidationHold("STALE_PLAN")
		}
		sources := aliasSources(req)
		var count int64
		if err := tx.Raw(`SELECT count(*) FROM project_consolidation_redirects WHERE selector IN ? OR selector = ? OR project_id IN ?`, sources, req.Target, sources).Scan(&count).Error; err != nil {
			return err
		}
		if count != int64(len(sources)) {
			return consolidationHold("STALE_PLAN")
		}
		deleted := tx.Exec(`DELETE FROM project_consolidation_redirects WHERE selector IN ? AND session_id = '' AND project_id = ? AND plan_fingerprint = ?`, sources, req.Target, applied.PlanFingerprint)
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != int64(len(sources)) {
			return consolidationHold("STALE_PLAN")
		}
		result = applied
		result.Status = "rolled_back"
		result.IdempotencyKey = request.IdempotencyKey
		result.ApplyKey = request.ApplyKey
		result.Actor = actor
		if err := aliasRecord(tx, request.IdempotencyKey, appliedOp.PlanID, applied.PlanFingerprint, result); err != nil {
			return err
		}
		return aliasAudit(tx, "project_alias_rollback", actor, "conditional rollback", result)
	})
	return result, err
}
