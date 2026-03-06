// Package main provides the session-start hook entry point.
package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/thebtf/engram/pkg/hooks"
)

// Input is the hook input from Claude Code.
type Input struct {
	hooks.BaseInput
	Source string `json:"source"` // "startup", "resume", "clear", "compact"
}

// Observation represents an observation from the API.
type Observation struct {
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Subtitle  string   `json:"subtitle"`
	Narrative string   `json:"narrative"`
	Facts     []string `json:"facts"`
	ID        int64    `json:"id"`
}

func main() {
	hooks.RunHook("SessionStart", handleSessionStart)
}

func handleSessionStart(ctx *hooks.HookContext, input *Input) (string, error) {
	// Fetch observations for context injection
	endpoint := fmt.Sprintf("/api/context/inject?project=%s&cwd=%s",
		url.QueryEscape(ctx.Project),
		url.QueryEscape(ctx.CWD))

	result, err := hooks.GET(ctx.Port, endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[engram] Warning: context fetch failed: %v\n", err)
		return "", nil
	}

	// Parse observations from response
	obsData, ok := result["observations"].([]interface{})
	if !ok || len(obsData) == 0 {
		// No observations - just continue normally
		return "", nil
	}

	// Get full_count from response (how many observations get full detail)
	fullCount := 25 // default
	if fc, ok := result["full_count"].(float64); ok && fc > 0 {
		fullCount = int(fc)
	}

	// Show count to user via stderr
	fmt.Fprintf(os.Stderr, "[engram] Injecting %d observations from project memory (%d detailed, %d condensed)\n",
		len(obsData), min(fullCount, len(obsData)), max(0, len(obsData)-fullCount))

	// Extract observation IDs and mark as injected (non-blocking, fire-and-forget)
	go func() {
		var ids []int64
		for _, o := range obsData {
			if obs, ok := o.(map[string]interface{}); ok {
				if id, ok := obs["id"].(float64); ok && id > 0 {
					ids = append(ids, int64(id))
				}
			}
		}
		// Also include guidance IDs
		if guidanceData, ok := result["guidance"].([]interface{}); ok {
			for _, g := range guidanceData {
				if gObs, ok := g.(map[string]interface{}); ok {
					if id, ok := gObs["id"].(float64); ok && id > 0 {
						ids = append(ids, int64(id))
					}
				}
			}
		}
		if len(ids) > 0 {
			_, _ = hooks.POST(ctx.Port, "/api/observations/mark-injected", map[string]interface{}{
				"ids": ids,
			})
		}
	}()

	// Build context string
	contextBuilder := "<engram-context>\n"
	contextBuilder += fmt.Sprintf("# Project Memory (%d observations)\n", len(obsData))
	contextBuilder += "Use this knowledge to answer questions without re-exploring the codebase.\n\n"

	for i, o := range obsData {
		obs, ok := o.(map[string]interface{})
		if !ok {
			continue
		}

		title := getString(obs, "title")
		obsType := getString(obs, "type")

		// First `fullCount` observations get full detail, rest are condensed
		if i < fullCount {
			// Full detail: include narrative and facts
			narrative := getString(obs, "narrative")

			contextBuilder += fmt.Sprintf("## %d. [%s] %s\n", i+1, strings.ToUpper(obsType), title)
			if narrative != "" {
				contextBuilder += narrative + "\n"
			}

			if facts, ok := obs["facts"].([]interface{}); ok && len(facts) > 0 {
				contextBuilder += "Key facts:\n"
				for _, f := range facts {
					if fact, ok := f.(string); ok && fact != "" {
						contextBuilder += fmt.Sprintf("- %s\n", fact)
					}
				}
			}
			contextBuilder += "\n"
		} else {
			// Condensed: just title and subtitle (one line)
			subtitle := getString(obs, "subtitle")
			if subtitle != "" {
				contextBuilder += fmt.Sprintf("- [%s] %s: %s\n", strings.ToUpper(obsType), title, subtitle)
			} else {
				contextBuilder += fmt.Sprintf("- [%s] %s\n", strings.ToUpper(obsType), title)
			}
		}
	}

	contextBuilder += "</engram-context>\n"

	// Build guidance block from separate guidance observations
	guidanceData, _ := result["guidance"].([]interface{})
	if len(guidanceData) > 0 {
		contextBuilder += "\n<engram-guidance>\n"
		contextBuilder += "# Behavioral Guidance\n"
		contextBuilder += "These are learned preferences and corrections. Follow them.\n\n"

		for i, g := range guidanceData {
			gObs, ok := g.(map[string]interface{})
			if !ok {
				continue
			}
			title := getString(gObs, "title")
			narrative := getString(gObs, "narrative")

			contextBuilder += fmt.Sprintf("%d. **%s**\n", i+1, title)
			if narrative != "" {
				contextBuilder += "   " + narrative + "\n"
			}
			if facts, ok := gObs["facts"].([]interface{}); ok {
				for _, f := range facts {
					if fact, ok := f.(string); ok && fact != "" {
						contextBuilder += fmt.Sprintf("   - %s\n", fact)
					}
				}
			}
			contextBuilder += "\n"
		}

		contextBuilder += "</engram-guidance>\n"

		fmt.Fprintf(os.Stderr, "[engram] Injecting %d guidance observations\n", len(guidanceData))
	}

	return contextBuilder, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
