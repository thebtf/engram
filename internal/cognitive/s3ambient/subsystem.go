package s3ambient

import (
	"context"
	"errors"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	subsystemName    = "engram.s3.ambient"
	subsystemVersion = "v1.0.0"
)

var ErrNoEmitter = errors.New("s3 ambient emitter not configured")

type Subsystem struct {
	emitter cognitive.HintEmitter
}

var (
	_ cognitivecore.Subsystem = (*Subsystem)(nil)
	_ cognitive.HintEmitter   = (*Subsystem)(nil)
)

func NewSubsystem(emitter cognitive.HintEmitter) *Subsystem {
	return &Subsystem{emitter: emitter}
}

func (s *Subsystem) Name() string { return subsystemName }

func (s *Subsystem) Version() string { return subsystemVersion }

func (s *Subsystem) Start(_ context.Context, _ cognitivecore.Dependencies) error { return nil }

func (s *Subsystem) Stop() error { return nil }

func (s *Subsystem) Implements() []string {
	return []string{"HintEmitter"}
}

func (s *Subsystem) Render(ctx context.Context, surface cognitive.HintSurface, sessionID string, hints []cognitive.HintProposal) (cognitive.HintDelivery, error) {
	if s == nil || s.emitter == nil {
		return cognitive.HintDelivery{}, ErrNoEmitter
	}
	return s.emitter.Render(ctx, surface, sessionID, hints)
}
