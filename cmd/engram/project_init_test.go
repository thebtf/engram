package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/projectidentity"
)

func TestProjectInitCreatesUntrackedAndReusesTrackedAnchor(t *testing.T) {
	root := t.TempDir()
	gitProjectInit(t, root, "init")
	var output bytes.Buffer
	if err := runProjectInit([]string{"--name", "Example Workspace"}, root, &output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".engram-project")
	if !strings.Contains(output.String(), path) || !strings.Contains(output.String(), "UNTRACKED") || !strings.Contains(output.String(), "git add -- .engram-project") {
		t.Fatalf("created output = %q", output.String())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := projectidentity.ParseAnchorV3(before)
	if err != nil || anchor.Name != "Example Workspace" || anchor.Scope != "repository" {
		t.Fatalf("created anchor = %+v, %v", anchor, err)
	}
	if _, err := projectidentity.DiscoverAnchorV3(root, "repository"); err == nil {
		t.Fatal("untracked anchor was discovered")
	}
	gitProjectInit(t, root, "add", "--", ".engram-project")
	if discovered, err := projectidentity.DiscoverAnchorV3(root, "repository"); err != nil || discovered != anchor {
		t.Fatalf("tracked discovery = %+v, %v", discovered, err)
	}
	output.Reset()
	if err := runProjectInit([]string{"--name", "Different Name"}, root, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "TRACKED") || strings.Contains(output.String(), "UNTRACKED") || !strings.Contains(output.String(), "Existing") {
		t.Fatalf("reused output = %q", output.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("reused anchor changed: %v", err)
	}
}

func TestProjectInitRefusesMalformedExistingAnchorWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	gitProjectInit(t, root, "init")
	path := filepath.Join(root, ".engram-project")
	before := []byte(`{"version":2,"name":"legacy"}`)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runProjectInit([]string{"--name", "Example Workspace"}, root, &output); err == nil {
		t.Fatal("malformed existing anchor accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("existing anchor overwritten: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("refusal printed success: %q", output.String())
	}
}

func TestProjectInitRefusesUnresolvableHEADWithoutAnchor(t *testing.T) {
	for _, state := range []string{"detached", "symbolic", "malformed-ref", "missing-ref"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			gitProjectInit(t, root, "init", "--quiet")
			gitProjectInit(t, root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Existing repository")
			if state == "malformed-ref" || state == "missing-ref" {
				anchor := []byte(`{"version":3,"project_id":"22222222-2222-4222-8222-222222222222","name":"existing","scope":"repository"}`)
				if err := os.WriteFile(filepath.Join(root, ".engram-project"), anchor, 0o600); err != nil {
					t.Fatal(err)
				}
				gitProjectInit(t, root, "add", "--", ".engram-project")
				gitProjectInit(t, root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Tracked anchor")
				gitProjectInit(t, root, "rm", "--quiet", "--", ".engram-project")
			}
			badObject := strings.Repeat("1", 40) + "\n"
			if state == "detached" {
				if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte(badObject), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if state == "symbolic" {
				gitProjectInit(t, root, "symbolic-ref", "HEAD", "refs/heads/broken-head")
				if err := os.WriteFile(filepath.Join(root, ".git", "refs", "heads", "broken-head"), []byte(badObject), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				branch, err := exec.Command("git", "-C", root, "symbolic-ref", "HEAD").Output()
				if err != nil {
					t.Fatal(err)
				}
				refPath := filepath.Join(root, ".git", filepath.FromSlash(strings.TrimSpace(string(branch))))
				if state == "malformed-ref" {
					if err := os.WriteFile(refPath, []byte("not-an-object\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Remove(refPath); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			err := runProjectInit([]string{"--name", "Replacement"}, root, &output)
			if err == nil || !strings.Contains(err.Error(), "refusing to replace") {
				t.Fatalf("project init with unresolved %s HEAD = %v, want refusal", state, err)
			}
			if _, err := os.Lstat(filepath.Join(root, ".engram-project")); !os.IsNotExist(err) {
				t.Fatalf("unresolved HEAD minted a replacement anchor: %v", err)
			}
			if output.Len() != 0 {
				t.Fatalf("refusal printed success: %q", output.String())
			}
		})
	}
}

func gitProjectInit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
