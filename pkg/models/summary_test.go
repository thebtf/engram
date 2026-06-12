// Package models contains domain models for engram.
package models

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// Contract: NewSessionSummary
// - Maps sdkSessionID and project directly onto the struct.
// - Non-empty parsed fields produce Valid sql.NullString; empty fields produce
//   Invalid sql.NullString (never stored as NULL-with-value).
// - promptNumber > 0 produces Valid sql.NullInt64; 0 produces Invalid.
// - discoveryTokens is stored verbatim.
// - CreatedAt is an RFC3339 timestamp captured at call time.
// - CreatedAtEpoch is milliseconds since Unix epoch captured at call time.
// -----------------------------------------------------------------------------

func TestNewSessionSummary_FullPayload(t *testing.T) {
	parsed := &ParsedSummary{
		Request:      "Fix the bug in handler.go",
		Investigated: "Looked at error logs",
		Learned:      "The issue was a race condition",
		Completed:    "Fixed the race condition",
		NextSteps:    "Add more tests",
		Notes:        "Consider adding mutex",
	}

	before := time.Now()
	s := NewSessionSummary("sdk-abc", "my-project", parsed, 7, 2048)
	after := time.Now()

	assert.Equal(t, "sdk-abc", s.SDKSessionID)
	assert.Equal(t, "my-project", s.Project)

	assert.True(t, s.Request.Valid)
	assert.Equal(t, "Fix the bug in handler.go", s.Request.String)
	assert.True(t, s.Investigated.Valid)
	assert.Equal(t, "Looked at error logs", s.Investigated.String)
	assert.True(t, s.Learned.Valid)
	assert.Equal(t, "The issue was a race condition", s.Learned.String)
	assert.True(t, s.Completed.Valid)
	assert.Equal(t, "Fixed the race condition", s.Completed.String)
	assert.True(t, s.NextSteps.Valid)
	assert.Equal(t, "Add more tests", s.NextSteps.String)
	assert.True(t, s.Notes.Valid)
	assert.Equal(t, "Consider adding mutex", s.Notes.String)

	assert.True(t, s.PromptNumber.Valid)
	assert.Equal(t, int64(7), s.PromptNumber.Int64)
	assert.Equal(t, int64(2048), s.DiscoveryTokens)

	// Timestamp must be an RFC3339 value within the call window.
	ts, err := time.Parse(time.RFC3339, s.CreatedAt)
	require.NoError(t, err)
	assert.False(t, ts.Before(before.Truncate(time.Second)), "CreatedAt should not precede the call")
	assert.False(t, ts.After(after.Add(time.Second)), "CreatedAt should not exceed the call window")

	assert.GreaterOrEqual(t, s.CreatedAtEpoch, before.UnixMilli())
	assert.LessOrEqual(t, s.CreatedAtEpoch, after.Add(time.Second).UnixMilli())
}

func TestNewSessionSummary_EmptyOptionalFields(t *testing.T) {
	// Only Request is set; all others are empty strings.
	parsed := &ParsedSummary{Request: "Only request"}

	s := NewSessionSummary("sdk-1", "proj", parsed, 0, 0)

	// Request non-empty → Valid.
	assert.True(t, s.Request.Valid)
	assert.Equal(t, "Only request", s.Request.String)

	// Empty fields → Invalid (NULL in the DB, omitted in JSON).
	assert.False(t, s.Investigated.Valid, "empty Investigated should be Invalid")
	assert.False(t, s.Learned.Valid, "empty Learned should be Invalid")
	assert.False(t, s.Completed.Valid, "empty Completed should be Invalid")
	assert.False(t, s.NextSteps.Valid, "empty NextSteps should be Invalid")
	assert.False(t, s.Notes.Valid, "empty Notes should be Invalid")

	// promptNumber == 0 → Invalid.
	assert.False(t, s.PromptNumber.Valid, "zero prompt number should be Invalid")

	assert.Equal(t, int64(0), s.DiscoveryTokens)
}

func TestNewSessionSummary_PromptNumber_BoundaryOne(t *testing.T) {
	// Exactly 1 is the smallest valid prompt number.
	parsed := &ParsedSummary{Request: "r"}
	s := NewSessionSummary("s", "p", parsed, 1, 0)
	assert.True(t, s.PromptNumber.Valid)
	assert.Equal(t, int64(1), s.PromptNumber.Int64)
}

func TestNewSessionSummary_AllParsedEmpty(t *testing.T) {
	// All fields empty including Request → all NullString values are Invalid.
	parsed := &ParsedSummary{}
	s := NewSessionSummary("s", "p", parsed, 0, 0)
	assert.False(t, s.Request.Valid)
	assert.False(t, s.Investigated.Valid)
	assert.False(t, s.Learned.Valid)
	assert.False(t, s.Completed.Valid)
	assert.False(t, s.NextSteps.Valid)
	assert.False(t, s.Notes.Valid)
	assert.False(t, s.PromptNumber.Valid)
}

func TestNewSessionSummary_TimestampPrecision(t *testing.T) {
	// Both CreatedAt and CreatedAtEpoch must represent the same moment
	// (to within 1 second to account for second truncation in RFC3339).
	parsed := &ParsedSummary{Request: "r"}
	s := NewSessionSummary("s", "p", parsed, 1, 0)

	ts, err := time.Parse(time.RFC3339, s.CreatedAt)
	require.NoError(t, err)

	epochFromString := ts.UnixMilli()
	// Allow 1 000 ms difference because RFC3339 truncates sub-second.
	diff := s.CreatedAtEpoch - epochFromString
	if diff < 0 {
		diff = -diff
	}
	assert.Less(t, diff, int64(1001), "CreatedAtEpoch and CreatedAt must agree within 1 s")
}

// -----------------------------------------------------------------------------
// Contract: SessionSummary.MarshalJSON
// - Valid NullString → field present in JSON with the string value.
// - Invalid NullString → field absent from JSON output (omitempty).
// - Valid NullInt64 → field present; Invalid → absent.
// - Non-nullable fields (ID, SDKSessionID, Project, DiscoveryTokens,
//   CreatedAt, CreatedAtEpoch) are always present.
// -----------------------------------------------------------------------------

func TestSessionSummary_MarshalJSON_ValidFields(t *testing.T) {
	s := &SessionSummary{
		ID:              42,
		SDKSessionID:    "sdk-xyz",
		Project:         "proj-a",
		Request:         sql.NullString{String: "req", Valid: true},
		Investigated:    sql.NullString{String: "inv", Valid: true},
		Learned:         sql.NullString{String: "lrn", Valid: true},
		Completed:       sql.NullString{String: "cmp", Valid: true},
		NextSteps:       sql.NullString{String: "nxt", Valid: true},
		Notes:           sql.NullString{String: "note", Valid: true},
		PromptNumber:    sql.NullInt64{Int64: 3, Valid: true},
		DiscoveryTokens: 500,
		CreatedAt:       "2024-06-01T00:00:00Z",
		CreatedAtEpoch:  1717200000000,
	}

	data, err := json.Marshal(s)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	assert.Equal(t, float64(42), m["id"])
	assert.Equal(t, "sdk-xyz", m["sdk_session_id"])
	assert.Equal(t, "proj-a", m["project"])
	assert.Equal(t, "req", m["request"])
	assert.Equal(t, "inv", m["investigated"])
	assert.Equal(t, "lrn", m["learned"])
	assert.Equal(t, "cmp", m["completed"])
	assert.Equal(t, "nxt", m["next_steps"])
	assert.Equal(t, "note", m["notes"])
	assert.Equal(t, float64(3), m["prompt_number"])
	assert.Equal(t, float64(500), m["discovery_tokens"])
	assert.Equal(t, "2024-06-01T00:00:00Z", m["created_at"])
	assert.Equal(t, float64(1717200000000), m["created_at_epoch"])
}

func TestSessionSummary_MarshalJSON_InvalidFieldsOmitted(t *testing.T) {
	s := &SessionSummary{
		ID:           1,
		SDKSessionID: "sdk-1",
		Project:      "p",
		// All optional NullString/NullInt64 Invalid
		Request:      sql.NullString{Valid: false},
		Investigated: sql.NullString{Valid: false},
		Learned:      sql.NullString{Valid: false},
		Completed:    sql.NullString{Valid: false},
		NextSteps:    sql.NullString{Valid: false},
		Notes:        sql.NullString{Valid: false},
		PromptNumber: sql.NullInt64{Valid: false},
		CreatedAt:    "2024-06-01T00:00:00Z",
	}

	data, err := json.Marshal(s)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	// Required fields must remain.
	assert.Contains(t, m, "id")
	assert.Contains(t, m, "sdk_session_id")
	assert.Contains(t, m, "project")
	assert.Contains(t, m, "created_at")

	// Optional invalid fields must be absent.
	for _, key := range []string{"investigated", "learned", "completed", "next_steps", "notes", "prompt_number"} {
		_, present := m[key]
		assert.False(t, present, "field %q should be omitted when Invalid", key)
	}
}

func TestSessionSummary_MarshalJSON_MixedValidity(t *testing.T) {
	// Some valid, some not — only valid ones appear.
	s := &SessionSummary{
		ID:           5,
		SDKSessionID: "s",
		Project:      "p",
		Request:      sql.NullString{String: "r", Valid: true},
		Investigated: sql.NullString{Valid: false},
		Learned:      sql.NullString{String: "l", Valid: true},
		Completed:    sql.NullString{Valid: false},
		NextSteps:    sql.NullString{String: "n", Valid: true},
		Notes:        sql.NullString{Valid: false},
		PromptNumber: sql.NullInt64{Valid: false},
		CreatedAt:    "2024-01-01T00:00:00Z",
	}

	data, err := json.Marshal(s)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	assert.Equal(t, "r", m["request"])
	assert.Equal(t, "l", m["learned"])
	assert.Equal(t, "n", m["next_steps"])

	_, hasInvestigated := m["investigated"]
	assert.False(t, hasInvestigated)
	_, hasCompleted := m["completed"]
	assert.False(t, hasCompleted)
	_, hasNotes := m["notes"]
	assert.False(t, hasNotes)
	_, hasPN := m["prompt_number"]
	assert.False(t, hasPN)
}

// -----------------------------------------------------------------------------
// Contract: JSON round-trip — marshal SessionSummary → unmarshal into
// SessionSummaryJSON — field values must survive the round trip exactly.
// -----------------------------------------------------------------------------

func TestSessionSummary_JSONRoundTrip(t *testing.T) {
	original := &SessionSummary{
		ID:              99,
		SDKSessionID:    "sdk-round",
		Project:         "round-project",
		Request:         sql.NullString{String: "req-round", Valid: true},
		Investigated:    sql.NullString{String: "inv-round", Valid: true},
		Learned:         sql.NullString{String: "lrn-round", Valid: true},
		Completed:       sql.NullString{String: "cmp-round", Valid: true},
		NextSteps:       sql.NullString{String: "nxt-round", Valid: true},
		Notes:           sql.NullString{String: "note-round", Valid: true},
		PromptNumber:    sql.NullInt64{Int64: 12, Valid: true},
		DiscoveryTokens: 9999,
		CreatedAt:       "2024-01-01T00:00:00Z",
		CreatedAtEpoch:  1704067200000,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var result SessionSummaryJSON
	require.NoError(t, json.Unmarshal(data, &result))

	assert.Equal(t, original.ID, result.ID)
	assert.Equal(t, original.SDKSessionID, result.SDKSessionID)
	assert.Equal(t, original.Project, result.Project)
	assert.Equal(t, original.Request.String, result.Request)
	assert.Equal(t, original.Investigated.String, result.Investigated)
	assert.Equal(t, original.Learned.String, result.Learned)
	assert.Equal(t, original.Completed.String, result.Completed)
	assert.Equal(t, original.NextSteps.String, result.NextSteps)
	assert.Equal(t, original.Notes.String, result.Notes)
	assert.Equal(t, original.PromptNumber.Int64, result.PromptNumber)
	assert.Equal(t, original.DiscoveryTokens, result.DiscoveryTokens)
	assert.Equal(t, original.CreatedAt, result.CreatedAt)
	assert.Equal(t, original.CreatedAtEpoch, result.CreatedAtEpoch)
}

// -----------------------------------------------------------------------------
// Contract: ParsedSummary — plain value struct with six string fields.
// All fields are independent; zero value is the empty string.
// -----------------------------------------------------------------------------

func TestParsedSummary_FieldAssignment(t *testing.T) {
	p := &ParsedSummary{
		Request:      "R",
		Investigated: "I",
		Learned:      "L",
		Completed:    "C",
		NextSteps:    "N",
		Notes:        "No",
	}
	assert.Equal(t, "R", p.Request)
	assert.Equal(t, "I", p.Investigated)
	assert.Equal(t, "L", p.Learned)
	assert.Equal(t, "C", p.Completed)
	assert.Equal(t, "N", p.NextSteps)
	assert.Equal(t, "No", p.Notes)
}

func TestParsedSummary_ZeroValue(t *testing.T) {
	var p ParsedSummary
	assert.Equal(t, "", p.Request)
	assert.Equal(t, "", p.Investigated)
	assert.Equal(t, "", p.Learned)
	assert.Equal(t, "", p.Completed)
	assert.Equal(t, "", p.NextSteps)
	assert.Equal(t, "", p.Notes)
}

// -----------------------------------------------------------------------------
// Contract: SessionSummaryJSON — JSON-serializable mirror of SessionSummary.
// All fields are plain Go types (no sql.Null*). omitempty applies to optional
// string and int64 fields, so zero values drop from output.
// -----------------------------------------------------------------------------

func TestSessionSummaryJSON_DirectFields(t *testing.T) {
	j := SessionSummaryJSON{
		ID:              7,
		SDKSessionID:    "sdk-j",
		Project:         "proj-j",
		Request:         "req-j",
		Investigated:    "inv-j",
		Learned:         "lrn-j",
		Completed:       "cmp-j",
		NextSteps:       "nxt-j",
		Notes:           "note-j",
		PromptNumber:    4,
		DiscoveryTokens: 128,
		CreatedAt:       "2024-03-15T12:00:00Z",
		CreatedAtEpoch:  1710504000000,
	}

	assert.Equal(t, int64(7), j.ID)
	assert.Equal(t, "sdk-j", j.SDKSessionID)
	assert.Equal(t, "proj-j", j.Project)
	assert.Equal(t, "req-j", j.Request)
	assert.Equal(t, "inv-j", j.Investigated)
	assert.Equal(t, "lrn-j", j.Learned)
	assert.Equal(t, "cmp-j", j.Completed)
	assert.Equal(t, "nxt-j", j.NextSteps)
	assert.Equal(t, "note-j", j.Notes)
	assert.Equal(t, int64(4), j.PromptNumber)
	assert.Equal(t, int64(128), j.DiscoveryTokens)
	assert.Equal(t, "2024-03-15T12:00:00Z", j.CreatedAt)
	assert.Equal(t, int64(1710504000000), j.CreatedAtEpoch)
}

func TestSessionSummaryJSON_OmitemptyZeroOptionals(t *testing.T) {
	// Zero-value optional fields should be absent from JSON output due to omitempty.
	j := SessionSummaryJSON{
		ID:           1,
		SDKSessionID: "s",
		Project:      "p",
		CreatedAt:    "2024-01-01T00:00:00Z",
		// All omitempty fields left at zero values.
	}

	data, err := json.Marshal(j)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	for _, key := range []string{"request", "investigated", "learned", "completed", "next_steps", "notes", "prompt_number"} {
		_, present := m[key]
		assert.False(t, present, "zero-value omitempty field %q should be absent", key)
	}
	// discovery_tokens has no omitempty — zero value must be present.
	assert.Contains(t, m, "discovery_tokens")
}
