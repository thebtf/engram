//go:build windows

package recoveryreceipt

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWriteAR1BaselineReceiptAcceptsWindowsShortPrimaryRoot(t *testing.T) {
	primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
	shortPrimaryRoot := recoveryWindowsShortPath(t, primaryRoot)
	if strings.EqualFold(filepath.Clean(shortPrimaryRoot), filepath.Clean(primaryRoot)) {
		t.Skip("short path alias is unavailable")
	}
	raw, outputPath := configureWriter(t, shortPrimaryRoot, candidateRoot, sourceCommit)

	receipt, err := writeAR1BaselineReceiptFromTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	assertWrittenReceipt(t, outputPath, receipt, fingerprint(raw))
}

func TestRequireCandidateWorktreeRootAcceptsWindowsShortPath(t *testing.T) {
	_, candidateRoot, _ := candidateWorktree(t)
	shortCandidateRoot := recoveryWindowsShortPath(t, candidateRoot)
	if strings.EqualFold(filepath.Clean(shortCandidateRoot), filepath.Clean(candidateRoot)) {
		t.Skip("short path alias is unavailable")
	}

	if err := requireCandidateWorktreeRoot(shortCandidateRoot); err != nil {
		t.Fatal(err)
	}
}

func recoveryWindowsShortPath(t *testing.T, path string) string {
	t.Helper()
	input, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	length, err := syscall.GetShortPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil || length == 0 || int(length) >= len(buffer) {
		t.Skipf("short path alias is unavailable: %v", err)
	}
	return syscall.UTF16ToString(buffer[:length])
}
