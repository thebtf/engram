package crystallization

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- SelectMode tests ---

func TestSelectMode_SingleShortSession_PerSession(t *testing.T) {
	mode := SelectMode(1, 1*time.Hour, false)
	assert.Equal(t, ModePerSession, mode,
		"1 session, 1h span, not shared → ModePerSession")
}

func TestSelectMode_MultipleSessions_PerBatch(t *testing.T) {
	mode := SelectMode(5, 30*time.Minute, false)
	assert.Equal(t, ModePerBatch, mode,
		"5 sessions → ModePerBatch regardless of span")
}

func TestSelectMode_LongSpan_PerBatch(t *testing.T) {
	mode := SelectMode(1, 25*time.Hour, false)
	assert.Equal(t, ModePerBatch, mode,
		"span > 24h → ModePerBatch")
}

func TestSelectMode_SharedProject_PerBatch(t *testing.T) {
	mode := SelectMode(1, 10*time.Minute, true)
	assert.Equal(t, ModePerBatch, mode,
		"sharedProject=true → ModePerBatch")
}

func TestSelectMode_ExactlyAtBoundary_PerSession(t *testing.T) {
	// sessionCount==1, span==24h exactly (not strictly greater), not shared → per-session
	mode := SelectMode(1, 24*time.Hour, false)
	assert.Equal(t, ModePerSession, mode,
		"span==24h (not > 24h) → ModePerSession")
}

// --- BuildDigest tests ---

func TestBuildDigest_PerSession_ContainsSessionLabel(t *testing.T) {
	out := BuildDigest([]string{"Agent decided to use Redis."}, ModePerSession)
	assert.Contains(t, out, "SESSION DIGEST", "per-session output must include SESSION DIGEST header")
	assert.Contains(t, out, "[SESSION 1]", "per-session output must label the first transcript")
	assert.Contains(t, out, "Agent decided to use Redis.")
}

func TestBuildDigest_PerBatch_ContainsBatchLabel(t *testing.T) {
	transcripts := []string{
		"Session A: decided to use Go.",
		"Session B: chose PostgreSQL.",
	}
	out := BuildDigest(transcripts, ModePerBatch)
	assert.Contains(t, out, "BATCH DIGEST", "per-batch output must include BATCH DIGEST header")
	assert.Contains(t, out, "BATCH_SEPARATOR", "per-batch output must include separator between transcripts")
	assert.Contains(t, out, "[TRANSCRIPT 1]")
	assert.Contains(t, out, "[TRANSCRIPT 2]")
}

// TestBuildDigest_DistinctOutputs is the key assertion: the same input set fed
// through ModePerSession vs ModePerBatch must produce different strings.
func TestBuildDigest_DistinctOutputs(t *testing.T) {
	transcripts := []string{"Decided to cache aggressively.", "Going forward, use HTTPS only."}
	sessionOut := BuildDigest(transcripts, ModePerSession)
	batchOut := BuildDigest(transcripts, ModePerBatch)
	assert.NotEqual(t, sessionOut, batchOut,
		"ModePerSession and ModePerBatch must produce distinct output for the same input")
}

func TestBuildDigest_EmptyTranscripts_ReturnsEmpty(t *testing.T) {
	assert.Empty(t, BuildDigest(nil, ModePerSession))
	assert.Empty(t, BuildDigest([]string{}, ModePerBatch))
}
