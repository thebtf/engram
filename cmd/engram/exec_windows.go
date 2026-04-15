//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

// execReplace starts a new process running exePath and exits the current
// process with code 0. This is the Windows equivalent of Unix exec-in-place:
// Windows does not support replacing the process image in-place via a syscall
// equivalent to execve, so we spawn a child and exit.
//
// The child inherits stdin/stdout/stderr and the same os.Args slice. The
// brief overlap between parent exit and child startup is acceptable — the
// caller (handleGracefulRestart) has already shut down the engine and modules,
// so no new requests are being processed.
//
// Design reference: tasks.md T058 step 7 (Windows fallback path).
func execReplace(exePath string, logger *slog.Logger) error {
	logger.Info("spawning replacement process", "binary", exePath)

	cmd := exec.Command(exePath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cmd.Start %q: %w", exePath, err)
	}

	logger.Info("replacement process started, exiting parent", "pid", cmd.Process.Pid)
	os.Exit(0)

	// Unreachable.
	return nil
}
