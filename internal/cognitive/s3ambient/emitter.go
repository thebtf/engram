package s3ambient

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	maxTitleChars  = 80
	maxReasonChars = 120
	ignorableIntro = "Memory suggests (you may ignore)"
)

// Emitter renders bounded ambient hints for same-turn prompt injection and MCP
// fallback polling.
type Emitter struct {
	enabled bool
}

func NewEmitter(enabled bool) *Emitter {
	return &Emitter{enabled: enabled}
}

func (e *Emitter) Render(ctx context.Context, surface cognitive.HintSurface, _ string, hints []cognitive.HintProposal) (cognitive.HintDelivery, error) {
	if e == nil || !e.enabled {
		return cognitive.HintDelivery{}, nil
	}
	if err := ctx.Err(); err != nil {
		return cognitive.HintDelivery{}, err
	}

	sanitized := sanitizeHintProposals(hints)
	if len(sanitized) == 0 {
		return cognitive.HintDelivery{}, nil
	}

	switch surface {
	case cognitive.HintSurfaceUserPromptSubmit:
		return cognitive.HintDelivery{
			Surface:           surface,
			AdditionalContext: renderAdditionalContext(sanitized),
		}, nil
	case cognitive.HintSurfaceMCPPoll:
		return cognitive.HintDelivery{
			Surface: surface,
			Hints:   sanitized,
		}, nil
	default:
		return cognitive.HintDelivery{}, fmt.Errorf("unsupported hint surface %q", surface)
	}
}

func sanitizeHintProposals(hints []cognitive.HintProposal) []cognitive.HintProposal {
	if len(hints) == 0 {
		return nil
	}
	if len(hints) > maxHintLimit {
		hints = hints[:maxHintLimit]
	}

	out := make([]cognitive.HintProposal, 0, len(hints))
	for _, hint := range hints {
		if sanitized, ok := sanitizeHintProposal(hint); ok {
			out = append(out, sanitized)
		}
	}
	return out
}

func sanitizeHintProposal(hint cognitive.HintProposal) (cognitive.HintProposal, bool) {
	if hint.ID == "" {
		return cognitive.HintProposal{}, false
	}
	title := truncateASCII(normalizeInlineWhitespace(hint.Title), maxTitleChars)
	if title == "" {
		return cognitive.HintProposal{}, false
	}
	copyHint := hint
	copyHint.Title = title
	copyHint.Reason = truncateASCII(normalizeInlineWhitespace(hint.Reason), maxReasonChars)
	copyHint.Tags = append([]string(nil), hint.Tags...)
	return copyHint, true
}

func renderAdditionalContext(hints []cognitive.HintProposal) string {
	lines := make([]string, 0, len(hints)+1)
	lines = append(lines, ignorableIntro)
	for _, hint := range hints {
		line := "- " + hint.Title
		if hint.Reason != "" {
			line += " — " + hint.Reason
		}
		meta := strings.TrimSpace(hint.Source)
		if meta != "" {
			line += " [" + meta
			if hint.Score != 0 {
				line += " " + strconv.FormatFloat(float64(hint.Score), 'f', 2, 32)
			}
			line += "]"
		} else if hint.Score != 0 {
			line += " [score " + strconv.FormatFloat(float64(hint.Score), 'f', 2, 32) + "]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func normalizeInlineWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateASCII(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
