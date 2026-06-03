package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMuxcoreDaemonVersionMatches(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "muxcore-daemon.version")
	currentExe := filepath.Join(t.TempDir(), "engram.exe")

	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, currentExe) {
		t.Fatal("missing marker must not match")
	}

	if err := os.WriteFile(path, []byte("v6.4.6\n"), 0o600); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, currentExe) {
		t.Fatal("legacy version-only marker must not match")
	}

	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234}`+"\n"), 0o600); err != nil {
		t.Fatalf("write marker without exe: %v", err)
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, currentExe) {
		t.Fatal("marker without executable path must not match")
	}

	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234,"exe":"`+filepath.ToSlash(currentExe)+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write marker with exe: %v", err)
	}
	if !muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, filepath.ToSlash(currentExe)) {
		t.Fatal("marker with matching version, pid, and executable should match")
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.7", 1234, currentExe) {
		t.Fatal("different daemon version must not match")
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 5678, currentExe) {
		t.Fatal("different daemon pid must not match")
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, filepath.Join(t.TempDir(), "other-engram.exe")) {
		t.Fatal("different daemon executable must not match")
	}
}
