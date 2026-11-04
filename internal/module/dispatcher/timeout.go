package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/thebtf/engram/internal/module"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// defaultToolTimeout is the hard cap applied by the dispatcher to every
// HandleTool call. Per FR-14 and design.md Section 4.7.
const defaultToolTimeout = 30 * time.Second

// dispatcherTimeoutError is the sentinel returned when a deadline expires and
// the provider does not report a preserved delivery outcome.
//
// content.go handleToolsCall detects this sentinel BEFORE the generic
// ModuleError check and maps it to JSON-RPC -32603 with timeout details, per
// the spec.md "dispatcher 30-second hard cap" edge case:
//
//	"A module's HandleTool exceeds the 30-second dispatcher cap — dispatcher
//	cancels the session context, module observes cancellation via ctx.Done(),
//	returns ctx.Err(), dispatcher sends -32603 internal error with timeout
//	details; other sessions are unaffected."
//
// A provider's CAPABILITY_NOT_SENT or OUTCOME_UNKNOWN ModuleError remains a
// result-level error even after a deadline. Other errors, including ErrTimeout,
// are mapped to this sentinel when the deadline has expired.
type dispatcherTimeoutError struct {
	wall time.Duration
}

// Error implements the error interface.
func (e *dispatcherTimeoutError) Error() string {
	return fmt.Sprintf("dispatcher %s cap exceeded — HandleTool must complete within 1 s per FR-13", e.wall)
}

// callToolWithTimeout wraps a module.ToolProvider.HandleTool call under a
// 30 s context timeout with panic recovery.
//
// toolName and projectID are only used for logging context; they are not
// passed through to HandleTool.
//
// Returns (result, nil) on success.
// Returns (nil, *dispatcherTimeoutError) for a deadline unless the provider
// reports CAPABILITY_NOT_SENT or OUTCOME_UNKNOWN; content.go maps the sentinel
// to -32603. Preserved delivery outcomes and other ModuleErrors returned before
// the deadline remain result-level errors with isError:true per FR-12.
// Returns (nil, panicErr) if HandleTool panics — panic is recovered.
//
// Design reference: FR-14 (30 s hard cap) and FR-15 (panic isolation).
func callToolWithTimeout(
	ctx context.Context,
	provider module.ToolProvider,
	p muxcore.ProjectContext,
	name string,
	args json.RawMessage,
	toolName string,
	projectID string,
	logger *slog.Logger,
) (result json.RawMessage, outErr error) {
	return callWithTimeout(ctx, p, toolName, projectID, logger, func(tctx context.Context) (json.RawMessage, error) {
		return provider.HandleTool(tctx, p, name, args)
	})
}

// callProxyToolWithTimeout wraps a module.ProxyToolProvider.ProxyHandleTool
// call under the same 30 s cap + panic recovery contract as the static
// ToolProvider path. Kept as a separate function so the two paths remain
// greppable and the dispatcher_test coverage can exercise them independently.
//
// Design reference: FR-11a (ProxyToolProvider) and design.md Section 5.2.
func callProxyToolWithTimeout(
	ctx context.Context,
	proxy module.ProxyToolProvider,
	p muxcore.ProjectContext,
	name string,
	args json.RawMessage,
	toolName string,
	projectID string,
	logger *slog.Logger,
) (result json.RawMessage, outErr error) {
	return callWithTimeout(ctx, p, toolName, projectID, logger, func(tctx context.Context) (json.RawMessage, error) {
		return proxy.ProxyHandleTool(tctx, p, name, args)
	})
}

// callWithTimeout is the shared 30 s cap + panic recovery + dispatcherTimeoutError
// sentinel injection used by both the static and proxy tool-call paths.
//
// fn MUST NOT capture tctx as a long-lived reference — tctx is cancelled by
// the defer below. fn receives tctx as an argument so it can pass it straight
// through to the module's HandleTool / ProxyHandleTool.
func callWithTimeout(
	ctx context.Context,
	p muxcore.ProjectContext,
	toolName string,
	projectID string,
	logger *slog.Logger,
	fn func(tctx context.Context) (json.RawMessage, error),
) (result json.RawMessage, outErr error) {
	tctx, cancel := context.WithTimeout(ctx, defaultToolTimeout)
	defer cancel()

	// Panic recovery: deferred before the actual call so it wraps the call.
	defer recoverHandleTool(toolName, p.ID, logger, &outErr)

	result, outErr = fn(tctx)

	// Preserve the provider's explicit delivery outcome even when the
	// deadline expires. For all other errors retain the timeout contract.
	if outErr != nil && tctx.Err() == context.DeadlineExceeded {
		if moduleErr, ok := outErr.(*module.ModuleError); !ok || (moduleErr.Code != "CAPABILITY_NOT_SENT" && moduleErr.Code != "OUTCOME_UNKNOWN") {
			outErr = &dispatcherTimeoutError{wall: defaultToolTimeout}
		}
	}
	return result, outErr
}
