package mcp

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func readBoundArgs(t *testing.T, raw string) map[string]any {
	t.Helper()
	args, err := parseArgs(json.RawMessage(raw))
	require.NoError(t, err)
	return args
}

func TestDocumentReadInputBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		want    int
		present bool
		wantErr bool
	}{
		{name: "omitted", raw: `{}`, want: 0},
		{name: "exact", raw: `{"version":2}`, want: 2, present: true},
		{name: "fraction", raw: `{"version":2.5}`, wantErr: true},
		{name: "string", raw: `{"version":"2"}`, wantErr: true},
		{name: "boolean", raw: `{"version":true}`, wantErr: true},
		{name: "zero", raw: `{"version":0}`, wantErr: true},
		{name: "negative", raw: `{"version":-1}`, wantErr: true},
		{name: "overflow", raw: `{"version":9223372036854775808}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, present, err := parseDocumentVersion(readBoundArgs(t, tc.raw))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.present, present)
		})
	}

	for _, value := range []any{math.NaN(), math.Inf(1)} {
		_, _, err := parseDocumentVersion(map[string]any{"version": value})
		require.Error(t, err)
	}
}

func TestDocumentListAndHistoryLimitBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		list    int
		history int
		wantErr bool
	}{
		{name: "omitted defaults", raw: `{}`, list: 50, history: 0},
		{name: "positive", raw: `{"limit":1}`, list: 1, history: 1},
		{name: "maximum", raw: `{"limit":100}`, list: 100, history: 100},
		{name: "zero", raw: `{"limit":0}`, wantErr: true},
		{name: "negative", raw: `{"limit":-1}`, wantErr: true},
		{name: "huge", raw: `{"limit":101}`, wantErr: true},
		{name: "fraction", raw: `{"limit":1.5}`, wantErr: true},
		{name: "overflow", raw: `{"limit":9223372036854775808}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := readBoundArgs(t, tc.raw)
			list, listErr := parseDocumentListLimit(args)
			history, historyErr := parseDocumentHistoryLimit(args)
			if tc.wantErr {
				require.Error(t, listErr)
				require.Error(t, historyErr)
				return
			}
			require.NoError(t, listErr)
			require.NoError(t, historyErr)
			require.Equal(t, tc.list, list)
			require.Equal(t, tc.history, history)
		})
	}
}

func TestIssueReadInputBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		limit   int
		since   int64
		wantErr bool
	}{
		{name: "omitted defaults", raw: `{}`, limit: 20},
		{name: "positive", raw: `{"limit":100,"resolved_since":1}`, limit: 100, since: 1},
		{name: "zero limit", raw: `{"limit":0}`, wantErr: true},
		{name: "negative limit", raw: `{"limit":-1}`, wantErr: true},
		{name: "huge limit", raw: `{"limit":101}`, wantErr: true},
		{name: "fraction limit", raw: `{"limit":1.5}`, wantErr: true},
		{name: "zero timestamp", raw: `{"resolved_since":0}`, wantErr: true},
		{name: "negative timestamp", raw: `{"resolved_since":-1}`, wantErr: true},
		{name: "overflow timestamp", raw: `{"resolved_since":9223372036854775808}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := readBoundArgs(t, tc.raw)
			limit, limitErr := parseIssueListLimit(args)
			since, sinceErr := parseIssueResolvedSince(args)
			if tc.wantErr {
				require.True(t, limitErr != nil || sinceErr != nil)
				return
			}
			require.NoError(t, limitErr)
			require.NoError(t, sinceErr)
			require.Equal(t, tc.limit, limit)
			require.Equal(t, tc.since, since)
		})
	}

	for _, raw := range []string{`{"id":1.5}`, `{"id":"1"}`, `{"id":true}`, `{"id":9223372036854775808}`} {
		args := readBoundArgs(t, raw)
		_, err := NewServer(ServerOptions{}).handleIssueGet(t.Context(), args)
		require.Error(t, err)
	}
}
