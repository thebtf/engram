package taskcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

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
	if response.Knowledge != nil {
		if len(response.Knowledge.Items) > 9 {
			return invalid
		}
		counts := map[string]int{"memory": 0, "note": 0, "rule": 0}
		for _, item := range response.Knowledge.Items {
			if _, ok := counts[item.Kind]; !ok || item.ID < 1 || item.ID > 9007199254740991 || item.Version < 0 || (item.Kind == "note" && item.Version == 0) || len(item.Content) > 4000 || len(item.Path) > 4096 || !utf8.ValidString(item.Content) || !utf8.ValidString(item.Path) || strings.TrimSpace(item.Content) == "" || (item.Kind == "note" && item.Path == "") {
				return invalid
			}
			digest := sha256.Sum256([]byte(item.Content))
			if item.ContentHash != hex.EncodeToString(digest[:]) {
				return invalid
			}
			sourceHash, err := hex.DecodeString(item.SourceHash)
			if err != nil || len(sourceHash) != sha256.Size || (!item.Excerpt && item.SourceHash != item.ContentHash) {
				return invalid
			}
			counts[item.Kind]++
		}
		for kind, state := range map[string]string{"memory": response.Knowledge.MemoryStatus, "note": response.Knowledge.NoteStatus, "rule": response.Knowledge.RuleStatus} {
			if state != "ok" && state != "empty" && state != "unavailable" && state != "deadline_exceeded" {
				return invalid
			}
			if (state == "ok") != (counts[kind] > 0) {
				return invalid
			}
		}
	}
	if response.Packet == nil {
		if response.Status == "ok" && (response.Knowledge == nil || len(response.Knowledge.Items) == 0) {
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
	if (response.CodeStatus == "ok" || (response.CodeStatus == "" && response.Status == "ok")) && (response.Packet.Source == nil || response.Packet.Source.Items == nil || len(*response.Packet.Source.Items) == 0) {
		return invalid
	}
	return nil
}
