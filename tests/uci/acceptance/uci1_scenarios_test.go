package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// cmd/engram is an executable package and cannot be imported here. Shell the
// package-local installed scenario boundary so this acceptance surface runs the
// same one-lifecycle RED and preserves its exact missing-evidence failure.
func TestUCI1Scenarios(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal("locate UCI acceptance package")
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", "..", ".."))
	command := exec.Command("go", "test", "./cmd/engram", "-run", "^TestUCI1Scenarios$")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run command-package UCI-1 installed scenarios: %v\n%s", err, output)
	}
}
