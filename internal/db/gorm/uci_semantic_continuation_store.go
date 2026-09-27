package gorm

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	ucidomain "github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
)

var _ ucidomain.SemanticContinuationStore = (*UCIProjectionStore)(nil)

// CreateSemanticContinuation persists one short-lived exact hybrid ranking and
// removes expired continuations in the same traffic-bound transaction.
func (s *UCIProjectionStore) CreateSemanticContinuation(ctx context.Context, continuation ucidomain.SemanticContinuation) error {
	if err := s.requireDB("create semantic continuation"); err != nil {
		return err
	}
	if err := continuation.Validate(); err != nil {
		return err
	}
	row := uciSemanticContinuationRow(continuation)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := deleteExpiredUCISemanticContinuations(ctx, tx); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("uci projection create semantic continuation: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// LoadSemanticContinuation returns the stored ranking only while it remains
// unexpired. Expired state is deleted before every lookup.
func (s *UCIProjectionStore) LoadSemanticContinuation(ctx context.Context, cursorRef string) (ucidomain.SemanticContinuation, bool, error) {
	if err := s.requireDB("load semantic continuation"); err != nil {
		return ucidomain.SemanticContinuation{}, false, err
	}
	if _, err := uuid.Parse(cursorRef); err != nil {
		return ucidomain.SemanticContinuation{}, false, nil
	}
	var row UCISemanticContinuation
	found := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := deleteExpiredUCISemanticContinuations(ctx, tx); err != nil {
			return err
		}
		result := tx.WithContext(ctx).Where("cursor_ref = ?", cursorRef).First(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if result.Error != nil {
			return fmt.Errorf("uci projection load semantic continuation: %w", result.Error)
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return ucidomain.SemanticContinuation{}, false, err
	}
	continuation := uciSemanticContinuationFromRow(row)
	if err := continuation.Validate(); err != nil {
		return ucidomain.SemanticContinuation{}, false, fmt.Errorf("uci projection semantic continuation is corrupt: %w", err)
	}
	return continuation, true, nil
}

func deleteExpiredUCISemanticContinuations(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Where("expires_at <= CURRENT_TIMESTAMP").Delete(&UCISemanticContinuation{}).Error; err != nil {
		return fmt.Errorf("uci projection cleanup semantic continuations: %w", err)
	}
	return nil
}

func uciSemanticContinuationRow(continuation ucidomain.SemanticContinuation) UCISemanticContinuation {
	return UCISemanticContinuation{
		CursorRef:          continuation.CursorRef,
		SpaceID:            uciSemanticContinuationSpaceID(continuation.Context.SpaceID),
		SourceID:           continuation.Context.SourceID,
		CheckoutID:         continuation.Context.CheckoutID,
		ViewID:             continuation.Context.ViewID,
		ProfileID:          continuation.Context.AnalysisProfileID,
		Generation:         continuation.Context.Generation,
		ClientSessionID:    continuation.ClientSessionID,
		ProfileFingerprint: continuation.ProfileFingerprint,
		QueryDigest:        continuation.QueryDigest,
		FilterDigest:       continuation.FilterDigest,
		Mode:               string(continuation.Mode),
		QueryOrder:         string(continuation.Order),
		QueryLimit:         continuation.Limit,
		NextOffset:         continuation.NextOffset,
		Vector:             pgvector.NewVector(append([]float32(nil), continuation.Vector...)),
		ExpiresAt:          continuation.ExpiresAt.UTC(),
		CreatedAt:          continuation.CreatedAt.UTC(),
	}
}

func uciSemanticContinuationFromRow(row UCISemanticContinuation) ucidomain.SemanticContinuation {
	return ucidomain.SemanticContinuation{
		CursorRef:          row.CursorRef,
		Context:            ucidomain.ContextRef{SpaceID: uciSemanticContinuationSpaceID(row.SpaceID), SourceID: row.SourceID, CheckoutID: row.CheckoutID, ViewID: row.ViewID, AnalysisProfileID: row.ProfileID, Generation: row.Generation},
		ClientSessionID:    row.ClientSessionID,
		ProfileFingerprint: row.ProfileFingerprint,
		QueryDigest:        row.QueryDigest,
		FilterDigest:       row.FilterDigest,
		Mode:               ucidomain.QueryMode(row.Mode),
		Order:              ucidomain.QueryOrder(row.QueryOrder),
		Limit:              row.QueryLimit,
		NextOffset:         row.NextOffset,
		Vector:             append([]float32(nil), row.Vector.Slice()...),
		ExpiresAt:          row.ExpiresAt.UTC(),
		CreatedAt:          row.CreatedAt.UTC(),
	}
}

func uciSemanticContinuationSpaceID(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
