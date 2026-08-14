//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/windows"
)

func liveProcessImageIdentity(pid int) (*processImageIdentity, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(process)

	path, err := queryFullProcessImageName(process)
	if err != nil {
		return nil, fmt.Errorf("query process %d executable: %w", pid, err)
	}
	image, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open process %d executable: %w", pid, err)
	}
	return &processImageIdentity{File: image}, nil
}

func verifyLiveProcessImageBinding(int, *processImageIdentity) error {
	return nil
}

func queryFullProcessImageName(process windows.Handle) (string, error) {
	for size := uint32(260); size <= 1<<15; size *= 2 {
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
		return windows.UTF16ToString(buffer[:length]), nil
	}
	return "", fmt.Errorf("process image path exceeds %d UTF-16 code units", 1<<15)
}

func muxcoreControlPeerPID(connection net.Conn) (int, error) {
	handleProvider, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return 0, fmt.Errorf("muxcore control connection does not expose a named-pipe handle")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(handleProvider.Fd()), &pid); err != nil {
		return 0, fmt.Errorf("query muxcore control server PID: %w", err)
	}
	if pid == 0 {
		return 0, fmt.Errorf("muxcore control server PID is unavailable")
	}
	return int(pid), nil
}
