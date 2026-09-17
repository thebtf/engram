package uci

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	semanticContinuationTokenVersion = "usc1"
	semanticContinuationTTL          = 10 * time.Minute
)

// SemanticContinuationStore persists the private ranking state behind an opaque
// semantic cursor. Implementations must remove expired rows on create and load.
type SemanticContinuationStore interface {
	CreateSemanticContinuation(context.Context, SemanticContinuation) error
	LoadSemanticContinuation(context.Context, string) (SemanticContinuation, bool, error)
}

// SemanticContinuation holds only the binding and exact vector required to
// resume one hybrid ranking. Query text is deliberately represented only by its digest.
type SemanticContinuation struct {
	CursorRef          string
	Context            ContextRef
	ClientSessionID    string
	ProfileFingerprint string
	QueryDigest        string
	FilterDigest       string
	Mode               QueryMode
	Order              QueryOrder
	Limit              int
	NextOffset         int
	Vector             []float32
	ExpiresAt          time.Time
	CreatedAt          time.Time
}

// Validate verifies that a stored continuation can safely affect hybrid retrieval.
func (continuation SemanticContinuation) Validate() error {
	if _, err := uuid.Parse(continuation.CursorRef); err != nil || !continuation.Context.valid() ||
		!validQueryIdentity(continuation.ClientSessionID, queryMaxClientSessionID) ||
		!validSemanticContinuationDigest(continuation.ProfileFingerprint) ||
		!validSemanticContinuationDigest(continuation.QueryDigest) ||
		!validSemanticContinuationDigest(continuation.FilterDigest) ||
		!continuation.Mode.valid() || continuation.Mode == QueryModeStructure || !continuation.Order.valid() ||
		continuation.Limit < 1 || continuation.Limit > queryMaxItems || continuation.NextOffset < 1 ||
		continuation.CreatedAt.IsZero() || continuation.ExpiresAt.IsZero() || !continuation.ExpiresAt.After(continuation.CreatedAt) {
		return fmt.Errorf("uci semantic: persisted continuation is invalid")
	}
	if err := validateSemanticVector(continuation.Vector, semanticVectorDimension); err != nil {
		return fmt.Errorf("uci semantic: persisted continuation vector is invalid: %w", err)
	}
	return nil
}

// IsSemanticContinuationToken reports whether token is a valid versioned opaque
// semantic cursor reference. It does not perform storage lookup or authorization.
func IsSemanticContinuationToken(token string) bool {
	_, ok := semanticContinuationTokenRef(token)
	return ok
}

func semanticContinuationToken(cursorRef string) string {
	return semanticContinuationTokenVersion + "." + cursorRef
}

func semanticContinuationTokenRef(token string) (string, bool) {
	prefix := semanticContinuationTokenVersion + "."
	if !strings.HasPrefix(token, prefix) || len(token) > queryMaxContinuation {
		return "", false
	}
	cursorRef := strings.TrimPrefix(token, prefix)
	if _, err := uuid.Parse(cursorRef); err != nil {
		return "", false
	}
	return cursorRef, true
}

func semanticProfileFingerprint(profile VectorProfile) string {
	return queryContinuationDigest("semantic-profile", []string{
		profile.ProviderRef,
		profile.Model,
		fmt.Sprintf("%d", profile.Dimension),
		profile.PreprocessingRevision,
		fmt.Sprintf("%t", profile.IncludeRelativePath),
	})
}

func validSemanticContinuationDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
