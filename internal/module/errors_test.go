package module

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestErrNotReady verifies constructor code, non-nil return, and RetryAfter presence.
func TestErrNotReady(t *testing.T) {
	d := 5 * time.Second
	e := ErrNotReady("backend initialising", d)
	require.NotNil(t, e)
	assert.Equal(t, "not_ready", e.Code)
	assert.Contains(t, e.Message, "backend initialising")
	require.NotNil(t, e.RetryAfter)
	assert.Equal(t, d, *e.RetryAfter)
}

// TestErrProjectNotFound verifies constructor code and Details field.
func TestErrProjectNotFound(t *testing.T) {
	e := ErrProjectNotFound("proj-abc")
	require.NotNil(t, e)
	assert.Equal(t, "project_not_found", e.Code)
	assert.Contains(t, e.Message, "proj-abc")
	assert.Equal(t, "proj-abc", e.Details["project_id"])
}

// TestErrToolDisabled verifies constructor code and Details field.
func TestErrToolDisabled(t *testing.T) {
	e := ErrToolDisabled("store", "feature flag off")
	require.NotNil(t, e)
	assert.Equal(t, "tool_disabled", e.Code)
	assert.Contains(t, e.Message, "feature flag off")
	assert.Equal(t, "store", e.Details["tool"])
}

// TestErrResourceExhausted verifies constructor code and Details field.
func TestErrResourceExhausted(t *testing.T) {
	e := ErrResourceExhausted("grpc_pool")
	require.NotNil(t, e)
	assert.Equal(t, "resource_exhausted", e.Code)
	assert.Contains(t, e.Message, "grpc_pool")
	assert.Equal(t, "grpc_pool", e.Details["resource"])
}

// TestErrUpstreamUnavailable verifies constructor code, Details, and nil cause handling.
func TestErrUpstreamUnavailable(t *testing.T) {
	t.Run("with cause", func(t *testing.T) {
		cause := assert.AnError
		e := ErrUpstreamUnavailable("engram-server", cause)
		require.NotNil(t, e)
		assert.Equal(t, "upstream_unavailable", e.Code)
		assert.Contains(t, e.Message, "engram-server")
		assert.Equal(t, "engram-server", e.Details["upstream"])
		assert.Equal(t, cause.Error(), e.Details["cause"])
	})

	t.Run("nil cause", func(t *testing.T) {
		e := ErrUpstreamUnavailable("redis", nil)
		require.NotNil(t, e)
		assert.Equal(t, "upstream_unavailable", e.Code)
		_, hasCause := e.Details["cause"]
		assert.False(t, hasCause, "nil cause must not add 'cause' key to Details")
	})
}

// TestErrTimeout verifies constructor code and duration in Details.
func TestErrTimeout(t *testing.T) {
	d := 30 * time.Second
	e := ErrTimeout(d)
	require.NotNil(t, e)
	assert.Equal(t, "timeout", e.Code)
	assert.Contains(t, e.Message, d.String())
	assert.Equal(t, d.String(), e.Details["wall_clock"])
}

// TestErrPreconditionFailed verifies constructor code and custom Details.
func TestErrPreconditionFailed(t *testing.T) {
	details := map[string]any{"expected": "ready", "actual": "initialising"}
	e := ErrPreconditionFailed("module not in ready state", details)
	require.NotNil(t, e)
	assert.Equal(t, "precondition_failed", e.Code)
	assert.Contains(t, e.Message, "module not in ready state")
	assert.Equal(t, "ready", e.Details["expected"])
	withoutDetails := ErrPreconditionFailed("r", nil)
	require.NotNil(t, withoutDetails)
	assert.Equal(t, "precondition_failed", withoutDetails.Code)
}

// TestModuleErrorMethod verifies the Error() string format.
func TestModuleErrorMethod(t *testing.T) {
	e := &ModuleError{Code: "not_ready", Message: "starting up"}
	assert.Equal(t, "not_ready: starting up", e.Error())
}

// TestModuleErrorJSONMarshal verifies that "code" and "message" always appear
// in the JSON output, and that RetryAfter is omitted when nil.
func TestModuleErrorJSONMarshal(t *testing.T) {
	t.Run("retry_after omitted when nil", func(t *testing.T) {
		e := ErrProjectNotFound("proj-123")
		b, err := json.Marshal(e)
		require.NoError(t, err)
		s := string(b)
		assert.True(t, strings.Contains(s, `"code"`), "JSON must contain code field")
		assert.True(t, strings.Contains(s, `"message"`), "JSON must contain message field")
		assert.False(t, strings.Contains(s, `"retry_after"`),
			"retry_after must be omitted when nil")
	})

	t.Run("retry_after present when set", func(t *testing.T) {
		d := 10 * time.Second
		e := ErrNotReady("waiting", d)
		b, err := json.Marshal(e)
		require.NoError(t, err)
		s := string(b)
		assert.True(t, strings.Contains(s, `"retry_after"`),
			"retry_after must appear in JSON when non-nil")
	})
}

// TestModuleErrorDetailsOmittedWhenNil verifies that details is omitted when nil.
func TestModuleErrorDetailsOmittedWhenNil(t *testing.T) {
	d := 5 * time.Second
	e := ErrNotReady("cold start", d)
	// ErrNotReady does not set Details.
	require.Nil(t, e.Details)

	b, err := json.Marshal(e)
	require.NoError(t, err)
	s := string(b)
	assert.False(t, strings.Contains(s, `"details"`),
		"details must be omitted from JSON when nil (omitempty)")
}
