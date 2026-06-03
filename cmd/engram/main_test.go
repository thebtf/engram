package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMuxcoreDaemonVersionMatches(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "muxcore-daemon.version")
	if muxcoreDaemonVersionMatches(path, "v6.4.6", 1234) {
		t.Fatal("missing marker must not match")
	}

	if err := os.WriteFile(path, []byte("v6.4.6\n"), 0o600); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}
	if muxcoreDaemonVersionMatches(path, "v6.4.6", 1234) {
		t.Fatal("legacy version-only marker must not match")
	}

	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234}`+"\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if !muxcoreDaemonVersionMatches(path, "v6.4.6", 1234) {
		t.Fatal("marker with matching version and pid should match")
	}
	if muxcoreDaemonVersionMatches(path, "v6.4.7", 1234) {
		t.Fatal("different daemon version must not match")
	}
	if muxcoreDaemonVersionMatches(path, "v6.4.6", 5678) {
		t.Fatal("different daemon pid must not match")
	}
}
