package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestProjectInitRefusesVisibleSiblingAnchor(t *testing.T) {
	for _, state := range []string{"branch", "packed-branch", "linked-preanchor", "detached-sibling"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			gitProjectInit(t, root, "init", "--quiet")
			gitProjectInit(t, root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Existing repository")
			gitProjectInit(t, root, "branch", "pre-anchor")
			anchor := []byte(`{"version":3,"project_id":"22222222-2222-4222-8222-222222222222","name":"existing","scope":"repository"}`)
			if err := os.WriteFile(filepath.Join(root, ".engram-project"), anchor, 0o600); err != nil {
				t.Fatal(err)
			}
			gitProjectInit(t, root, "add", "--", ".engram-project")
			if state == "detached-sibling" {
				tree, err := exec.Command("git", "-C", root, "write-tree").Output()
				if err != nil {
					t.Fatal(err)
				}
				commit, err := exec.Command("git", "-C", root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "commit-tree", strings.TrimSpace(string(tree)), "-p", "HEAD", "-m", "Visible detached anchor").Output()
				if err != nil {
					t.Fatal(err)
				}
				gitProjectInit(t, root, "rm", "--cached", "--quiet", "--", ".engram-project")
				if err := os.Remove(filepath.Join(root, ".engram-project")); err != nil {
					t.Fatal(err)
				}
				gitProjectInit(t, root, "worktree", "add", "--quiet", "--detach", filepath.Join(t.TempDir(), "anchored"), strings.TrimSpace(string(commit)))
			} else {
				gitProjectInit(t, root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Tracked anchor")
				if state == "linked-preanchor" {
					linked := filepath.Join(t.TempDir(), "preanchor")
					gitProjectInit(t, root, "worktree", "add", "--quiet", linked, "pre-anchor")
					root = linked
				} else {
					gitProjectInit(t, root, "checkout", "--quiet", "pre-anchor")
					if state == "packed-branch" {
						gitProjectInit(t, root, "pack-refs", "--all")
					}
				}
			}
			var output bytes.Buffer
			err := runProjectInit([]string{"--name", "Replacement"}, root, &output)
			if err == nil || !strings.Contains(err.Error(), "refusing to replace") {
				t.Fatalf("project init with visible %s anchor = %v, want refusal", state, err)
			}
			if _, err := os.Lstat(filepath.Join(root, ".engram-project")); !os.IsNotExist(err) {
				t.Fatalf("visible sibling anchor caused a replacement UUID: %v", err)
			}
			if output.Len() != 0 {
				t.Fatalf("refusal printed success: %q", output.String())
			}
		})
	}
}

func TestProjectInitAllowsNeverAnchoredLinkedAndPackedRepositories(t *testing.T) {
	for _, state := range []string{"linked", "packed"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			gitProjectInit(t, root, "init", "--quiet")
			gitProjectInit(t, root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Existing repository")
			if state == "linked" {
				linked := filepath.Join(t.TempDir(), "neveranchored")
				gitProjectInit(t, root, "worktree", "add", "--quiet", "--detach", linked, "HEAD")
				root = linked
			} else {
				gitProjectInit(t, root, "branch", "other-neveranchored")
				gitProjectInit(t, root, "pack-refs", "--all")
			}
			var output bytes.Buffer
			if err := runProjectInit([]string{"--name", "New logical project"}, root, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "Created") || !strings.Contains(output.String(), "UNTRACKED") {
				t.Fatalf("never-anchored initialization = %q", output.String())
			}
		})
	}
}

func TestProjectInitRefusesExcessVisibleRefEvidence(t *testing.T) {
	root := t.TempDir()
	gitProjectInit(t, root, "init", "--quiet")
	gitProjectInit(t, root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Existing repository")
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	var updates strings.Builder
	for i := range 129 {
		updates.WriteString("create refs/heads/evidence-" + strconv.Itoa(i) + " " + strings.TrimSpace(string(head)) + "\n")
	}
	command := exec.Command("git", "-C", root, "update-ref", "--stdin")
	command.Stdin = strings.NewReader(updates.String())
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create owned ref evidence: %v: %s", err, out)
	}
	var output bytes.Buffer
	if err := runProjectInit([]string{"--name", "Replacement"}, root, &output); err == nil {
		t.Fatal("excess ref evidence admitted a new anchor")
	}
	if _, err := os.Lstat(filepath.Join(root, ".engram-project")); !os.IsNotExist(err) {
		t.Fatalf("bounded inspection created a replacement anchor: %v", err)
	}
}

func TestProjectInitProtectsVisibleSiblingIndexAnchor(t *testing.T) {
	for _, state := range []string{"indexed", "untracked", "index-error", "indexed-newline"} {
		t.Run(state, func(t *testing.T) {
			if state == "indexed-newline" && runtime.GOOS == "windows" {
				t.Skip("Windows paths cannot contain newline characters")
			}
			root := t.TempDir()
			gitProjectInit(t, root, "init", "--quiet")
			gitProjectInit(t, root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Existing repository")
			name := "anchor sibling кириллица"
			if state == "indexed-newline" {
				name += "\nHEAD injected path line"
			}
			linked := filepath.Join(t.TempDir(), name)
			gitProjectInit(t, root, "worktree", "add", "--quiet", "--detach", linked, "HEAD")
			anchorBytes := []byte(`{"version":3,"project_id":"22222222-2222-4222-8222-222222222222","name":"existing","scope":"repository"}`)
			if err := os.WriteFile(filepath.Join(linked, ".engram-project"), anchorBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(state, "indexed") {
				gitProjectInit(t, linked, "add", "--", ".engram-project")
				anchor, err := projectidentity.DiscoverAnchorV3(linked, "repository")
				if err != nil || anchor.ProjectID != "22222222-2222-4222-8222-222222222222" {
					t.Fatalf("index-tracked producer acceptance = %+v: %v", anchor, err)
				}
				t.Log("actual producer accepts index-tracked anchor before commit")
			} else if state == "index-error" {
				index, err := exec.Command("git", "-C", linked, "rev-parse", "--git-path", "index").Output()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(strings.TrimSpace(string(index))); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(strings.TrimSpace(string(index)), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			index, err := exec.Command("git", "-C", linked, "rev-parse", "--git-path", "index").Output()
			if err != nil {
				t.Fatal(err)
			}
			var indexBefore []byte
			if state != "index-error" {
				indexBefore, err = os.ReadFile(strings.TrimSpace(string(index)))
				if err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			err = runProjectInit([]string{"--name", "Replacement"}, root, &output)
			if state == "untracked" {
				if err != nil || !strings.Contains(output.String(), "Created") {
					t.Fatalf("untracked sibling incorrectly blocked first use: %v: %q", err, output.String())
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "refusing to replace") {
					t.Fatalf("visible sibling %s initialization = %v, want refusal", state, err)
				}
				if _, err := os.Lstat(filepath.Join(root, ".engram-project")); !os.IsNotExist(err) {
					t.Fatalf("sibling index admitted replacement UUID: %v", err)
				}
				if output.Len() != 0 {
					t.Fatalf("refusal printed success: %q", output.String())
				}
			}
			after, err := os.ReadFile(filepath.Join(linked, ".engram-project"))
			if err != nil || !bytes.Equal(anchorBytes, after) {
				t.Fatalf("inspection altered sibling anchor: %v", err)
			}
			if state != "index-error" {
				indexAfter, err := os.ReadFile(strings.TrimSpace(string(index)))
				if err != nil || !bytes.Equal(indexBefore, indexAfter) {
					t.Fatalf("inspection altered sibling index: %v", err)
				}
			}
		})
	}
}

func TestProjectInitBindsGitEvidenceDespiteLocationOverrides(t *testing.T) {
	for _, state := range []string{"foreign-directory", "foreign-common", "foreign-index", "accepted-anchor"} {
		t.Run(state, func(t *testing.T) {
			root, foreign := t.TempDir(), t.TempDir()
			for _, repository := range []string{root, foreign} {
				gitProjectInit(t, repository, "init", "--quiet")
				gitProjectInit(t, repository, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Existing repository")
			}
			anchor := []byte(`{"version":3,"project_id":"22222222-2222-4222-8222-222222222222","name":"existing","scope":"repository"}`)
			if state == "foreign-index" {
				linked := filepath.Join(t.TempDir(), "indexed sibling")
				gitProjectInit(t, root, "worktree", "add", "--quiet", "--detach", linked, "HEAD")
				if err := os.WriteFile(filepath.Join(linked, ".engram-project"), anchor, 0o600); err != nil {
					t.Fatal(err)
				}
				gitProjectInit(t, linked, "add", "--", ".engram-project")
				if _, err := projectidentity.DiscoverAnchorV3(linked, "repository"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(root, ".engram-project"), anchor, 0o600); err != nil {
					t.Fatal(err)
				}
				gitProjectInit(t, root, "add", "--", ".engram-project")
				gitProjectInit(t, root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Tracked anchor")
				if state != "accepted-anchor" {
					gitProjectInit(t, root, "rm", "--quiet", "--", ".engram-project")
				}
			}
			overrides := map[string]string{}
			switch state {
			case "foreign-directory":
				overrides["GIT_DIR"] = filepath.Join(foreign, ".git")
				overrides["GIT_WORK_TREE"] = root
			case "foreign-common":
				overrides["GIT_COMMON_DIR"] = filepath.Join(foreign, ".git")
			case "foreign-index":
				overrides["GIT_INDEX_FILE"] = filepath.Join(foreign, ".git", "index")
			case "accepted-anchor":
				overrides = map[string]string{
					"GIT_DIR": filepath.Join(foreign, ".git"), "GIT_WORK_TREE": root,
					"GIT_INDEX_FILE": filepath.Join(foreign, ".git", "index"), "GIT_COMMON_DIR": filepath.Join(foreign, ".git"),
					"GIT_OBJECT_DIRECTORY":             filepath.Join(foreign, ".git", "objects"),
					"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(foreign, ".git", "objects"), "GIT_NAMESPACE": "foreign",
				}
			}
			for key, value := range overrides {
				t.Setenv(key, value)
			}
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "user.name")
			t.Setenv("GIT_CONFIG_VALUE_0", "Preserved non-location configuration")
			var output bytes.Buffer
			if state == "accepted-anchor" {
				observed, err := projectidentity.DiscoverAnchorV3(root, "repository")
				if err != nil || observed.ProjectID != "22222222-2222-4222-8222-222222222222" {
					t.Fatalf("selected accepted anchor was redirected: %v", err)
				}
				if err := runProjectInit([]string{"--name", "Replacement"}, root, &output); err != nil || !strings.Contains(output.String(), "TRACKED") || strings.Contains(output.String(), "UNTRACKED") {
					t.Fatalf("accepted selected anchor replay = %v: %q", err, output.String())
				}
			} else {
				_, discoveryErr := projectidentity.DiscoverAnchorV3(root, "repository")
				initErr := runProjectInit([]string{"--name", "Replacement"}, root, &output)
				t.Logf("selected-root discovery missing=%t initialization refused=%t", errors.Is(discoveryErr, projectidentity.ErrAnchorMissingV3), initErr != nil)
				if discoveryErr == nil || errors.Is(discoveryErr, projectidentity.ErrAnchorMissingV3) {
					t.Errorf("foreign Git metadata admitted fresh discovery: %v", discoveryErr)
				}
				if initErr == nil {
					t.Error("foreign Git metadata minted replacement UUID")
				}
				if _, err := os.Lstat(filepath.Join(root, ".engram-project")); !os.IsNotExist(err) {
					t.Errorf("refusal created a replacement anchor: %v", err)
				}
			}
			for key, value := range overrides {
				if os.Getenv(key) != value {
					t.Fatalf("producer mutated process environment key %s", key)
				}
			}
			if os.Getenv("GIT_CONFIG_VALUE_0") != "Preserved non-location configuration" {
				t.Fatal("producer mutated non-location Git configuration")
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
