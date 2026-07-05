package s6

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

const (
	defaultProposalLimit = 10
	maxProposalLimit     = 25
	proposalSource       = "s6.outcome_policy"
)

// OutcomeStore is the bounded read seam used by OutcomeProposer.
// It intentionally returns only project-scoped memory rows for ranking.
type OutcomeStore interface {
	ListOutcomeCandidates(ctx context.Context, project string, limit int) ([]*models.Memory, error)
}

// OutcomeProposer adapts outcome-ranked memory rows into the shared S3
// CandidateProposer surface without leaking memory content.
type OutcomeProposer struct {
	store OutcomeStore
}

func NewOutcomeProposer(store OutcomeStore) *OutcomeProposer {
	return &OutcomeProposer{store: store}
}

func (p *OutcomeProposer) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	if p == nil || p.store == nil {
		return nil, fmt.Errorf("outcome proposer store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	project := strings.TrimSpace(event.Project)
	if project == "" {
		return []cognitive.HintProposal{}, nil
	}
	limit = normalizeProposalLimit(limit)

	memories, err := p.store.ListOutcomeCandidates(ctx, project, limit)
	if err != nil {
		return nil, err
	}

	ranked := make([]scoredCandidate, 0, len(memories))
	for _, memory := range memories {
		if memory == nil || memory.Project != project {
			continue
		}
		ranked = append(ranked, scoredCandidate{
			memory: memory,
			score:  outcomePosterior(memory),
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].memory.CreatedAt.After(ranked[j].memory.CreatedAt)
		}
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	proposals := make([]cognitive.HintProposal, 0, len(ranked))
	for _, candidate := range ranked {
		proposals = append(proposals, cognitive.HintProposal{
			ID:        fmt.Sprintf("%d", candidate.memory.ID),
			Title:     advisoryTitle(candidate.memory.ID),
			Tags:      append([]string(nil), candidate.memory.Tags...),
			CreatedAt: candidate.memory.CreatedAt,
			Score:     float32(candidate.score),
			Source:    proposalSource,
		})
	}
	return proposals, nil
}

type scoredCandidate struct {
	memory *models.Memory
	score  float64
}

func normalizeProposalLimit(limit int) int {
	if limit <= 0 {
		return defaultProposalLimit
	}
	if limit > maxProposalLimit {
		return maxProposalLimit
	}
	return limit
}

func outcomePosterior(memory *models.Memory) float64 {
	if memory == nil {
		return 0
	}
	alpha := memory.TsAlpha
	beta := memory.TsBeta
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}
	return alpha / (alpha + beta)
}

func advisoryTitle(id int64) string {
	return fmt.Sprintf("Memory %d", id)
}
