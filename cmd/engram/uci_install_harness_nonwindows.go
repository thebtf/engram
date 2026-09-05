//go:build !windows

package main

import (
	"errors"
	"os"
	"sync"
)

// The installed-path contract is Windows-native. This fallback preserves
// portable compilation and reaps direct children when the helper is used on
// another platform.
type uciInstallHarnessProcessTree struct {
	processes []*os.Process

	closeOnce sync.Once
	closeErr  error
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
			if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		tree.closeErr = errors.Join(cleanupErrors...)
	})
	return tree.closeErr
}
