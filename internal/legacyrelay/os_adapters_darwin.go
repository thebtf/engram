//go:build darwin

package legacyrelay

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	darwinProcPIDPathInfo           = 11
	darwinProcPIDUniqueIdentityInfo = 17
	darwinProcessPathMax            = 4096
)

type darwinUniqueIdentity struct {
	UUID            [16]byte
	UniqueID        uint64
	ParentUniqueID  uint64
	IDVersion       int32
	OrigPPIDVersion int32
	Reserved2       uint64
	Reserved3       uint64
}

func (osProcessInspector) Inspect(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, ErrBootstrapUnavailable
	}
	unique, err := inspectDarwinUniqueIdentity(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("query process %d parent: %w", pid, err)
	}
	image, err := inspectDarwinProcessPath(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	return NewProcessIdentity(pid, fmt.Sprintf("darwin:%d:%x", unique.UniqueID, unique.UUID), image, int(info.Eproc.Ppid))
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
		pid, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, fmt.Errorf("inspect legacy relay Unix socket: %w", err)
	}
	if socketErr != nil {
		return 0, fmt.Errorf("read legacy relay peer PID: %w", socketErr)
	}
	if pid <= 0 {
		return 0, errors.New("legacy relay Unix peer PID is unavailable")
	}
	return pid, nil
}

func inspectDarwinUniqueIdentity(pid int) (darwinUniqueIdentity, error) {
	var identity darwinUniqueIdentity
	result, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, 2, uintptr(pid), darwinProcPIDUniqueIdentityInfo, 0, uintptr(unsafe.Pointer(&identity)), unsafe.Sizeof(identity))
	if errno != 0 {
		return darwinUniqueIdentity{}, fmt.Errorf("query process %d identity: %w", pid, errno)
	}
	if result != unsafe.Sizeof(identity) || identity.UniqueID == 0 || identity.UUID == [16]byte{} {
		return darwinUniqueIdentity{}, fmt.Errorf("query process %d identity: invalid response", pid)
	}
	return identity, nil
}

func inspectDarwinProcessPath(pid int) (string, error) {
	buffer := make([]byte, darwinProcessPathMax)
	_, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, 2, uintptr(pid), darwinProcPIDPathInfo, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if errno != 0 {
		return "", fmt.Errorf("query process %d image: %w", pid, errno)
	}
	length := 0
	for length < len(buffer) && buffer[length] != 0 {
		length++
	}
	if length == 0 || length == len(buffer) {
		return "", fmt.Errorf("query process %d image: invalid path", pid)
	}
	return string(buffer[:length]), nil
}
