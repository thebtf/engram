// Package pipeline provides the deterministic observation extraction pipeline.
// Level 0 processing: no LLM required, rule-based classification and extraction.
package pipeline

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/strutil"
)

// checkboxToggleRe matches checkbox state changes in task lists.
var checkboxToggleRe = regexp.MustCompile(`-\s*\[[ x]\]\s`)

// taskFileRe matches common task/checklist file names.
var taskFileRe = regexp.MustCompile(`(?i)(tasks\.md|todo\.md|checklist\.md)`)

// IsCheckboxToggle detects task checkbox toggle edits ([ ]→[x] or [x]→[ ]).
// These are routine progress tracking, not meaningful decisions.
func IsCheckboxToggle(toolName, toolInput string) bool {
	if toolName != "Edit" {
		return false
	}
	// Check if file is a task/checklist file
	if !taskFileRe.MatchString(toolInput) {
		return false
	}
	// Check for checkbox pattern in old_string or new_string
	return checkboxToggleRe.MatchString(toolInput)
}

// ClassifyEvent determines the observation type from a raw tool event.
// Rule-based classification achieving ~80% accuracy vs LLM.
func ClassifyEvent(toolName, toolInput, toolResult string) models.ObservationType {
	// Checkbox toggles in task files are routine progress, not decisions.
	if IsCheckboxToggle(toolName, toolInput) {
		return models.ObsTypeChange
	}

	lowerInput := strings.ToLower(toolInput)
	lowerResult := strings.ToLower(toolResult)

	switch toolName {
	case "Edit", "Write", "NotebookEdit":
		return models.ObsTypeChange

	case "Bash":
		// Check for error indicators
		if containsAny(lowerResult, []string{
			"error:", "failed", "exit code 1", "panic:", "fatal:",
			"exception", "traceback", "segfault",
		}) {
			return models.ObsTypeBugfix
		}
		// Check for test execution
		if containsAny(lowerInput, []string{"test", "spec", "jest", "pytest", "go test"}) {
			return models.ObsTypeDiscovery
		}
		// Check for architectural commands
		if containsAny(lowerInput, []string{
			"docker", "migrate", "schema", "deploy", "init",
		}) {
			return models.ObsTypeChange
		}
		return models.ObsTypeChange

	case "Read":
		return models.ObsTypeDiscovery

	case "Grep":
		return models.ObsTypeDiscovery

	case "WebFetch", "WebSearch":
		return models.ObsTypeDiscovery
	}

	// Check content for decision indicators
	if containsAny(lowerInput+lowerResult, []string{
		"architecture", "design", "decision", "chose", "selected",
		"tradeoff", "approach", "strategy",
	}) {
		return models.ObsTypeDecision
	}

	// Check for refactoring
	if containsAny(lowerInput+lowerResult, []string{
		"refactor", "rename", "extract", "consolidate", "cleanup",
	}) {
		return models.ObsTypeRefactor
	}

	return models.ObsTypeChange
}

// GenerateTitle creates a template-based title for the observation.
func GenerateTitle(toolName, toolInput string) string {
	switch toolName {
	case "Edit":
		if path := extractFilePath(toolInput); path != "" {
			return fmt.Sprintf("Edit: %s", filepath.Base(path))
		}
		return "Edit: file"

	case "Write":
		if path := extractFilePath(toolInput); path != "" {
			return fmt.Sprintf("Write: %s", filepath.Base(path))
		}
		return "Write: new file"

	case "Bash":
		cmd := extractBashCommand(toolInput)
		if cmd != "" {
			// Truncate long commands
			if len(cmd) > 80 {
				cmd = cmd[:77] + "..."
			}
			return fmt.Sprintf("Bash: %s", cmd)
		}
		return "Bash: command"

	case "Read":
		if path := extractFilePath(toolInput); path != "" {
			return fmt.Sprintf("Read: %s", filepath.Base(path))
		}
		return "Read: file"

	case "Grep":
		return "Grep: search"

	case "WebFetch":
		return "WebFetch: external resource"

	case "WebSearch":
		return "WebSearch: web query"

	case "NotebookEdit":
		return "NotebookEdit: cell"
	}

	return fmt.Sprintf("%s: operation", toolName)
}

// ExtractConcepts extracts keyword-based concepts from the event.
// Returns deduplicated concept tags.
func ExtractConcepts(toolName, toolInput, toolResult string) []string {
	combined := strings.ToLower(toolInput + " " + toolResult)
	seen := make(map[string]bool)
	var concepts []string

	// Domain keywords
	domainKeywords := map[string]string{
		"authentication": "authentication",
		"auth":           "authentication",
		"login":          "authentication",
		"database":       "database",
		"migration":      "database",
		"schema":         "database",
		"api":            "api",
		"endpoint":       "api",
		"handler":        "api",
		"test":           "testing",
		"spec":           "testing",
		"coverage":       "testing",
		"docker":         "docker",
		"container":      "docker",
		"security":       "security",
		"encrypt":        "security",
		"token":          "security",
		"performance":    "performance",
		"cache":          "performance",
		"optimize":       "performance",
		"debug":          "debugging",
		"error":          "debugging",
		"fix":            "debugging",
		"config":         "configuration",
		"setting":        "configuration",
		"deploy":         "deployment",
		"ci":             "ci-cd",
		"pipeline":       "ci-cd",
		"refactor":       "refactoring",
		"architecture":   "architecture",
		"design":         "architecture",
		"pattern":        "pattern",
	}

	for keyword, concept := range domainKeywords {
		if strings.Contains(combined, keyword) && !seen[concept] {
			seen[concept] = true
			concepts = append(concepts, concept)
		}
	}

	// Extract from file paths
	if path := extractFilePath(toolInput); path != "" {
		dir := filepath.Dir(path)
		parts := strings.Split(filepath.ToSlash(dir), "/")
		for _, part := range parts {
			part = strings.ToLower(part)
			if len(part) > 2 && !seen[part] && isRelevantDirName(part) {
				seen[part] = true
				concepts = append(concepts, part)
			}
		}
	}

	return concepts
}

// ExtractFilePaths extracts file paths from tool input and output.
func ExtractFilePaths(toolInput, toolResult string) []string {
	seen := make(map[string]bool)
	var paths []string

	// Extract from JSON-like file_path fields
	for _, match := range filePathPattern.FindAllStringSubmatch(toolInput, -1) {
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
			paths = append(paths, match[1])
		}
	}

	// Extract standalone file paths from the combined text
	for _, match := range standalonePathPattern.FindAllString(toolInput+" "+toolResult, -1) {
		clean := strings.TrimSpace(match)
		if !seen[clean] && looksLikeFilePath(clean) {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}

	return paths
}

// ExtractFacts extracts diff-based facts from tool events.
func ExtractFacts(toolName, toolInput, toolResult string) []string {
	var facts []string

	switch toolName {
	case "Edit":
		if path := extractFilePath(toolInput); path != "" {
			facts = append(facts, fmt.Sprintf("Modified file: %s", filepath.Base(path)))
		}

	case "Write":
		if path := extractFilePath(toolInput); path != "" {
			facts = append(facts, fmt.Sprintf("Created file: %s", filepath.Base(path)))
		}

	case "Bash":
		cmd := extractBashCommand(toolInput)
		if cmd != "" {
			facts = append(facts, fmt.Sprintf("Executed: %s", truncate(cmd, 120)))
		}
		// Extract exit code or error info
		if strings.Contains(toolResult, "exit code") {
			facts = append(facts, "Command completed with non-zero exit code")
		}
	}

	return facts
}

// FormatEmbeddingText creates compact text for embedding from an observation.
func FormatEmbeddingText(obs *models.Observation) string {
	var parts []string

	if obs.Type != "" {
		parts = append(parts, string(obs.Type))
	}
	if obs.Title.Valid && obs.Title.String != "" {
		parts = append(parts, obs.Title.String)
	}
	if len(obs.FilesModified) > 0 {
		parts = append(parts, "Files: "+strings.Join(obs.FilesModified, ", "))
	}
	if len(obs.Concepts) > 0 {
		parts = append(parts, "Concepts: "+strings.Join(obs.Concepts, ", "))
	}
	if len(obs.Facts) > 0 {
		for _, f := range obs.Facts {
			parts = append(parts, f)
		}
	}

	text := strings.Join(parts, "\n")

	// Rough truncation at ~1500 chars (~400 tokens for BGE)
	if len(text) > 1500 {
		text = text[:1497] + "..."
	}

	return text
}

// ShouldSkipTool checks if a tool should be skipped for observation extraction.
// Exported version of the filter from sdk/processor.go.
func ShouldSkipTool(toolName string) bool {
	skipTools := map[string]bool{
		"TodoWrite":       true,
		"Task":            true,
		"TaskOutput":      true,
		"TaskCreate":      true,
		"TaskUpdate":      true,
		"TaskList":        true,
		"TaskGet":         true,
		"Glob":            true,
		"ListDir":         true,
		"LS":              true,
		"KillShell":       true,
		"TaskStop":        true,
		"AskUserQuestion": true,
		"EnterPlanMode":   true,
		"ExitPlanMode":    true,
		"Skill":           true,
		"SlashCommand":    true,
		"EnterWorktree":   true,
		"ToolSearch":      true,
		// High-volume, low-value tools — create noise without meaningful observations
		"Read":      true,
		"Grep":      true,
		"WebSearch": true,
	}
	return skipTools[toolName]
}

// ShouldSkipTrivial checks if the operation is too trivial to process.
// Exported version of the filter from sdk/processor.go.
func ShouldSkipTrivial(toolName, toolInput, toolResult string) bool {
	if len(toolResult) < 50 {
		return true
	}

	lowerResult := strings.ToLower(toolResult)
	lowerInput := strings.ToLower(toolInput)

	trivialOutputs := []string{
		"no matches found", "file not found", "directory not found",
		"permission denied", "command not found", "no such file",
		"is a directory", "[]", "{}",
	}
	for _, trivial := range trivialOutputs {
		if strings.Contains(lowerResult, trivial) || toolResult == trivial {
			return true
		}
	}

	switch toolName {
	case "Read":
		boringFiles := []string{
			"package-lock.json", "yarn.lock", "pnpm-lock.yaml",
			"go.sum", "cargo.lock", "gemfile.lock", "poetry.lock",
			".gitignore", ".dockerignore", ".eslintignore",
		}
		for _, boring := range boringFiles {
			if strings.Contains(lowerInput, boring) {
				return true
			}
		}

	case "Bash":
		boringCommands := []string{
			"git status", "git diff", "git log", "git branch",
			"ls ", "pwd", "echo ", "cat ", "which ", "type ",
		}
		for _, boring := range boringCommands {
			if strings.Contains(lowerInput, boring) {
				return true
			}
		}
	}

	return false
}

// --- Helpers ---

var (
	filePathPattern       = regexp.MustCompile(`"file_path"\s*:\s*"([^"]+)"`)
	standalonePathPattern = regexp.MustCompile(`(?:^|[\s"'])([A-Za-z]:\\[^\s"']+|/[^\s"']+\.[a-zA-Z0-9]+)`)
)

var containsAny = strutil.ContainsAny

func extractFilePath(input string) string {
	matches := filePathPattern.FindStringSubmatch(input)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func extractBashCommand(input string) string {
	// Try to extract "command" field from JSON-like input
	cmdPattern := regexp.MustCompile(`"command"\s*:\s*"([^"]*)"`)
	matches := cmdPattern.FindStringSubmatch(input)
	if len(matches) > 1 {
		return matches[1]
	}
	// Fallback: first line of input
	if idx := strings.IndexByte(input, '\n'); idx > 0 {
		return strings.TrimSpace(input[:idx])
	}
	if len(input) > 120 {
		return input[:117] + "..."
	}
	return input
}

func isRelevantDirName(name string) bool {
	irrelevant := map[string]bool{
		"src": true, "lib": true, "pkg": true, "cmd": true,
		"internal": true, "vendor": true, "node_modules": true,
		"dist": true, "build": true, "bin": true, ".": true,
	}
	return !irrelevant[name]
}

func looksLikeFilePath(s string) bool {
	ext := filepath.Ext(s)
	return ext != "" && len(ext) <= 6 && len(s) > 5
}

var truncate = strutil.Truncate
