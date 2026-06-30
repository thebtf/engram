package stateplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/thebtf/engram/pkg/cognitive"
)

// FallbackReader reads an explicit filesystem/export resume packet.
type FallbackReader interface {
	ReadResumePacket(ctx context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error)
}

type resumeReadAuditLogger interface {
	LogResumeReadAudit(ctx context.Context, request cognitive.ResumePacketRequest, packet cognitive.ResumePacket, action, result, reason string)
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
	path := strings.TrimSpace(r.Path)
	if path == "" {
		return cognitive.ResumePacket{}, fmt.Errorf("stateplane fallback: path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cognitive.ResumePacket{}, fmt.Errorf("stateplane fallback read %q: %w", path, err)
	}
	var packet cognitive.ResumePacket
	if err := json.Unmarshal(data, &packet); err != nil {
		return cognitive.ResumePacket{}, fmt.Errorf("stateplane fallback parse %q: %w", path, err)
	}
	packet.Source = cognitive.StatePacketSourceFilesystemFallback
	packet.FallbackPath = path
	if packet.Freshness == "" {
		packet.Freshness = cognitive.StateFreshnessUnknown
	}
	if packet.Drift.Kind == "" {
		packet.Drift.Kind = cognitive.StateDriftUnknown
	}
	if packet.Principal == "" {
		packet.Principal = request.Principal
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
	packet.FallbackUsed = true
	packet.EvidenceRefs = packetEvidenceRefs(packet, request, true)
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
	request = normalizeResumePacketRequest(request)
	if err := requireResumePrincipal(request); err != nil {
		return cognitive.ResumePacket{}, err
	}
	if err := validateResumePacketRequest(request); err != nil {
		return cognitive.ResumePacket{}, err
	}

	nativePacket, nativeErr := s.native.ReadResumePacket(ctx, request)
	if nativeErr == nil {
		nativePacket = s.normalizeNativePacket(nativePacket, request)
		if err := validateNativePacket(nativePacket, request); err != nil {
			return cognitive.ResumePacket{}, err
		}
	}
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
		s.auditResumeRead(ctx, request, fallbackPacket, "read_resume_fallback", "fallback_after_native_error", "explicit filesystem fallback returned after native read error")
		return fallbackPacket, nil
	}
	if fallbackErr != nil {
		s.auditResumeRead(ctx, request, nativePacket, "read_resume_fallback_error", "native_after_fallback_error", fmt.Sprintf("explicit filesystem fallback read failed; native retained: %v", fallbackErr))
		return nativePacket, nil
	}

	fallbackPacket = s.normalizeFallbackPacket(fallbackPacket, request)
	if err := validateFallbackPacket(fallbackPacket, request); err != nil {
		return cognitive.ResumePacket{}, err
	}
	conflicts := packetConflicts(nativePacket, fallbackPacket)
	if len(conflicts) > 0 {
		packet := s.conflictPacket(nativePacket, fallbackPacket, conflicts)
		s.auditResumeRead(ctx, request, packet, "read_resume_conflict", "mixed_conflict", "native and filesystem fallback disagreed; native retained until resolved")
		return packet, nil
	}
	if fallbackIsNewer(nativePacket, fallbackPacket) {
		packet := s.fallbackNewerPacket(nativePacket, fallbackPacket)
		s.auditResumeRead(ctx, request, packet, "read_resume_fallback_newer", "mixed_fallback_newer", "filesystem fallback generated_at is newer than native state_version")
		return packet, nil
	}
	return nativePacket, nil
}

func (s *Service) requireNative() error {
	if s == nil || s.native == nil {
		return fmt.Errorf("stateplane: native state plane is not configured")
	}
	return nil
}

func (s *Service) auditResumeRead(ctx context.Context, request cognitive.ResumePacketRequest, packet cognitive.ResumePacket, action, result, reason string) {
	auditor, ok := s.native.(resumeReadAuditLogger)
	if !ok || auditor == nil {
		return
	}
	auditor.LogResumeReadAudit(ctx, request, packet, action, result, reason)
}

func (s *Service) normalizeFallbackPacket(packet cognitive.ResumePacket, request cognitive.ResumePacketRequest) cognitive.ResumePacket {
	if packet.Principal == "" {
		packet.Principal = request.Principal
	}
	now := s.clock()
	packet.Source = cognitive.StatePacketSourceFilesystemFallback
	packet.FallbackPath = strings.TrimSpace(packet.FallbackPath)
	if packet.Freshness == "" {
		packet.Freshness = cognitive.StateFreshnessUnknown
	}
	if packet.Drift.Kind == "" {
		packet.Drift.Kind = cognitive.StateDriftUnknown
	}
	if packet.Drift.CheckedAt.IsZero() {
		packet.Drift.CheckedAt = now
	}
	packet.Drift = normalizeStateDriftConflicts(packet.Drift)
	if strings.TrimSpace(packet.StateVersion) == "" {
		if !packet.GeneratedAt.IsZero() {
			packet.StateVersion = fallbackStateVersion(packet.GeneratedAt)
		}
	} else {
		packet.StateVersion = strings.TrimSpace(packet.StateVersion)
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
	if strings.TrimSpace(packet.PacketID) == "" {
		packet.PacketID = fallbackResumePacketID(packet, request)
	} else {
		packet.PacketID = strings.TrimSpace(packet.PacketID)
	}
	if len(packet.Scopes) == 0 && packet.SessionID != "" {
		packet.Scopes = []cognitive.StateScopeKind{cognitive.StateScopeSession}
	}
	packet.Scopes = canonicalizeResumeScopes(packet.Scopes)
	packet.FallbackUsed = true
	packet.EvidenceRefs = packetEvidenceRefs(packet, request, true)
	return packet
}

func fallbackStateVersion(generatedAt time.Time) string {
	if generatedAt.IsZero() {
		return "unknown"
	}
	return generatedAt.UTC().Format(time.RFC3339Nano)
}

func fallbackResumePacketID(packet cognitive.ResumePacket, request cognitive.ResumePacketRequest) string {
	identity := struct {
		Source           cognitive.StatePacketSource `json:"source"`
		FallbackPath     string                      `json:"fallback_path"`
		PacketProject    string                      `json:"packet_project"`
		PacketPrincipal  string                      `json:"packet_principal"`
		PacketSessionID  string                      `json:"packet_session_id"`
		PacketGoalID     string                      `json:"packet_goal_id"`
		PacketTaskID     string                      `json:"packet_task_id"`
		RequestProject   string                      `json:"request_project"`
		RequestPrincipal string                      `json:"request_principal"`
		RequestSessionID string                      `json:"request_session_id"`
		RequestGoalID    string                      `json:"request_goal_id"`
		RequestTaskID    string                      `json:"request_task_id"`
		StateVersion     string                      `json:"state_version"`
		GeneratedAt      string                      `json:"generated_at"`
	}{
		Source:           packet.Source,
		FallbackPath:     strings.TrimSpace(packet.FallbackPath),
		PacketProject:    strings.TrimSpace(packet.Project),
		PacketPrincipal:  strings.TrimSpace(packet.Principal),
		PacketSessionID:  strings.TrimSpace(packet.SessionID),
		PacketGoalID:     strings.TrimSpace(packet.GoalID),
		PacketTaskID:     strings.TrimSpace(packet.TaskID),
		RequestProject:   strings.TrimSpace(request.Project),
		RequestPrincipal: strings.TrimSpace(request.Principal),
		RequestSessionID: strings.TrimSpace(request.SessionID),
		RequestGoalID:    strings.TrimSpace(request.GoalID),
		RequestTaskID:    strings.TrimSpace(request.TaskID),
		StateVersion:     strings.TrimSpace(packet.StateVersion),
		GeneratedAt:      fallbackStateVersion(packet.GeneratedAt),
	}
	data, _ := json.Marshal(identity)
	sum := sha256.Sum256(data)
	return "resume:" + hex.EncodeToString(sum[:])
}

type resumePacketIdentityMaterial struct {
	PacketID     string                      `json:"packet_id"`
	Project      string                      `json:"project"`
	Principal    string                      `json:"principal"`
	SessionID    string                      `json:"session_id"`
	GoalID       string                      `json:"goal_id"`
	TaskID       string                      `json:"task_id"`
	StateVersion string                      `json:"state_version"`
	Source       cognitive.StatePacketSource `json:"source"`
	Freshness    cognitive.StateFreshness    `json:"freshness"`
	FallbackPath string                      `json:"fallback_path"`
	EvidenceRefs []string                    `json:"evidence_refs"`
}

func mixedResumePacketID(nativePacket, fallbackPacket, packet cognitive.ResumePacket) string {
	identity := struct {
		Kind           string                       `json:"kind"`
		Source         cognitive.StatePacketSource  `json:"source"`
		Freshness      cognitive.StateFreshness     `json:"freshness"`
		FallbackUsed   bool                         `json:"fallback_used"`
		FallbackPath   string                       `json:"fallback_path"`
		Project        string                       `json:"project"`
		Principal      string                       `json:"principal"`
		SessionID      string                       `json:"session_id"`
		GoalID         string                       `json:"goal_id"`
		TaskID         string                       `json:"task_id"`
		StateVersion   string                       `json:"state_version"`
		DriftKind      cognitive.StateDriftKind     `json:"drift_kind"`
		DriftConflicts []cognitive.StateConflict    `json:"drift_conflicts,omitempty"`
		EvidenceRefs   []string                     `json:"evidence_refs"`
		NativePacket   resumePacketIdentityMaterial `json:"native_packet"`
		FallbackPacket resumePacketIdentityMaterial `json:"fallback_packet"`
	}{
		Kind:           "mixed_resume_packet",
		Source:         packet.Source,
		Freshness:      packet.Freshness,
		FallbackUsed:   packet.FallbackUsed,
		FallbackPath:   strings.TrimSpace(packet.FallbackPath),
		Project:        strings.TrimSpace(packet.Project),
		Principal:      strings.TrimSpace(packet.Principal),
		SessionID:      strings.TrimSpace(packet.SessionID),
		GoalID:         strings.TrimSpace(packet.GoalID),
		TaskID:         strings.TrimSpace(packet.TaskID),
		StateVersion:   strings.TrimSpace(packet.StateVersion),
		DriftKind:      packet.Drift.Kind,
		DriftConflicts: packet.Drift.Conflicts,
		EvidenceRefs:   normalizedEvidenceRefs(packet.EvidenceRefs),
		NativePacket:   resumePacketIdentity(nativePacket),
		FallbackPacket: resumePacketIdentity(fallbackPacket),
	}
	data, _ := json.Marshal(identity)
	sum := sha256.Sum256(data)
	return "resume:" + hex.EncodeToString(sum[:])
}

func resumePacketIdentity(packet cognitive.ResumePacket) resumePacketIdentityMaterial {
	return resumePacketIdentityMaterial{
		PacketID:     strings.TrimSpace(packet.PacketID),
		Project:      strings.TrimSpace(packet.Project),
		Principal:    strings.TrimSpace(packet.Principal),
		SessionID:    strings.TrimSpace(packet.SessionID),
		GoalID:       strings.TrimSpace(packet.GoalID),
		TaskID:       strings.TrimSpace(packet.TaskID),
		StateVersion: strings.TrimSpace(packet.StateVersion),
		Source:       packet.Source,
		Freshness:    packet.Freshness,
		FallbackPath: strings.TrimSpace(packet.FallbackPath),
		EvidenceRefs: normalizedEvidenceRefs(packet.EvidenceRefs),
	}
}

func normalizedEvidenceRefs(refs []string) []string {
	normalized := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		normalized = append(normalized, ref)
	}
	return normalized
}

func (s *Service) normalizeNativePacket(packet cognitive.ResumePacket, request cognitive.ResumePacketRequest) cognitive.ResumePacket {
	now := s.clock()
	if packet.Freshness == "" {
		packet.Freshness = cognitive.StateFreshnessFresh
	}
	if packet.Drift.Kind == "" {
		packet.Drift.Kind = cognitive.StateDriftNone
	}
	if packet.Drift.CheckedAt.IsZero() {
		packet.Drift.CheckedAt = now
	}
	packet.Drift = normalizeStateDriftConflicts(packet.Drift)
	if packet.Project == "" {
		packet.Project = request.Project
	}
	if packet.Principal == "" {
		packet.Principal = request.Principal
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
	packet.Scopes = canonicalizeResumeScopes(packet.Scopes)
	return packet
}

func (s *Service) conflictPacket(nativePacket, fallbackPacket cognitive.ResumePacket, conflicts []cognitive.StateConflict) cognitive.ResumePacket {
	now := s.clock()
	packet := nativePacket
	packet.Source = cognitive.StatePacketSourceMixed
	packet.Freshness = cognitive.StateFreshnessStale
	packet.FallbackUsed = true
	packet.FallbackPath = fallbackPacket.FallbackPath
	packet.GeneratedAt = now
	packet.EvidenceRefs = combinedEvidenceRefs(nativePacket.EvidenceRefs, fallbackPacket.EvidenceRefs)
	packet.Drift = cognitive.StateDrift{
		Kind:      cognitive.StateDriftConflict,
		Conflicts: conflicts,
		CheckedAt: now,
	}
	packet.PacketID = mixedResumePacketID(nativePacket, fallbackPacket, packet)
	return packet
}

func (s *Service) fallbackNewerPacket(nativePacket, fallbackPacket cognitive.ResumePacket) cognitive.ResumePacket {
	now := s.clock()
	packet := nativePacket
	packet.Freshness = cognitive.StateFreshnessStale
	packet.Source = cognitive.StatePacketSourceMixed
	packet.FallbackUsed = true
	packet.FallbackPath = fallbackPacket.FallbackPath
	packet.EvidenceRefs = combinedEvidenceRefs(nativePacket.EvidenceRefs, fallbackPacket.EvidenceRefs)
	packet.GeneratedAt = now
	packet.Drift = cognitive.StateDrift{
		Kind:      cognitive.StateDriftFallbackNewer,
		Conflicts: []cognitive.StateConflict{},
		CheckedAt: now,
	}
	packet.PacketID = mixedResumePacketID(nativePacket, fallbackPacket, packet)
	return packet
}

func (s *Service) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func normalizeResumePacketRequest(request cognitive.ResumePacketRequest) cognitive.ResumePacketRequest {
	request.Project = strings.TrimSpace(request.Project)
	request.Principal = strings.TrimSpace(request.Principal)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.GoalID = strings.TrimSpace(request.GoalID)
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.Scopes = canonicalizeResumeScopes(request.Scopes)
	return request
}

func canonicalizeResumeScopes(scopes []cognitive.StateScopeKind) []cognitive.StateScopeKind {
	if len(scopes) == 0 {
		return nil
	}
	seen := make(map[cognitive.StateScopeKind]struct{}, len(scopes))
	for _, scope := range scopes {
		seen[cognitive.StateScopeKind(strings.TrimSpace(string(scope)))] = struct{}{}
	}
	ordered := make([]cognitive.StateScopeKind, 0, len(seen))
	for _, scope := range []cognitive.StateScopeKind{
		cognitive.StateScopeSession,
		cognitive.StateScopeProject,
		cognitive.StateScopeGoal,
		cognitive.StateScopeTask,
	} {
		if _, ok := seen[scope]; ok {
			ordered = append(ordered, scope)
			delete(seen, scope)
		}
	}
	if len(seen) == 0 {
		return ordered
	}
	extra := make([]string, 0, len(seen))
	for scope := range seen {
		extra = append(extra, string(scope))
	}
	sort.Strings(extra)
	for _, scope := range extra {
		ordered = append(ordered, cognitive.StateScopeKind(scope))
	}
	return ordered
}

func normalizeStateDriftConflicts(drift cognitive.StateDrift) cognitive.StateDrift {
	if drift.Kind != cognitive.StateDriftConflict && drift.Conflicts == nil {
		drift.Conflicts = []cognitive.StateConflict{}
	}
	return drift
}

func requireResumePrincipal(request cognitive.ResumePacketRequest) error {
	if request.Principal == "" {
		return fmt.Errorf("stateplane read_resume: principal is required")
	}
	return nil
}

func validateResumePacketRequest(request cognitive.ResumePacketRequest) error {
	if len(request.Scopes) == 0 {
		return fmt.Errorf("stateplane read_resume: scopes is required")
	}
	for _, scope := range request.Scopes {
		switch scope {
		case cognitive.StateScopeSession:
			if request.SessionID == "" {
				return fmt.Errorf("stateplane read_resume: session_id is required for session scope")
			}
		case cognitive.StateScopeProject:
			if request.Project == "" {
				return fmt.Errorf("stateplane read_resume: project is required for project scope")
			}
		case cognitive.StateScopeGoal:
			if request.GoalID == "" {
				return fmt.Errorf("stateplane read_resume: goal_id is required for goal scope")
			}
		case cognitive.StateScopeTask:
			if request.TaskID == "" {
				return fmt.Errorf("stateplane read_resume: task_id is required for task scope")
			}
		default:
			return fmt.Errorf("stateplane read_resume: unsupported scope %q", scope)
		}
	}
	return nil
}

func validatePacketPrincipal(prefix string, packet cognitive.ResumePacket, request cognitive.ResumePacketRequest) error {
	if packet.Principal == "" {
		return fmt.Errorf("%s: principal is required", prefix)
	}
	if request.Principal != "" && packet.Principal != request.Principal {
		return fmt.Errorf("%s: principal does not match resume request", prefix)
	}
	return nil
}

func validateDeterministicResumePacketFields(prefix string, packet cognitive.ResumePacket) error {
	if strings.TrimSpace(packet.PacketID) == "" {
		return fmt.Errorf("%s: packet_id is required", prefix)
	}
	if strings.TrimSpace(packet.StateVersion) == "" {
		return fmt.Errorf("%s: state_version is required", prefix)
	}
	if packet.GeneratedAt.IsZero() {
		return fmt.Errorf("%s: generated_at is required", prefix)
	}
	if len(normalizedEvidenceRefs(packet.EvidenceRefs)) == 0 {
		return fmt.Errorf("%s: evidence_refs is required", prefix)
	}
	if err := validatePacketScopes(prefix, packet.Scopes); err != nil {
		return err
	}
	if err := validateStateAction(prefix, packet.NextAction); err != nil {
		return err
	}
	if err := validateStateVerification(prefix, packet.NextVerification); err != nil {
		return err
	}
	return nil
}

func validatePacketScopes(prefix string, scopes []cognitive.StateScopeKind) error {
	if len(scopes) == 0 {
		return fmt.Errorf("%s: scopes is required", prefix)
	}
	for _, scope := range scopes {
		switch cognitive.StateScopeKind(strings.TrimSpace(string(scope))) {
		case cognitive.StateScopeSession, cognitive.StateScopeProject, cognitive.StateScopeGoal, cognitive.StateScopeTask:
		default:
			return fmt.Errorf("%s: unsupported scope %q", prefix, scope)
		}
	}
	return nil
}

func validateStateAction(prefix string, action cognitive.StateAction) error {
	kind := cognitive.StateActionKind(strings.TrimSpace(string(action.Kind)))
	if kind == "" {
		return fmt.Errorf("%s: next_action.kind is required", prefix)
	}
	switch kind {
	case cognitive.StateActionCommand, cognitive.StateActionInstruction, cognitive.StateActionReviewGate:
	default:
		return fmt.Errorf("%s: next_action.kind %q is not a recognized action kind", prefix, kind)
	}
	if strings.TrimSpace(action.Description) == "" {
		return fmt.Errorf("%s: next_action.description is required", prefix)
	}
	if kind == cognitive.StateActionCommand && strings.TrimSpace(action.Command) == "" {
		return fmt.Errorf("%s: next_action.command is required for command kind", prefix)
	}
	return nil
}

func validateStateVerification(prefix string, verification cognitive.StateVerification) error {
	kind := cognitive.StateVerificationKind(strings.TrimSpace(string(verification.Kind)))
	if kind == "" {
		return fmt.Errorf("%s: next_verification.kind is required", prefix)
	}
	switch kind {
	case cognitive.StateVerificationCommand, cognitive.StateVerificationArtifact, cognitive.StateVerificationManual:
	default:
		return fmt.Errorf("%s: next_verification.kind %q is not a recognized verification kind", prefix, kind)
	}
	if strings.TrimSpace(verification.Description) == "" {
		return fmt.Errorf("%s: next_verification.description is required", prefix)
	}
	if kind == cognitive.StateVerificationCommand && strings.TrimSpace(verification.Command) == "" {
		return fmt.Errorf("%s: next_verification.command is required for command kind", prefix)
	}
	return nil
}

func validateNativePacket(packet cognitive.ResumePacket, request cognitive.ResumePacketRequest) error {
	if err := validatePacketPrincipal("stateplane native", packet, request); err != nil {
		return err
	}
	if request.Project != "" && packet.Project != request.Project {
		return fmt.Errorf("stateplane native: project does not match resume request")
	}
	if request.SessionID != "" && packet.SessionID != request.SessionID {
		return fmt.Errorf("stateplane native: session_id does not match resume request")
	}
	if request.GoalID != "" && packet.GoalID != request.GoalID {
		return fmt.Errorf("stateplane native: goal_id does not match resume request")
	}
	if request.TaskID != "" && packet.TaskID != request.TaskID {
		return fmt.Errorf("stateplane native: task_id does not match resume request")
	}
	if packet.Source != cognitive.StatePacketSourceNative {
		return fmt.Errorf("stateplane native: source %q is not native", packet.Source)
	}
	if err := validateDeterministicResumePacketFields("stateplane native", packet); err != nil {
		return err
	}
	if packet.FallbackUsed {
		return fmt.Errorf("stateplane native: fallback_used must be false")
	}
	if strings.TrimSpace(packet.FallbackPath) != "" {
		return fmt.Errorf("stateplane native: fallback_path must be empty")
	}
	for _, ref := range packet.EvidenceRefs {
		if strings.HasPrefix(strings.TrimSpace(ref), "filesystem_fallback:") {
			return fmt.Errorf("stateplane native: filesystem fallback evidence is not allowed")
		}
	}
	return nil
}

func validateFallbackPacket(packet cognitive.ResumePacket, request cognitive.ResumePacketRequest) error {
	if err := validatePacketPrincipal("stateplane fallback", packet, request); err != nil {
		return err
	}
	if packet.Source != cognitive.StatePacketSourceFilesystemFallback {
		return fmt.Errorf("stateplane fallback: source must be filesystem_fallback")
	}
	fallbackPath := strings.TrimSpace(packet.FallbackPath)
	if fallbackPath == "" {
		return fmt.Errorf("stateplane fallback: fallback_path is required")
	}
	if len(packet.EvidenceRefs) == 0 {
		return fmt.Errorf("stateplane fallback: evidence_refs is required")
	}
	if !evidenceRefsContain(packet.EvidenceRefs, "filesystem_fallback:"+fallbackPath) {
		return fmt.Errorf("stateplane fallback: evidence_refs must include filesystem fallback path")
	}
	if !packet.FallbackUsed {
		return fmt.Errorf("stateplane fallback: fallback_used must be true")
	}
	if packet.GeneratedAt.IsZero() && strings.TrimSpace(packet.StateVersion) == "" {
		return fmt.Errorf("stateplane fallback: generated_at or state_version is required")
	}
	if err := validateStateAction("stateplane fallback", packet.NextAction); err != nil {
		return err
	}
	if err := validateStateVerification("stateplane fallback", packet.NextVerification); err != nil {
		return err
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
	if fallbackPacket.GeneratedAt.IsZero() {
		return false
	}
	nativeStateTime, ok := nativePersistedStateTime(nativePacket)
	if !ok || nativeStateTime.IsZero() {
		return false
	}
	return fallbackPacket.GeneratedAt.After(nativeStateTime)
}

func nativePersistedStateTime(packet cognitive.ResumePacket) (time.Time, bool) {
	if stateVersion := strings.TrimSpace(packet.StateVersion); stateVersion != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, stateVersion); err == nil {
			return parsed, true
		}
	}
	return packet.GeneratedAt, !packet.GeneratedAt.IsZero()
}

func packetEvidenceRefs(packet cognitive.ResumePacket, request cognitive.ResumePacketRequest, fallback bool) []string {
	refs := make([]string, 0, len(packet.EvidenceRefs)+2)
	for _, ref := range packet.EvidenceRefs {
		refs = appendUniqueEvidenceRef(refs, ref)
	}
	if fallback {
		if packet.FallbackPath != "" {
			refs = appendUniqueEvidenceRef(refs, "filesystem_fallback:"+packet.FallbackPath)
		}
		if len(refs) == 0 {
			sessionID := packet.SessionID
			if sessionID == "" {
				sessionID = request.SessionID
			}
			if sessionID != "" {
				refs = appendUniqueEvidenceRef(refs, "resume_packet:fallback:"+sessionID)
			}
		}
	} else if len(refs) == 0 {
		sessionID := packet.SessionID
		if sessionID == "" {
			sessionID = request.SessionID
		}
		if sessionID != "" && packet.StateVersion != "" {
			refs = appendUniqueEvidenceRef(refs, fmt.Sprintf("agent_session_state:%s@%s", sessionID, packet.StateVersion))
		} else if sessionID != "" {
			refs = appendUniqueEvidenceRef(refs, "resume_packet:native:"+sessionID)
		}
	}
	if len(refs) == 0 {
		if fallback {
			return []string{"resume_packet:fallback"}
		}
		return []string{"resume_packet:native"}
	}
	return refs
}

func combinedEvidenceRefs(groups ...[]string) []string {
	refs := make([]string, 0)
	for _, group := range groups {
		for _, ref := range group {
			refs = appendUniqueEvidenceRef(refs, ref)
		}
	}
	return refs
}

func appendUniqueEvidenceRef(refs []string, ref string) []string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return refs
	}
	for _, existing := range refs {
		if existing == ref {
			return refs
		}
	}
	return append(refs, ref)
}

func evidenceRefsContain(refs []string, expected string) bool {
	expected = strings.TrimSpace(expected)
	for _, ref := range refs {
		if strings.TrimSpace(ref) == expected {
			return true
		}
	}
	return false
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
