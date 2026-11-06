package engramcore

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAutomaticOriginRecoveryProxyWindowsShortPath(t *testing.T) {
	testAutomaticOriginRecoveryProxy(t, func(path string) string {
		short := make([]uint16, windows.MAX_LONG_PATH)
		n, err := windows.GetShortPathName(windows.StringToUTF16Ptr(path), &short[0], uint32(len(short)))
		if err != nil || n == 0 || int(n) >= len(short) {
			t.Fatalf("get short path: n=%d err=%v", n, err)
		}
		result := windows.UTF16ToString(short[:n])
		if strings.EqualFold(result, path) {
			t.Skip("volume has no alternate short path for this fixture")
		}
		return result
	})
}
