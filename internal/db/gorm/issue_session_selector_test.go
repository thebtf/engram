package gorm

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssueSourceSessionSelector(t *testing.T) {
	for _, value := range []string{"", "host/session:42", "01234567-89ab-cdef-0123-456789abcdef", strings.Repeat("x", 256)} {
		require.True(t, ValidIssueSessionSelector(value), "valid selector %q", value)
	}
	for _, value := range []string{" bad", "bad ", "bad\n", "bad\u200b", "\xff", strings.Repeat("x", 257), "00000000-0000-0000-0000-000000000000"} {
		require.False(t, ValidIssueSessionSelector(value), "invalid selector %q", value)
		store := NewIssueStore(nil)
		for _, params := range []IssueListParams{{SourceSession: value}, {TargetSession: value}, {ParticipantSession: value}} {
			params.Limit = 1
			_, err := store.ListTaskIssues(context.Background(), IssueTaskListParams{IssueListParams: params})
			require.Error(t, err, "invalid selector must fail before database access")
			_, _, err = store.ListIssuesEx(context.Background(), params)
			require.ErrorIs(t, err, ErrIssueInvalidInput)
		}
	}
}

func TestIssueSessionProvenance(t *testing.T) {
	transport := selectIssueAttribution("transport-id", "reported-id", "omp")
	require.Equal(t, issueAttribution{Session: "transport-id", Source: "transport", Client: "omp"}, transport)
	reported := selectIssueAttribution("bad\n", "reported-id", "openclaw")
	require.Equal(t, issueAttribution{Session: "reported-id", Source: "reported", Client: "openclaw"}, reported)
	unknown := selectIssueAttribution("bad\n", " bad", "unrecognized")
	require.Equal(t, issueAttribution{}, unknown)
}
