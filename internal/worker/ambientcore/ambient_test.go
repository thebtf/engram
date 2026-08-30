package ambientcore

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestBoundedAdditionalContextCapsUTF8WithoutSplittingRune(t *testing.T) {
	value := strings.Repeat("x", MaxAdditionalContextBytes-1) + "界" + "trailing"
	bounded := BoundedAdditionalContext(value)
	require.LessOrEqual(t, len(bounded), MaxAdditionalContextBytes)
	require.True(t, utf8.ValidString(bounded))
}
