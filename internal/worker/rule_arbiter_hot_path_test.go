package worker

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuleArbiter_NotOnSessionStartOrPromptHotPaths(t *testing.T) {
	hotPathFiles := []string{
		"handlers_context.go",
		"handlers_sessions.go",
		"handlers_segment.go",
		"handlers_correction.go",
		"../grpcserver/session_start.go",
		"../../plugin/engram/hooks/session-start.js",
		"../../plugin/engram/hooks/user-prompt.js",
	}
	for _, path := range hotPathFiles {
		t.Run(path, func(t *testing.T) {
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			src := string(b)
			for _, forbidden := range []string{
				"internal/llm",
				"RuleArbiterWorker",
				"NewRuleArbiterWorker",
				"RuleArbiterEvaluator",
				"NewLLMRuleArbiterEvaluator",
				"startRuleArbiterWorker",
				"rule_arbiter",
			} {
				require.Falsef(t, strings.Contains(src, forbidden), "hot path %s must not depend on %s", path, forbidden)
			}
		})
	}
}
