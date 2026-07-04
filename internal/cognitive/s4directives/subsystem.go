package s4directives

import (
	"context"
	"errors"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	subsystemName    = "engram.s4a.directives_capture"
	subsystemVersion = "v1.0.0"
)

var ErrNoService = errors.New("s4a directives service not configured")

type Subsystem struct {
	service *Service
}

var (
	_ cognitivecore.Subsystem       = (*Subsystem)(nil)
	_ cognitive.AttentionEventWriter = (*Subsystem)(nil)
	_ cognitive.DirectiveDistiller   = (*Subsystem)(nil)
)

func NewSubsystem(service *Service) *Subsystem {
	return &Subsystem{service: service}
}

func (s *Subsystem) Name() string { return subsystemName }

func (s *Subsystem) Version() string { return subsystemVersion }

func (s *Subsystem) Start(_ context.Context, _ cognitivecore.Dependencies) error { return nil }

func (s *Subsystem) Stop() error { return nil }

func (s *Subsystem) Implements() []string {
	return []string{"AttentionEventWriter", "DirectiveDistiller"}
}

func (s *Subsystem) WriteAttentionEvent(ctx context.Context, event cognitive.AttentionEventRecord) error {
	if s == nil || s.service == nil {
		return ErrNoService
	}
	return s.service.WriteAttentionEvent(ctx, event)
}

func (s *Subsystem) Distill(ctx context.Context, rawSignal cognitive.RawSignal) (cognitive.Distilled, error) {
	if s == nil || s.service == nil {
		return cognitive.Distilled{}, ErrNoService
	}
	return s.service.Distill(ctx, rawSignal)
}
