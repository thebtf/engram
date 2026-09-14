//go:build darwin

package legacyrelay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeEndpointUsesPrivateFallbackForLongDarwinRoot(t *testing.T) {
	root := filepath.Join("/", strings.Repeat("private-", 24))
	endpoint, err := RuntimeEndpoint(root, mustGeneration(t, "daemon-generation"))
	if err != nil {
		t.Fatalf("RuntimeEndpoint: %v", err)
	}
	if len(endpoint) > darwinUnixSocketPathMax {
		t.Fatalf("Darwin socket path length = %d, want at most %d", len(endpoint), darwinUnixSocketPathMax)
	}
	if strings.HasPrefix(endpoint, root) {
		t.Fatalf("long private root was not replaced: %q", endpoint)
	}

	directory := darwinRuntimeSocketDirectory(root)
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
