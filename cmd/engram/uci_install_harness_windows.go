//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// uciInstallHarnessProcessTree owns every harness child through a Windows Job
// Object. Closing the final job handle terminates the whole descendant tree.
type uciInstallHarnessProcessTree struct {
	job       windows.Handle
	processes []*os.Process
	fallback  []*os.Process

	closeOnce sync.Once
	closeErr  error
}

func newUCIInstallHarnessProcessTree() (*uciInstallHarnessProcessTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create UCI install harness job object: %w", err)
	}

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	result, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	)
	if err == nil && result == 0 {
		err = errors.New("SetInformationJobObject returned zero")
	}
	if err != nil {
		closeErr := windows.CloseHandle(job)
		return nil, errors.Join(
			fmt.Errorf("configure UCI install harness job object: %w", err),
			closeErr,
		)
	}

	return &uciInstallHarnessProcessTree{job: job}, nil
}

func (tree *uciInstallHarnessProcessTree) Attach(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return errors.New("UCI install harness process is unavailable")
	}
	tree.processes = append(tree.processes, process)

	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(process.Pid))
	if err != nil {
		tree.fallback = append(tree.fallback, process)
		return nil
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(tree.job, handle); err != nil {
		tree.fallback = append(tree.fallback, process)
	}
	return nil
}

func (tree *uciInstallHarnessProcessTree) Close() error {
	tree.closeOnce.Do(func() {
		var cleanupErrors []error
		jobCloseErr := error(nil)
		if tree.job != 0 {
			jobCloseErr = windows.CloseHandle(tree.job)
			tree.job = 0
			if jobCloseErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("close UCI install harness job object: %w", jobCloseErr))
			}
		}

		targets := tree.fallback
		if jobCloseErr != nil {
			targets = tree.processes
		}
		for _, process := range targets {
			if err := uciTerminateWindowsInstallHarnessTree(process); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		tree.closeErr = errors.Join(cleanupErrors...)
	})
	return tree.closeErr
}

func uciTerminateWindowsInstallHarnessTree(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return nil
	}

	terminate := exec.Command("taskkill.exe", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
	terminate.Stdout = io.Discard
	terminate.Stderr = io.Discard
	if err := terminate.Run(); err == nil {
		return nil
	} else {
		killErr := process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			return nil
		}
		if killErr == nil {
			return fmt.Errorf("terminate UCI install harness process tree rooted at %d: %w", process.Pid, err)
		}
		return errors.Join(
			fmt.Errorf("terminate UCI install harness process tree rooted at %d: %w", process.Pid, err),
			fmt.Errorf("kill UCI install harness root process %d: %w", process.Pid, killErr),
		)
	}
}
