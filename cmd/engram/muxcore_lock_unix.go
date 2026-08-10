//go:build !windows

package main

import (
	"os"
	"syscall"
)

func lockMuxcoreDaemonFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockMuxcoreDaemonFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func isMuxcoreDaemonLockContended(err error) bool {
	return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
}
