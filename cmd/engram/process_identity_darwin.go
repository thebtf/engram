//go:build darwin

package main

import (
	"debug/macho"
	"errors"
	"fmt"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	darwinProcPidPathInfoMaxSize    = 4096
	darwinProcPIDUniqIdentifierInfo = 17
	darwinMachOLoadCommandUUID      = 0x1b
	darwinMachOLoadCommandUUIDSize  = 24
)

// darwinProcUniqueIdentifierInfo exactly matches XNU's 56-byte
// proc_uniqidentifierinfo ABI.
type darwinProcUniqueIdentifierInfo struct {
	UUID            [16]byte
	UniqueID        uint64
	ParentUniqueID  uint64
	IDVersion       int32
	OrigPPIDVersion int32
	Reserved2       uint64
	Reserved3       uint64
}

var _ [56]byte = [unsafe.Sizeof(darwinProcUniqueIdentifierInfo{})]byte{}

func liveProcessImageIdentity(pid int) (*processImageIdentity, error) {
	process, err := darwinProcessIdentity(pid)
	if err != nil {
		return nil, err
	}
	path, err := darwinProcessPath(pid)
	if err != nil {
		return nil, err
	}
	image, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open process %d executable: %w", pid, err)
	}
	uuid, err := darwinMachOUUID(image)
	if err != nil {
		image.Close()
		return nil, fmt.Errorf("read process %d executable UUID: %w", pid, err)
	}
	if uuid != process.UUID {
		image.Close()
		return nil, errors.New("opened process executable UUID does not match kernel process image UUID")
	}
	return &processImageIdentity{File: image, darwinUUID: uuid, darwinUniqueID: process.UniqueID}, nil
}

func verifyLiveProcessImageBinding(pid int, image *processImageIdentity) error {
	if image == nil || image.File == nil {
		return errors.New("missing opened Darwin process image binding")
	}
	process, err := darwinProcessIdentity(pid)
	if err != nil {
		return err
	}
	if !sameDarwinProcessImageBinding(image.darwinUUID, image.darwinUniqueID, process.UUID, process.UniqueID) {
		return errors.New("Darwin process image changed during live image proof")
	}
	return nil
}

func darwinProcessIdentity(pid int) (darwinProcUniqueIdentifierInfo, error) {
	var identity darwinProcUniqueIdentifierInfo
	result, _, errno := syscallSyscall6(libproc_proc_pidinfo_trampoline_addr,
		uintptr(pid), darwinProcPIDUniqIdentifierInfo, 0,
		uintptr(unsafe.Pointer(&identity)), unsafe.Sizeof(identity), 0)
	if errno != 0 {
		return darwinProcUniqueIdentifierInfo{}, fmt.Errorf("query process %d identity: %w", pid, errno)
	}
	if result != unsafe.Sizeof(identity) {
		return darwinProcUniqueIdentifierInfo{}, fmt.Errorf("query process %d identity: returned %d bytes", pid, result)
	}
	if identity.UniqueID == 0 || identity.UUID == [16]byte{} {
		return darwinProcUniqueIdentifierInfo{}, fmt.Errorf("query process %d identity: missing process incarnation or executable UUID", pid)
	}
	return identity, nil
}

func darwinProcessPath(pid int) (string, error) {
	buffer := make([]byte, darwinProcPidPathInfoMaxSize)
	result, _, errno := syscallSyscall6(libproc_proc_pidpath_trampoline_addr, uintptr(pid), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0, 0)
	if errno != 0 || result == 0 {
		if errno != 0 {
			return "", fmt.Errorf("query process %d executable: %w", pid, errno)
		}
		return "", fmt.Errorf("query process %d executable: empty path", pid)
	}
	pathLength := int(result)
	if pathLength >= len(buffer) {
		return "", fmt.Errorf("query process %d executable: path is too long", pid)
	}
	path := string(buffer[:pathLength])
	if pathLength > 0 && path[pathLength-1] == 0 {
		path = path[:pathLength-1]
	}
	if path == "" {
		return "", fmt.Errorf("query process %d executable: empty path", pid)
	}
	return path, nil
}

func darwinMachOUUID(image *os.File) ([16]byte, error) {
	if image == nil {
		return [16]byte{}, errors.New("missing executable file")
	}
	file, err := macho.NewFile(image)
	if err != nil {
		return [16]byte{}, err
	}
	if file.Cpu != macho.CpuArm64 {
		return [16]byte{}, errors.New("unsupported Mach-O CPU or fat binary")
	}
	var uuid [16]byte
	found := false
	for _, load := range file.Loads {
		raw := load.Raw()
		if len(raw) < 4 || file.ByteOrder.Uint32(raw[:4]) != darwinMachOLoadCommandUUID {
			continue
		}
		if found || len(raw) != darwinMachOLoadCommandUUIDSize || file.ByteOrder.Uint32(raw[4:8]) != darwinMachOLoadCommandUUIDSize {
			return [16]byte{}, errors.New("malformed Mach-O UUID load command")
		}
		copy(uuid[:], raw[8:])
		found = true
	}
	if !found || uuid == [16]byte{} {
		return [16]byte{}, errors.New("Mach-O executable UUID is missing")
	}
	return uuid, nil
}

var (
	libproc_proc_pidpath_trampoline_addr uintptr
	libproc_proc_pidinfo_trampoline_addr uintptr
)

//go:cgo_import_dynamic libproc_proc_pidpath proc_pidpath "/usr/lib/libproc.dylib"
//go:cgo_import_dynamic libproc_proc_pidinfo proc_pidinfo "/usr/lib/libproc.dylib"

//go:linkname syscallSyscall6 syscall.syscall6
func syscallSyscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err unix.Errno)

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
		pid, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, fmt.Errorf("inspect muxcore control socket: %w", err)
	}
	if socketErr != nil {
		return 0, fmt.Errorf("read muxcore control peer PID: %w", socketErr)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("muxcore control server PID is unavailable")
	}
	return pid, nil
}
