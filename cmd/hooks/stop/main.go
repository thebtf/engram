// Package main provides the stop hook entry point.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/thebtf/engram/pkg/hooks"
)

// Input is the hook input from Claude Code.
type Input struct {
	hooks.BaseInput
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// TranscriptMessage represents a message in the transcript JSONL file.
type TranscriptMessage struct {
	Message struct {
		Content any    `json:"content"`
		Role    string `json:"role"`
	} `json:"message"`
	Type string `json:"type"` // Can be string or array
}

// extractTextContent extracts text content from message content (handles both string and array formats).
func extractTextContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var texts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if m["type"] == "text" {
					if text, ok := m["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

// ParsedMessage represents a parsed message from the transcript.
type ParsedMessage struct {
	Role string // "user" or "assistant"
	Text string
}

// maxTranscriptMessages is the maximum number of messages to retain from a transcript.
const maxTranscriptMessages = 50

// parseTranscript reads the transcript file and returns all user/assistant messages
// (up to maxTranscriptMessages, keeping the most recent ones).
// Also returns the last user and assistant messages for backward compatibility.
func parseTranscript(path string) (messages []ParsedMessage, lastUser, lastAssistant string) {
	// Expand ~ to home directory
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = strings.Replace(path, "~", home, 1)
		}
	}

	file, err := os.Open(path) // #nosec G304 -- path is from internal conversation file location
	if err != nil {
		return nil, "", ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Increase buffer size for large messages
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var allMessages []ParsedMessage

	for scanner.Scan() {
		var msg TranscriptMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}

		if msg.Type != "user" && msg.Type != "assistant" {
			continue
		}

		text := extractTextContent(msg.Message.Content)
		if text == "" {
			continue
		}

		allMessages = append(allMessages, ParsedMessage{Role: msg.Type, Text: text})

		switch msg.Type {
		case "user":
			lastUser = text
		case "assistant":
			lastAssistant = text
		}
	}

	// Keep only the last N messages
	if len(allMessages) > maxTranscriptMessages {
		allMessages = allMessages[len(allMessages)-maxTranscriptMessages:]
	}

	return allMessages, lastUser, lastAssistant
}

// InjectedObservation represents an observation fetched for utility analysis.
type InjectedObservation struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// correctionPatterns are patterns indicating user corrected an observation.
var correctionPatterns = []string{
	"no, use ",
	"don't use ",
	"that's wrong",
	"that's incorrect",
	"actually, ",
	"instead of that",
	"not like that",
	"wrong approach",
	"that's not right",
}

// detectUtilitySignals analyzes transcript messages to detect whether injected
// observations were actually used by the assistant, and sends utility signals.
func detectUtilitySignals(ctx *hooks.HookContext, messages []ParsedMessage) {
	// Fetch recently injected observations
	endpoint := fmt.Sprintf("/api/observations/recently-injected?project=%s&limit=50",
		url.QueryEscape(ctx.Project))

	result, err := hooks.GET(ctx.Port, endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[stop] Warning: failed to fetch injected observations: %v\n", err)
		return
	}

	// Parse observation list from response
	obsListRaw, ok := result["observations"]
	if !ok {
		// Try direct array response
		return
	}
	obsArray, ok := obsListRaw.([]interface{})
	if !ok || len(obsArray) == 0 {
		return
	}

	var observations []InjectedObservation
	for _, o := range obsArray {
		obs, ok := o.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := obs["id"].(float64)
		title, _ := obs["title"].(string)
		if id > 0 && title != "" {
			observations = append(observations, InjectedObservation{ID: int64(id), Title: title})
		}
	}

	if len(observations) == 0 {
		return
	}

	// Build combined assistant and user text for matching
	var assistantText strings.Builder
	var userText strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "assistant":
			assistantText.WriteString(strings.ToLower(msg.Text))
			assistantText.WriteString("\n")
		case "user":
			userText.WriteString(strings.ToLower(msg.Text))
			userText.WriteString("\n")
		}
	}
	assistantLower := assistantText.String()
	userLower := userText.String()

	// Check for correction patterns in user messages
	hasCorrection := false
	for _, pattern := range correctionPatterns {
		if strings.Contains(userLower, pattern) {
			hasCorrection = true
			break
		}
	}

	// Detect utility signals per observation
	var usedCount, correctedCount, ignoredCount int
	for _, obs := range observations {
		titleLower := strings.ToLower(obs.Title)

		// Skip very short titles (too many false positives)
		if len(titleLower) < 10 {
			continue
		}

		signal := "ignored" // Default: not found in transcript
		if strings.Contains(assistantLower, titleLower) {
			signal = "used"
		} else if hasCorrection {
			signal = "corrected"
		}

		// Send utility signal (fire-and-forget)
		_, _ = hooks.POST(ctx.Port, fmt.Sprintf("/api/observations/%d/utility", obs.ID),
			map[string]interface{}{"signal": signal})

		switch signal {
		case "used":
			usedCount++
		case "corrected":
			correctedCount++
		default:
			ignoredCount++
		}
	}

	if usedCount+correctedCount+ignoredCount > 0 {
		fmt.Fprintf(os.Stderr, "[stop] Utility signals: %d used, %d corrected, %d ignored\n",
			usedCount, correctedCount, ignoredCount)
	}
}

func main() {
	hooks.RunHook("Stop", handleStop)
}

func handleStop(ctx *hooks.HookContext, input *Input) (string, error) {
	// Debug: dump raw input
	fmt.Fprintf(os.Stderr, "[stop] Raw input: %s\n", string(ctx.RawInput))

	// Find session
	result, err := hooks.GET(ctx.Port, fmt.Sprintf("/api/sessions?claudeSessionId=%s", ctx.SessionID))
	if err != nil || result == nil {
		// Session might not exist, that's OK
		return "", nil
	}

	sessionID, ok := result["id"].(float64)
	if !ok {
		return "", nil
	}

	// Parse transcript to get messages for summary context and utility analysis
	var transcriptMessages []ParsedMessage
	lastUser, lastAssistant := "", ""
	if input.TranscriptPath != "" {
		transcriptMessages, lastUser, lastAssistant = parseTranscript(input.TranscriptPath)
	}
	// Detect utility signals from transcript (Task 2.4)
	if len(transcriptMessages) > 0 {
		go detectUtilitySignals(ctx, transcriptMessages)
	}

	// Debug: log what we extracted
	fmt.Fprintf(os.Stderr, "[stop] Transcript path: %s\n", input.TranscriptPath)
	fmt.Fprintf(os.Stderr, "[stop] Last user message length: %d\n", len(lastUser))
	fmt.Fprintf(os.Stderr, "[stop] Last assistant message length: %d\n", len(lastAssistant))
	if len(lastAssistant) > 0 {
		preview := lastAssistant
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		fmt.Fprintf(os.Stderr, "[stop] Last assistant preview: %s\n", preview)
	}
	fmt.Fprintf(os.Stderr, "[stop] Requesting summary for session %d (transcript: %v)\n", int64(sessionID), input.TranscriptPath != "")

	// Request summary with message context from transcript
	_, err = hooks.POST(ctx.Port, fmt.Sprintf("/sessions/%d/summarize", int64(sessionID)), map[string]interface{}{
		"lastUserMessage":      lastUser,
		"lastAssistantMessage": lastAssistant,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[stop] Warning: summary request failed: %v\n", err)
	}

	return "", nil
}
