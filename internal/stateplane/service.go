package stateplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/thebtf/engram/pkg/cognitive"
)

// FallbackReader reads an explicit filesystem/export resume packet.
type FallbackReader interface {
	ReadResumePacket(ctx context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error)
}

// Service composes the native state plane with an optional explicit fallback
// reader. Native reads remain the happy path; fallback is opt-in per request.
type Service struct {
	native   cognitive.StatePlane
	fallback FallbackReader
	now      func() time.Time
}

var _ cognitive.StatePlane = (*Service)(nil)

// NewService wraps a native state plane with explicit fallback/drift behavior.
func NewService(native cognitive.StatePlane, fallback FallbackReader) *Service {
	return &Service{
		native:   native,
		fallback: fallback,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// JSONFileFallbackReader reads a serialized ResumePacket from one known file.
type JSONFileFallbackReader struct {
	Path string
}

// ReadResumePacket reads the configured JSON fallback file.
func (r JSONFileFallbackReader) ReadResumePacket(ctx context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	select {
	case <-ctx.Done():
		return cognitive.ResumePacket{}, ctx.Err()
	default:
	}
	if r.Path == "" {
		return cognitive.ResumePacket{}, fmt.Errorf("stateplane fallback: path is required")
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return cognitive.ResumePacket{}, fmt.Errorf("stateplane fallback read %q: %w", r.Path, err)
	}
	var packet cognitive.ResumePacket
	if err := json.Unmarshal(data, &packet); err != nil {
		return cognitive.ResumePacket{}, fmt.Errorf("stateplane fallback parse %q: %w", r.Path, err)
	}
	packet.Source = cognitive.StatePacketSourceFilesystemFallback
	if packet.FallbackPath == "" {
		packet.FallbackPath = r.Path
	}
	if packet.Project == "" {
		packet.Project = request.Project
	}
	if packet.SessionID == "" {
		packet.SessionID = request.SessionID
	}
	if packet.GoalID == "" {
		packet.GoalID = request.GoalID
	}
	if packet.TaskID == "" {
		packet.TaskID = request.TaskID
	}
	if len(packet.Scopes) == 0 && packet.SessionID != "" {
		packet.Scopes = []cognitive.StateScopeKind{cognitive.StateScopeSession}
	}
	return packet, nil
}

// WriteSessionState delegates native session-state writes.
func (s *Service) WriteSessionState(ctx context.Context, sessionID string, slots cognitive.SessionStateSlots) error {
	if err := s.requireNative(); err != nil {
		return err
	}
	return s.native.WriteSessionState(ctx, sessionID, slots)
}

// WriteProjectState delegates native project-state writes.
func (s *Service) WriteProjectState(ctx context.Context, project string, state cognitive.ProjectStateRecord) error {
	if err := s.requireNative(); err != nil {
		return err
	}
	return s.native.WriteProjectState(ctx, project, state)
}

// ReadSessionState delegates native session-state reads.
func (s *Service) ReadSessionState(ctx context.Context, sessionID string) (cognitive.SessionStateSlots, error) {
	if err := s.requireNative(); err != nil {
		return cognitive.SessionStateSlots{}, err
	}
	return s.native.ReadSessionState(ctx, sessionID)
}

// ReadProjectState delegates native project-state reads.
func (s *Service) ReadProjectState(ctx context.Context, project string) (cognitive.ProjectStateRecord, error) {
	if err := s.requireNative(); err != nil {
		return cognitive.ProjectStateRecord{}, err
	}
	return s.native.ReadProjectState(ctx, project)
}

// ReadResumePacket returns native state first and consults filesystem fallback
// only when the caller explicitly opted in.
func (s *Service) ReadResumePacket(ctx context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	if err := s.requireNative(); err != nil {
		return cognitive.ResumePacket{}, err
	}

	nativePacket, nativeErr := s.native.ReadResumePacket(ctx, request)
	if !request.AllowFilesystemFallback || s.fallback == nil {
		if nativeErr != nil {
			return cognitive.ResumePacket{}, nativeErr
		}
		return nativePacket, nil
	}

	fallbackPacket, fallbackErr := s.fallback.ReadResumePacket(ctx, request)
	if nativeErr != nil {
		if fallbackErr != nil {
			return cognitive.ResumePacket{}, fmt.Errorf("stateplane read_resume: native and fallback failed: %w", errors.Join(nativeErr, fallbackErr))
		}
		fallbackPacket = s.normalizeFallbackPacket(fallbackPacket, request)
		if err := validateFallbackPacket(fallbackPacket, request); err != nil {
			return cognitive.ResumePacket{}, err
		}
		return fallbackPacket, nil
	}
	if fallbackErr != nil {
		return nativePacket, nil
	}

	fallbackPacket = s.normalizeFallbackPacket(fallbackPacket, request)
	if err := validateFallbackPacket(fallbackPacket, request); err != nil {
		return cognitive.ResumePacket{}, err
	}
	conflicts := packetConflicts(nativePacket, fallbackPacket)
	if len(conflicts) > 0 {
		return s.conflictPacket(nativePacket, fallbackPacket, conflicts), nil
	}
	if fallbackIsNewer(nativePacket, fallbackPacket) {
		return s.fallbackNewerPacket(nativePacket, fallbackPacket), nil
	}
	return nativePacket, nil
}

func (s *Service) requireNative() error {
	if s == nil || s.native == nil {
		return fmt.Errorf("stateplane: native state plane is not configured")
	}
	return nil
}

func (s *Service) normalizeFallbackPacket(packet cognitive.ResumePacket, request cognitive.ResumePacketRequest) cognitive.ResumePacket {
	now := s.clock()
	packet.Source = cognitive.StatePacketSourceFilesystemFallback
	if packet.Freshness == "" {
		packet.Freshness = cognitive.StateFreshnessUnknown
	}
	if packet.Drift.Kind == "" {
		packet.Drift.Kind = cognitive.StateDriftUnknown
	}
	if packet.Drift.CheckedAt.IsZero() {
		packet.Drift.CheckedAt = now
	}
	if packet.GeneratedAt.IsZero() {
		packet.GeneratedAt = now
	}
	if packet.Project == "" {
		packet.Project = request.Project
	}
	if packet.SessionID == "" {
		packet.SessionID = request.SessionID
	}
	if packet.GoalID == "" {
		packet.GoalID = request.GoalID
	}
	if packet.TaskID == "" {
		packet.TaskID = request.TaskID
	}
	if len(packet.Scopes) == 0 && packet.SessionID != "" {
		packet.Scopes = []cognitive.StateScopeKind{cognitive.StateScopeSession}
	}
	return packet
}

func (s *Service) conflictPacket(nativePacket, fallbackPacket cognitive.ResumePacket, conflicts []cognitive.StateConflict) cognitive.ResumePacket {
	now := s.clock()
	packet := nativePacket
	packet.Source = cognitive.StatePacketSourceConflict
	packet.Freshness = cognitive.StateFreshnessStale
	packet.FallbackPath = fallbackPacket.FallbackPath
	packet.GeneratedAt = now
	packet.Drift = cognitive.StateDrift{
		Kind:      cognitive.StateDriftConflict,
		Conflicts: conflicts,
		CheckedAt: now,
	}
	return packet
}

func (s *Service) fallbackNewerPacket(nativePacket, fallbackPacket cognitive.ResumePacket) cognitive.ResumePacket {
	now := s.clock()
	packet := nativePacket
	packet.Freshness = cognitive.StateFreshnessStale
	packet.FallbackPath = fallbackPacket.FallbackPath
	packet.Drift = cognitive.StateDrift{
		Kind:      cognitive.StateDriftFallbackNewer,
		CheckedAt: now,
	}
	return packet
}

func (s *Service) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func validateFallbackPacket(packet cognitive.ResumePacket, request cognitive.ResumePacketRequest) error {
	if packet.NextAction.Description == "" {
		return fmt.Errorf("stateplane fallback: next_action.description is required")
	}
	if packet.NextAction.Kind == "" {
		return fmt.Errorf("stateplane fallback: next_action.kind is required")
	}
	if packet.NextVerification.Description == "" {
		return fmt.Errorf("stateplane fallback: next_verification.description is required")
	}
	if packet.NextVerification.Kind == "" {
		return fmt.Errorf("stateplane fallback: next_verification.kind is required")
	}
	if request.Project != "" && packet.Project != request.Project {
		return fmt.Errorf("stateplane fallback: project does not match resume request")
	}
	if request.SessionID != "" && packet.SessionID != request.SessionID {
		return fmt.Errorf("stateplane fallback: session_id does not match resume request")
	}
	if request.GoalID != "" && packet.GoalID != request.GoalID {
		return fmt.Errorf("stateplane fallback: goal_id does not match resume request")
	}
	if request.TaskID != "" && packet.TaskID != request.TaskID {
		return fmt.Errorf("stateplane fallback: task_id does not match resume request")
	}
	return nil
}

func packetConflicts(nativePacket, fallbackPacket cognitive.ResumePacket) []cognitive.StateConflict {
	var conflicts []cognitive.StateConflict
	addConflict := func(field string, nativeValue, fallbackValue interface{}) {
		if reflect.DeepEqual(nativeValue, fallbackValue) {
			return
		}
		conflicts = append(conflicts, cognitive.StateConflict{
			Field:         field,
			NativeValue:   valueString(nativeValue),
			FallbackValue: valueString(fallbackValue),
			Resolution:    "native_retained_until_resolved",
		})
	}

	addConflict("next_action", nativePacket.NextAction, fallbackPacket.NextAction)
	addConflict("next_verification", nativePacket.NextVerification, fallbackPacket.NextVerification)
	addConflict("goal_id", nativePacket.GoalID, fallbackPacket.GoalID)
	addConflict("task_id", nativePacket.TaskID, fallbackPacket.TaskID)
	return conflicts
}

func fallbackIsNewer(nativePacket, fallbackPacket cognitive.ResumePacket) bool {
	if nativePacket.GeneratedAt.IsZero() || fallbackPacket.GeneratedAt.IsZero() {
		return false
	}
	return fallbackPacket.GeneratedAt.After(nativePacket.GeneratedAt)
}

func valueString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(data)
	}
}
