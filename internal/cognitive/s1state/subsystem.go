package s1state

import (
	"context"
	"errors"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	subsystemName    = "engram.s1.state_writer"
	subsystemVersion = "v1.0.0"
)

var ErrNoWriter = errors.New("s1 state writer not configured")

type Subsystem struct {
	writer cognitive.StateWriter
}

var (
	_ cognitivecore.Subsystem = (*Subsystem)(nil)
	_ cognitive.StateWriter   = (*Subsystem)(nil)
)

func NewSubsystem(writer cognitive.StateWriter) *Subsystem {
	return &Subsystem{writer: writer}
}

func (s *Subsystem) Name() string {
	return subsystemName
}

func (s *Subsystem) Version() string {
	return subsystemVersion
}

func (s *Subsystem) Start(_ context.Context, _ cognitivecore.Dependencies) error {
	return nil
}

func (s *Subsystem) Stop() error {
	return nil
}

func (s *Subsystem) Implements() []string {
	return []string{"StateWriter"}
}

func (s *Subsystem) WriteSessionState(ctx context.Context, sessionID string, slots cognitive.SessionStateSlots) error {
	if s.writer == nil {
		return ErrNoWriter
	}
	return s.writer.WriteSessionState(ctx, sessionID, slots)
}

func (s *Subsystem) WriteProjectState(ctx context.Context, project string, state cognitive.ProjectStateRecord) error {
	if s.writer == nil {
		return ErrNoWriter
	}
	return s.writer.WriteProjectState(ctx, project, state)
}
