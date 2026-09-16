package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/intervention"
	gormlib "gorm.io/gorm"
)

const interventionReceiptColumns = `
	receipt_id,
	operation_id,
	key_epoch_commitment,
	integrity_digest,
	channel_key,
	host_family,
	canonical_project,
	actor_principal,
	actor_kind,
	workstation,
	session_key,
	occurrence_key,
	content_commitment,
	capability_snapshot_commitment,
	outcome,
	closed_reason,
	decision_mode,
	evaluated_count,
	eligible_count,
	snapshot_refs,
	selected_memory_id,
	selected_memory_version,
	selected_source_project,
	selected_source_tier,
	selected_policy_id,
	selected_snapshot_id,
	selected_snapshot_version,
	selected_text_digest,
	created_at,
	expires_at`

const interventionReceiptLookupSQL = `
	SELECT ` + interventionReceiptColumns + `
	FROM task_memory_intervention_receipts
	WHERE channel_key = ?
	  AND canonical_project = ?
	  AND actor_principal = ?
	  AND actor_kind = ?
	  AND workstation = ?
	  AND occurrence_key = ?`

const interventionReceiptInsertSQL = `
	INSERT INTO task_memory_intervention_receipts (` + interventionReceiptColumns + `)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT ON CONSTRAINT task_memory_intervention_receipts_occurrence_unique DO NOTHING
	RETURNING ` + interventionReceiptColumns

// InterventionReceiptStore is the PostgreSQL implementation of the immutable
// first-writer-wins IEP v2 receipt port.
type InterventionReceiptStore struct {
	db *gormlib.DB
}

// NewInterventionReceiptStore constructs a receipt store over db. Method calls
// return an error for a nil database so construction remains side-effect free.
func NewInterventionReceiptStore(db *gormlib.DB) *InterventionReceiptStore {
	return &InterventionReceiptStore{db: db}
}

// Lookup returns the one immutable receipt for the six-axis replay namespace.
func (s *InterventionReceiptStore) Lookup(ctx context.Context, axis intervention.ReceiptAxis) (intervention.Receipt, bool, error) {
	if err := interventionReceiptContext(ctx); err != nil {
		return intervention.Receipt{}, false, err
	}
	db, err := s.database()
	if err != nil {
		return intervention.Receipt{}, false, err
	}
	lookup, err := interventionReceiptLookupValuesFromAxis(axis)
	if err != nil {
		return intervention.Receipt{}, false, err
	}

	row, found, err := readInterventionReceiptRow(ctx, db, lookup)
	if err != nil || !found {
		return intervention.Receipt{}, found, err
	}
	receipt, err := row.receipt()
	if err != nil {
		return intervention.Receipt{}, false, fmt.Errorf("restore immutable intervention receipt: %w", err)
	}
	return receipt, true, nil
}

// Commit inserts receipt exactly once for its six-axis replay namespace. A
// conflict reads and returns the committed winner without updating any row.
func (s *InterventionReceiptStore) Commit(ctx context.Context, receipt intervention.Receipt) (intervention.Receipt, bool, error) {
	if err := interventionReceiptContext(ctx); err != nil {
		return intervention.Receipt{}, false, err
	}
	if !receipt.CanCommit() {
		return intervention.Receipt{}, false, fmt.Errorf("invalid intervention receipt commit input: receipt is not trusted and authorable")
	}
	db, err := s.database()
	if err != nil {
		return intervention.Receipt{}, false, err
	}

	row, err := interventionReceiptRowFromRecord(receipt.PersistenceRecord())
	if err != nil {
		return intervention.Receipt{}, false, err
	}
	lookup, err := interventionReceiptLookupValuesFromAxis(receipt.Axis())
	if err != nil {
		return intervention.Receipt{}, false, err
	}

	var committed intervention.Receipt
	inserted := false
	err = db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		insertedRow, didInsert, err := insertInterventionReceiptRow(ctx, tx, row)
		if err != nil {
			return err
		}
		if didInsert {
			committed, err = insertedRow.receipt()
			if err != nil {
				return fmt.Errorf("restore inserted immutable intervention receipt: %w", err)
			}
			inserted = true
			return nil
		}

		winner, found, err := readInterventionReceiptRow(ctx, tx, lookup)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("intervention receipt conflict did not expose a committed occurrence winner")
		}
		committed, err = winner.receipt()
		if err != nil {
			return fmt.Errorf("restore committed intervention receipt winner: %w", err)
		}
		return nil
	})
	if err != nil {
		return intervention.Receipt{}, false, fmt.Errorf("commit immutable intervention receipt: %w", err)
	}
	return committed, inserted, nil
}

// DistinctInterventionReceiptEpochs returns every retained epoch commitment in
// deterministic byte order. It refuses a malformed persisted width instead of
// silently truncating or padding a historical key epoch.
func (s *InterventionReceiptStore) DistinctInterventionReceiptEpochs(ctx context.Context) ([][32]byte, error) {
	if err := interventionReceiptContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}

	rows, err := db.WithContext(ctx).Raw(`
		SELECT DISTINCT key_epoch_commitment
		FROM task_memory_intervention_receipts
		ORDER BY key_epoch_commitment ASC
	`).Rows()
	if err != nil {
		return nil, fmt.Errorf("list intervention receipt epochs: %w", err)
	}
	defer rows.Close()

	epochs := make([][32]byte, 0)
	for rows.Next() {
		var persisted []byte
		if err := rows.Scan(&persisted); err != nil {
			return nil, fmt.Errorf("scan intervention receipt epoch: %w", err)
		}
		epoch, err := interventionReceiptDigestFromBytes("key_epoch_commitment", persisted)
		if err != nil {
			return nil, err
		}
		epochs = append(epochs, epoch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate intervention receipt epochs: %w", err)
	}
	return epochs, nil
}

func (s *InterventionReceiptStore) database() (*gormlib.DB, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("intervention receipt store has nil database")
	}
	return s.db, nil
}

func interventionReceiptContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("intervention receipt store received nil context")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("intervention receipt store context: %w", err)
	}
	return nil
}

type interventionReceiptLookupValues struct {
	ChannelKey       []byte
	CanonicalProject string
	ActorPrincipal   string
	ActorKind        string
	Workstation      string
	OccurrenceKey    []byte
}

func interventionReceiptLookupValuesFromAxis(axis intervention.ReceiptAxis) (interventionReceiptLookupValues, error) {
	channelKey, err := interventionReceiptDigestFromDigest("channel_key", axis.ChannelKey())
	if err != nil {
		return interventionReceiptLookupValues{}, err
	}
	sessionKey, err := interventionReceiptDigestFromDigest("session_key", axis.SessionKey())
	if err != nil {
		return interventionReceiptLookupValues{}, err
	}
	_ = sessionKey // Validate every protected axis fact even though session is not a replay key.
	occurrenceKey, err := interventionReceiptDigestFromDigest("occurrence_key", axis.OccurrenceKey())
	if err != nil {
		return interventionReceiptLookupValues{}, err
	}
	contentCommitment, err := interventionReceiptDigestFromDigest("content_commitment", axis.ContentCommitment())
	if err != nil {
		return interventionReceiptLookupValues{}, err
	}
	_ = contentCommitment
	capabilityCommitment, err := interventionReceiptDigestFromDigest("capability_snapshot_commitment", axis.CapabilityCommitment())
	if err != nil {
		return interventionReceiptLookupValues{}, err
	}
	_ = capabilityCommitment
	if _, err := interventionReceiptHostFamilyText(axis.HostFamily()); err != nil {
		return interventionReceiptLookupValues{}, err
	}
	if !interventionReceiptCanonicalUUID(axis.CanonicalProject()) ||
		!interventionReceiptPrincipalPair(axis.ActorPrincipal(), axis.ActorKind()) ||
		!interventionReceiptOpaqueText(axis.Workstation()) {
		return interventionReceiptLookupValues{}, fmt.Errorf("invalid intervention receipt axis: %w", intervention.ErrInvalidInput)
	}
	return interventionReceiptLookupValues{
		ChannelKey:       channelKey[:],
		CanonicalProject: axis.CanonicalProject(),
		ActorPrincipal:   axis.ActorPrincipal(),
		ActorKind:        axis.ActorKind(),
		Workstation:      axis.Workstation(),
		OccurrenceKey:    occurrenceKey[:],
	}, nil
}

func readInterventionReceiptRow(ctx context.Context, db *gormlib.DB, lookup interventionReceiptLookupValues) (interventionReceiptRow, bool, error) {
	rows, err := db.WithContext(ctx).Raw(interventionReceiptLookupSQL,
		lookup.ChannelKey,
		lookup.CanonicalProject,
		lookup.ActorPrincipal,
		lookup.ActorKind,
		lookup.Workstation,
		lookup.OccurrenceKey,
	).Rows()
	if err != nil {
		return interventionReceiptRow{}, false, fmt.Errorf("lookup intervention receipt: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return interventionReceiptRow{}, false, fmt.Errorf("iterate intervention receipt lookup: %w", err)
		}
		return interventionReceiptRow{}, false, nil
	}

	var row interventionReceiptRow
	if err := db.ScanRows(rows, &row); err != nil {
		return interventionReceiptRow{}, false, fmt.Errorf("scan intervention receipt lookup: %w", err)
	}
	if rows.Next() {
		return interventionReceiptRow{}, false, fmt.Errorf("intervention receipt occurrence namespace is not unique")
	}
	if err := rows.Err(); err != nil {
		return interventionReceiptRow{}, false, fmt.Errorf("iterate intervention receipt lookup: %w", err)
	}
	return row, true, nil
}

func insertInterventionReceiptRow(ctx context.Context, db *gormlib.DB, row interventionReceiptRow) (interventionReceiptRow, bool, error) {
	rows, err := db.WithContext(ctx).Raw(interventionReceiptInsertSQL, row.insertArguments()...).Rows()
	if err != nil {
		return interventionReceiptRow{}, false, fmt.Errorf("insert intervention receipt: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return interventionReceiptRow{}, false, fmt.Errorf("iterate intervention receipt insert: %w", err)
		}
		return interventionReceiptRow{}, false, nil
	}

	var inserted interventionReceiptRow
	if err := db.ScanRows(rows, &inserted); err != nil {
		return interventionReceiptRow{}, false, fmt.Errorf("scan inserted intervention receipt: %w", err)
	}
	if rows.Next() {
		return interventionReceiptRow{}, false, fmt.Errorf("intervention receipt insert returned multiple rows")
	}
	if err := rows.Err(); err != nil {
		return interventionReceiptRow{}, false, fmt.Errorf("iterate intervention receipt insert: %w", err)
	}
	return inserted, true, nil
}

type interventionReceiptRow struct {
	ReceiptID                    string         `gorm:"column:receipt_id"`
	OperationID                  string         `gorm:"column:operation_id"`
	KeyEpochCommitment           []byte         `gorm:"column:key_epoch_commitment"`
	IntegrityDigest              []byte         `gorm:"column:integrity_digest"`
	ChannelKey                   []byte         `gorm:"column:channel_key"`
	HostFamily                   string         `gorm:"column:host_family"`
	CanonicalProject             string         `gorm:"column:canonical_project"`
	ActorPrincipal               string         `gorm:"column:actor_principal"`
	ActorKind                    string         `gorm:"column:actor_kind"`
	Workstation                  string         `gorm:"column:workstation"`
	SessionKey                   []byte         `gorm:"column:session_key"`
	OccurrenceKey                []byte         `gorm:"column:occurrence_key"`
	ContentCommitment            []byte         `gorm:"column:content_commitment"`
	CapabilitySnapshotCommitment []byte         `gorm:"column:capability_snapshot_commitment"`
	Outcome                      string         `gorm:"column:outcome"`
	ClosedReason                 sql.NullString `gorm:"column:closed_reason"`
	DecisionMode                 string         `gorm:"column:decision_mode"`
	EvaluatedCount               int            `gorm:"column:evaluated_count"`
	EligibleCount                int            `gorm:"column:eligible_count"`
	SnapshotRefs                 []byte         `gorm:"column:snapshot_refs"`
	SelectedMemoryID             sql.NullInt64  `gorm:"column:selected_memory_id"`
	SelectedMemoryVersion        sql.NullInt64  `gorm:"column:selected_memory_version"`
	SelectedSourceProject        sql.NullString `gorm:"column:selected_source_project"`
	SelectedSourceTier           sql.NullInt64  `gorm:"column:selected_source_tier"`
	SelectedPolicyID             []byte         `gorm:"column:selected_policy_id"`
	SelectedSnapshotID           sql.NullString `gorm:"column:selected_snapshot_id"`
	SelectedSnapshotVersion      sql.NullInt64  `gorm:"column:selected_snapshot_version"`
	SelectedTextDigest           []byte         `gorm:"column:selected_text_digest"`
	CreatedAt                    time.Time      `gorm:"column:created_at"`
	ExpiresAt                    time.Time      `gorm:"column:expires_at"`
}

func interventionReceiptRowFromRecord(record intervention.ReceiptPersistenceRecord) (interventionReceiptRow, error) {
	outcome, err := interventionReceiptOutcomeText(record.Outcome)
	if err != nil {
		return interventionReceiptRow{}, err
	}
	decisionMode, err := interventionReceiptDecisionModeText(record.DecisionMode)
	if err != nil {
		return interventionReceiptRow{}, err
	}
	hostFamily, err := interventionReceiptHostFamilyText(record.HostFamily)
	if err != nil {
		return interventionReceiptRow{}, err
	}

	row := interventionReceiptRow{
		ReceiptID:                    record.ReceiptID,
		OperationID:                  record.OperationID,
		KeyEpochCommitment:           interventionReceiptDigestBytes(record.KeyEpochCommitment),
		IntegrityDigest:              interventionReceiptDigestBytes(record.IntegrityDigest),
		ChannelKey:                   interventionReceiptDigestBytes(record.ChannelKey),
		HostFamily:                   hostFamily,
		CanonicalProject:             record.CanonicalProject,
		ActorPrincipal:               record.ActorPrincipal,
		ActorKind:                    record.ActorKind,
		Workstation:                  record.Workstation,
		SessionKey:                   interventionReceiptDigestBytes(record.SessionKey),
		OccurrenceKey:                interventionReceiptDigestBytes(record.OccurrenceKey),
		ContentCommitment:            interventionReceiptDigestBytes(record.ContentCommitment),
		CapabilitySnapshotCommitment: interventionReceiptDigestBytes(record.CapabilityCommitment),
		Outcome:                      outcome,
		DecisionMode:                 decisionMode,
		EvaluatedCount:               record.EvaluatedCount,
		EligibleCount:                record.EligibleCount,
		SnapshotRefs:                 append([]byte(nil), record.SnapshotRefsJSON...),
		CreatedAt:                    record.CreatedAt,
		ExpiresAt:                    record.ExpiresAt,
	}
	if record.ClosedReason != nil {
		closedReason, err := interventionReceiptAbstentionReasonText(*record.ClosedReason)
		if err != nil {
			return interventionReceiptRow{}, err
		}
		row.ClosedReason = sql.NullString{String: closedReason, Valid: true}
	}
	if record.Selection == nil {
		return row, nil
	}

	switch record.DecisionMode {
	case intervention.ReceiptDecisionModeContextReference:
		memoryID, memoryVersion, sourceProject, sourceTier, textDigest, ok := record.Selection.ContextReference()
		if !ok {
			return interventionReceiptRow{}, fmt.Errorf("invalid context-reference selection: %w", intervention.ErrInvalidInput)
		}
		row.SelectedMemoryID = sql.NullInt64{Int64: memoryID, Valid: true}
		row.SelectedMemoryVersion = sql.NullInt64{Int64: int64(memoryVersion), Valid: true}
		row.SelectedSourceProject = sql.NullString{String: sourceProject, Valid: true}
		row.SelectedSourceTier = sql.NullInt64{Int64: int64(sourceTier), Valid: true}
		row.SelectedTextDigest = interventionReceiptDigestBytes([32]byte(textDigest))
	case intervention.ReceiptDecisionModeEligible, intervention.ReceiptDecisionModeCanary:
		memoryID, memoryVersion, policyID, snapshotID, snapshotVersion, textDigest, ok := record.Selection.LearnedIntervention()
		if !ok {
			return interventionReceiptRow{}, fmt.Errorf("invalid learned-intervention selection: %w", intervention.ErrInvalidInput)
		}
		row.SelectedMemoryID = sql.NullInt64{Int64: memoryID, Valid: true}
		row.SelectedMemoryVersion = sql.NullInt64{Int64: int64(memoryVersion), Valid: true}
		row.SelectedPolicyID = interventionReceiptDigestBytes([32]byte(policyID))
		row.SelectedSnapshotID = sql.NullString{String: snapshotID, Valid: true}
		row.SelectedSnapshotVersion = sql.NullInt64{Int64: snapshotVersion, Valid: true}
		row.SelectedTextDigest = interventionReceiptDigestBytes([32]byte(textDigest))
	default:
		return interventionReceiptRow{}, fmt.Errorf("invalid intervention receipt selected decision mode: %w", intervention.ErrInvalidInput)
	}
	return row, nil
}

func (row interventionReceiptRow) receipt() (intervention.Receipt, error) {
	keyEpoch, err := interventionReceiptDigestFromBytes("key_epoch_commitment", row.KeyEpochCommitment)
	if err != nil {
		return intervention.Receipt{}, err
	}
	integrity, err := interventionReceiptDigestFromBytes("integrity_digest", row.IntegrityDigest)
	if err != nil {
		return intervention.Receipt{}, err
	}
	channel, err := interventionReceiptDigestFromBytes("channel_key", row.ChannelKey)
	if err != nil {
		return intervention.Receipt{}, err
	}
	session, err := interventionReceiptDigestFromBytes("session_key", row.SessionKey)
	if err != nil {
		return intervention.Receipt{}, err
	}
	occurrence, err := interventionReceiptDigestFromBytes("occurrence_key", row.OccurrenceKey)
	if err != nil {
		return intervention.Receipt{}, err
	}
	content, err := interventionReceiptDigestFromBytes("content_commitment", row.ContentCommitment)
	if err != nil {
		return intervention.Receipt{}, err
	}
	capability, err := interventionReceiptDigestFromBytes("capability_snapshot_commitment", row.CapabilitySnapshotCommitment)
	if err != nil {
		return intervention.Receipt{}, err
	}
	outcome, err := interventionReceiptOutcomeFromText(row.Outcome)
	if err != nil {
		return intervention.Receipt{}, err
	}
	decisionMode, err := interventionReceiptDecisionModeFromText(row.DecisionMode)
	if err != nil {
		return intervention.Receipt{}, err
	}
	hostFamily, err := interventionReceiptHostFamilyFromText(row.HostFamily)
	if err != nil {
		return intervention.Receipt{}, err
	}

	var closedReason *intervention.AbstentionReason
	if row.ClosedReason.Valid {
		reason, err := interventionReceiptAbstentionReasonFromText(row.ClosedReason.String)
		if err != nil {
			return intervention.Receipt{}, err
		}
		closedReason = &reason
	}

	selection, err := row.selection(decisionMode)
	if err != nil {
		return intervention.Receipt{}, err
	}

	return intervention.RestoreUnverifiedReceipt(intervention.ReceiptPersistenceRecord{
		ReceiptID:            row.ReceiptID,
		OperationID:          row.OperationID,
		KeyEpochCommitment:   keyEpoch,
		IntegrityDigest:      integrity,
		ChannelKey:           channel,
		HostFamily:           hostFamily,
		CanonicalProject:     row.CanonicalProject,
		ActorPrincipal:       row.ActorPrincipal,
		ActorKind:            row.ActorKind,
		Workstation:          row.Workstation,
		SessionKey:           session,
		OccurrenceKey:        occurrence,
		ContentCommitment:    content,
		CapabilityCommitment: capability,
		Outcome:              outcome,
		ClosedReason:         closedReason,
		DecisionMode:         decisionMode,
		EvaluatedCount:       row.EvaluatedCount,
		EligibleCount:        row.EligibleCount,
		SnapshotRefsJSON:     append([]byte(nil), row.SnapshotRefs...),
		Selection:            selection,
		CreatedAt:            row.CreatedAt,
		ExpiresAt:            row.ExpiresAt,
	})
}

func (row interventionReceiptRow) selection(decisionMode intervention.ReceiptDecisionMode) (*intervention.ReceiptSelectionRecord, error) {
	if !row.hasSelectedReference() {
		return nil, nil
	}
	memoryVersion, textDigest, err := row.selectionParts()
	if err != nil {
		return nil, err
	}
	var selection intervention.ReceiptSelectionRecord
	switch decisionMode {
	case intervention.ReceiptDecisionModeContextReference:
		selection, err = row.contextReferenceSelection(memoryVersion, textDigest)
	case intervention.ReceiptDecisionModeEligible, intervention.ReceiptDecisionModeCanary:
		selection, err = row.learnedInterventionSelection(memoryVersion, textDigest)
	default:
		return nil, fmt.Errorf("persisted intervention receipt has a selected reference for a non-emitted mode")
	}
	if err != nil {
		return nil, fmt.Errorf("restore persisted intervention receipt selection: %w", err)
	}
	return &selection, nil
}

func (row interventionReceiptRow) hasSelectedReference() bool {
	return row.SelectedMemoryID.Valid || row.SelectedMemoryVersion.Valid || row.SelectedSourceProject.Valid || row.SelectedSourceTier.Valid || row.SelectedPolicyID != nil || row.SelectedSnapshotID.Valid || row.SelectedSnapshotVersion.Valid || row.SelectedTextDigest != nil
}

func (row interventionReceiptRow) selectionParts() (int, intervention.Digest, error) {
	if !row.SelectedMemoryID.Valid || !row.SelectedMemoryVersion.Valid || row.SelectedTextDigest == nil {
		return 0, intervention.Digest{}, fmt.Errorf("persisted intervention receipt has a partial selected reference")
	}
	memoryVersion, err := interventionReceiptMemoryVersion(row.SelectedMemoryVersion.Int64)
	if err != nil {
		return 0, intervention.Digest{}, err
	}
	textDigest, err := interventionReceiptDigestFromBytes("selected_text_digest", row.SelectedTextDigest)
	if err != nil {
		return 0, intervention.Digest{}, err
	}
	return memoryVersion, intervention.Digest(textDigest), nil
}

func (row interventionReceiptRow) contextReferenceSelection(memoryVersion int, textDigest intervention.Digest) (intervention.ReceiptSelectionRecord, error) {
	if !row.SelectedSourceProject.Valid || !row.SelectedSourceTier.Valid || row.SelectedPolicyID != nil || row.SelectedSnapshotID.Valid || row.SelectedSnapshotVersion.Valid {
		return intervention.ReceiptSelectionRecord{}, fmt.Errorf("persisted context reference receipt has a mixed selected reference")
	}
	return intervention.NewContextReferenceReceiptSelection(row.SelectedMemoryID.Int64, memoryVersion, row.SelectedSourceProject.String, intervention.CandidateTier(row.SelectedSourceTier.Int64), textDigest)
}

func (row interventionReceiptRow) learnedInterventionSelection(memoryVersion int, textDigest intervention.Digest) (intervention.ReceiptSelectionRecord, error) {
	if row.SelectedSourceProject.Valid || row.SelectedSourceTier.Valid || row.SelectedPolicyID == nil || !row.SelectedSnapshotID.Valid || !row.SelectedSnapshotVersion.Valid {
		return intervention.ReceiptSelectionRecord{}, fmt.Errorf("persisted learned intervention receipt has a mixed selected reference")
	}
	policyID, err := interventionReceiptDigestFromBytes("selected_policy_id", row.SelectedPolicyID)
	if err != nil {
		return intervention.ReceiptSelectionRecord{}, err
	}
	return intervention.NewLearnedInterventionReceiptSelection(row.SelectedMemoryID.Int64, memoryVersion, intervention.Digest(policyID), row.SelectedSnapshotID.String, row.SelectedSnapshotVersion.Int64, textDigest)
}

func interventionReceiptMemoryVersion(value int64) (int, error) {
	if value < 1 || value > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("persisted intervention receipt has an invalid selected memory version")
	}
	return int(value), nil
}

func (row interventionReceiptRow) insertArguments() []any {
	var closedReason any
	if row.ClosedReason.Valid {
		closedReason = row.ClosedReason.String
	}
	var selectedMemoryID any
	if row.SelectedMemoryID.Valid {
		selectedMemoryID = row.SelectedMemoryID.Int64
	}
	var selectedMemoryVersion any
	if row.SelectedMemoryVersion.Valid {
		selectedMemoryVersion = row.SelectedMemoryVersion.Int64
	}
	var selectedSourceProject any
	if row.SelectedSourceProject.Valid {
		selectedSourceProject = row.SelectedSourceProject.String
	}
	var selectedSourceTier any
	if row.SelectedSourceTier.Valid {
		selectedSourceTier = row.SelectedSourceTier.Int64
	}
	var selectedPolicyID any
	if row.SelectedPolicyID != nil {
		selectedPolicyID = row.SelectedPolicyID
	}
	var selectedSnapshotID any
	if row.SelectedSnapshotID.Valid {
		selectedSnapshotID = row.SelectedSnapshotID.String
	}
	var selectedSnapshotVersion any
	if row.SelectedSnapshotVersion.Valid {
		selectedSnapshotVersion = row.SelectedSnapshotVersion.Int64
	}
	var selectedTextDigest any
	if row.SelectedTextDigest != nil {
		selectedTextDigest = row.SelectedTextDigest
	}
	return []any{
		row.ReceiptID,
		row.OperationID,
		row.KeyEpochCommitment,
		row.IntegrityDigest,
		row.ChannelKey,
		row.HostFamily,
		row.CanonicalProject,
		row.ActorPrincipal,
		row.ActorKind,
		row.Workstation,
		row.SessionKey,
		row.OccurrenceKey,
		row.ContentCommitment,
		row.CapabilitySnapshotCommitment,
		row.Outcome,
		closedReason,
		row.DecisionMode,
		row.EvaluatedCount,
		row.EligibleCount,
		string(row.SnapshotRefs),
		selectedMemoryID,
		selectedMemoryVersion,
		selectedSourceProject,
		selectedSourceTier,
		selectedPolicyID,
		selectedSnapshotID,
		selectedSnapshotVersion,
		selectedTextDigest,
		row.CreatedAt,
		row.ExpiresAt,
	}
}

func interventionReceiptDigestFromDigest(name string, value intervention.Digest) ([32]byte, error) {
	return interventionReceiptDigestFromBytes(name, value[:])
}

func interventionReceiptDigestFromBytes(name string, value []byte) ([32]byte, error) {
	if len(value) != 32 {
		return [32]byte{}, fmt.Errorf("intervention receipt %s must contain exactly 32 bytes, got %d", name, len(value))
	}
	var result [32]byte
	copy(result[:], value)
	if result == ([32]byte{}) {
		return [32]byte{}, fmt.Errorf("intervention receipt %s must not be zero", name)
	}
	return result, nil
}

func interventionReceiptDigestBytes(value [32]byte) []byte {
	return append([]byte(nil), value[:]...)
}

func interventionReceiptCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func interventionReceiptOpaqueText(value string) bool {
	if value == "" || len(value) > intervention.MaxOpaqueReferenceBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func interventionReceiptPrincipalPair(principal, kind string) bool {
	if principal == "" {
		return kind == ""
	}
	if !interventionReceiptOpaqueText(principal) {
		return false
	}
	switch kind {
	case "human", "agent", "service":
		return true
	default:
		return false
	}
}

func interventionReceiptHostFamilyText(family intervention.HostFamily) (string, error) {
	switch family {
	case intervention.HostFamilyOMP:
		return "omp", nil
	case intervention.HostFamilyClaudeCode:
		return "claude_code", nil
	case intervention.HostFamilyCodex:
		return "codex", nil
	default:
		return "", fmt.Errorf("invalid intervention receipt host family: %w", intervention.ErrInvalidInput)
	}
}

func interventionReceiptHostFamilyFromText(value string) (intervention.HostFamily, error) {
	switch value {
	case "omp":
		return intervention.HostFamilyOMP, nil
	case "claude_code":
		return intervention.HostFamilyClaudeCode, nil
	case "codex":
		return intervention.HostFamilyCodex, nil
	default:
		return 0, fmt.Errorf("persisted intervention receipt has invalid host family %q", value)
	}
}

func interventionReceiptOutcomeText(outcome intervention.ReceiptOutcome) (string, error) {
	switch outcome {
	case intervention.ReceiptOutcomeEmit:
		return "emit", nil
	case intervention.ReceiptOutcomeAbstain:
		return "abstain", nil
	default:
		return "", fmt.Errorf("invalid intervention receipt outcome: %w", intervention.ErrInvalidInput)
	}
}

func interventionReceiptOutcomeFromText(value string) (intervention.ReceiptOutcome, error) {
	switch value {
	case "emit":
		return intervention.ReceiptOutcomeEmit, nil
	case "abstain":
		return intervention.ReceiptOutcomeAbstain, nil
	default:
		return 0, fmt.Errorf("persisted intervention receipt has invalid outcome %q", value)
	}
}

func interventionReceiptDecisionModeText(mode intervention.ReceiptDecisionMode) (string, error) {
	switch mode {
	case intervention.ReceiptDecisionModeEligible:
		return "eligible", nil
	case intervention.ReceiptDecisionModeCanary:
		return "canary", nil
	case intervention.ReceiptDecisionModeNone:
		return "none", nil
	case intervention.ReceiptDecisionModeContextReference:
		return "context_reference", nil
	default:
		return "", fmt.Errorf("invalid intervention receipt decision mode: %w", intervention.ErrInvalidInput)
	}
}

func interventionReceiptDecisionModeFromText(value string) (intervention.ReceiptDecisionMode, error) {
	switch value {
	case "eligible":
		return intervention.ReceiptDecisionModeEligible, nil
	case "canary":
		return intervention.ReceiptDecisionModeCanary, nil
	case "none":
		return intervention.ReceiptDecisionModeNone, nil
	case "context_reference":
		return intervention.ReceiptDecisionModeContextReference, nil
	default:
		return 0, fmt.Errorf("persisted intervention receipt has invalid decision mode %q", value)
	}
}

func interventionReceiptAbstentionReasonText(reason intervention.AbstentionReason) (string, error) {
	switch reason {
	case intervention.AbstentionNoCandidates:
		return "no_candidates", nil
	case intervention.AbstentionPolicyObserving:
		return "policy_observing", nil
	case intervention.AbstentionPolicyShadow:
		return "policy_shadow", nil
	case intervention.AbstentionPolicyCanaryBudget:
		return "policy_canary_budget", nil
	case intervention.AbstentionEvidenceInsufficient:
		return "evidence_insufficient", nil
	case intervention.AbstentionHarmBound:
		return "harm_bound", nil
	case intervention.AbstentionPolicySuppressed:
		return "policy_suppressed", nil
	case intervention.AbstentionTaskFit:
		return "task_fit", nil
	case intervention.AbstentionActionability:
		return "actionability", nil
	case intervention.AbstentionEvidenceState:
		return "evidence_state", nil
	case intervention.AbstentionAlreadyVisible:
		return "already_visible", nil
	case intervention.AbstentionContextBudget:
		return "context_budget", nil
	case intervention.AbstentionAmbiguousConflict:
		return "ambiguous_conflict", nil
	default:
		return "", fmt.Errorf("invalid intervention receipt abstention reason: %w", intervention.ErrInvalidInput)
	}
}

func interventionReceiptAbstentionReasonFromText(value string) (intervention.AbstentionReason, error) {
	switch value {
	case "no_candidates":
		return intervention.AbstentionNoCandidates, nil
	case "policy_observing":
		return intervention.AbstentionPolicyObserving, nil
	case "policy_shadow":
		return intervention.AbstentionPolicyShadow, nil
	case "policy_canary_budget":
		return intervention.AbstentionPolicyCanaryBudget, nil
	case "evidence_insufficient":
		return intervention.AbstentionEvidenceInsufficient, nil
	case "harm_bound":
		return intervention.AbstentionHarmBound, nil
	case "policy_suppressed":
		return intervention.AbstentionPolicySuppressed, nil
	case "task_fit":
		return intervention.AbstentionTaskFit, nil
	case "actionability":
		return intervention.AbstentionActionability, nil
	case "evidence_state":
		return intervention.AbstentionEvidenceState, nil
	case "already_visible":
		return intervention.AbstentionAlreadyVisible, nil
	case "context_budget":
		return intervention.AbstentionContextBudget, nil
	case "ambiguous_conflict":
		return intervention.AbstentionAmbiguousConflict, nil
	default:
		return 0, fmt.Errorf("persisted intervention receipt has invalid abstention reason %q", value)
	}
}
