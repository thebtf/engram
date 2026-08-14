//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

func liveProcessImageIdentity(pid int) (*processImageIdentity, error) {
	image, err := os.Open(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return nil, fmt.Errorf("open process %d executable: %w", pid, err)
	}
	return &processImageIdentity{File: image}, nil
}

func verifyLiveProcessImageBinding(int, *processImageIdentity) error {
	return nil
}

func muxcoreControlPeerPID(connection net.Conn) (int, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("muxcore control connection is not a Unix socket")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("access muxcore control socket: %w", err)
	}
	pid := 0
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil {
			socketErr = err
			return
		}
		pid = int(credentials.Pid)
	}); err != nil {
		return 0, fmt.Errorf("inspect muxcore control socket: %w", err)
	}
	if socketErr != nil {
		return 0, fmt.Errorf("read muxcore control peer credentials: %w", socketErr)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("muxcore control server PID is unavailable")
	}
	return pid, nil
}
