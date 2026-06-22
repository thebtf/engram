package smoke

import (
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const defaultCR002SmokeDatabaseDSN = "postgres://engram:engram@localhost:5432/engram?sslmode=disable"

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
	cmd.Env = append(cmd.Env, "DATABASE_DSN="+cr002SmokeDatabaseDSN(t))

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CR-002 principal query smoke failed: %v\n%s", err, output)
	}
}

func cr002SmokeDatabaseDSN(t *testing.T) string {
	t.Helper()

	if dsn := os.Getenv("DATABASE_DSN"); dsn != "" {
		return dsn
	}
	if os.Getenv("CI") == "true" {
		t.Skip("DATABASE_DSN not set, skipping DB-backed CR-002 smoke in non-DB CI lane")
	}
	if !postgresURLReachable(defaultCR002SmokeDatabaseDSN) {
		t.Skip("default DATABASE_DSN not reachable, skipping DB-backed CR-002 smoke")
	}
	return defaultCR002SmokeDatabaseDSN
}

func postgresURLReachable(dsn string) bool {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" {
		return false
	}

	conn, err := net.DialTimeout("tcp", parsed.Host, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
