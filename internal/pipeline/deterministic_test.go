// Package pipeline provides tests for the deterministic observation extraction pipeline.
package pipeline

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

// --- ClassifyEvent ---

func TestClassifyEvent(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		toolInput  string
		toolResult string
		want       models.ObservationType
	}{
		// Edit / Write / NotebookEdit → change
		{
			name:     "Edit returns change",
			toolName: "Edit",
			want:     models.ObsTypeChange,
		},
		{
			name:     "Write returns change",
			toolName: "Write",
			want:     models.ObsTypeChange,
		},
		{
			name:     "NotebookEdit returns change",
			toolName: "NotebookEdit",
			want:     models.ObsTypeChange,
		},
		// Read / Grep / WebFetch / WebSearch → discovery
		{
			name:     "Read returns discovery",
			toolName: "Read",
			want:     models.ObsTypeDiscovery,
		},
		{
			name:     "Grep returns discovery",
			toolName: "Grep",
			want:     models.ObsTypeDiscovery,
		},
		{
			name:     "WebFetch returns discovery",
			toolName: "WebFetch",
			want:     models.ObsTypeDiscovery,
		},
		{
			name:     "WebSearch returns discovery",
			toolName: "WebSearch",
			want:     models.ObsTypeDiscovery,
		},
		// Bash with error in result → bugfix
		{
			name:       "Bash with error: in result",
			toolName:   "Bash",
			toolResult: "error: undefined variable",
			want:       models.ObsTypeBugfix,
		},
		{
			name:       "Bash with failed in result",
			toolName:   "Bash",
			toolResult: "compilation failed",
			want:       models.ObsTypeBugfix,
		},
		{
			name:       "Bash with exit code 1 in result",
			toolName:   "Bash",
			toolResult: "process exited with exit code 1",
			want:       models.ObsTypeBugfix,
		},
		{
			name:       "Bash with panic in result",
			toolName:   "Bash",
			toolResult: "panic: nil pointer dereference",
			want:       models.ObsTypeBugfix,
		},
		{
			name:       "Bash with fatal in result",
			toolName:   "Bash",
			toolResult: "fatal: repository not found",
			want:       models.ObsTypeBugfix,
		},
		{
			name:       "Bash with exception in result",
			toolName:   "Bash",
			toolResult: "NullPointerException at line 12",
			want:       models.ObsTypeBugfix,
		},
		{
			name:       "Bash with traceback in result",
			toolName:   "Bash",
			toolResult: "Traceback (most recent call last):",
			want:       models.ObsTypeBugfix,
		},
		{
			name:       "Bash with segfault in result",
			toolName:   "Bash",
			toolResult: "segfault at 0x0",
			want:       models.ObsTypeBugfix,
		},
		// Bash with test keywords in input → discovery
		{
			name:       "Bash with test keyword in input",
			toolName:   "Bash",
			toolInput:  `{"command": "go test ./..."}`,
			toolResult: "ok  github.com/thebtf/engram  0.123s",
			want:       models.ObsTypeDiscovery,
		},
		{
			name:       "Bash with spec keyword in input",
			toolName:   "Bash",
			toolInput:  `{"command": "rspec spec/models"}`,
			toolResult: "3 examples, 0 failures",
			want:       models.ObsTypeDiscovery,
		},
		{
			name:       "Bash with jest keyword in input",
			toolName:   "Bash",
			toolInput:  `{"command": "jest --coverage"}`,
			toolResult: "Tests: 10 passed, 10 total",
			want:       models.ObsTypeDiscovery,
		},
		{
			name:       "Bash with pytest keyword in input",
			toolName:   "Bash",
			toolInput:  `{"command": "pytest tests/"}`,
			toolResult: "collected 5 items\n5 passed",
			want:       models.ObsTypeDiscovery,
		},
		// Bash with architectural command → change
		{
			name:       "Bash with docker command",
			toolName:   "Bash",
			toolInput:  `{"command": "docker compose up -d"}`,
			toolResult: "Container started",
			want:       models.ObsTypeChange,
		},
		{
			name:       "Bash with migrate command",
			toolName:   "Bash",
			toolInput:  `{"command": "migrate up"}`,
			toolResult: "Migration applied",
			want:       models.ObsTypeChange,
		},
		// Generic Bash with no special indicators → change
		{
			name:       "Bash default returns change",
			toolName:   "Bash",
			toolInput:  `{"command": "make build"}`,
			toolResult: "Build successful",
			want:       models.ObsTypeChange,
		},
		// Unknown tool with decision keywords → decision
		{
			name:       "Unknown tool with architecture keyword",
			toolName:   "CustomTool",
			toolInput:  "We chose this architecture based on requirements",
			toolResult: "",
			want:       models.ObsTypeDecision,
		},
		{
			name:       "Unknown tool with design keyword in result",
			toolName:   "AnotherTool",
			toolInput:  "",
			toolResult: "The design decision was made to use Redis",
			want:       models.ObsTypeDecision,
		},
		{
			name:       "Unknown tool with tradeoff keyword",
			toolName:   "SomeTool",
			toolInput:  "tradeoff between speed and memory",
			toolResult: "",
			want:       models.ObsTypeDecision,
		},
		{
			name:       "Unknown tool with strategy keyword",
			toolName:   "SomeTool",
			toolInput:  "our strategy for caching",
			toolResult: "",
			want:       models.ObsTypeDecision,
		},
		// Unknown tool with refactor keywords → refactor
		{
			name:       "Unknown tool with refactor keyword",
			toolName:   "SomeTool",
			toolInput:  "refactor the user module",
			toolResult: "",
			want:       models.ObsTypeRefactor,
		},
		{
			name:       "Unknown tool with rename keyword",
			toolName:   "SomeTool",
			toolInput:  "rename the function for clarity",
			toolResult: "",
			want:       models.ObsTypeRefactor,
		},
		{
			name:       "Unknown tool with extract keyword",
			toolName:   "SomeTool",
			toolInput:  "extract helper utilities",
			toolResult: "",
			want:       models.ObsTypeRefactor,
		},
		{
			name:       "Unknown tool with consolidate keyword",
			toolName:   "SomeTool",
			toolInput:  "consolidate duplicate logic",
			toolResult: "",
			want:       models.ObsTypeRefactor,
		},
		{
			name:       "Unknown tool with cleanup keyword",
			toolName:   "SomeTool",
			toolInput:  "cleanup dead code",
			toolResult: "",
			want:       models.ObsTypeRefactor,
		},
		// Unknown tool with no special content → change (default)
		{
			name:       "Unknown tool default returns change",
			toolName:   "UnknownTool",
			toolInput:  "some random input",
			toolResult: "some random output",
			want:       models.ObsTypeChange,
		},
		// Empty inputs
		{
			name:     "Empty everything with Edit",
			toolName: "Edit",
			want:     models.ObsTypeChange,
		},
		{
			name:       "Empty everything with unknown tool",
			toolName:   "Mystery",
			toolInput:  "",
			toolResult: "",
			want:       models.ObsTypeChange,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyEvent(tc.toolName, tc.toolInput, tc.toolResult)
			if got != tc.want {
				t.Errorf("ClassifyEvent(%q, %q, %q) = %q, want %q",
					tc.toolName, tc.toolInput, tc.toolResult, got, tc.want)
			}
		})
	}
}

// --- GenerateTitle ---

func TestGenerateTitle(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		toolInput string
		want      string
	}{
		{
			name:      "Edit with file_path JSON",
			toolName:  "Edit",
			toolInput: `{"file_path": "/home/user/project/main.go", "old_string": "x", "new_string": "y"}`,
			want:      "Edit: main.go",
		},
		{
			name:      "Edit without file_path falls back",
			toolName:  "Edit",
			toolInput: `{"old_string": "x", "new_string": "y"}`,
			want:      "Edit: file",
		},
		{
			name:      "Edit empty input falls back",
			toolName:  "Edit",
			toolInput: "",
			want:      "Edit: file",
		},
		{
			name:      "Write with file_path JSON",
			toolName:  "Write",
			toolInput: `{"file_path": "/src/api/handler.go", "content": "..."}`,
			want:      "Write: handler.go",
		},
		{
			name:      "Write without file_path falls back",
			toolName:  "Write",
			toolInput: `{"content": "..."}`,
			want:      "Write: new file",
		},
		{
			name:      "Write empty input falls back",
			toolName:  "Write",
			toolInput: "",
			want:      "Write: new file",
		},
		{
			name:      "Bash with short command",
			toolName:  "Bash",
			toolInput: `{"command": "make build"}`,
			want:      "Bash: make build",
		},
		{
			name:      "Bash with long command truncated at 80 chars",
			toolName:  "Bash",
			toolInput: `{"command": "go test -v -count=1 -race -coverprofile=coverage.out ./internal/pipeline/... ./pkg/models/..."}`,
			// 93-char command gets truncated: cmd[:77] + "..."
			// The 77th character is a space, so result ends with "... ..."
			want: "Bash: go test -v -count=1 -race -coverprofile=coverage.out ./internal/pipeline/... ...",
		},
		{
			name:      "Bash without JSON command field falls back to first line",
			toolName:  "Bash",
			toolInput: "make install\nsome second line",
			want:      "Bash: make install",
		},
		{
			name:      "Bash empty input falls back",
			toolName:  "Bash",
			toolInput: "",
			want:      "Bash: command",
		},
		{
			name:      "Read with file_path JSON",
			toolName:  "Read",
			toolInput: `{"file_path": "/etc/config/settings.yaml"}`,
			want:      "Read: settings.yaml",
		},
		{
			name:      "Read without file_path falls back",
			toolName:  "Read",
			toolInput: `{}`,
			want:      "Read: file",
		},
		{
			name:      "Grep returns fixed title",
			toolName:  "Grep",
			toolInput: `{"pattern": "TODO"}`,
			want:      "Grep: search",
		},
		{
			name:      "WebFetch returns fixed title",
			toolName:  "WebFetch",
			toolInput: `{"url": "https://example.com"}`,
			want:      "WebFetch: external resource",
		},
		{
			name:      "WebSearch returns fixed title",
			toolName:  "WebSearch",
			toolInput: `{"query": "golang channels"}`,
			want:      "WebSearch: web query",
		},
		{
			name:      "NotebookEdit returns fixed title",
			toolName:  "NotebookEdit",
			toolInput: `{"notebook_path": "/work/analysis.ipynb"}`,
			want:      "NotebookEdit: cell",
		},
		{
			name:      "Unknown tool uses generic format",
			toolName:  "CustomTool",
			toolInput: "",
			want:      "CustomTool: operation",
		},
		{
			name:      "Bash command exactly 80 chars not truncated",
			toolName:  "Bash",
			toolInput: `{"command": "` + strings.Repeat("a", 80) + `"}`,
			want:      "Bash: " + strings.Repeat("a", 80),
		},
		{
			name:      "Bash command exactly 81 chars gets truncated",
			toolName:  "Bash",
			toolInput: `{"command": "` + strings.Repeat("b", 81) + `"}`,
			// 81 chars > 80, truncated to 77 + "..."
			want: "Bash: " + strings.Repeat("b", 77) + "...",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GenerateTitle(tc.toolName, tc.toolInput)
			if got != tc.want {
				t.Errorf("GenerateTitle(%q, %q) = %q, want %q",
					tc.toolName, tc.toolInput, got, tc.want)
			}
		})
	}
}

// --- ExtractConcepts ---

func TestExtractConcepts(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		toolInput   string
		toolResult  string
		wantContain []string // all must be present
		wantAbsent  []string // none may be present
	}{
		{
			name:        "auth keyword maps to authentication",
			toolName:    "Edit",
			toolInput:   "auth middleware",
			toolResult:  "",
			wantContain: []string{"authentication"},
		},
		{
			name:        "login keyword maps to authentication",
			toolName:    "Edit",
			toolInput:   "login handler",
			toolResult:  "",
			wantContain: []string{"authentication"},
		},
		{
			name:        "authentication keyword maps to authentication (no duplicate)",
			toolName:    "Edit",
			toolInput:   "authentication login auth",
			toolResult:  "",
			wantContain: []string{"authentication"},
		},
		{
			name:        "db keyword maps to database",
			toolName:    "Bash",
			toolInput:   "db migration",
			toolResult:  "",
			wantContain: []string{"database"},
		},
		{
			name:        "schema keyword maps to database",
			toolName:    "Edit",
			toolInput:   "schema update",
			toolResult:  "",
			wantContain: []string{"database"},
		},
		{
			name:        "api keyword maps to api",
			toolName:    "Edit",
			toolInput:   "api endpoint handler",
			toolResult:  "",
			wantContain: []string{"api"},
		},
		{
			name:        "endpoint keyword maps to api",
			toolName:    "Edit",
			toolInput:   "new endpoint",
			toolResult:  "",
			wantContain: []string{"api"},
		},
		{
			name:        "test keyword maps to testing",
			toolName:    "Bash",
			toolInput:   "go test ./...",
			toolResult:  "ok  0.1s",
			wantContain: []string{"testing"},
		},
		{
			name:        "coverage keyword maps to testing",
			toolName:    "Bash",
			toolInput:   "coverage report",
			toolResult:  "",
			wantContain: []string{"testing"},
		},
		{
			name:        "docker keyword maps to docker",
			toolName:    "Bash",
			toolInput:   "docker compose",
			toolResult:  "",
			wantContain: []string{"docker"},
		},
		{
			name:        "container keyword maps to docker",
			toolName:    "Edit",
			toolInput:   "container config",
			toolResult:  "",
			wantContain: []string{"docker"},
		},
		{
			name:        "security keyword maps to security",
			toolName:    "Edit",
			toolInput:   "security check",
			toolResult:  "",
			wantContain: []string{"security"},
		},
		{
			name:        "encrypt keyword maps to security",
			toolName:    "Edit",
			toolInput:   "encrypt passwords",
			toolResult:  "",
			wantContain: []string{"security"},
		},
		{
			name:        "token keyword maps to security",
			toolName:    "Edit",
			toolInput:   "token validation",
			toolResult:  "",
			wantContain: []string{"security"},
		},
		{
			name:        "performance keyword maps to performance",
			toolName:    "Edit",
			toolInput:   "performance improvement",
			toolResult:  "",
			wantContain: []string{"performance"},
		},
		{
			name:        "cache keyword maps to performance",
			toolName:    "Edit",
			toolInput:   "cache invalidation",
			toolResult:  "",
			wantContain: []string{"performance"},
		},
		{
			name:        "optimize keyword maps to performance",
			toolName:    "Bash",
			toolInput:   "optimize query",
			toolResult:  "",
			wantContain: []string{"performance"},
		},
		{
			name:        "debug keyword maps to debugging",
			toolName:    "Bash",
			toolInput:   "debug mode",
			toolResult:  "",
			wantContain: []string{"debugging"},
		},
		{
			name:        "error keyword maps to debugging",
			toolName:    "Bash",
			toolInput:   "",
			toolResult:  "error occurred",
			wantContain: []string{"debugging"},
		},
		{
			name:        "fix keyword maps to debugging",
			toolName:    "Edit",
			toolInput:   "fix the bug",
			toolResult:  "",
			wantContain: []string{"debugging"},
		},
		{
			name:        "config keyword maps to configuration",
			toolName:    "Edit",
			toolInput:   "config file update",
			toolResult:  "",
			wantContain: []string{"configuration"},
		},
		{
			name:        "setting keyword maps to configuration",
			toolName:    "Read",
			toolInput:   "setting values",
			toolResult:  "",
			wantContain: []string{"configuration"},
		},
		{
			name:        "deploy keyword maps to deployment",
			toolName:    "Bash",
			toolInput:   "deploy to production",
			toolResult:  "",
			wantContain: []string{"deployment"},
		},
		{
			name:        "ci keyword maps to ci-cd",
			toolName:    "Bash",
			toolInput:   "ci pipeline",
			toolResult:  "",
			wantContain: []string{"ci-cd"},
		},
		{
			name:        "pipeline keyword maps to ci-cd",
			toolName:    "Edit",
			toolInput:   "pipeline config",
			toolResult:  "",
			wantContain: []string{"ci-cd"},
		},
		{
			name:        "refactor keyword maps to refactoring",
			toolName:    "Edit",
			toolInput:   "refactor user service",
			toolResult:  "",
			wantContain: []string{"refactoring"},
		},
		{
			name:        "architecture keyword maps to architecture",
			toolName:    "Edit",
			toolInput:   "architecture diagram",
			toolResult:  "",
			wantContain: []string{"architecture"},
		},
		{
			name:        "design keyword maps to architecture",
			toolName:    "Edit",
			toolInput:   "design pattern",
			toolResult:  "",
			wantContain: []string{"architecture"},
		},
		{
			name:        "pattern keyword maps to pattern",
			toolName:    "Edit",
			toolInput:   "pattern matching",
			toolResult:  "",
			wantContain: []string{"pattern"},
		},
		{
			name:        "file path extracts relevant directory parts",
			toolName:    "Edit",
			toolInput:   `{"file_path": "/src/internal/handlers/auth/login.go"}`,
			toolResult:  "",
			wantContain: []string{"handlers", "auth"},
			// "src" and "internal" are in the irrelevant blocklist so they should be absent
			wantAbsent: []string{"src", "internal", "vendor", "lib", "pkg", "cmd"},
		},
		{
			name:        "no keywords returns empty",
			toolName:    "Bash",
			toolInput:   "make build",
			toolResult:  "build done",
			wantContain: []string{},
		},
		{
			name:        "empty inputs return empty",
			toolName:    "Read",
			toolInput:   "",
			toolResult:  "",
			wantContain: []string{},
		},
		{
			name:        "keyword case insensitive match",
			toolName:    "Edit",
			toolInput:   "AUTH middleware",
			toolResult:  "",
			wantContain: []string{"authentication"},
		},
		{
			name:        "keyword in result also matched",
			toolName:    "Bash",
			toolInput:   "",
			toolResult:  "authentication required",
			wantContain: []string{"authentication"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractConcepts(tc.toolName, tc.toolInput, tc.toolResult)

			// Build a set for O(1) lookup
			gotSet := make(map[string]bool, len(got))
			for _, c := range got {
				gotSet[c] = true
			}

			for _, want := range tc.wantContain {
				if !gotSet[want] {
					t.Errorf("ExtractConcepts missing concept %q; got %v", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if gotSet[absent] {
					t.Errorf("ExtractConcepts should not contain %q; got %v", absent, got)
				}
			}
		})
	}
}

func TestExtractConceptsDeduplication(t *testing.T) {
	// "auth", "authentication", and "login" all map to the same "authentication" concept.
	// It should appear only once.
	got := ExtractConcepts("Edit", "auth authentication login", "")
	count := 0
	for _, c := range got {
		if c == "authentication" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 'authentication' exactly once, got %d times in %v", count, got)
	}
}

// --- ExtractFilePaths ---

func TestExtractFilePaths(t *testing.T) {
	tests := []struct {
		name        string
		toolInput   string
		toolResult  string
		wantContain []string
		wantLen     int // -1 = don't check exact length
	}{
		{
			name:        "JSON file_path extraction",
			toolInput:   `{"file_path": "/home/user/project/main.go"}`,
			toolResult:  "",
			wantContain: []string{"/home/user/project/main.go"},
			wantLen:     -1,
		},
		{
			name:        "multiple JSON file_path fields",
			toolInput:   `{"file_path": "/src/a.go"} {"file_path": "/src/b.go"}`,
			toolResult:  "",
			wantContain: []string{"/src/a.go", "/src/b.go"},
			wantLen:     -1,
		},
		{
			name:        "standalone Unix path in result",
			toolInput:   "",
			toolResult:  "see /internal/api/handler.go for details",
			wantContain: []string{"/internal/api/handler.go"},
			wantLen:     -1,
		},
		{
			name:        "standalone Windows path in input",
			toolInput:   `C:\Users\dev\project\main.go updated`,
			toolResult:  "",
			wantContain: []string{`C:\Users\dev\project\main.go`},
			wantLen:     -1,
		},
		{
			name:        "no paths returns empty slice",
			toolInput:   "no paths here",
			toolResult:  "none here either",
			wantContain: []string{},
			wantLen:     0,
		},
		{
			name:        "empty inputs return empty slice",
			toolInput:   "",
			toolResult:  "",
			wantContain: []string{},
			wantLen:     0,
		},
		{
			name:        "deduplication: same path in input and result",
			toolInput:   `{"file_path": "/path/to/file.go"}`,
			toolResult:  " /path/to/file.go",
			wantContain: []string{"/path/to/file.go"},
			// JSON extraction gets it; standalone might re-find but dedup should prevent duplicate
			wantLen: -1,
		},
		{
			name:        "unix path without extension not extracted",
			toolInput:   "",
			toolResult:  "/usr/bin/bash more text",
			wantContain: []string{},
			// /usr/bin/bash has no extension, looksLikeFilePath = false
			wantLen: 0,
		},
		{
			name:        "short path with extension extracted",
			toolInput:   "",
			toolResult:  " /a/b.go",
			wantContain: []string{"/a/b.go"},
			wantLen:     -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractFilePaths(tc.toolInput, tc.toolResult)

			gotSet := make(map[string]bool, len(got))
			for _, p := range got {
				gotSet[p] = true
			}

			for _, want := range tc.wantContain {
				if !gotSet[want] {
					t.Errorf("ExtractFilePaths missing path %q; got %v", want, got)
				}
			}

			if tc.wantLen >= 0 && len(got) != tc.wantLen {
				t.Errorf("ExtractFilePaths len = %d, want %d; paths: %v", len(got), tc.wantLen, got)
			}
		})
	}
}

// --- ExtractFacts ---

func TestExtractFacts(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		toolInput   string
		toolResult  string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "Edit with file_path emits Modified file fact",
			toolName:    "Edit",
			toolInput:   `{"file_path": "/src/api/server.go"}`,
			toolResult:  "",
			wantContain: []string{"Modified file: server.go"},
		},
		{
			name:        "Edit without file_path emits no facts",
			toolName:    "Edit",
			toolInput:   `{}`,
			toolResult:  "",
			wantContain: []string{},
		},
		{
			name:        "Write with file_path emits Created file fact",
			toolName:    "Write",
			toolInput:   `{"file_path": "/src/newfile.go", "content": "..."}`,
			toolResult:  "",
			wantContain: []string{"Created file: newfile.go"},
		},
		{
			name:        "Write without file_path emits no facts",
			toolName:    "Write",
			toolInput:   `{}`,
			toolResult:  "",
			wantContain: []string{},
		},
		{
			name:        "Bash emits Executed fact",
			toolName:    "Bash",
			toolInput:   `{"command": "make build"}`,
			toolResult:  "Build successful",
			wantContain: []string{"Executed: make build"},
		},
		{
			name:        "Bash with exit code in result emits extra fact",
			toolName:    "Bash",
			toolInput:   `{"command": "go build ./..."}`,
			toolResult:  "process terminated with exit code 1",
			wantContain: []string{"Executed: go build ./...", "Command completed with non-zero exit code"},
		},
		{
			name:        "Bash without exit code in result does not emit exit code fact",
			toolName:    "Bash",
			toolInput:   `{"command": "echo hello"}`,
			toolResult:  "hello",
			wantContain: []string{"Executed: echo hello"},
			wantAbsent:  []string{"Command completed with non-zero exit code"},
		},
		{
			name:        "Bash long command is truncated in fact",
			toolName:    "Bash",
			toolInput:   `{"command": "` + strings.Repeat("x", 150) + `"}`,
			toolResult:  "",
			wantContain: []string{"Executed: " + strings.Repeat("x", 117) + "..."},
		},
		{
			name:        "Read emits no facts",
			toolName:    "Read",
			toolInput:   `{"file_path": "/src/main.go"}`,
			toolResult:  "package main",
			wantContain: []string{},
		},
		{
			name:        "Unknown tool emits no facts",
			toolName:    "UnknownTool",
			toolInput:   "anything",
			toolResult:  "anything",
			wantContain: []string{},
		},
		{
			name:        "empty inputs emit no facts for Edit",
			toolName:    "Edit",
			toolInput:   "",
			toolResult:  "",
			wantContain: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractFacts(tc.toolName, tc.toolInput, tc.toolResult)

			gotSet := make(map[string]bool, len(got))
			for _, f := range got {
				gotSet[f] = true
			}

			for _, want := range tc.wantContain {
				if !gotSet[want] {
					t.Errorf("ExtractFacts missing fact %q; got %v", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if gotSet[absent] {
					t.Errorf("ExtractFacts should not contain %q; got %v", absent, got)
				}
			}
		})
	}
}

// --- FormatEmbeddingText ---

func TestFormatEmbeddingText(t *testing.T) {
	t.Run("full observation produces all sections", func(t *testing.T) {
		obs := &models.Observation{
			Type:          models.ObsTypeChange,
			Title:         sql.NullString{String: "Edit: main.go", Valid: true},
			FilesModified: models.JSONStringArray{"main.go", "server.go"},
			Concepts:      models.JSONStringArray{"api", "testing"},
			Facts:         models.JSONStringArray{"Modified file: main.go", "Executed: go build"},
		}
		got := FormatEmbeddingText(obs)

		for _, want := range []string{"change", "Edit: main.go", "Files: main.go, server.go", "Concepts: api, testing", "Modified file: main.go", "Executed: go build"} {
			if !strings.Contains(got, want) {
				t.Errorf("FormatEmbeddingText missing %q in output:\n%s", want, got)
			}
		}
	})

	t.Run("observation without optional fields", func(t *testing.T) {
		obs := &models.Observation{
			Type: models.ObsTypeDiscovery,
		}
		got := FormatEmbeddingText(obs)
		if got != "discovery" {
			t.Errorf("FormatEmbeddingText = %q, want %q", got, "discovery")
		}
	})

	t.Run("empty observation returns empty string", func(t *testing.T) {
		obs := &models.Observation{}
		got := FormatEmbeddingText(obs)
		if got != "" {
			t.Errorf("FormatEmbeddingText with empty obs = %q, want empty", got)
		}
	})

	t.Run("invalid NullString title is excluded", func(t *testing.T) {
		obs := &models.Observation{
			Type:  models.ObsTypeChange,
			Title: sql.NullString{String: "should not appear", Valid: false},
		}
		got := FormatEmbeddingText(obs)
		if strings.Contains(got, "should not appear") {
			t.Errorf("FormatEmbeddingText included invalid NullString title: %q", got)
		}
	})

	t.Run("valid but empty NullString title is excluded", func(t *testing.T) {
		obs := &models.Observation{
			Type:  models.ObsTypeChange,
			Title: sql.NullString{String: "", Valid: true},
		}
		got := FormatEmbeddingText(obs)
		// Should only have the type
		if got != "change" {
			t.Errorf("FormatEmbeddingText = %q, want %q", got, "change")
		}
	})

	t.Run("text truncated at 1500 chars", func(t *testing.T) {
		bigFact := strings.Repeat("a", 2000)
		obs := &models.Observation{
			Type:  models.ObsTypeChange,
			Facts: models.JSONStringArray{bigFact},
		}
		got := FormatEmbeddingText(obs)
		if len(got) > 1500 {
			t.Errorf("FormatEmbeddingText len = %d, want <= 1500", len(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("FormatEmbeddingText truncated text should end with '...', got: %q", got[max(0, len(got)-10):])
		}
	})

	t.Run("text at exactly 1500 chars is not truncated", func(t *testing.T) {
		// Build text that totals exactly 1500 chars.
		// "change\n" = 7 chars; we need 1493 more chars in a fact.
		obs := &models.Observation{
			Type:  models.ObsTypeChange,
			Facts: models.JSONStringArray{strings.Repeat("b", 1493)},
		}
		got := FormatEmbeddingText(obs)
		if len(got) != 1500 {
			t.Errorf("FormatEmbeddingText len = %d, want 1500", len(got))
		}
		if strings.HasSuffix(got, "...") {
			t.Errorf("FormatEmbeddingText should not truncate text at exactly 1500 chars")
		}
	})

	t.Run("multiple facts all included when under limit", func(t *testing.T) {
		obs := &models.Observation{
			Type:  models.ObsTypeChange,
			Facts: models.JSONStringArray{"fact one", "fact two", "fact three"},
		}
		got := FormatEmbeddingText(obs)
		for _, f := range []string{"fact one", "fact two", "fact three"} {
			if !strings.Contains(got, f) {
				t.Errorf("FormatEmbeddingText missing fact %q in:\n%s", f, got)
			}
		}
	})
}

// max is a helper for Go < 1.21 compat (1.21+ has built-in max).
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// --- ShouldSkipTool ---

func TestShouldSkipTool(t *testing.T) {
	tests := []struct {
		toolName string
		want     bool
	}{
		// Tools that should be skipped
		{"TodoWrite", true},
		{"Task", true},
		{"TaskOutput", true},
		{"TaskCreate", true},
		{"TaskUpdate", true},
		{"TaskList", true},
		{"TaskGet", true},
		{"Glob", true},
		{"ListDir", true},
		{"LS", true},
		{"KillShell", true},
		{"TaskStop", true},
		{"AskUserQuestion", true},
		{"EnterPlanMode", true},
		{"ExitPlanMode", true},
		{"Skill", true},
		{"SlashCommand", true},
		{"EnterWorktree", true},
		{"ToolSearch", true},
		// Tools that should NOT be skipped
		{"Edit", false},
		{"Write", false},
		{"Read", false},
		{"Bash", false},
		{"Grep", false},
		{"WebFetch", false},
		{"WebSearch", false},
		{"NotebookEdit", false},
		// Unknown tools should not be skipped
		{"UnknownTool", false},
		{"", false},
		{"todowrite", false}, // case sensitive
		{"glob", false},      // case sensitive
	}

	for _, tc := range tests {
		t.Run(tc.toolName, func(t *testing.T) {
			got := ShouldSkipTool(tc.toolName)
			if got != tc.want {
				t.Errorf("ShouldSkipTool(%q) = %v, want %v", tc.toolName, got, tc.want)
			}
		})
	}
}

// --- ShouldSkipTrivial ---

func TestShouldSkipTrivial(t *testing.T) {
	// A result long enough to not be skipped by the length check alone.
	longResult := strings.Repeat("x", 60)

	tests := []struct {
		name       string
		toolName   string
		toolInput  string
		toolResult string
		want       bool
	}{
		// Short result (< 50 chars) → skip
		{
			name:       "short result skipped",
			toolName:   "Bash",
			toolInput:  "anything",
			toolResult: "short",
			want:       true,
		},
		{
			name:       "result exactly 49 chars skipped",
			toolName:   "Bash",
			toolInput:  "anything",
			toolResult: strings.Repeat("y", 49),
			want:       true,
		},
		{
			name:       "result exactly 50 chars not skipped by length",
			toolName:   "Edit",
			toolInput:  "input",
			toolResult: strings.Repeat("z", 50),
			want:       false,
		},
		// Trivial output patterns → skip
		{
			name:       "no matches found skipped",
			toolName:   "Grep",
			toolInput:  "pattern",
			toolResult: "no matches found in any files",
			want:       true,
		},
		{
			name:       "file not found skipped",
			toolName:   "Read",
			toolInput:  "file.go",
			toolResult: "file not found: no such file or directory",
			want:       true,
		},
		{
			name:       "directory not found skipped",
			toolName:   "Read",
			toolInput:  "/path",
			toolResult: "directory not found in the filesystem check",
			want:       true,
		},
		{
			name:       "permission denied skipped",
			toolName:   "Bash",
			toolInput:  "cat /etc/shadow",
			toolResult: "permission denied opening the requested file",
			want:       true,
		},
		{
			name:       "command not found skipped",
			toolName:   "Bash",
			toolInput:  "foobar",
			toolResult: "command not found: foobar is not recognized",
			want:       true,
		},
		{
			name:       "no such file skipped",
			toolName:   "Bash",
			toolInput:  "cat missing.txt",
			toolResult: "no such file or directory found here",
			want:       true,
		},
		{
			name:       "is a directory skipped",
			toolName:   "Read",
			toolInput:  "/tmp",
			toolResult: "read /tmp: is a directory not a file",
			want:       true,
		},
		{
			name:       "empty JSON array skipped",
			toolName:   "Bash",
			toolInput:  "list",
			toolResult: "[]" + strings.Repeat(" ", 48),
			want:       true,
		},
		{
			name:       "empty JSON object skipped",
			toolName:   "Bash",
			toolInput:  "get",
			toolResult: "{}" + strings.Repeat(" ", 48),
			want:       true,
		},
		// Read boring files → skip
		{
			name:       "package-lock.json skipped",
			toolName:   "Read",
			toolInput:  "/project/package-lock.json",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "yarn.lock skipped",
			toolName:   "Read",
			toolInput:  "/project/yarn.lock",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "pnpm-lock.yaml skipped",
			toolName:   "Read",
			toolInput:  "/project/pnpm-lock.yaml",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "go.sum skipped",
			toolName:   "Read",
			toolInput:  "/project/go.sum",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "cargo.lock skipped",
			toolName:   "Read",
			toolInput:  "/project/cargo.lock",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "gemfile.lock skipped",
			toolName:   "Read",
			toolInput:  "/project/Gemfile.lock",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "poetry.lock skipped",
			toolName:   "Read",
			toolInput:  "/project/poetry.lock",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       ".gitignore skipped",
			toolName:   "Read",
			toolInput:  "/project/.gitignore",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       ".dockerignore skipped",
			toolName:   "Read",
			toolInput:  "/project/.dockerignore",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       ".eslintignore skipped",
			toolName:   "Read",
			toolInput:  "/project/.eslintignore",
			toolResult: longResult,
			want:       true,
		},
		// Bash boring commands → skip
		{
			name:       "git status command skipped",
			toolName:   "Bash",
			toolInput:  "git status",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "git diff command skipped",
			toolName:   "Bash",
			toolInput:  "git diff HEAD",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "git log command skipped",
			toolName:   "Bash",
			toolInput:  "git log --oneline",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "git branch command skipped",
			toolName:   "Bash",
			toolInput:  "git branch -a",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "ls command skipped",
			toolName:   "Bash",
			toolInput:  "ls /tmp",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "pwd command skipped",
			toolName:   "Bash",
			toolInput:  "pwd",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "echo command skipped",
			toolName:   "Bash",
			toolInput:  "echo hello world",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "cat command skipped",
			toolName:   "Bash",
			toolInput:  "cat /etc/hosts",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "which command skipped",
			toolName:   "Bash",
			toolInput:  "which go",
			toolResult: longResult,
			want:       true,
		},
		{
			name:       "type command skipped",
			toolName:   "Bash",
			toolInput:  "type go",
			toolResult: longResult,
			want:       true,
		},
		// Normal operations should NOT be skipped
		{
			name:       "normal Edit not skipped",
			toolName:   "Edit",
			toolInput:  `{"file_path": "/src/main.go"}`,
			toolResult: longResult,
			want:       false,
		},
		{
			name:       "normal Bash make build not skipped",
			toolName:   "Bash",
			toolInput:  "make build",
			toolResult: longResult,
			want:       false,
		},
		{
			name:       "normal Read of source file not skipped",
			toolName:   "Read",
			toolInput:  "/src/main.go",
			toolResult: longResult,
			want:       false,
		},
		{
			name:       "normal go test not skipped",
			toolName:   "Bash",
			toolInput:  "go test ./...",
			toolResult: longResult,
			want:       false,
		},
		{
			name:       "empty tool name not skipped if result long enough",
			toolName:   "",
			toolInput:  "input",
			toolResult: longResult,
			want:       false,
		},
		// Trivial output checks are case-insensitive
		{
			name:       "File Not Found (mixed case) skipped",
			toolName:   "Bash",
			toolInput:  "open",
			toolResult: "File Not Found: cannot open the requested resource here",
			want:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldSkipTrivial(tc.toolName, tc.toolInput, tc.toolResult)
			if got != tc.want {
				t.Errorf("ShouldSkipTrivial(%q, %q, %q) = %v, want %v",
					tc.toolName, tc.toolInput, tc.toolResult, got, tc.want)
			}
		})
	}
}
