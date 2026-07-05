package s6

import (
	"context"
	"fmt"

	core "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	subsystemName    = "engram.s6.outcome_policy"
	subsystemVersion = "v1.0.0"
)

type Subsystem struct {
	proposer cognitive.CandidateProposer
}

var (
	_ core.Subsystem              = (*Subsystem)(nil)
	_ cognitive.CandidateProposer = (*Subsystem)(nil)
)

func NewSubsystem(store OutcomeStore) *Subsystem {
	return &Subsystem{proposer: NewOutcomeProposer(store)}
}

func (s *Subsystem) Name() string { return subsystemName }

func (s *Subsystem) Version() string { return subsystemVersion }

func (s *Subsystem) Start(_ context.Context, _ core.Dependencies) error { return nil }

func (s *Subsystem) Stop() error { return nil }

func (s *Subsystem) Implements() []string { return []string{"CandidateProposer"} }

func (s *Subsystem) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	if s == nil || s.proposer == nil {
		return nil, fmt.Errorf("outcome proposer is not configured")
	}
	return s.proposer.Propose(ctx, event, limit)
}
