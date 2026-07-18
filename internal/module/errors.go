package module

import "time"

// ModuleError is the structured error type returned by [ToolProvider.HandleTool]
// when a module-level error occurs. It is returned as the MCP "result" field
// (not as a JSON-RPC protocol error) so that AI agent clients can reason about
// the structured error code and decide on retry strategy rather than having
// the transport layer retry blindly.
//
// The Code field contains a stable enum value — see the constructor functions
// below for the complete set. Callers MUST NOT create ModuleError values with
// ad-hoc codes; use the provided constructors.
//
// See design.md Section 3.4 for the three-layer error taxonomy and the
// rationale for result-level vs protocol-level errors.
type ModuleError struct {
	// Code is a stable, machine-readable error identifier. Never changes
	// between daemon versions for a given semantic category.
	Code string `json:"code"`
	// Message is a human-readable description of the error. MAY change
	// between versions; callers MUST NOT parse it programmatically.
	Message string `json:"message"`
	// Details carries optional structured context (e.g. upstream name, project
	// ID). Omitted from JSON when empty.
	Details map[string]any `json:"details,omitempty"`
	// RetryAfter is an optional hint to the caller about when to retry.
	// nil means the module has no retry hint. Omitted from JSON when nil.
	RetryAfter *time.Duration `json:"retry_after,omitempty"`
}

// Error implements the error interface. Returns "<code>: <message>".
func (e *ModuleError) Error() string { return e.Code + ": " + e.Message }

// RequiredProxyToolsError marks a dynamic tool-discovery failure that must be
// surfaced to the client because the proxy backend is configured and
// authoritative. Plain ProxyTools errors remain optional/offline failures and
// may fall back to static tools.
type RequiredProxyToolsError struct {
	Cause error
}

func (e *RequiredProxyToolsError) Error() string {
	if e == nil || e.Cause == nil {
		return "required proxy tool discovery failed"
	}
	return "required proxy tool discovery failed: " + e.Cause.Error()
}

func (e *RequiredProxyToolsError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ErrNotReady returns a ModuleError indicating the module has not yet
// completed initialisation or its backend is not reachable. retryAfter
// provides a suggested wait before the next attempt.
func ErrNotReady(reason string, retryAfter time.Duration) *ModuleError {
	return &ModuleError{
		Code:       "not_ready",
		Message:    reason,
		RetryAfter: &retryAfter,
	}
}

// ErrProjectNotFound returns a ModuleError indicating that the requested
// project could not be resolved. The projectID is included in Details for
// structured log correlation.
func ErrProjectNotFound(projectID string) *ModuleError {
	return &ModuleError{
		Code:    "project_not_found",
		Message: "project not found: " + projectID,
		Details: map[string]any{"project_id": projectID},
	}
}

// ErrToolDisabled returns a ModuleError indicating the named tool has been
// disabled, either by configuration or because a required capability is
// absent. reason explains why the tool is unavailable.
func ErrToolDisabled(name string, reason string) *ModuleError {
	return &ModuleError{
		Code:    "tool_disabled",
		Message: "tool disabled: " + reason,
		Details: map[string]any{"tool": name},
	}
}

// ErrResourceExhausted returns a ModuleError indicating a resource limit
// (connection pool, rate limit, quota) has been reached. resource names the
// exhausted resource for structured log correlation.
func ErrResourceExhausted(resource string) *ModuleError {
	return &ModuleError{
		Code:    "resource_exhausted",
		Message: "resource exhausted: " + resource,
		Details: map[string]any{"resource": resource},
	}
}

// ErrUpstreamUnavailable returns a ModuleError indicating that a required
// upstream service is unreachable. upstream names the service; cause is the
// underlying Go error, recorded in Details for log correlation.
func ErrUpstreamUnavailable(upstream string, cause error) *ModuleError {
	d := map[string]any{"upstream": upstream}
	if cause != nil {
		d["cause"] = cause.Error()
	}
	return &ModuleError{
		Code:    "upstream_unavailable",
		Message: "upstream unavailable: " + upstream,
		Details: d,
	}
}

// ErrTimeout returns a ModuleError indicating an operation exceeded its
// wall-clock deadline. wallClock is the duration that was exceeded, included
// in Details for structured log correlation.
func ErrTimeout(wallClock time.Duration) *ModuleError {
	return &ModuleError{
		Code:    "timeout",
		Message: "operation timed out after " + wallClock.String(),
		Details: map[string]any{"wall_clock": wallClock.String()},
	}
}

// ErrPreconditionFailed returns a ModuleError indicating that a required
// precondition was not met before the operation could proceed. reason
// describes the failed precondition; details carries optional structured
// context (e.g. expected vs actual values).
func ErrPreconditionFailed(reason string, details map[string]any) *ModuleError {
	return &ModuleError{
		Code:    "precondition_failed",
		Message: "precondition failed: " + reason,
		Details: details,
	}
}
