package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/projectidentity"
	"gorm.io/gorm/clause"
)

var (
	errProjectIdentityComparisonStoreUnavailable = errors.New("V3 project identity comparison store is unavailable")
	errProjectIdentityComparisonInvalid          = errors.New("invalid V3 project identity comparison")
	errProjectIdentityComparisonReplayConflict   = errors.New("conflicting V3 project identity comparison replay")
)

// RecordComparisonV3 durably records a validated telemetry observation. The
// unique idempotency key returns the first record for an identical replay and
// rejects a conflicting observation; this method never reads or mutates a
// project, binding, merge, or V2 row.
func (store *Store) RecordComparisonV3(ctx context.Context, observation projectidentity.ComparisonObservationV3) (projectidentity.ComparisonReceiptV3, error) {
	if store == nil || store.DB == nil {
		return projectidentity.ComparisonReceiptV3{}, errProjectIdentityComparisonStoreUnavailable
	}
	if !observation.Valid() {
		return projectidentity.ComparisonReceiptV3{}, errProjectIdentityComparisonInvalid
	}

	candidate := ProjectIdentityComparison{
		ComparisonID:        uuid.NewString(),
		IdempotencyKey:      observation.IdempotencyKey,
		Correlation:         string(observation.Correlation),
		V3Outcome:           string(observation.V3Outcome),
		LegacyOutcome:       string(observation.LegacyOutcome),
		Classification:      string(observation.Classification()),
		ClientInstanceID:    observation.ClientInstanceID,
		Transport:           string(observation.Transport),
		Scope:               string(observation.Scope),
		Freshness:           string(observation.Freshness),
		EvidenceFingerprint: observation.EvidenceFingerprint,
	}
	result := store.DB.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "idempotency_key"}}, DoNothing: true}).
		Create(&candidate)
	if result.Error != nil {
		return projectidentity.ComparisonReceiptV3{}, fmt.Errorf("record V3 project identity comparison: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var persisted ProjectIdentityComparison
		if err := store.DB.WithContext(ctx).Where("idempotency_key = ?", observation.IdempotencyKey).First(&persisted).Error; err != nil {
			return projectidentity.ComparisonReceiptV3{}, fmt.Errorf("load idempotent V3 project identity comparison: %w", err)
		}
		candidate = persisted
		if !comparisonRecordMatchesObservation(candidate, observation) {
			return projectidentity.ComparisonReceiptV3{}, errProjectIdentityComparisonReplayConflict
		}
	}
	return comparisonReceiptV3(candidate), nil
}

func comparisonRecordMatchesObservation(record ProjectIdentityComparison, observation projectidentity.ComparisonObservationV3) bool {
	return record.IdempotencyKey == observation.IdempotencyKey &&
		record.Correlation == string(observation.Correlation) &&
		record.V3Outcome == string(observation.V3Outcome) &&
		record.LegacyOutcome == string(observation.LegacyOutcome) &&
		record.Classification == string(observation.Classification()) &&
		record.ClientInstanceID == observation.ClientInstanceID &&
		record.Transport == string(observation.Transport) &&
		record.Scope == string(observation.Scope) &&
		record.Freshness == string(observation.Freshness) &&
		record.EvidenceFingerprint == observation.EvidenceFingerprint
}

func comparisonReceiptV3(record ProjectIdentityComparison) projectidentity.ComparisonReceiptV3 {
	return projectidentity.ComparisonReceiptV3{
		RecordID:            record.ComparisonID,
		IdempotencyKey:      record.IdempotencyKey,
		Correlation:         projectidentity.CorrelationV3(record.Correlation),
		V3Outcome:           projectidentity.ResolutionOutcomeV3(record.V3Outcome),
		LegacyOutcome:       projectidentity.LegacyComparisonOutcomeV2(record.LegacyOutcome),
		Classification:      projectidentity.ComparisonClassV3(record.Classification),
		ClientInstanceID:    record.ClientInstanceID,
		Transport:           projectidentity.ComparisonTransportV3(record.Transport),
		Scope:               projectidentity.ComparisonScopeV3(record.Scope),
		Freshness:           projectidentity.ComparisonFreshnessV3(record.Freshness),
		EvidenceFingerprint: record.EvidenceFingerprint,
		RecordedAt:          record.CreatedAt,
	}
}

// ObserveLegacyOutcomeV2 performs a bounded SELECT over existing inventoried
// identifiers. It returns only a redacted outcome and never returns, locks,
// caches, creates, or mutates a project identity row.
func (store *Store) ObserveLegacyOutcomeV2(ctx context.Context, descriptor projectidentity.LegacyComparisonDescriptorV3) (projectidentity.LegacyComparisonOutcomeV2, error) {
	if store == nil || store.DB == nil {
		return projectidentity.LegacyComparisonUnavailableV2, errProjectIdentityComparisonStoreUnavailable
	}
	identifiers := descriptor.LegacyIdentifiers()
	if len(identifiers) == 0 {
		return projectidentity.LegacyComparisonUnavailableV2, nil
	}
	conditions := make([]string, 0, len(identifiers))
	arguments := make([]any, 0, len(identifiers)*2)
	for _, identifier := range identifiers {
		conditions = append(conditions, "(scheme = ? AND normalized_value = ?)")
		arguments = append(arguments, string(identifier.Scheme), identifier.Value)
	}
	var ownerCount int64
	result := store.DB.WithContext(ctx).
		Model(&ProjectIdentifier{}).
		Select("COUNT(DISTINCT project_key)").
		Where("status IN ?", []string{"active", "redirected"}).
		Where(strings.Join(conditions, " OR "), arguments...).
		Scan(&ownerCount)
	if result.Error != nil {
		return projectidentity.LegacyComparisonUnavailableV2, fmt.Errorf("observe V2 project identity comparison: %w", result.Error)
	}
	if ownerCount == 1 {
		return projectidentity.LegacyComparisonResolvedV2, nil
	}
	return projectidentity.LegacyComparisonRefusalV2, nil
}

// ReadComparisonsByCorrelationV3 returns only persisted redacted observations.
// A correlation may deliberately have multiple rows; exact-cardinality checks
// belong to the receipt boundary that knows the requested evidence set.
func (store *Store) ReadComparisonsByCorrelationV3(ctx context.Context, correlations []projectidentity.CorrelationV3) ([]projectidentity.ComparisonObservationV3, error) {
	if store == nil || store.DB == nil {
		return nil, errProjectIdentityComparisonStoreUnavailable
	}
	values := make([]string, len(correlations))
	for index, correlation := range correlations {
		if _, err := projectidentity.NewCorrelationV3(string(correlation)); err != nil {
			return nil, errProjectIdentityComparisonInvalid
		}
		values[index] = string(correlation)
	}
	if len(values) == 0 {
		return []projectidentity.ComparisonObservationV3{}, nil
	}
	var records []ProjectIdentityComparison
	if err := store.DB.WithContext(ctx).Where("correlation IN ?", values).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("read V3 project identity comparisons: %w", err)
	}
	observations := make([]projectidentity.ComparisonObservationV3, 0, len(records))
	for _, record := range records {
		observation := comparisonObservationV3(record)
		if !observation.Valid() {
			return nil, errProjectIdentityComparisonInvalid
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func comparisonObservationV3(record ProjectIdentityComparison) projectidentity.ComparisonObservationV3 {
	return projectidentity.ComparisonObservationV3{
		IdempotencyKey:      record.IdempotencyKey,
		Correlation:         projectidentity.CorrelationV3(record.Correlation),
		V3Outcome:           projectidentity.ResolutionOutcomeV3(record.V3Outcome),
		LegacyOutcome:       projectidentity.LegacyComparisonOutcomeV2(record.LegacyOutcome),
		ClientInstanceID:    record.ClientInstanceID,
		Transport:           projectidentity.ComparisonTransportV3(record.Transport),
		Scope:               projectidentity.ComparisonScopeV3(record.Scope),
		Freshness:           projectidentity.ComparisonFreshnessV3(record.Freshness),
		EvidenceFingerprint: record.EvidenceFingerprint,
	}
}
