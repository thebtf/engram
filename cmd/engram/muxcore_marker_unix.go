//go:build !windows

package main

import "os"

func readMuxcoreMarkerFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
