//go:build !darwin && !windows

package legacyrelay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const maxLinuxUnixSocketPath = 107

func TestRuntimeEndpointUsesPrivateFallbackForLongUnixRoot(t *testing.T) {
	root := filepath.Join("/", strings.Repeat("private-", 24))
	endpoint, directory, err := runtimeEndpointForListener(root, mustGeneration(t, "daemon-generation"))
	if err != nil {
		t.Fatalf("runtimeEndpointForListener: %v", err)
	}
	if len(endpoint) > maxLinuxUnixSocketPath {
		t.Fatalf("Linux socket path length = %d, want at most %d", len(endpoint), maxLinuxUnixSocketPath)
	}
	if directory == "" || strings.HasPrefix(endpoint, root) {
		t.Fatalf("long private root was not replaced: endpoint=%q directory=%q", endpoint, directory)
	}

	t.Cleanup(func() { cleanupRuntimeEndpoint(directory) })
	if err := prepareRuntimeEndpoint(directory); err != nil {
		t.Fatalf("prepareRuntimeEndpoint: %v", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat private socket directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("private socket directory mode = %o, want 700", info.Mode().Perm())
	}
	cleanupRuntimeEndpoint(directory)
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleaned private socket directory stat error = %v, want not exist", err)
	}
}
