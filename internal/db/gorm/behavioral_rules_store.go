// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thebtf/engram/pkg/models"
)

// BehavioralRulesStore provides behavioral-rule database operations using GORM.
// It targets the dedicated behavioral_rules table created by migration 089.
//
// Global rules (project IS NULL) are always included in List results regardless of the
// project filter — this ensures every session receives globally-applicable guidance.
//
// Immutability contract: Create and Update return NEW *models.BehavioralRule values
// populated from the database row. The caller's input struct is never mutated.
type BehavioralRulesStore struct {
	db *gorm.DB
}

var (
	// ErrBehavioralRuleSelectionDenied means a current browser scope has no usable selection.
	ErrBehavioralRuleSelectionDenied = errors.New("behavioral rule selection denied")
	// ErrBehavioralRuleSelectionInvalid means the request cannot safely name one Rules action.
	ErrBehavioralRuleSelectionInvalid = errors.New("behavioral rule selection is invalid")
	// ErrBehavioralRuleSelectionConflict means a selected rule or declared reorder scope changed.
	ErrBehavioralRuleSelectionConflict = errors.New("behavioral rule selection conflict")
)

// BehavioralRuleSelectionAction is the closed action matrix for selected Rules.
type BehavioralRuleSelectionAction string

const (
	BehavioralRuleSelectionEnable  BehavioralRuleSelectionAction = "enable"
	BehavioralRuleSelectionDisable BehavioralRuleSelectionAction = "disable"
	BehavioralRuleSelectionDelete  BehavioralRuleSelectionAction = "delete"
	BehavioralRuleSelectionUpdate  BehavioralRuleSelectionAction = "update"
	BehavioralRuleSelectionReorder BehavioralRuleSelectionAction = "reorder"
)

// BehavioralRuleSelectionOutcome records the durable completion of one selected Rule.
// Conflicts abort the transaction and therefore have no per-item durable outcome.
type BehavioralRuleSelectionOutcome string

const (
	BehavioralRuleSelectionCommitted BehavioralRuleSelectionOutcome = "committed"
)

// BehavioralRuleScope identifies the one project scope an atomic reorder may affect.
// A nil Project is the global Rules scope.
type BehavioralRuleScope struct {
	Project *string
}

// BehavioralRuleOrder names one expected rule revision in its desired scope order.
type BehavioralRuleOrder struct {
	RuleID          int64
	ExpectedVersion uint64
}

// BehavioralRuleSelectionOperation is a server-side Rules action over the
// selection currently bound to one browser owner/session/domain scope.
type BehavioralRuleSelectionOperation struct {
	Action           BehavioralRuleSelectionAction
	SelectionKind    CollectionSelectionKind
	SelectionVersion int64
	SelectionToken   string
	Content          *string
	Priority         *int
	EditedBy         *string
	Scope            *BehavioralRuleScope
	Order            []BehavioralRuleOrder
}

// BehavioralRuleSelectionOperationItem records a committed or conflicting target.
// Rule is intentionally nil for deletions so destructive readback never discloses
// a deleted row.
type BehavioralRuleSelectionOperationItem struct {
	TargetID        int64
	Outcome         BehavioralRuleSelectionOutcome
	ObservedVersion *int
	Rule            *models.BehavioralRule
	Deleted         bool
	ReadbackPending bool
}

// BehavioralRuleSelectionOperationResult holds all known per-target outcomes.
type BehavioralRuleSelectionOperationResult struct {
	Items []BehavioralRuleSelectionOperationItem
}

// NewBehavioralRulesStore creates a new BehavioralRulesStore backed by the given Store.
func NewBehavioralRulesStore(store *Store) *BehavioralRulesStore {
	return &BehavioralRulesStore{db: store.DB}
}

// Create inserts a new behavioral rule. Returns a new *models.BehavioralRule populated with
// the database-assigned ID and timestamps. The caller's input is never mutated.
func (s *BehavioralRulesStore) Create(ctx context.Context, rule *models.BehavioralRule) (*models.BehavioralRule, error) {
	if rule == nil {
		return nil, fmt.Errorf("behavioral rule must not be nil")
	}
	if rule.Content == "" {
		return nil, fmt.Errorf("behavioral rule Content must not be empty")
	}

	now := time.Now().UTC()

	// Copy the nullable project string to prevent the caller's pointer from aliasing
	// the row stored in the database — immutability contract: caller's input is never mutated.
	var project *string
	if rule.Project != nil {
		p := *rule.Project
		project = &p
	}

	row := &BehavioralRule{
		Project:   project,
		Content:   rule.Content,
		Priority:  rule.Priority,
		EditedBy:  rule.EditedBy,
		Enabled:   true,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if rule.Version > 0 {
		row.Version = rule.Version
	}

	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("create behavioral rule: %w", err)
	}
	return behavioralRuleRowToModel(row), nil
}

// Get returns the active (non-soft-deleted) behavioral rule with the given ID.
// Returns a wrapped gorm.ErrRecordNotFound if no active row exists.
func (s *BehavioralRulesStore) Get(ctx context.Context, id int64) (*models.BehavioralRule, error) {
	if id == 0 {
		return nil, fmt.Errorf("id must be non-zero")
	}
	var row BehavioralRule
	err := s.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get behavioral rule id=%d: %w", id, err)
	}
	return behavioralRuleRowToModel(&row), nil
}

// List returns active behavioral rules.
//
// Scoping rules:
//   - project == nil  → returns only global rules (WHERE project IS NULL AND deleted_at IS NULL)
//   - project != nil  → returns project-scoped AND global rules
//     (WHERE (project = ? OR project IS NULL) AND deleted_at IS NULL)
//
// Results are ordered by priority DESC, created_at DESC.
// limit must be > 0; if ≤ 0 it is clamped to 50.
func (s *BehavioralRulesStore) List(ctx context.Context, project *string, limit int) ([]*models.BehavioralRule, error) {
	return s.list(ctx, project, limit, false)
}

// ListEnabled returns only enabled active behavioral rules for injection paths.
// Operator registry screens use List/ListAll so disabled rules remain visible and recoverable.
func (s *BehavioralRulesStore) ListEnabled(ctx context.Context, project *string, limit int) ([]*models.BehavioralRule, error) {
	return s.list(ctx, project, limit, true)
}

func (s *BehavioralRulesStore) list(ctx context.Context, project *string, limit int, enabledOnly bool) ([]*models.BehavioralRule, error) {
	if limit <= 0 {
		limit = 50
	}

	q := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Order("priority DESC, created_at DESC").
		Limit(limit)

	if project == nil {
		q = q.Where("project IS NULL")
	} else {
		q = q.Where("project = ? OR project IS NULL", *project)
	}
	if enabledOnly {
		q = q.Where("enabled = ?", true)
	}

	var rows []BehavioralRule
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list behavioral rules: %w", err)
	}
	result := make([]*models.BehavioralRule, len(rows))
	for i := range rows {
		result[i] = behavioralRuleRowToModel(&rows[i])
	}
	return result, nil
}

// ListAll returns all active behavioral rules across global and project scopes.
// Results are ordered by priority DESC, created_at DESC.
// limit must be > 0; if ≤ 0 it is clamped to 50.
func (s *BehavioralRulesStore) ListAll(ctx context.Context, limit int) ([]*models.BehavioralRule, error) {
	if limit <= 0 {
		limit = 50
	}

	var rows []BehavioralRule
	if err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Order("priority DESC, created_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list all behavioral rules: %w", err)
	}

	result := make([]*models.BehavioralRule, len(rows))
	for i := range rows {
		result[i] = behavioralRuleRowToModel(&rows[i])
	}
	return result, nil
}

// Update updates an existing behavioral rule by ID.
// Bumps version and sets updated_at. Returns a NEW populated model.
// The caller's input struct is never mutated.
func (s *BehavioralRulesStore) Update(ctx context.Context, rule *models.BehavioralRule) (*models.BehavioralRule, error) {
	if rule == nil {
		return nil, fmt.Errorf("behavioral rule must not be nil")
	}
	if rule.ID == 0 {
		return nil, fmt.Errorf("behavioral rule ID must be set for Update")
	}
	if rule.Content == "" {
		return nil, fmt.Errorf("behavioral rule Content must not be empty")
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"content":    rule.Content,
		"priority":   rule.Priority,
		"edited_by":  rule.EditedBy,
		"updated_at": now,
		"version":    gorm.Expr("version + 1"),
	}
	// project is intentionally excluded from partial updates — changing a rule's scope
	// (global → project-scoped or vice versa) is a design-time concern, not a runtime one.

	result := s.db.WithContext(ctx).
		Model(&BehavioralRule{}).
		Where("id = ? AND deleted_at IS NULL", rule.ID).
		Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("update behavioral rule id=%d: %w", rule.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("update behavioral rule id=%d: %w", rule.ID, gorm.ErrRecordNotFound)
	}

	return s.Get(ctx, rule.ID)
}

// SetEnabled toggles an active behavioral rule without changing content, priority, or scope.
func (s *BehavioralRulesStore) SetEnabled(ctx context.Context, id int64, enabled bool, editedBy *string) (*models.BehavioralRule, error) {
	if id == 0 {
		return nil, fmt.Errorf("behavioral rule ID must be set for SetEnabled")
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"enabled":    enabled,
		"updated_at": now,
		"version":    gorm.Expr("version + 1"),
	}
	if editedBy != nil {
		if trimmed := strings.TrimSpace(*editedBy); trimmed != "" {
			updates["edited_by"] = trimmed
		}
	}

	result := s.db.WithContext(ctx).
		Model(&BehavioralRule{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("set behavioral rule enabled id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("set behavioral rule enabled id=%d: %w", id, gorm.ErrRecordNotFound)
	}

	return s.Get(ctx, id)
}

// Delete soft-deletes the behavioral rule by setting deleted_at = NOW().
// Returns gorm.ErrRecordNotFound if no active row exists.
func (s *BehavioralRulesStore) Delete(ctx context.Context, id int64) error {
	if id == 0 {
		return fmt.Errorf("behavioral rule id must be non-zero")
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).
		Model(&BehavioralRule{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{
			"deleted_at": now,
			"updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("delete behavioral rule id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete behavioral rule id=%d: %w", id, gorm.ErrRecordNotFound)
	}
	return nil
}

// behavioralRuleRowToModel converts a GORM BehavioralRule row to the pkg/models.BehavioralRule type.
func behavioralRuleRowToModel(row *BehavioralRule) *models.BehavioralRule {
	return &models.BehavioralRule{
		ID:        row.ID,
		Project:   row.Project,
		Content:   row.Content,
		Priority:  row.Priority,
		EditedBy:  row.EditedBy,
		Version:   row.Version,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		DeletedAt: row.DeletedAt,
		Enabled:   row.Enabled,
	}
}

type behavioralRuleSelectionTarget struct {
	id              int64
	expectedVersion uint64
}

// ApplySelectionOperation resolves the current scoped selection and applies one
// allowed Rules action. Selection resolution, optimistic checks, and every
// mutation share one transaction: a conflict aborts the whole operation.
func (s *BehavioralRulesStore) ApplySelectionOperation(ctx context.Context, scope CollectionSelectionScope, operation BehavioralRuleSelectionOperation) (BehavioralRuleSelectionOperationResult, error) {
	if s == nil || s.db == nil {
		return BehavioralRuleSelectionOperationResult{}, errors.New("behavioral rules store is not configured")
	}
	if err := validateBehavioralRuleSelectionOperation(operation); err != nil {
		return BehavioralRuleSelectionOperationResult{}, err
	}

	var result BehavioralRuleSelectionOperationResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		targets, err := behavioralRuleSelectionTargetsForOperation(tx, scope, operation)
		if err != nil {
			return err
		}
		if operation.Action == BehavioralRuleSelectionReorder {
			return applyBehavioralRuleReorder(tx, targets, operation, &result)
		}
		return applyBehavioralRuleSelectionMutation(tx, targets, operation, &result)
	})
	if err != nil {
		return BehavioralRuleSelectionOperationResult{}, err
	}

	s.readBehavioralRuleSelectionReadback(ctx, operation, &result)
	return result, nil
}

func validateBehavioralRuleSelectionOperation(operation BehavioralRuleSelectionOperation) error {
	if operation.SelectionVersion < 1 {
		return fmt.Errorf("%w: selection version is required", ErrBehavioralRuleSelectionInvalid)
	}
	switch operation.SelectionKind {
	case CollectionSelectionExplicit, CollectionSelectionPage:
		if operation.SelectionToken != "" {
			return fmt.Errorf("%w: only frozen selections carry a token", ErrBehavioralRuleSelectionInvalid)
		}
	case CollectionSelectionFrozenFilter:
		if operation.SelectionToken == "" {
			return fmt.Errorf("%w: frozen selection token is required", ErrBehavioralRuleSelectionInvalid)
		}
	default:
		return fmt.Errorf("%w: selection kind is unsupported", ErrBehavioralRuleSelectionInvalid)
	}
	switch operation.Action {
	case BehavioralRuleSelectionEnable, BehavioralRuleSelectionDisable:
		if operation.Content != nil || operation.Priority != nil || operation.Scope != nil || len(operation.Order) != 0 {
			return fmt.Errorf("%w: toggle accepts no field or reorder payload", ErrBehavioralRuleSelectionInvalid)
		}
	case BehavioralRuleSelectionDelete:
		if operation.Content != nil || operation.Priority != nil || operation.EditedBy != nil || operation.Scope != nil || len(operation.Order) != 0 {
			return fmt.Errorf("%w: delete accepts no field or reorder payload", ErrBehavioralRuleSelectionInvalid)
		}
	case BehavioralRuleSelectionUpdate:
		if operation.Content == nil && operation.Priority == nil {
			return fmt.Errorf("%w: update requires content or priority", ErrBehavioralRuleSelectionInvalid)
		}
		if operation.Content != nil && strings.TrimSpace(*operation.Content) == "" {
			return fmt.Errorf("%w: content must not be empty", ErrBehavioralRuleSelectionInvalid)
		}
		if operation.Scope != nil || len(operation.Order) != 0 {
			return fmt.Errorf("%w: update accepts no reorder payload", ErrBehavioralRuleSelectionInvalid)
		}
	case BehavioralRuleSelectionReorder:
		if operation.Scope == nil || len(operation.Order) == 0 || operation.Content != nil || operation.Priority != nil || operation.EditedBy != nil {
			return fmt.Errorf("%w: reorder requires only a scope and complete order", ErrBehavioralRuleSelectionInvalid)
		}
		if operation.Scope.Project != nil && (strings.TrimSpace(*operation.Scope.Project) == "" || strings.TrimSpace(*operation.Scope.Project) != *operation.Scope.Project) {
			return fmt.Errorf("%w: reorder scope project is invalid", ErrBehavioralRuleSelectionInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported action", ErrBehavioralRuleSelectionInvalid)
	}
	return nil
}

func behavioralRuleSelectionTargetsForOperation(tx *gorm.DB, scope CollectionSelectionScope, operation BehavioralRuleSelectionOperation) ([]behavioralRuleSelectionTarget, error) {
	if err := validateCollectionSelectionScope(scope); err != nil || scope.Domain != "rules" {
		return nil, fmt.Errorf("%w: rules scope is invalid", ErrBehavioralRuleSelectionDenied)
	}

	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"subject_user_id = ? AND session_id = ? AND domain = ?", scope.SubjectUserID, scope.SessionID, scope.Domain,
	)
	if operation.SelectionToken != "" {
		if !validCollectionSelectionToken(operation.SelectionToken) {
			return nil, fmt.Errorf("%w: frozen token is invalid", ErrBehavioralRuleSelectionDenied)
		}
		query = query.Where("selection_token = ?", operation.SelectionToken)
	}

	var record CollectionSelectionRecord
	if err := query.First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBehavioralRuleSelectionDenied
		}
		return nil, fmt.Errorf("load behavioral rule selection: %w", err)
	}
	if operation.SelectionToken != "" && record.Kind != CollectionSelectionFrozenFilter {
		return nil, ErrBehavioralRuleSelectionDenied
	}
	if operation.SelectionToken == "" && record.Kind == CollectionSelectionFrozenFilter {
		return nil, ErrBehavioralRuleSelectionDenied
	}
	if record.ReconfirmationRequired {
		return nil, ErrCollectionSelectionReconfirmationRequired
	}
	if _, changed := collectionSelectionChangeReason(record, scope, time.Now().UTC()); changed {
		return nil, ErrCollectionSelectionReconfirmationRequired
	}
	if record.Kind != operation.SelectionKind {
		return nil, ErrBehavioralRuleSelectionConflict
	}
	selection, err := collectionSelectionFromRecord(record)
	if err != nil {
		return nil, fmt.Errorf("load behavioral rule selection: %w", err)
	}
	if selection.Version != operation.SelectionVersion {
		return nil, ErrBehavioralRuleSelectionConflict
	}
	return behavioralRuleOperationTargets(selection)
}

func behavioralRuleOperationTargets(selection CollectionSelection) ([]behavioralRuleSelectionTarget, error) {
	excluded := make(map[string]struct{}, len(selection.ExcludedIDs))
	for _, id := range selection.ExcludedIDs {
		excluded[id] = struct{}{}
	}
	targets := make([]behavioralRuleSelectionTarget, 0, len(selection.Targets))
	seen := make(map[int64]struct{}, len(selection.Targets))
	maxInt := uint64(^uint(0) >> 1)
	for _, target := range selection.Targets {
		if _, skip := excluded[target.ID]; skip {
			continue
		}
		id, err := strconv.ParseInt(target.ID, 10, 64)
		if err != nil || id <= 0 || target.ExpectedVersion == 0 || target.ExpectedVersion > maxInt {
			return nil, fmt.Errorf("%w: selected target is invalid", ErrBehavioralRuleSelectionInvalid)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("%w: selected target is duplicated", ErrBehavioralRuleSelectionInvalid)
		}
		seen[id] = struct{}{}
		targets = append(targets, behavioralRuleSelectionTarget{id: id, expectedVersion: target.ExpectedVersion})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: selection has no effective targets", ErrBehavioralRuleSelectionInvalid)
	}
	return targets, nil
}

func applyBehavioralRuleSelectionMutation(tx *gorm.DB, targets []behavioralRuleSelectionTarget, operation BehavioralRuleSelectionOperation, result *BehavioralRuleSelectionOperationResult) error {
	ids := make([]int64, len(targets))
	for index, target := range targets {
		ids[index] = target.id
	}
	var rows []BehavioralRule
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("lock selected behavioral rules: %w", err)
	}
	byID := make(map[int64]*BehavioralRule, len(rows))
	for index := range rows {
		byID[rows[index].ID] = &rows[index]
	}
	for _, target := range targets {
		row, found := byID[target.id]
		if !found || uint64(row.Version) != target.expectedVersion {
			return ErrBehavioralRuleSelectionConflict
		}
	}

	now := time.Now().UTC()
	for _, target := range targets {
		item, err := updateSelectedBehavioralRule(tx, byID[target.id], operation, now)
		if err != nil {
			return err
		}
		result.Items = append(result.Items, item)
	}
	return nil
}

func updateSelectedBehavioralRule(tx *gorm.DB, row *BehavioralRule, operation BehavioralRuleSelectionOperation, now time.Time) (BehavioralRuleSelectionOperationItem, error) {
	item := BehavioralRuleSelectionOperationItem{TargetID: row.ID, Outcome: BehavioralRuleSelectionCommitted}
	updates := map[string]any{"updated_at": now}
	if operation.Action == BehavioralRuleSelectionDelete {
		updates["deleted_at"] = now
		result := tx.Model(&BehavioralRule{}).
			Where("id = ? AND version = ? AND deleted_at IS NULL", row.ID, row.Version).
			Updates(updates)
		if result.Error != nil {
			return BehavioralRuleSelectionOperationItem{}, fmt.Errorf("delete selected behavioral rule id=%d: %w", row.ID, result.Error)
		}
		if result.RowsAffected != 1 {
			return BehavioralRuleSelectionOperationItem{}, ErrBehavioralRuleSelectionConflict
		}
		return item, nil
	}

	updates["version"] = gorm.Expr("version + 1")
	switch operation.Action {
	case BehavioralRuleSelectionEnable:
		updates["enabled"] = true
		row.Enabled = true
	case BehavioralRuleSelectionDisable:
		updates["enabled"] = false
		row.Enabled = false
	case BehavioralRuleSelectionUpdate:
		if operation.Content != nil {
			row.Content = strings.TrimSpace(*operation.Content)
			updates["content"] = row.Content
		}
		if operation.Priority != nil {
			row.Priority = *operation.Priority
			updates["priority"] = row.Priority
		}
	}
	if operation.EditedBy != nil {
		if editedBy := strings.TrimSpace(*operation.EditedBy); editedBy != "" {
			row.EditedBy = editedBy
			updates["edited_by"] = editedBy
		}
	}
	result := tx.Model(&BehavioralRule{}).
		Where("id = ? AND version = ? AND deleted_at IS NULL", row.ID, row.Version).
		Updates(updates)
	if result.Error != nil {
		return BehavioralRuleSelectionOperationItem{}, fmt.Errorf("update selected behavioral rule id=%d: %w", row.ID, result.Error)
	}
	if result.RowsAffected != 1 {
		return BehavioralRuleSelectionOperationItem{}, ErrBehavioralRuleSelectionConflict
	}
	row.Version++
	row.UpdatedAt = now
	item.Rule = behavioralRuleRowToModel(row)
	observedVersion := new(int)
	*observedVersion = row.Version
	item.ObservedVersion = observedVersion
	return item, nil
}

func applyBehavioralRuleReorder(tx *gorm.DB, targets []behavioralRuleSelectionTarget, operation BehavioralRuleSelectionOperation, result *BehavioralRuleSelectionOperationResult) error {
	if len(operation.Order) != len(targets) {
		return fmt.Errorf("%w: reorder must include every selected rule", ErrBehavioralRuleSelectionInvalid)
	}
	selected := make(map[int64]uint64, len(targets))
	for _, target := range targets {
		selected[target.id] = target.expectedVersion
	}
	ordered := make(map[int64]BehavioralRuleOrder, len(operation.Order))
	for _, order := range operation.Order {
		if order.RuleID <= 0 || order.ExpectedVersion == 0 {
			return fmt.Errorf("%w: reorder target is invalid", ErrBehavioralRuleSelectionInvalid)
		}
		if _, duplicate := ordered[order.RuleID]; duplicate {
			return fmt.Errorf("%w: reorder target is duplicated", ErrBehavioralRuleSelectionInvalid)
		}
		if expected, selected := selected[order.RuleID]; !selected || expected != order.ExpectedVersion {
			return fmt.Errorf("%w: reorder target is not the selected revision", ErrBehavioralRuleSelectionInvalid)
		}
		ordered[order.RuleID] = order
	}

	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("deleted_at IS NULL")
	if operation.Scope.Project == nil {
		query = query.Where("project IS NULL")
	} else {
		query = query.Where("project = ?", *operation.Scope.Project)
	}
	var rows []BehavioralRule
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return fmt.Errorf("lock behavioral rule reorder scope: %w", err)
	}
	if len(rows) != len(operation.Order) {
		return ErrBehavioralRuleSelectionConflict
	}
	byID := make(map[int64]*BehavioralRule, len(rows))
	for index := range rows {
		row := &rows[index]
		order, found := ordered[row.ID]
		if !found || uint64(row.Version) != order.ExpectedVersion {
			return ErrBehavioralRuleSelectionConflict
		}
		byID[row.ID] = row
	}

	now := time.Now().UTC()
	for index, order := range operation.Order {
		row := byID[order.RuleID]
		priority := (len(operation.Order) - index) * 10
		updates := map[string]any{
			"priority":   priority,
			"updated_at": now,
			"version":    gorm.Expr("version + 1"),
		}
		updated := tx.Model(&BehavioralRule{}).
			Where("id = ? AND version = ? AND deleted_at IS NULL", row.ID, row.Version).
			Updates(updates)
		if updated.Error != nil {
			return fmt.Errorf("reorder behavioral rule id=%d: %w", row.ID, updated.Error)
		}
		if updated.RowsAffected != 1 {
			return ErrBehavioralRuleSelectionConflict
		}
		row.Priority = priority
		row.Version++
		row.UpdatedAt = now
		observedVersion := new(int)
		*observedVersion = row.Version
		result.Items = append(result.Items, BehavioralRuleSelectionOperationItem{
			TargetID:        row.ID,
			Outcome:         BehavioralRuleSelectionCommitted,
			ObservedVersion: observedVersion,
			Rule:            behavioralRuleRowToModel(row),
		})
	}
	return nil
}

func (s *BehavioralRulesStore) readBehavioralRuleSelectionReadback(ctx context.Context, operation BehavioralRuleSelectionOperation, result *BehavioralRuleSelectionOperationResult) {
	for index := range result.Items {
		item := &result.Items[index]
		if item.Outcome != BehavioralRuleSelectionCommitted {
			continue
		}
		if operation.Action == BehavioralRuleSelectionDelete {
			_, err := s.Get(ctx, item.TargetID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				item.Deleted = true
				continue
			}
			item.ReadbackPending = true
			continue
		}
		expected := item.Rule
		current, err := s.Get(ctx, item.TargetID)
		if err != nil || expected == nil || current.Version != expected.Version || current.Content != expected.Content || current.Priority != expected.Priority || current.EditedBy != expected.EditedBy || current.Enabled != expected.Enabled {
			item.ReadbackPending = true
			continue
		}
		item.Rule = current
		observedVersion := new(int)
		*observedVersion = current.Version
		item.ObservedVersion = observedVersion
	}
}
