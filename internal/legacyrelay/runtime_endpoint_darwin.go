//go:build darwin

package legacyrelay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const (
	darwinUnixSocketPathMax = 103
	darwinSocketDirPrefix   = "engram-hap-01b-"
)

func runtimeEndpointPath(baseDir, fileName string) (string, string, error) {
	endpoint := filepath.Join(baseDir, fileName)
	if len(endpoint) <= darwinUnixSocketPathMax {
		return endpoint, "", nil
	}

	directory := darwinRuntimeSocketDirectory(baseDir)
	endpoint = filepath.Join(directory, fileName)
	if len(endpoint) > darwinUnixSocketPathMax {
		return "", "", fmt.Errorf("legacy relay Darwin socket path exceeds %d bytes", darwinUnixSocketPathMax)
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

func darwinRuntimeSocketDirectory(baseDir string) string {
	digest := sha256.Sum256([]byte(baseDir))
	return filepath.Join("/tmp", darwinSocketDirPrefix+hex.EncodeToString(digest[:16]))
}
