package embedding

import (
	"sync"
	"time"
	"unicode/utf8"
)

// EmbedError holds the details of the most-recent embedding failure seen by
// the backfill loop. All fields are zero-value when no error has occurred.
type EmbedError struct {
	At         time.Time `json:"at"`
	StatusCode int       `json:"status_code,omitempty"`
	Message    string    `json:"message,omitempty"` // truncated to ≤200 chars
}

// BackfillRecorder is a lightweight, process-lifetime counter for embedding
// backfill outcomes. It is written by the Backfill goroutine and read by the
// stats/vnext HTTP handler. All methods are safe for concurrent use.
type BackfillRecorder struct {
	mu           sync.Mutex
	successCount int64
	failureCount int64
	lastError    EmbedError
}

// RecordSuccess increments the success counter by n (number of chunks stored).
func (r *BackfillRecorder) RecordSuccess(n int) {
	r.mu.Lock()
	r.successCount += int64(n)
	r.mu.Unlock()
}

// RecordFailure increments the failure counter and stores the most-recent error.
// statusCode is 0 when the error is not an HTTP-level failure.
// msg is truncated to 200 runes to keep the telemetry payload compact; truncation
// is rune-aware so a non-ASCII error message never stores an invalid UTF-8 sequence.
func (r *BackfillRecorder) RecordFailure(statusCode int, msg string) {
	const maxMsg = 200
	if utf8.RuneCountInString(msg) > maxMsg {
		msg = string([]rune(msg)[:maxMsg])
	}
	r.mu.Lock()
	r.failureCount++
	r.lastError = EmbedError{
		At:         time.Now().UTC(),
		StatusCode: statusCode,
		Message:    msg,
	}
	r.mu.Unlock()
}

// Snapshot returns a consistent read of the recorder's counters.
func (r *BackfillRecorder) Snapshot() (successCount, failureCount int64, lastErr EmbedError) {
	r.mu.Lock()
	successCount = r.successCount
	failureCount = r.failureCount
	lastErr = r.lastError
	r.mu.Unlock()
	return
}
