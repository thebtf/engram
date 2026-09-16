//go:build windows

package legacyrelay

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (osProcessInspector) Inspect(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, ErrBootstrapUnavailable
	}
	parentPID, err := windowsParentPID(uint32(pid))
	if err != nil {
		return ProcessIdentity{}, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(process)

	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return ProcessIdentity{}, fmt.Errorf("query process %d creation time: %w", pid, err)
	}
	image, err := windowsProcessImage(process)
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("query process %d image: %w", pid, err)
	}
	return NewProcessIdentity(pid, fmt.Sprintf("windows:%d", created.Nanoseconds()), image, int(parentPID))
}

func (osPeerResolver) PeerPID(connection net.Conn) (int, error) {
	handleProvider, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return 0, errors.New("legacy relay connection does not expose a named-pipe handle")
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(handleProvider.Fd()), &pid); err != nil {
		return 0, fmt.Errorf("query legacy relay named-pipe peer: %w", err)
	}
	if pid == 0 {
		return 0, errors.New("legacy relay named-pipe peer PID is unavailable")
	}
	return int(pid), nil
}

func windowsParentPID(pid uint32) (uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, fmt.Errorf("snapshot process table: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0, fmt.Errorf("read process table: %w", err)
	}
	for {
		if entry.ProcessID == pid {
			return entry.ParentProcessID, nil
		}
		err := windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("walk process table: %w", err)
		}
	}
	return 0, fmt.Errorf("process %d is not live", pid)
}

func windowsProcessImage(process windows.Handle) (string, error) {
	for size := uint32(windows.MAX_PATH); size <= 1<<15; size *= 2 {
		buffer := make([]uint16, size)
		length := size
		err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &length)
		if errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if err != nil {
			return "", err
		}
		if length == 0 {
			return "", errors.New("empty process image path")
		}
		image := strings.TrimSpace(windows.UTF16ToString(buffer[:length]))
		if image == "" {
			return "", errors.New("empty process image path")
		}
		return image, nil
	}
	return "", errors.New("process image path exceeds limit")
}
