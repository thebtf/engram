package s3ambient

import (
	"context"
	"sort"

	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	defaultHintLimit = 3
	maxHintLimit     = 3
	rrfK             = 60
)

// Fusion deadline-fans out the shared CandidateProposer surface and fuses
// enabled results via reciprocal rank fusion.
type Fusion struct {
	enabled   bool
	proposers []cognitive.CandidateProposer
}

func NewFusion(enabled bool, proposers []cognitive.CandidateProposer) *Fusion {
	return &Fusion{
		enabled:   enabled,
		proposers: compactCandidateProposers(proposers),
	}
}

func compactCandidateProposers(proposers []cognitive.CandidateProposer) []cognitive.CandidateProposer {
	if len(proposers) == 0 {
		return nil
	}
	out := make([]cognitive.CandidateProposer, 0, len(proposers))
	for _, proposer := range proposers {
		if proposer == nil {
			continue
		}
		out = append(out, proposer)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (f *Fusion) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	if f == nil || !f.enabled {
		return []cognitive.HintProposal{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	activeProposers := f.proposers
	if len(activeProposers) == 0 {
		return []cognitive.HintProposal{}, nil
	}

	limit = normalizeHintLimit(limit)
	type proposerResult struct {
		proposals []cognitive.HintProposal
		err       error
	}
	results := make(chan proposerResult, len(activeProposers))

	for _, proposer := range activeProposers {
		go func(p cognitive.CandidateProposer) {
			proposals, err := p.Propose(ctx, event, limit)
			results <- proposerResult{proposals: proposals, err: err}
		}(proposer)
	}

	merged := make([][]cognitive.HintProposal, 0, len(activeProposers))
	for range len(activeProposers) {
		select {
		case result := <-results:
			if result.err != nil {
				continue
			}
			if len(result.proposals) == 0 {
				continue
			}
			merged = append(merged, result.proposals)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if len(merged) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return []cognitive.HintProposal{}, nil
	}

	return reciprocalRankFuse(merged, limit), nil
}

type fusedProposal struct {
	proposal  cognitive.HintProposal
	fused     float32
	bestRank  int
	firstSeen int
}

func reciprocalRankFuse(lists [][]cognitive.HintProposal, limit int) []cognitive.HintProposal {
	if limit <= 0 {
		return []cognitive.HintProposal{}
	}

	seen := make(map[string]*fusedProposal, limit*len(lists))
	order := 0
	for _, proposals := range lists {
		for rank, proposal := range proposals {
			if proposal.ID == "" {
				continue
			}
			score := float32(1.0 / float64(rrfK+rank+1))
			entry, ok := seen[proposal.ID]
			if !ok {
				copyProposal := proposal
				seen[proposal.ID] = &fusedProposal{
					proposal:  copyProposal,
					fused:     score,
					bestRank:  rank,
					firstSeen: order,
				}
				order++
				continue
			}
			entry.fused += score
			if rank < entry.bestRank || (rank == entry.bestRank && proposal.Score > entry.proposal.Score) {
				entry.proposal = proposal
				entry.bestRank = rank
			}
		}
	}

	ranked := make([]fusedProposal, 0, len(seen))
	for _, proposal := range seen {
		proposal.proposal.Score = proposal.fused
		ranked = append(ranked, *proposal)
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].fused == ranked[j].fused {
			if ranked[i].bestRank == ranked[j].bestRank {
				return ranked[i].firstSeen < ranked[j].firstSeen
			}
			return ranked[i].bestRank < ranked[j].bestRank
		}
		return ranked[i].fused > ranked[j].fused
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	out := make([]cognitive.HintProposal, 0, len(ranked))
	for _, proposal := range ranked {
		out = append(out, proposal.proposal)
	}
	return out
}

func normalizeHintLimit(limit int) int {
	if limit <= 0 {
		return defaultHintLimit
	}
	if limit > maxHintLimit {
		return maxHintLimit
	}
	return limit
}
