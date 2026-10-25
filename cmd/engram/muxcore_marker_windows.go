//go:build windows

package main

import (
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

var muxcoreMarkerCanUseLongPaths = func() bool {
	version := windows.RtlGetVersion()
	return version.MajorVersion > 10 || version.MajorVersion == 10 && (version.MinorVersion > 0 || version.BuildNumber >= 15063)
}()

// Match os.fixLongPath on kernels predating Go's build15063 long-path opt-in.
func muxcoreMarkerOpenPath(path string, canUseLongPaths bool) string {
	if canUseLongPaths {
		return path
	}
	if len(path) >= 4 && (path[:4] == `\??\` || os.IsPathSeparator(path[0]) && os.IsPathSeparator(path[1]) && path[2] == '?' && os.IsPathSeparator(path[3])) {
		return path
	}
	length := len(path)
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return path
		}
		length += len(cwd) + 1
	}
	if length < 248 {
		return path
	}
	full, err := windows.FullPath(path)
	if err != nil {
		// Like os.fixLongPath, leave conversion failures to the single native open.
		return path
	}
	if len(path) >= 2 && os.IsPathSeparator(path[0]) && os.IsPathSeparator(path[1]) {
		if len(path) >= 4 && path[2] == '.' && os.IsPathSeparator(path[3]) {
			return full
		}
		return `\\?\UNC\` + full[2:]
	}
	return `\\?\` + full
}

func readMuxcoreMarkerFile(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(muxcoreMarkerOpenPath(path, muxcoreMarkerCanUseLongPaths))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// The publisher atomically replaces markers with a DELETE-access handle.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return io.ReadAll(file)
}
