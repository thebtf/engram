package staleness

import (
	"testing"
	"time"
)

func TestDetectRelativeTime(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"currently", "The flag is currently set to true", []string{"currently"}},
		{"recently phrase", "We recently switched the default", []string{"recently"}},
		{"multi distinct", "Currently the API is at v2; last week it was v1", []string{"currently", "last week"}},
		{"dedup", "currently this, and Currently that", []string{"currently"}},
		{"right now compound", "The build is green right now", []string{"right now"}},
		{"whitespace collapse", "changed last    week", []string{"last week"}},
		// True negatives — these must NOT trip the detector (false-positive guards).
		{"bare now excluded", "the value is known now that we checked", nil},
		{"absolute date ok", "As of 2026-06-17 the dim is 1536", nil},
		{"no temporal", "engram stores memories in PostgreSQL", nil},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DetectRelativeTime(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("DetectRelativeTime(%q) = %v, want %v", tc.content, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("DetectRelativeTime(%q)[%d] = %q, want %q", tc.content, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestIsStaleCandidate_MeasuresFromCreatedAt locks the resolved Codex-review decision:
// staleness is measured from created_at (immutable), NOT updated_at (a row-mutation
// timestamp bumped by injection/citation). An old "currently…" memory must stay flagged
// even though, in production, its updated_at would be freshly bumped by the feedback loop
// on every injection. This test guards against a future flip back to updated_at.
func TestIsStaleCandidate_MeasuresFromCreatedAt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	created := now.Add(-200 * 24 * time.Hour) // created 200 days ago

	if !IsStaleCandidate("the value is currently 1536", created, now, 0) {
		t.Error("an old relative-time memory must be flagged stale by created_at age, " +
			"regardless of any updated_at bump from injection/citation")
	}
}

func TestIsStaleCandidate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour)  // 40 days — past the 30d window
	fresh := now.Add(-5 * 24 * time.Hour) // 5 days — inside the window

	cases := []struct {
		name      string
		content   string
		createdAt time.Time
		window    time.Duration
		want      bool
	}{
		{"relative + old → stale", "currently 1536", old, 0, true},
		{"relative + fresh → not stale", "currently 1536", fresh, 0, false},
		{"absolute + old → not stale", "dim is 1536 as of migration 142", old, 0, false},
		{"no temporal + old → not stale", "stored in PostgreSQL", old, 0, false},
		{"custom window catches sooner", "recently changed", now.Add(-2 * 24 * time.Hour), 24 * time.Hour, true},
		{"zero window uses default", "currently x", old, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := IsStaleCandidate(tc.content, tc.createdAt, now, tc.window)
			if got != tc.want {
				t.Errorf("IsStaleCandidate(%q, age=%v, window=%v) = %v, want %v",
					tc.content, now.Sub(tc.createdAt), tc.window, got, tc.want)
			}
		})
	}
}
