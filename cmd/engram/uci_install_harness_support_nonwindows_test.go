//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// The installed-path contract is Windows-native. This fallback preserves
// portable compilation and reaps direct children when the helper is used on
// another platform.
type uciInstallHarnessProcessTree struct {
	processes []*os.Process

	closeOnce sync.Once
	closeErr  error
}

func uciConfigureInstallHarnessCommand(command *exec.Cmd) error {
	if command == nil {
		return errors.New("UCI install harness command is nil")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func newUCIInstallHarnessProcessTree() (*uciInstallHarnessProcessTree, error) {
	return &uciInstallHarnessProcessTree{}, nil
}

func (tree *uciInstallHarnessProcessTree) Attach(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return errors.New("UCI install harness process is unavailable")
	}
	tree.processes = append(tree.processes, process)
	return nil
}

func (tree *uciInstallHarnessProcessTree) Close() error {
	tree.closeOnce.Do(func() {
		var cleanupErrors []error
		for _, process := range tree.processes {
			if err := uciTerminateNonWindowsInstallHarnessTree(process); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		tree.closeErr = errors.Join(cleanupErrors...)
	})
	return tree.closeErr
}

func uciTerminateNonWindowsInstallHarnessTree(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return nil
	}

	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		killErr := process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		return errors.Join(
			fmt.Errorf("terminate UCI install harness process group rooted at %d: %w", process.Pid, err),
			killErr,
		)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		probeErr := syscall.Kill(-process.Pid, 0)
		if errors.Is(probeErr, syscall.ESRCH) {
			return nil
		}
		if probeErr != nil {
			return fmt.Errorf("verify UCI install harness process group rooted at %d: %w", process.Pid, probeErr)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("UCI install harness process group rooted at %d remained alive after 5s", process.Pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (tree *uciInstallHarnessProcessTree) ObservedPIDsForExecutable(string) []int {
	return nil
}
