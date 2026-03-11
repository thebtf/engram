package sessions

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const maxJSONLLineSize = 1024 * 1024

// Exchange represents a user-assistant message pair.
type Exchange struct {
	UserText      string
	AssistantText string
	ToolsUsed     []string
	Timestamp     time.Time
}

// SessionMeta holds metadata extracted from a parsed session.
type SessionMeta struct {
	SessionID     string
	ProjectPath   string
	GitBranch     string
	FirstMsgAt    time.Time
	LastMsgAt     time.Time
	Exchanges     []Exchange
	ToolCounts    map[string]int
	ExchangeCount int
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

type sessionMessage struct {
	Content json.RawMessage `json:"content"`
}

type sessionLine struct {
	Type      string         `json:"type"`
	Message   sessionMessage `json:"message"`
	Timestamp string         `json:"timestamp"`
	SessionID string         `json:"sessionId"`
	CWD       string         `json:"cwd"`
	GitBranch string         `json:"gitBranch"`
}

// ParseSession reads a Claude JSONL session file and returns parsed session metadata.
func ParseSession(path string) (*SessionMeta, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	return ParseSessionReader(file)
}

// ParseSessionReader reads Claude JSONL session data from an io.Reader.
func ParseSessionReader(r io.Reader) (*SessionMeta, error) {
	meta := &SessionMeta{ToolCounts: make(map[string]int)}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, bufio.MaxScanTokenSize), maxJSONLLineSize)

	var pendingUser *Exchange
	var assistantTexts []string
	var assistantTools []string

	// flushExchange saves the accumulated user+assistant exchange.
	flushExchange := func() {
		if pendingUser == nil {
			return
		}
		exchange := Exchange{
			UserText:      pendingUser.UserText,
			AssistantText: strings.Join(assistantTexts, ""),
			ToolsUsed:     dedupeStrings(assistantTools),
			Timestamp:     pendingUser.Timestamp,
		}
		for _, toolName := range assistantTools {
			if toolName == "" {
				continue
			}
			meta.ToolCounts[toolName]++
		}
		meta.Exchanges = append(meta.Exchanges, exchange)
		meta.ExchangeCount++
		pendingUser = nil
		assistantTexts = nil
		assistantTools = nil
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var parsedLine sessionLine
		if err := json.Unmarshal([]byte(line), &parsedLine); err != nil {
			// Skip malformed JSON lines.
			continue
		}

		timestamp, hasTimestamp := parseTimestamp(parsedLine.Timestamp)
		if hasTimestamp {
			if meta.FirstMsgAt.IsZero() {
				meta.FirstMsgAt = timestamp
			}
			meta.LastMsgAt = timestamp
		}

		if meta.SessionID == "" {
			meta.SessionID = parsedLine.SessionID
		}
		if meta.ProjectPath == "" {
			meta.ProjectPath = parsedLine.CWD
		}
		if meta.GitBranch == "" {
			meta.GitBranch = parsedLine.GitBranch
		}

		switch parsedLine.Type {
		case "user":
			// Flush any pending exchange before starting a new one
			flushExchange()
			pendingUser = &Exchange{
				UserText:  strings.Join(extractTextItems(parsedLine.Message.Content), ""),
				Timestamp: timestamp,
			}
		case "assistant":
			if pendingUser == nil {
				continue
			}
			// Accumulate all assistant blocks (text, tool_use) for this exchange
			assistantTexts = append(assistantTexts, extractTextItems(parsedLine.Message.Content)...)
			assistantTools = append(assistantTools, extractToolNames(parsedLine.Message.Content)...)
		default:
			continue
		}
	}

	// Flush the last exchange if present
	flushExchange()

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan session file: %w", err)
	}

	return meta, nil
}

// WorkstationID computes a deterministic 8-char hexadecimal ID from hostname + machine_id.
func WorkstationID() string {
	hostname, _ := os.Hostname()
	machineID := machineIdentifier()
	if machineID == "" {
		machineID = hostname
	}

	input := fmt.Sprintf("%s%s", hostname, machineID)
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])[:8]
}

// ProjectID computes a deterministic 8-char hexadecimal project ID.
// Tries git remote origin URL first (stable across machines and OS path layouts),
// falls back to SHA-256 of the absolute path if not a git repo or has no remote.
func ProjectID(cwdPath string) string {
	if id, err := GitRemoteProjectID(cwdPath); err == nil && id != "" {
		return id
	}
	hash := sha256.Sum256([]byte(cwdPath))
	return hex.EncodeToString(hash[:])[:8]
}

// GitRemoteProjectID computes a stable 8-char project ID from the git remote origin URL
// and the repository-relative path of cwdPath (via git rev-parse --show-prefix).
// Returns "", error if cwdPath is not inside a git repo or has no remote configured.
func GitRemoteProjectID(cwdPath string) (string, error) {
	remoteURL, err := runGitCommand(cwdPath, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	if remoteURL == "" {
		return "", fmt.Errorf("git remote origin URL is empty")
	}
	relativePath, err := runGitCommand(cwdPath, "rev-parse", "--show-prefix")
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-prefix: %w", err)
	}
	key := remoteURL + "/" + relativePath
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])[:8], nil
}

// runGitCommand runs git with the given args inside cwdPath and returns trimmed stdout.
func runGitCommand(cwdPath string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", cwdPath}, args...)
	out, err := exec.Command("git", fullArgs...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CompositeKey builds the isolation key: workstation_id:project_id:session_id.
func CompositeKey(workstationID, projectID, sessionID string) string {
	return fmt.Sprintf("%s:%s:%s", workstationID, projectID, sessionID)
}

func parseTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}

	t, err := time.Parse(time.RFC3339, raw)
	if err == nil {
		return t, true
	}
	t, err = time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func extractTextItems(content json.RawMessage) []string {
	// Try plain string first (Claude Code sometimes sends content as a string)
	if plainText, ok := parseContentString(content); ok {
		if plainText != "" {
			return []string{plainText}
		}
		return nil
	}

	items, ok := parseContentItems(content)
	if !ok {
		return nil
	}

	texts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Type == "text" {
			texts = append(texts, item.Text)
		}
	}
	return texts
}

func extractToolNames(content json.RawMessage) []string {
	items, ok := parseContentItems(content)
	if !ok {
		return nil
	}

	tools := make([]string, 0, len(items))
	for _, item := range items {
		if item.Type == "tool_use" {
			tools = append(tools, item.Name)
		}
	}
	return tools
}

func parseContentString(content json.RawMessage) (string, bool) {
	if len(content) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(content, &s); err != nil {
		return "", false
	}
	return s, true
}

func parseContentItems(content json.RawMessage) ([]contentItem, bool) {
	if len(content) == 0 {
		return nil, false
	}

	var items []contentItem
	if err := json.Unmarshal(content, &items); err != nil {
		return nil, false
	}
	return items, true
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func machineIdentifier() string {
	if runtime.GOOS != "linux" {
		return ""
	}

	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return ""
	}

	identifier := strings.TrimSpace(string(data))
	if identifier == "" {
		return ""
	}

	return identifier
}
