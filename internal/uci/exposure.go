package uci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	exposureBindingVersion = 1
	exposureRefPrefix      = "uci-exp_"
)

var (
	// ErrExposureUnavailable is the closed public outcome for an initial
	// durable-exposure append that cannot complete.
	ErrExposureUnavailable = errors.New("EXPOSURE_UNAVAILABLE")
	// ErrIdempotencyMismatch is deliberately non-disclosing: callers receive
	// neither a prior nor a new receipt when a key is rebound.
	ErrIdempotencyMismatch = errors.New("IDEMPOTENCY_MISMATCH")
	// ErrCompletionEvidenceUnavailable is returned only to an explicit
	// supported-host callback whose evidence append cannot complete.
	ErrCompletionEvidenceUnavailable = errors.New("COMPLETION_EVIDENCE_UNAVAILABLE")
	// ErrExposureNotFound is a store-local completion parent lookup failure.
	// ExposureRecorder maps it to the closed callback outcome.
	ErrExposureNotFound = errors.New("UCI_EXPOSURE_NOT_FOUND")
)

// ExposureOperation identifies the persisted evidence operation for an
// authorized UCI result.
type ExposureOperation string

const (
	ExposureOperationCodeSearch    ExposureOperation = "code_search"
	ExposureOperationCodeGraph     ExposureOperation = "code_graph"
	ExposureOperationVersionedRead ExposureOperation = "versioned_read"
)

// BrowserSubject is the narrow canonical browser identity contract. The auth
// package's BrowserSubject implements it without making UCI depend on the
// transport authentication graph. Browser composition supplies this value only
// after authenticating the persisted session user.
type BrowserSubject interface {
	Valid() bool
}

// ExposureResultState describes the closed result decision bound to a receipt.
type ExposureResultState string

const (
	ExposureResultOK          ExposureResultState = "ok"
	ExposureResultEmpty       ExposureResultState = "empty"
	ExposureResultPartial     ExposureResultState = "partial"
	ExposureResultStale       ExposureResultState = "stale"
	ExposureResultUnavailable ExposureResultState = "unavailable"
)

// ExposureRetrievalMode describes the retrieval mechanism behind a result.
type ExposureRetrievalMode string

const (
	ExposureRetrievalExact       ExposureRetrievalMode = "exact"
	ExposureRetrievalLexical     ExposureRetrievalMode = "lexical"
	ExposureRetrievalHybrid      ExposureRetrievalMode = "hybrid"
	ExposureRetrievalGraph       ExposureRetrievalMode = "graph"
	ExposureRetrievalUnavailable ExposureRetrievalMode = "unavailable"
)

// ExposureCoverageState captures the closed structural coverage label.
type ExposureCoverageState string

const (
	ExposureCoverageComplete    ExposureCoverageState = "complete"
	ExposureCoveragePartial     ExposureCoverageState = "partial"
	ExposureCoverageUnavailable ExposureCoverageState = "unavailable"
)

// ExposureEvidenceSource identifies the evidence mechanism without persisting
// result content, query arguments, request bodies, or filesystem locators.
type ExposureEvidenceSource string

const (
	ExposureEvidenceExact  ExposureEvidenceSource = "exact"
	ExposureEvidenceFTS    ExposureEvidenceSource = "fts"
	ExposureEvidenceVector ExposureEvidenceSource = "vector"
	ExposureEvidenceGraph  ExposureEvidenceSource = "graph"
	ExposureEvidenceMixed  ExposureEvidenceSource = "mixed"
	ExposureEvidenceNone   ExposureEvidenceSource = "none"
)

// ExposureCertainty is the closed confidence label derived from the released result.
type ExposureCertainty string

const (
	ExposureCertaintyEstablished ExposureCertainty = "established"
	ExposureCertaintyPartial     ExposureCertainty = "partial"
	ExposureCertaintyUnavailable ExposureCertainty = "unavailable"
)

// CompletionOutcome is written only by an explicit verified supported-host callback.
type CompletionOutcome string

const (
	CompletionSucceeded CompletionOutcome = "succeeded"
	CompletionPartial   CompletionOutcome = "partial"
	CompletionFailed    CompletionOutcome = "failed"
	CompletionAbandoned CompletionOutcome = "abandoned"
)

// ExposureInput contains only the metadata needed to derive one non-content
// receipt. ClientKeycard is populated exclusively by MCP callers. Browser
// callers carry their canonical subject and a successful document-binding
// proof separately, so a browser identity is never treated as a keycard.
// Every raw value is used only to derive opaque hashes and is never included
// in ExposureRecord.
type ExposureInput struct {
	AuthRealm              string
	ClientKeycard          string
	BrowserSubject         BrowserSubject
	BrowserDocumentBinding string
	ClientSession          string
	RequestID              string
	RequestBindingDigest   string
	Operation              ExposureOperation
	Response               QueryResponse
	RecordedAt             time.Time
}

// ExposureRecord is the complete durable, non-content representation of one
// authorized result receipt. ExposureRef is assigned by the store; all identity
// references and idempotency material are opaque SHA-256 values.
type ExposureRecord struct {
	ExposureRef      string
	AuthRealm        string
	SourceID         string
	CheckoutID       string
	ViewID           string
	ClientRef        string
	ClientSessionRef string
	RequestRef       string
	Operation        ExposureOperation
	Result           ExposureResultState
	Retrieval        ExposureRetrievalMode
	Coverage         ExposureCoverageState
	Evidence         ExposureEvidenceSource
	Certainty        ExposureCertainty
	IdempotencyKey   string
	BindingDigest    string
	RecordedAt       time.Time
}

// CompletionEvidence is one durable child of an exposure. It contains no
// source result, query, argument, or caller keycard material.
type CompletionEvidence struct {
	ExposureRef      string
	SupportedHostRef string
	CallbackRef      string
	Outcome          CompletionOutcome
	IdempotencyKey   string
	BindingDigest    string
	OccurredAt       time.Time
}

// VerifiedSupportedHostCallback is the sole type accepted for completion
// append. Construction of this value is the supported-host capability boundary;
// absence of a value leaves the parent receipt's completion state unknown.
type VerifiedSupportedHostCallback struct {
	ExposureRef      string
	SupportedHostRef string
	CallbackRef      string
	Outcome          CompletionOutcome
	IdempotencyKey   string
	OccurredAt       time.Time
}

// ExposureStore is the narrow persistence port owned by the exposure domain.
// An exact retry returns the original record; a changed binding returns only
// ErrIdempotencyMismatch.
type ExposureStore interface {
	AppendExposure(context.Context, ExposureRecord) (ExposureRecord, error)
	AppendCompletion(context.Context, CompletionEvidence) (CompletionEvidence, error)
}

// ExposureRecorder derives canonical receipt metadata and owns the scoped
// health state for one durable exposure store.
type ExposureRecorder struct {
	store  ExposureStore
	health ExposureHealthTracker
}

// NewExposureRecorder creates one recorder over a narrow durable-store port.
func NewExposureRecorder(store ExposureStore, health ExposureHealthTracker) *ExposureRecorder {
	if health == nil {
		health = NewExposureHealthController(store != nil)
	}
	return &ExposureRecorder{store: store, health: health}
}

// Health returns the secret-free health snapshot for this recorder only.
func (recorder *ExposureRecorder) Health() ExposureHealthSnapshot {
	if recorder == nil || recorder.health == nil {
		return ExposureHealthSnapshot{
			State:           ExposureHealthUnavailable,
			LastFailureCode: ExposureHealthFailureExposureUnavailable,
		}
	}
	return recorder.health.Snapshot()
}

// Record validates the one authorized pre-exposure result, derives all opaque
// receipt identity and idempotency values, appends it, and returns a release
// receipt only after a durable append succeeds.
func (recorder *ExposureRecorder) Record(ctx context.Context, authorized AuthorizedContext, input ExposureInput) (QueryExposure, error) {
	record, err := deriveExposureRecord(authorized, input)
	if err != nil {
		return QueryExposure{}, err
	}
	if recorder == nil || recorder.store == nil {
		if recorder != nil && recorder.health != nil {
			recorder.health.RecordInitialExposureFailure()
		}
		return QueryExposure{}, ErrExposureUnavailable
	}

	stored, err := recorder.store.AppendExposure(ctx, record)
	if err != nil {
		if errors.Is(err, ErrIdempotencyMismatch) {
			recorder.health.RecordIdempotencyMismatch()
			return QueryExposure{}, ErrIdempotencyMismatch
		}
		recorder.health.RecordInitialExposureFailure()
		return QueryExposure{}, ErrExposureUnavailable
	}
	if err := validateStoredExposure(record, stored); err != nil {
		recorder.health.RecordInitialExposureFailure()
		return QueryExposure{}, ErrExposureUnavailable
	}
	recorder.health.RecordInitialExposureSuccess()
	return QueryExposure{ExposureRef: stored.ExposureRef, CompletionState: QueryCompletionUnknown}, nil
}

// RecordCompletion appends one explicit verified supported-host callback child.
// It never infers completion from elapsed time or response outcome.
func (recorder *ExposureRecorder) RecordCompletion(ctx context.Context, callback VerifiedSupportedHostCallback) (CompletionEvidence, error) {
	evidence, err := completionEvidenceFromCallback(callback)
	if err != nil {
		return CompletionEvidence{}, ErrCompletionEvidenceUnavailable
	}
	if recorder == nil || recorder.store == nil {
		if recorder != nil && recorder.health != nil {
			recorder.health.RecordCompletionFailure()
		}
		return CompletionEvidence{}, ErrCompletionEvidenceUnavailable
	}

	stored, err := recorder.store.AppendCompletion(ctx, evidence)
	if err != nil {
		if errors.Is(err, ErrIdempotencyMismatch) {
			recorder.health.RecordIdempotencyMismatch()
			return CompletionEvidence{}, ErrIdempotencyMismatch
		}
		recorder.health.RecordCompletionFailure()
		return CompletionEvidence{}, ErrCompletionEvidenceUnavailable
	}
	if err := validateStoredCompletion(evidence, stored); err != nil {
		recorder.health.RecordCompletionFailure()
		return CompletionEvidence{}, ErrCompletionEvidenceUnavailable
	}
	recorder.health.RecordCompletionSuccess()
	return stored, nil
}

// NewExposureRef creates the opaque external receipt identifier stored by an
// ExposureStore. It contains no source, checkout, view, caller, or request data.
func NewExposureRef() string {
	return exposureRefPrefix + uuid.NewString()
}

// ValidExposureRef reports whether value is the canonical opaque receipt form.
func ValidExposureRef(value string) bool {
	if !strings.HasPrefix(value, exposureRefPrefix) {
		return false
	}
	parsed, err := uuid.Parse(strings.TrimPrefix(value, exposureRefPrefix))
	return err == nil && parsed != uuid.Nil && parsed.String() == strings.TrimPrefix(value, exposureRefPrefix)
}

// CanonicalExposureBindingDigest returns the versioned non-content digest used
// to distinguish an exact retry from an idempotency-key rebinding attempt.
func CanonicalExposureBindingDigest(record ExposureRecord) (string, error) {
	if err := validateExposureRecordMetadata(record); err != nil {
		return "", err
	}
	return canonicalExposureDigest(struct {
		AuthRealm        string                 `json:"auth_realm"`
		Certainty        ExposureCertainty      `json:"certainty"`
		CheckoutID       string                 `json:"checkout_id"`
		ClientRef        string                 `json:"client_ref"`
		ClientSessionRef string                 `json:"client_session_ref"`
		Coverage         ExposureCoverageState  `json:"coverage_state"`
		Evidence         ExposureEvidenceSource `json:"evidence_source"`
		IdempotencyKey   string                 `json:"idempotency_key"`
		Kind             string                 `json:"kind"`
		Operation        ExposureOperation      `json:"operation_kind"`
		RequestRef       string                 `json:"request_ref"`
		Result           ExposureResultState    `json:"result_state"`
		Retrieval        ExposureRetrievalMode  `json:"retrieval_mode"`
		SourceID         string                 `json:"source_id"`
		Version          int                    `json:"version"`
		ViewID           string                 `json:"view_id"`
	}{
		AuthRealm:        record.AuthRealm,
		Certainty:        record.Certainty,
		CheckoutID:       record.CheckoutID,
		ClientRef:        record.ClientRef,
		ClientSessionRef: record.ClientSessionRef,
		Coverage:         record.Coverage,
		Evidence:         record.Evidence,
		IdempotencyKey:   record.IdempotencyKey,
		Kind:             "exposure",
		Operation:        record.Operation,
		RequestRef:       record.RequestRef,
		Result:           record.Result,
		Retrieval:        record.Retrieval,
		SourceID:         record.SourceID,
		Version:          exposureBindingVersion,
		ViewID:           record.ViewID,
	})
}

// CanonicalCompletionBindingDigest returns the versioned callback evidence
// binding. The opaque exposure reference binds the child to its parent without
// requiring the domain to expose a persistence identifier.
func CanonicalCompletionBindingDigest(evidence CompletionEvidence) (string, error) {
	if err := validateCompletionEvidenceMetadata(evidence); err != nil {
		return "", err
	}
	return canonicalExposureDigest(struct {
		CallbackRef      string            `json:"callback_ref"`
		ExposureRef      string            `json:"exposure_ref"`
		IdempotencyKey   string            `json:"idempotency_key"`
		Kind             string            `json:"kind"`
		Outcome          CompletionOutcome `json:"outcome"`
		SupportedHostRef string            `json:"supported_host_ref"`
		Version          int               `json:"version"`
	}{
		CallbackRef:      evidence.CallbackRef,
		ExposureRef:      evidence.ExposureRef,
		IdempotencyKey:   evidence.IdempotencyKey,
		Kind:             "completion",
		Outcome:          evidence.Outcome,
		SupportedHostRef: evidence.SupportedHostRef,
		Version:          exposureBindingVersion,
	})
}

// Validate checks the durable exposure record shape and its canonical digest.
func (record ExposureRecord) Validate() error {
	if err := validateExposureRecordMetadata(record); err != nil {
		return err
	}
	if record.ExposureRef != "" && !ValidExposureRef(record.ExposureRef) {
		return errors.New("uci exposure: invalid exposure_ref")
	}
	if !validExposureDigest(record.BindingDigest) {
		return errors.New("uci exposure: invalid binding digest")
	}
	canonical, err := CanonicalExposureBindingDigest(record)
	if err != nil {
		return err
	}
	if record.BindingDigest != canonical {
		return errors.New("uci exposure: binding digest does not match record")
	}
	if record.RecordedAt.IsZero() {
		return errors.New("uci exposure: recorded_at is required")
	}
	return nil
}

// Validate checks the durable completion evidence shape and its canonical digest.
func (evidence CompletionEvidence) Validate() error {
	if err := validateCompletionEvidenceMetadata(evidence); err != nil {
		return err
	}
	if !validExposureDigest(evidence.BindingDigest) {
		return errors.New("uci completion: invalid binding digest")
	}
	canonical, err := CanonicalCompletionBindingDigest(evidence)
	if err != nil {
		return err
	}
	if evidence.BindingDigest != canonical {
		return errors.New("uci completion: binding digest does not match evidence")
	}
	if evidence.OccurredAt.IsZero() {
		return errors.New("uci completion: occurred_at is required")
	}
	return nil
}

func deriveExposureRecord(authorized AuthorizedContext, input ExposureInput) (ExposureRecord, error) {
	ref := authorized.Ref()
	if !ref.valid() {
		return ExposureRecord{}, errors.New("uci exposure: invalid authorized context")
	}
	if err := validateExposureInput(input); err != nil {
		return ExposureRecord{}, err
	}
	result, retrieval, coverage, evidence, certainty, err := classifyExposure(input.Response)
	if err != nil {
		return ExposureRecord{}, err
	}

	var clientRef string
	if input.BrowserSubject != nil {
		clientRef, err = opaqueExposureHash("browser_subject", input.BrowserSubject)
	} else {
		clientRef, err = opaqueExposureHash("client_keycard", input.ClientKeycard)
	}
	if err != nil {
		return ExposureRecord{}, err
	}
	clientSessionRef, err := opaqueExposureHash("client_session", input.ClientSession)
	if err != nil {
		return ExposureRecord{}, err
	}
	requestRef, err := opaqueExposureHash("request", struct {
		RequestID              string `json:"request_id"`
		BindingDigest          string `json:"binding_digest"`
		BrowserDocumentBinding string `json:"browser_document_binding,omitempty"`
	}{
		RequestID:              input.RequestID,
		BindingDigest:          input.RequestBindingDigest,
		BrowserDocumentBinding: input.BrowserDocumentBinding,
	})
	if err != nil {
		return ExposureRecord{}, err
	}
	idempotencyKey, err := opaqueExposureHash("idempotency", struct {
		ClientSession string            `json:"client_session"`
		Operation     ExposureOperation `json:"operation"`
		RequestID     string            `json:"request_id"`
	}{
		ClientSession: input.ClientSession,
		Operation:     input.Operation,
		RequestID:     input.RequestID,
	})
	if err != nil {
		return ExposureRecord{}, err
	}

	recordedAt := input.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = time.Now().UTC()
	} else {
		recordedAt = recordedAt.UTC()
	}
	record := ExposureRecord{
		AuthRealm:        input.AuthRealm,
		SourceID:         ref.SourceID,
		CheckoutID:       ref.CheckoutID,
		ViewID:           ref.ViewID,
		ClientRef:        clientRef,
		ClientSessionRef: clientSessionRef,
		RequestRef:       requestRef,
		Operation:        input.Operation,
		Result:           result,
		Retrieval:        retrieval,
		Coverage:         coverage,
		Evidence:         evidence,
		Certainty:        certainty,
		IdempotencyKey:   idempotencyKey,
		RecordedAt:       recordedAt,
	}
	bindingDigest, err := CanonicalExposureBindingDigest(record)
	if err != nil {
		return ExposureRecord{}, err
	}
	record.BindingDigest = bindingDigest
	return record, nil
}

func completionEvidenceFromCallback(callback VerifiedSupportedHostCallback) (CompletionEvidence, error) {
	evidence := CompletionEvidence{
		ExposureRef:      callback.ExposureRef,
		SupportedHostRef: callback.SupportedHostRef,
		CallbackRef:      callback.CallbackRef,
		Outcome:          callback.Outcome,
		IdempotencyKey:   callback.IdempotencyKey,
		OccurredAt:       callback.OccurredAt.UTC(),
	}
	if err := validateCompletionEvidenceMetadata(evidence); err != nil {
		return CompletionEvidence{}, err
	}
	if evidence.OccurredAt.IsZero() {
		return CompletionEvidence{}, errors.New("uci completion: occurred_at is required")
	}
	bindingDigest, err := CanonicalCompletionBindingDigest(evidence)
	if err != nil {
		return CompletionEvidence{}, err
	}
	evidence.BindingDigest = bindingDigest
	return evidence, nil
}

func validateStoredExposure(want, got ExposureRecord) error {
	if err := got.Validate(); err != nil {
		return err
	}
	if got.ExposureRef == "" || got.BindingDigest != want.BindingDigest || got.AuthRealm != want.AuthRealm ||
		got.SourceID != want.SourceID || got.CheckoutID != want.CheckoutID || got.ViewID != want.ViewID ||
		got.ClientRef != want.ClientRef || got.ClientSessionRef != want.ClientSessionRef || got.RequestRef != want.RequestRef ||
		got.Operation != want.Operation || got.Result != want.Result || got.Retrieval != want.Retrieval || got.Coverage != want.Coverage ||
		got.Evidence != want.Evidence || got.Certainty != want.Certainty || got.IdempotencyKey != want.IdempotencyKey {
		return errors.New("uci exposure: store returned a differently bound record")
	}
	return nil
}

func validateStoredCompletion(want, got CompletionEvidence) error {
	if err := got.Validate(); err != nil {
		return err
	}
	if got.ExposureRef != want.ExposureRef || got.SupportedHostRef != want.SupportedHostRef || got.CallbackRef != want.CallbackRef ||
		got.Outcome != want.Outcome || got.IdempotencyKey != want.IdempotencyKey || got.BindingDigest != want.BindingDigest {
		return errors.New("uci completion: store returned differently bound evidence")
	}
	return nil
}

func classifyExposure(response QueryResponse) (ExposureResultState, ExposureRetrievalMode, ExposureCoverageState, ExposureEvidenceSource, ExposureCertainty, error) {
	if err := response.ValidatePreExposure(); err != nil {
		return "", "", "", "", "", err
	}

	result := ExposureResultState(response.Status)
	retrieval := ExposureRetrievalMode(response.Retrieval.Mode)
	coverage := ExposureCoverageState(response.Coverage.Structural)
	var evidence ExposureEvidenceSource
	switch response.Retrieval.Mode {
	case QueryRetrievalExact:
		evidence = ExposureEvidenceExact
	case QueryRetrievalLexical:
		evidence = ExposureEvidenceFTS
	case QueryRetrievalHybrid:
		evidence = ExposureEvidenceMixed
	case QueryRetrievalGraph:
		evidence = ExposureEvidenceGraph
	case QueryRetrievalUnavailable:
		evidence = ExposureEvidenceNone
	default:
		return "", "", "", "", "", errors.New("uci exposure: unsupported retrieval mode")
	}

	certainty := ExposureCertaintyEstablished
	if response.Status == QueryStatusUnavailable || response.Retrieval.Mode == QueryRetrievalUnavailable || response.Coverage.Structural == IndexCoverageUnavailable {
		certainty = ExposureCertaintyUnavailable
	} else if response.Status == QueryStatusPartial || response.Status == QueryStatusStale || response.Coverage.Structural == IndexCoveragePartial {
		certainty = ExposureCertaintyPartial
	}
	return result, retrieval, coverage, evidence, certainty, nil
}

func validateExposureInput(input ExposureInput) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"auth_realm", input.AuthRealm},
		{"client_session", input.ClientSession},
		{"request_id", input.RequestID},
	} {
		if !validExposureText(field.value) {
			return fmt.Errorf("uci exposure: invalid %s", field.name)
		}
	}
	if input.BrowserSubject == nil {
		if !validExposureText(input.ClientKeycard) || input.BrowserDocumentBinding != "" {
			return errors.New("uci exposure: invalid MCP caller")
		}
	} else if input.ClientKeycard != "" || !input.BrowserSubject.Valid() || !validExposureUUID(input.BrowserDocumentBinding) {
		return errors.New("uci exposure: invalid browser caller")
	}
	if !validExposureDigest(input.RequestBindingDigest) {
		return errors.New("uci exposure: invalid request binding digest")
	}
	if !input.Operation.valid() {
		return fmt.Errorf("uci exposure: unsupported operation %q", input.Operation)
	}
	return nil
}

func validateExposureRecordMetadata(record ExposureRecord) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"auth_realm", record.AuthRealm},
		{"client_ref", record.ClientRef},
		{"client_session_ref", record.ClientSessionRef},
		{"request_ref", record.RequestRef},
		{"idempotency_key", record.IdempotencyKey},
	} {
		if !validExposureText(field.value) {
			return fmt.Errorf("uci exposure: invalid %s", field.name)
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"source_id", record.SourceID},
		{"checkout_id", record.CheckoutID},
		{"view_id", record.ViewID},
	} {
		if !validExposureUUID(field.value) {
			return fmt.Errorf("uci exposure: invalid %s", field.name)
		}
	}
	if !record.Operation.valid() || !record.Result.valid() || !record.Retrieval.valid() || !record.Coverage.valid() || !record.Evidence.valid() || !record.Certainty.valid() {
		return errors.New("uci exposure: invalid closed classification")
	}
	if !validExposureDigest(record.ClientRef) || !validExposureDigest(record.ClientSessionRef) || !validExposureDigest(record.RequestRef) || !validExposureDigest(record.IdempotencyKey) {
		return errors.New("uci exposure: identity references must be opaque SHA-256 values")
	}
	return nil
}

func validateCompletionEvidenceMetadata(evidence CompletionEvidence) error {
	if !ValidExposureRef(evidence.ExposureRef) {
		return errors.New("uci completion: invalid exposure_ref")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"supported_host_ref", evidence.SupportedHostRef},
		{"callback_ref", evidence.CallbackRef},
		{"idempotency_key", evidence.IdempotencyKey},
	} {
		if !validExposureText(field.value) {
			return fmt.Errorf("uci completion: invalid %s", field.name)
		}
	}
	if !evidence.Outcome.valid() {
		return fmt.Errorf("uci completion: unsupported outcome %q", evidence.Outcome)
	}
	return nil
}

func canonicalExposureDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize UCI exposure binding: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func opaqueExposureHash(kind string, value any) (string, error) {
	return canonicalExposureDigest(struct {
		Kind    string `json:"kind"`
		Value   any    `json:"value"`
		Version int    `json:"version"`
	}{Kind: kind, Value: value, Version: exposureBindingVersion})
}

func validExposureText(value string) bool {
	if value == "" || len(value) > 4_096 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validExposureUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validExposureDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (operation ExposureOperation) valid() bool {
	switch operation {
	case ExposureOperationCodeSearch, ExposureOperationCodeGraph, ExposureOperationVersionedRead:
		return true
	default:
		return false
	}
}

func (state ExposureResultState) valid() bool {
	switch state {
	case ExposureResultOK, ExposureResultEmpty, ExposureResultPartial, ExposureResultStale, ExposureResultUnavailable:
		return true
	default:
		return false
	}
}

func (mode ExposureRetrievalMode) valid() bool {
	switch mode {
	case ExposureRetrievalExact, ExposureRetrievalLexical, ExposureRetrievalHybrid, ExposureRetrievalGraph, ExposureRetrievalUnavailable:
		return true
	default:
		return false
	}
}

func (state ExposureCoverageState) valid() bool {
	switch state {
	case ExposureCoverageComplete, ExposureCoveragePartial, ExposureCoverageUnavailable:
		return true
	default:
		return false
	}
}

func (source ExposureEvidenceSource) valid() bool {
	switch source {
	case ExposureEvidenceExact, ExposureEvidenceFTS, ExposureEvidenceVector, ExposureEvidenceGraph, ExposureEvidenceMixed, ExposureEvidenceNone:
		return true
	default:
		return false
	}
}

func (certainty ExposureCertainty) valid() bool {
	switch certainty {
	case ExposureCertaintyEstablished, ExposureCertaintyPartial, ExposureCertaintyUnavailable:
		return true
	default:
		return false
	}
}

func (outcome CompletionOutcome) valid() bool {
	switch outcome {
	case CompletionSucceeded, CompletionPartial, CompletionFailed, CompletionAbandoned:
		return true
	default:
		return false
	}
}
