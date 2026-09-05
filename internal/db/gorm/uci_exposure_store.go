package gorm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
)

var (
	// ErrUCIIdempotencyMismatch deliberately exposes only the closed public code.
	ErrUCIIdempotencyMismatch        = errors.New("IDEMPOTENCY_MISMATCH")
	ErrUCIExposureNotFound           = errors.New("UCI_EXPOSURE_NOT_FOUND")
	ErrUCIEvidenceIntegrity          = errors.New("UCI_EVIDENCE_INTEGRITY_FAILED")
	errUCIExposureStoreNotConfigured = errors.New("uci exposure store not configured")
)

const (
	uciEvidenceBindingVersion = 1
	uciExposureRefPrefix      = "uci-exp_"
	uciEvidenceRetentionLimit = 1000
)

// UCIExposureStore owns the durable evidence lifecycle. It accepts only closed,
// already-authorized metadata; it does not select contexts, grant access, or
// publish Views.
type UCIExposureStore struct {
	db     *gorm.DB
	health uci.ExposureHealthTracker
}

func NewUCIExposureStore(db *gorm.DB) *UCIExposureStore {
	return NewUCIExposureStoreWithHealth(db, uci.NewExposureHealthController(db != nil))
}

// NewUCIExposureStoreWithHealth binds recorder-scoped health without aggregating
// daemon or indexer state.
func NewUCIExposureStoreWithHealth(db *gorm.DB, health uci.ExposureHealthTracker) *UCIExposureStore {
	if health == nil {
		health = uci.NewExposureHealthController(db != nil)
	}
	return &UCIExposureStore{db: db, health: health}
}

// UCIExposureInput is the complete, non-content input to one authorized result receipt.
type UCIExposureInput struct {
	AuthRealm        string
	SourceID         string
	CheckoutID       string
	ViewID           string
	ClientRef        string
	ClientSessionRef string
	RequestRef       string
	OperationKind    UCIExposureOperationKind
	ResultState      UCIExposureResultState
	RetrievalMode    UCIRetrievalMode
	CoverageState    UCICoverageState
	EvidenceSource   UCIEvidenceSource
	Certainty        UCICertainty
	IdempotencyKey   string
	RecordedAt       time.Time
}

// UCICompletionInput is supplied only after a caller has verified its supported-host capability.
// The opaque exposure ref is resolved to its server UUID before binding the completion digest.
type UCICompletionInput struct {
	ExposureRef      string
	SupportedHostRef string
	CallbackRef      string
	Outcome          UCICompletionOutcome
	IdempotencyKey   string
	OccurredAt       time.Time
}

// UCIEvidencePruneResult reports the bounded recorder-owned retention transaction.
type UCIEvidencePruneResult struct {
	CompletionsDeleted int64
	ExposuresDeleted   int64
}

// RecordExposure appends one exposure, returning the stored original row for an exact retry.
// A key reuse with any different canonical binding returns only ErrUCIIdempotencyMismatch.
func (s *UCIExposureStore) RecordExposure(ctx context.Context, in UCIExposureInput) (exposure *UCIExposure, err error) {
	attempted := false
	defer func() {
		s.observeInitialExposureResult(attempted, err)
	}()

	if err := s.requireDB("record exposure"); err != nil {
		s.markInitialExposureFailure()
		return nil, err
	}
	normalized, err := normalizeUCIExposureInput(in)
	if err != nil {
		return nil, err
	}
	digest, err := CanonicalUCIExposureBindingDigest(normalized)
	if err != nil {
		return nil, err
	}

	attempted = true
	existing, found, err := s.findExposureByIdempotency(ctx, normalized.AuthRealm, normalized.ClientSessionRef, normalized.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if found {
		return sameUCIExposureDigest(existing, digest)
	}

	row := &UCIExposure{
		ExposureID:               uuid.NewString(),
		ExposureRef:              uciExposureRefPrefix + uuid.NewString(),
		AuthRealm:                normalized.AuthRealm,
		SourceID:                 normalized.SourceID,
		CheckoutID:               normalized.CheckoutID,
		ViewID:                   normalized.ViewID,
		ClientRef:                normalized.ClientRef,
		ClientSessionRef:         normalized.ClientSessionRef,
		RequestRef:               normalized.RequestRef,
		OperationKind:            normalized.OperationKind,
		ResultState:              normalized.ResultState,
		RetrievalMode:            normalized.RetrievalMode,
		CoverageState:            normalized.CoverageState,
		EvidenceSource:           normalized.EvidenceSource,
		Certainty:                normalized.Certainty,
		IdempotencyKey:           normalized.IdempotencyKey,
		IdempotencyBindingDigest: digest,
		RecordedAt:               normalized.RecordedAt,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err == nil {
		return row, nil
	} else {
		// A competing identical retry can win between the preflight lookup and INSERT.
		// Resolve only through the exact unique key; an unrelated write failure remains visible.
		existing, found, lookupErr := s.findExposureByIdempotency(ctx, normalized.AuthRealm, normalized.ClientSessionRef, normalized.IdempotencyKey)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if found {
			return sameUCIExposureDigest(existing, digest)
		}
		return nil, fmt.Errorf("uci exposure record: %w", err)
	}
}

// RecordCompletion appends a verified supported-host callback evidence row.
func (s *UCIExposureStore) RecordCompletion(ctx context.Context, in UCICompletionInput) (completion *UCICompletionEvidence, err error) {
	attempted := false
	defer func() {
		s.observeCompletionResult(attempted, err)
	}()

	if err := s.requireDB("record completion"); err != nil {
		if s != nil && s.health != nil {
			s.health.RecordCompletionFailure()
		}
		return nil, err
	}
	if !isUCIExposureRef(in.ExposureRef) {
		return nil, fmt.Errorf("uci completion: exposure_ref must be an opaque UCI exposure reference")
	}
	normalized, err := normalizeUCICompletionInput(in)
	if err != nil {
		return nil, err
	}

	attempted = true
	var exposure UCIExposure
	if err := s.db.WithContext(ctx).Where("exposure_ref = ?", normalized.ExposureRef).First(&exposure).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUCIExposureNotFound
		}
		return nil, fmt.Errorf("uci completion resolve exposure: %w", err)
	}
	digest, err := canonicalUCICompletionBindingDigest(exposure.ExposureID, normalized)
	if err != nil {
		return nil, err
	}

	existing, found, err := s.findCompletionByIdempotency(ctx, exposure.ExposureID, normalized.SupportedHostRef, normalized.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if found {
		return sameUCICompletionDigest(existing, digest)
	}

	row := &UCICompletionEvidence{
		CompletionEvidenceID:     uuid.NewString(),
		ExposureID:               exposure.ExposureID,
		SupportedHostRef:         normalized.SupportedHostRef,
		CallbackRef:              normalized.CallbackRef,
		Outcome:                  normalized.Outcome,
		IdempotencyKey:           normalized.IdempotencyKey,
		IdempotencyBindingDigest: digest,
		OccurredAt:               normalized.OccurredAt,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err == nil {
		return row, nil
	} else {
		existing, found, lookupErr := s.findCompletionByIdempotency(ctx, exposure.ExposureID, normalized.SupportedHostRef, normalized.IdempotencyKey)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if found {
			return sameUCICompletionDigest(existing, digest)
		}
		return nil, fmt.Errorf("uci completion record: %w", err)
	}
}

// CompletionState returns unknown when no verified completion child exists.
func (s *UCIExposureStore) CompletionState(ctx context.Context, exposureRef string) (UCICompletionState, error) {
	if err := s.requireDB("completion state"); err != nil {
		return "", err
	}
	if !isUCIExposureRef(exposureRef) {
		return "", fmt.Errorf("uci exposure: exposure_ref must be an opaque UCI exposure reference")
	}

	var exposure UCIExposure
	if err := s.db.WithContext(ctx).Where("exposure_ref = ?", exposureRef).First(&exposure).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrUCIExposureNotFound
		}
		return "", fmt.Errorf("uci exposure completion state: %w", err)
	}
	var completion UCICompletionEvidence
	err := s.db.WithContext(ctx).
		Where("exposure_id = ?", exposure.ExposureID).
		Order("occurred_at DESC, completion_evidence_id DESC").
		First(&completion).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return UCICompletionUnknown, nil
	}
	if err != nil {
		return "", fmt.Errorf("uci exposure completion state: %w", err)
	}
	if err := validateStoredUCICompletion(completion); err != nil {
		return "", ErrUCIEvidenceIntegrity
	}
	return UCICompletionState(completion.Outcome), nil
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
func (s *UCIExposureStore) VerifyIntegrity(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			s.markIntegrityFailure()
		}
	}()

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
		if !isUCIExposureRef(exposure.ExposureRef) {
			return ErrUCIEvidenceIntegrity
		}
		digest, err := CanonicalUCIExposureBindingDigest(UCIExposureInput{
			AuthRealm:        exposure.AuthRealm,
			SourceID:         exposure.SourceID,
			CheckoutID:       exposure.CheckoutID,
			ViewID:           exposure.ViewID,
			ClientRef:        exposure.ClientRef,
			ClientSessionRef: exposure.ClientSessionRef,
			RequestRef:       exposure.RequestRef,
			OperationKind:    exposure.OperationKind,
			ResultState:      exposure.ResultState,
			RetrievalMode:    exposure.RetrievalMode,
			CoverageState:    exposure.CoverageState,
			EvidenceSource:   exposure.EvidenceSource,
			Certainty:        exposure.Certainty,
			IdempotencyKey:   exposure.IdempotencyKey,
			RecordedAt:       exposure.RecordedAt,
		})
		if err != nil || digest != exposure.IdempotencyBindingDigest {
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
		if err := validateStoredUCICompletion(completion); err != nil {
			return ErrUCIEvidenceIntegrity
		}
		digest, err := canonicalUCICompletionBindingDigest(completion.ExposureID, UCICompletionInput{
			SupportedHostRef: completion.SupportedHostRef,
			CallbackRef:      completion.CallbackRef,
			Outcome:          completion.Outcome,
			IdempotencyKey:   completion.IdempotencyKey,
			OccurredAt:       completion.OccurredAt,
		})
		if err != nil || digest != completion.IdempotencyBindingDigest {
			return ErrUCIEvidenceIntegrity
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("uci evidence integrity iterate completions: %w", err)
	}
	return nil
}

// CanonicalUCIExposureBindingDigest computes the versioned canonical non-content binding.
func CanonicalUCIExposureBindingDigest(in UCIExposureInput) (string, error) {
	normalized, err := normalizeUCIExposureInput(in)
	if err != nil {
		return "", err
	}
	return uciCanonicalBindingDigest(map[string]any{
		"auth_realm":         normalized.AuthRealm,
		"certainty":          normalized.Certainty,
		"checkout_id":        normalized.CheckoutID,
		"client_ref":         normalized.ClientRef,
		"client_session_ref": normalized.ClientSessionRef,
		"coverage_state":     normalized.CoverageState,
		"evidence_source":    normalized.EvidenceSource,
		"idempotency_key":    normalized.IdempotencyKey,
		"kind":               "exposure",
		"operation_kind":     normalized.OperationKind,
		"request_ref":        normalized.RequestRef,
		"result_state":       normalized.ResultState,
		"retrieval_mode":     normalized.RetrievalMode,
		"source_id":          normalized.SourceID,
		"version":            uciEvidenceBindingVersion,
		"view_id":            normalized.ViewID,
	})
}

func canonicalUCICompletionBindingDigest(exposureID string, in UCICompletionInput) (string, error) {
	if err := validateUCIUUID("exposure_id", exposureID); err != nil {
		return "", err
	}
	normalized, err := normalizeUCICompletionInput(in)
	if err != nil {
		return "", err
	}
	return uciCanonicalBindingDigest(map[string]any{
		"callback_ref":       normalized.CallbackRef,
		"exposure_id":        exposureID,
		"idempotency_key":    normalized.IdempotencyKey,
		"kind":               "completion",
		"outcome":            normalized.Outcome,
		"supported_host_ref": normalized.SupportedHostRef,
		"version":            uciEvidenceBindingVersion,
	})
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
		return nil, ErrUCIIdempotencyMismatch
	}
	return existing, nil
}

func sameUCICompletionDigest(existing *UCICompletionEvidence, digest string) (*UCICompletionEvidence, error) {
	if existing.IdempotencyBindingDigest != digest {
		return nil, ErrUCIIdempotencyMismatch
	}
	return existing, nil
}

func normalizeUCIExposureInput(in UCIExposureInput) (UCIExposureInput, error) {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"auth_realm", in.AuthRealm},
		{"client_ref", in.ClientRef},
		{"client_session_ref", in.ClientSessionRef},
		{"request_ref", in.RequestRef},
		{"idempotency_key", in.IdempotencyKey},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return UCIExposureInput{}, err
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"source_id", in.SourceID},
		{"checkout_id", in.CheckoutID},
		{"view_id", in.ViewID},
	} {
		if err := validateUCIUUID(field.name, field.value); err != nil {
			return UCIExposureInput{}, err
		}
	}
	if !isUCIExposureOperationKind(in.OperationKind) {
		return UCIExposureInput{}, fmt.Errorf("uci exposure: unsupported operation_kind %q", in.OperationKind)
	}
	if !isUCIExposureResultState(in.ResultState) {
		return UCIExposureInput{}, fmt.Errorf("uci exposure: unsupported result_state %q", in.ResultState)
	}
	if !isUCIRetrievalMode(in.RetrievalMode) {
		return UCIExposureInput{}, fmt.Errorf("uci exposure: unsupported retrieval_mode %q", in.RetrievalMode)
	}
	if !isUCICoverageState(in.CoverageState) {
		return UCIExposureInput{}, fmt.Errorf("uci exposure: unsupported coverage_state %q", in.CoverageState)
	}
	if !isUCIEvidenceSource(in.EvidenceSource) {
		return UCIExposureInput{}, fmt.Errorf("uci exposure: unsupported evidence_source %q", in.EvidenceSource)
	}
	if !isUCICertainty(in.Certainty) {
		return UCIExposureInput{}, fmt.Errorf("uci exposure: unsupported certainty %q", in.Certainty)
	}
	if in.RecordedAt.IsZero() {
		in.RecordedAt = time.Now().UTC()
	} else {
		in.RecordedAt = in.RecordedAt.UTC()
	}
	return in, nil
}

func normalizeUCICompletionInput(in UCICompletionInput) (UCICompletionInput, error) {
	if in.ExposureRef != "" && !isUCIExposureRef(in.ExposureRef) {
		return UCICompletionInput{}, fmt.Errorf("uci completion: exposure_ref must be an opaque UCI exposure reference")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"supported_host_ref", in.SupportedHostRef},
		{"callback_ref", in.CallbackRef},
		{"idempotency_key", in.IdempotencyKey},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return UCICompletionInput{}, err
		}
	}
	if !isUCICompletionOutcome(in.Outcome) {
		return UCICompletionInput{}, fmt.Errorf("uci completion: unsupported outcome %q", in.Outcome)
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	} else {
		in.OccurredAt = in.OccurredAt.UTC()
	}
	return in, nil
}

func validateStoredUCICompletion(completion UCICompletionEvidence) error {
	if err := validateUCIUUID("completion_evidence_id", completion.CompletionEvidenceID); err != nil {
		return err
	}
	if err := validateUCIUUID("exposure_id", completion.ExposureID); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"supported_host_ref", completion.SupportedHostRef},
		{"callback_ref", completion.CallbackRef},
		{"idempotency_key", completion.IdempotencyKey},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return err
		}
	}
	if !isUCICompletionOutcome(completion.Outcome) || !isUCIDigest(completion.IdempotencyBindingDigest) {
		return ErrUCIEvidenceIntegrity
	}
	return nil
}

func isUCIExposureRef(value string) bool {
	if !strings.HasPrefix(value, uciExposureRefPrefix) {
		return false
	}
	parsed, err := uuid.Parse(strings.TrimPrefix(value, uciExposureRefPrefix))
	return err == nil && parsed != uuid.Nil && parsed.String() == strings.TrimPrefix(value, uciExposureRefPrefix)
}

func uciCanonicalBindingDigest(payload map[string]any) (string, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return "", fmt.Errorf("canonicalize UCI evidence binding: %w", err)
	}
	canonical := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (s *UCIExposureStore) observeInitialExposureResult(attempted bool, err error) {
	if !attempted || s == nil || s.health == nil {
		return
	}
	if err == nil {
		s.health.RecordInitialExposureSuccess()
		return
	}
	if errors.Is(err, ErrUCIIdempotencyMismatch) {
		s.health.RecordIdempotencyMismatch()
		return
	}
	s.health.RecordInitialExposureFailure()
}

func (s *UCIExposureStore) observeCompletionResult(attempted bool, err error) {
	if !attempted || s == nil || s.health == nil {
		return
	}
	if err == nil {
		s.health.RecordCompletionSuccess()
		return
	}
	if errors.Is(err, ErrUCIIdempotencyMismatch) {
		s.health.RecordIdempotencyMismatch()
		return
	}
	if errors.Is(err, ErrUCIExposureNotFound) {
		return
	}
	s.health.RecordCompletionFailure()
}

func (s *UCIExposureStore) markInitialExposureFailure() {
	if s != nil && s.health != nil {
		s.health.RecordInitialExposureFailure()
	}
}

func (s *UCIExposureStore) markIntegrityFailure() {
	if s != nil && s.health != nil {
		s.health.RecordIntegrityFailure()
	}
}
