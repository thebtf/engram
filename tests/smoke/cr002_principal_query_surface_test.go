package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// @covers CR-002-principal-query-surface
func TestCR002PrincipalQuerySurfaceSmoke(t *testing.T) {
	t.Parallel()

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	args := []string{
		"test",
		"./internal/principalmemory",
		"./internal/db/gorm",
		"./internal/worker",
		"./internal/mcp",
		"-run",
		"Principal|RecallMemoryIncludePrincipals|RecallMemoryPrincipal|MemoryStore.*Principal",
		"-count=1",
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	if os.Getenv("DATABASE_DSN") == "" {
		cmd.Env = append(cmd.Env, "DATABASE_DSN=postgres://engram:engram@localhost:5432/engram?sslmode=disable")
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CR-002 principal query smoke failed: %v\n%s", err, output)
	}
}
