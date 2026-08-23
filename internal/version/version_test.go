package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
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
		filepath.Join("plugin", "engram", ".omp-plugin", "plugin.json"),
		filepath.Join(".claude-plugin", "marketplace.json"),
		filepath.Join(".claude-plugin", "plugin.json"),
		filepath.Join(".omp-plugin", "marketplace.json"),
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

func TestDockerSHAImageIdentityDoesNotOverrideDaemonCompatibility(t *testing.T) {
	if !semver.IsValid(Daemon) || semver.Canonical(Daemon) != Daemon {
		t.Fatalf("Daemon must remain canonical SemVer, got %q", Daemon)
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dockerfile, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if !strings.Contains(string(dockerfile), "commit_pattern='^sha-[0-9a-f]{40}$'") {
		t.Fatal("Dockerfile must continue to accept SHA image identities")
	}

	_, clientBuild, found := strings.Cut(string(dockerfile), "# Build client-side binary")
	if !found {
		t.Fatal("Dockerfile client build section is absent")
	}
	clientBuild, _, found = strings.Cut(clientBuild, "\n\n# --- Server image ---")
	if !found {
		t.Fatal("Dockerfile client build section is unterminated")
	}
	if !strings.Contains(clientBuild, "-X main.Version=${VERSION}") {
		t.Fatal("Dockerfile client build must retain image identity injection")
	}
	if strings.Contains(clientBuild, "internal/version.Daemon=") {
		t.Fatal("Dockerfile client build must not override source SemVer daemon compatibility with image identity")
	}
}
