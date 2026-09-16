package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// cmd/engram is an executable package and cannot be imported by this external
// acceptance package. Run its synthetic, package-local receipt contract through
// the Go tool so this acceptance surface covers the same encoder used by the
// installed harness without requiring a database, service, or real run.
func TestUCIInstalledReceiptContract(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal("locate UCI acceptance package")
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", "..", ".."))
	command := exec.Command("go", "test", "./cmd/engram", "-run", "^TestUCIInstalledReceiptContract$", "-count=1")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run command-package installed receipt contract: %v\n%s", err, output)
	}
}
