package s3ambient

import (
	"time"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/pkg/cognitive"
)

const queuedHintMaxAge = 24 * time.Hour

// DrainQueuedProposals drains the full per-session queue once, filters stale
// entries, and converts the substrate payloads into public hint proposals.
func DrainQueuedProposals(queue cognitivecore.HintQueue, sessionID string, now time.Time) []cognitive.HintProposal {
	if queue == nil || sessionID == "" {
		return nil
	}
	stats := queue.Stats(sessionID)
	if stats.QueuedNow == 0 {
		return nil
	}
	return FreshQueuedProposals(queue.Drain(sessionID, stats.QueuedNow), now)
}

// FreshQueuedProposals converts queued payloads to public hint proposals while
// dropping entries that have expired past the fallback delivery window.
func FreshQueuedProposals(entries []cognitivecore.HintProposalPayload, now time.Time) []cognitive.HintProposal {
	if len(entries) == 0 {
		return nil
	}
	out := make([]cognitive.HintProposal, 0, len(entries))
	for _, entry := range entries {
		if !entry.CreatedAt.IsZero() && now.Sub(entry.CreatedAt) > queuedHintMaxAge {
			continue
		}
		out = append(out, cognitive.HintProposal{
			ID:        entry.ID,
			Title:     entry.Title,
			Tags:      append([]string(nil), entry.Tags...),
			CreatedAt: entry.CreatedAt,
			Score:     entry.Score,
			Source:    entry.Source,
			Reason:    entry.Reason,
		})
	}
	return out
}
