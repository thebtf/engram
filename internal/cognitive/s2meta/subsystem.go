package s2meta

import (
	"context"
	"errors"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	subsystemName    = "engram.s2.meta_memory"
	subsystemVersion = "v1.0.0"
)

var ErrNoMetaIndex = errors.New("s2 meta-memory index not configured")

type Subsystem struct {
	proposer cognitive.CandidateProposer
}

var (
	_ cognitivecore.Subsystem     = (*Subsystem)(nil)
	_ cognitive.CandidateProposer = (*Subsystem)(nil)
)

func NewSubsystem(index MetaIndex) *Subsystem {
	return &Subsystem{proposer: NewMetaIndexProposer(index)}
}

func (s *Subsystem) Name() string { return subsystemName }

func (s *Subsystem) Version() string { return subsystemVersion }

func (s *Subsystem) Start(_ context.Context, _ cognitivecore.Dependencies) error { return nil }

func (s *Subsystem) Stop() error { return nil }

func (s *Subsystem) Implements() []string { return []string{"CandidateProposer"} }

func (s *Subsystem) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	if s == nil || s.proposer == nil {
		return nil, ErrNoMetaIndex
	}
	return s.proposer.Propose(ctx, event, limit)
}
