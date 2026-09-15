//go:build !darwin

package legacyrelay

import "path/filepath"

func runtimeEndpointPath(baseDir, fileName string) (string, string, error) {
	return filepath.Join(baseDir, fileName), "", nil
}

func prepareRuntimeEndpoint(string) error { return nil }

func cleanupRuntimeEndpoint(string) {
	// Non-Darwin builds do not create a fallback runtime endpoint directory.
}
