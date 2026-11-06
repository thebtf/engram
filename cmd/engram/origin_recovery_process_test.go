package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/projectidentity"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func TestAutomaticOriginCandidateMCPProcess(t *testing.T) {
	launcher := os.Getenv("ENGRAM_TEST_MCP_LAUNCHER")
	if launcher == "" {
		t.Skip("explicit candidate MCP launcher not supplied")
	}
	scratch := clientIdentityTempDir(t)
	binary := filepath.Join(scratch, "engram-origin-smoke")
	goName := "go"
	if runtime.GOOS == "windows" {
		binary += ".exe"
		goName += ".exe"
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", goName), "build", "-buildvcs=false", "-p=1", "-o", binary, ".")
	build.Env = projectidentity.RepositoryGitEnvironmentV3()
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("candidate build: %v: %s", err, raw)
	}
	root := filepath.Join(scratch, "caller")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet", "--template="}, {"remote", "add", "origin", "https://example.test/Fixture/Repo.git"}} {
		git := exec.Command("git", append([]string{"-C", root}, args...)...)
		git.Env = projectidentity.RepositoryGitEnvironmentV3()
		if output, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v: %s", err, output)
		}
	}
	marker := filepath.Join(root, ".engram-project")
	original := []byte(`{"name":"Original fixture"}`)
	if err := os.WriteFile(marker, original, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := newMemoryTestBackend(t)
	var gets, posts atomic.Int32
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			backend.server.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			posts.Add(1)
			http.Error(w, "unexpected mutation", 500)
			return
		}
		gets.Add(1)
		if r.URL.Path != "/api/projects/resolve" || r.URL.Query().Get("repository_locator") != "example.test/Fixture/Repo" || r.Header.Get("Authorization") != "Bearer "+memoryTestTokenA {
			http.Error(w, "denied", 403)
			return
		}
		fmt.Fprintf(w, `{"status":"resolved","binding":{"id":"%s","project_key":"%s","identity_status":"active","match":"manual_alias"}}`, memoryTestProjectA, memoryTestProjectA)
	}), &http2.Server{}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, launcher, "-binary", binary, "-cwd", root, "-mode", "tool", "-tool", "recall", "-args", `{"query":"","limit":20}`, "-timeout", "60", "--", "--session-memory")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "ENGRAM_") || strings.HasPrefix(upper, "CLAUDE_") || strings.HasPrefix(upper, "MCP_MUX_") || strings.HasPrefix(upper, "GIT_") || upper == "HOME" || upper == "USERPROFILE" || upper == "APPDATA" || upper == "LOCALAPPDATA" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "ENGRAM_URL="+server.URL, "ENGRAM_TOKEN="+memoryTestTokenA, "ENGRAM_DATA_DIR="+filepath.Join(scratch, "installation"), "ENGRAM_TLS_CA=", "CLAUDE_SESSION_ID=fixture-a", "HOME="+scratch, "USERPROFILE="+scratch, "APPDATA="+scratch, "LOCALAPPDATA="+scratch)
	resolve := exec.CommandContext(ctx, binary, "project", "resolve", "--root", root, "--json")
	resolve.Dir = root
	resolve.Env = cmd.Env
	resolved, err := resolve.CombinedOutput()
	if err != nil || !strings.Contains(string(resolved), memoryTestProjectA) {
		t.Fatalf("candidate project resolve: %v: %s", err, resolved)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("candidate launcher: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "fixture-public-a") || strings.Contains(string(output), "fixture-public-b") {
		t.Fatalf("unexpected scoped recall: %s", output)
	}
	if gets.Load() != 2 || posts.Load() != 0 {
		t.Fatalf("lookup=%d bootstrap=%d", gets.Load(), posts.Load())
	}
	current, err := os.ReadFile(marker)
	if err != nil || string(current) != string(original) {
		t.Fatal("marker changed")
	}
	if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
		t.Fatal("cache created")
	}
}
