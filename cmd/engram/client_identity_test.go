package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// Keep installation fixtures under the physical primary checkout: macOS
// t.TempDir can live beneath /var, a symlink to /private/var.
func clientIdentityTempDir(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	common := strings.TrimSpace(string(output))
	if filepath.Base(common) != ".git" {
		t.Fatalf("unexpected Git common directory %q", common)
	}
	primary, err := filepath.EvalSymlinks(filepath.Dir(common))
	if err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(primary, ".agent")
	info, err := os.Lstat(agent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unsafe primary coordination directory %q: %v", agent, err)
	}
	scratch := filepath.Join(agent, "tmp")
	if err := os.Mkdir(scratch, 0o700); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	info, err = os.Lstat(scratch)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unsafe primary scratch directory %q: %v", scratch, err)
	}
	dir, err := os.MkdirTemp(scratch, "client-identity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestDirectClientIdentityCanonicalizesTempAliasBeforeInstall(t *testing.T) {
	root := clientIdentityTempDir(t)
	alias := filepath.Join(root, "temp-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", "")
	t.Setenv("ENGRAM_DATA_DIR", filepath.Join(alias, "installation"))
	if _, err := directClientInstanceID(); err == nil {
		t.Fatal("followed symlink in installation path")
	}
	physical, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_DATA_DIR", filepath.Join(physical, "installation"))
	id, err := directClientInstanceID()
	if err != nil || !regexp.MustCompile(`^engram-[0-9a-f]{32}$`).MatchString(id) {
		t.Fatalf("physical installation identity = %q, error = %v", id, err)
	}
}

func TestDirectClientIdentityPersistsAcrossConcurrentStarts(t *testing.T) {
	dir := filepath.Join(clientIdentityTempDir(t), "installation")
	t.Setenv("ENGRAM_DATA_DIR", dir)
	t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", "")
	const count = 12
	var wg sync.WaitGroup
	ids := make(chan string, count)
	errs := make(chan error, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := directClientInstanceID()
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	for range count {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for id := range ids {
		if !regexp.MustCompile(`^engram-[0-9a-f]{32}$`).MatchString(id) {
			t.Fatalf("unexpected identity %q", id)
		}
		if first != "" && first != id {
			t.Fatalf("concurrent starts diverged: %q != %q", first, id)
		}
		first = id
	}
	stored, err := os.ReadFile(filepath.Join(dir, "client-instance-id"))
	if err != nil || string(stored) != first+"\n" {
		t.Fatalf("stored identity = %q, error = %v", stored, err)
	}
	if id, err := directClientInstanceID(); err != nil || id != first {
		t.Fatalf("restart identity = %q, error = %v", id, err)
	}
}

func TestDirectClientIdentityExplicitAndInvalidInputs(t *testing.T) {
	dir := filepath.Join(clientIdentityTempDir(t), "installation")
	t.Setenv("ENGRAM_DATA_DIR", dir)
	t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", "operator-install-alpha")
	id, err := directClientInstanceID()
	if err != nil || id != "operator-install-alpha" {
		t.Fatalf("explicit identity = %q, error = %v", id, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("explicit identity created installation directory: %v", err)
	}
	t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", "")
	for _, invalid := range []string{"bad\n", "http:private", "C:\\private", "/private/operator"} {
		t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", invalid)
		if _, err := directClientInstanceID(); err == nil {
			t.Fatalf("accepted invalid explicit identity %q", invalid)
		}
	}
	t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", "")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "client-instance-id")
	if err := os.WriteFile(destination, []byte("invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := directClientInstanceID(); err == nil {
		t.Fatal("accepted malformed persisted identity")
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), destination); err == nil {
		if _, err := directClientInstanceID(); err == nil {
			t.Fatal("followed identity symlink")
		}
		if err := os.Remove(destination); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err == nil {
		t.Setenv("ENGRAM_DATA_DIR", filepath.Join(link, "nested"))
		if _, err := directClientInstanceID(); err == nil {
			t.Fatal("followed installation directory symlink")
		}
	}
	t.Setenv("ENGRAM_DATA_DIR", "relative-state")
	if _, err := directClientInstanceID(); err == nil {
		t.Fatal("accepted relative installation data path")
	}
}
