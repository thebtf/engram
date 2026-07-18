//go:build critical
// +build critical

// Package recovery_test contains @critical recovery tests.
package recovery_test

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const recoveryOwnerLabel = "engram.recovery.owner"

type recoveryOwnerMarker struct {
	SchemaVersion        int    `json:"schema_version"`
	Prefix               string `json:"prefix"`
	PID                  int    `json:"pid"`
	ProcessStartUTCTicks int64  `json:"process_start_time_utc_ticks"`
}

// @critical
func TestOperatorRecoveryMissingDockerPreservesActionableDependencyError(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Fatalf("recovery requires pwsh: %v", err)
	}

	repoRoot, script := recoveryPaths(t)
	cmd := exec.Command("pwsh", "-NoLogo", "-NoProfile", "-File", script, "-DockerCommand", "engram-deliberately-missing-docker")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("missing Docker dependency was accepted")
	}
	if !strings.Contains(string(output), "required dependency is unavailable: engram-deliberately-missing-docker") {
		t.Fatalf("missing dependency error was masked: %v\n%s", err, output)
	}
}

// @critical
func TestOperatorCanRestorePostgresBackup_RecoversDurableEngramDataAndRejectsUnsafeRestores(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("recovery requires docker: %v", err)
	}
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Fatalf("recovery requires pwsh: %v", err)
	}

	repoRoot, script := recoveryPaths(t)
	cmd := exec.Command("pwsh", "-NoLogo", "-NoProfile", "-File", script)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("operator recovery flow failed: %v\n%s", err, output)
	}
	// Deterministic mutation proof: the script must have observed an in-flight write
	// transaction (non-null backend_xid) via pg_stat_activity before interrupting.
	// A time-only approach (e.g. a fixed sleep) would not emit this line.
	if !strings.Contains(string(output), "RECOVERY MUTATION PROOF:") {
		t.Fatalf("interrupted-restore-negative did not prove an in-flight write transaction:\n%s", output)
	}
}

// @critical
func TestOperatorRecoveryScavengesKilledRunWithoutTouchingLiveOwner(t *testing.T) {
	for _, command := range []string{"docker", "pwsh"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("recovery requires %s: %v", command, err)
		}
	}
	repoRoot, script := recoveryPaths(t)
	image := os.Getenv("ENGRAM_RECOVERY_POSTGRES_IMAGE")
	if image == "" {
		image = "engram:r2-postgres"
	}
	dockerOutput(t, "image", "inspect", image)

	liveProcess := exec.Command("pwsh", "-NoLogo", "-NoProfile", "-Command", `$p=Get-Process -Id $PID; Write-Output ("{0}|{1}" -f $PID,$p.StartTime.ToUniversalTime().Ticks); [Console]::Out.Flush(); Start-Sleep -Seconds 120`)
	liveStdout, err := liveProcess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := liveProcess.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = liveProcess.Process.Kill()
		_, _ = liveProcess.Process.Wait()
	}()
	liveIdentity, err := bufio.NewReader(liveStdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read live recovery owner identity: %v", err)
	}
	identityParts := strings.Split(strings.TrimSpace(liveIdentity), "|")
	if len(identityParts) != 2 {
		t.Fatalf("invalid live recovery owner identity: %q", liveIdentity)
	}
	livePID, err := strconv.Atoi(identityParts[0])
	if err != nil {
		t.Fatal(err)
	}
	liveTicks, err := strconv.ParseInt(identityParts[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	livePrefix := newRecoveryPrefix(t)
	writeRecoveryOwner(t, livePrefix, livePID, liveTicks)
	createRecoveryResidue(t, livePrefix, image)
	defer removeRecoveryResidue(livePrefix)

	killed := exec.Command("pwsh", "-NoLogo", "-NoProfile", "-File", script)
	killed.Dir = repoRoot
	if err := killed.Start(); err != nil {
		t.Fatal(err)
	}
	killedPrefix := waitForRecoveryResources(t, killed.Process.Pid, 45*time.Second)
	defer removeRecoveryResidue(killedPrefix)
	if err := killed.Process.Kill(); err != nil {
		t.Fatalf("kill recovery process: %v", err)
	}
	_, _ = killed.Process.Wait()

	runRecoveryScavenger(t, repoRoot, script)
	assertRecoveryResidue(t, killedPrefix, false)
	assertRecoveryResidue(t, livePrefix, true)

	if err := liveProcess.Process.Kill(); err != nil {
		t.Fatalf("kill live owner: %v", err)
	}
	_, _ = liveProcess.Process.Wait()
	runRecoveryScavenger(t, repoRoot, script)
	assertRecoveryResidue(t, livePrefix, false)
}

// @critical
// TestOperatorRecoveryScavengerSkipsMalformedMarker proves that the scavenger
// exits successfully and preserves every directory whose .engram-recovery-owner.json
// is malformed (invalid JSON), has a missing required field, carries a wrong-type
// schema_version, or has a mismatched prefix. Only a valid schema-1 marker with
// a confirmed inactive owner may be scavenged; unknown/malformed state must fail
// closed against deletion.
func TestOperatorRecoveryScavengerSkipsMalformedMarker(t *testing.T) {
	for _, command := range []string{"docker", "pwsh"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("recovery requires %s: %v", command, err)
		}
	}
	repoRoot, script := recoveryPaths(t)
	image := os.Getenv("ENGRAM_RECOVERY_POSTGRES_IMAGE")
	if image == "" {
		image = "engram:r2-postgres"
	}
	dockerOutput(t, "image", "inspect", image)

	type markerCase struct {
		name    string
		content string
	}
	cases := []markerCase{
		{"malformed-json", `{not-json`},
		{"missing-schema-version", `{"prefix":"PLACEHOLDER"}`},
		{"wrong-schema-version", `{"schema_version":2,"prefix":"PLACEHOLDER"}`},
		{"schema-version-not-int", `{"schema_version":"1","prefix":"PLACEHOLDER"}`},
		{"wrong-prefix", `{"schema_version":1,"prefix":"engram-recovery-wrong"}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			prefix := newRecoveryPrefix(t)
			directory := filepath.Join(os.TempDir(), prefix)
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			// Explicit cleanup ensures no test residue remains regardless of outcome.
			t.Cleanup(func() { removeRecoveryResidue(prefix) })
			content := strings.ReplaceAll(tc.content, "PLACEHOLDER", prefix)
			if err := os.WriteFile(filepath.Join(directory, ".engram-recovery-owner.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			createRecoveryResidue(t, prefix, image)

			runRecoveryScavenger(t, repoRoot, script)
			// Malformed/unknown markers must be preserved — the scavenger fails closed
			// and must not delete a directory it cannot positively identify as stale.
			assertRecoveryResidue(t, prefix, true)
		})
	}
}

func newRecoveryPrefix(t *testing.T) string {
	t.Helper()
	bytes := make([]byte, 5)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	return "engram-recovery-" + hex.EncodeToString(bytes)
}

func writeRecoveryOwner(t *testing.T, prefix string, pid int, ticks int64) {
	t.Helper()
	directory := filepath.Join(os.TempDir(), prefix)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(recoveryOwnerMarker{SchemaVersion: 1, Prefix: prefix, PID: pid, ProcessStartUTCTicks: ticks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".engram-recovery-owner.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"postgres-password", "vault-key", "wrong-vault-key"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("synthetic-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func createRecoveryResidue(t *testing.T, prefix, image string) {
	t.Helper()
	label := recoveryOwnerLabel + "=" + prefix
	dockerOutput(t, "network", "create", "--label", label, prefix+"-net")
	dockerOutput(t, "volume", "create", "--label", label, prefix+"-probe-data")
	dockerOutput(t, "create", "--name", prefix+"-probe", "--label", label, image, "postgres", "--version")
}

func waitForRecoveryResources(t *testing.T, pid int, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "engram-recovery-*", ".engram-recovery-owner.json"))
		for _, markerPath := range matches {
			data, err := os.ReadFile(markerPath)
			if err != nil {
				continue
			}
			var marker recoveryOwnerMarker
			if json.Unmarshal(data, &marker) != nil || marker.PID != pid {
				continue
			}
			if recoveryDockerResourceCount(marker.Prefix) == 3 {
				return marker.Prefix
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("recovery process %d did not create labeled container, volume, and network", pid)
	return ""
}

func runRecoveryScavenger(t *testing.T, repoRoot, script string) {
	t.Helper()
	cmd := exec.Command("pwsh", "-NoLogo", "-NoProfile", "-File", script, "-ScavengeOnly")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recovery scavenger failed: %v\n%s", err, output)
	}
}

func assertRecoveryResidue(t *testing.T, prefix string, want bool) {
	t.Helper()
	_, statErr := os.Stat(filepath.Join(os.TempDir(), prefix))
	directoryExists := statErr == nil
	resourceCount := recoveryDockerResourceCount(prefix)
	if (want && (!directoryExists || resourceCount != 3)) || (!want && (directoryExists || resourceCount != 0)) {
		t.Fatalf("recovery residue %s: directory=%t resource_kinds=%d, want=%t", prefix, directoryExists, resourceCount, want)
	}
}

func recoveryDockerResourceCount(prefix string) int {
	filter := "label=" + recoveryOwnerLabel + "=" + prefix
	commands := [][]string{
		{"ps", "--all", "--quiet", "--filter", filter},
		{"volume", "ls", "--quiet", "--filter", filter},
		{"network", "ls", "--quiet", "--filter", filter},
	}
	count := 0
	for _, args := range commands {
		output, err := exec.Command("docker", args...).Output()
		if err == nil && strings.TrimSpace(string(output)) != "" {
			count++
		}
	}
	return count
}

func dockerOutput(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func removeRecoveryResidue(prefix string) {
	filter := "label=" + recoveryOwnerLabel + "=" + prefix
	for _, kind := range [][]string{{"ps", "--all", "--quiet"}, {"volume", "ls", "--quiet"}, {"network", "ls", "--quiet"}} {
		args := append(append([]string{}, kind...), "--filter", filter)
		output, _ := exec.Command("docker", args...).Output()
		for _, id := range strings.Fields(string(output)) {
			var cleanup []string
			switch kind[0] {
			case "ps":
				cleanup = []string{"rm", "--force", "--volumes", id}
			case "volume":
				cleanup = []string{"volume", "rm", "--force", id}
			default:
				cleanup = []string{"network", "rm", id}
			}
			_ = exec.Command("docker", cleanup...).Run()
		}
	}
	_ = os.RemoveAll(filepath.Join(os.TempDir(), prefix))
}

func recoveryPaths(t *testing.T) (string, string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve recovery test path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	script := filepath.Join(repoRoot, "scripts", "recovery", "verify-postgres-backup-restore.ps1")
	return repoRoot, script
}
