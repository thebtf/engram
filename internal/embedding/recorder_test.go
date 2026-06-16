package embedding

import (
	"strings"
	"testing"
	"time"
)

// TestBackfillRecorder_ZeroValue asserts Snapshot on a brand-new recorder returns
// all-zero counters and a zero-time EmbedError — no panic on uninitialized use.
func TestBackfillRecorder_ZeroValue(t *testing.T) {
	var rec BackfillRecorder
	succ, fail, lastErr := rec.Snapshot()
	if succ != 0 {
		t.Errorf("successCount = %d, want 0", succ)
	}
	if fail != 0 {
		t.Errorf("failureCount = %d, want 0", fail)
	}
	if !lastErr.At.IsZero() {
		t.Errorf("lastErr.At = %v, want zero time", lastErr.At)
	}
}

// TestBackfillRecorder_RecordSuccess accumulates success counts correctly.
func TestBackfillRecorder_RecordSuccess(t *testing.T) {
	var rec BackfillRecorder
	rec.RecordSuccess(10)
	rec.RecordSuccess(5)
	succ, fail, _ := rec.Snapshot()
	if succ != 15 {
		t.Errorf("successCount = %d, want 15", succ)
	}
	if fail != 0 {
		t.Errorf("failureCount = %d, want 0", fail)
	}
}

// TestBackfillRecorder_RecordFailure stores the most-recent error details.
func TestBackfillRecorder_RecordFailure(t *testing.T) {
	var rec BackfillRecorder
	before := time.Now()
	rec.RecordFailure(429, "rate limited by upstream")
	after := time.Now()

	_, fail, lastErr := rec.Snapshot()
	if fail != 1 {
		t.Errorf("failureCount = %d, want 1", fail)
	}
	if lastErr.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", lastErr.StatusCode)
	}
	if lastErr.Message != "rate limited by upstream" {
		t.Errorf("Message = %q, want 'rate limited by upstream'", lastErr.Message)
	}
	if lastErr.At.Before(before) || lastErr.At.After(after) {
		t.Errorf("At = %v, want between %v and %v", lastErr.At, before, after)
	}
}

// TestBackfillRecorder_RecordFailure_OverwritesPrevious asserts that only the
// most-recent failure is retained.
func TestBackfillRecorder_RecordFailure_OverwritesPrevious(t *testing.T) {
	var rec BackfillRecorder
	rec.RecordFailure(500, "first error")
	rec.RecordFailure(503, "second error")

	_, fail, lastErr := rec.Snapshot()
	if fail != 2 {
		t.Errorf("failureCount = %d, want 2", fail)
	}
	if lastErr.StatusCode != 503 {
		t.Errorf("StatusCode = %d, want 503 (most recent)", lastErr.StatusCode)
	}
	if lastErr.Message != "second error" {
		t.Errorf("Message = %q, want 'second error'", lastErr.Message)
	}
}

// TestBackfillRecorder_MessageTruncation asserts messages longer than 200 chars
// are truncated rather than stored verbatim.
func TestBackfillRecorder_MessageTruncation(t *testing.T) {
	var rec BackfillRecorder
	longMsg := strings.Repeat("x", 300)
	rec.RecordFailure(0, longMsg)
	_, _, lastErr := rec.Snapshot()
	if len(lastErr.Message) != 200 {
		t.Errorf("len(Message) = %d, want 200 (truncated)", len(lastErr.Message))
	}
}

// TestBackfillRecorder_MixedCounts asserts combined success+failure accounting.
func TestBackfillRecorder_MixedCounts(t *testing.T) {
	var rec BackfillRecorder
	rec.RecordSuccess(50)
	rec.RecordFailure(0, "embed error")
	rec.RecordSuccess(25)
	rec.RecordFailure(422, "invalid input")

	succ, fail, lastErr := rec.Snapshot()
	if succ != 75 {
		t.Errorf("successCount = %d, want 75", succ)
	}
	if fail != 2 {
		t.Errorf("failureCount = %d, want 2", fail)
	}
	if lastErr.StatusCode != 422 {
		t.Errorf("StatusCode = %d, want 422", lastErr.StatusCode)
	}
}
