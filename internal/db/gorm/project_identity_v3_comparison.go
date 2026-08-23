package gorm

import (
	"context"
	"errors"
	"fmt"

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
		if err := store.DB.WithContext(ctx).Where("idempotency_key = ?", observation.IdempotencyKey).First(&candidate).Error; err != nil {
			return projectidentity.ComparisonReceiptV3{}, fmt.Errorf("load idempotent V3 project identity comparison: %w", err)
		}
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
