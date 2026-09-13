package gorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	ucidomain "github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const uciIndexIntentIDWhere = "intent_id = ?"

var (
	_ ucidomain.IndexIntentStore = (*UCIIndexIntentStore)(nil)

	errUCIIndexIntentStoreNotConfigured = errors.New("uci index intent store: not configured")
)

// indexIntentRow is registered by T034. Its table is intentionally separate
// from ci_jobs: an IndexIntent records a durable request, not worker transport.
type indexIntentRow struct {
	IntentID             string     `gorm:"column:intent_id;type:uuid;primaryKey"`
	RequestRef           string     `gorm:"column:request_ref;type:text;not null;uniqueIndex:uci_index_intents_request_ref"`
	Kind                 string     `gorm:"column:kind;type:text;not null"`
	SourceID             string     `gorm:"column:source_id;type:uuid;not null"`
	CheckoutID           string     `gorm:"column:checkout_id;type:uuid;not null"`
	IncarnationID        string     `gorm:"column:incarnation_id;type:uuid;not null"`
	ProfileID            string     `gorm:"column:profile_id;type:uuid;not null"`
	PreviousSpaceID      *string    `gorm:"column:previous_space_id;type:uuid"`
	PreviousViewID       *string    `gorm:"column:previous_view_id;type:uuid"`
	PreviousGeneration   *int64     `gorm:"column:previous_generation"`
	State                string     `gorm:"column:state;type:text;not null"`
	Attempt              int        `gorm:"column:attempt;not null"`
	AcknowledgedOwner    *string    `gorm:"column:acknowledged_owner;type:text"`
	AcknowledgementEpoch int64      `gorm:"column:acknowledgement_epoch;not null"`
	AcknowledgedAt       *time.Time `gorm:"column:acknowledged_at;type:timestamptz"`
	ClaimExpiresAt       *time.Time `gorm:"column:claim_expires_at;type:timestamptz"`
	PublicationBuildID   *string    `gorm:"column:publication_build_id;type:uuid"`
	ResultSpaceID        *string    `gorm:"column:result_space_id;type:uuid"`
	ResultViewID         *string    `gorm:"column:result_view_id;type:uuid"`
	ResultGeneration     *int64     `gorm:"column:result_generation"`
	CreatedAt            time.Time  `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt            time.Time  `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (indexIntentRow) TableName() string { return "uci_index_intents" }

type indexIntentReceiptRow struct {
	IntentID       string     `gorm:"column:intent_id;type:uuid;primaryKey"`
	OperationRef   string     `gorm:"column:operation_ref;type:text;primaryKey"`
	Operation      string     `gorm:"column:operation;type:text;not null"`
	OwnerKey       string     `gorm:"column:owner_key;type:text;not null"`
	ClaimEpoch     int64      `gorm:"column:claim_epoch;not null"`
	ResultState    string     `gorm:"column:result_state;type:text;not null"`
	ResultAttempt  int        `gorm:"column:result_attempt;not null"`
	LeaseExpiresAt *time.Time `gorm:"column:lease_expires_at;type:timestamptz"`
	CreatedAt      time.Time  `gorm:"column:created_at;type:timestamptz;not null"`
}

func (indexIntentReceiptRow) TableName() string { return "uci_index_intent_receipts" }

// UCIIndexIntentStore persists the private, durable index request lifecycle.
type UCIIndexIntentStore struct {
	db *gorm.DB
}

func NewUCIIndexIntentStore(db *gorm.DB) *UCIIndexIntentStore {
	return &UCIIndexIntentStore{db: db}
}

// GetIndexIntent returns one validated safe intent by its opaque durable ID.
func (s *UCIIndexIntentStore) GetIndexIntent(ctx context.Context, intentID string) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("get"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	if ctx == nil || ctx.Err() != nil || !validUCIIndexIntentID(intentID) {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent get: invalid request")
	}
	if err := s.ReconcileIndexIntent(ctx, intentID); err != nil {
		return ucidomain.IndexIntent{}, err
	}

	var row indexIntentRow
	if err := s.db.WithContext(ctx).Where(uciIndexIntentIDWhere, intentID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ucidomain.IndexIntent{}, ucidomain.ErrIndexIntentNotFound
		}
		return ucidomain.IndexIntent{}, err
	}
	intent, err := indexIntentFromRow(row)
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	return intent.Clone(), nil
}

// GetIndexIntentByRequestRef returns one validated durable intent selected by
// its opaque browser request reference.
func (s *UCIIndexIntentStore) GetIndexIntentByRequestRef(ctx context.Context, requestRef string) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("get by request reference"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	if ctx == nil || ctx.Err() != nil || !ucidomain.ValidIndexIntentRequestRef(requestRef) {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent get by request reference: invalid request")
	}
	var row indexIntentRow
	if err := s.db.WithContext(ctx).Where("request_ref = ?", requestRef).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ucidomain.IndexIntent{}, ucidomain.ErrIndexIntentNotFound
		}
		return ucidomain.IndexIntent{}, err
	}
	return s.GetIndexIntent(ctx, row.IntentID)
}

func (s *UCIIndexIntentStore) SubmitIndexIntent(ctx context.Context, input ucidomain.IndexIntentInput) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("submit"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	input = input.Clone()
	if ctx == nil || ctx.Err() != nil || input.Validate() != nil {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent submit: invalid input")
	}

	var intent ucidomain.IndexIntent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		candidate := indexIntentRowFromInput(uuid.NewString(), input, now)
		created := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "request_ref"}},
			DoNothing: true,
		}).Create(&candidate)
		if created.Error != nil {
			return fmt.Errorf("uci index intent submit create: %w", created.Error)
		}
		if created.RowsAffected == 1 {
			var err error
			intent, err = indexIntentFromRow(candidate)
			return err
		}

		existing, err := lockIndexIntentRow(ctx, tx, input.RequestRef, true)
		if err != nil {
			return err
		}
		if !sameIndexIntentBinding(*existing, input) {
			return ucidomain.ErrIndexIntentBindingMismatch
		}
		intent, err = indexIntentFromRow(*existing)
		return err
	})
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	return intent.Clone(), nil
}

func (s *UCIIndexIntentStore) QueueIndexIntent(ctx context.Context, intentID string) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("queue"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	if ctx == nil || ctx.Err() != nil {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent queue: invalid request")
	}
	return s.transition(ctx, intentID, func(row *indexIntentRow, now time.Time) error {
		if row.State != string(ucidomain.IndexIntentSubmitted) {
			return ucidomain.ErrIndexIntentInvalidTransition
		}
		row.State = string(ucidomain.IndexIntentQueued)
		row.UpdatedAt = now
		return nil
	})
}

func (s *UCIIndexIntentStore) AcknowledgeIndexIntent(ctx context.Context, intentID, owner string) (ucidomain.IndexIntentClaim, error) {
	if err := s.requireDB("acknowledge"); err != nil {
		return ucidomain.IndexIntentClaim{}, err
	}
	if ctx == nil || ctx.Err() != nil {
		return ucidomain.IndexIntentClaim{}, fmt.Errorf("uci index intent acknowledge: invalid request")
	}

	var claim ucidomain.IndexIntentClaim
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockIndexIntentRow(ctx, tx, intentID, false)
		if err != nil {
			return err
		}
		if row.State != string(ucidomain.IndexIntentQueued) {
			return ucidomain.ErrIndexIntentInvalidTransition
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		claim, err = ucidomain.NewLeasedIndexIntentClaim(row.IntentID, owner, row.AcknowledgementEpoch+1, now, now.Add(ucidomain.DefaultIndexPublicationLimits().LeaseTTL))
		if err != nil {
			return fmt.Errorf("uci index intent acknowledge: invalid owner")
		}
		row.State = string(ucidomain.IndexIntentAcknowledged)
		row.Attempt++
		row.AcknowledgementEpoch = claim.Epoch
		row.AcknowledgedOwner = indexIntentString(claim.Owner)
		row.AcknowledgedAt = indexIntentTime(claim.AcknowledgedAt)
		row.ClaimExpiresAt = indexIntentTime(claim.LeaseExpiresAt)
		row.UpdatedAt = now
		return updateIndexIntentRow(ctx, tx, *row, string(ucidomain.IndexIntentQueued), nil)
	})
	if err != nil {
		return ucidomain.IndexIntentClaim{}, err
	}
	return claim, nil
}

func (s *UCIIndexIntentStore) StartIndexIntent(ctx context.Context, claim ucidomain.IndexIntentClaim) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("start"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	if ctx == nil || ctx.Err() != nil || claim.Validate() != nil {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent start: invalid request")
	}
	return s.transitionClaim(ctx, claim, func(row *indexIntentRow, now time.Time) error {
		if row.State != string(ucidomain.IndexIntentAcknowledged) {
			return ucidomain.ErrIndexIntentInvalidTransition
		}
		row.State = string(ucidomain.IndexIntentRunning)
		row.UpdatedAt = now
		return nil
	})
}

func (s *UCIIndexIntentStore) CompleteIndexIntent(ctx context.Context, claim ucidomain.IndexIntentClaim, result ucidomain.ContextRef, readable ucidomain.IndexIntentReadableView) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("complete"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	if ctx == nil || ctx.Err() != nil || claim.Validate() != nil {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent complete: invalid request")
	}
	result = result.Clone()

	var intent ucidomain.IndexIntent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockIndexIntentRow(ctx, tx, claim.IntentID, false)
		if err != nil {
			return err
		}
		if row.State != string(ucidomain.IndexIntentRunning) {
			return ucidomain.ErrIndexIntentInvalidTransition
		}
		if !indexIntentClaimMatches(*row, claim) {
			return ucidomain.ErrIndexIntentOwnerLost
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if err := requireIndexIntentActiveClaim(*row, claim.Owner, claim.Epoch, now); err != nil {
			return err
		}
		current, err := indexIntentFromRow(*row)
		if err != nil {
			return err
		}
		if err := ucidomain.ValidateIndexIntentResult(current, result); err != nil {
			return err
		}
		if current.PreviousView != nil && result.ViewID == current.PreviousView.ViewID && result.Generation == current.PreviousView.Generation {
			return ucidomain.ErrIndexIntentResultInvalid
		}
		if readable == nil || readable.ReadableIndexIntentView(ctx, result) != nil {
			return ucidomain.ErrIndexIntentResultUnreadable
		}

		row.State = string(ucidomain.IndexIntentCompleted)
		row.ResultSpaceID = indexIntentOptionalString(result.SpaceID)
		row.ResultViewID = indexIntentString(result.ViewID)
		row.ResultGeneration = indexIntentInt64(result.Generation)
		row.UpdatedAt = now
		if err := updateIndexIntentRow(ctx, tx, *row, string(ucidomain.IndexIntentRunning), &claim); err != nil {
			return err
		}
		intent, err = indexIntentFromRow(*row)
		return err
	})
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	return intent.Clone(), nil
}

func (s *UCIIndexIntentStore) MarkIndexIntentUnavailable(ctx context.Context, intentID string) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("mark unavailable"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	if ctx == nil || ctx.Err() != nil {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent unavailable: invalid request")
	}
	return s.transition(ctx, intentID, func(row *indexIntentRow, now time.Time) error {
		if row.State != string(ucidomain.IndexIntentSubmitted) && row.State != string(ucidomain.IndexIntentQueued) {
			return ucidomain.ErrIndexIntentInvalidTransition
		}
		row.State = string(ucidomain.IndexIntentUnavailable)
		row.UpdatedAt = now
		return nil
	})
}

func (s *UCIIndexIntentStore) RetryIndexIntent(ctx context.Context, intentID string, authorizer ucidomain.IndexIntentRetryAuthorizer) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("retry"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	if ctx == nil || ctx.Err() != nil || authorizer == nil {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent retry: invalid request")
	}

	var intent ucidomain.IndexIntent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockIndexIntentRow(ctx, tx, intentID, false)
		if err != nil {
			return err
		}
		if row.State != string(ucidomain.IndexIntentUnavailable) {
			return ucidomain.ErrIndexIntentInvalidTransition
		}
		current, err := indexIntentFromRow(*row)
		if err != nil {
			return err
		}
		if authorizer.AuthorizeIndexIntentRetry(ctx, current.Clone()) != nil {
			return ucidomain.ErrIndexIntentRetryUnauthorized
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		row.State = string(ucidomain.IndexIntentQueued)
		row.UpdatedAt = now
		if err := updateIndexIntentRow(ctx, tx, *row, string(ucidomain.IndexIntentUnavailable), nil); err != nil {
			return err
		}
		intent, err = indexIntentFromRow(*row)
		return err
	})
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	return intent.Clone(), nil
}

func (s *UCIIndexIntentStore) FailIndexIntent(ctx context.Context, claim ucidomain.IndexIntentClaim) (ucidomain.IndexIntent, error) {
	if err := s.requireDB("fail"); err != nil {
		return ucidomain.IndexIntent{}, err
	}
	if ctx == nil || ctx.Err() != nil || claim.Validate() != nil {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent failure: invalid request")
	}
	return s.transitionClaim(ctx, claim, func(row *indexIntentRow, now time.Time) error {
		if row.State != string(ucidomain.IndexIntentAcknowledged) && row.State != string(ucidomain.IndexIntentRunning) {
			return ucidomain.ErrIndexIntentInvalidTransition
		}
		row.State = string(ucidomain.IndexIntentFailed)
		row.UpdatedAt = now
		return nil
	})
}

func (s *UCIIndexIntentStore) PollIndexIntent(ctx context.Context, binding ucidomain.IndexBinding, owner ucidomain.IndexIntentOwnerBinding) (*ucidomain.IndexIntent, error) {
	if err := s.requireDB("poll"); err != nil {
		return nil, err
	}
	binding = binding.Clone()
	ownerKey, ownerErr := owner.OwnerKey()
	if ctx == nil || ctx.Err() != nil || binding.Validate() != nil || ownerErr != nil {
		return nil, fmt.Errorf("uci index intent poll: invalid request")
	}
	if err := s.ReconcileIndexIntents(ctx, binding); err != nil {
		return nil, err
	}
	var row indexIntentRow
	err := s.db.WithContext(ctx).Where(
		"source_id = ? AND checkout_id = ? AND incarnation_id = ? AND profile_id = ? AND (state = ? OR (acknowledged_owner = ? AND state = ?) OR (acknowledged_owner = ? AND state = ? AND publication_build_id IS NULL))",
		binding.Scope.SourceID, binding.Scope.CheckoutID, binding.Scope.IncarnationID, binding.ProfileID,
		string(ucidomain.IndexIntentQueued), ownerKey, string(ucidomain.IndexIntentAcknowledged), ownerKey, string(ucidomain.IndexIntentRunning),
	).Order("created_at ASC, intent_id ASC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("uci index intent poll: %w", err)
	}
	intent, err := indexIntentFromRow(row)
	if err != nil {
		return nil, err
	}
	intent = intent.Clone()
	return &intent, nil
}

// LoadIndexIntentClaim resolves a daemon token to the exact persisted owner
// claim after the transport reauthorized the same binding.
func (s *UCIIndexIntentStore) LoadIndexIntentClaim(ctx context.Context, binding ucidomain.IndexBinding, owner ucidomain.IndexIntentOwnerBinding, intentID string, epoch int64, allowCompleted bool) (ucidomain.IndexIntentClaim, error) {
	if err := s.requireDB("load claim"); err != nil {
		return ucidomain.IndexIntentClaim{}, err
	}
	ownerKey, err := owner.OwnerKey()
	if ctx == nil || ctx.Err() != nil || binding.Validate() != nil || !validUCIIndexIntentID(intentID) || epoch < 1 || err != nil {
		return ucidomain.IndexIntentClaim{}, fmt.Errorf("uci index intent claim: invalid request")
	}
	var row indexIntentRow
	if err := s.db.WithContext(ctx).Where(uciIndexIntentIDWhere, intentID).First(&row).Error; err != nil {
		return ucidomain.IndexIntentClaim{}, err
	}
	if !indexIntentRowMatchesBinding(row, binding) || row.AcknowledgedOwner == nil || *row.AcknowledgedOwner != ownerKey || row.AcknowledgementEpoch != epoch {
		return ucidomain.IndexIntentClaim{}, ucidomain.ErrIndexIntentOwnerLost
	}
	if row.State != string(ucidomain.IndexIntentRunning) && !(allowCompleted && row.State == string(ucidomain.IndexIntentCompleted)) {
		return ucidomain.IndexIntentClaim{}, ucidomain.ErrIndexIntentInvalidTransition
	}
	claim, err := indexIntentClaimFromRow(row)
	if err != nil || claim == nil {
		return ucidomain.IndexIntentClaim{}, ucidomain.ErrIndexIntentOwnerLost
	}
	now, err := uciDatabaseClock(ctx, s.db)
	if err != nil {
		return ucidomain.IndexIntentClaim{}, err
	}
	if !allowCompleted && !claim.LeaseExpiresAt.After(now) {
		return ucidomain.IndexIntentClaim{}, ucidomain.ErrIndexIntentLeaseExpired
	}
	return *claim, nil
}

// UpdateIndexIntent applies one exact replay-keyed executor transition. The
// owner key is derived from authenticated transport facts by the caller.
func (s *UCIIndexIntentStore) UpdateIndexIntent(ctx context.Context, binding ucidomain.IndexBinding, owner ucidomain.IndexIntentOwnerBinding, intentID string, update ucidomain.IndexIntentUpdate) (ucidomain.IndexIntentUpdateResult, error) {
	if err := s.requireDB("update"); err != nil {
		return ucidomain.IndexIntentUpdateResult{}, err
	}
	binding = binding.Clone()
	ownerKey, err := owner.OwnerKey()
	if ctx == nil || ctx.Err() != nil || binding.Validate() != nil || !validUCIIndexIntentID(intentID) || update.Validate() != nil || err != nil {
		return ucidomain.IndexIntentUpdateResult{}, fmt.Errorf("uci index intent update: invalid request")
	}

	var result ucidomain.IndexIntentUpdateResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockIndexIntentRow(ctx, tx, intentID, false)
		if err != nil {
			return err
		}
		if !indexIntentRowMatchesBinding(*row, binding) {
			return ucidomain.ErrIndexIntentBindingMismatch
		}
		if receipt, found, err := loadIndexIntentReceipt(ctx, tx, intentID, update.OperationRef); err != nil {
			return err
		} else if found {
			if receipt.Operation != string(update.Operation) || receipt.OwnerKey != ownerKey ||
				(update.Operation != ucidomain.IndexIntentAcknowledge && receipt.ClaimEpoch != update.ClaimEpoch) {
				return ucidomain.ErrIndexIntentBindingMismatch
			}
			result, err = indexIntentUpdateResultFromReceipt(receipt)
			return err
		}

		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		leaseExpiry := now.Add(ucidomain.DefaultIndexPublicationLimits().LeaseTTL)
		expectedState := row.State
		switch update.Operation {
		case ucidomain.IndexIntentAcknowledge:
			if row.State != string(ucidomain.IndexIntentQueued) {
				return ucidomain.ErrIndexIntentInvalidTransition
			}
			row.State = string(ucidomain.IndexIntentAcknowledged)
			row.Attempt++
			row.AcknowledgementEpoch++
			row.AcknowledgedOwner = indexIntentString(ownerKey)
			row.AcknowledgedAt = indexIntentTime(now)
			row.ClaimExpiresAt = indexIntentTime(leaseExpiry)
		case ucidomain.IndexIntentStart:
			if err := requireIndexIntentActiveClaim(*row, ownerKey, update.ClaimEpoch, now); err != nil {
				return err
			}
			if row.State != string(ucidomain.IndexIntentAcknowledged) {
				return ucidomain.ErrIndexIntentInvalidTransition
			}
			row.State = string(ucidomain.IndexIntentRunning)
		case ucidomain.IndexIntentRenew:
			if err := requireIndexIntentActiveClaim(*row, ownerKey, update.ClaimEpoch, now); err != nil {
				return err
			}
			if row.State != string(ucidomain.IndexIntentAcknowledged) && row.State != string(ucidomain.IndexIntentRunning) {
				return ucidomain.ErrIndexIntentInvalidTransition
			}
			row.ClaimExpiresAt = indexIntentTime(leaseExpiry)
		case ucidomain.IndexIntentFail:
			if row.State == string(ucidomain.IndexIntentCompleted) {
				if row.AcknowledgedOwner == nil || *row.AcknowledgedOwner != ownerKey || row.AcknowledgementEpoch != update.ClaimEpoch {
					return ucidomain.ErrIndexIntentOwnerLost
				}
				break
			}
			if err := requireIndexIntentActiveClaim(*row, ownerKey, update.ClaimEpoch, now); err != nil {
				return err
			}
			if row.State != string(ucidomain.IndexIntentAcknowledged) && row.State != string(ucidomain.IndexIntentRunning) {
				return ucidomain.ErrIndexIntentInvalidTransition
			}
			row.State = string(ucidomain.IndexIntentFailed)
		default:
			return ucidomain.ErrIndexIntentInvalidTransition
		}
		row.UpdatedAt = now
		if err := updateIndexIntentRow(ctx, tx, *row, expectedState, nil); err != nil {
			return err
		}
		result = ucidomain.IndexIntentUpdateResult{
			IntentID: row.IntentID, State: ucidomain.IndexIntentState(row.State), Attempt: row.Attempt,
			ClaimEpoch: row.AcknowledgementEpoch, LeaseExpiresAt: *row.ClaimExpiresAt,
		}
		if err := result.Validate(); err != nil {
			return err
		}
		receipt := indexIntentReceiptRow{
			IntentID: row.IntentID, OperationRef: update.OperationRef, Operation: string(update.Operation), OwnerKey: ownerKey,
			ClaimEpoch: row.AcknowledgementEpoch, ResultState: row.State, ResultAttempt: row.Attempt,
			LeaseExpiresAt: indexIntentTime(result.LeaseExpiresAt), CreatedAt: now,
		}
		return tx.WithContext(ctx).Create(&receipt).Error
	})
	if err != nil {
		return ucidomain.IndexIntentUpdateResult{}, err
	}
	return result, nil
}

func (s *UCIIndexIntentStore) ReconcileIndexIntents(ctx context.Context, binding ucidomain.IndexBinding) error {
	var ids []string
	if err := s.db.WithContext(ctx).Model(&indexIntentRow{}).
		Where("source_id = ? AND checkout_id = ? AND incarnation_id = ? AND profile_id = ? AND state IN ?",
			binding.Scope.SourceID, binding.Scope.CheckoutID, binding.Scope.IncarnationID, binding.ProfileID,
			[]string{string(ucidomain.IndexIntentAcknowledged), string(ucidomain.IndexIntentRunning)}).
		Order("created_at ASC, intent_id ASC").Limit(16).Pluck("intent_id", &ids).Error; err != nil {
		return err
	}
	for _, intentID := range ids {
		if err := s.ReconcileIndexIntent(ctx, intentID); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileIndexIntent closes expired owners and recovers a committed linked
// publication before exposing status. Locks follow checkout -> job -> intent.
func (s *UCIIndexIntentStore) ReconcileIndexIntent(ctx context.Context, intentID string) error {
	if s == nil || s.db == nil || ctx == nil || ctx.Err() != nil || !validUCIIndexIntentID(intentID) {
		return nil
	}
	var snapshot indexIntentRow
	if err := s.db.WithContext(ctx).Where(uciIndexIntentIDWhere, intentID).First(&snapshot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if snapshot.State != string(ucidomain.IndexIntentAcknowledged) && snapshot.State != string(ucidomain.IndexIntentRunning) {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		checkout, err := lockUCIPublicationCheckout(ctx, tx, ucidomain.IndexScope{
			SourceID: snapshot.SourceID, CheckoutID: snapshot.CheckoutID, IncarnationID: snapshot.IncarnationID,
		})
		if err != nil {
			return nil
		}
		var job *UCIJob
		if snapshot.PublicationBuildID != nil {
			job, err = lockUCIPublicationJobByID(ctx, tx, *snapshot.PublicationBuildID)
			if err != nil {
				return err
			}
		}
		row, err := lockIndexIntentRow(ctx, tx, intentID, false)
		if err != nil {
			return err
		}
		if row.State != string(ucidomain.IndexIntentAcknowledged) && row.State != string(ucidomain.IndexIntentRunning) {
			return nil
		}
		if job != nil && (job.IndexIntentID == nil || *job.IndexIntentID != row.IntentID) {
			return ucidomain.ErrIndexIntentBindingMismatch
		}
		if job != nil && job.ResultViewID != nil {
			published, err := loadUCIPublishedViewForJob(ctx, tx, *job)
			if err != nil {
				return err
			}
			return completeIndexIntentRowFromPublication(ctx, tx, row, published.Context)
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if row.ClaimExpiresAt != nil && row.ClaimExpiresAt.After(now) {
			return nil
		}
		expectedState := row.State
		row.State = string(ucidomain.IndexIntentFailed)
		row.UpdatedAt = now
		if err := updateIndexIntentRow(ctx, tx, *row, expectedState, nil); err != nil {
			return err
		}
		if job != nil && job.IndexIntentID != nil && *job.IndexIntentID == row.IntentID && job.State == UCIJobRunning {
			code := "INDEX_INTENT_LEASE_EXPIRED"
			if err := tx.WithContext(ctx).Model(&UCIJob{}).Where("job_id = ?", job.JobID).Updates(map[string]any{
				"state": UCIJobFailedTerminal, "error_code": code, "lease_expiry": nil, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if checkout.OwnerInstance != nil && job.LeaseOwner != nil && *checkout.OwnerInstance == *job.LeaseOwner && checkout.LeaseEpoch == *job.OwnerEpoch {
				if err := tx.WithContext(ctx).Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Updates(map[string]any{
					"owner_instance": nil, "lease_expires_at": nil, "updated_at": now,
				}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func completeIndexIntentRowFromPublication(ctx context.Context, tx *gorm.DB, row *indexIntentRow, result ucidomain.ContextRef) error {
	if row == nil {
		return ucidomain.ErrIndexIntentResultInvalid
	}
	if row.State == string(ucidomain.IndexIntentCompleted) {
		stored, err := indexIntentContextFromRow(row.ResultSpaceID, row.ResultViewID, row.ResultGeneration, *row)
		if err != nil || stored == nil || stored.ViewID != result.ViewID || stored.Generation != result.Generation || !sameIndexIntentOptionalString(stored.SpaceID, result.SpaceID) {
			return ucidomain.ErrIndexIntentResultInvalid
		}
		return nil
	}
	intent, err := indexIntentFromRow(*row)
	if err != nil {
		return err
	}
	if err := ucidomain.ValidateIndexIntentResult(intent, result); err != nil {
		return err
	}
	now, err := uciDatabaseClock(ctx, tx)
	if err != nil {
		return err
	}
	expectedState := row.State
	row.State = string(ucidomain.IndexIntentCompleted)
	row.ResultSpaceID = indexIntentOptionalString(result.SpaceID)
	row.ResultViewID = indexIntentString(result.ViewID)
	row.ResultGeneration = indexIntentInt64(result.Generation)
	row.UpdatedAt = now
	return updateIndexIntentRow(ctx, tx, *row, expectedState, nil)
}

func requireIndexIntentActiveClaim(row indexIntentRow, owner string, epoch int64, now time.Time) error {
	if row.AcknowledgedOwner == nil || *row.AcknowledgedOwner != owner || row.AcknowledgementEpoch != epoch {
		return ucidomain.ErrIndexIntentOwnerLost
	}
	if row.ClaimExpiresAt == nil || !row.ClaimExpiresAt.After(now) {
		return ucidomain.ErrIndexIntentLeaseExpired
	}
	return nil
}

func loadIndexIntentReceipt(ctx context.Context, tx *gorm.DB, intentID, operationRef string) (indexIntentReceiptRow, bool, error) {
	var row indexIntentReceiptRow
	err := tx.WithContext(ctx).Where("intent_id = ? AND operation_ref = ?", intentID, operationRef).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return indexIntentReceiptRow{}, false, nil
	}
	if err != nil {
		return indexIntentReceiptRow{}, false, err
	}
	return row, true, nil
}

func indexIntentUpdateResultFromReceipt(row indexIntentReceiptRow) (ucidomain.IndexIntentUpdateResult, error) {
	result := ucidomain.IndexIntentUpdateResult{
		IntentID: row.IntentID, State: ucidomain.IndexIntentState(row.ResultState), Attempt: row.ResultAttempt, ClaimEpoch: row.ClaimEpoch,
	}
	if row.LeaseExpiresAt != nil {
		result.LeaseExpiresAt = row.LeaseExpiresAt.UTC()
	}
	return result, result.Validate()
}

func indexIntentRowMatchesBinding(row indexIntentRow, binding ucidomain.IndexBinding) bool {
	return row.SourceID == binding.Scope.SourceID && row.CheckoutID == binding.Scope.CheckoutID &&
		row.IncarnationID == binding.Scope.IncarnationID && row.ProfileID == binding.ProfileID && binding.WorkstationID != ""
}

func (s *UCIIndexIntentStore) transition(ctx context.Context, intentID string, apply func(*indexIntentRow, time.Time) error) (ucidomain.IndexIntent, error) {
	var intent ucidomain.IndexIntent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockIndexIntentRow(ctx, tx, intentID, false)
		if err != nil {
			return err
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		expectedState := row.State
		if err := apply(row, now); err != nil {
			return err
		}
		if err := updateIndexIntentRow(ctx, tx, *row, expectedState, nil); err != nil {
			return err
		}
		intent, err = indexIntentFromRow(*row)
		return err
	})
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	return intent.Clone(), nil
}

func (s *UCIIndexIntentStore) transitionClaim(ctx context.Context, claim ucidomain.IndexIntentClaim, apply func(*indexIntentRow, time.Time) error) (ucidomain.IndexIntent, error) {
	var intent ucidomain.IndexIntent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockIndexIntentRow(ctx, tx, claim.IntentID, false)
		if err != nil {
			return err
		}
		if !indexIntentClaimMatches(*row, claim) {
			return ucidomain.ErrIndexIntentOwnerLost
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if err := requireIndexIntentActiveClaim(*row, claim.Owner, claim.Epoch, now); err != nil {
			return err
		}
		expectedState := row.State
		if err := apply(row, now); err != nil {
			return err
		}
		if err := updateIndexIntentRow(ctx, tx, *row, expectedState, &claim); err != nil {
			return err
		}
		intent, err = indexIntentFromRow(*row)
		return err
	})
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	return intent.Clone(), nil
}

func (s *UCIIndexIntentStore) requireDB(operation string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("uci index intent %s: %w", operation, errUCIIndexIntentStoreNotConfigured)
	}
	return nil
}

func validUCIIndexIntentID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func lockIndexIntentRow(ctx context.Context, tx *gorm.DB, value string, byRequestRef bool) (*indexIntentRow, error) {
	var row indexIntentRow
	query := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"})
	if byRequestRef {
		query = query.Where("request_ref = ?", value)
	} else {
		query = query.Where(uciIndexIntentIDWhere, value)
	}
	if err := query.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ucidomain.ErrIndexIntentNotFound
		}
		return nil, err
	}
	return &row, nil
}

func updateIndexIntentRow(ctx context.Context, tx *gorm.DB, row indexIntentRow, expectedState string, claim *ucidomain.IndexIntentClaim) error {
	query := tx.WithContext(ctx).Model(&indexIntentRow{}).Where("intent_id = ? AND state = ?", row.IntentID, expectedState)
	if claim != nil {
		query = query.Where("acknowledged_owner = ? AND acknowledgement_epoch = ?", claim.Owner, claim.Epoch)
	}
	result := query.Updates(map[string]any{
		"state":                 row.State,
		"attempt":               row.Attempt,
		"acknowledged_owner":    row.AcknowledgedOwner,
		"acknowledgement_epoch": row.AcknowledgementEpoch,
		"acknowledged_at":       row.AcknowledgedAt,
		"claim_expires_at":      row.ClaimExpiresAt,
		"publication_build_id":  row.PublicationBuildID,
		"result_space_id":       row.ResultSpaceID,
		"result_view_id":        row.ResultViewID,
		"result_generation":     row.ResultGeneration,
		"updated_at":            row.UpdatedAt,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ucidomain.ErrIndexIntentInvalidTransition
	}
	return nil
}

func indexIntentRowFromInput(intentID string, input ucidomain.IndexIntentInput, now time.Time) indexIntentRow {
	row := indexIntentRow{
		IntentID:      intentID,
		RequestRef:    input.RequestRef,
		Kind:          string(input.Kind),
		SourceID:      input.Scope.SourceID,
		CheckoutID:    input.Scope.CheckoutID,
		IncarnationID: input.Scope.IncarnationID,
		ProfileID:     input.ProfileID,
		State:         string(ucidomain.IndexIntentSubmitted),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if input.PreviousView != nil {
		row.PreviousSpaceID = indexIntentOptionalString(input.PreviousView.SpaceID)
		row.PreviousViewID = indexIntentString(input.PreviousView.ViewID)
		row.PreviousGeneration = indexIntentInt64(input.PreviousView.Generation)
	}
	return row
}

func indexIntentFromRow(row indexIntentRow) (ucidomain.IndexIntent, error) {
	previous, err := indexIntentContextFromRow(row.PreviousSpaceID, row.PreviousViewID, row.PreviousGeneration, row)
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	result, err := indexIntentContextFromRow(row.ResultSpaceID, row.ResultViewID, row.ResultGeneration, row)
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	claim, err := indexIntentClaimFromRow(row)
	if err != nil {
		return ucidomain.IndexIntent{}, err
	}
	intent := ucidomain.IndexIntent{
		ID:         row.IntentID,
		RequestRef: row.RequestRef,
		Kind:       ucidomain.IndexIntentKind(row.Kind),
		Scope: ucidomain.IndexScope{
			SourceID:      row.SourceID,
			CheckoutID:    row.CheckoutID,
			IncarnationID: row.IncarnationID,
		},
		ProfileID:          row.ProfileID,
		PreviousView:       previous,
		State:              ucidomain.IndexIntentState(row.State),
		Attempt:            row.Attempt,
		Acknowledgement:    claim,
		PublicationBuildID: indexIntentOptionalValue(row.PublicationBuildID),
		ResultView:         result,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
	if err := intent.Validate(); err != nil {
		return ucidomain.IndexIntent{}, fmt.Errorf("uci index intent: invalid stored record: %w", err)
	}
	return intent, nil
}

func indexIntentContextFromRow(spaceID, viewID *string, generation *int64, row indexIntentRow) (*ucidomain.ContextRef, error) {
	if spaceID == nil && viewID == nil && generation == nil {
		return nil, nil
	}
	if viewID == nil || generation == nil {
		return nil, fmt.Errorf("uci index intent: incomplete stored view")
	}
	ref := ucidomain.ContextRef{
		SpaceID:           indexIntentOptionalString(spaceID),
		SourceID:          row.SourceID,
		CheckoutID:        row.CheckoutID,
		ViewID:            *viewID,
		AnalysisProfileID: row.ProfileID,
		Generation:        *generation,
	}
	return &ref, nil
}

func indexIntentClaimFromRow(row indexIntentRow) (*ucidomain.IndexIntentClaim, error) {
	if row.AcknowledgedOwner == nil && row.AcknowledgedAt == nil && row.ClaimExpiresAt == nil && row.AcknowledgementEpoch == 0 {
		return nil, nil
	}
	if row.AcknowledgedOwner == nil || row.AcknowledgedAt == nil || row.ClaimExpiresAt == nil {
		return nil, fmt.Errorf("uci index intent: incomplete stored acknowledgement")
	}
	claim, err := ucidomain.NewLeasedIndexIntentClaim(row.IntentID, *row.AcknowledgedOwner, row.AcknowledgementEpoch, *row.AcknowledgedAt, *row.ClaimExpiresAt)
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

func sameIndexIntentBinding(row indexIntentRow, input ucidomain.IndexIntentInput) bool {
	return row.Kind == string(input.Kind) &&
		row.SourceID == input.Scope.SourceID &&
		row.CheckoutID == input.Scope.CheckoutID &&
		row.IncarnationID == input.Scope.IncarnationID &&
		row.ProfileID == input.ProfileID &&
		sameIndexIntentStoredView(row.PreviousSpaceID, row.PreviousViewID, row.PreviousGeneration, input.PreviousView)
}

func sameIndexIntentStoredView(spaceID, viewID *string, generation *int64, view *ucidomain.ContextRef) bool {
	if view == nil {
		return spaceID == nil && viewID == nil && generation == nil
	}
	return viewID != nil && generation != nil && *viewID == view.ViewID && *generation == view.Generation && sameIndexIntentOptionalString(spaceID, view.SpaceID)
}

func indexIntentClaimMatches(row indexIntentRow, claim ucidomain.IndexIntentClaim) bool {
	return row.IntentID == claim.IntentID && row.AcknowledgedOwner != nil && *row.AcknowledgedOwner == claim.Owner && row.AcknowledgementEpoch == claim.Epoch
}

func indexIntentOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func indexIntentString(value string) *string {
	copy := value
	return &copy
}

func indexIntentInt64(value int64) *int64 {
	copy := value
	return &copy
}

func indexIntentTime(value time.Time) *time.Time {
	copy := value
	return &copy
}

func sameIndexIntentOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func indexIntentOptionalValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
