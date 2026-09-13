//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	uciInstallHarnessJobCompletionKey     uintptr = 0x554349
	uciInstallHarnessJobMessageNewProcess         = 6
)

type uciInstallHarnessJobCompletionPort struct {
	CompletionKey  uintptr
	CompletionPort windows.Handle
}

// uciInstallHarnessProcessTree owns every harness child through a Windows Job
// Object. Closing the final job handle terminates the whole descendant tree.
// The completion port observes descendants (including one-shot parser workers)
// without changing their executable bytes or transport.
type uciInstallHarnessProcessTree struct {
	job       windows.Handle
	processes []*os.Process
	fallback  []*os.Process

	completionPort windows.Handle
	observeDone    chan struct{}
	observeWG      sync.WaitGroup
	observedMu     sync.Mutex
	observed       map[int]string

	closeOnce sync.Once
	closeErr  error
}

func uciConfigureInstallHarnessCommand(*exec.Cmd) error {
	return nil
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

	tree := &uciInstallHarnessProcessTree{
		job:      job,
		observed: make(map[int]string),
	}
	tree.startObservation()
	return tree, nil
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

func (tree *uciInstallHarnessProcessTree) startObservation() {
	if tree == nil || tree.job == 0 {
		return
	}
	port, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, uciInstallHarnessJobCompletionKey, 1)
	if err != nil {
		return
	}
	association := uciInstallHarnessJobCompletionPort{
		CompletionKey:  uciInstallHarnessJobCompletionKey,
		CompletionPort: port,
	}
	result, err := windows.SetInformationJobObject(
		tree.job,
		windows.JobObjectAssociateCompletionPortInformation,
		uintptr(unsafe.Pointer(&association)),
		uint32(unsafe.Sizeof(association)),
	)
	if err != nil || result == 0 {
		_ = windows.CloseHandle(port)
		return
	}
	tree.completionPort = port
	tree.observeDone = make(chan struct{})
	tree.observeWG.Add(1)
	go tree.observeJobProcesses(port, tree.observeDone)
}

func (tree *uciInstallHarnessProcessTree) observeJobProcesses(port windows.Handle, done <-chan struct{}) {
	defer tree.observeWG.Done()
	for {
		select {
		case <-done:
			return
		default:
		}
		var message uint32
		var key uintptr
		var completion *windows.Overlapped
		err := windows.GetQueuedCompletionStatus(port, &message, &key, &completion, 100)
		if err != nil {
			if errors.Is(err, windows.WAIT_TIMEOUT) {
				continue
			}
			return
		}
		if key != uciInstallHarnessJobCompletionKey || message != uciInstallHarnessJobMessageNewProcess || completion == nil {
			continue
		}
		pid := int(uintptr(unsafe.Pointer(completion)))
		if pid > 0 {
			tree.recordObservedProcess(pid)
		}
	}
}

func (tree *uciInstallHarnessProcessTree) recordObservedProcess(pid int) {
	executable, err := uciInstallHarnessWindowsExecutable(pid)
	if err != nil || executable == "" {
		return
	}
	tree.observedMu.Lock()
	tree.observed[pid] = executable
	tree.observedMu.Unlock()
}

func uciInstallHarnessWindowsExecutable(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32*1024)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func (tree *uciInstallHarnessProcessTree) stopObservation() {
	if tree == nil || tree.observeDone == nil {
		return
	}
	close(tree.observeDone)
	tree.observeWG.Wait()
	tree.observeDone = nil
	if tree.completionPort != 0 {
		_ = windows.CloseHandle(tree.completionPort)
		tree.completionPort = 0
	}
}

func (tree *uciInstallHarnessProcessTree) ObservedPIDsForExecutable(executable string) []int {
	if tree == nil || executable == "" {
		return nil
	}
	target := filepath.Clean(executable)
	tree.observedMu.Lock()
	defer tree.observedMu.Unlock()
	pids := make([]int, 0)
	for pid, observed := range tree.observed {
		if strings.EqualFold(filepath.Clean(observed), target) {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids
}

func (tree *uciInstallHarnessProcessTree) Close() error {
	tree.closeOnce.Do(func() {
		tree.stopObservation()
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
