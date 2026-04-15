package loom_test

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	loomlib "github.com/thebtf/aimux/loom"
	loomhandler "github.com/thebtf/engram/internal/handlers/loom"
)

// echoTask returns a TaskRequest that runs the platform's echo binary
// with the given prompt on stdin. The echo binary simply writes its first
// positional argument, so we use "cat" on Unix and "cmd /c type CON" on
// Windows to read from stdin. For simplicity, we use a synthetic helper.
func echoTask(t *testing.T, prompt string) *loomlib.Task {
	t.Helper()
	return &loomlib.Task{
		ID:     "test-task",
		Status: loomlib.TaskStatusRunning,
		CLI:    echoBinary(t),
		Prompt: prompt,
	}
}

// echoBinary returns the name of a binary that reads stdin and writes it
// back to stdout. On all platforms we use a small helper that the test
// creates in t.TempDir() so we have full control without relying on
// OS-specific behaviour differences.
func echoBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "cat"
}

// TestCliWorker_HappyPath verifies that the worker runs an allowlisted
// binary and returns the stdout content as the result.
func TestCliWorker_HappyPath(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("happy path test requires a POSIX shell (cat reads stdin)")
	}
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not in PATH")
	}

	// cat reads stdin and writes it back — verifies the stdin delivery path.
	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"cat"})
	task := &loomlib.Task{
		ID:     "t1",
		Status: loomlib.TaskStatusRunning,
		CLI:    "cat",
		Prompt: "hello world",
	}

	result, err := w.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if result.Content != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", result.Content)
	}
}

// TestCliWorker_AllowlistDeny verifies that a binary not in the allowlist
// is rejected with an error.
func TestCliWorker_AllowlistDeny(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"codex", "claude"})
	task := &loomlib.Task{
		ID:     "t2",
		Status: loomlib.TaskStatusRunning,
		CLI:    "notallowed",
		Prompt: "anything",
	}

	_, err := w.Execute(context.Background(), task)
	if err == nil {
		t.Fatal("expected error for non-allowlisted binary, got nil")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("expected 'allowlist' in error, got: %v", err)
	}
}

// TestCliWorker_PathSeparatorReject verifies that binary names containing
// path separators are rejected to prevent path traversal.
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
			task := &loomlib.Task{
				ID:     "t3",
				Status: loomlib.TaskStatusRunning,
				CLI:    cli,
				Prompt: "anything",
			}
			_, err := w.Execute(context.Background(), task)
			if err == nil {
				t.Fatalf("expected error for path separator in %q, got nil", cli)
			}
			if !strings.Contains(err.Error(), "path separator") && !strings.Contains(err.Error(), "drive colon") {
				t.Errorf("error should mention path separator or drive colon, got: %v", err)
			}
		})
	}
}

// TestCliWorker_EnvMerge verifies that task.Env values override the
// daemon's environment when the subprocess is invoked.
func TestCliWorker_EnvMerge(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("env merge test requires a POSIX shell")
	}

	// Use a shell to print the value of MY_TEST_VAR.
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"sh"})
	task := &loomlib.Task{
		ID:     "t4",
		Status: loomlib.TaskStatusRunning,
		CLI:    "sh",
		Prompt: "echo $MY_TEST_VAR",
		Env:    map[string]string{"MY_TEST_VAR": "engram_test_value"},
	}

	result, err := w.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "engram_test_value") {
		t.Errorf("expected env var in output, got: %q", result.Content)
	}
}

// TestCliWorker_Timeout verifies that a long-running subprocess is killed
// when the context deadline expires.
func TestCliWorker_Timeout(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("timeout test requires a POSIX shell")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"sh"})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// "exec sleep 10" uses the exec builtin to replace the shell process with
	// sleep, ensuring exec.CommandContext kills the sleeping process directly
	// (not just the shell wrapper) when the context deadline fires.
	task := &loomlib.Task{
		ID:     "t5",
		Status: loomlib.TaskStatusRunning,
		CLI:    "sh",
		Prompt: "exec sleep 10",
	}

	start := time.Now()
	_, err := w.Execute(ctx, task)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on context timeout, got nil")
	}
	if elapsed >= 2*time.Second {
		t.Errorf("Execute took %v, expected cancellation within 2s", elapsed)
	}
}

// TestCliWorker_StderrCapture verifies that non-zero exit code errors
// include the subprocess's stderr output in the returned error.
func TestCliWorker_StderrCapture(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("stderr capture test requires a POSIX shell")
	}

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"sh"})
	task := &loomlib.Task{
		ID:     "t6",
		Status: loomlib.TaskStatusRunning,
		CLI:    "sh",
		Prompt: "echo 'sentinel_error_msg' >&2 && exit 1",
	}

	_, err := w.Execute(context.Background(), task)
	if err == nil {
		t.Fatal("expected error on non-zero exit, got nil")
	}
	if !strings.Contains(err.Error(), "sentinel_error_msg") {
		t.Errorf("expected stderr in error message, got: %v", err)
	}
}

// TestCliWorker_EmptyStdoutTriggersRetry verifies that empty stdout results
// in a WorkerResult with empty Content (loom quality gate will retry).
func TestCliWorker_EmptyStdoutTriggersRetry(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("empty stdout test requires a POSIX shell")
	}

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"sh"})
	task := &loomlib.Task{
		ID:     "t7",
		Status: loomlib.TaskStatusRunning,
		CLI:    "sh",
		Prompt: "true", // exits 0 with no output
	}

	result, err := w.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil, expected WorkerResult with empty content")
	}
	if result.Content != "" {
		t.Errorf("expected empty Content to trigger retry, got: %q", result.Content)
	}
}

// TestCliWorker_ContextCancellation verifies that cancelling the context
// mid-execution causes Execute to return an error promptly.
func TestCliWorker_ContextCancellation(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("context cancellation test requires a POSIX shell")
	}

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"sh"})
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// "exec sleep 30" uses the exec builtin to replace the shell process with
	// sleep, ensuring exec.CommandContext kills the sleeping process directly
	// (not just the shell wrapper) when the context is cancelled.
	task := &loomlib.Task{
		ID:     "t8",
		Status: loomlib.TaskStatusRunning,
		CLI:    "sh",
		Prompt: "exec sleep 30",
	}

	start := time.Now()
	_, err := w.Execute(ctx, task)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if elapsed >= 2*time.Second {
		t.Errorf("Execute took %v after cancel, expected <2s", elapsed)
	}
}

// TestCliWorker_InvalidEnvKey verifies that an invalid environment variable
// key name is rejected with an error.
func TestCliWorker_InvalidEnvKey(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo not in PATH")
	}

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{"echo"})
	task := &loomlib.Task{
		ID:     "t9",
		Status: loomlib.TaskStatusRunning,
		CLI:    "echo",
		Prompt: "test",
		Env: map[string]string{
			"123INVALID": "value",
		},
	}

	_, err := w.Execute(context.Background(), task)
	if err == nil {
		t.Fatal("expected error for invalid env key, got nil")
	}
	if !strings.Contains(err.Error(), "invalid env key") {
		t.Errorf("expected 'invalid env key' in error, got: %v", err)
	}
}

// Ensure NewCLIWorkerWithAllowlist is exported (used in tests above).
// This line is a compile-time assertion that loomhandler exports the function.
var _ = loomhandler.NewCLIWorkerWithAllowlist
