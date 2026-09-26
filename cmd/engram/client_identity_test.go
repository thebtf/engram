package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

func TestDirectClientIdentityPersistsAcrossConcurrentStarts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "installation")
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
	dir := filepath.Join(t.TempDir(), "installation")
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
