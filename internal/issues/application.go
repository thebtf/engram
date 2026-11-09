// Package issues owns admission and commands shared by issue transports. It does
// not activate or replace the legacy writers; adapters cut over separately.
package issues

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/issues/contract"
	"github.com/thebtf/engram/internal/redaction"
)

type Error = contract.Error

func Failure(code string) *Error { return contract.Failure(code) }

// Actor is the immutable projection of the existing authenticated identity.
// The same leaf sampling seam is used by application and locked bulk writers.
type Actor = contract.Actor

type ActorDescriptor = contract.ActorDescriptor

func CaptureActor(ctx context.Context) (Actor, error) {
	return contract.CaptureActor(ctx)
}

// Scope preserves project-free reads when ReadProject is empty. A nonempty
// project narrows the existing canonical role reader; it never grants an ACL.
type (
	Scope           = contract.Scope
	CreateCommand   = contract.CreateCommand
	MutationCommand = contract.MutationCommand
	Result          = contract.Result
	Persistence     = contract.Persistence
)

type Application struct {
	store Persistence
	rules []redaction.CompiledRule
}

// New receives the existing startup-compiled rules, not a new configuration path.
func New(store Persistence, rules []redaction.CompiledRule) *Application {
	return &Application{store: store, rules: append([]redaction.CompiledRule(nil), rules...)}
}

func (a *Application) Create(ctx context.Context, scope Scope, raw json.RawMessage) (Result, error) {
	actor, err := CaptureActor(ctx)
	if err != nil {
		return Result{}, err
	}
	if !actor.CanWrite() {
		return Result{}, Failure("FORBIDDEN")
	}
	if err := a.store.AdmitTaskScope(ctx, actor, scope); err != nil {
		return Result{}, err
	}
	var command CreateCommand
	received, err := decodeObject(raw, &command, a.rules)
	if err != nil {
		return Result{}, err
	}
	command.TransportSession = contract.TransportSession(ctx)
	if command.SourceProject != "" && command.SourceProject != scope.SourceProject {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	command.SourceProject = scope.SourceProject
	if _, present := received["target_project"]; !present {
		command.TargetProject = scope.SourceProject
	} else if command.TargetProject == "" {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	command.Type = strings.ToLower(strings.TrimSpace(command.Type))
	if command.Type == "" {
		command.Type = "task"
	}
	if command.Priority == "" {
		command.Priority = "medium"
	}
	if command.Title == "" || !validType(command.Type) || !validPriority(command.Priority) {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	if !validLabel(command.OwnerLabel) || !validLabel(command.RecipientLabel) {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	if command.RequestKey != nil {
		key := *command.RequestKey
		if key == "" || !utf8.ValidString(key) || strings.ContainsRune(key, 0) {
			return Result{}, Failure("INVALID_ARGUMENT")
		}
		if len(key) > 512 {
			return Result{}, Failure("PAYLOAD_TOO_LARGE")
		}
	}
	if command.Metadata == nil {
		command.Metadata = json.RawMessage(`{}`)
	}
	if err := validateMetadata(command.Metadata); err != nil {
		return Result{}, err
	}
	command.Labels = NormalizeLabels(command.Labels)
	return a.store.CreateTaskIssue(ctx, actor, scope, command)
}

func (a *Application) Mutate(ctx context.Context, scope Scope, id int64, raw json.RawMessage) (Result, error) {
	actor, err := CaptureActor(ctx)
	if err != nil {
		return Result{}, err
	}
	if !actor.CanWrite() {
		return Result{}, Failure("FORBIDDEN")
	}
	if scope.SourceProject != "" {
		if err := a.store.AdmitTaskScope(ctx, actor, scope); err != nil {
			return Result{}, err
		}
	}
	var command MutationCommand
	if _, err := decodeObject(raw, &command, a.rules); err != nil {
		return Result{}, err
	}
	command.TransportSession = contract.TransportSession(ctx)
	if id <= 0 || (command.SourceProject != "" && command.SourceProject != scope.SourceProject) {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	command.SourceProject = scope.SourceProject
	if !command.Material() && command.Comment == nil {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	if command.Comment == nil && command.CommentMetadata != nil {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	if command.Comment != nil && *command.Comment == "" {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	if command.Title != nil && *command.Title == "" {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	if command.Type != nil {
		v := strings.ToLower(strings.TrimSpace(*command.Type))
		command.Type = &v
		if !validType(v) {
			return Result{}, Failure("INVALID_ARGUMENT")
		}
	}
	if command.Priority != nil && !validPriority(*command.Priority) {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	if command.Status != nil && !validStatus(*command.Status) {
		return Result{}, Failure("INVALID_ARGUMENT")
	}
	for _, metadata := range []json.RawMessage{command.Metadata, command.CommentMetadata} {
		if metadata != nil {
			if err := validateMetadata(metadata); err != nil {
				return Result{}, err
			}
		}
	}
	for _, label := range []json.RawMessage{command.OwnerLabel, command.RecipientLabel} {
		if label != nil {
			var value *string
			if json.Unmarshal(label, &value) != nil || !validLabel(value) {
				return Result{}, Failure("INVALID_ARGUMENT")
			}
		}
	}
	if command.ExpectedRevision != nil {
		if _, err := ParseRevision(*command.ExpectedRevision); err != nil {
			return Result{}, err
		}
	}
	if command.Labels != nil {
		labels := NormalizeLabels(*command.Labels)
		command.Labels = &labels
	}
	if command.CommentMetadata == nil {
		command.CommentMetadata = json.RawMessage(`{}`)
	}
	return a.store.MutateTaskIssue(ctx, actor, scope, id, command)
}

func ParseRevision(value string) (int64, error) { return contract.ParseRevision(value) }
func validLabel(value *string) bool             { return value == nil || *value != "" }
func validType(value string) bool {
	return value == "bug" || value == "feature" || value == "improvement" || value == "task"
}

func validPriority(value string) bool {
	return value == "critical" || value == "high" || value == "medium" || value == "low"
}

func validStatus(value string) bool {
	return value == "open" || value == "acknowledged" || value == "resolved" || value == "reopened" || value == "closed" || value == "rejected"
}
