package loom

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	loom "github.com/thebtf/aimux/loom"
	"github.com/thebtf/engram/internal/module"
)

// maxOutputBytes caps the stdout read from a CLI worker to prevent a
// runaway subprocess from exhausting daemon memory (DoS protection).
const maxOutputBytes = 10 * 1024 * 1024 // 10 MiB

// defaultAllowlist contains the CLI binaries that the cliWorker is permitted
// to invoke. Callers cannot override this list at runtime; operator extension
// is done by registering additional Worker types (see docs/modules/loom.md).
var defaultAllowlist = []string{"codex", "claude", "aimux"}

// envKeyRe validates that an environment variable key matches the POSIX
// [A-Za-z_][A-Za-z0-9_]* pattern.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// cliWorker executes tasks by shelling out to an allowlisted binary.
// The task prompt is delivered on stdin, NOT as a command-line argument,
// so the full prompt (including newlines, special characters, secrets) is
// never visible in the process list.
//
// Security invariants:
//   - Binary name MUST be in the allowlist (fail-closed).
//   - Binary name MUST NOT contain path separators ('/', '\', ':') to
//     prevent path traversal attacks.
//   - Environment keys are validated against [A-Za-z_][A-Za-z0-9_]* before
//     being passed to the child process.
//   - Task.Prompt is delivered via stdin, not CLI args.
type cliWorker struct {
	allowlist []string
}

// newCLIWorker returns a cliWorker using the default allowlist.
func newCLIWorker() *cliWorker {
	al := make([]string, len(defaultAllowlist))
	copy(al, defaultAllowlist)
	return &cliWorker{allowlist: al}
}

// NewCLIWorkerWithAllowlist constructs a cliWorker with a custom allowlist.
// Intended for testing only — production code uses the module registration
// path which calls newCLIWorker() with the default allowlist.
func NewCLIWorkerWithAllowlist(allowlist []string) *cliWorker {
	al := make([]string, len(allowlist))
	copy(al, allowlist)
	return &cliWorker{allowlist: al}
}

// Type returns WorkerTypeCLI. Implements loom.Worker.
func (w *cliWorker) Type() loom.WorkerType { return loom.WorkerTypeCLI }

// Execute runs the task by invoking task.CLI with task.Prompt on stdin.
// Implements loom.Worker.
//
// Return semantics (CONTRACT.md):
//   - Non-nil error → task marked failed immediately (no retry).
//   - WorkerResult with empty Content → quality gate triggers retry.
//   - WorkerResult with non-empty Content → gate decides accept/reject.
func (w *cliWorker) Execute(ctx context.Context, task *loom.Task) (*loom.WorkerResult, error) {
	// Validate binary is in allowlist (fail-closed by default).
	if !w.isAllowed(task.CLI) {
		return nil, fmt.Errorf("loom: cli worker: binary %q is not in allowlist %v", task.CLI, w.allowlist)
	}

	// Reject path separators to prevent path traversal.
	if strings.ContainsAny(task.CLI, "/\\:") {
		return nil, fmt.Errorf("loom: cli worker: binary name %q contains a path separator or drive colon", task.CLI)
	}

	// Build optional CLI arguments from structured task fields.
	// Prompt is NEVER put on the command line — it goes to stdin.
	var args []string
	if task.Role != "" {
		args = append(args, "--role", task.Role)
	}
	if task.Model != "" {
		args = append(args, "--model", task.Model)
	}
	if task.Effort != "" {
		args = append(args, "--effort", task.Effort)
	}

	cmd := exec.CommandContext(ctx, task.CLI, args...)

	// Deliver prompt via stdin.
	cmd.Stdin = strings.NewReader(task.Prompt)

	// Set working directory if specified.
	if task.CWD != "" {
		cmd.Dir = task.CWD
	}

	// Merge task.Env over os.Environ(). Validate each key before use.
	env := os.Environ()
	for k, v := range task.Env {
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("loom: cli worker: invalid env key %q (must match [A-Za-z_][A-Za-z0-9_]*)", k)
		}
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	// Capture stdout up to maxOutputBytes to prevent memory exhaustion from
	// a runaway subprocess. Stderr is captured separately for error reporting.
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdoutBuf, n: maxOutputBytes}
	cmd.Stderr = &stderrBuf

	start := time.Now()
	err := cmd.Run()
	durationMS := time.Since(start).Milliseconds()

	if err != nil {
		// ctx was cancelled or deadline exceeded — return raw error.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// exec.ExitError: process exited non-zero; include stderr in message.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(stderrBuf.String())
			if stderr != "" {
				return nil, fmt.Errorf("loom: cli worker: %s exited with code %d: %s",
					task.CLI, exitErr.ExitCode(), stderr)
			}
			return nil, fmt.Errorf("loom: cli worker: %s exited with code %d",
				task.CLI, exitErr.ExitCode())
		}
		return nil, fmt.Errorf("loom: cli worker: run %s: %w", task.CLI, err)
	}

	// Empty stdout → return empty WorkerResult so loom's quality gate retries.
	content := strings.TrimSpace(stdoutBuf.String())
	return &loom.WorkerResult{
		Content:    content,
		DurationMS: durationMS,
	}, nil
}

// isAllowed checks whether name is in the allowlist (case-sensitive).
func (w *cliWorker) isAllowed(name string) bool {
	for _, a := range w.allowlist {
		if a == name {
			return true
		}
	}
	return false
}

// compile-time assertion: cliWorker must satisfy loom.Worker.
var _ loom.Worker = (*cliWorker)(nil)

// registerWorkers is called from Module.Init after RecoverCrashed.
// It registers every built-in worker with the engine.
func registerWorkers(eng loomEngine, _ module.ModuleDeps) {
	eng.RegisterWorker(loom.WorkerTypeCLI, newCLIWorker())
}

// limitedWriter wraps an io.Writer and silently discards bytes beyond the
// limit n. Used to cap subprocess stdout and prevent memory exhaustion.
type limitedWriter struct {
	w io.Writer
	n int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil // discard
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.w.Write(p)
	l.n -= int64(n)
	return len(p), err // report full len to avoid short-write errors
}
