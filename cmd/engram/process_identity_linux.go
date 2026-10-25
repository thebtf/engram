//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

type linuxProcessExitProof struct {
	poll [1]unix.PollFd
}

func openProcessExitProof(pid int) (muxcoreProcessExitProof, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return &linuxProcessExitProof{poll: [1]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}}, nil
}

func (process *linuxProcessExitProof) Exited() (bool, error) {
	n, err := unix.Poll(process.poll[:], 0)
	if err == unix.EINTR {
		return false, nil
	}
	if err != nil || n == 0 {
		return false, err
	}
	if process.poll[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
		return true, nil
	}
	return false, fmt.Errorf("unexpected pidfd poll status %d", process.poll[0].Revents)
}

func (process *linuxProcessExitProof) Close() error {
	return unix.Close(int(process.poll[0].Fd))
}

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
