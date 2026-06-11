// Package crystallization provides session-end decision and pattern extraction.
package crystallization

import (
	"fmt"

	"github.com/thebtf/engram/internal/privacy"
	pkgmodels "github.com/thebtf/engram/pkg/models"
)

// BuildMemories converts extracted decisions into Memory records ready for storage.
// Privacy redaction is applied to decision text before the content is set.
//
// Parameters:
//   - decisions: output from ExtractDecisions; nil or empty → nil returned.
//   - sessionID:  MCP session identifier used to build the session provenance tag.
//   - project:    project slug to scope the memory.
//
// Each produced memory has:
//   - EpistemicType = "decision"
//   - Tier          = "episodic"
//   - SourceAgent   = "crystallization"
//   - Tags          = ["crystallization", "session:<sessionID>"]
//   - Content       = privacy.RedactSecrets(decision.Text)
func BuildMemories(decisions []ExtractedDecision, sessionID, project string) []*pkgmodels.Memory {
	if len(decisions) == 0 {
		return nil
	}

	out := make([]*pkgmodels.Memory, 0, len(decisions))
	sessionTag := fmt.Sprintf("session:%s", sessionID)

	for _, d := range decisions {
		mem := &pkgmodels.Memory{
			Project:       project,
			Content:       privacy.RedactSecrets(d.Text),
			Tags:          []string{"crystallization", sessionTag},
			SourceAgent:   "crystallization",
			EpistemicType: "decision",
			Tier:          "episodic",
		}
		out = append(out, mem)
	}
	return out
}
