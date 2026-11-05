package sqlitetest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMembershipSQLRejectsUnsupportedRegex(t *testing.T) {
	_, err := MembershipSQL("SELECT 1 FROM project_consolidation_redirects WHERE selector ~ 'unsupported'")
	require.ErrorContains(t, err, "unsupported regex")
}
