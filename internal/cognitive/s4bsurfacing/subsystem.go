package s4bsurfacing

import (
	"context"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	subsystemName    = "engram.s4b.directives_surfacing"
	subsystemVersion = "v1.0.0"
)

type Subsystem struct {
	proposer *Proposer
}

var (
	_ cognitivecore.Subsystem     = (*Subsystem)(nil)
	_ cognitive.CandidateProposer = (*Subsystem)(nil)
)

func NewSubsystem(source Source) *Subsystem {
	return &Subsystem{proposer: NewProposer(source)}
}

func (s *Subsystem) Name() string { return subsystemName }

func (s *Subsystem) Version() string { return subsystemVersion }

func (s *Subsystem) Start(ctx context.Context, deps cognitivecore.Dependencies) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.proposer == nil || !hasEffectiveSource(s.proposer.source) {
		return ErrNoSource
	}
	s.proposer.setMeter(deps.Meter)
	return nil
}

func (s *Subsystem) Stop() error { return nil }

func (s *Subsystem) Implements() []string { return []string{"CandidateProposer"} }

func (s *Subsystem) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	if s == nil || s.proposer == nil {
		return nil, ErrNoSource
	}
	return s.proposer.Propose(ctx, event, limit)
}
