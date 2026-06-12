package sdk

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// truncate
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		maxLen   int
	}{
		{
			name:     "shorter_than_max",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "equal_to_max",
			input:    "hello",
			maxLen:   5,
			expected: "hello",
		},
		{
			name:     "longer_than_max",
			input:    "hello world",
			maxLen:   5,
			expected: "hello...",
		},
		{
			name:     "empty_string",
			input:    "",
			maxLen:   5,
			expected: "",
		},
		{
			name:     "zero_max_length",
			input:    "hello",
			maxLen:   0,
			expected: "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, truncate(tt.input, tt.maxLen))
		})
	}
}

// ---------------------------------------------------------------------------
// BuildObservationPrompt
// ---------------------------------------------------------------------------

func TestBuildObservationPrompt_BasicReadTool(t *testing.T) {
	now := time.Now().UnixMilli()
	exec := ToolExecution{
		ID:             1,
		ToolName:       "Read",
		ToolInput:      `{"file_path": "/path/to/file.go"}`,
		ToolOutput:     `package main\nfunc main() {}`,
		CreatedAtEpoch: now,
		CWD:            "/project",
	}

	result := BuildObservationPrompt(exec)

	assert.Contains(t, result, "<observed_from_primary_session>")
	assert.Contains(t, result, "<what_happened>Read</what_happened>")
	assert.Contains(t, result, "<working_directory>/project</working_directory>")
	assert.Contains(t, result, "<parameters>")
	assert.Contains(t, result, "file_path")
	assert.Contains(t, result, "<outcome>")
	assert.Contains(t, result, "</observed_from_primary_session>")
	assert.NotContains(t, result, "<user_intent>", "no user_intent when UserIntent is empty")
}

func TestBuildObservationPrompt_WithUserIntent(t *testing.T) {
	exec := ToolExecution{
		ID:             10,
		ToolName:       "Bash",
		ToolInput:      `{"command": "go test ./..."}`,
		ToolOutput:     "ok",
		CreatedAtEpoch: time.Now().UnixMilli(),
		CWD:            "/workspace",
		UserIntent:     "run all unit tests and verify coverage",
	}

	result := BuildObservationPrompt(exec)

	assert.Contains(t, result, "<user_intent>run all unit tests and verify coverage</user_intent>",
		"user_intent tag must appear when UserIntent is non-empty")
}

func TestBuildObservationPrompt_UserIntent_Truncated(t *testing.T) {
	exec := ToolExecution{
		ToolName:       "Read",
		ToolInput:      `{}`,
		ToolOutput:     `{}`,
		CreatedAtEpoch: time.Now().UnixMilli(),
		UserIntent:     strings.Repeat("x", 600), // exceeds 500-char limit
	}

	result := BuildObservationPrompt(exec)

	assert.Contains(t, result, "<user_intent>")
	assert.Contains(t, result, "...", "long UserIntent must be truncated with ellipsis")
}

func TestBuildObservationPrompt_NoWorkingDirectory(t *testing.T) {
	exec := ToolExecution{
		ToolName:       "Bash",
		ToolInput:      `{"command": "go test"}`,
		ToolOutput:     "ok",
		CreatedAtEpoch: time.Now().UnixMilli(),
		CWD:            "",
	}

	result := BuildObservationPrompt(exec)

	assert.Contains(t, result, "<what_happened>Bash</what_happened>")
	assert.NotContains(t, result, "<working_directory>", "working_directory must be omitted when CWD is empty")
}

func TestBuildObservationPrompt_EditTool(t *testing.T) {
	exec := ToolExecution{
		ToolName:       "Edit",
		ToolInput:      `{"file_path": "/file.go", "old_string": "foo", "new_string": "bar"}`,
		ToolOutput:     "Edit applied successfully",
		CreatedAtEpoch: time.Now().UnixMilli(),
	}

	result := BuildObservationPrompt(exec)

	assert.Contains(t, result, "<what_happened>Edit</what_happened>")
	assert.Contains(t, result, "file_path")
	assert.Contains(t, result, "old_string")
	assert.Contains(t, result, "new_string")
}

func TestBuildObservationPrompt_InvalidJSONInput(t *testing.T) {
	// When ToolInput is not valid JSON it must be treated as a raw string, not panic.
	exec := ToolExecution{
		ToolName:       "Write",
		ToolInput:      "not { json at all",
		ToolOutput:     "ok",
		CreatedAtEpoch: time.Now().UnixMilli(),
	}

	result := BuildObservationPrompt(exec)

	assert.Contains(t, result, "<observed_from_primary_session>")
	assert.Contains(t, result, "not { json at all", "raw input must be embedded verbatim when not JSON")
}

func TestBuildObservationPrompt_InvalidJSONOutput(t *testing.T) {
	exec := ToolExecution{
		ToolName:       "Bash",
		ToolInput:      `{"command": "echo hi"}`,
		ToolOutput:     "not json output \x00",
		CreatedAtEpoch: time.Now().UnixMilli(),
	}

	result := BuildObservationPrompt(exec)

	assert.Contains(t, result, "<outcome>")
}

func TestBuildObservationPrompt_TimestampIsRFC3339(t *testing.T) {
	epoch := int64(1700000000000) // a fixed known epoch
	exec := ToolExecution{
		ToolName:       "Read",
		ToolInput:      `{}`,
		ToolOutput:     `{}`,
		CreatedAtEpoch: epoch,
	}

	result := BuildObservationPrompt(exec)

	expected := time.UnixMilli(epoch).Format(time.RFC3339)
	assert.Contains(t, result, fmt.Sprintf("<occurred_at>%s</occurred_at>", expected))
}

func TestBuildObservationPrompt_TruncatesLongContent(t *testing.T) {
	longInput := strings.Repeat("x", 5000)
	longOutput := strings.Repeat("y", 7000)

	exec := ToolExecution{
		ToolName:       "Read",
		ToolInput:      longInput,
		ToolOutput:     longOutput,
		CreatedAtEpoch: time.Now().UnixMilli(),
		CWD:            "/project",
	}

	result := BuildObservationPrompt(exec)

	assert.Contains(t, result, "...", "long content must be truncated with ellipsis")
	assert.Less(t, len(result), 10_000, "result must not be excessively long")
}

// ---------------------------------------------------------------------------
// BuildSummaryPrompt
// ---------------------------------------------------------------------------

func TestBuildSummaryPrompt_BasicRequest(t *testing.T) {
	req := SummaryRequest{
		SessionDBID:  1,
		SDKSessionID: "sdk-123",
		Project:      "test-project",
	}

	result := BuildSummaryPrompt(req)

	for _, tag := range []string{"PROGRESS SUMMARY CHECKPOINT", "<summary>", "<request>", "<investigated>", "<learned>", "<completed>", "<next_steps>", "<notes>", "</summary>"} {
		assert.Contains(t, result, tag)
	}
}

func TestBuildSummaryPrompt_WithAssistantMessage(t *testing.T) {
	req := SummaryRequest{
		SessionDBID:          2,
		SDKSessionID:         "sdk-456",
		Project:              "project-b",
		LastAssistantMessage: "I fixed the authentication bug by updating the JWT validation.",
	}

	result := BuildSummaryPrompt(req)

	assert.Contains(t, result, "Claude's Full Response to User:")
	assert.Contains(t, result, "fixed the authentication")
}

func TestBuildSummaryPrompt_EmptyAssistantMessage(t *testing.T) {
	req := SummaryRequest{
		SessionDBID:          3,
		SDKSessionID:         "sdk-789",
		Project:              "project-c",
		LastAssistantMessage: "",
	}

	result := BuildSummaryPrompt(req)

	assert.Contains(t, result, "PROGRESS SUMMARY CHECKPOINT")
	assert.NotContains(t, result, "Claude's Full Response", "assistant message header must be omitted when message is empty")
}

func TestBuildSummaryPrompt_TruncatesLongAssistantMessage(t *testing.T) {
	req := SummaryRequest{
		SessionDBID:          1,
		SDKSessionID:         "sdk-123",
		Project:              "test",
		LastAssistantMessage: strings.Repeat("a", 5000),
	}

	result := BuildSummaryPrompt(req)

	assert.Contains(t, result, "...", "long assistant message must be truncated")
	assert.Less(t, len(result), 6000)
}

func TestBuildSummaryPrompt_ContainsXMLSchema(t *testing.T) {
	req := SummaryRequest{SessionDBID: 1}
	result := BuildSummaryPrompt(req)
	assert.Contains(t, result, "IMPORTANT! DO NOT do any work right now")
}

func TestBuildSummaryPrompt_IgnoresUserPromptAndLastUserMessage(t *testing.T) {
	// UserPrompt and LastUserMessage fields exist on SummaryRequest but
	// BuildSummaryPrompt does not include them in the prompt body — verify
	// they don't bleed through.
	req := SummaryRequest{
		SessionDBID:     10,
		UserPrompt:      "fix the cache bug",
		LastUserMessage: "please help with auth",
	}

	result := BuildSummaryPrompt(req)

	// The prompt template does not embed these fields.
	assert.NotContains(t, result, "fix the cache bug")
	assert.NotContains(t, result, "please help with auth")
}

// ---------------------------------------------------------------------------
// ToolExecution and SummaryRequest struct field coverage
// ---------------------------------------------------------------------------

func TestToolExecution_AllFields(t *testing.T) {
	exec := ToolExecution{
		ID:             42,
		ToolName:       "Write",
		ToolInput:      `{"file_path": "/test.go"}`,
		ToolOutput:     "File written",
		CreatedAtEpoch: 1234567890000,
		CWD:            "/workspace",
		UserIntent:     "create the config file",
	}

	require.Equal(t, int64(42), exec.ID)
	require.Equal(t, "Write", exec.ToolName)
	require.Equal(t, `{"file_path": "/test.go"}`, exec.ToolInput)
	require.Equal(t, "File written", exec.ToolOutput)
	require.Equal(t, int64(1234567890000), exec.CreatedAtEpoch)
	require.Equal(t, "/workspace", exec.CWD)
	require.Equal(t, "create the config file", exec.UserIntent)
}

func TestSummaryRequest_AllFields(t *testing.T) {
	req := SummaryRequest{
		SessionDBID:          100,
		SDKSessionID:         "sdk-abc",
		Project:              "my-project",
		UserPrompt:           "Fix the bug",
		LastUserMessage:      "Please fix the auth bug",
		LastAssistantMessage: "I've fixed the authentication issue",
	}

	require.Equal(t, int64(100), req.SessionDBID)
	require.Equal(t, "sdk-abc", req.SDKSessionID)
	require.Equal(t, "my-project", req.Project)
	require.Equal(t, "Fix the bug", req.UserPrompt)
	require.Equal(t, "Please fix the auth bug", req.LastUserMessage)
	require.Equal(t, "I've fixed the authentication issue", req.LastAssistantMessage)
}

// ---------------------------------------------------------------------------
// Exported constants
// ---------------------------------------------------------------------------

func TestObservationTypes_ContainsExpectedValues(t *testing.T) {
	expected := []string{"bugfix", "feature", "refactor", "change", "discovery", "decision"}
	assert.Equal(t, expected, ObservationTypes)
}

func TestObservationConcepts_ContainsExpectedValues(t *testing.T) {
	expected := []string{
		"how-it-works",
		"why-it-exists",
		"what-changed",
		"problem-solution",
		"gotcha",
		"pattern",
		"trade-off",
	}
	assert.Equal(t, expected, ObservationConcepts)
}

// ---------------------------------------------------------------------------
// DetectReasoning
// ---------------------------------------------------------------------------

func TestDetectReasoning_TooShort(t *testing.T) {
	assert.False(t, DetectReasoning("short text"), "text shorter than minReasoningTextLength must return false")
}

func TestDetectReasoning_LongButNoPatterns(t *testing.T) {
	text := strings.Repeat("hello world this is just filler content without any reasoning signals. ", 5)
	assert.False(t, DetectReasoning(text))
}

func TestDetectReasoning_FewerThanThreePatterns(t *testing.T) {
	// Only 2 pattern matches — must return false.
	base := strings.Repeat("padding sentence with no signal. ", 8)
	text := base + "because it is important. therefore we continue."
	assert.False(t, DetectReasoning(text))
}

func TestDetectReasoning_ExactlyThreePatterns(t *testing.T) {
	// 3 pattern matches — must return true.
	base := strings.Repeat("padding sentence with no signal. ", 8)
	text := base + "because it matters, therefore I concluded after analyzing the evidence."
	assert.True(t, DetectReasoning(text))
}

func TestDetectReasoning_MultiplePatterns(t *testing.T) {
	text := strings.Repeat(
		"because of this, therefore I investigated the root cause. "+
			"First I examined option a and option b. Weighed the pros and cons. "+
			"Verified the hypothesis and confirmed the conclusion. ", 2)
	assert.True(t, DetectReasoning(text))
}

func TestDetectReasoning_CaseInsensitive(t *testing.T) {
	base := strings.Repeat("filler words here for minimum length. ", 8)
	text := base + "BECAUSE of this, THEREFORE I concluded, AFTER ANALYZING the evidence."
	assert.True(t, DetectReasoning(text))
}
