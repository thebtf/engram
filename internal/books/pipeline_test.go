package books_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	booksdomain "github.com/thebtf/engram/internal/books"
)

type residualJobStore struct {
	calls int
}

func (s *residualJobStore) RetireNonterminal(_ context.Context, reason string) (int64, error) {
	s.calls++
	if reason != booksdomain.RetirementFailureReason {
		return 0, fmt.Errorf("unexpected reason %q", reason)
	}
	return 2, nil
}

func TestRetireResidualJobsRequiresQuiescence(t *testing.T) {
	store := &residualJobStore{}
	_, err := booksdomain.RetireResidualJobs(context.Background(), store, false)
	require.ErrorIs(t, err, booksdomain.ErrBookWriterNotQuiesced)
	assert.Zero(t, store.calls)

	changed, err := booksdomain.RetireResidualJobs(context.Background(), store, true)
	require.NoError(t, err)
	assert.Equal(t, int64(2), changed)
	assert.Equal(t, 1, store.calls)
}

func TestRetireResidualJobsRejectsNilStore(t *testing.T) {
	_, err := booksdomain.RetireResidualJobs(context.Background(), nil, true)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, booksdomain.ErrBookWriterNotQuiesced)
}
