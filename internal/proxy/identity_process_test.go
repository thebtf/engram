package proxy_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/proxy"
)

const (
	projectIdentityProcessHelperEnv       = "ENGRAM_TEST_PROJECT_IDENTITY_PROCESS_HELPER"
	projectIdentityProcessWorkspaceEnv    = "ENGRAM_TEST_PROJECT_IDENTITY_WORKSPACE"
	projectIdentityProcessHelperTest      = "^TestResolveProjectIdentityV2_ProcessHelper$"
	projectIdentityProcessAnchorFile      = ".engram-project-v2.json"
	projectIdentityProcessChildren        = 12
	projectIdentityProcessFreshRoundCount = 3
)

var strictProjectIdentityProcessAnchor = regexp.MustCompile(`^[0-9a-f]{32}$`)

type projectIdentityProcessResult struct {
	OK     bool   `json:"ok"`
	Anchor string `json:"anchor,omitempty"`
	Error  string `json:"error,omitempty"`
}

type projectIdentityProcessAnchor struct {
	Version uint32 `json:"version"`
	Anchor  string `json:"anchor"`
	Shared  bool   `json:"shared"`
}

type projectIdentityPublicationObservations struct {
	complete atomic.Int64
	partial  atomic.Int64
}

// TestResolveProjectIdentityV2_ProcessHelper is intentionally inert in an
// ordinary test run. The parent acceptance test re-executes this test binary
// with the helper environment set, waits for READY on stdout, and releases all
// children through stdin only after every child is blocked at the barrier.
func TestResolveProjectIdentityV2_ProcessHelper(t *testing.T) {
	if os.Getenv(projectIdentityProcessHelperEnv) != "1" {
		return
	}

	workspace := os.Getenv(projectIdentityProcessWorkspaceEnv)
	if workspace == "" {
		t.Fatal("child workspace is empty")
	}
	if _, err := fmt.Fprintln(os.Stdout, "READY"); err != nil {
		t.Fatalf("announce child readiness: %v", err)
	}

	var release [1]byte
	if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
		t.Fatalf("await parent release: %v", err)
	}
	if release[0] != 'G' {
		t.Fatalf("unexpected release token %q", release[0])
	}

	identity, err := proxy.ResolveProjectIdentityV2(workspace)
	result := projectIdentityProcessResult{OK: err == nil}
	if err != nil {
		result.Error = err.Error()
	} else {
		result.Anchor = identity.NonGitAnchor
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatalf("encode child result: %v", err)
	}
}

// TestResolveProjectIdentityV2_ChildProcessPublicationContract is a permanent
// process-boundary acceptance rail. Goroutines cannot stand in for independent
// client processes because the original R2 defect was an inter-process
// publication window.
func TestResolveProjectIdentityV2_ChildProcessPublicationContract(t *testing.T) {
	t.Run("concurrent first use publishes only complete bytes", func(t *testing.T) {
		for round := 0; round < projectIdentityProcessFreshRoundCount; round++ {
			workspace := t.TempDir()
			results, observations := runProjectIdentityProcessWave(t, workspace, projectIdentityProcessChildren, true)
			winner := requireProjectIdentityProcessConvergence(t, results)
			anchorBytes := requireCompleteProjectIdentityProcessAnchor(t, workspace, winner)
			if observations.complete.Load() == 0 {
				t.Fatal("parent monitor observed no complete final anchor")
			}
			if observations.partial.Load() != 0 {
				t.Fatalf("parent monitor observed %d zero-length, partial, or unreadable final anchors", observations.partial.Load())
			}
			assertNoProjectIdentityProcessResidue(t, workspace)
			t.Logf("fresh round=%d children=%d complete_observations=%d partial_observations=%d anchor_bytes=%d",
				round+1, len(results), observations.complete.Load(), observations.partial.Load(), len(anchorBytes))
		}
	})

	t.Run("existing valid anchor stays byte identical", func(t *testing.T) {
		workspace := t.TempDir()
		anchorPath := filepath.Join(workspace, projectIdentityProcessAnchorFile)
		original := []byte("{\n  \"version\": 2,\n  \"anchor\": \"00112233445566778899aabbccddeeff\",\n  \"shared\": false\n}\n")
		if err := os.WriteFile(anchorPath, original, 0o600); err != nil {
			t.Fatal(err)
		}

		for wave := 0; wave < 2; wave++ {
			results, _ := runProjectIdentityProcessWave(t, workspace, projectIdentityProcessChildren, false)
			winner := requireProjectIdentityProcessConvergence(t, results)
			if winner != "00112233445566778899aabbccddeeff" {
				t.Fatalf("wave %d resolved anchor %q", wave+1, winner)
			}
			got, err := os.ReadFile(anchorPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, original) {
				t.Fatalf("wave %d changed existing valid bytes:\n%s", wave+1, got)
			}
			assertNoProjectIdentityProcessResidue(t, workspace)
		}
	})

	t.Run("existing malformed anchor fails closed and stays byte identical", func(t *testing.T) {
		workspace := t.TempDir()
		anchorPath := filepath.Join(workspace, projectIdentityProcessAnchorFile)
		original := []byte(`{"version":2`)
		if err := os.WriteFile(anchorPath, original, 0o600); err != nil {
			t.Fatal(err)
		}

		results, _ := runProjectIdentityProcessWave(t, workspace, projectIdentityProcessChildren, false)
		requireProjectIdentityProcessInvalid(t, results)
		got, err := os.ReadFile(anchorPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("malformed anchor was repaired or replaced: %q", got)
		}
		assertNoProjectIdentityProcessResidue(t, workspace)
	})

	t.Run("open delayed partial writer fails closed and is not replaced", func(t *testing.T) {
		workspace := t.TempDir()
		anchorPath := filepath.Join(workspace, projectIdentityProcessAnchorFile)
		writer, err := os.OpenFile(anchorPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		writerOpen := true
		defer func() {
			if writerOpen {
				_ = writer.Close()
			}
		}()

		partial := []byte(`{"version":2`)
		if _, err := writer.Write(partial); err != nil {
			t.Fatal(err)
		}
		if err := writer.Sync(); err != nil {
			t.Fatal(err)
		}

		results, _ := runProjectIdentityProcessWave(t, workspace, projectIdentityProcessChildren, false)
		requireProjectIdentityProcessInvalid(t, results)
		got, err := os.ReadFile(anchorPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, partial) {
			t.Fatalf("open delayed partial writer was repaired or replaced: %q", got)
		}
		assertNoProjectIdentityProcessResidue(t, workspace)
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		writerOpen = false
	})
}

type projectIdentityChildProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr bytes.Buffer
	waited bool
}

func runProjectIdentityProcessWave(t *testing.T, workspace string, count int, monitor bool) ([]projectIdentityProcessResult, *projectIdentityPublicationObservations) {
	t.Helper()
	if count < 2 {
		t.Fatalf("child count=%d, want at least two independent processes", count)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	children := make([]projectIdentityChildProcess, count)
	defer func() {
		for i := range children {
			child := &children[i]
			if child.stdin != nil {
				_ = child.stdin.Close()
			}
			if child.cmd != nil && child.cmd.Process != nil && !child.waited {
				_ = child.cmd.Process.Kill()
				_ = child.cmd.Wait()
				child.waited = true
			}
		}
	}()

	seenPIDs := make(map[int]struct{}, count)
	for i := range children {
		cmd := exec.CommandContext(ctx, executable, "-test.run="+projectIdentityProcessHelperTest, "-test.count=1")
		cmd.Env = append(os.Environ(),
			projectIdentityProcessHelperEnv+"=1",
			projectIdentityProcessWorkspaceEnv+"="+workspace,
		)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("child %d stdin: %v", i, err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("child %d stdout: %v", i, err)
		}
		children[i].cmd = cmd
		children[i].stdin = stdin
		children[i].stdout = bufio.NewReader(stdout)
		cmd.Stderr = &children[i].stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %d: %v", i, err)
		}
		pid := cmd.Process.Pid
		if pid == os.Getpid() {
			t.Fatalf("child %d reused parent pid %d", i, pid)
		}
		if _, exists := seenPIDs[pid]; exists {
			t.Fatalf("duplicate child pid %d", pid)
		}
		seenPIDs[pid] = struct{}{}
	}

	for i := range children {
		ready, err := children[i].stdout.ReadString('\n')
		if err != nil {
			t.Fatalf("child %d readiness: %v; stderr=%s", i, err, children[i].stderr.String())
		}
		if strings.TrimSpace(ready) != "READY" {
			t.Fatalf("child %d readiness=%q; stderr=%s", i, ready, children[i].stderr.String())
		}
	}

	observations := &projectIdentityPublicationObservations{}
	stopMonitor := func() {}
	awaitComplete := func() error { return nil }
	if monitor {
		stopMonitor, awaitComplete = startProjectIdentityProcessMonitor(workspace, observations)
		defer stopMonitor()
	}

	releaseGate := make(chan struct{})
	releaseErrors := make(chan error, count)
	var releaseWG sync.WaitGroup
	for i := range children {
		releaseWG.Add(1)
		go func(i int) {
			defer releaseWG.Done()
			<-releaseGate
			_, writeErr := children[i].stdin.Write([]byte{'G'})
			closeErr := children[i].stdin.Close()
			if writeErr != nil {
				releaseErrors <- fmt.Errorf("child %d release write: %w", i, writeErr)
				return
			}
			if closeErr != nil {
				releaseErrors <- fmt.Errorf("child %d release close: %w", i, closeErr)
				return
			}
			releaseErrors <- nil
		}(i)
	}
	close(releaseGate)
	releaseWG.Wait()
	close(releaseErrors)
	for releaseErr := range releaseErrors {
		if releaseErr != nil {
			t.Fatal(releaseErr)
		}
	}

	results := make([]projectIdentityProcessResult, count)
	for i := range children {
		line, err := children[i].stdout.ReadString('\n')
		if err != nil {
			t.Fatalf("child %d result: %v; stderr=%s", i, err, children[i].stderr.String())
		}
		if err := json.Unmarshal([]byte(line), &results[i]); err != nil {
			t.Fatalf("child %d result=%q: %v; stderr=%s", i, line, err, children[i].stderr.String())
		}
		if err := children[i].cmd.Wait(); err != nil {
			children[i].waited = true
			t.Fatalf("child %d exit: %v; stderr=%s", i, err, children[i].stderr.String())
		}
		children[i].waited = true
	}

	if monitor {
		if err := awaitComplete(); err != nil {
			t.Fatal(err)
		}
		stopMonitor()
	}
	return results, observations
}

func startProjectIdentityProcessMonitor(workspace string, observations *projectIdentityPublicationObservations) (stop func(), awaitComplete func() error) {
	anchorPath := filepath.Join(workspace, projectIdentityProcessAnchorFile)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	completeObserved := make(chan struct{})
	var completeOnce sync.Once
	var stopOnce sync.Once

	go func() {
		defer close(done)
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			data, err := os.ReadFile(anchorPath)
			switch {
			case err == nil && validProjectIdentityProcessAnchorBytes(data):
				observations.complete.Add(1)
				completeOnce.Do(func() { close(completeObserved) })
			case err == nil:
				observations.partial.Add(1)
			case !errors.Is(err, os.ErrNotExist):
				observations.partial.Add(1)
			}
			runtime.Gosched()
		}
	}()

	stop = func() {
		stopOnce.Do(func() {
			close(stopCh)
			<-done
		})
	}
	awaitComplete = func() error {
		select {
		case <-completeObserved:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("parent monitor did not observe a complete final anchor")
		}
	}
	return stop, awaitComplete
}

func requireProjectIdentityProcessConvergence(t *testing.T, results []projectIdentityProcessResult) string {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("zero child results")
	}
	winner := results[0].Anchor
	for i, result := range results {
		if !result.OK || result.Error != "" || result.Anchor == "" || result.Anchor != winner {
			t.Fatalf("child %d did not converge: %#v; winner=%q", i, result, winner)
		}
	}
	return winner
}

func requireProjectIdentityProcessInvalid(t *testing.T, results []projectIdentityProcessResult) {
	t.Helper()
	for i, result := range results {
		if result.OK || result.Anchor != "" || !strings.Contains(result.Error, "PROJECT_IDENTITY_INVALID") {
			t.Fatalf("child %d did not fail closed: %#v", i, result)
		}
	}
}

func requireCompleteProjectIdentityProcessAnchor(t *testing.T, workspace, expectedAnchor string) []byte {
	t.Helper()
	anchorPath := filepath.Join(workspace, projectIdentityProcessAnchorFile)
	data, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !validProjectIdentityProcessAnchorBytes(data) {
		t.Fatalf("final anchor is not complete strict JSON:\n%s", data)
	}
	var anchor projectIdentityProcessAnchor
	if err := json.Unmarshal(data, &anchor); err != nil {
		t.Fatal(err)
	}
	if anchor.Anchor != expectedAnchor {
		t.Fatalf("final anchor %q differs from converged child result %q", anchor.Anchor, expectedAnchor)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(anchorPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("final anchor mode=%#o, want 0600", info.Mode().Perm())
		}
		t.Logf("final_anchor_mode=%#o", info.Mode().Perm())
	}
	return data
}

func validProjectIdentityProcessAnchorBytes(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var anchor projectIdentityProcessAnchor
	if err := decoder.Decode(&anchor); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false
	}
	if anchor.Version != 2 || !strictProjectIdentityProcessAnchor.MatchString(anchor.Anchor) || anchor.Shared {
		return false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",") == "anchor,shared,version"
}

func assertNoProjectIdentityProcessResidue(t *testing.T, workspace string) {
	t.Helper()
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, projectIdentityProcessAnchorFile+".tmp-") ||
			strings.HasPrefix(name, ".engram-project.tmp-") {
			t.Fatalf("temporary project-anchor residue: %s", name)
		}
	}
}
