package legacyrelay

import (
	"path/filepath"
	"testing"
)

func TestRuntimeEndpointUsesBoundedGenerationScopedName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	first := mustGeneration(t, "daemon-generation-one")
	second := mustGeneration(t, "daemon-generation-two")

	firstEndpoint, err := RuntimeEndpoint(root, first)
	if err != nil {
		t.Fatalf("first RuntimeEndpoint: %v", err)
	}
	secondEndpoint, err := RuntimeEndpoint(root, second)
	if err != nil {
		t.Fatalf("second RuntimeEndpoint: %v", err)
	}
	if firstEndpoint == secondEndpoint {
		t.Fatal("generation-specific endpoints collided")
	}
	if name := runtimeEndpointFileName(first); filepath.Base(firstEndpoint) != name || len(name) > 64 {
		t.Fatalf("first endpoint name = %q, want bounded generation-scoped name", filepath.Base(firstEndpoint))
	}
	if name := runtimeEndpointFileName(second); filepath.Base(secondEndpoint) != name || len(name) > 64 {
		t.Fatalf("second endpoint name = %q, want bounded generation-scoped name", filepath.Base(secondEndpoint))
	}
}
