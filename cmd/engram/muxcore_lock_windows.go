//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	muxcoreKernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockMuxcoreFileEx   = muxcoreKernel32.NewProc("LockFileEx")
	unlockMuxcoreFileEx = muxcoreKernel32.NewProc("UnlockFileEx")
)

func lockMuxcoreDaemonFile(file *os.File) error {
	const flags = 0x00000002 | 0x00000001
	overlapped := &syscall.Overlapped{}
	result, _, err := lockMuxcoreFileEx.Call(uintptr(file.Fd()), uintptr(flags), 0, 1, 0, uintptr(unsafe.Pointer(overlapped)))
	if result == 0 {
		return err
	}
	return nil
}

func unlockMuxcoreDaemonFile(file *os.File) error {
	overlapped := &syscall.Overlapped{}
	result, _, err := unlockMuxcoreFileEx.Call(uintptr(file.Fd()), 0, 1, 0, uintptr(unsafe.Pointer(overlapped)))
	if result == 0 {
		return err
	}
	return nil
}

func isMuxcoreDaemonLockContended(err error) bool {
	return err == syscall.Errno(33)
}
