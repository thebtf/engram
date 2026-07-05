package s6

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplySignificanceRatingRejectsNilMemory(t *testing.T) {
	t.Parallel()

	err := ApplySignificanceRating(nil, RatingUseful)
	require.ErrorContains(t, err, "memory is nil")
}
