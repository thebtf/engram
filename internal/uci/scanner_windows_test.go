//go:build windows

package uci

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestUCIScannerCanonicalizesWindowsShortAuthorizedRoot(t *testing.T) {
	fixture := newScannerRealFixture(t)
	shortRoot := scannerWindowsShortPath(t, fixture.root)
	if strings.EqualFold(filepath.Clean(shortRoot), filepath.Clean(fixture.root)) {
		t.Skip("short path alias is unavailable")
	}

	result, err := fixture.scanner.Scan(t.Context(), AuthorizedRootEvidence{RootPath: shortRoot})
	if err != nil {
		t.Fatalf("scan through authorized short root: %v", err)
	}
	scannerAssertCensus(t, result, IndexScanComplete, true, true)
}

func scannerWindowsShortPath(t *testing.T, path string) string {
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
