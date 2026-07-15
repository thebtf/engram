package s4bsurfacing

import (
	"context"
	"errors"
	"fmt"

	"github.com/thebtf/engram/internal/cognitive/s4directives"
)

const sourceScanLimit = 50

var (
	ErrNoSource      = errors.New("s4b directive source not configured")
	ErrSourceFailure = errors.New("s4b directive source failure")
)

// Source is the bounded read seam for persisted S4a attention events.
type Source interface {
	ListByProject(ctx context.Context, project string, limit int) ([]s4directives.StoredAttentionEvent, error)
}

// SourceError distinguishes source failures from filtering and matching outcomes.
type SourceError struct {
	Err error
}

func (e *SourceError) Error() string {
	if e == nil || e.Err == nil {
		return ErrSourceFailure.Error()
	}
	return fmt.Sprintf("%s: %v", ErrSourceFailure, e.Err)
}

func (e *SourceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *SourceError) Is(target error) bool { return target == ErrSourceFailure }
