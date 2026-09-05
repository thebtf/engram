package gorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
)

var (
	ErrUCIEvidenceIntegrity          = errors.New("UCI_EVIDENCE_INTEGRITY_FAILED")
	errUCIExposureStoreNotConfigured = errors.New("uci exposure store not configured")
)

const uciEvidenceRetentionLimit = 1000

// UCIExposureStore is the PostgreSQL adapter for the UCI ExposureStore port.
// It receives only already-derived, non-content domain records.
type UCIExposureStore struct {
	db *gorm.DB
}

var _ uci.ExposureStore = (*UCIExposureStore)(nil)

func NewUCIExposureStore(db *gorm.DB) *UCIExposureStore {
	return &UCIExposureStore{db: db}
}

// UCIEvidencePruneResult reports the bounded recorder-owned retention transaction.
type UCIEvidencePruneResult struct {
	CompletionsDeleted int64
	ExposuresDeleted   int64
}

// AppendExposure persists one canonical non-content record. An exact retry
// returns the original record; any changed binding returns only the closed
// domain mismatch error.
func (s *UCIExposureStore) AppendExposure(ctx context.Context, record uci.ExposureRecord) (uci.ExposureRecord, error) {
	if err := s.requireDB("append exposure"); err != nil {
		return uci.ExposureRecord{}, err
	}
	if err := record.Validate(); err != nil {
		return uci.ExposureRecord{}, err
	}

	existing, found, err := s.findExposureByIdempotency(ctx, record.AuthRealm, record.ClientSessionRef, record.IdempotencyKey)
	if err != nil {
		return uci.ExposureRecord{}, err
	}
	if found {
		matched, err := sameUCIExposureDigest(existing, record.BindingDigest)
		if err != nil {
			return uci.ExposureRecord{}, err
		}
		return exposureRecordFromModel(*matched), nil
	}

	row := exposureModelFromRecord(record)
	if err := s.db.WithContext(ctx).Create(&row).Error; err == nil {
		return exposureRecordFromModel(row), nil
	} else {
		// A competing exact retry can win between lookup and INSERT. Resolve only
		// through the full idempotency scope; unrelated database failures remain
		// visible to the domain recorder as an unavailable append.
		existing, found, lookupErr := s.findExposureByIdempotency(ctx, record.AuthRealm, record.ClientSessionRef, record.IdempotencyKey)
		if lookupErr != nil {
			return uci.ExposureRecord{}, lookupErr
		}
		if found {
			matched, matchErr := sameUCIExposureDigest(existing, record.BindingDigest)
			if matchErr != nil {
				return uci.ExposureRecord{}, matchErr
			}
			return exposureRecordFromModel(*matched), nil
		}
		return uci.ExposureRecord{}, fmt.Errorf("uci exposure append: %w", err)
	}
}

// AppendCompletion persists one child supplied only through the domain's
// VerifiedSupportedHostCallback path.
func (s *UCIExposureStore) AppendCompletion(ctx context.Context, evidence uci.CompletionEvidence) (uci.CompletionEvidence, error) {
	if err := s.requireDB("append completion"); err != nil {
		return uci.CompletionEvidence{}, err
	}
	if err := evidence.Validate(); err != nil {
		return uci.CompletionEvidence{}, err
	}

	var exposure UCIExposure
	if err := s.db.WithContext(ctx).Where("exposure_ref = ?", evidence.ExposureRef).First(&exposure).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uci.CompletionEvidence{}, uci.ErrExposureNotFound
		}
		return uci.CompletionEvidence{}, fmt.Errorf("uci completion resolve exposure: %w", err)
	}

	existing, found, err := s.findCompletionByIdempotency(ctx, exposure.ExposureID, evidence.SupportedHostRef, evidence.IdempotencyKey)
	if err != nil {
		return uci.CompletionEvidence{}, err
	}
	if found {
		matched, err := sameUCICompletionDigest(existing, evidence.BindingDigest)
		if err != nil {
			return uci.CompletionEvidence{}, err
		}
		return completionEvidenceFromModel(*matched, evidence.ExposureRef), nil
	}

	row := UCICompletionEvidence{
		CompletionEvidenceID:     uuid.NewString(),
		ExposureID:               exposure.ExposureID,
		SupportedHostRef:         evidence.SupportedHostRef,
		CallbackRef:              evidence.CallbackRef,
		Outcome:                  UCICompletionOutcome(evidence.Outcome),
		IdempotencyKey:           evidence.IdempotencyKey,
		IdempotencyBindingDigest: evidence.BindingDigest,
		OccurredAt:               evidence.OccurredAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err == nil {
		return completionEvidenceFromModel(row, evidence.ExposureRef), nil
	} else {
		existing, found, lookupErr := s.findCompletionByIdempotency(ctx, exposure.ExposureID, evidence.SupportedHostRef, evidence.IdempotencyKey)
		if lookupErr != nil {
			return uci.CompletionEvidence{}, lookupErr
		}
		if found {
			matched, matchErr := sameUCICompletionDigest(existing, evidence.BindingDigest)
			if matchErr != nil {
				return uci.CompletionEvidence{}, matchErr
			}
			return completionEvidenceFromModel(*matched, evidence.ExposureRef), nil
		}
		return uci.CompletionEvidence{}, fmt.Errorf("uci completion append: %w", err)
	}
}

// CompletionState returns unknown when no verified completion child exists.
func (s *UCIExposureStore) CompletionState(ctx context.Context, exposureRef string) (uci.QueryCompletionState, error) {
	if err := s.requireDB("completion state"); err != nil {
		return "", err
	}
	if !uci.ValidExposureRef(exposureRef) {
		return "", fmt.Errorf("uci exposure: exposure_ref must be an opaque UCI exposure reference")
	}

	var exposure UCIExposure
	if err := s.db.WithContext(ctx).Where("exposure_ref = ?", exposureRef).First(&exposure).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", uci.ErrExposureNotFound
		}
		return "", fmt.Errorf("uci exposure completion state: %w", err)
	}
	var completion UCICompletionEvidence
	err := s.db.WithContext(ctx).
		Where("exposure_id = ?", exposure.ExposureID).
		Order("occurred_at DESC, completion_evidence_id DESC").
		First(&completion).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return uci.QueryCompletionUnknown, nil
	}
	if err != nil {
		return "", fmt.Errorf("uci exposure completion state: %w", err)
	}
	if err := completionEvidenceFromModel(completion, exposureRef).Validate(); err != nil {
		return "", ErrUCIEvidenceIntegrity
	}
	return uci.QueryCompletionState(completion.Outcome), nil
}

// PruneExpired invokes the database-owned bounded retention function. The function
// deletes completion children before their expired parents and cannot update evidence.
func (s *UCIExposureStore) PruneExpired(ctx context.Context, before time.Time, limit int) (UCIEvidencePruneResult, error) {
	if err := s.requireDB("prune evidence"); err != nil {
		return UCIEvidencePruneResult{}, err
	}
	if before.IsZero() {
		return UCIEvidencePruneResult{}, fmt.Errorf("uci evidence prune: before must be set")
	}
	if limit < 1 || limit > uciEvidenceRetentionLimit {
		return UCIEvidencePruneResult{}, fmt.Errorf("uci evidence prune: limit must be between 1 and %d", uciEvidenceRetentionLimit)
	}

	var row struct {
		CompletionsDeleted int64 `gorm:"column:completions_deleted"`
		ExposuresDeleted   int64 `gorm:"column:exposures_deleted"`
	}
	if err := s.db.WithContext(ctx).
		Raw(`SELECT completions_deleted, exposures_deleted FROM uci_prune_evidence(?, ?)`, before.UTC(), limit).
		Scan(&row).Error; err != nil {
		return UCIEvidencePruneResult{}, fmt.Errorf("uci evidence prune: %w", err)
	}
	return UCIEvidencePruneResult{CompletionsDeleted: row.CompletionsDeleted, ExposuresDeleted: row.ExposuresDeleted}, nil
}

// VerifyIntegrity is the normal PostgreSQL backup/restore verification path for
// durable UCI evidence. It checks stored canonical digests, closed values, tuple
// reachability, completion parents, and the database append-only guards.
func (s *UCIExposureStore) VerifyIntegrity(ctx context.Context) error {
	if err := s.requireDB("verify evidence integrity"); err != nil {
		return err
	}

	if err := s.verifyExposureDigests(ctx); err != nil {
		return err
	}
	if err := s.verifyCompletionDigests(ctx); err != nil {
		return err
	}

	var brokenTupleCount int64
	if err := s.db.WithContext(ctx).Raw(`
        SELECT COUNT(*)
        FROM uci_exposures AS exposure
        LEFT JOIN ci_views AS view_row
            ON view_row.view_id = exposure.view_id
            AND view_row.checkout_id = exposure.checkout_id
            AND view_row.source_id = exposure.source_id
        LEFT JOIN sources AS source_row
            ON source_row.source_id = exposure.source_id
            AND source_row.auth_realm = exposure.auth_realm
        WHERE view_row.view_id IS NULL OR source_row.source_id IS NULL
    `).Scan(&brokenTupleCount).Error; err != nil {
		return fmt.Errorf("uci evidence integrity check exposure tuple: %w", err)
	}
	if brokenTupleCount != 0 {
		return ErrUCIEvidenceIntegrity
	}

	var orphanCompletionCount int64
	if err := s.db.WithContext(ctx).Raw(`
        SELECT COUNT(*)
        FROM uci_completion_evidence AS completion
        LEFT JOIN uci_exposures AS exposure ON exposure.exposure_id = completion.exposure_id
        WHERE exposure.exposure_id IS NULL
    `).Scan(&orphanCompletionCount).Error; err != nil {
		return fmt.Errorf("uci evidence integrity check completion parent: %w", err)
	}
	if orphanCompletionCount != 0 {
		return ErrUCIEvidenceIntegrity
	}

	var guardCount int64
	if err := s.db.WithContext(ctx).Raw(`
        SELECT COUNT(*)
        FROM pg_trigger AS trigger_row
        JOIN pg_class AS relation ON relation.oid = trigger_row.tgrelid
        JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
        WHERE namespace.nspname = current_schema()
            AND relation.relname IN ('uci_exposures', 'uci_completion_evidence')
            AND trigger_row.tgname IN ('uci_exposures_append_only_guard', 'uci_completion_evidence_append_only_guard')
            AND trigger_row.tgenabled <> 'D'
            AND pg_get_triggerdef(trigger_row.oid) LIKE '%uci_evidence_reject_mutation%'
            AND NOT trigger_row.tgisinternal
    `).Scan(&guardCount).Error; err != nil {
		return fmt.Errorf("uci evidence integrity check append-only guard: %w", err)
	}
	if guardCount != 2 {
		return ErrUCIEvidenceIntegrity
	}
	return nil
}

func (s *UCIExposureStore) verifyExposureDigests(ctx context.Context) error {
	rows, err := s.db.WithContext(ctx).Model(&UCIExposure{}).Order("exposure_id ASC").Rows()
	if err != nil {
		return fmt.Errorf("uci evidence integrity load exposures: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var exposure UCIExposure
		if err := s.db.ScanRows(rows, &exposure); err != nil {
			return fmt.Errorf("uci evidence integrity scan exposure: %w", err)
		}
		if !validEvidenceUUID(exposure.ExposureID) || exposureRecordFromModel(exposure).Validate() != nil {
			return ErrUCIEvidenceIntegrity
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("uci evidence integrity iterate exposures: %w", err)
	}
	return nil
}

func (s *UCIExposureStore) verifyCompletionDigests(ctx context.Context) error {
	rows, err := s.db.WithContext(ctx).Model(&UCICompletionEvidence{}).Order("completion_evidence_id ASC").Rows()
	if err != nil {
		return fmt.Errorf("uci evidence integrity load completions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var completion UCICompletionEvidence
		if err := s.db.ScanRows(rows, &completion); err != nil {
			return fmt.Errorf("uci evidence integrity scan completion: %w", err)
		}
		if !validEvidenceUUID(completion.CompletionEvidenceID) || !validEvidenceUUID(completion.ExposureID) {
			return ErrUCIEvidenceIntegrity
		}
		var exposure UCIExposure
		if err := s.db.WithContext(ctx).Select("exposure_id", "exposure_ref").Where("exposure_id = ?", completion.ExposureID).First(&exposure).Error; err != nil {
			return ErrUCIEvidenceIntegrity
		}
		if err := completionEvidenceFromModel(completion, exposure.ExposureRef).Validate(); err != nil {
			return ErrUCIEvidenceIntegrity
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("uci evidence integrity iterate completions: %w", err)
	}
	return nil
}

func (s *UCIExposureStore) findExposureByIdempotency(ctx context.Context, authRealm, clientSessionRef, idempotencyKey string) (*UCIExposure, bool, error) {
	var row UCIExposure
	err := s.db.WithContext(ctx).
		Where("auth_realm = ? AND client_session_ref = ? AND idempotency_key = ?", authRealm, clientSessionRef, idempotencyKey).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("uci exposure lookup idempotency: %w", err)
	}
	return &row, true, nil
}

func (s *UCIExposureStore) findCompletionByIdempotency(ctx context.Context, exposureID, supportedHostRef, idempotencyKey string) (*UCICompletionEvidence, bool, error) {
	var row UCICompletionEvidence
	err := s.db.WithContext(ctx).
		Where("exposure_id = ? AND supported_host_ref = ? AND idempotency_key = ?", exposureID, supportedHostRef, idempotencyKey).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("uci completion lookup idempotency: %w", err)
	}
	return &row, true, nil
}

func (s *UCIExposureStore) requireDB(operation string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("uci exposure %s: %w", operation, errUCIExposureStoreNotConfigured)
	}
	return nil
}

func sameUCIExposureDigest(existing *UCIExposure, digest string) (*UCIExposure, error) {
	if existing.IdempotencyBindingDigest != digest {
		return nil, uci.ErrIdempotencyMismatch
	}
	return existing, nil
}

func sameUCICompletionDigest(existing *UCICompletionEvidence, digest string) (*UCICompletionEvidence, error) {
	if existing.IdempotencyBindingDigest != digest {
		return nil, uci.ErrIdempotencyMismatch
	}
	return existing, nil
}

func exposureModelFromRecord(record uci.ExposureRecord) UCIExposure {
	return UCIExposure{
		ExposureID:               uuid.NewString(),
		ExposureRef:              uci.NewExposureRef(),
		AuthRealm:                record.AuthRealm,
		SourceID:                 record.SourceID,
		CheckoutID:               record.CheckoutID,
		ViewID:                   record.ViewID,
		ClientRef:                record.ClientRef,
		ClientSessionRef:         record.ClientSessionRef,
		RequestRef:               record.RequestRef,
		OperationKind:            UCIExposureOperationKind(record.Operation),
		ResultState:              UCIExposureResultState(record.Result),
		RetrievalMode:            UCIRetrievalMode(record.Retrieval),
		CoverageState:            UCICoverageState(record.Coverage),
		EvidenceSource:           UCIEvidenceSource(record.Evidence),
		Certainty:                UCICertainty(record.Certainty),
		IdempotencyKey:           record.IdempotencyKey,
		IdempotencyBindingDigest: record.BindingDigest,
		RecordedAt:               record.RecordedAt.UTC(),
	}
}

func exposureRecordFromModel(model UCIExposure) uci.ExposureRecord {
	return uci.ExposureRecord{
		ExposureRef:      model.ExposureRef,
		AuthRealm:        model.AuthRealm,
		SourceID:         model.SourceID,
		CheckoutID:       model.CheckoutID,
		ViewID:           model.ViewID,
		ClientRef:        model.ClientRef,
		ClientSessionRef: model.ClientSessionRef,
		RequestRef:       model.RequestRef,
		Operation:        uci.ExposureOperation(model.OperationKind),
		Result:           uci.ExposureResultState(model.ResultState),
		Retrieval:        uci.ExposureRetrievalMode(model.RetrievalMode),
		Coverage:         uci.ExposureCoverageState(model.CoverageState),
		Evidence:         uci.ExposureEvidenceSource(model.EvidenceSource),
		Certainty:        uci.ExposureCertainty(model.Certainty),
		IdempotencyKey:   model.IdempotencyKey,
		BindingDigest:    model.IdempotencyBindingDigest,
		RecordedAt:       model.RecordedAt.UTC(),
	}
}

func completionEvidenceFromModel(model UCICompletionEvidence, exposureRef string) uci.CompletionEvidence {
	return uci.CompletionEvidence{
		ExposureRef:      exposureRef,
		SupportedHostRef: model.SupportedHostRef,
		CallbackRef:      model.CallbackRef,
		Outcome:          uci.CompletionOutcome(model.Outcome),
		IdempotencyKey:   model.IdempotencyKey,
		BindingDigest:    model.IdempotencyBindingDigest,
		OccurredAt:       model.OccurredAt.UTC(),
	}
}

func validEvidenceUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}
