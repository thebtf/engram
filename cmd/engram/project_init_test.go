package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thebtf/engram/internal/projectidentity"
)

func TestLogicalProjectCLI(t *testing.T) {
	root := t.TempDir()
	gitProjectInit(t, root, "init", "--quiet")
	var output bytes.Buffer
	if err := runProjectInit([]string{"--name", "Example"}, root, &output); err != nil {
		t.Fatal(err)
	}
	var first projectidentity.Project
	if err := json.Unmarshal(output.Bytes(), &first); err != nil || !projectidentity.ValidProjectID(first.ProjectID) {
		t.Fatalf("CLI output: %s, %v", output.Bytes(), err)
	}
	t.Setenv("ENGRAM_URL", "")
	t.Setenv("ENGRAM_SERVER_URL", "")
	output.Reset()
	if err := runProjectCommand([]string{"resolve", "--root", root, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var repeated projectidentity.Project
	if err := json.Unmarshal(output.Bytes(), &repeated); err != nil || repeated != first {
		t.Fatalf("repeat: %+v %v", repeated, err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runProjectInit([]string{"--name", "Different"}, root, &output); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing ID was overwritten")
	}
	output.Reset()
	if err := runProjectCommand([]string{"resolve", "--json"}, &output); err == nil || !bytes.Contains(output.Bytes(), []byte("PROJECT_CONTEXT_REQUIRED")) {
		t.Fatal("missing root borrowed process cwd")
	}
}

func TestLogicalProjectCLIInvalidMarker(t *testing.T) {
	root := t.TempDir()
	before := []byte(`{"project_id":"not-a-uuid"}`)
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), before, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runProjectInit([]string{"--name", "Example"}, root, &output); err == nil {
		t.Fatal("invalid marker accepted")
	}
	after, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid marker overwritten")
	}
}

func gitProjectInit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
