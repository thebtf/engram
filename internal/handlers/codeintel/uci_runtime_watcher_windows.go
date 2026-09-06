//go:build windows

package codeintel

import (
	"io/fs"
	"os"
	"syscall"
)

func uciRuntimeWatcherIsReparse(info os.FileInfo) bool {
	if info.Mode()&fs.ModeSymlink != 0 {
		return true
	}
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
