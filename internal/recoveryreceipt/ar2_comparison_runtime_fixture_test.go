package recoveryreceipt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
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

	"github.com/thebtf/engram/internal/projectidentity"
)

const (
	ar2FixtureAnchorProjectID     = "11111111-1111-4111-8111-111111111111"
	ar2FixtureBuildPreflightOptIn = "ENGRAM_AR2_FIXTURE_SERVER_BUILD_PREFLIGHT"
)

func TestAR2CandidateCommitRequiresCleanWorktree(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		commit, err := ar2CandidateCommitFromWorktree(ctx, ar2CandidateTestWorktree(t, ctx))
		if err != nil || !validCommit(commit) {
			t.Fatalf("clean worktree commit = %q, %v", commit, err)
		}
	})

	for _, testCase := range []struct {
		name   string
		want   string
		mutate func(t *testing.T, root string)
	}{
		{
			name: "ordinary tracked change",
			want: "candidate source worktree is dirty",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("modified\n"), 0o600); err != nil {
					t.Fatal("modify tracked fixture")
				}
			},
		},
		{
			name: "staged change",
			want: "candidate source worktree is dirty",
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
			want: "candidate source worktree is dirty",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
					t.Fatal("write untracked fixture")
				}
			},
		},
		{
			name: "assume unchanged scan relevant source",
			want: "index-hidden scan-relevant source",
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
			want: "ignored scan-relevant source",
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
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			root := ar2CandidateTestWorktree(t, ctx)
			testCase.mutate(t, root)
			if _, err := ar2CandidateCommitFromWorktree(ctx, root); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("candidate guard error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestAR2CandidateCommitAllowsOnlyFixtureGeneratedArtifacts(t *testing.T) {
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
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
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

func TestAR2FixtureServerBuildPreflight(t *testing.T) {
	if os.Getenv(ar2FixtureBuildPreflightOptIn) != "1" {
		t.Skipf("fixture build preflight requires %s=1", ar2FixtureBuildPreflightOptIn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	root := ar2FixtureRoot(t)
	candidateCommit := ar2CandidateCommit(t, ctx, root)
	fixtureDir, removeFixtureDir := ar2FixtureDir(t, ctx, root)
	defer removeFixtureDir()
	candidate := ar2StartCandidateSnapshot(t, ctx, root, fixtureDir, candidateCommit)
	defer candidate.Close(t)
	binary := filepath.Join(fixtureDir, "engram-server.exe")
	if _, err := ar2Command(ctx, candidate.root, "go", "build", "-tags", "fts5", "-ldflags", "-X main.SourceCommit="+candidateCommit, "-o", binary, "./cmd/engram-server"); err != nil {
		t.Fatal("build fixture server preflight")
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() || info.Size() == 0 {
		t.Fatalf("fixture server preflight did not produce a binary: %v", err)
	}
	t.Logf("AR-2 fixture server build preflight: candidate=%s fts5=passed", candidateCommit)
}

func TestAR2DaemonRepositoryBuildsAcceptedV3Descriptor(t *testing.T) {
	repo := ar2DaemonRepository(t, t.TempDir())
	anchor, err := projectidentity.DiscoverAnchorV3(t.Context(), repo, "repository")
	if err != nil {
		t.Fatalf("discover daemon repository V3 anchor: %v", err)
	}
	rawRemote, err := ar2Command(context.Background(), repo, "git", "remote", "get-url", "origin")
	if err != nil {
		t.Fatal("read daemon repository origin")
	}
	remote, disposition, err := projectidentity.NormalizeGitRemoteV3(strings.TrimSpace(string(rawRemote)))
	if err != nil || disposition != projectidentity.RemoteNormalizedV3 {
		t.Fatalf("normalize daemon repository origin = %q, %q, %v", remote, disposition, err)
	}
	descriptor, err := projectidentity.BuildDescriptorV3(anchor, []string{remote}, nil, "ar2-daemon-fixture-client-17")
	if err != nil {
		t.Fatalf("build daemon repository V3 descriptor: %v", err)
	}
	if descriptor.AnchorProjectID != ar2FixtureAnchorProjectID || descriptor.Name != "ar2-comparison-project" || descriptor.Scope != "repository" || len(descriptor.NormalizedGitRemotes) != 1 || descriptor.NormalizedGitRemotes[0] != "example.invalid/acme/engram" {
		t.Fatalf("daemon repository V3 descriptor = %#v", descriptor)
	}
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
		diagnostic := strings.ReplaceAll(err.Error(), root, "[REDACTED]")
		diagnostic = strings.ReplaceAll(diagnostic, filepath.ToSlash(root), "[REDACTED]")
		t.Fatalf("git %s failed: %s", strings.Join(args, " "), diagnostic)
	}
}

const (
	ar2HookDriverFailureCode                  = "AR2_HOOK_DRIVER_FAILURE"
	ar2HookDriverFailureMessage               = "Hook driver failed"
	ar2HookDriverUnexpectedRequestCode        = "AR2_HOOK_DRIVER_UNEXPECTED_REQUEST"
	ar2HookDriverBodyOrHeadersMismatchCode    = "AR2_HOOK_DRIVER_REQUEST_BODY_OR_HEADERS_MISMATCH"
	ar2HookDriverResolverResponseMismatchCode = "AR2_HOOK_DRIVER_RESOLVER_RESPONSE_MISMATCH"
	ar2HookDriverResolverResultMissingCode    = "AR2_HOOK_DRIVER_RESOLVER_RESULT_MISSING"
)

func ar2HookDriverSource() string {
	return fmt.Sprintf(`
const hook = require(process.env.AR2_HOOK_LIB);
const nativeFetch = globalThis.fetch;
const descriptor = JSON.parse(process.env.AR2_DESCRIPTOR);
const canonicalizeJSON = (value) => {
  if (Array.isArray(value)) return value.map(canonicalizeJSON);
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonicalizeJSON(value[key])]));
  }
  return value;
};
const closedSemanticPayload = (value) => JSON.stringify(canonicalizeJSON(value));
const expectedPayload = closedSemanticPayload({ project_descriptor: descriptor, identity_only: true });
const HookDriverAssertionError = class extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
};
let requests = 0;
let correlation = '';
globalThis.fetch = async (url, init = {}) => {
  requests += 1;
  if (requests !== 1 || url !== process.env.AR2_SERVER_URL + '/api/context/inject' || init.method !== 'POST') throw new HookDriverAssertionError(%q, 'unexpected Hook request');
  const headers = Object.fromEntries(new Headers(init.headers).entries());
  let bodyMatches = false;
  try {
    bodyMatches = closedSemanticPayload(JSON.parse(String(init.body))) === expectedPayload;
  } catch {
    bodyMatches = false;
  }
  if (Object.keys(headers).sort().join(',') !== 'content-type,x-engram-project-identity-adapter,x-request-id' || headers['content-type'] !== 'application/json' || headers['x-engram-project-identity-adapter'] !== 'hook' || headers['x-request-id'] !== 'hook-fixture-attempt-17' || !bodyMatches) throw new HookDriverAssertionError(%q, 'unexpected Hook request body or headers');
  const response = await nativeFetch(url, init);
  const body = await response.clone().json();
  const resolution = body?.project_resolution_v3;
  if (!response.ok || resolution?.outcome !== 'PROJECT_RESOLVED' || resolution?.project_key !== process.env.AR2_PROJECT_KEY || resolution?.resolved_scope !== descriptor.scope || typeof resolution?.correlation !== 'string' || !resolution.correlation) throw new HookDriverAssertionError(%q, 'unexpected Hook resolver response');
  correlation = resolution.correlation;
  return response;
};
(async () => {
  const context = { ProjectDescriptorV3: descriptor };
  await hook.registerProjectIdentityV3(context, undefined, { serverURL: process.env.AR2_SERVER_URL, requestID: 'hook-fixture-attempt-17' });
  if (requests !== 1 || !correlation || context.Project !== process.env.AR2_PROJECT_KEY) throw new HookDriverAssertionError(%q, 'Hook resolver result missing');
  process.stdout.write(JSON.stringify({ step: 'hook', correlation }));
})().catch((error) => {
  const code = error instanceof HookDriverAssertionError ? error.code : %q;
  process.stderr.write(JSON.stringify({ code, message: %q }) + '\n');
  process.exitCode = 1;
});
`, ar2HookDriverUnexpectedRequestCode, ar2HookDriverBodyOrHeadersMismatchCode, ar2HookDriverResolverResponseMismatchCode, ar2HookDriverResolverResultMissingCode, ar2HookDriverFailureCode, ar2HookDriverFailureMessage)
}

func ar2DaemonRepository(t *testing.T, fixtureDir string) string {
	t.Helper()
	repo := filepath.Join(fixtureDir, "daemon-repository")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal("create daemon repository fixture")
	}
	anchor, err := json.Marshal(projectidentity.AnchorV3{
		Version:   3,
		ProjectID: ar2FixtureAnchorProjectID,
		Name:      "ar2-comparison-project",
		Scope:     "repository",
	})
	if err != nil {
		t.Fatal("marshal daemon V3 anchor")
	}
	anchor = append(anchor, '\n')
	if err := os.WriteFile(filepath.Join(repo, ".engram-project"), anchor, 0o600); err != nil {
		t.Fatal("write daemon V3 anchor")
	}
	for _, arguments := range [][]string{{"init"}, {"remote", "add", "origin", "https://example.invalid/acme/engram.git"}, {"add", ".engram-project"}} {
		if _, err := ar2Command(context.Background(), repo, "git", arguments...); err != nil {
			t.Fatal("prepare daemon repository fixture")
		}
	}
	return repo
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

type ar2FixturePrivacyEvidence struct {
	descriptor string
	projectKey string
	serverURL  string
	secret     string
}

func ar2RecordFixtureCallable(capture *ar2ControlledFixtureCapture, cleanupCorrelations *[]projectidentity.CorrelationV3, callable string, correlation projectidentity.CorrelationV3) error {
	if err := capture.record(callable, correlation); err != nil {
		return err
	}
	*cleanupCorrelations = append(*cleanupCorrelations, correlation)
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

var ar2FixtureBuildEnvironmentAliases = map[string]struct{}{
	"CGO_ENABLED": {},
	"GOMODCACHE":  {},
	"GOPATH":      {},
	"GOTOOLCHAIN": {},
}

var ar2FixtureIsolatedPathAliases = []string{
	"HOME",
	"USERPROFILE",
	"APPDATA",
	"LOCALAPPDATA",
	"TEMP",
	"TMP",
	"GOCACHE",
	"ENGRAM_DATA_DIR",
}

const ar2FixtureCommandStderrLimit = 4096

func ar2FixtureEnvironment(t *testing.T, bootstrapAdminToken string, overrides map[string]string) []string {
	t.Helper()
	isolated := ar2FixtureIsolatedOverrides(t.TempDir())
	for key, value := range overrides {
		isolated[key] = value
	}
	if err := ar2EnsureFixtureOverrideDirectories(isolated); err != nil {
		t.Fatal("create isolated fixture environment directories")
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

func ar2EnsureFixtureOverrideDirectories(overrides map[string]string) error {
	for _, key := range ar2FixtureIsolatedPathAliases {
		path, found := overrides[key]
		if !found || path == "" {
			return fmt.Errorf("fixture environment omitted %s directory", key)
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create %s directory: %w", key, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %s directory: %w", key, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("fixture %s path is not a directory", key)
		}
	}
	return nil
}

func ar2ScrubFixtureEnvironment(base []string, bootstrapAdminToken string, overrides map[string]string) ([]string, error) {
	canonicalOverrides, err := ar2CanonicalFixtureOverrides(bootstrapAdminToken, overrides)
	if err != nil {
		return nil, err
	}
	baseValues, err := ar2AllowedFixtureBaseValues(base, canonicalOverrides)
	if err != nil {
		return nil, err
	}
	return ar2FixtureEnvironmentEntries(baseValues, canonicalOverrides, bootstrapAdminToken, overrides), nil
}

func ar2CanonicalFixtureOverrides(bootstrapAdminToken string, overrides map[string]string) (map[string]string, error) {
	if overrides == nil {
		overrides = map[string]string{}
	}
	canonicalOverrides := make(map[string]string, len(overrides)+1)
	for key := range overrides {
		canonical := strings.ToUpper(key)
		if err := ar2ValidateFixtureOverride(key, canonical, canonicalOverrides); err != nil {
			return nil, err
		}
		canonicalOverrides[canonical] = key
	}
	if bootstrapAdminToken != "" {
		canonicalOverrides["ENGRAM_AUTH_ADMIN_TOKEN"] = "ENGRAM_AUTH_ADMIN_TOKEN"
	}
	return canonicalOverrides, nil
}

func ar2ValidateFixtureOverride(key, canonical string, canonicalOverrides map[string]string) error {
	if _, forbidden := ar2FixtureCredentialAliases[canonical]; forbidden {
		return fmt.Errorf("fixture override %q is a credential alias", key)
	}
	if _, forbidden := ar2FixtureSessionOnlyAliases[canonical]; forbidden {
		return fmt.Errorf("fixture override %q is reserved for the mux session", key)
	}
	if _, allowed := ar2FixtureControlAliases[canonical]; !allowed {
		return fmt.Errorf("fixture override %q is outside the allowlist", key)
	}
	if _, duplicate := canonicalOverrides[canonical]; duplicate {
		return fmt.Errorf("fixture overrides repeat %q with a case variant", key)
	}
	return nil
}

func ar2AllowedFixtureBaseValues(base []string, canonicalOverrides map[string]string) (map[string]string, error) {
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
	return baseValues, nil
}

func ar2FixtureEnvironmentEntries(baseValues, canonicalOverrides map[string]string, bootstrapAdminToken string, overrides map[string]string) []string {
	baseKeys := sortedFixtureEnvironmentKeys(baseValues)
	output := make([]string, 0, len(baseKeys)+len(canonicalOverrides))
	for _, canonical := range baseKeys {
		output = append(output, baseValues[canonical])
	}
	for _, canonical := range sortedFixtureEnvironmentKeys(canonicalOverrides) {
		if canonical == "ENGRAM_AUTH_ADMIN_TOKEN" {
			output = append(output, canonical+"="+bootstrapAdminToken)
		} else {
			key := canonicalOverrides[canonical]
			output = append(output, key+"="+overrides[key])
		}
	}
	return output
}

func sortedFixtureEnvironmentKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ar2ValidateFixtureEnvironment(environment []string, bootstrapAdminToken string) error {
	adminTokenEntries := 0
	seen := make(map[string]struct{}, len(environment))
	for _, entry := range environment {
		adminToken, err := ar2ValidateFixtureEnvironmentEntry(entry, bootstrapAdminToken, seen)
		if err != nil {
			return err
		}
		if adminToken {
			adminTokenEntries++
		}
	}
	if bootstrapAdminToken == "" && adminTokenEntries != 0 {
		return fmt.Errorf("runtime fixture environment retained an admin token")
	}
	if bootstrapAdminToken != "" && adminTokenEntries != 1 {
		return fmt.Errorf("bootstrap fixture environment did not contain exactly one admin token")
	}
	return nil
}

func ar2ValidateFixtureEnvironmentEntry(entry, bootstrapAdminToken string, seen map[string]struct{}) (bool, error) {
	key, value, found := strings.Cut(entry, "=")
	if !found {
		return false, fmt.Errorf("malformed environment entry")
	}
	canonical := strings.ToUpper(key)
	if _, duplicate := seen[canonical]; duplicate {
		return false, fmt.Errorf("fixture environment repeats %q", key)
	}
	seen[canonical] = struct{}{}
	if _, reserved := ar2FixtureSessionOnlyAliases[canonical]; reserved {
		return false, fmt.Errorf("fixture environment contains session-only alias %q", key)
	}
	if _, forbidden := ar2FixtureCredentialAliases[canonical]; forbidden {
		if canonical != "ENGRAM_AUTH_ADMIN_TOKEN" || bootstrapAdminToken == "" || key != "ENGRAM_AUTH_ADMIN_TOKEN" || value != bootstrapAdminToken {
			return false, fmt.Errorf("fixture environment contains prohibited credential alias %q", key)
		}
		return true, nil
	}
	if ar2AllowedFixtureEnvironmentAlias(canonical) {
		return false, nil
	}
	return false, fmt.Errorf("fixture environment contains non-allowlisted alias %q", key)
}

func ar2AllowedFixtureEnvironmentAlias(canonical string) bool {
	if _, allowed := ar2FixtureSystemEnvironmentAliases[canonical]; allowed {
		return true
	}
	if _, allowed := ar2FixtureControlAliases[canonical]; allowed {
		return true
	}
	_, allowed := ar2FixtureBuildEnvironmentAliases[canonical]
	return allowed
}

func TestAR2FixtureEnvironmentScrubsCredentialAliases(t *testing.T) {
	base := []string{"PATH=fixture", "CGO_ENABLED=0", "engram_token=host-token", "api_token=host-bare-api-token", "ENGRAM_WORKSTATION_TOKEN=host-workstation", "Engram_Api_Token=host-api", "claude_plugin_option_api_token=host-hook", "ENGRAM_CLAUDE_USERCONFIG_TOKEN=host-user", "ENGRAM_AUTH_ADMIN_TOKEN=host-admin", "eNgRaM_Url=http://host-daemon.invalid", "ENGRAM_SERVER_URL=http://host-server.invalid", "VENDOR_API_KEY=host-vendor-key", "ENGRAM_CLIENT_INSTANCE_ID=host-client"}
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
		for _, forbidden := range []string{"CGO_ENABLED=0", "host-bare-api-token", "host-daemon.invalid", "host-server.invalid", "host-vendor-key", "host-client"} {
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

func TestAR2FixtureEnvironmentCreatesOverrideDirectories(t *testing.T) {
	root := t.TempDir()
	overrides := ar2FixtureIsolatedOverrides(root)
	environment := ar2FixtureEnvironment(t, "", overrides)
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("fixture environment contained malformed entry %q", entry)
		}
		values[key] = value
	}
	for key, expected := range overrides {
		actual, found := values[key]
		if !found || actual != expected {
			t.Fatalf("fixture environment did not preserve %s override", key)
		}
		info, err := os.Stat(actual)
		if err != nil || !info.IsDir() {
			t.Fatalf("fixture environment did not create %s directory %q: %v", key, actual, err)
		}
	}
}

func TestAR2FixtureGoBuildEnvironmentForcesCGO(t *testing.T) {
	buildEnvironment := ar2FixtureGoBuildEnvironment(
		[]string{"PATH=fixture"},
		[]string{
			"CGO_ENABLED=0",
			"CC=host-cc",
			"CXX=host-cxx",
			"GOMODCACHE=host-module-cache",
			"GOPATH=host-go-path",
			"GOTOOLCHAIN=host-toolchain",
		},
	)
	values := make(map[string]string, len(buildEnvironment))
	for _, entry := range buildEnvironment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("build environment contained malformed entry %q", entry)
		}
		if _, duplicate := values[key]; duplicate {
			t.Fatalf("build environment repeated %q", key)
		}
		values[key] = value
	}
	if values["CGO_ENABLED"] != "1" {
		t.Fatalf("build environment did not force CGO_ENABLED=1: %q", values["CGO_ENABLED"])
	}
	for _, key := range []string{"CC", "CXX"} {
		if _, found := values[key]; found {
			t.Fatalf("build environment inherited %s instead of using PATH defaults", key)
		}
	}
	if err := ar2ValidateFixtureEnvironment(buildEnvironment, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAR2CommandEnvSanitizesBoundedCredentialFailure(t *testing.T) {
	const (
		commandPassword      = "fixture-command-password"
		dockerPassword       = "ar2-fixture-password"
		dsnPassword          = "fixture-dsn-password"
		apiKey               = "fixture-api-key"
		accessKey            = "fixture-access-key"
		apiKeyAssignment     = "fixture-api-key-assignment"
		accessKeyAssignment  = "fixture-access-key-assignment"
		genericDSN           = "nats://fixture-generic-dsn"
		bearerToken          = "fixture-bearer-token"
		inlineEnvironmentKey = "fixture-inline-api-key"
	)
	dsn := "postgres://postgres:" + dsnPassword + "@127.0.0.1:5432/engram?sslmode=disable"
	args := []string{
		"--password", commandPassword,
		"-e", "POSTGRES_PASSWORD=" + dockerPassword,
		"--api-key", apiKey,
		"--access-key", accessKey,
		"API_KEY=" + apiKeyAssignment,
		"ACCESS_KEY=" + accessKeyAssignment,
		"UPSTREAM_DSN=" + genericDSN,
		"Bearer", bearerToken,
		"--env=API_KEY=" + inlineEnvironmentKey,
	}
	childArgs := append([]string{"-test.run=^TestAR2FixtureCommandChildProcess$", "--", ar2FixtureCommandChildMarker, "failure"}, args...)
	environment := ar2FixtureEnvironment(t, "", map[string]string{"DATABASE_DSN": dsn})
	output, err := ar2CommandEnv(context.Background(), t.TempDir(), environment, os.Args[0], childArgs...)
	if err == nil {
		t.Fatal("failing fixture command returned nil error")
	}
	if output != nil {
		t.Fatalf("failing fixture command returned stdout: %q", output)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("fixture command error did not wrap ExitError: %v", err)
	}
	diagnostic := err.Error()
	for _, secret := range []string{commandPassword, dockerPassword, dsnPassword, dsn, apiKey, accessKey, apiKeyAssignment, accessKeyAssignment, genericDSN, bearerToken, inlineEnvironmentKey} {
		if strings.Contains(diagnostic, secret) {
			t.Fatalf("fixture command diagnostic leaked %q: %s", secret, diagnostic)
		}
	}
	if !strings.Contains(diagnostic, "[REDACTED]") {
		t.Fatalf("fixture command diagnostic did not retain redaction marker: %s", diagnostic)
	}
	maxDiagnosticLength := len(os.Args[0]+" failed: ") + len(exitError.Error()) + len(": ") + ar2FixtureCommandStderrLimit
	if len(diagnostic) > maxDiagnosticLength {
		t.Fatalf("fixture command diagnostic exceeded its stderr bound: got=%d want<=%d", len(diagnostic), maxDiagnosticLength)
	}
	if !strings.Contains(diagnostic, "...[truncated]") {
		t.Fatalf("fixture command diagnostic did not mark truncation: %s", diagnostic)
	}

	output, err = ar2CommandEnv(context.Background(), t.TempDir(), environment, os.Args[0], "-test.run=^TestAR2FixtureCommandChildProcess$", "--", ar2FixtureCommandChildMarker, "success")
	if err != nil {
		t.Fatalf("successful fixture command: %v", err)
	}
	if strings.TrimSpace(string(output)) != "fixture-stdout" {
		t.Fatalf("fixture command did not preserve stdout: %q", output)
	}
}

const ar2FixtureCommandChildMarker = "ar2-fixture-command-child"

func TestAR2FixtureCommandChildProcess(t *testing.T) {
	for index, argument := range os.Args {
		if argument != ar2FixtureCommandChildMarker {
			continue
		}
		if index+1 >= len(os.Args) {
			t.Fatal("fixture command child mode is missing")
		}
		switch os.Args[index+1] {
		case "failure":
			fmt.Fprintln(os.Stderr, strings.Join(os.Args[index+2:], " "))
			for range 256 {
				fmt.Fprintln(os.Stderr, "fixture-diagnostic-padding")
			}
			os.Exit(23)
		case "success":
			fmt.Fprintln(os.Stdout, "fixture-stdout")
			os.Exit(0)
		default:
			t.Fatalf("fixture command child mode = %q", os.Args[index+1])
		}
	}
}

type ar2HookDriverDiagnosticCase struct {
	name             string
	hookBody         string
	resolverResponse string
	code             string
	success          bool
}

func TestAR2HookDriverDiagnosticClassification(t *testing.T) {
	const (
		bootstrapAdminToken = "fixture-hook-bootstrap-token"
		projectKey          = "fixture-hook-project-key"
		descriptor          = `{"scope":"repository","name":"fixture-hook-descriptor"}`
		correlation         = "fixture-hook-correlation"
	)
	validResponse := `{"project_resolution_v3":{"outcome":"PROJECT_RESOLVED","project_key":"fixture-hook-project-key","resolved_scope":"repository","correlation":"fixture-hook-correlation"}}`
	validRequest := `
const descriptor = JSON.parse(process.env.AR2_DESCRIPTOR);
const response = await fetch(process.env.AR2_SERVER_URL + '/api/context/inject', {
  method: 'POST',
  headers: {
    'content-type': 'application/json',
    'x-engram-project-identity-adapter': 'hook',
    'x-request-id': 'hook-fixture-attempt-17',
  },
  body: JSON.stringify({ project_descriptor: descriptor, identity_only: true }),
});
const body = await response.json();
context.Project = body.project_resolution_v3.project_key;
`
	validRequestWithoutResult := `
const descriptor = JSON.parse(process.env.AR2_DESCRIPTOR);
await fetch(process.env.AR2_SERVER_URL + '/api/context/inject', {
  method: 'POST',
  headers: {
    'content-type': 'application/json',
    'x-engram-project-identity-adapter': 'hook',
    'x-request-id': 'hook-fixture-attempt-17',
  },
  body: JSON.stringify({ project_descriptor: descriptor, identity_only: true }),
});
`

	for _, testCase := range []ar2HookDriverDiagnosticCase{
		{
			name:     "unexpected request",
			hookBody: `await fetch(process.env.AR2_SERVER_URL + '/unexpected', { method: 'POST' });`,
			code:     "AR2_HOOK_DRIVER_UNEXPECTED_REQUEST",
		},
		{
			name: "malformed JSON request body",
			hookBody: `await fetch(process.env.AR2_SERVER_URL + '/api/context/inject', {
  method: 'POST',
  headers: {
    'content-type': 'application/json',
    'x-engram-project-identity-adapter': 'hook',
    'x-request-id': 'hook-fixture-attempt-17',
  },
  body: 'wrong',
});`,
			code: ar2HookDriverBodyOrHeadersMismatchCode,
		},
		{
			name: "semantically reordered descriptor is accepted",
			hookBody: `const response = await fetch(process.env.AR2_SERVER_URL + '/api/context/inject', {
  method: 'POST',
  headers: {
    'content-type': 'application/json',
    'x-engram-project-identity-adapter': 'hook',
    'x-request-id': 'hook-fixture-attempt-17',
  },
  body: '{"identity_only":true,"project_descriptor":{"name":"fixture-hook-descriptor","scope":"repository"}}',
});
const body = await response.json();
context.Project = body.project_resolution_v3.project_key;`,
			resolverResponse: validResponse,
			success:          true,
		},
		{
			name: "missing descriptor property fails semantic mismatch",
			hookBody: `await fetch(process.env.AR2_SERVER_URL + '/api/context/inject', {
  method: 'POST',
  headers: {
    'content-type': 'application/json',
    'x-engram-project-identity-adapter': 'hook',
    'x-request-id': 'hook-fixture-attempt-17',
  },
  body: '{"project_descriptor":{"scope":"repository"},"identity_only":true}',
});`,
			code: ar2HookDriverBodyOrHeadersMismatchCode,
		},
		{
			name: "extra payload property fails semantic mismatch",
			hookBody: `await fetch(process.env.AR2_SERVER_URL + '/api/context/inject', {
  method: 'POST',
  headers: {
    'content-type': 'application/json',
    'x-engram-project-identity-adapter': 'hook',
    'x-request-id': 'hook-fixture-attempt-17',
  },
  body: '{"project_descriptor":{"scope":"repository","name":"fixture-hook-descriptor"},"identity_only":true,"extra":"unexpected"}',
});`,
			code: ar2HookDriverBodyOrHeadersMismatchCode,
		},
		{
			name: "payload value change fails semantic mismatch",
			hookBody: `await fetch(process.env.AR2_SERVER_URL + '/api/context/inject', {
  method: 'POST',
  headers: {
    'content-type': 'application/json',
    'x-engram-project-identity-adapter': 'hook',
    'x-request-id': 'hook-fixture-attempt-17',
  },
  body: '{"project_descriptor":{"scope":"repository","name":"fixture-hook-descriptor"},"identity_only":false}',
});`,
			code: ar2HookDriverBodyOrHeadersMismatchCode,
		},
		{
			name:             "resolver response mismatch",
			hookBody:         validRequest,
			resolverResponse: `{}`,
			code:             "AR2_HOOK_DRIVER_RESOLVER_RESPONSE_MISMATCH",
		},
		{
			name:             "resolver result missing",
			hookBody:         validRequestWithoutResult,
			resolverResponse: validResponse,
			code:             "AR2_HOOK_DRIVER_RESOLVER_RESULT_MISSING",
		},
		{
			name:     "unknown Hook rejection remains generic and redacted",
			hookBody: `throw new Error(["fixture simulated Hook rejection", process.env.AR2_DESCRIPTOR, process.env.AR2_PROJECT_KEY, process.env.AR2_SERVER_URL, "fixture-hook-bootstrap-token"].join('|'));`,
			code:     ar2HookDriverFailureCode,
		},
		{
			name:             "successful Hook result remains unchanged",
			hookBody:         validRequest,
			resolverResponse: validResponse,
			success:          true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runAR2HookDiagnosticCase(t, testCase, descriptor, projectKey, correlation, bootstrapAdminToken)
		})
	}
}

func runAR2HookDiagnosticCase(t *testing.T, testCase ar2HookDriverDiagnosticCase, descriptor, projectKey, correlation, bootstrapAdminToken string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, testCase.resolverResponse)
	}))
	defer server.Close()
	fixtureDir := t.TempDir()
	driver := filepath.Join(fixtureDir, "hook-driver.cjs")
	if err := os.WriteFile(driver, []byte(ar2HookDriverSource()), 0o600); err != nil {
		t.Fatal("write Hook diagnostic driver")
	}
	hook := filepath.Join(fixtureDir, "hook.cjs")
	hookSource := "module.exports = { registerProjectIdentityV3: async (context) => {" + testCase.hookBody + "} };\n"
	if err := os.WriteFile(hook, []byte(hookSource), 0o600); err != nil {
		t.Fatal("write Hook diagnostic module")
	}
	output, err := ar2CommandEnv(context.Background(), fixtureDir, ar2FixtureEnvironment(t, "", map[string]string{
		"AR2_DESCRIPTOR":  descriptor,
		"AR2_HOOK_LIB":    hook,
		"AR2_PROJECT_KEY": projectKey,
		"AR2_SERVER_URL":  server.URL,
	}), "node", driver)
	if testCase.success {
		assertAR2HookDiagnosticSuccess(t, err, output, correlation)
		return
	}
	assertAR2HookDiagnosticFailure(t, err, output, testCase.code, ar2FixturePrivacyEvidence{
		descriptor: descriptor,
		projectKey: projectKey,
		serverURL:  server.URL,
		secret:     bootstrapAdminToken,
	})
}

func assertAR2HookDiagnosticSuccess(t *testing.T, err error, output []byte, correlation string) {
	t.Helper()
	if err != nil {
		t.Fatalf("successful Hook driver: %v", err)
	}
	var result struct {
		Step        string `json:"step"`
		Correlation string `json:"correlation"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.Step != "hook" || result.Correlation != correlation {
		t.Fatalf("successful Hook driver output = %q, %v", output, err)
	}
}

func assertAR2HookDiagnosticFailure(t *testing.T, err error, output []byte, code string, evidence ar2FixturePrivacyEvidence) {
	t.Helper()
	if err == nil {
		t.Fatal("failing Hook driver returned nil error")
	}
	if len(output) != 0 {
		t.Fatalf("failing Hook driver emitted stdout: %q", output)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("Hook driver error did not wrap ExitError: %v", err)
	}
	diagnostic := err.Error()
	for _, expected := range []string{code, ar2HookDriverFailureMessage} {
		if !strings.Contains(diagnostic, expected) {
			t.Fatalf("Hook driver omitted stable diagnostic %q: %s", expected, diagnostic)
		}
	}
	if code != ar2HookDriverFailureCode && strings.Contains(diagnostic, ar2HookDriverFailureCode) {
		t.Fatalf("known Hook assertion used generic failure code: %s", diagnostic)
	}
	for _, forbidden := range []string{"fixture simulated Hook rejection", evidence.descriptor, evidence.projectKey, evidence.serverURL, evidence.secret} {
		if strings.Contains(diagnostic, forbidden) {
			t.Fatalf("Hook driver leaked %q: %s", forbidden, diagnostic)
		}
	}
}

func TestAR2FixtureDiagnosticSanitizerRedactsHookControls(t *testing.T) {
	const (
		bootstrapAdminToken = "fixture-sanitizer-bootstrap-token"
		projectKey          = "fixture-sanitizer-project-key"
		serverURL           = "http://fixture-sanitizer-server.invalid:37777"
		descriptor          = `{"scope":"repository","name":"fixture-sanitizer-descriptor"}`
		credential          = "fixture-sanitizer-password"
	)
	dsn := "postgres://fixture:" + credential + "@fixture.invalid/engram"
	environment := ar2FixtureEnvironment(t, bootstrapAdminToken, map[string]string{
		"AR2_DESCRIPTOR":  descriptor,
		"AR2_PROJECT_KEY": projectKey,
		"AR2_SERVER_URL":  serverURL,
		"DATABASE_DSN":    dsn,
	})
	diagnostic := ar2SanitizeFixtureCommandStderr([]byte(strings.Join([]string{bootstrapAdminToken, projectKey, serverURL, descriptor, dsn}, " ")), environment, nil)
	for _, forbidden := range []string{bootstrapAdminToken, projectKey, serverURL, descriptor, dsn, credential} {
		if strings.Contains(diagnostic, forbidden) {
			t.Fatalf("fixture diagnostic sanitizer leaked %q: %s", forbidden, diagnostic)
		}
	}
	if strings.Count(diagnostic, "[REDACTED]") != 5 {
		t.Fatalf("fixture diagnostic sanitizer did not redact every Hook control value: %s", diagnostic)
	}
}

func TestAR2FixtureCallableCaptureOwnsSuccessfulCorrelationsBeforeHookFailure(t *testing.T) {
	capture, err := newAR2ControlledFixtureCapture(ar2FixtureCandidateAttestation())
	if err != nil {
		t.Fatal(err)
	}
	var cleanupCorrelations []projectidentity.CorrelationV3
	grpcCorrelation := ar2Correlation(t, "ar2-fixture-cleanup-grpc")
	httpCorrelation := ar2Correlation(t, "ar2-fixture-cleanup-http")
	if err := ar2RecordFixtureCallable(capture, &cleanupCorrelations, ar2ControlledFixtureCallables[0].callable, grpcCorrelation); err != nil {
		t.Fatal(err)
	}
	if err := ar2RecordFixtureCallable(capture, &cleanupCorrelations, ar2ControlledFixtureCallables[1].callable, httpCorrelation); err != nil {
		t.Fatal(err)
	}
	if capture.next != 2 {
		t.Fatalf("fixture capture before Hook failure advanced to slot %d", capture.next)
	}
	if len(cleanupCorrelations) != 2 || cleanupCorrelations[0] != grpcCorrelation || cleanupCorrelations[1] != httpCorrelation {
		t.Fatalf("cleanup ownership after Hook failure = %#v", cleanupCorrelations)
	}
}

func TestAR2WrapFixtureCommandErrorRetainsSanitizedDiagnostic(t *testing.T) {
	const secret = "fixture-hook-diagnostic-secret"
	_, commandErr := ar2CommandEnv(context.Background(), t.TempDir(), ar2FixtureEnvironment(t, "", nil), os.Args[0], "-test.run=^TestAR2FixtureCommandChildProcess$", "--", ar2FixtureCommandChildMarker, "failure", "--api-key", secret)
	if commandErr == nil {
		t.Fatal("failing fixture command returned nil error")
	}
	wrapped := ar2WrapFixtureCommandError("run actual Hook V3 HTTP callable", commandErr)
	if !errors.Is(wrapped, commandErr) {
		t.Fatal("wrapped Hook diagnostic did not retain the child error")
	}
	diagnostic := wrapped.Error()
	for _, expected := range []string{"run actual Hook V3 HTTP callable", "[REDACTED]"} {
		if !strings.Contains(diagnostic, expected) {
			t.Fatalf("wrapped Hook diagnostic omitted %q: %s", expected, diagnostic)
		}
	}
	if strings.Contains(diagnostic, secret) {
		t.Fatalf("wrapped Hook diagnostic leaked %q: %s", secret, diagnostic)
	}
}

func ar2Command(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	root, err := os.MkdirTemp("", "engram-ar2-command-")
	if err != nil {
		return nil, fmt.Errorf("create isolated command environment: %w", err)
	}
	defer os.RemoveAll(root)
	overrides := ar2FixtureIsolatedOverrides(root)
	if err := ar2EnsureFixtureOverrideDirectories(overrides); err != nil {
		return nil, fmt.Errorf("create isolated command directories: %w", err)
	}
	base := os.Environ()
	environment, err := ar2ScrubFixtureEnvironment(base, "", overrides)
	if err != nil {
		return nil, fmt.Errorf("construct fixture command environment: %w", err)
	}
	if name == "go" {
		environment = ar2FixtureGoBuildEnvironment(environment, base)
	}
	return ar2CommandEnv(ctx, dir, environment, name, args...)
}

func ar2FixtureGoBuildEnvironment(environment, base []string) []string {
	values := make(map[string]string, len(ar2FixtureBuildEnvironmentAliases))
	for _, entry := range base {
		key, value, found := strings.Cut(entry, "=")
		if !found || value == "" {
			continue
		}
		canonical := strings.ToUpper(key)
		if canonical == "CGO_ENABLED" {
			continue
		}
		if _, allowed := ar2FixtureBuildEnvironmentAliases[canonical]; allowed {
			values[canonical] = value
		}
	}
	values["CGO_ENABLED"] = "1"
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func ar2CommandEnv(ctx context.Context, dir string, environment []string, name string, args ...string) ([]byte, error) {
	if err := ar2ValidateFixtureEnvironment(environment, ""); err != nil {
		return nil, fmt.Errorf("validate fixture child environment: %w", err)
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = environment
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	exitError, isExitError := err.(*exec.ExitError)
	if !isExitError {
		return nil, fmt.Errorf("%s failed: %w", name, err)
	}
	stderr := ar2SanitizeFixtureCommandStderr(exitError.Stderr, environment, args)
	if stderr == "" {
		return nil, fmt.Errorf("%s failed: %w", name, err)
	}
	return nil, fmt.Errorf("%s failed: %w: %s", name, err, stderr)
}

func ar2WrapFixtureCommandError(operation string, err error) error {
	return fmt.Errorf("%s: %w", operation, err)
}

func ar2SanitizeFixtureCommandStderr(stderr []byte, environment, args []string) string {
	message := strings.TrimSpace(string(stderr))
	sensitive := ar2FixtureCommandSensitiveValues(environment, args)
	sort.Slice(sensitive, func(left, right int) bool {
		return len(sensitive[left]) > len(sensitive[right])
	})
	for _, value := range sensitive {
		message = strings.ReplaceAll(message, value, "[REDACTED]")
	}
	if len(message) <= ar2FixtureCommandStderrLimit {
		return message
	}
	const suffix = "...[truncated]"
	return message[:ar2FixtureCommandStderrLimit-len(suffix)] + suffix
}

func ar2FixtureCommandSensitiveValues(environment, args []string) []string {
	values := sensitiveFixtureValues{}
	values.addEnvironment(environment)
	values.addArguments(args)
	return values.values
}

type sensitiveFixtureValues struct {
	seen   map[string]struct{}
	values []string
}

func (values *sensitiveFixtureValues) add(value string) {
	if value == "" {
		return
	}
	if values.seen == nil {
		values.seen = make(map[string]struct{})
	}
	if _, duplicate := values.seen[value]; duplicate {
		return
	}
	values.seen[value] = struct{}{}
	values.values = append(values.values, value)
}

func (values *sensitiveFixtureValues) addEnvironment(environment []string) {
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found && ar2FixtureDiagnosticSensitiveEnvironmentKey(key) {
			values.add(value)
		}
	}
}

func (values *sensitiveFixtureValues) addArguments(args []string) {
	for index, argument := range args {
		values.addAssignedArgument(argument)
		values.addFollowingArgument(argument, index, args)
	}
}

func (values *sensitiveFixtureValues) addAssignedArgument(argument string) {
	key, value, hasValue := strings.Cut(argument, "=")
	if hasValue && ar2FixtureSensitiveCommandKey(key) {
		values.add(value)
	}
	if hasValue && strings.EqualFold(strings.TrimLeft(key, "-"), "env") {
		values.addEnvironmentAssignment(value)
	}
}

func (values *sensitiveFixtureValues) addFollowingArgument(argument string, index int, args []string) {
	if index+1 >= len(args) {
		return
	}
	following := args[index+1]
	if ar2FixtureSensitiveCommandKey(argument) || strings.EqualFold(argument, "Bearer") {
		values.add(following)
	}
	if strings.EqualFold(strings.TrimLeft(argument, "-"), "env") {
		values.addEnvironmentAssignment(following)
	}
}

func (values *sensitiveFixtureValues) addEnvironmentAssignment(assignment string) {
	key, value, ok := strings.Cut(assignment, "=")
	if ok && ar2FixtureSensitiveCommandKey(key) {
		values.add(value)
	}
}

func ar2FixtureDiagnosticSensitiveEnvironmentKey(key string) bool {
	if ar2FixtureSensitiveCommandKey(key) {
		return true
	}
	normalized := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimLeft(key, "-")))
	_, control := ar2FixtureControlAliases[normalized]
	return control
}

func ar2FixtureSensitiveCommandKey(key string) bool {
	normalized := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimLeft(key, "-")))
	if _, credential := ar2FixtureCredentialAliases[normalized]; credential {
		return true
	}
	if normalized == "API_KEY" || normalized == "ACCESS_KEY" || normalized == "BEARER" || normalized == "DSN" || strings.HasSuffix(normalized, "_DSN") {
		return true
	}
	return strings.Contains(normalized, "PASSWORD") || strings.Contains(normalized, "TOKEN") || strings.Contains(normalized, "SECRET") || strings.Contains(normalized, "CREDENTIAL")
}
