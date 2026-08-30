//go:build linux

package legacyrelay

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func (osProcessInspector) Inspect(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, ErrBootstrapUnavailable
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("read process %d stat: %w", pid, err)
	}
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 || closing+2 >= len(stat) {
		return ProcessIdentity{}, fmt.Errorf("parse process %d stat: malformed comm field", pid)
	}
	fields := strings.Fields(string(stat[closing+2:]))
	if len(fields) <= 19 {
		return ProcessIdentity{}, fmt.Errorf("parse process %d stat: missing fields", pid)
	}
	parentPID, err := strconv.Atoi(fields[1])
	if err != nil || parentPID < 0 {
		return ProcessIdentity{}, fmt.Errorf("parse process %d parent PID", pid)
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || startTicks == 0 {
		return ProcessIdentity{}, fmt.Errorf("parse process %d start time", pid)
	}
	image, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("read process %d image: %w", pid, err)
	}
	return NewProcessIdentity(pid, fmt.Sprintf("linux:%d", startTicks), image, parentPID)
}

func (osPeerResolver) PeerPID(connection net.Conn) (int, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, errors.New("legacy relay connection is not a Unix socket")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("access legacy relay Unix socket: %w", err)
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
		return 0, fmt.Errorf("inspect legacy relay Unix socket: %w", err)
	}
	if socketErr != nil {
		return 0, fmt.Errorf("read legacy relay peer credentials: %w", socketErr)
	}
	if pid <= 0 {
		return 0, errors.New("legacy relay Unix peer PID is unavailable")
	}
	return pid, nil
}
