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
	ResultSpaceID        *string    `gorm:"column:result_space_id;type:uuid"`
	ResultViewID         *string    `gorm:"column:result_view_id;type:uuid"`
	ResultGeneration     *int64     `gorm:"column:result_generation"`
	CreatedAt            time.Time  `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt            time.Time  `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (indexIntentRow) TableName() string { return "uci_index_intents" }

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

	var row indexIntentRow
	if err := s.db.WithContext(ctx).Where("intent_id = ?", intentID).First(&row).Error; err != nil {
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
		now := time.Now().UTC()
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
		now := time.Now().UTC()
		claim, err = ucidomain.NewIndexIntentClaim(row.IntentID, owner, row.AcknowledgementEpoch+1, now)
		if err != nil {
			return fmt.Errorf("uci index intent acknowledge: invalid owner")
		}
		row.State = string(ucidomain.IndexIntentAcknowledged)
		row.Attempt++
		row.AcknowledgementEpoch = claim.Epoch
		row.AcknowledgedOwner = indexIntentString(claim.Owner)
		row.AcknowledgedAt = indexIntentTime(claim.AcknowledgedAt)
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
		current, err := indexIntentFromRow(*row)
		if err != nil {
			return err
		}
		if err := ucidomain.ValidateIndexIntentResult(current, result); err != nil {
			return err
		}
		if readable == nil || readable.ReadableIndexIntentView(ctx, result) != nil {
			return ucidomain.ErrIndexIntentResultUnreadable
		}

		now := time.Now().UTC()
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
		row.State = string(ucidomain.IndexIntentQueued)
		row.UpdatedAt = time.Now().UTC()
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

func (s *UCIIndexIntentStore) transition(ctx context.Context, intentID string, apply func(*indexIntentRow, time.Time) error) (ucidomain.IndexIntent, error) {
	var intent ucidomain.IndexIntent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockIndexIntentRow(ctx, tx, intentID, false)
		if err != nil {
			return err
		}
		expectedState := row.State
		if err := apply(row, time.Now().UTC()); err != nil {
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
		expectedState := row.State
		if err := apply(row, time.Now().UTC()); err != nil {
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
		query = query.Where("intent_id = ?", value)
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
		ProfileID:       row.ProfileID,
		PreviousView:    previous,
		State:           ucidomain.IndexIntentState(row.State),
		Attempt:         row.Attempt,
		Acknowledgement: claim,
		ResultView:      result,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
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
	if row.AcknowledgedOwner == nil && row.AcknowledgedAt == nil && row.AcknowledgementEpoch == 0 {
		return nil, nil
	}
	if row.AcknowledgedOwner == nil || row.AcknowledgedAt == nil {
		return nil, fmt.Errorf("uci index intent: incomplete stored acknowledgement")
	}
	claim, err := ucidomain.NewIndexIntentClaim(row.IntentID, *row.AcknowledgedOwner, row.AcknowledgementEpoch, *row.AcknowledgedAt)
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
