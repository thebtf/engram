package loom_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomlib "github.com/thebtf/aimux/loom"
	loomhandler "github.com/thebtf/engram/internal/handlers/loom"
)

func helperTask(prompt, mode string) *loomlib.Task {
	env := map[string]string{}
	if mode != "" {
		env["LOOM_HELPER_MODE"] = mode
	}
	return &loomlib.Task{
		ID:     "test-task",
		Status: loomlib.TaskStatusRunning,
		CLI:    loomCLIHelperName,
		Prompt: prompt,
		Env:    env,
	}
}

// TestCliWorker_HappyPath verifies stdin delivery and stdout capture using the
// compiled cross-platform helper installed in PATH by TestMain.
func TestCliWorker_HappyPath(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	result, err := w.Execute(context.Background(), helperTask("hello world", "echo"))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "hello world", result.Content)
}

func TestCliWorker_AllowlistDeny(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"codex", "claude"})
	task := helperTask("anything", "echo")
	task.CLI = "notallowed"

	_, err := w.Execute(context.Background(), task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist")
}

func TestCliWorker_PathSeparatorReject(t *testing.T) {
	t.Parallel()

	cases := []string{
		"../etc/passwd",
		"/usr/bin/sh",
		"sub/binary",
		"C:\\Windows\\system32\\cmd",
		"with:colon",
	}

	for _, cli := range cases {
		cli := cli
		t.Run(cli, func(t *testing.T) {
			t.Parallel()
			w := loomhandler.NewCLIWorkerWithAllowlist([]string{cli})
			task := helperTask("anything", "echo")
			task.CLI = cli
			_, err := w.Execute(context.Background(), task)
			require.Error(t, err)
			assert.ErrorContains(t, err, "path separator")
		})
	}
}

func TestCliWorker_EnvMerge(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	task := helperTask("", "env")
	task.Env["MY_TEST_VAR"] = "engram_test_value"

	result, err := w.Execute(context.Background(), task)
	require.NoError(t, err)
	assert.Equal(t, "engram_test_value", result.Content)
}

func TestCliWorker_Timeout(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := w.Execute(ctx, helperTask("", "sleep"))
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 2*time.Second)
}

func TestCliWorker_StderrCapture(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	_, err := w.Execute(context.Background(), helperTask("", "stderr"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sentinel_error_msg")
}

func TestCliWorker_EmptyStdoutTriggersRetry(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	result, err := w.Execute(context.Background(), helperTask("", "empty"))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Content)
}

func TestCliWorker_ContextCancellation(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()

	start := time.Now()
	_, err := w.Execute(ctx, helperTask("", "sleep"))
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 2*time.Second)
}

func TestCliWorker_InvalidEnvKey(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	task := helperTask("test", "echo")
	task.Env["123INVALID"] = "value"

	_, err := w.Execute(context.Background(), task)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "invalid env key"))
}

var _ = loomhandler.NewCLIWorkerWithAllowlist
