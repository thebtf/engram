package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type pluginManifest struct {
	Version string `json:"version"`
}

func TestDaemonVersionMatchesPluginManifests(t *testing.T) {
	if !strings.HasPrefix(Daemon, "v") {
		t.Fatalf("Daemon version must include v prefix, got %q", Daemon)
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	want := strings.TrimPrefix(Daemon, "v")

	for _, rel := range []string{
		filepath.Join("plugin", "engram", ".codex-plugin", "plugin.json"),
		filepath.Join("plugin", "engram", ".claude-plugin", "plugin.json"),
	} {
		data, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}

		var manifest pluginManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		if manifest.Version != want {
			t.Fatalf("%s version = %q, want %q from Daemon %q", rel, manifest.Version, want, Daemon)
		}
	}
}
