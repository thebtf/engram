package recoveryreceipt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/operability"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	muxipc "github.com/thebtf/mcp-mux/muxcore/ipc"
	muxserverid "github.com/thebtf/mcp-mux/muxcore/serverid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	gormlib "gorm.io/gorm"
)

const (
	ar2FixtureAnchorProjectID = "11111111-1111-4111-8111-111111111111"
	ar2FixtureLegacyID        = "ar2-comparison-legacy-17"
	ar2RuntimeFixtureOptIn    = "ENGRAM_AR2_COMPARISON_RUNTIME_FIXTURE"
)

func TestAR2CandidateCommitRequiresCleanWorktree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("clean", func(t *testing.T) {
		commit, err := ar2CandidateCommitFromWorktree(ctx, ar2CandidateTestWorktree(t, ctx))
		if err != nil || !validCommit(commit) {
			t.Fatalf("clean worktree commit = %q, %v", commit, err)
		}
	})

	for _, testCase := range []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{
			name: "ordinary tracked change",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("modified\n"), 0o600); err != nil {
					t.Fatal("modify tracked fixture")
				}
			},
		},
		{
			name: "staged change",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("staged\n"), 0o600); err != nil {
					t.Fatal("modify staged fixture")
				}
				ar2RunFixtureGit(t, context.Background(), root, "add", "tracked.txt")
			},
		},
		{
			name: "untracked change",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
					t.Fatal("write untracked fixture")
				}
			},
		},
		{
			name: "assume unchanged scan relevant source",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				ar2RunFixtureGit(t, context.Background(), root, "update-index", "--assume-unchanged", "--", "internal/fixture.go")
				if err := os.WriteFile(filepath.Join(root, "internal", "fixture.go"), []byte("package internal\n\nconst Changed = true\n"), 0o600); err != nil {
					t.Fatal("modify index-hidden fixture source")
				}
			},
		},
		{
			name: "ignored scan relevant source",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				path := filepath.Join(root, "ignored", "forged.go")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal("create ignored fixture source directory")
				}
				if err := os.WriteFile(path, []byte("package ignored\n"), 0o600); err != nil {
					t.Fatal("write ignored fixture source")
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := ar2CandidateTestWorktree(t, ctx)
			testCase.mutate(t, root)
			if _, err := ar2CandidateCommitFromWorktree(ctx, root); err == nil {
				t.Fatal("candidate guard accepted dirty source")
			}
		})
	}
}

func TestAR2CandidateCommitAllowsOnlyFixtureGeneratedArtifacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, testCase := range []struct {
		name    string
		files   map[string]string
		allowed bool
	}{
		{
			name: "allows exact generated metadata and output",
			files: map[string]string{
				".specify/feature.json":                 "{}\n",
				"plugin/openclaw-engram/dist/client.js": "export {};\n",
			},
			allowed: true,
		},
		{
			name: "rejects generated metadata near miss",
			files: map[string]string{
				".specify/feature-copy.json": "{}\n",
			},
		},
		{
			name: "rejects OpenClaw output near miss",
			files: map[string]string{
				"plugin/openclaw-engram/dist-evil/client.js": "export {};\n",
			},
		},
		{
			name: "rejects arbitrary ignored source",
			files: map[string]string{
				"ignored/forged.go": "package ignored\n",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := ar2CandidateTestWorktree(t, ctx)
			for path, content := range testCase.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(path))), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			_, err := ar2CandidateCommitFromWorktree(ctx, root)
			if testCase.allowed && err != nil {
				t.Fatalf("fixture generated artifacts refused: %v", err)
			}
			if !testCase.allowed && err == nil {
				t.Fatal("fixture candidate guard accepted noncanonical ignored source")
			}
		})
	}
}

func TestAR2OpenClawSnapshotPathGuards(t *testing.T) {
	candidateRoot := t.TempDir()
	openClawDist, err := ar2ExactSnapshotOpenClawDist(candidateRoot, filepath.Join(candidateRoot, "plugin", "openclaw-engram", "dist", "client.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ar2RequireNoInheritedOpenClawDist(candidateRoot); err != nil {
		t.Fatalf("fresh candidate snapshot refused: %v", err)
	}
	if err := os.MkdirAll(openClawDist, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ar2RequireNoInheritedOpenClawDist(candidateRoot); err == nil {
		t.Fatal("accepted inherited OpenClaw dist")
	}
	if _, err := ar2ExactSnapshotOpenClawDist(candidateRoot, filepath.Join(candidateRoot, "plugin", "openclaw-engram", "dist-evil", "client.js")); err == nil {
		t.Fatal("accepted noncanonical OpenClaw dist path")
	}
}

func TestAR2CandidatePayloadFingerprintBindsNestedExecutableArtifacts(t *testing.T) {
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal("create fixture payload directory")
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal("write fixture payload artifact")
		}
	}
	newPayload := func() (root, server, daemon, openClawDist, hookSource string) {
		root = t.TempDir()
		server = filepath.Join(root, "bin", "engram-server")
		daemon = filepath.Join(root, "bin", "engram")
		openClawDist = filepath.Join(root, "openclaw", "dist")
		hookSource = filepath.Join(root, "hooks")
		write(server, "server-v1")
		write(daemon, "daemon-v1")
		write(filepath.Join(openClawDist, "client.js"), "import './nested/availability.js';\n")
		write(filepath.Join(openClawDist, "nested", "availability.js"), "export const state = 'available';\n")
		write(filepath.Join(hookSource, "lib.js"), "module.exports = require('./nested/project-identity-v3.js');\n")
		write(filepath.Join(hookSource, "nested", "project-identity-v3.js"), "module.exports = { version: 3 };\n")
		return root, server, daemon, openClawDist, hookSource
	}
	artifacts := func(root, server, daemon, openClawDist, hookSource string) []ar2PayloadArtifact {
		return []ar2PayloadArtifact{
			{label: "server", root: root, path: server},
			{label: "daemon", root: root, path: daemon},
			{label: "openclaw-dist", root: root, path: openClawDist},
			{label: "hook-source", root: root, path: hookSource},
		}
	}

	root, server, daemon, openClawDist, hookSource := newPayload()
	baseline := ar2CandidatePayloadFingerprint(t, artifacts(root, server, daemon, openClawDist, hookSource)...)
	identicalRoot, identicalServer, identicalDaemon, identicalOpenClawDist, identicalHookSource := newPayload()
	if identical := ar2CandidatePayloadFingerprint(t, artifacts(identicalRoot, identicalServer, identicalDaemon, identicalOpenClawDist, identicalHookSource)...); baseline != identical {
		t.Fatal("payload fingerprint included an absolute fixture path")
	}

	write(filepath.Join(openClawDist, "nested", "availability.js"), "export const state = 'unavailable';\n")
	afterOpenClawMutation := ar2CandidatePayloadFingerprint(t, artifacts(root, server, daemon, openClawDist, hookSource)...)
	if afterOpenClawMutation == baseline {
		t.Fatal("nested built OpenClaw artifact did not change payload fingerprint")
	}

	write(filepath.Join(hookSource, "nested", "project-identity-v3.js"), "module.exports = { version: 4 };\n")
	if afterHookMutation := ar2CandidatePayloadFingerprint(t, artifacts(root, server, daemon, openClawDist, hookSource)...); afterHookMutation == afterOpenClawMutation {
		t.Fatal("nested Hook source artifact did not change payload fingerprint")
	}
}

func TestAR2CandidatePayloadFingerprintRejectsUnsafeClosurePaths(t *testing.T) {
	write := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal("create fixture payload directory")
		}
		if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
			t.Fatal("write fixture payload artifact")
		}
	}
	newPayload := func(t *testing.T) (root, payload string) {
		t.Helper()
		root = t.TempDir()
		payload = filepath.Join(root, "payload")
		write(t, filepath.Join(payload, "entry.js"))
		return root, payload
	}
	assertRejected := func(t *testing.T, root, path string) {
		t.Helper()
		if err := ar2ValidatePayloadArtifacts(ar2PayloadArtifact{label: "payload", root: root, path: path}); err == nil {
			t.Fatal("accepted unsafe payload closure path")
		}
	}
	link := func(t *testing.T, target, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal("create linked fixture payload directory")
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks are unavailable: %v", err)
		}
	}

	t.Run("top-level symlink", func(t *testing.T) {
		root, payload := newPayload(t)
		linked := filepath.Join(root, "linked.js")
		link(t, filepath.Join(payload, "entry.js"), linked)
		assertRejected(t, root, linked)
	})
	t.Run("top-level ancestor symlink", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()
		write(t, filepath.Join(target, "entry.js"))
		linked := filepath.Join(root, "linked")
		link(t, target, linked)
		assertRejected(t, root, filepath.Join(linked, "entry.js"))
	})
	t.Run("nested file symlink", func(t *testing.T) {
		root, payload := newPayload(t)
		target := filepath.Join(t.TempDir(), "entry.js")
		write(t, target)
		link(t, target, filepath.Join(payload, "nested", "entry.js"))
		assertRejected(t, root, payload)
	})
	t.Run("nested directory symlink", func(t *testing.T) {
		root, payload := newPayload(t)
		target := t.TempDir()
		write(t, filepath.Join(target, "entry.js"))
		link(t, target, filepath.Join(payload, "nested"))
		assertRejected(t, root, payload)
	})
	t.Run("path escape", func(t *testing.T) {
		root, _ := newPayload(t)
		escaped := filepath.Join(t.TempDir(), "entry.js")
		write(t, escaped)
		assertRejected(t, root, escaped)
	})
	t.Run("non-regular file", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("mkfifo is unavailable on Windows")
		}
		root := t.TempDir()
		pipe := filepath.Join(root, "payload.pipe")
		if output, err := exec.Command("mkfifo", pipe).CombinedOutput(); err != nil {
			t.Skipf("mkfifo is unavailable: %v: %s", err, output)
		}
		assertRejected(t, root, pipe)
	})
	t.Run("junction reparse component", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("junctions are Windows-only")
		}
		root := t.TempDir()
		target := t.TempDir()
		write(t, filepath.Join(target, "entry.js"))
		junction := filepath.Join(root, "junction")
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
			t.Skipf("junctions are unavailable: %v: %s", err, output)
		}
		assertRejected(t, root, junction)
	})
}

// TestAR2ComparisonRuntimeFixture exercises every public V3 adapter against one
// disposable pgvector PostgreSQL instance. It intentionally collects only resolver
// responses and persisted redacted rows; no ComparisonObservation is test-injected.
func TestAR2ComparisonRuntimeFixture(t *testing.T) {
	if os.Getenv(ar2RuntimeFixtureOptIn) != "1" {
		t.Skipf("runtime fixture requires %s=1", ar2RuntimeFixtureOptIn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	root := ar2FixtureRoot(t)
	candidateCommit := ar2CandidateCommit(t, ctx, root)
	fixtureDir, removeFixtureDir := ar2FixtureDir(t, ctx, root)
	defer removeFixtureDir()
	candidate := ar2StartCandidateSnapshot(t, ctx, root, fixtureDir, candidateCommit)
	defer candidate.Close(t)
	candidateRoot := candidate.root

	postgres := ar2StartPostgres(t, ctx)
	defer postgres.Close(t)

	store := ar2OpenStore(t, ctx, postgres.dsn)
	var server *ar2FixtureServer
	var cleanupCorrelations []projectidentity.CorrelationV3
	var projectKey string
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		if server != nil {
			server.Close()
		}
		if err := ar2DeleteOwnedRows(store.GetDB(), cleanupCorrelations, projectKey); err != nil {
			t.Errorf("fixture database cleanup: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("fixture database close: %v", err)
		}
	}
	defer cleanup()

	fixtureMasterToken := ar2FixtureMasterToken(t)
	server = ar2StartServer(t, ctx, candidateRoot, fixtureDir, candidateCommit, postgres.dsn, fixtureMasterToken)
	ar2WaitReady(t, ctx, server.baseURL)
	ar2AssertCandidateServerHealth(t, server, candidateCommit)
	seed := ar2RegisterV3Binding(t, ctx, server.grpcAddr, fixtureMasterToken, ar2ProtoIdentity("binding-registration-fixture-client"))
	server.Close()
	server = ar2StartServer(t, ctx, candidateRoot, fixtureDir, candidateCommit, postgres.dsn, "")
	ar2WaitReady(t, ctx, server.baseURL)
	ar2AssertCandidateServerHealth(t, server, candidateCommit)
	projectKey = seed.projectKey
	cleanupCorrelations = append(cleanupCorrelations, seed.registrationCorrelation)
	protectedBefore := ar2ProtectedTableCounts(t, store.GetDB())
	if protectedBefore.projects != 1 || protectedBefore.identifiers != 0 || protectedBefore.merges != 0 || protectedBefore.mergeSources != 0 {
		t.Fatalf("explicit V3 registration did not establish the expected protected-table baseline: %#v", protectedBefore)
	}

	pluginDir := filepath.Join(candidateRoot, "plugin", "openclaw-engram")
	openClawClient := ar2BuildOpenClawClient(t, ctx, pluginDir)
	daemon := ar2StartCandidateDaemon(t, ctx, candidateRoot, fixtureDir)
	defer daemon.Close()

	attestation := ar2AttestCandidate(t, candidateRoot, candidateCommit, server, daemon, openClawClient, filepath.Join(candidateRoot, "plugin", "engram", "hooks"))
	capture, err := newAR2ControlledFixtureCapture(attestation)
	if err != nil {
		t.Fatal("attest exact candidate fixture")
	}
	if err := capture.record(ar2ControlledFixtureCallables[0].callable, ar2ResponseCorrelation(t, ar2Initialize(t, ctx, server.grpcAddr, ar2ProtoIdentity("grpc-fixture-client-17"), "grpc-fixture-attempt-17"), projectKey)); err != nil {
		t.Fatal("capture gRPC fixture callable")
	}
	if err := capture.record(ar2ControlledFixtureCallables[1].callable, ar2InvokeHTTP(t, ctx, server.baseURL, ar2WireIdentity("http-fixture-client-17"), "http-fixture-attempt-17", projectKey)); err != nil {
		t.Fatal("capture HTTP fixture callable")
	}
	if err := capture.record(ar2ControlledFixtureCallables[2].callable, ar2InvokeHook(t, ctx, candidateRoot, fixtureDir, server.baseURL, ar2WireIdentity("hook-fixture-client-17"), projectKey)); err != nil {
		t.Fatal("capture Hook fixture callable")
	}
	if err := capture.record(ar2ControlledFixtureCallables[3].callable, ar2InvokeDaemon(t, ctx, daemon, server.grpcAddr, ar2DaemonRepository(t, fixtureDir), projectKey)); err != nil {
		t.Fatal("capture daemon fixture callable")
	}
	if err := capture.record(ar2ControlledFixtureCallables[4].callable, ar2InvokeOpenClaw(t, ctx, fixtureDir, server.baseURL, ar2WireIdentity("openclaw-fixture-client-17"), projectKey, openClawClient)); err != nil {
		t.Fatal("capture OpenClaw fixture callable")
	}

	comparisonCorrelations := ar2FixtureCorrelationList(t, capture)
	cleanupCorrelations = append(cleanupCorrelations, comparisonCorrelations...)
	protectedAfter := ar2ProtectedTableCounts(t, store.GetDB())
	expectedCallRows := int64(len(comparisonCorrelations))
	if protectedAfter.projects != protectedBefore.projects || protectedAfter.identifiers != protectedBefore.identifiers || protectedAfter.merges != protectedBefore.merges || protectedAfter.mergeSources != protectedBefore.mergeSources || protectedAfter.comparisons != protectedBefore.comparisons+expectedCallRows || protectedAfter.attempts != protectedBefore.attempts+expectedCallRows {
		t.Fatalf("comparison callables changed protected tables or failed to emit exactly one comparison/attempt each: before=%#v after=%#v", protectedBefore, protectedAfter)
	}
	ar2AssertFixtureCorrelationReadbacks(t, store.GetDB(), comparisonCorrelations)

	receipt, err := buildAR2IdentityExpandReceiptFromControlledFixture(ctx, store, capture)
	if err != nil {
		t.Fatal("build AR-2 receipt from controlled fixture")
	}
	ar2AssertReceiptCoverage(t, receipt)
	encodedReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal("encode AR-2 receipt")
	}
	for _, forbidden := range []string{ar2FixtureAnchorProjectID, projectKey, "fixture-client"} {
		if bytes.Contains(encodedReceipt, []byte(forbidden)) {
			t.Fatal("AR-2 receipt exposed non-redacted fixture identity")
		}
	}

	cleanup()
	postgres.Close(t)
	receiptDigest := sha256.Sum256(encodedReceipt)
	t.Logf("AR-2 runtime receipt: callables=5/5 denominators=1 persisted_readback=5 cleanup=zero sha256:%s", hex.EncodeToString(receiptDigest[:]))
}

type ar2ProtectedCounts struct {
	projects     int64
	identifiers  int64
	merges       int64
	mergeSources int64
	comparisons  int64
	attempts     int64
}

type ar2FixtureSeed struct {
	projectKey              string
	registrationCorrelation projectidentity.CorrelationV3
}

type ar2PostgresFixture struct {
	name   string
	dsn    string
	closed bool
}

type ar2FixtureServer struct {
	binary             string
	payloadRoot        string
	baseURL            string
	grpcAddr           string
	sourceCommit       string
	payloadFingerprint string
	cmd                *exec.Cmd
	done               chan struct{}
	once               sync.Once
}
type ar2FixtureDaemon struct {
	binary      string
	payloadRoot string
	controlPath string
	cmd         *exec.Cmd
	done        chan struct{}
	once        sync.Once
}

type ar2FixtureDaemonMarker struct {
	PID              int    `json:"pid"`
	DaemonGeneration string `json:"daemon_generation"`
	Exe              string `json:"exe"`
}

type ar2CandidateSnapshot struct {
	sourceRoot string
	root       string
	once       sync.Once
}

func ar2FixtureDir(t *testing.T, ctx context.Context, root string) (string, func()) {
	t.Helper()
	output, err := ar2Command(ctx, root, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		t.Fatal("resolve fixture coordination root")
	}
	commonDirectory := strings.TrimSpace(string(output))
	if commonDirectory == "" {
		t.Fatal("resolve fixture coordination root")
	}
	parent := filepath.Join(filepath.Dir(filepath.Clean(commonDirectory)), ".agent", "tmp")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal("create fixture workspace")
	}
	dir, err := os.MkdirTemp(parent, "ar2-comparison-runtime-")
	if err != nil {
		t.Fatal("create fixture directory")
	}
	relative, err := filepath.Rel(parent, dir)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		_ = os.RemoveAll(dir)
		t.Fatal("fixture directory escaped canonical scratch parent")
	}
	return dir, func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove fixture directory: %v", err)
		}
	}
}

func ar2StartCandidateSnapshot(t *testing.T, ctx context.Context, sourceRoot, fixtureDir, candidateCommit string) *ar2CandidateSnapshot {
	t.Helper()
	snapshot := &ar2CandidateSnapshot{sourceRoot: sourceRoot, root: filepath.Join(fixtureDir, "candidate-source")}
	if _, err := ar2Command(ctx, sourceRoot, "git", "worktree", "add", "--detach", snapshot.root, candidateCommit); err != nil {
		t.Fatal("materialize exact clean candidate snapshot")
	}
	snapshotCommit, err := ar2CandidateCommitFromWorktree(ctx, snapshot.root)
	if err != nil || snapshotCommit != candidateCommit {
		snapshot.Close(t)
		t.Fatal("candidate snapshot is not the exact clean commit")
	}
	if err := ar2RequireNoInheritedOpenClawDist(snapshot.root); err != nil {
		snapshot.Close(t)
		t.Fatal("candidate snapshot inherited OpenClaw dist before build")
	}
	return snapshot
}

func ar2RequireNoInheritedOpenClawDist(candidateRoot string) error {
	dist := filepath.Join(candidateRoot, "plugin", "openclaw-engram", "dist")
	if _, err := os.Lstat(dist); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect candidate snapshot OpenClaw dist: %w", err)
	}
	return fmt.Errorf("candidate snapshot inherited OpenClaw dist")
}

func ar2ExactSnapshotOpenClawDist(candidateRoot, openClawClient string) (string, error) {
	dist := filepath.Join(candidateRoot, "plugin", "openclaw-engram", "dist")
	if filepath.Clean(openClawClient) != filepath.Join(dist, "client.js") {
		return "", fmt.Errorf("OpenClaw client is not the exact snapshot dist client")
	}
	return dist, nil
}

func (snapshot *ar2CandidateSnapshot) Close(t *testing.T) {
	t.Helper()
	if snapshot == nil {
		return
	}
	snapshot.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := ar2Command(ctx, snapshot.sourceRoot, "git", "worktree", "remove", "--force", snapshot.root); err != nil {
			t.Errorf("remove exact candidate snapshot: %v", err)
			return
		}
		if _, err := os.Stat(snapshot.root); !os.IsNotExist(err) {
			t.Errorf("exact candidate snapshot remains after cleanup")
		}
	})
}

type ar2InitializeCapture struct {
	pb.UnimplementedEngramServiceServer
	upstream  pb.EngramServiceClient
	responses chan *pb.InitializeResponse
}

func (capture *ar2InitializeCapture) Initialize(ctx context.Context, request *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	outgoing := ctx
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		outgoing = metadata.NewOutgoingContext(ctx, incoming.Copy())
	}
	response, err := capture.upstream.Initialize(outgoing, request)
	if err == nil {
		select {
		case capture.responses <- response:
		default:
		}
	}
	return response, err
}

func ar2FixtureMasterToken(t *testing.T) string {
	t.Helper()
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal("generate fixture-only master token")
	}
	return "ar2-fixture-" + hex.EncodeToString(bytes)
}

func ar2FixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate fixture source root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func ar2CandidateCommit(t *testing.T, ctx context.Context, root string) string {
	t.Helper()
	commit, err := ar2CandidateCommitFromWorktree(ctx, root)
	if err != nil {
		t.Fatal("fixture candidate is not an exact clean worktree")
	}
	return commit
}

func ar2CandidateCommitFromWorktree(ctx context.Context, root string) (string, error) {
	if err := requireCandidateWorktreeRoot(root); err != nil {
		return "", fmt.Errorf("validate candidate worktree root: %w", err)
	}
	if err := requireCleanGitWorktreeWithIgnoredScanRelevantSourceAllowance(root, ar2FixtureIgnoredScanRelevantSourceAllowance); err != nil {
		return "", fmt.Errorf("validate clean candidate worktree: %w", err)
	}
	return candidateCommit(root)
}

func ar2FixtureIgnoredScanRelevantSourceAllowance(relative string) bool {
	canonical := filepath.ToSlash(filepath.Clean(relative))
	if canonical == ".specify/feature.json" || canonical == "plugin/openclaw-engram/dist" {
		return true
	}
	return strings.HasPrefix(canonical, "plugin/openclaw-engram/dist/")
}

func ar2CandidateTestWorktree(t *testing.T, ctx context.Context) string {
	t.Helper()
	root := t.TempDir()
	ar2RunFixtureGit(t, ctx, root, "init")
	ar2RunFixtureGit(t, ctx, root, "config", "user.email", "fixture@example.invalid")
	ar2RunFixtureGit(t, ctx, root, "config", "user.name", "Fixture")
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o700); err != nil {
		t.Fatal("create candidate fixture source directory")
	}
	for path, content := range map[string]string{
		".gitignore":          "ignored/\n.specify/\nplugin/openclaw-engram/dist/\nplugin/openclaw-engram/dist-evil/\n",
		"tracked.txt":         "clean\n",
		"internal/fixture.go": "package internal\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
			t.Fatalf("write candidate fixture %s: %v", path, err)
		}
	}
	ar2RunFixtureGit(t, ctx, root, "add", ".")
	ar2RunFixtureGit(t, ctx, root, "commit", "-m", "fixture")
	return root
}

func ar2RunFixtureGit(t *testing.T, ctx context.Context, root string, args ...string) {
	t.Helper()
	if _, err := ar2Command(ctx, root, "git", args...); err != nil {
		t.Fatalf("git %s failed", strings.Join(args, " "))
	}
}

func ar2StartPostgres(t *testing.T, ctx context.Context) *ar2PostgresFixture {
	t.Helper()
	name := "engram-ar2-comparison-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := ar2Command(ctx, "", "docker", "run", "-d", "--rm", "--name", name,
		"--label", "engram.fixture=ar2-comparison-runtime",
		"-e", "POSTGRES_PASSWORD=ar2-fixture-password",
		"-e", "POSTGRES_DB=engram",
		"-p", "127.0.0.1::5432",
		"pgvector/pgvector:pg17"); err != nil {
		t.Fatal("start isolated pgvector fixture")
	}
	fixture := &ar2PostgresFixture{name: name}
	portOutput, err := ar2Command(ctx, "", "docker", "port", name, "5432/tcp")
	if err != nil {
		fixture.Close(t)
		t.Fatal("read isolated pgvector port")
	}
	address := strings.TrimSpace(strings.Split(string(portOutput), "\n")[0])
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		fixture.Close(t)
		t.Fatal("parse isolated pgvector port")
	}
	fixture.dsn = "postgres://postgres:ar2-fixture-password@127.0.0.1:" + port + "/engram?sslmode=disable"
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		db, openErr := sql.Open("postgres", fixture.dsn)
		if openErr == nil {
			pingCtx, cancel := context.WithTimeout(ctx, time.Second)
			pingErr := db.PingContext(pingCtx)
			cancel()
			_ = db.Close()
			if pingErr == nil {
				return fixture
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	fixture.Close(t)
	t.Fatal("isolated pgvector fixture did not become ready")
	return nil
}

func (fixture *ar2PostgresFixture) Close(t *testing.T) {
	t.Helper()
	if fixture == nil || fixture.closed {
		return
	}
	fixture.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := ar2Command(ctx, "", "docker", "rm", "-f", fixture.name); err != nil {
		t.Errorf("remove isolated pgvector fixture")
		return
	}
	if _, err := ar2Command(ctx, "", "docker", "container", "inspect", fixture.name); err == nil {
		t.Errorf("isolated pgvector fixture remains after cleanup")
	}
}

func ar2OpenStore(t *testing.T, ctx context.Context, dsn string) *gormdb.Store {
	t.Helper()
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 4, LogLevel: 0})
	if err != nil {
		t.Fatal("migrate isolated pgvector fixture")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.GetRawDB().PingContext(pingCtx); err != nil {
		_ = store.Close()
		t.Fatal("verify isolated pgvector fixture")
	}
	return store
}

func ar2RegisterV3Binding(t *testing.T, ctx context.Context, address, masterToken string, identity *pb.ProjectIdentityV3) ar2FixtureSeed {
	t.Helper()
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal("connect explicit V3 registration authority")
	}
	defer connection.Close()
	callCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+masterToken)
	callCtx, cancel := context.WithTimeout(callCtx, 10*time.Second)
	defer cancel()
	response, err := pb.NewEngramServiceClient(connection).RegisterProjectIdentityV3(callCtx, &pb.RegisterProjectIdentityV3Request{ProjectIdentityV3: identity})
	if err != nil {
		t.Fatal("establish fixture V3 binding through explicit registration")
	}
	resolution := response.GetProjectResolutionV3()
	if resolution == nil || resolution.GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED || resolution.GetProjectKey() == "" || resolution.GetResolvedScope() != "repository" {
		t.Fatal("explicit V3 registration did not return an active binding")
	}
	correlation, err := projectidentity.NewCorrelationV3(resolution.GetCorrelation())
	if err != nil {
		t.Fatal("explicit V3 registration did not return a valid correlation")
	}
	return ar2FixtureSeed{projectKey: resolution.GetProjectKey(), registrationCorrelation: correlation}
}

func ar2ProtectedTableCounts(t *testing.T, db *gormlib.DB) ar2ProtectedCounts {
	t.Helper()
	var counts ar2ProtectedCounts
	for _, count := range []struct {
		model any
		value *int64
		name  string
	}{
		{&gormdb.Project{}, &counts.projects, "projects"},
		{&gormdb.ProjectIdentifier{}, &counts.identifiers, "project identifiers"},
		{&gormdb.ProjectMergeAudit{}, &counts.merges, "project merge audits"},
		{&gormdb.ProjectMergeAuditSource{}, &counts.mergeSources, "project merge audit sources"},
		{&gormdb.ProjectIdentityComparison{}, &counts.comparisons, "project comparisons"},
		{&gormdb.ProjectResolutionAttempt{}, &counts.attempts, "project resolution attempts"},
	} {
		if err := db.Model(count.model).Count(count.value).Error; err != nil {
			t.Fatalf("count %s: %v", count.name, err)
		}
	}
	return counts
}

func ar2StartServer(t *testing.T, ctx context.Context, root, fixtureDir, candidateCommit, dsn, masterToken string) *ar2FixtureServer {
	t.Helper()
	binary := filepath.Join(fixtureDir, "engram-server.exe")
	if _, err := ar2Command(ctx, root, "go", "build", "-tags", "fts5", "-ldflags", "-X main.SourceCommit="+candidateCommit, "-o", binary, "./cmd/engram-server"); err != nil {
		t.Fatal("build fixture server")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("allocate fixture server port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	home := filepath.Join(fixtureDir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal("create isolated server home")
	}
	authDisabled := "true"
	if masterToken != "" {
		authDisabled = "false"
	}
	server := &ar2FixtureServer{
		binary:      binary,
		payloadRoot: fixtureDir,
		baseURL:     "http://127.0.0.1:" + strconv.Itoa(port),
		grpcAddr:    "127.0.0.1:" + strconv.Itoa(port),
		done:        make(chan struct{}),
	}
	server.cmd = exec.Command(binary)
	server.cmd.Dir = root
	overrides := map[string]string{
		"DATABASE_DSN":         dsn,
		"DATABASE_MAX_CONNS":   "4",
		"ENGRAM_AUTH_DISABLED": authDisabled,
		"ENGRAM_WORKER_HOST":   "127.0.0.1",
		"ENGRAM_WORKER_PORT":   strconv.Itoa(port),
		"HOME":                 home,
		"USERPROFILE":          home,
		"APPDATA":              filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":         filepath.Join(home, "AppData", "Local"),
	}
	server.cmd.Env = ar2FixtureEnvironment(t, masterToken, overrides)
	server.cmd.Stdout = io.Discard
	server.cmd.Stderr = io.Discard
	if err := server.cmd.Start(); err != nil {
		t.Fatal("start isolated fixture server")
	}
	go func() {
		_ = server.cmd.Wait()
		close(server.done)
	}()
	return server
}

func (server *ar2FixtureServer) Close() {
	if server == nil {
		return
	}
	server.once.Do(func() {
		select {
		case <-server.done:
			return
		default:
		}
		_ = server.cmd.Process.Kill()
		<-server.done
	})
}

func ar2WaitReady(t *testing.T, ctx context.Context, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	for {
		if ctx.Err() != nil {
			t.Fatal("fixture server did not become ready")
		}
		response, err := client.Get(baseURL + "/api/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func ar2AssertCandidateServerHealth(t *testing.T, server *ar2FixtureServer, candidateCommit string) {
	t.Helper()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(server.baseURL + "/api/health")
	if err != nil {
		t.Fatal("read candidate server health")
	}
	defer response.Body.Close()
	var health struct {
		Status       string `json:"status"`
		SourceCommit string `json:"source_commit"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&health) != nil || health.Status != "ready" || health.SourceCommit != candidateCommit {
		t.Fatal("candidate server health is not bound to the exact source commit")
	}
	server.sourceCommit = health.SourceCommit
	server.payloadFingerprint = ar2CandidatePayloadFingerprint(t, ar2PayloadArtifact{label: "server", root: server.payloadRoot, path: server.binary})
	if !validFingerprint(server.payloadFingerprint) {
		t.Fatal("candidate server payload fingerprint is invalid")
	}
}

func ar2AttestCandidate(t *testing.T, candidateRoot, candidateCommit string, server *ar2FixtureServer, daemon *ar2FixtureDaemon, openClawClient, hookSource string) ar2CandidateAttestation {
	t.Helper()
	serverArtifact := ar2PayloadArtifact{label: "server", root: server.payloadRoot, path: server.binary}
	if server.sourceCommit != candidateCommit || server.payloadFingerprint != ar2CandidatePayloadFingerprint(t, serverArtifact) {
		t.Fatal("candidate server health does not bind the built server payload")
	}
	openClawDist, err := ar2ExactSnapshotOpenClawDist(candidateRoot, openClawClient)
	if err != nil {
		t.Fatal(err)
	}

	payloadFingerprint := ar2CandidatePayloadFingerprint(t,
		serverArtifact,
		ar2PayloadArtifact{label: "daemon", root: daemon.payloadRoot, path: daemon.binary},
		ar2PayloadArtifact{label: "openclaw-dist", root: candidateRoot, path: openClawDist},
		ar2PayloadArtifact{label: "hook-source", root: candidateRoot, path: hookSource},
	)
	return ar2CandidateAttestation{
		candidate: ar2CandidateIdentity{
			SourceCommit:                candidateCommit,
			CandidateCommit:             candidateCommit,
			CandidatePayloadFingerprint: payloadFingerprint,
		},
		healthSourceCommit:        server.sourceCommit,
		runtimePayloadFingerprint: payloadFingerprint,
		v2Compatibility:           AR2V2ReadCompatible,
		capabilityState:           AR2CapabilityAvailable,
	}
}

func ar2ProtoIdentity(clientInstanceID string) *pb.ProjectIdentityV3 {
	return &pb.ProjectIdentityV3{
		Version:         3,
		AnchorProjectId: ar2FixtureAnchorProjectID,
		Name:            "ar2-comparison-project",
		Scope:           "repository",
		NormalizedGitRemotes: []string{
			"example.invalid/acme/engram",
		},
		LegacyIdentifiers: []*pb.ProjectLegacyIdentifierV3{{
			Scheme:     "binding_v2",
			Value:      ar2FixtureLegacyID,
			Provenance: "fixture",
		}},
		ClientInstanceId: clientInstanceID,
	}
}

func ar2WireIdentity(clientInstanceID string) map[string]any {
	return map[string]any{
		"version":                3,
		"anchor_project_id":      ar2FixtureAnchorProjectID,
		"name":                   "ar2-comparison-project",
		"scope":                  "repository",
		"normalized_git_remotes": []string{"example.invalid/acme/engram"},
		"legacy_identifiers": []map[string]string{{
			"scheme": "binding_v2", "value": ar2FixtureLegacyID, "provenance": "fixture",
		}},
		"client_instance_id": clientInstanceID,
	}
}

func ar2Initialize(t *testing.T, ctx context.Context, address string, identity *pb.ProjectIdentityV3, requestID string) *pb.InitializeResponse {
	t.Helper()
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal("connect fixture gRPC client")
	}
	defer connection.Close()
	callCtx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("x-request-id", requestID)), 10*time.Second)
	defer cancel()
	response, err := pb.NewEngramServiceClient(connection).Initialize(callCtx, &pb.InitializeRequest{ProjectIdentityV3: identity})
	if err != nil {
		t.Fatal("call fixture gRPC Initialize")
	}
	return response
}

func ar2ResponseCorrelation(t *testing.T, response *pb.InitializeResponse, projectKey string) projectidentity.CorrelationV3 {
	t.Helper()
	resolution := response.GetProjectResolutionV3()
	if resolution == nil || resolution.GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED || resolution.GetProjectKey() != projectKey || resolution.GetResolvedScope() != "repository" {
		t.Fatal("V3 response did not return the registered resolution")
	}
	correlation, err := projectidentity.NewCorrelationV3(resolution.GetCorrelation())
	if err != nil {
		t.Fatal("V3 response did not return a valid correlation")
	}
	return correlation
}

func ar2InvokeHTTP(t *testing.T, ctx context.Context, baseURL string, descriptor map[string]any, requestID, projectKey string) projectidentity.CorrelationV3 {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"project_descriptor": descriptor, "identity_only": true})
	if err != nil {
		t.Fatal("encode V3 HTTP fixture request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/context/inject", bytes.NewReader(payload))
	if err != nil {
		t.Fatal("construct V3 HTTP fixture request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", requestID)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("call fixture HTTP context route")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("fixture HTTP context route refused a valid descriptor")
	}
	var body struct {
		Resolution struct {
			Outcome       string `json:"outcome"`
			Correlation   string `json:"correlation"`
			ProjectKey    string `json:"project_key"`
			ResolvedScope string `json:"resolved_scope"`
		} `json:"project_resolution_v3"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal("decode fixture HTTP context response")
	}
	if body.Resolution.Outcome != "PROJECT_RESOLVED" || body.Resolution.ProjectKey != projectKey || body.Resolution.ResolvedScope != "repository" {
		t.Fatal("fixture HTTP response did not return the registered resolution")
	}
	correlation, err := projectidentity.NewCorrelationV3(body.Resolution.Correlation)
	if err != nil {
		t.Fatal("fixture HTTP response did not return a valid correlation")
	}
	return correlation
}

func ar2InvokeHook(t *testing.T, ctx context.Context, root, fixtureDir, baseURL string, descriptor map[string]any, projectKey string) projectidentity.CorrelationV3 {
	t.Helper()
	driver := filepath.Join(fixtureDir, "hook-driver.cjs")
	source := `
const hook = require(process.env.AR2_HOOK_LIB);
const nativeFetch = globalThis.fetch;
const descriptor = JSON.parse(process.env.AR2_DESCRIPTOR);
const expectedBody = JSON.stringify({ project_descriptor: descriptor, identity_only: true });
let requests = 0;
let correlation = '';
globalThis.fetch = async (url, init = {}) => {
  requests += 1;
  if (requests !== 1 || url !== process.env.AR2_SERVER_URL + '/api/context/inject' || init.method !== 'POST') throw new Error('unexpected Hook request');
  const headers = Object.fromEntries(new Headers(init.headers).entries());
  if (Object.keys(headers).sort().join(',') !== 'content-type,x-engram-project-identity-adapter,x-request-id' || headers['content-type'] !== 'application/json' || headers['x-engram-project-identity-adapter'] !== 'hook' || headers['x-request-id'] !== 'hook-fixture-attempt-17' || String(init.body) !== expectedBody) throw new Error('unexpected Hook request body or headers');
  const response = await nativeFetch(url, init);
  const body = await response.clone().json();
  const resolution = body?.project_resolution_v3;
  if (!response.ok || resolution?.outcome !== 'PROJECT_RESOLVED' || resolution?.project_key !== process.env.AR2_PROJECT_KEY || resolution?.resolved_scope !== descriptor.scope || typeof resolution?.correlation !== 'string' || !resolution.correlation) throw new Error('unexpected Hook resolver response');
  correlation = resolution.correlation;
  return response;
};
(async () => {
  const context = { ProjectDescriptorV3: descriptor };
  await hook.registerProjectIdentityV3(context, undefined, { serverURL: process.env.AR2_SERVER_URL, requestID: 'hook-fixture-attempt-17' });
  if (requests !== 1 || !correlation || context.Project !== process.env.AR2_PROJECT_KEY) throw new Error('Hook resolver result missing');
  process.stdout.write(JSON.stringify({ step: 'hook', correlation }));
})().catch(() => process.exit(1));
`
	if err := os.WriteFile(driver, []byte(source), 0o600); err != nil {
		t.Fatal("write Hook runtime driver")
	}
	output, err := ar2CommandEnv(ctx, root, ar2FixtureEnvironment(t, "", map[string]string{
		"AR2_HOOK_LIB":    filepath.Join(root, "plugin", "engram", "hooks", "lib.js"),
		"AR2_DESCRIPTOR":  string(ar2MustJSON(t, descriptor)),
		"AR2_SERVER_URL":  baseURL,
		"AR2_PROJECT_KEY": projectKey,
	}), "node", driver)
	if err != nil {
		t.Fatal("run actual Hook V3 HTTP callable")
	}
	var result struct {
		Step        string `json:"step"`
		Correlation string `json:"correlation"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.Step != "hook" {
		t.Fatal("Hook V3 callable did not return its fixed correlation slot")
	}
	correlation, err := projectidentity.NewCorrelationV3(result.Correlation)
	if err != nil {
		t.Fatal("Hook V3 callable did not capture a resolver correlation")
	}
	return correlation
}

func ar2DaemonRepository(t *testing.T, fixtureDir string) string {
	t.Helper()
	repo := filepath.Join(fixtureDir, "daemon-repository")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal("create daemon repository fixture")
	}
	anchor := fmt.Sprintf(`{"version":3,"project_id":%q,"name":"ar2-comparison-project","scope":"repository"}\n`, ar2FixtureAnchorProjectID)
	if err := os.WriteFile(filepath.Join(repo, ".engram-project"), []byte(anchor), 0o600); err != nil {
		t.Fatal("write daemon V3 anchor")
	}
	for _, arguments := range [][]string{{"init"}, {"remote", "add", "origin", "https://example.invalid/acme/engram.git"}} {
		if _, err := ar2Command(context.Background(), repo, "git", arguments...); err != nil {
			t.Fatal("prepare daemon repository fixture")
		}
	}
	return repo
}

func ar2StartCandidateDaemon(t *testing.T, ctx context.Context, candidateRoot, fixtureDir string) *ar2FixtureDaemon {
	t.Helper()
	daemon := &ar2FixtureDaemon{
		binary:      filepath.Join(fixtureDir, "engram-daemon.exe"),
		payloadRoot: fixtureDir,
		controlPath: muxserverid.DaemonControlPath(fixtureDir, "engram"),
		done:        make(chan struct{}),
	}
	if _, err := ar2Command(ctx, candidateRoot, "go", "build", "-o", daemon.binary, "./cmd/engram"); err != nil {
		t.Fatal("build candidate daemon")
	}
	daemon.cmd = exec.Command(daemon.binary, "--muxcore-daemon")
	daemon.cmd.Dir = candidateRoot
	daemon.cmd.Env = ar2FixtureEnvironment(t, "", map[string]string{
		"TEMP":                      fixtureDir,
		"TMP":                       fixtureDir,
		"HOME":                      filepath.Join(fixtureDir, "daemon-home"),
		"USERPROFILE":               filepath.Join(fixtureDir, "daemon-home"),
		"LOCALAPPDATA":              filepath.Join(fixtureDir, "daemon-local"),
		"APPDATA":                   filepath.Join(fixtureDir, "daemon-roaming"),
		"ENGRAM_DATA_DIR":           filepath.Join(fixtureDir, "daemon-data"),
		"ENGRAM_CLIENT_INSTANCE_ID": "ar2-daemon-fixture-client-17",
	})
	daemon.cmd.Stdout = io.Discard
	daemon.cmd.Stderr = io.Discard
	if err := daemon.cmd.Start(); err != nil {
		t.Fatal("start candidate daemon")
	}
	go func() {
		_ = daemon.cmd.Wait()
		close(daemon.done)
	}()
	ar2WaitCandidateDaemon(t, ctx, daemon)
	return daemon
}

func ar2WaitCandidateDaemon(t *testing.T, ctx context.Context, daemon *ar2FixtureDaemon) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		response, err := muxcontrol.SendWithTimeout(daemon.controlPath, muxcontrol.Request{Cmd: "status"}, time.Second)
		if err == nil && response != nil && response.OK {
			var status struct {
				PID              int    `json:"pid"`
				DaemonGeneration string `json:"daemon_generation"`
			}
			markerBytes, markerErr := os.ReadFile(daemon.controlPath + ".marker.json")
			var marker ar2FixtureDaemonMarker
			if json.Unmarshal(response.Data, &status) == nil && status.PID == daemon.cmd.Process.Pid && status.DaemonGeneration != "" && markerErr == nil && json.Unmarshal(markerBytes, &marker) == nil && marker.PID == daemon.cmd.Process.Pid && marker.DaemonGeneration == status.DaemonGeneration && ar2SameFixtureExecutable(marker.Exe, daemon.binary) {
				return
			}
		}
		select {
		case <-daemon.done:
			t.Fatal("candidate daemon exited before control readiness")
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("candidate daemon did not publish matching control status and marker")
}

func ar2SameFixtureExecutable(left, right string) bool {
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(leftAbsolute), filepath.Clean(rightAbsolute))
}

func (daemon *ar2FixtureDaemon) Close() {
	if daemon == nil {
		return
	}
	daemon.once.Do(func() {
		select {
		case <-daemon.done:
			return
		default:
		}
		_ = daemon.cmd.Process.Kill()
		<-daemon.done
	})
}

func ar2InvokeDaemon(t *testing.T, ctx context.Context, daemon *ar2FixtureDaemon, upstreamAddr, repo, projectKey string) projectidentity.CorrelationV3 {
	t.Helper()
	upstream, err := grpc.NewClient(upstreamAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal("connect daemon capture proxy")
	}
	defer upstream.Close()
	capture := &ar2InitializeCapture{upstream: pb.NewEngramServiceClient(upstream), responses: make(chan *pb.InitializeResponse, 1)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen daemon capture proxy")
	}
	proxy := grpc.NewServer()
	pb.RegisterEngramServiceServer(proxy, capture)
	go func() { _ = proxy.Serve(listener) }()
	defer proxy.Stop()
	defer listener.Close()

	spawn, err := muxcontrol.SendWithTimeout(daemon.controlPath, muxcontrol.Request{Cmd: "spawn", Command: daemon.binary, Cwd: repo, Mode: "isolated", Env: map[string]string{"ENGRAM_URL": "http://" + listener.Addr().String()}}, 10*time.Second)
	if err != nil || spawn == nil || !spawn.OK || spawn.IPCPath == "" || spawn.Token == "" {
		t.Fatal("spawn isolated candidate daemon session")
	}
	connection, err := muxipc.Dial(spawn.IPCPath)
	if err != nil {
		t.Fatal("connect isolated candidate daemon session")
	}
	defer connection.Close()
	if _, err := fmt.Fprintln(connection, spawn.Token); err != nil {
		t.Fatal("bind isolated candidate daemon session")
	}
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal("set candidate daemon session deadline")
	}
	writer := bufio.NewWriter(connection)
	if _, err := writer.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"ar2-fixture","version":"1"}}}` + "\n"); err != nil || writer.Flush() != nil {
		t.Fatal("initialize isolated candidate daemon session")
	}
	scanner := bufio.NewScanner(connection)
	if err := ar2ReadJSONRPCResponse(scanner, "1"); err != nil {
		t.Fatalf("initialize isolated candidate daemon session response: %v", err)
	}
	if _, err := writer.WriteString(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n"); err != nil || writer.Flush() != nil {
		t.Fatal("notify isolated candidate daemon session")
	}
	if _, err := writer.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"); err != nil || writer.Flush() != nil {
		t.Fatal("list isolated candidate daemon tools")
	}
	if err := ar2ReadJSONRPCResponse(scanner, "2"); err != nil {
		t.Fatalf("tools/list isolated candidate daemon session response: %v", err)
	}
	select {
	case response := <-capture.responses:
		return ar2ResponseCorrelation(t, response, projectKey)
	case <-time.After(10 * time.Second):
		t.Fatal("clean candidate daemon did not return a resolver response")
	}
	return ""
}

func ar2ReadJSONRPCResponse(scanner *bufio.Scanner, wantID string) error {
	for scanner.Scan() {
		var frame struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil || string(frame.ID) != wantID {
			continue
		}
		if frame.JSONRPC != "2.0" {
			return fmt.Errorf("response %s has invalid protocol", wantID)
		}
		if frame.Error != nil {
			return fmt.Errorf("response %s failed with %d: %s", wantID, frame.Error.Code, frame.Error.Message)
		}
		if result := bytes.TrimSpace(frame.Result); len(result) == 0 || bytes.Equal(result, []byte("null")) {
			return fmt.Errorf("response %s has no result", wantID)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read response %s: %w", wantID, err)
	}
	return fmt.Errorf("missing response %s", wantID)
}

func TestAR2ReadJSONRPCResponseHandlesToolsListFrames(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		frame   string
		wantErr bool
	}{
		{name: "success", frame: `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`},
		{name: "error", frame: `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"fixture refusal"}}`, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := ar2ReadJSONRPCResponse(bufio.NewScanner(strings.NewReader(testCase.frame+"\n")), "2")
			if (err != nil) != testCase.wantErr {
				t.Fatalf("tools/list response error = %v, wantErr %t", err, testCase.wantErr)
			}
		})
	}
}

func ar2InvokeOpenClaw(t *testing.T, ctx context.Context, fixtureDir, baseURL string, descriptor map[string]any, projectKey, clientPath string) projectidentity.CorrelationV3 {
	t.Helper()
	driver := filepath.Join(fixtureDir, "openclaw-driver.mjs")
	driverSource := `
import { pathToFileURL } from 'node:url';
const { EngramRestClient } = await import(pathToFileURL(process.env.AR2_OPENCLAW_CLIENT).href);
const nativeFetch = globalThis.fetch;
const descriptor = JSON.parse(process.env.AR2_DESCRIPTOR);
const expectedBody = JSON.stringify({ project_descriptor: descriptor, identity_only: true });
let requests = 0;
let correlation = '';
globalThis.fetch = async (url, init = {}) => {
  requests += 1;
  if (requests !== 1 || url !== process.env.AR2_SERVER_URL + '/api/context/inject' || init.method !== 'POST') throw new Error('unexpected OpenClaw request');
  const headers = Object.fromEntries(new Headers(init.headers).entries());
  if (Object.keys(headers).sort().join(',') !== 'authorization,content-type,x-engram-project-identity-adapter,x-request-id' || headers.authorization !== 'Bearer' || headers['content-type'] !== 'application/json' || headers['x-engram-project-identity-adapter'] !== 'openclaw' || !headers['x-request-id'] || String(init.body) !== expectedBody) throw new Error('unexpected OpenClaw request body or headers');
  const response = await nativeFetch(url, init);
  const body = await response.clone().json();
  const resolution = body?.project_resolution_v3;
  if (!response.ok || resolution?.outcome !== 'PROJECT_RESOLVED' || resolution?.project_key !== process.env.AR2_PROJECT_KEY || resolution?.resolved_scope !== descriptor.scope || typeof resolution?.correlation !== 'string' || !resolution.correlation) throw new Error('unexpected OpenClaw resolver response');
  correlation = resolution.correlation;
  return response;
};
const client = new EngramRestClient({ url: process.env.AR2_SERVER_URL, token: '', timeoutMs: 5000, clientInstanceId: 'openclaw-fixture-client-17' });
const result = await client.registerAndResolveProject({ projectId: 'ar2-openclaw-fixture', agentId: 'ar2-openclaw-agent', projectIdentityV3: descriptor }, 'ar2-openclaw-selector');
if (requests !== 1 || !result.ok || result.canonicalProject !== process.env.AR2_PROJECT_KEY || !correlation) throw new Error('OpenClaw resolver result missing');
process.stdout.write(JSON.stringify({ step: 'openclaw', correlation }));
`
	if err := os.WriteFile(driver, []byte(driverSource), 0o600); err != nil {
		t.Fatal("write OpenClaw runtime driver")
	}
	output, err := ar2CommandEnv(ctx, filepath.Dir(clientPath), ar2FixtureEnvironment(t, "", map[string]string{
		"AR2_OPENCLAW_CLIENT": clientPath,
		"AR2_DESCRIPTOR":      string(ar2MustJSON(t, descriptor)),
		"AR2_SERVER_URL":      baseURL,
		"AR2_PROJECT_KEY":     projectKey,
	}), "node", driver)
	if err != nil {
		t.Fatal("run actual built OpenClaw V3 HTTP callable")
	}
	var result struct {
		Step        string `json:"step"`
		Correlation string `json:"correlation"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.Step != "openclaw" {
		t.Fatal("built OpenClaw V3 callable did not return its fixed correlation slot")
	}
	correlation, err := projectidentity.NewCorrelationV3(result.Correlation)
	if err != nil {
		t.Fatal("built OpenClaw V3 callable did not capture a resolver correlation")
	}
	return correlation
}

func ar2BuildOpenClawClient(t *testing.T, ctx context.Context, pluginDir string) string {
	t.Helper()
	if _, err := ar2Command(ctx, pluginDir, "npm", "install", "--no-save", "--package-lock=false", "--ignore-scripts", "--no-audit", "--no-fund"); err != nil {
		t.Fatal("install OpenClaw build dependencies")
	}
	if _, err := ar2Command(ctx, pluginDir, "npm", "run", "build"); err != nil {
		t.Fatal("build OpenClaw dist through package command")
	}
	clientPath := filepath.Join(pluginDir, "dist", "client.js")
	if _, err := os.Stat(clientPath); err != nil {
		t.Fatal("locate exact built OpenClaw client")
	}
	return clientPath
}

func ar2FixtureCorrelationList(t *testing.T, capture *ar2ControlledFixtureCapture) []projectidentity.CorrelationV3 {
	t.Helper()
	ordered, err := capture.ordered()
	if err != nil {
		t.Fatal("fixture callable capture is incomplete")
	}
	correlations := make([]projectidentity.CorrelationV3, 0, len(ordered))
	seen := make(map[projectidentity.CorrelationV3]struct{}, len(ordered))
	for _, correlation := range ordered {
		if correlation == "" {
			t.Fatal("fixture callable did not return a resolver correlation")
		}
		if _, duplicate := seen[correlation]; duplicate {
			t.Fatal("fixture callables returned duplicate resolver correlations")
		}
		seen[correlation] = struct{}{}
		correlations = append(correlations, correlation)
	}
	return correlations
}

func ar2AssertFixtureCorrelationReadbacks(t *testing.T, db *gormlib.DB, correlations []projectidentity.CorrelationV3) {
	t.Helper()
	for _, correlation := range correlations {
		value := string(correlation)
		var comparisons int64
		if err := db.Model(&gormdb.ProjectIdentityComparison{}).Where("correlation = ?", value).Count(&comparisons).Error; err != nil || comparisons != 1 {
			t.Fatalf("fixture correlation %q has %d persisted comparisons", value, comparisons)
		}
		var attempts []gormdb.ProjectResolutionAttempt
		if err := db.Where("correlation = ?", value).Find(&attempts).Error; err != nil || len(attempts) != 1 {
			t.Fatalf("fixture correlation %q has %d persisted resolution attempts", value, len(attempts))
		}
		attempt := attempts[0]
		if attempt.Intent != string(projectidentity.ResolveExistingIntentV3) || attempt.Outcome != string(projectidentity.ProjectResolvedOutcomeV3) || !attempt.AnchorProjectID.Valid || attempt.AnchorProjectID.String != ar2FixtureAnchorProjectID || attempt.DescriptorVersion != 3 || attempt.Provenance != "anchor_v3" {
			t.Fatalf("fixture correlation %q persisted an incomplete resolution attempt", value)
		}
	}
}

func ar2AssertReceiptCoverage(t *testing.T, receipt ar2IdentityExpandReceipt) {
	t.Helper()
	if len(receipt.AdapterCoverage) != len(ar2ControlledFixtureCallables) {
		t.Fatal("AR-2 receipt did not retain all fixed callable slots")
	}
	for index, spec := range ar2ControlledFixtureCallables {
		coverage := receipt.AdapterCoverage[index]
		if coverage.Adapter != spec.adapter || coverage.Callable != spec.callable || coverage.Metric.DenominatorValue != 1 || coverage.Metric.NumeratorValue != 1 || coverage.Metric.ResultStatus != operability.Computed || coverage.Metric.Ratio == nil || *coverage.Metric.Ratio != 1 {
			t.Fatal("AR-2 receipt callable denominator is not one computed observation")
		}
	}
}

func ar2DeleteOwnedRows(db *gormlib.DB, correlations []projectidentity.CorrelationV3, projectKey string) error {
	values := make([]string, len(correlations))
	for i, correlation := range correlations {
		values[i] = string(correlation)
	}
	if len(values) > 0 {
		if err := db.Where("correlation IN ?", values).Delete(&gormdb.ProjectIdentityComparison{}).Error; err != nil {
			return fmt.Errorf("delete comparisons")
		}
		if err := db.Where("correlation IN ?", values).Delete(&gormdb.ProjectResolutionAttempt{}).Error; err != nil {
			return fmt.Errorf("delete resolution attempts")
		}
	}
	if projectKey != "" {
		if err := db.Where("source_project_key = ?", projectKey).Delete(&gormdb.ProjectMergeAuditSource{}).Error; err != nil {
			return fmt.Errorf("delete fixture merge audit sources")
		}
		if err := db.Where("target_project_key = ?", projectKey).Delete(&gormdb.ProjectMergeAudit{}).Error; err != nil {
			return fmt.Errorf("delete fixture merge audits")
		}
		if err := db.Where("project_key = ?", projectKey).Delete(&gormdb.ProjectIdentifier{}).Error; err != nil {
			return fmt.Errorf("delete fixture identifiers")
		}
		if err := db.Where("id = ?", projectKey).Delete(&gormdb.Project{}).Error; err != nil {
			return fmt.Errorf("delete fixture project")
		}
	}
	for _, check := range []struct {
		model any
		name  string
	}{
		{&gormdb.ProjectIdentityComparison{}, "comparisons"},
		{&gormdb.ProjectResolutionAttempt{}, "resolution attempts"},
		{&gormdb.ProjectMergeAuditSource{}, "merge audit sources"},
		{&gormdb.ProjectMergeAudit{}, "merge audits"},
		{&gormdb.ProjectIdentifier{}, "identifiers"},
		{&gormdb.Project{}, "projects"},
	} {
		var count int64
		if err := db.Model(check.model).Count(&count).Error; err != nil || count != 0 {
			return fmt.Errorf("fixture cleanup did not reach zero %s", check.name)
		}
	}
	return nil
}

type ar2PayloadArtifact struct {
	label string
	root  string
	path  string
}

type ar2PayloadFile struct {
	relativePath string
	path         string
}

func ar2CandidatePayloadFingerprint(t *testing.T, artifacts ...ar2PayloadArtifact) string {
	t.Helper()
	sortedArtifacts := append([]ar2PayloadArtifact(nil), artifacts...)
	sort.Slice(sortedArtifacts, func(left, right int) bool {
		return sortedArtifacts[left].label < sortedArtifacts[right].label
	})
	if err := ar2ValidatePayloadArtifacts(sortedArtifacts...); err != nil {
		t.Fatal("inspect exact fixture payload artifact")
	}
	hasher := sha256.New()
	for _, artifact := range sortedArtifacts {
		info, err := ar2ContainedPayloadInfo(artifact.root, artifact.path)
		if err != nil {
			t.Fatal("inspect exact fixture payload artifact")
		}
		ar2WritePayloadFingerprintFrame(t, hasher, "artifact")
		ar2WritePayloadFingerprintFrame(t, hasher, artifact.label)
		switch {
		case info.Mode().IsRegular():
			ar2WritePayloadFingerprintFrame(t, hasher, "file")
			ar2HashPayloadFile(t, hasher, artifact.root, ".", artifact.path)
		case info.IsDir():
			ar2WritePayloadFingerprintFrame(t, hasher, "directory")
			ar2HashPayloadDirectory(t, hasher, artifact.root, artifact.path)
		default:
			t.Fatal("exact fixture payload artifact is not a regular file or directory")
		}
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func ar2ValidatePayloadArtifacts(artifacts ...ar2PayloadArtifact) error {
	labels := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.label == "" || artifact.root == "" || artifact.path == "" {
			return fmt.Errorf("invalid payload artifact")
		}
		if _, exists := labels[artifact.label]; exists {
			return fmt.Errorf("duplicate payload artifact label")
		}
		labels[artifact.label] = struct{}{}
		info, err := ar2ContainedPayloadInfo(artifact.root, artifact.path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode().IsRegular():
		case info.IsDir():
			if err := ar2ValidatePayloadDirectory(artifact); err != nil {
				return err
			}
		default:
			return fmt.Errorf("payload artifact is not a regular file or directory")
		}
	}
	return nil
}

func ar2ValidatePayloadDirectory(artifact ar2PayloadArtifact) error {
	return filepath.WalkDir(artifact.path, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := ar2ContainedPayloadInfo(artifact.root, path)
		if err != nil {
			return err
		}
		if info.IsDir() || info.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("payload artifact is not a regular file or directory")
	})
}

func ar2ContainedPayloadInfo(root, path string) (os.FileInfo, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := containedPath(absRoot, absPath); err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(absRoot)
	if err != nil {
		return nil, err
	}
	if linkOrReparse(rootInfo) || !rootInfo.IsDir() {
		return nil, fmt.Errorf("payload root is not a regular directory")
	}
	relativePath, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return nil, err
	}
	components := strings.Split(relativePath, string(filepath.Separator))
	current := absRoot
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if linkOrReparse(info) {
			return nil, fmt.Errorf("payload path contains a link or reparse component")
		}
		if index < len(components)-1 && !info.IsDir() {
			return nil, fmt.Errorf("payload path component is not a directory")
		}
		if index == len(components)-1 {
			return info, nil
		}
	}
	return nil, fmt.Errorf("payload path is invalid")
}

func ar2HashPayloadDirectory(t *testing.T, hasher io.Writer, artifactRoot, root string) {
	t.Helper()
	files := make([]ar2PayloadFile, 0)
	if err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := ar2ContainedPayloadInfo(artifactRoot, path)
		if err != nil {
			return err
		}
		if path == root || info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular payload artifact")
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil || relativePath == "." || relativePath == ".." || filepath.IsAbs(relativePath) || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid payload artifact path")
		}
		files = append(files, ar2PayloadFile{relativePath: filepath.ToSlash(relativePath), path: path})
		return nil
	}); err != nil {
		t.Fatal("scan exact fixture payload directory")
	}
	sort.Slice(files, func(left, right int) bool {
		return files[left].relativePath < files[right].relativePath
	})
	for _, file := range files {
		ar2HashPayloadFile(t, hasher, artifactRoot, file.relativePath, file.path)
	}
}

func ar2HashPayloadFile(t *testing.T, hasher io.Writer, artifactRoot, relativePath, path string) {
	t.Helper()
	expectedInfo, err := ar2ContainedPayloadInfo(artifactRoot, path)
	if err != nil {
		t.Fatal("inspect exact fixture payload artifact")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("open exact fixture payload artifact")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(expectedInfo, info) {
		_ = file.Close()
		t.Fatal("inspect exact fixture payload artifact")
	}
	ar2WritePayloadFingerprintFrame(t, hasher, "path")
	ar2WritePayloadFingerprintFrame(t, hasher, relativePath)
	ar2WritePayloadFingerprintFrame(t, hasher, "bytes")
	ar2WritePayloadFingerprintFrame(t, hasher, strconv.FormatInt(info.Size(), 10))
	bytesCopied, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || bytesCopied != info.Size() {
		t.Fatal("fingerprint exact fixture payload artifact")
	}
}

func ar2WritePayloadFingerprintFrame(t *testing.T, destination io.Writer, value string) {
	t.Helper()
	if _, err := fmt.Fprintf(destination, "%d:", len(value)); err != nil {
		t.Fatal("frame exact fixture payload fingerprint")
	}
	if _, err := io.WriteString(destination, value); err != nil {
		t.Fatal("write exact fixture payload fingerprint")
	}
}

func ar2MustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal("encode fixture value")
	}
	return encoded
}

var ar2FixtureCredentialAliases = map[string]struct{}{
	"ENGRAM_AUTH_ADMIN_TOKEN":        {},
	"ENGRAM_TOKEN":                   {},
	"ENGRAM_WORKSTATION_TOKEN":       {},
	"ENGRAM_API_TOKEN":               {},
	"API_TOKEN":                      {},
	"CLAUDE_PLUGIN_OPTION_API_TOKEN": {},
	"ENGRAM_CLAUDE_USERCONFIG_TOKEN": {},
}

var ar2FixtureSessionOnlyAliases = map[string]struct{}{
	"ENGRAM_URL":        {},
	"ENGRAM_SERVER_URL": {},
}

var ar2FixtureSystemEnvironmentAliases = map[string]struct{}{
	"COMSPEC":                {},
	"NUMBER_OF_PROCESSORS":   {},
	"OS":                     {},
	"PATH":                   {},
	"PATHEXT":                {},
	"PROCESSOR_ARCHITECTURE": {},
	"SYSTEMDRIVE":            {},
	"SYSTEMROOT":             {},
	"WINDIR":                 {},
}

var ar2FixtureControlAliases = map[string]struct{}{
	"APPDATA":                   {},
	"AR2_DESCRIPTOR":            {},
	"AR2_HOOK_LIB":              {},
	"AR2_OPENCLAW_CLIENT":       {},
	"AR2_PROJECT_KEY":           {},
	"AR2_SERVER_URL":            {},
	"DATABASE_DSN":              {},
	"DATABASE_MAX_CONNS":        {},
	"ENGRAM_AUTH_DISABLED":      {},
	"ENGRAM_CLIENT_INSTANCE_ID": {},
	"ENGRAM_DATA_DIR":           {},
	"ENGRAM_WORKER_HOST":        {},
	"ENGRAM_WORKER_PORT":        {},
	"GOCACHE":                   {},
	"HOME":                      {},
	"LOCALAPPDATA":              {},
	"TEMP":                      {},
	"TMP":                       {},
	"USERPROFILE":               {},
}

func ar2FixtureEnvironment(t *testing.T, bootstrapAdminToken string, overrides map[string]string) []string {
	t.Helper()
	isolated := ar2FixtureIsolatedOverrides(t.TempDir())
	for key, value := range overrides {
		isolated[key] = value
	}
	environment, err := ar2ScrubFixtureEnvironment(os.Environ(), bootstrapAdminToken, isolated)
	if err != nil {
		t.Fatal("construct fixture environment")
	}
	if err := ar2ValidateFixtureEnvironment(environment, bootstrapAdminToken); err != nil {
		t.Fatal("fixture environment escaped its allowlist")
	}
	return environment
}

func ar2FixtureIsolatedOverrides(root string) map[string]string {
	home := filepath.Join(root, "home")
	return map[string]string{
		"HOME":            home,
		"USERPROFILE":     home,
		"APPDATA":         filepath.Join(root, "appdata", "roaming"),
		"LOCALAPPDATA":    filepath.Join(root, "appdata", "local"),
		"TEMP":            filepath.Join(root, "temp"),
		"TMP":             filepath.Join(root, "temp"),
		"GOCACHE":         filepath.Join(root, "go-cache"),
		"ENGRAM_DATA_DIR": filepath.Join(root, "data"),
	}
}

func ar2ScrubFixtureEnvironment(base []string, bootstrapAdminToken string, overrides map[string]string) ([]string, error) {
	if overrides == nil {
		overrides = map[string]string{}
	}
	canonicalOverrides := make(map[string]string, len(overrides)+1)
	for key := range overrides {
		canonical := strings.ToUpper(key)
		if _, forbidden := ar2FixtureCredentialAliases[canonical]; forbidden {
			return nil, fmt.Errorf("fixture override %q is a credential alias", key)
		}
		if _, forbidden := ar2FixtureSessionOnlyAliases[canonical]; forbidden {
			return nil, fmt.Errorf("fixture override %q is reserved for the mux session", key)
		}
		if _, allowed := ar2FixtureControlAliases[canonical]; !allowed {
			return nil, fmt.Errorf("fixture override %q is outside the allowlist", key)
		}
		if _, duplicate := canonicalOverrides[canonical]; duplicate {
			return nil, fmt.Errorf("fixture overrides repeat %q with a case variant", key)
		}
		canonicalOverrides[canonical] = key
	}
	if bootstrapAdminToken != "" {
		canonicalOverrides["ENGRAM_AUTH_ADMIN_TOKEN"] = "ENGRAM_AUTH_ADMIN_TOKEN"
	}

	baseValues := make(map[string]string, len(base))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		canonical := strings.ToUpper(key)
		if _, allowed := ar2FixtureSystemEnvironmentAliases[canonical]; !allowed {
			continue
		}
		if _, overridden := canonicalOverrides[canonical]; overridden {
			continue
		}
		if _, duplicate := baseValues[canonical]; duplicate {
			return nil, fmt.Errorf("fixture base environment repeats %q", key)
		}
		baseValues[canonical] = entry
	}

	baseKeys := make([]string, 0, len(baseValues))
	for canonical := range baseValues {
		baseKeys = append(baseKeys, canonical)
	}
	sort.Strings(baseKeys)
	output := make([]string, 0, len(baseKeys)+len(canonicalOverrides))
	for _, canonical := range baseKeys {
		output = append(output, baseValues[canonical])
	}
	overrideKeys := make([]string, 0, len(canonicalOverrides))
	for canonical := range canonicalOverrides {
		overrideKeys = append(overrideKeys, canonical)
	}
	sort.Strings(overrideKeys)
	for _, canonical := range overrideKeys {
		if canonical == "ENGRAM_AUTH_ADMIN_TOKEN" {
			output = append(output, canonical+"="+bootstrapAdminToken)
			continue
		}
		key := canonicalOverrides[canonical]
		output = append(output, key+"="+overrides[key])
	}
	return output, nil
}

func ar2ValidateFixtureEnvironment(environment []string, bootstrapAdminToken string) error {
	adminTokenEntries := 0
	seen := make(map[string]struct{}, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			return fmt.Errorf("malformed environment entry")
		}
		canonical := strings.ToUpper(key)
		if _, duplicate := seen[canonical]; duplicate {
			return fmt.Errorf("fixture environment repeats %q", key)
		}
		seen[canonical] = struct{}{}
		if _, reserved := ar2FixtureSessionOnlyAliases[canonical]; reserved {
			return fmt.Errorf("fixture environment contains session-only alias %q", key)
		}
		if _, forbidden := ar2FixtureCredentialAliases[canonical]; forbidden {
			if canonical != "ENGRAM_AUTH_ADMIN_TOKEN" || bootstrapAdminToken == "" || key != "ENGRAM_AUTH_ADMIN_TOKEN" || value != bootstrapAdminToken {
				return fmt.Errorf("fixture environment contains prohibited credential alias %q", key)
			}
			adminTokenEntries++
			continue
		}
		if _, allowed := ar2FixtureSystemEnvironmentAliases[canonical]; allowed {
			continue
		}
		if _, allowed := ar2FixtureControlAliases[canonical]; allowed {
			continue
		}
		return fmt.Errorf("fixture environment contains non-allowlisted alias %q", key)
	}
	if bootstrapAdminToken == "" && adminTokenEntries != 0 {
		return fmt.Errorf("runtime fixture environment retained an admin token")
	}
	if bootstrapAdminToken != "" && adminTokenEntries != 1 {
		return fmt.Errorf("bootstrap fixture environment did not contain exactly one admin token")
	}
	return nil
}

func TestAR2FixtureEnvironmentScrubsCredentialAliases(t *testing.T) {
	base := []string{"PATH=fixture", "engram_token=host-token", "api_token=host-bare-api-token", "ENGRAM_WORKSTATION_TOKEN=host-workstation", "Engram_Api_Token=host-api", "claude_plugin_option_api_token=host-hook", "ENGRAM_CLAUDE_USERCONFIG_TOKEN=host-user", "ENGRAM_AUTH_ADMIN_TOKEN=host-admin", "eNgRaM_Url=http://host-daemon.invalid", "ENGRAM_SERVER_URL=http://host-server.invalid", "VENDOR_API_KEY=host-vendor-key", "ENGRAM_CLIENT_INSTANCE_ID=host-client"}
	overrides := map[string]string{"HOME": "fixture-home", "ENGRAM_CLIENT_INSTANCE_ID": "fixture-daemon-client"}
	runtimeEnvironment, err := ar2ScrubFixtureEnvironment(base, "", overrides)
	if err != nil {
		t.Fatal(err)
	}
	if err := ar2ValidateFixtureEnvironment(runtimeEnvironment, ""); err != nil {
		t.Fatal(err)
	}
	bootstrapEnvironment, err := ar2ScrubFixtureEnvironment(base, "fixture-bootstrap-token", overrides)
	if err != nil {
		t.Fatal(err)
	}
	if err := ar2ValidateFixtureEnvironment(bootstrapEnvironment, "fixture-bootstrap-token"); err != nil {
		t.Fatal(err)
	}
	for _, environment := range [][]string{runtimeEnvironment, bootstrapEnvironment} {
		joined := strings.Join(environment, "\n")
		for _, forbidden := range []string{"host-bare-api-token", "host-daemon.invalid", "host-server.invalid", "host-vendor-key", "host-client"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("fixture environment retained %q", forbidden)
			}
		}
		for _, required := range []string{"PATH=fixture", "HOME=fixture-home", "ENGRAM_CLIENT_INSTANCE_ID=fixture-daemon-client"} {
			if !strings.Contains(joined, required) {
				t.Fatalf("fixture environment omitted %q", required)
			}
		}
	}
	if _, err := ar2ScrubFixtureEnvironment(base, "", map[string]string{"api_token": "forbidden"}); err == nil {
		t.Fatal("bare API_TOKEN override was accepted")
	}
	if _, err := ar2ScrubFixtureEnvironment(base, "", map[string]string{"ENGRAM_URL": "forbidden"}); err == nil {
		t.Fatal("daemon URL override was accepted")
	}
	if _, err := ar2ScrubFixtureEnvironment(base, "", map[string]string{"VENDOR_API_KEY": "forbidden"}); err == nil {
		t.Fatal("non-allowlisted override was accepted")
	}
}

func ar2Command(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	root, err := os.MkdirTemp("", "engram-ar2-command-")
	if err != nil {
		return nil, fmt.Errorf("create isolated command environment: %w", err)
	}
	defer os.RemoveAll(root)
	environment, err := ar2ScrubFixtureEnvironment(os.Environ(), "", ar2FixtureIsolatedOverrides(root))
	if err != nil {
		return nil, fmt.Errorf("construct fixture command environment: %w", err)
	}
	return ar2CommandEnv(ctx, dir, environment, name, args...)
}

func ar2CommandEnv(ctx context.Context, dir string, environment []string, name string, args ...string) ([]byte, error) {
	if err := ar2ValidateFixtureEnvironment(environment, ""); err != nil {
		return nil, fmt.Errorf("validate fixture child environment: %w", err)
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed", name)
	}
	return output, nil
}
