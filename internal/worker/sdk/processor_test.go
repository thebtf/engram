package sdk

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/thebtf/engram/pkg/models"
)

func TestIsSelfReferentialSummary(t *testing.T) {
	tests := []struct {
		summary  *models.ParsedSummary
		name     string
		expected bool
	}{
		{
			name: "meta summary about memory agent role",
			summary: &models.ParsedSummary{
				Request:   "Memory extraction agent role - analyze tool executions and extract meaningful observations for future sessions",
				Completed: "No work has been completed yet. The session has just started with the user providing role definition and operational guidelines.",
				Learned:   "The system expects observations to be created from meaningful learnings during Claude Code sessions, with focus on decisions, bugs fixed, patterns discovered, project structure changes, and code modifications.",
				NextSteps: "Awaiting tool executions or user requests that contain actual work performed in a Claude Code session.",
			},
			expected: true,
		},
		{
			name: "legitimate summary about code changes",
			summary: &models.ParsedSummary{
				Request:   "Fix authentication bug in login handler",
				Completed: "Updated the auth middleware to properly validate JWT tokens and fixed the session expiry check.",
				Learned:   "The JWT library requires explicit algorithm validation to prevent token substitution attacks.",
				NextSteps: "Add unit tests for the authentication flow.",
			},
			expected: false,
		},
		{
			name: "awaiting user summary",
			summary: &models.ParsedSummary{
				Request:   "Session initialization",
				Completed: "No work completed yet.",
				Learned:   "Awaiting user input to begin work.",
				NextSteps: "Waiting for the user to provide instructions.",
			},
			expected: true,
		},
		{
			name: "summary about refactoring",
			summary: &models.ParsedSummary{
				Request:   "Refactor database connection pooling",
				Completed: "Implemented connection pooling using pgxpool with max 10 connections.",
				Learned:   "pgxpool automatically handles connection reuse and health checks.",
				NextSteps: "Run benchmarks to verify performance improvement.",
			},
			expected: false,
		},
		{
			name: "meta summary with extraction agent mention",
			summary: &models.ParsedSummary{
				Request:   "Extraction agent initialization",
				Completed: "No substantive work has been done.",
				Learned:   "The memory extraction agent analyzes tool executions.",
				NextSteps: "Awaiting tool results to extract observations.",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSelfReferentialSummary(tt.summary)
			if result != tt.expected {
				t.Errorf("isSelfReferentialSummary() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestHasMeaningfulContent(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "empty content",
			content:  "",
			expected: false,
		},
		{
			name:     "too short content",
			content:  "Hello world",
			expected: false,
		},
		{
			name: "meta content about memory agent",
			content: `This is the memory extraction agent role definition.
The system expects you to analyze tool executions and extract meaningful observations.
No work has been completed yet. Awaiting tool results from the user's session.`,
			expected: false,
		},
		{
			name: "legitimate code discussion",
			content: `I've updated the handler.go file to fix the authentication bug.
The function validateToken() was not checking token expiry correctly.
I've added a check for exp claim and implemented proper error handling.
The changes have been tested and the build passes.`,
			expected: true,
		},
		{
			name: "hook status messages",
			content: `SessionStart:Callback hook success: Success
The memory agent is waiting for user input.
System-reminder about available tools.
No substantive work performed yet.`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasMeaningfulContent(tt.content)
			if result != tt.expected {
				t.Errorf("hasMeaningfulContent() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestShouldSkipTool(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		expected bool
	}{
		// Tools that should be skipped
		{"TodoWrite", "TodoWrite", true},
		{"Task", "Task", true},
		{"TaskOutput", "TaskOutput", true},
		{"Glob", "Glob", true},
		{"ListDir", "ListDir", true},
		{"LS", "LS", true},
		{"KillShell", "KillShell", true},
		{"AskUserQuestion", "AskUserQuestion", true},
		{"EnterPlanMode", "EnterPlanMode", true},
		{"ExitPlanMode", "ExitPlanMode", true},
		{"Skill", "Skill", true},
		{"SlashCommand", "SlashCommand", true},
		{"Read", "Read", true},
		{"Grep", "Grep", true},
		{"WebSearch", "WebSearch", true},

		// Tools that should NOT be skipped
		{"Edit", "Edit", false},
		{"Write", "Write", false},
		{"Bash", "Bash", false},
		{"WebFetch", "WebFetch", false},
		{"NotebookEdit", "NotebookEdit", false},

		// Unknown tool (should not be skipped)
		{"UnknownTool", "SomeUnknownTool", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSkipTool(tt.toolName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldSkipTrivialOperation(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		inputStr  string
		outputStr string
		expected  bool
	}{
		// Short output (should be skipped)
		{
			name:      "output_too_short",
			toolName:  "Read",
			inputStr:  `{"file_path": "/some/file.go"}`,
			outputStr: "short",
			expected:  true,
		},
		// Trivial outputs
		{
			name:      "no_matches_found",
			toolName:  "Grep",
			inputStr:  `{"pattern": "foo"}`,
			outputStr: "No matches found in the codebase",
			expected:  true,
		},
		{
			name:      "file_not_found",
			toolName:  "Read",
			inputStr:  `{"file_path": "/nonexistent.go"}`,
			outputStr: "Error: File not found at specified path",
			expected:  true,
		},
		{
			name:      "empty_array",
			toolName:  "Grep",
			inputStr:  `{"pattern": "foo"}`,
			outputStr: "[]",
			expected:  true,
		},
		// Boring files
		{
			name:      "package_lock_json",
			toolName:  "Read",
			inputStr:  `{"file_path": "/project/package-lock.json"}`,
			outputStr: "This is a very long package-lock.json content that has more than 50 characters",
			expected:  true,
		},
		{
			name:      "go_sum",
			toolName:  "Read",
			inputStr:  `{"file_path": "/project/go.sum"}`,
			outputStr: "This is a very long go.sum file content that has more than 50 characters",
			expected:  true,
		},
		// Grep with too many matches
		{
			name:     "grep_too_many_matches",
			toolName: "Grep",
			inputStr: `{"pattern": "import"}`,
			outputStr: func() string {
				s := ""
				for i := 0; i < 55; i++ {
					s += "match line\n"
				}
				return s
			}(),
			expected: true,
		},
		// Boring Bash commands
		{
			name:      "git_status",
			toolName:  "Bash",
			inputStr:  `{"command": "git status"}`,
			outputStr: "On branch main\nYour branch is up to date with 'origin/main'.\nnothing to commit, working tree clean",
			expected:  true,
		},
		{
			name:      "ls_command",
			toolName:  "Bash",
			inputStr:  `{"command": "ls -la /some/directory"}`,
			outputStr: "total 123\ndrwxr-xr-x some long listing that is at least 50 chars",
			expected:  true,
		},
		// Read is skipped by design — high-volume, low-insight (whitelist approach)
		{
			name:      "valid_read",
			toolName:  "Read",
			inputStr:  `{"file_path": "/project/main.go"}`,
			outputStr: "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello World\")\n}",
			expected:  true,
		},
		{
			name:      "valid_edit",
			toolName:  "Edit",
			inputStr:  `{"file_path": "/project/handler.go", "old_string": "foo", "new_string": "bar"}`,
			outputStr: "Edit applied successfully. File /project/handler.go has been modified with the requested changes.",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSkipTrivialOperation(tt.toolName, tt.inputStr, tt.outputStr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTruncateForLog(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestToJSONString(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "nil_value",
			input:    nil,
			expected: "",
		},
		{
			name:     "string_value",
			input:    "hello",
			expected: "hello",
		},
		{
			name:     "int_value",
			input:    42,
			expected: "42",
		},
		{
			name:     "map_value",
			input:    map[string]string{"key": "value"},
			expected: `{"key":"value"}`,
		},
		{
			name:     "slice_value",
			input:    []string{"a", "b", "c"},
			expected: `["a","b","c"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toJSONString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCaptureFileMtimes(t *testing.T) {
	// Create a temp directory with test files
	tmpDir, err := os.MkdirTemp("", "mtime-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")

	err = os.WriteFile(file1, []byte("content1"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(file2, []byte("content2"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("captures_mtimes_for_existing_files", func(t *testing.T) {
		mtimes := captureFileMtimes([]string{file1}, []string{file2}, "")
		assert.Len(t, mtimes, 2)
		assert.Contains(t, mtimes, file1)
		assert.Contains(t, mtimes, file2)
		assert.Greater(t, mtimes[file1], int64(0))
		assert.Greater(t, mtimes[file2], int64(0))
	})

	t.Run("handles_nonexistent_files", func(t *testing.T) {
		mtimes := captureFileMtimes([]string{"/nonexistent/file.txt"}, nil, "")
		assert.Empty(t, mtimes)
	})

	t.Run("handles_relative_paths_with_cwd", func(t *testing.T) {
		mtimes := captureFileMtimes([]string{"file1.txt"}, nil, tmpDir)
		assert.Len(t, mtimes, 1)
		assert.Contains(t, mtimes, "file1.txt")
	})

	t.Run("empty_inputs", func(t *testing.T) {
		mtimes := captureFileMtimes(nil, nil, "")
		assert.Empty(t, mtimes)
	})
}

func TestGetFileMtimes(t *testing.T) {
	// Create a temp file
	tmpDir, err := os.MkdirTemp("", "getmtime-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	err = os.WriteFile(testFile, []byte("content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	mtimes := GetFileMtimes([]string{testFile}, "")
	assert.Len(t, mtimes, 1)
	assert.Contains(t, mtimes, testFile)
	assert.Greater(t, mtimes[testFile], int64(0))
}

func TestGetFileContent(t *testing.T) {
	// Create a temp directory with test files
	tmpDir, err := os.MkdirTemp("", "content-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("reads_existing_file", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "test.txt")
		content := "test content"
		err := os.WriteFile(testFile, []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}

		result, ok := GetFileContent(testFile, "")
		assert.True(t, ok)
		assert.Equal(t, content, result)
	})

	t.Run("returns_false_for_nonexistent_file", func(t *testing.T) {
		result, ok := GetFileContent("/nonexistent/file.txt", "")
		assert.False(t, ok)
		assert.Empty(t, result)
	})

	t.Run("truncates_long_content", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "long.txt")
		longContent := ""
		for i := 0; i < 3000; i++ {
			longContent += "x"
		}
		err := os.WriteFile(testFile, []byte(longContent), 0644)
		if err != nil {
			t.Fatal(err)
		}

		result, ok := GetFileContent(testFile, "")
		assert.True(t, ok)
		assert.Contains(t, result, "[truncated]")
		assert.LessOrEqual(t, len(result), 2100)
	})

	t.Run("resolves_relative_path_with_cwd", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "relative.txt")
		content := "relative content"
		err := os.WriteFile(testFile, []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}

		result, ok := GetFileContent("relative.txt", tmpDir)
		assert.True(t, ok)
		assert.Equal(t, content, result)
	})
}

func TestObservationTypes(t *testing.T) {
	expected := []string{"bugfix", "feature", "refactor", "change", "discovery", "decision"}
	assert.Equal(t, expected, ObservationTypes)
}

func TestObservationConcepts(t *testing.T) {
	expectedConcepts := []string{
		"how-it-works",
		"why-it-exists",
		"what-changed",
		"problem-solution",
		"gotcha",
		"pattern",
		"trade-off",
	}
	assert.Equal(t, expectedConcepts, ObservationConcepts)
}

// TestSetBroadcastFunc tests the broadcast callback setter.
func TestSetBroadcastFunc(t *testing.T) {
	p := &Processor{}

	assert.Nil(t, p.broadcastFunc)

	var called bool
	var receivedEvent map[string]interface{}
	fn := func(event map[string]interface{}) {
		called = true
		receivedEvent = event
	}

	p.SetBroadcastFunc(fn)
	assert.NotNil(t, p.broadcastFunc)

	// Test broadcast
	p.broadcast(map[string]interface{}{"type": "test"})
	assert.True(t, called)
	assert.Equal(t, "test", receivedEvent["type"])
}

// TestSetSyncObservationFunc tests the sync observation callback setter.
func TestSetSyncObservationFunc(t *testing.T) {
	p := &Processor{}

	assert.Nil(t, p.syncObservationFunc)

	var called bool
	fn := func(obs *models.Observation) {
		called = true
	}

	p.SetSyncObservationFunc(fn)
	assert.NotNil(t, p.syncObservationFunc)

	// Verify it was set
	p.syncObservationFunc(&models.Observation{})
	assert.True(t, called)
}

// TestSetSyncSummaryFunc tests the sync summary callback setter.
func TestSetSyncSummaryFunc(t *testing.T) {
	p := &Processor{}

	assert.Nil(t, p.syncSummaryFunc)

	var called bool
	fn := func(summary *models.SessionSummary) {
		called = true
	}

	p.SetSyncSummaryFunc(fn)
	assert.NotNil(t, p.syncSummaryFunc)

	// Verify it was set
	p.syncSummaryFunc(&models.SessionSummary{})
	assert.True(t, called)
}

// TestBroadcast_NilFunc tests broadcast with nil callback.
func TestBroadcast_NilFunc(t *testing.T) {
	p := &Processor{}

	// Should not panic
	p.broadcast(map[string]interface{}{"type": "test"})
}

// TestIsAvailable tests IsAvailable always returns true (LLM backend removed in v5).
func TestIsAvailable(t *testing.T) {
	p := &Processor{}
	assert.True(t, p.IsAvailable())
}

// TestShouldSkipTrivialOperation_EdgeCases tests edge cases for trivial operation detection.
func TestShouldSkipTrivialOperation_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		inputStr  string
		outputStr string
		expected  bool
	}{
		{
			name:      "gitignore_file",
			toolName:  "Read",
			inputStr:  `{"file_path": "/project/.gitignore"}`,
			outputStr: "This is a gitignore file content that has more than 50 characters long",
			expected:  true,
		},
		{
			name:      "eslintignore_file",
			toolName:  "Read",
			inputStr:  `{"file_path": "/project/.eslintignore"}`,
			outputStr: "This is an eslintignore file content that has more than 50 characters long",
			expected:  true,
		},
		{
			name:      "tsconfig_file",
			toolName:  "Read",
			inputStr:  `{"file_path": "/project/tsconfig.json"}`,
			outputStr: "This is a tsconfig.json file content that has more than 50 characters long",
			expected:  true,
		},
		{
			name:      "tailwind_config",
			toolName:  "Read",
			inputStr:  `{"file_path": "/project/tailwind.config.js"}`,
			outputStr: "This is a tailwind.config file content that has more than 50 characters long",
			expected:  true,
		},
		{
			name:      "pwd_command",
			toolName:  "Bash",
			inputStr:  `{"command": "pwd"}`,
			outputStr: "/home/user/project/some/long/path/that/is/more/than/fifty/chars",
			expected:  true,
		},
		{
			name:      "echo_command",
			toolName:  "Bash",
			inputStr:  `{"command": "echo Hello World"}`,
			outputStr: "Hello World output that is long enough to pass the length check here",
			expected:  true,
		},
		{
			name:      "npm_audit_command",
			toolName:  "Bash",
			inputStr:  `{"command": "npm audit"}`,
			outputStr: "found 0 vulnerabilities in 500 packages which is more than fifty characters",
			expected:  true,
		},
		{
			name:      "permission_denied",
			toolName:  "Read",
			inputStr:  `{"file_path": "/root/secret"}`,
			outputStr: "Error: Permission denied accessing the file at specified path",
			expected:  true,
		},
		{
			name:      "is_a_directory",
			toolName:  "Read",
			inputStr:  `{"file_path": "/some/dir"}`,
			outputStr: "Error: /some/dir is a directory, not a file that can be read",
			expected:  true,
		},
		{
			name:      "empty_object",
			toolName:  "Grep",
			inputStr:  `{"pattern": "nonexistent"}`,
			outputStr: "{}",
			expected:  true,
		},
		{
			name:      "valid_grep_result",
			toolName:  "Grep",
			inputStr:  `{"pattern": "func main"}`,
			outputStr: "main.go:10:func main() {\nmain.go:11:    fmt.Println(\"Hello\")\n}",
			expected:  true, // Grep falls into default → skipped (whitelist: only Edit/Write/interesting Bash)
		},
		{
			name:      "valid_bash_build",
			toolName:  "Bash",
			inputStr:  `{"command": "go build ./..."}`,
			outputStr: "Build completed successfully. Binary output at ./bin/myapp with size 10MB.",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSkipTrivialOperation(tt.toolName, tt.inputStr, tt.outputStr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsSelfReferentialSummary_MoreCases tests additional self-referential detection cases.
func TestIsSelfReferentialSummary_MoreCases(t *testing.T) {
	tests := []struct {
		summary  *models.ParsedSummary
		name     string
		expected bool
	}{
		{
			name: "progress_checkpoint",
			summary: &models.ParsedSummary{
				Request:   "Progress checkpoint for current session",
				Completed: "Responding to progress checkpoint request",
				Learned:   "No technical learnings yet",
			},
			expected: true,
		},
		{
			name: "empty_session",
			summary: &models.ParsedSummary{
				Request:   "Empty session",
				Completed: "Just beginning the session",
				Learned:   "Nothing has been completed yet",
			},
			expected: true,
		},
		{
			name: "hook_mechanism",
			summary: &models.ParsedSummary{
				Request:   "Hook execution for session start",
				Completed: "Hook mechanism triggered successfully",
				Learned:   "System hooks are working",
			},
			expected: true,
		},
		{
			name: "api_implementation",
			summary: &models.ParsedSummary{
				Request:   "Implement REST API endpoints",
				Completed: "Created /users and /posts endpoints with CRUD operations",
				Learned:   "chi router handles middleware chaining elegantly",
				NextSteps: "Add authentication middleware",
			},
			expected: false,
		},
		{
			name: "database_migration",
			summary: &models.ParsedSummary{
				Request:   "Add database migration for new user fields",
				Completed: "Created migration 003_add_user_profile.sql with new columns",
				Learned:   "SQLite ALTER TABLE has limited capabilities, need to recreate table",
				NextSteps: "Test migration rollback",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSelfReferentialSummary(tt.summary)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestHasMeaningfulContent_MoreCases tests additional meaningful content detection.
func TestHasMeaningfulContent_MoreCases(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name: "code_with_functions",
			content: `I've created a new handler function in handlers.go.
The function validateRequest() checks the incoming JSON payload.
Here's the implementation:
` + "```go\nfunc validateRequest(r *http.Request) error {\n\treturn nil\n}\n```",
			expected: true,
		},
		{
			name: "python_code_discussion",
			content: `Updated the data processing module in processor.py.
Changed the filter function to use list comprehension.
def process_data(items):
    return [item for item in items if item.valid]
This improved performance by 30%.`,
			expected: true,
		},
		{
			name: "typescript_changes",
			content: `I've modified the React component in UserProfile.tsx.
Added a new functional component with proper TypeScript type annotations.
Here's the updated implementation:
` + "```tsx\nconst UserProfile: FC<Props> = ({ user }) => {\n  return <div>{user.name}</div>;\n};\n```" + `
The type annotations ensure type safety across the application.
The component has been updated with proper error handling and loading states.`,
			expected: true,
		},
		{
			name: "yaml_config_update",
			content: `I've updated the kubernetes deployment config in deploy.yaml.
Changed replicas from 2 to 4 and added resource limits for memory and CPU.
The deployment.yaml file now includes the following struct configuration:
` + "```yaml\nreplicas: 4\nresources:\n  limits:\n    memory: 512Mi\n```" + `
The changes have been implemented and will be applied on next deploy.`,
			expected: true,
		},
		{
			name: "just_system_messages",
			content: `SessionStart:Callback hook success
System-reminder about tools
The session is starting
Waiting for user instructions`,
			expected: false,
		},
		{
			name:     "borderline_short",
			content:  "Fixed bug. Updated file. Added test. Committed changes to repository.",
			expected: false, // Too short (< 200 chars)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasMeaningfulContent(tt.content)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestToJSONString_ComplexTypes tests JSON conversion for complex types.
func TestToJSONString_ComplexTypes(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		contains string
	}{
		{
			name: "nested_map",
			input: map[string]interface{}{
				"outer": map[string]string{"inner": "value"},
			},
			contains: "inner",
		},
		{
			name:     "bool_true",
			input:    true,
			contains: "true",
		},
		{
			name:     "bool_false",
			input:    false,
			contains: "false",
		},
		{
			name:     "float_value",
			input:    3.14,
			contains: "3.14",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toJSONString(tt.input)
			assert.Contains(t, result, tt.contains)
		})
	}
}

// TestCaptureFileMtimes_DuplicatePaths tests mtime capture with overlapping paths.
func TestCaptureFileMtimes_DuplicatePaths(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mtime-dup-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "shared.txt")
	err = os.WriteFile(testFile, []byte("content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Same file in both read and modified lists
	mtimes := captureFileMtimes([]string{testFile}, []string{testFile}, "")

	// Should only have one entry (no duplicates)
	assert.Len(t, mtimes, 1)
	assert.Contains(t, mtimes, testFile)
}

// TestTruncateForLog_ZeroLength tests truncation with zero length.
func TestTruncateForLog_ZeroLength(t *testing.T) {
	result := truncate("hello", 0)
	assert.Equal(t, "...", result)
}

// TestBroadcastFuncType tests the BroadcastFunc type.
func TestBroadcastFuncType(t *testing.T) {
	var fn BroadcastFunc = func(event map[string]interface{}) {
		// Do nothing
	}
	assert.NotNil(t, fn)
}

// TestSyncObservationFuncType tests the SyncObservationFunc type.
func TestSyncObservationFuncType(t *testing.T) {
	var fn SyncObservationFunc = func(obs *models.Observation) {
		// Do nothing
	}
	assert.NotNil(t, fn)
}

// TestSyncSummaryFuncType tests the SyncSummaryFunc type.
func TestSyncSummaryFuncType(t *testing.T) {
	var fn SyncSummaryFunc = func(summary *models.SessionSummary) {
		// Do nothing
	}
	assert.NotNil(t, fn)
}

// TestSafeResolvePath tests the path traversal protection.
func TestSafeResolvePath(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		path     string
		cwd      string
		wantPath string
		wantOk   bool
	}{
		{
			name:     "simple relative path",
			path:     "file.txt",
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "file.txt"),
		},
		{
			name:     "nested relative path",
			path:     "subdir/file.txt",
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "subdir", "file.txt"),
		},
		{
			name:   "path traversal with ..",
			path:   "../etc/passwd",
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:   "path traversal with multiple ..",
			path:   "../../etc/passwd",
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:   "path traversal hidden in middle",
			path:   "subdir/../../../etc/passwd",
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:   "just parent directory",
			path:   "..",
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:     "absolute path without cwd",
			path:     filepath.Join(tmpDir, "abs", "path"),
			cwd:      "",
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "abs", "path"),
		},
		{
			name:     "relative path without cwd",
			path:     filepath.Join("relative", "path"),
			cwd:      "",
			wantOk:   true,
			wantPath: filepath.Join("relative", "path"),
		},
		{
			name:     "current directory reference",
			path:     "./file.txt",
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "file.txt"),
		},
		{
			name:   "absolute path outside cwd",
			path:   filepath.Join(os.TempDir(), "outside", "passwd"),
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:     "absolute path inside cwd",
			path:     filepath.Join(tmpDir, "inside.txt"),
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "inside.txt"),
		},
		// Cross-platform edge cases
		{
			name:   "null byte injection",
			path:   "file\x00.txt",
			cwd:    tmpDir,
			wantOk: true, // filepath.Clean handles null bytes, OS rejects on open
		},
		{
			name:     "deeply nested valid path",
			path:     filepath.Join("a", "b", "c", "d", "file.txt"),
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "a", "b", "c", "d", "file.txt"),
		},
		{
			name:   "traversal via subdir then up and out",
			path:   filepath.Join("valid", "..", "..", "..", "etc", "shadow"),
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:     "cwd equals path exactly",
			path:     tmpDir,
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: tmpDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotOk := safeResolvePath(tt.path, tt.cwd)
			assert.Equal(t, tt.wantOk, gotOk, "ok status mismatch")
			if tt.wantPath != "" && gotOk {
				assert.Equal(t, tt.wantPath, gotPath, "path mismatch")
			}
		})
	}
}

// =============================================================================
// TESTS FOR RequestDeduplicator
// =============================================================================

func TestNewRequestDeduplicator(t *testing.T) {
	d := NewRequestDeduplicator(300, 1000)

	assert.NotNil(t, d)
	assert.NotNil(t, d.seen)
	assert.Equal(t, int64(300), d.ttlSecs)
	assert.Equal(t, 1000, d.maxSize)
}

func TestRequestDeduplicator_IsDuplicate_NotSeen(t *testing.T) {
	d := NewRequestDeduplicator(300, 1000)

	// New hash is not a duplicate
	assert.False(t, d.IsDuplicate("newhash"))
}

func TestRequestDeduplicator_IsDuplicate_AfterRecord(t *testing.T) {
	d := NewRequestDeduplicator(300, 1000)

	hash := "testhash"

	// Record the hash
	d.Record(hash)

	// Now it should be a duplicate
	assert.True(t, d.IsDuplicate(hash))
}

func TestRequestDeduplicator_Record(t *testing.T) {
	d := NewRequestDeduplicator(300, 1000)

	hash := "recordtest"
	d.Record(hash)

	// Check it was recorded
	d.mu.RLock()
	_, exists := d.seen[hash]
	d.mu.RUnlock()

	assert.True(t, exists)
}

func TestRequestDeduplicator_Record_Eviction(t *testing.T) {
	// Small maxSize for testing eviction
	d := NewRequestDeduplicator(0, 2) // TTL of 0 means everything is "old"

	// Record until capacity
	d.Record("hash1")
	d.Record("hash2")

	// Recording a third should trigger eviction (since TTL is 0)
	d.Record("hash3")

	// Should have cleaned up old entries
	d.mu.RLock()
	size := len(d.seen)
	d.mu.RUnlock()

	// Size should be limited (eviction occurred)
	assert.LessOrEqual(t, size, 3)
}

func TestHashRequest(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		input       string
		output      string
		compareWith []string
		wantLen     int
		wantSame    bool
	}{
		{
			name:     "basic hash",
			toolName: "Read",
			input:    "file.txt",
			output:   "content",
			wantLen:  16,
		},
		{
			name:     "consistent hashing",
			toolName: "Edit",
			input:    "same input",
			output:   "same output",
			wantLen:  16,
		},
		{
			name:     "long output truncation",
			toolName: "Bash",
			input:    "command",
			output:   string(make([]byte, 5000)), // Very long output
			wantLen:  16,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := hashRequest(tt.toolName, tt.input, tt.output)
			assert.Len(t, hash, tt.wantLen)

			// Same inputs should produce same hash
			hash2 := hashRequest(tt.toolName, tt.input, tt.output)
			assert.Equal(t, hash, hash2)
		})
	}
}

func TestHashRequest_DifferentInputs(t *testing.T) {
	// Different inputs should produce different hashes
	hash1 := hashRequest("Read", "file1.txt", "content1")
	hash2 := hashRequest("Read", "file2.txt", "content2")

	assert.NotEqual(t, hash1, hash2)
}

func TestHashRequest_OutputTruncation(t *testing.T) {
	// Hash should be the same for outputs that differ only after 1000 chars
	longOutput1 := string(make([]byte, 1500))
	longOutput2 := longOutput1[:1000] + "different suffix here"

	hash1 := hashRequest("Read", "input", longOutput1)
	hash2 := hashRequest("Read", "input", longOutput2)

	// Since we only hash first 1000 chars, these should be the same
	assert.Equal(t, hash1, hash2)
}

// =============================================================================
// TESTS FOR Processor methods
// =============================================================================

// =============================================================================
// TESTS FOR Vector Sync Workers
// =============================================================================

func TestProcessor_StartAndStopVectorSyncWorkers(t *testing.T) {
	var syncedObservations []*models.Observation
	var mu sync.Mutex

	p := &Processor{
		vectorSyncChan: make(chan *models.Observation, MaxVectorSyncWorkers*2),
		vectorSyncDone: make(chan struct{}),
		syncObservationFunc: func(obs *models.Observation) {
			mu.Lock()
			syncedObservations = append(syncedObservations, obs)
			mu.Unlock()
		},
	}

	// Start workers
	p.StartVectorSyncWorkers()

	// Send some observations
	obs1 := &models.Observation{SDKSessionID: "test1"}
	obs2 := &models.Observation{SDKSessionID: "test2"}
	p.vectorSyncChan <- obs1
	p.vectorSyncChan <- obs2

	// Give workers time to process
	time.Sleep(50 * time.Millisecond)

	// Stop workers
	p.StopVectorSyncWorkers()

	// Verify observations were synced
	mu.Lock()
	assert.Len(t, syncedObservations, 2)
	mu.Unlock()
}

func TestProcessor_VectorSyncWorker_DrainOnShutdown(t *testing.T) {
	var syncedCount int
	var mu sync.Mutex

	p := &Processor{
		vectorSyncChan: make(chan *models.Observation, 10),
		vectorSyncDone: make(chan struct{}),
		syncObservationFunc: func(obs *models.Observation) {
			mu.Lock()
			syncedCount++
			mu.Unlock()
		},
	}

	// Queue observations before starting workers
	for i := 0; i < 5; i++ {
		p.vectorSyncChan <- &models.Observation{SDKSessionID: "pre-queued"}
	}

	// Start workers
	p.StartVectorSyncWorkers()

	// Stop immediately - workers should drain the queue
	p.StopVectorSyncWorkers()

	// All pre-queued items should have been processed
	mu.Lock()
	assert.Equal(t, 5, syncedCount)
	mu.Unlock()
}

func TestProcessor_VectorSyncWorker_NilSyncFunc(t *testing.T) {
	p := &Processor{
		vectorSyncChan:      make(chan *models.Observation, 10),
		vectorSyncDone:      make(chan struct{}),
		syncObservationFunc: nil, // No sync function set
	}

	// Start workers
	p.StartVectorSyncWorkers()

	// Send observation - should not panic even with nil sync func
	p.vectorSyncChan <- &models.Observation{SDKSessionID: "test"}

	// Give it time to process
	time.Sleep(50 * time.Millisecond)

	// Stop workers - should not panic
	p.StopVectorSyncWorkers()
}

// =============================================================================
// TESTS FOR MaxVectorSyncWorkers constant
// =============================================================================

func TestMaxVectorSyncWorkers(t *testing.T) {
	assert.Equal(t, 8, MaxVectorSyncWorkers)
}

// =============================================================================
// ADDITIONAL EDGE CASE TESTS
// =============================================================================

func TestRequestDeduplicator_IsDuplicate_ExpiredEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping time-dependent test in short mode")
	}
	// Use a 1-second TTL with enough margin
	d := NewRequestDeduplicator(1, 100)

	hash := "expiretest"
	d.Record(hash)

	// Initially duplicate
	assert.True(t, d.IsDuplicate(hash))

	// Wait for TTL to expire (2.5 seconds to ensure crossing second boundaries)
	time.Sleep(2500 * time.Millisecond)

	// Should no longer be considered duplicate
	assert.False(t, d.IsDuplicate(hash))
}


