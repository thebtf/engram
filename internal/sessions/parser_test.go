package sessions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSessionReader(t *testing.T) {
	t.Parallel()

	firstMsg, _ := parseTimestamp("2026-02-27T10:33:00.000Z")
	lastMsg, _ := parseTimestamp("2026-02-27T10:34:30.000Z")
	userTwoMsg, _ := parseTimestamp("2026-02-27T10:34:00.000Z")

	tests := []struct {
		name     string
		input    string
		expected SessionMeta
	}{
		{
			name: "two exchanges with tools",
			input: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Hi "},{"type":"text","text":"there"}]},"uuid":"u-1","timestamp":"2026-02-27T10:33:00.000Z","sessionId":"session-123","cwd":"/path/to/project","gitBranch":"main"}
` +
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Understood"},{"type":"tool_use","name":"Bash","input":{}},{"type":"tool_use","name":"Read","input":{}}]},"uuid":"a-1","timestamp":"2026-02-27T10:33:30.000Z","sessionId":"session-123","cwd":"/path/to/project","gitBranch":"main"}
` +
				`not-json-line
` +
				`{"type":"system","message":{"role":"system","content":[{"type":"text","text":"skip"}]},"uuid":"s-1","timestamp":"2026-02-27T10:33:40.000Z","sessionId":"session-123","cwd":"/path/to/project","gitBranch":"main"}
` +
				`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Next "},{"type":"text","text":"step"}]},"uuid":"u-2","timestamp":"2026-02-27T10:34:00.000Z","sessionId":"session-123","cwd":"/path/to/project","gitBranch":"main"}
` +
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done"},{"type":"tool_use","name":"Write","input":{}},{"type":"tool_use","name":"Write","input":{}},{"type":"tool_use","name":"Bash","input":{}}]},"uuid":"a-2","timestamp":"2026-02-27T10:34:30.000Z","sessionId":"session-123","cwd":"/path/to/project","gitBranch":"main"}`,
			expected: SessionMeta{
				SessionID:     "session-123",
				ProjectPath:   "/path/to/project",
				GitBranch:     "main",
				FirstMsgAt:    firstMsg,
				LastMsgAt:     lastMsg,
				ExchangeCount: 2,
				Exchanges: []Exchange{
					{
						UserText:      "Hi there",
						AssistantText: "Understood",
						ToolsUsed:     []string{"Bash", "Read"},
						Timestamp:     firstMsg,
					},
					{
						UserText:      "Next step",
						AssistantText: "Done",
						ToolsUsed:     []string{"Write", "Bash"},
						Timestamp:     userTwoMsg,
					},
				},
				ToolCounts: map[string]int{"Bash": 2, "Read": 1, "Write": 2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseSessionReader(strings.NewReader(tt.input))
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.expected.SessionID, result.SessionID)
			assert.Equal(t, tt.expected.ProjectPath, result.ProjectPath)
			assert.Equal(t, tt.expected.GitBranch, result.GitBranch)
			assert.Equal(t, tt.expected.FirstMsgAt, result.FirstMsgAt)
			assert.Equal(t, tt.expected.LastMsgAt, result.LastMsgAt)
			assert.Equal(t, tt.expected.ExchangeCount, result.ExchangeCount)
			assert.Equal(t, tt.expected.Exchanges, result.Exchanges)
			assert.Equal(t, tt.expected.ToolCounts, result.ToolCounts)
		})
	}
}

func TestParseSessionReaderEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty string", input: ""},
		{name: "whitespace", input: "\n\n\t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseSessionReader(strings.NewReader(tt.input))
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Empty(t, result.Exchanges)
			assert.Equal(t, 0, result.ExchangeCount)
			assert.Empty(t, result.ToolCounts)
		})
	}
}

func TestWorkstationID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "deterministic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := WorkstationID()
			second := WorkstationID()
			assert.Equal(t, 8, len(first))
			assert.Equal(t, 8, len(second))
			assert.Regexp(t, `^[0-9a-f]{8}$`, first)
			assert.Equal(t, first, second)
		})
	}
}

func TestProjectID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "same path stable", path: "/home/example/project"},
		{name: "different path", path: "/home/example/project-2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := ProjectID(tt.path)
			second := ProjectID(tt.path)
			assert.Equal(t, 8, len(first))
			assert.Equal(t, 8, len(second))
			assert.Regexp(t, `^[0-9a-f]{8}$`, first)
			assert.Equal(t, first, second)
		})
	}
}

func TestCompositeKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		workstation string
		project     string
		session     string
		expected    string
	}{
		{
			name:        "format",
			workstation: "abcd1234",
			project:     "efgh5678",
			session:     "session-1",
			expected:    "abcd1234:efgh5678:session-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := CompositeKey(tt.workstation, tt.project, tt.session)
			assert.Equal(t, tt.expected, actual)
		})
	}
}
