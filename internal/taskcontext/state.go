package taskcontext

import (
	"context"
	"errors"

	"github.com/thebtf/engram/internal/uci"
)

type locatorKey struct{}

// WithNativeLocator is a transport-owned input, never a tool/browser argument.
func WithNativeLocator(ctx context.Context, locator string) context.Context {
	return context.WithValue(ctx, locatorKey{}, locator)
}
func NativeLocator(ctx context.Context) string {
	value, _ := ctx.Value(locatorKey{}).(string)
	return value
}

type Failure struct{ Status, Reason string }

func (failure *Failure) Error() string   { return failure.Reason }
func Refuse(status, reason string) error { return &Failure{status, reason} }

func FromError(ctx context.Context, err error) Response {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Closed("deadline_exceeded", "DEADLINE_EXCEEDED")
	}
	var failure *Failure
	if errors.As(err, &failure) {
		return Closed(failure.Status, failure.Reason)
	}
	var closed *uci.ContextError
	if errors.As(err, &closed) {
		switch closed.Code() {
		case uci.ContextRequired:
			return Closed("context_required", "CONTEXT_REQUIRED")
		case uci.PermissionDenied:
			return Closed("forbidden", "PERMISSION_DENIED")
		case uci.RegistrationProfileUnbound:
			return Closed("not_ready", "REGISTRATION_PROFILE_UNBOUND")
		default:
			return Closed("context_required", "CONTEXT_MISMATCH")
		}
	}
	return Closed("unavailable", "SERVER_UNAVAILABLE")
}

func (response Response) Validate() error {
	invalid := errors.New("invalid task context response")
	if response.Schema != 1 {
		return invalid
	}
	switch response.Status {
	case "ok", "empty", "not_ready", "stale", "context_required", "forbidden", "unavailable", "deadline_exceeded":
	default:
		return invalid
	}
	switch response.ReasonCode {
	case "OK", "NO_MATCH", "INSTALLATION_NOT_READY", "DAEMON_NOT_READY", "OPERATION_NOT_READY", "CONTEXT_REQUIRED", "CONTEXT_MISMATCH", "PROJECT_CONTEXT_REQUIRED", "PROJECT_CONTEXT_MISMATCH", "PERMISSION_DENIED", "SOURCE_UNAVAILABLE", "VIEW_RETIRED", "CHECKOUT_OFFLINE", "INDEX_CATCHING_UP", "REGISTRATION_PROFILE_UNBOUND", "SERVER_UNAVAILABLE", "TRANSPORT_UNAVAILABLE", "EXPOSURE_UNAVAILABLE", "BUDGET_EXCEEDED", "INVALID_REQUEST", "INVALID_RESPONSE", "DEADLINE_EXCEEDED":
	default:
		return invalid
	}
	if response.Project != nil && response.Project.Validate() != nil {
		return invalid
	}
	if response.Packet == nil {
		if response.Status == "ok" {
			return invalid
		}
		return nil
	}
	if response.Status != "ok" && response.Status != "empty" && response.Status != "stale" {
		return invalid
	}
	parts := []uci.QueryResponse{response.Packet.Search}
	if response.Packet.Source != nil {
		parts = append(parts, *response.Packet.Source)
	}
	if response.Packet.Calls != nil {
		parts = append(parts, *response.Packet.Calls)
	}
	var pinned uci.QueryContextRef
	for index, part := range parts {
		if part.Validate() != nil || part.Exposure == nil || part.Contexts == nil || len(*part.Contexts) != 1 {
			return invalid
		}
		ref := (*part.Contexts)[0]
		if index == 0 {
			pinned = ref
		} else if ref.SourceID != pinned.SourceID || ref.CheckoutID != pinned.CheckoutID || ref.ViewID != pinned.ViewID || ref.Generation != pinned.Generation || ref.ProfileID != pinned.ProfileID {
			return invalid
		}
	}
	if response.Status == "ok" && (response.Packet.Source == nil || response.Packet.Source.Items == nil || len(*response.Packet.Source.Items) == 0) {
		return invalid
	}
	return nil
}
