//go:build !darwin

package legacyrelay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	linuxUnixSocketPathMax = 107
	linuxSocketDirPrefix   = "engram-hap-01b-"
)

func runtimeEndpointPath(baseDir, fileName string) (string, string, error) {
	endpoint := filepath.Join(baseDir, fileName)
	if runtime.GOOS == "windows" || len(endpoint) <= linuxUnixSocketPathMax {
		return endpoint, "", nil
	}

	directory := linuxRuntimeSocketDirectory(baseDir)
	endpoint = filepath.Join(directory, fileName)
	if len(endpoint) > linuxUnixSocketPathMax {
		return "", "", fmt.Errorf("legacy relay Unix socket path exceeds %d bytes", linuxUnixSocketPathMax)
	}
	return endpoint, directory, nil
}

func prepareRuntimeEndpoint(directory string) error {
	if directory == "" {
		return nil
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create private legacy relay socket directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("set private legacy relay socket directory permissions: %w", err)
	}
	return nil
}

func cleanupRuntimeEndpoint(directory string) {
	if directory != "" {
		_ = os.Remove(directory)
	}
}

func linuxRuntimeSocketDirectory(baseDir string) string {
	digest := sha256.Sum256([]byte(baseDir))
	return filepath.Join("/tmp", linuxSocketDirPrefix+hex.EncodeToString(digest[:16]))
}
