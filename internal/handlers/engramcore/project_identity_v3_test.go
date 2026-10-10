package engramcore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/proxy"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const daemonV3CanonicalProject = "11111111-1111-4111-8111-111111111111"

func TestLegacySelectedScopeAcceptsFilesystemAliases(t *testing.T) {
	root := daemonV3Repository(t)
	selectedRoot := strings.ToUpper(root)
	if runtime.GOOS != "windows" {
		selectedRoot = filepath.Join(t.TempDir(), "repository-alias")
		if err := os.Symlink(root, selectedRoot); err != nil {
			t.Fatal(err)
		}
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	selectedInfo, err := os.Stat(selectedRoot)
	if err != nil || !os.SameFile(rootInfo, selectedInfo) {
		t.Fatalf("fixture alias is not the same directory: %v", err)
	}
	for _, suffix := range []string{"", "nested"} {
		t.Run(suffix, func(t *testing.T) {
			selected := filepath.Join(selectedRoot, suffix)
			if err := os.MkdirAll(selected, 0o700); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command("git", "-C", selected, "rev-parse", "--show-toplevel").Output()
			if err != nil {
				t.Fatal(err)
			}
			gitRoot := strings.TrimSpace(string(output))
			if filepath.Clean(selectedRoot) == filepath.Clean(gitRoot) {
				t.Fatal("fixture did not produce distinct Git and selected root spellings")
			}
			prefix := ""
			if suffix != "" {
				prefix = suffix + "/"
			}
			if !legacySelectedScopeUnchanged(selected, gitRoot, prefix) {
				t.Fatal("same filesystem repository was refused for a different path spelling")
			}
		})
	}
	if runtime.GOOS != "windows" {
		srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
		_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
		project.Cwd = selectedRoot
		mod.v3ClientInstanceID = "fixture-daemon-install"
		if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := mod.ProxyTools(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(selectedRoot); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "nested"), selectedRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if srv.callReq.GetProjectIdentity().GetRelativePath() != "nested/" {
			t.Fatal("retargeted filesystem alias reused the previous selected prefix")
		}
	}
}

func TestLegacySelectedDirectoryMoveInvalidatesScope(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(configDir, "system.gitconfig"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(configDir, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_COUNT", "0")
	for _, phase := range []string{"during-resolution", "warm"} {
		t.Run(phase, func(t *testing.T) {
			root := daemonV3Repository(t)
			if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			selected, moved := filepath.Join(root, "old"), filepath.Join(root, "new")
			if err := os.Mkdir(selected, 0o700); err != nil {
				t.Fatal(err)
			}
			directory, err := os.Open(selected)
			if err != nil {
				t.Fatal(err)
			}
			before, statErr := directory.Stat()
			closeErr := directory.Close()
			if statErr != nil {
				t.Fatal(statErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			moveSelected := func() {
				if err := os.Rename(selected, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, selected); err != nil {
					t.Fatalf("selected-directory symlink capability unavailable: %v", err)
				}
				after, err := os.Stat(moved)
				if err != nil {
					t.Fatal(err)
				}
				alias, err := os.Stat(selected)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(before, after) || !os.SameFile(after, alias) {
					t.Fatal("selected-directory move or alias changed the inode")
				}
			}
			project := muxcore.ProjectContext{ID: "fixture", Cwd: selected}
			mod := NewModuleWithClientInstanceID("fixture-daemon-install")
			key := cacheKey(project)
			if phase == "warm" {
				if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
					t.Fatalf("initial legacy admission: %v", err)
				}
				if !mod.cache.legacy[key].cacheEligible {
					t.Fatal("fixture did not exercise warm cache reuse")
				}
				moveSelected()
			} else {
				original := resolveLegacyGitIdentity
				t.Cleanup(func() { resolveLegacyGitIdentity = original })
				changed := false
				resolveLegacyGitIdentity = func(ctx context.Context, cwd, name, prefix string) (string, proxy.ProjectIdentityV2, error) {
					if prefix != "old/" {
						t.Fatalf("race did not capture the old batched prefix: %q", prefix)
					}
					changed = true
					moveSelected()
					return original(ctx, cwd, name, prefix)
				}
				if _, _, err := mod.v3Identity(context.Background(), project); err == nil || !changed {
					slug, identity, _ := mod.proxyV2Identity(context.Background(), project)
					t.Fatalf("moved selected directory admitted stale scope: slug=%s prefix=%q changed=%t err=%v", slug, identity.GetRelativePath(), changed, err)
				}
				if _, ok := mod.cache.legacy[key]; ok {
					t.Fatal("unstable admission retained legacy scope")
				}
				if _, ok := mod.cache.entries.Load(key); ok {
					t.Fatal("unstable admission retained a slug")
				}
				if _, ok := mod.cache.identities.Load(key); ok {
					t.Fatal("unstable admission retained V2 metadata")
				}
				resolveLegacyGitIdentity = original
			}
			output, err := exec.Command("git", "-C", selected, "rev-parse", "--show-prefix").Output()
			if err != nil || strings.TrimSpace(string(output)) != "new/" {
				t.Fatalf("Git did not select the moved relative scope: %v: %s", err, output)
			}
			for range 2 {
				if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
					t.Fatalf("refreshed legacy admission: %v", err)
				}
				slug, identity, err := mod.proxyV2Identity(context.Background(), project)
				want := fmt.Sprintf("%x", sha256.Sum256([]byte("https://git.example.test/Platform/Daemon.git/new/")))[:8]
				if err != nil || slug != want || identity.GetRelativePath() != "new/" {
					t.Fatalf("moved directory reused old scope: slug=%s prefix=%q err=%v", slug, identity.GetRelativePath(), err)
				}
			}
		})
	}
}

func TestLegacyGitMetadataPreservesPathWhitespace(t *testing.T) {
	for _, test := range []struct {
		name, root, selected, gitDir string
		paddedConfig                 bool
	}{
		{name: "selected-leading-space-refused", root: "repo", selected: " foo"},
		{name: "selected-internal-space", root: "repo", selected: "foo bar"},
		{name: "root-trailing-space", root: "repo ", selected: "nested"},
		{name: "git-directory-spaces", root: "repo", selected: "nested", gitDir: " gitdir "},
		{name: "git-directory-internal-space", root: "repo", selected: "nested", gitDir: "git dir"},
		{name: "config-file-trailing-space", root: "repo", selected: "nested", paddedConfig: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.gitDir != "" {
				capabilityDir := t.TempDir()
				if err := os.Mkdir(filepath.Join(capabilityDir, test.gitDir), 0o700); err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(capabilityDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].Name() != test.gitDir {
					t.Skip("filesystem does not preserve the exact requested separate Git directory name")
				}
			}
			configDir := t.TempDir()
			global := filepath.Join(configDir, "global.gitconfig")
			if test.paddedConfig {
				global += " "
			}
			if err := os.WriteFile(global, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if test.paddedConfig {
				entries, err := os.ReadDir(configDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].Name() != filepath.Base(global) {
					t.Skip("filesystem does not preserve the exact trailing-space config filename")
				}
			}
			t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
			t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(configDir, "system.gitconfig"))
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			t.Setenv("GIT_CONFIG_COUNT", "0")
			parent := t.TempDir()
			root := filepath.Join(parent, test.root)
			if err := os.Rename(daemonV3Repository(t), root); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != test.root {
				t.Skip("filesystem does not preserve the requested repository name")
			}
			if test.gitDir != "" {
				gitDir := filepath.Join(parent, test.gitDir)
				if output, err := exec.Command("git", "-C", root, "init", "--quiet", "--separate-git-dir", gitDir).CombinedOutput(); err != nil {
					t.Fatalf("separate Git directory: %v: %s", err, output)
				}
			}
			if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			selected := filepath.Join(root, test.selected)
			if err := os.Mkdir(selected, 0o700); err != nil {
				t.Fatal(err)
			}
			prefix := test.selected + "/"
			project := muxcore.ProjectContext{ID: "fixture", Cwd: selected}
			mod := NewModuleWithClientInstanceID("fixture-daemon-install")
			key := cacheKey(project)
			if test.selected == " foo" {
				original := resolveLegacyGitIdentity
				t.Cleanup(func() { resolveLegacyGitIdentity = original })
				var observedPrefix string
				var validationErr error
				resolveLegacyGitIdentity = func(ctx context.Context, cwd, name, prefix string) (string, proxy.ProjectIdentityV2, error) {
					observedPrefix = prefix
					slug, identity, err := original(ctx, cwd, name, prefix)
					validationErr = err
					return slug, identity, err
				}
				for range 2 {
					observedPrefix, validationErr = "", nil
					_, enabled, err := mod.v3Identity(context.Background(), project)
					var refusal *module.ModuleError
					if !enabled || !errors.As(err, &refusal) || refusal.Code != "PROJECT_ANCHOR_INVALID" || observedPrefix != prefix ||
						validationErr == nil || !strings.Contains(validationErr.Error(), "PROJECT_IDENTITY_INVALID") {
						t.Fatalf("leading-space scope was normalized or bypassed V2 refusal: observed=%q validation=%v err=%v", observedPrefix, validationErr, err)
					}
					_, legacy := mod.cache.legacy[key]
					_, slug := mod.cache.entries.Load(key)
					_, identity := mod.cache.identities.Load(key)
					if legacy || slug || identity {
						t.Fatal("unsupported leading-space scope retained a normalized identity cache")
					}
				}
				return
			}
			remote := "https://git.example.test/Platform/Daemon.git"
			for round := range 3 {
				if round == 1 && test.paddedConfig {
					if err := os.WriteFile(global, []byte("[url \"https://example.invalid/changed/\"]\n\tinsteadOf = https://git.example.test/Platform/\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					remote = "https://example.invalid/changed/Daemon.git"
				}
				if round == 2 {
					t.Setenv("PATH", t.TempDir())
				}
				if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
					t.Fatalf("supported whitespace legacy admission: enabled=%t err=%v", enabled, err)
				}
				slug, identity, err := mod.proxyV2Identity(context.Background(), project)
				want := fmt.Sprintf("%x", sha256.Sum256([]byte(remote+"/"+prefix)))[:8]
				if err != nil || slug != want || identity.GetRelativePath() != prefix || identity.GetGitRemote() != remote {
					t.Fatalf("supported path bytes changed: slug=%s prefix=%q remote=%q err=%v", slug, identity.GetRelativePath(), identity.GetGitRemote(), err)
				}
				cached := mod.cache.legacy[key]
				if _, ok := cached.files[global]; !ok || !cached.cacheEligible {
					t.Fatal("Git config path bytes were lost or warm reuse was disabled")
				}
			}
		})
	}
}

func TestProxyLegacyColdScopeRefusesUntrackingDuringAdmission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real Git interposition requires a POSIX executable script")
	}
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n\"$ENGRAM_TEST_REAL_GIT\" \"$@\"\nstatus=$?\nif [ \"$3\" = ls-files ] && [ \"$status\" -eq 0 ]; then\n  \"$ENGRAM_TEST_REAL_GIT\" -C \"$2\" update-index --force-remove -- .engram-project || exit $?\nfi\nexit \"$status\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_TEST_REAL_GIT", realGit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := mod.ProxyTools(context.Background(), project); err == nil {
		t.Fatal("marker untracked after admission reached legacy identity")
	}
	if output, err := exec.Command(realGit, "-C", project.Cwd, "ls-files", "--", ".engram-project").Output(); err != nil || len(output) != 0 {
		t.Fatalf("real Git untracking did not occur: %v: %s", err, output)
	}
	if srv.initReq != nil {
		t.Fatal("untracked marker reached Initialize")
	}
	key := cacheKey(project)
	if _, ok := mod.cache.legacy[key]; ok {
		t.Fatal("unstable admission retained legacy cache")
	}
	if _, ok := mod.cache.identities.Load(key); ok {
		t.Fatal("unstable admission retained V2 identity")
	}
	for range 2 {
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
			t.Fatal("untracked marker reused legacy authority")
		}
		if srv.callReq != nil {
			t.Fatal("untracked marker reached memory scope")
		}
	}
}

func TestProxyLegacyColdScopeRefusesConfigTransition(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	original := resolveLegacyGitIdentity
	t.Cleanup(func() { resolveLegacyGitIdentity = original })
	changed := false
	resolveLegacyGitIdentity = func(ctx context.Context, cwd, name, prefix string) (string, proxy.ProjectIdentityV2, error) {
		slug, identity, err := original(ctx, cwd, name, prefix)
		if err == nil && !changed {
			changed = true
			if output, err := exec.Command("git", "-C", cwd, "remote", "set-url", "origin", "https://git.example.test/new/repo.git").CombinedOutput(); err != nil {
				t.Fatalf("transition config: %v: %s", err, output)
			}
		}
		return slug, identity, err
	}
	if _, err := mod.ProxyTools(context.Background(), project); err == nil {
		t.Fatal("old cold identity accepted under new config fingerprints")
	}
	if srv.initReq != nil {
		t.Fatal("unstable cold identity reached Initialize")
	}
	for range 2 {
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if srv.callReq.GetProjectIdentity().GetGitRemote() != "https://git.example.test/new/repo.git" {
			t.Fatal("subsequent request reused stale origin")
		}
	}
}

func TestLegacyBatchedMetadataPreservesSelectedScope(t *testing.T) {
	root := daemonV3Repository(t)
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "user.name", "identity-fixture"},
		{"config", "user.email", "identity@example.invalid"},
		{"remote", "set-url", "origin", "fixture:repo.git"},
		{"config", "url.https://fixture-user:fixture-credential@example.invalid/acme/.insteadOf", "fixture:"},
		{"add", ".engram-project"},
		{"commit", "--quiet", "-m", "identity fixture"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture Git: %v: %s", err, output)
		}
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if output, err := exec.Command("git", "-C", root, "worktree", "add", "--quiet", "--detach", linked, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("fixture worktree: %v: %s", err, output)
	}
	for _, selectedRoot := range []string{root, linked} {
		for _, prefix := range []string{"", "nested/"} {
			t.Run(filepath.Base(selectedRoot)+"/"+prefix, func(t *testing.T) {
				selected := filepath.Join(selectedRoot, prefix)
				if err := os.MkdirAll(selected, 0o700); err != nil {
					t.Fatal(err)
				}
				index, err := exec.Command("git", "-C", selectedRoot, "rev-parse", "--path-format=absolute", "--git-path", "index").Output()
				if err != nil {
					t.Fatal(err)
				}
				indexPath := strings.TrimSpace(string(index))
				before, err := os.ReadFile(indexPath)
				if err != nil {
					t.Fatal(err)
				}
				project := muxcore.ProjectContext{ID: "fixture", Cwd: selected}
				mod := NewModuleWithClientInstanceID("fixture-daemon-install")
				identity, enabled, err := mod.v3Identity(context.Background(), project)
				if err != nil || enabled || identity != nil {
					t.Fatalf("legacy classification: enabled=%t err=%v", enabled, err)
				}
				slug, metadata, err := mod.proxyV2Identity(context.Background(), project)
				const remote = "https://example.invalid/acme/repo.git"
				want := fmt.Sprintf("%x", sha256.Sum256([]byte(remote+"/"+prefix)))[:8]
				if err != nil || slug != want || metadata.GetGitRemote() != remote || metadata.GetRelativePath() != prefix {
					t.Fatalf("selected scope or normalized origin changed: slug=%s prefix=%q err=%v", slug, metadata.GetRelativePath(), err)
				}
				for _, name := range []string{"config", "index", "HEAD", "config.worktree"} {
					output, err := exec.Command("git", "-C", selectedRoot, "rev-parse", "--path-format=absolute", "--git-path", name).Output()
					if err != nil {
						t.Fatal(err)
					}
					if _, ok := mod.cache.legacy[cacheKey(project)].files[filepath.Clean(strings.TrimSpace(string(output)))]; !ok {
						t.Fatalf("batched metadata lost %s dependency", name)
					}
				}
				after, err := os.ReadFile(indexPath)
				if err != nil || string(before) != string(after) {
					t.Fatal("metadata resolution changed the index")
				}
				if prefix != "" {
					for _, marker := range []string{".engram-project", ".engram-project-v2.json"} {
						if _, err := os.Stat(filepath.Join(selected, marker)); !os.IsNotExist(err) {
							t.Fatal("metadata resolution created a selected-directory marker")
						}
					}
				}
			})
		}
	}
}

func TestLegacyConfigAbsenceCreationInvalidatesScope(t *testing.T) {
	for _, variable := range []string{"GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL"} {
		for _, phase := range []string{"warm", "during-resolution"} {
			t.Run(variable+"/"+phase, func(t *testing.T) {
				configDir := t.TempDir()
				t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
				t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(configDir, "system.gitconfig"))
				t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(configDir, "global.gitconfig"))
				root := daemonV3Repository(t)
				if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				project := muxcore.ProjectContext{ID: "fixture", Cwd: root}
				mod := NewModuleWithClientInstanceID("fixture-daemon-install")
				createConfig := func() {
					if err := os.WriteFile(os.Getenv(variable), []byte("[url \"https://example.invalid/changed/\"]\n\tinsteadOf = https://git.example.test/Platform/\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "warm" {
					if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
						t.Fatalf("initial legacy admission: %v", err)
					}
					createConfig()
				} else {
					original := resolveLegacyGitIdentity
					t.Cleanup(func() { resolveLegacyGitIdentity = original })
					resolveLegacyGitIdentity = func(ctx context.Context, cwd, name, prefix string) (string, proxy.ProjectIdentityV2, error) {
						slug, identity, err := original(ctx, cwd, name, prefix)
						if err == nil {
							createConfig()
						}
						return slug, identity, err
					}
					if _, _, err := mod.v3Identity(context.Background(), project); err == nil {
						t.Fatal("config created during resolution granted stale scope")
					}
					if _, ok := mod.cache.legacy[cacheKey(project)]; ok {
						t.Fatal("unstable scope remained cached")
					}
					resolveLegacyGitIdentity = original
				}
				if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
					t.Fatalf("refreshed legacy admission: %v", err)
				}
				slug, identity, err := mod.proxyV2Identity(context.Background(), project)
				const remote = "https://example.invalid/changed/Daemon.git"
				want := fmt.Sprintf("%x", sha256.Sum256([]byte(remote+"/")))[:8]
				if err != nil || slug != want || identity.GetGitRemote() != remote {
					t.Fatalf("created config reused stale scope: slug=%s err=%v", slug, err)
				}
			})
		}
	}
}

func TestLegacyGitEnvironmentVariableCase(t *testing.T) {
	t.Setenv("git_config_legacy_fingerprint_test", "before")
	before := legacyGitEnvironment()
	t.Setenv("git_config_legacy_fingerprint_test", "after")
	changed := before != legacyGitEnvironment()
	if changed != (runtime.GOOS == "windows") {
		t.Fatal("Git environment fingerprint does not follow platform variable-name case semantics")
	}
}

func TestLegacyWindowsLowercaseConfigSwitchInvalidatesScope(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows environment variable names are case-insensitive")
	}
	for _, variable := range []string{"GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL"} {
		for _, phase := range []string{"warm", "during-resolution"} {
			t.Run(variable+"/"+phase, func(t *testing.T) {
				root := daemonV3Repository(t)
				if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				initial, replacement := filepath.Join(dir, "initial.gitconfig"), filepath.Join(dir, "replacement.gitconfig")
				if err := os.WriteFile(initial, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(replacement, []byte("[url \"https://example.invalid/changed/\"]\n\tinsteadOf = https://git.example.test/Platform/\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				originalValue, present := os.LookupEnv(variable)
				if err := os.Unsetenv(variable); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if present {
						if err := os.Setenv(variable, originalValue); err != nil {
							t.Fatal(err)
						}
					}
				})
				lowercase := strings.ToLower(variable)
				t.Setenv(lowercase, initial)
				t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
				project := muxcore.ProjectContext{ID: "fixture", Cwd: root}
				mod := NewModuleWithClientInstanceID("fixture-daemon-install")
				if phase == "warm" {
					if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
						t.Fatalf("initial legacy admission: %v", err)
					}
					t.Setenv(lowercase, replacement)
				} else {
					original := resolveLegacyGitIdentity
					t.Cleanup(func() { resolveLegacyGitIdentity = original })
					changed := false
					resolveLegacyGitIdentity = func(ctx context.Context, cwd, name, prefix string) (string, proxy.ProjectIdentityV2, error) {
						slug, identity, err := original(ctx, cwd, name, prefix)
						if err == nil {
							changed = true
							t.Setenv(lowercase, replacement)
						}
						return slug, identity, err
					}
					if _, _, err := mod.v3Identity(context.Background(), project); err == nil || !changed {
						t.Fatalf("config environment switched during resolution was not refused: changed=%t err=%v", changed, err)
					}
					if _, ok := mod.cache.legacy[cacheKey(project)]; ok {
						t.Fatal("unstable environment retained legacy scope")
					}
					resolveLegacyGitIdentity = original
				}
				if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
					t.Fatalf("refreshed legacy admission: %v", err)
				}
				_, identity, err := mod.proxyV2Identity(context.Background(), project)
				if err != nil || identity.GetGitRemote() != "https://example.invalid/changed/Daemon.git" {
					t.Fatal("lowercase config switch reused stale project scope")
				}
			})
		}
	}
}

func TestLegacySymlinkConfigTargetTransition(t *testing.T) {
	for _, phase := range []string{"warm", "during-resolution"} {
		t.Run(phase, func(t *testing.T) {
			root := daemonV3Repository(t)
			if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			target, link := filepath.Join(dir, "config.target"), filepath.Join(dir, "global.gitconfig")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("config.target", link); err != nil {
				t.Fatalf("config symlink capability unavailable: %v", err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", link)
			project := muxcore.ProjectContext{ID: "fixture", Cwd: root}
			mod := NewModuleWithClientInstanceID("fixture-daemon-install")
			replaceTarget := func() {
				replacement := filepath.Join(dir, "replacement.target")
				if err := os.WriteFile(replacement, []byte("[url \"https://example.invalid/changed/\"]\n\tinsteadOf = https://git.example.test/Platform/\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, target); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "warm" {
				if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
					t.Fatalf("ordinary symlinked config refused: %v", err)
				}
				replaceTarget()
				if legacyFilesUnchanged(mod.cache.legacy[cacheKey(project)].files) {
					t.Fatal("replaced config target passed the cached fingerprint")
				}
			} else {
				original := resolveLegacyGitIdentity
				t.Cleanup(func() { resolveLegacyGitIdentity = original })
				changed := false
				resolveLegacyGitIdentity = func(ctx context.Context, cwd, name, prefix string) (string, proxy.ProjectIdentityV2, error) {
					slug, identity, err := original(ctx, cwd, name, prefix)
					if err == nil {
						changed = true
						replaceTarget()
					}
					return slug, identity, err
				}
				if _, _, err := mod.v3Identity(context.Background(), project); err == nil || !changed {
					t.Fatalf("target changed during resolution was not inspected and refused: changed=%t err=%v", changed, err)
				}
				if _, ok := mod.cache.legacy[cacheKey(project)]; ok {
					t.Fatal("unstable symlink target retained legacy scope")
				}
				resolveLegacyGitIdentity = original
			}
			if _, enabled, err := mod.v3Identity(context.Background(), project); err != nil || enabled {
				t.Fatalf("refreshed symlink config refused: %v", err)
			}
			_, identity, err := mod.proxyV2Identity(context.Background(), project)
			if err != nil || identity.GetGitRemote() != "https://example.invalid/changed/Daemon.git" {
				t.Fatal("symlink target replacement reused stale project scope")
			}
		})
	}
}

func TestLegacyConfigSymlinkFingerprintBoundaries(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.target"), filepath.Join(dir, "second.target")
	content := []byte("[fixture]\nvalue = same\n")
	for _, filename := range []string{first, second} {
		if err := os.WriteFile(filename, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inner, outer := filepath.Join(dir, "inner.link"), filepath.Join(dir, "global.gitconfig")
	for link, target := range map[string]string{inner: "first.target", outer: "inner.link"} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	configs := map[string]bool{outer: true}
	snapshot := func() map[string]legacyFileState {
		t.Helper()
		result, err := legacyFileFingerprints([]string{outer}, configs)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Run("same-content-target-replacement", func(t *testing.T) {
		before := snapshot()
		replacement := filepath.Join(dir, "replacement.target")
		if err := os.WriteFile(replacement, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, first); err != nil {
			t.Fatal(err)
		}
		if legacyFilesUnchanged(before) {
			t.Fatal("same-content config target replacement did not invalidate identity")
		}
	})
	t.Run("same-target-link-replacement", func(t *testing.T) {
		before := snapshot()
		replacement := filepath.Join(dir, "replacement.link")
		if err := os.Symlink("first.target", replacement); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, inner); err != nil {
			t.Fatal(err)
		}
		if legacyFilesUnchanged(before) {
			t.Fatal("same-target intermediate link replacement did not invalidate identity")
		}
	})
	t.Run("retargeted-link", func(t *testing.T) {
		before := snapshot()
		replacement := filepath.Join(dir, "retargeted.link")
		if err := os.Symlink("second.target", replacement); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, inner); err != nil {
			t.Fatal(err)
		}
		if legacyFilesUnchanged(before) {
			t.Fatal("retargeted config link did not invalidate identity")
		}
	})
	t.Run("non-config-link-remains-refused", func(t *testing.T) {
		if _, err := legacyFileFingerprints([]string{outer}, nil); err == nil {
			t.Fatal("config-link support weakened the non-config file guard")
		}
	})
	t.Run("loop-refused", func(t *testing.T) {
		loop := filepath.Join(dir, "loop.gitconfig")
		if err := os.Symlink("loop.gitconfig", loop); err != nil {
			t.Fatal(err)
		}
		if _, err := legacyFileFingerprints([]string{loop}, map[string]bool{loop: true}); err == nil {
			t.Fatal("config symlink loop was accepted")
		}
	})
	t.Run("absent-target-creation", func(t *testing.T) {
		link, target := filepath.Join(dir, "absent.gitconfig"), filepath.Join(dir, "absent.target")
		if err := os.Symlink("absent.target", link); err != nil {
			t.Fatal(err)
		}
		before, err := legacyFileFingerprints([]string{link}, map[string]bool{link: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if legacyFilesUnchanged(before) {
			t.Fatal("creation of absent config-link target reused a cache entry")
		}
	})
	t.Run("oversized-target-refused", func(t *testing.T) {
		target, link := filepath.Join(dir, "oversized.target"), filepath.Join(dir, "oversized.gitconfig")
		if err := os.WriteFile(target, make([]byte, 1024*1024+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("oversized.target", link); err != nil {
			t.Fatal(err)
		}
		if _, err := legacyFileFingerprints([]string{link}, map[string]bool{link: true}); err == nil {
			t.Fatal("config symlink bypassed the dependency size bound")
		}
	})
}

func TestLegacyConfigDependenciesRequireFullDerivation(t *testing.T) {
	root := daemonV3Repository(t)
	if output, err := exec.Command("git", "-C", root, "config", "fixture.multiline", "first\nGIT_CONFIG_GLOBAL=not-a-path\nlast").CombinedOutput(); err != nil {
		t.Fatalf("multiline config: %v: %s", err, output)
	}
	if _, eligible, err := legacyConfigDependencies(context.Background(), root); err != nil || !eligible {
		t.Fatalf("file-only config is not complete: %v", err)
	}
	for _, key := range []string{"include.path", "includeIf.gitdir:/not-selected/.path"} {
		t.Run(key, func(t *testing.T) {
			if output, err := exec.Command("git", "-C", root, "config", key, filepath.Join(root, "absent.gitconfig")).CombinedOutput(); err != nil {
				t.Fatalf("include config: %v: %s", err, output)
			}
			if _, eligible, err := legacyConfigDependencies(context.Background(), root); err != nil || eligible {
				t.Fatalf("incomplete include config was reusable: eligible=%t err=%v", eligible, err)
			}
			if output, err := exec.Command("git", "-C", root, "config", "--unset", key).CombinedOutput(); err != nil {
				t.Fatalf("remove fixture include: %v: %s", err, output)
			}
		})
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "fixture.command")
	t.Setenv("GIT_CONFIG_VALUE_0", "value")
	if _, eligible, err := legacyConfigDependencies(context.Background(), root); err != nil || eligible {
		t.Fatalf("command-origin config was reusable: eligible=%t err=%v", eligible, err)
	}
}

func TestProxyLegacyWarmScopeRejectsNearerRepository(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	root := daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nearer := filepath.Join(root, "packages")
	project.Cwd = filepath.Join(nearer, "app")
	if err := os.MkdirAll(project.Cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", nearer, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("nearer repo: %v: %s", err, output)
	}
	for range 2 {
		srv.callReq = nil
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
			t.Fatal("nearer repository reused old scoped authority")
		}
		if srv.callReq != nil {
			t.Fatal("nearer repository reached old memory scope")
		}
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || string(before) != string(after) {
		t.Fatal("topology transition mutated old index")
	}
}

func TestProxyLegacyWarmScopeRefreshesIncludedOrigin(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	included := filepath.Join(project.Cwd, "origin.gitconfig")
	for _, args := range [][]string{{"config", "--unset", "remote.origin.url"}, {"config", "include.path", included}} {
		if output, err := exec.Command("git", append([]string{"-C", project.Cwd}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("include config: %v: %s", err, output)
		}
	}
	writeOrigin := func(remote string) {
		if err := os.WriteFile(included, []byte("[remote \"origin\"]\nurl = "+remote+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeOrigin("https://github.com/thebtf/engram.git")
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	writeOrigin("https://git.example.test/new/repo.git")
	for range 2 {
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if srv.callReq.GetProjectIdentity().GetGitRemote() != "https://git.example.test/new/repo.git" {
			t.Fatal("included config reused old scope")
		}
	}
}

func TestProxyLegacyNestedScopeIsReadOnly(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	root := daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	project.Cwd = filepath.Join(root, "nested")
	if err := os.Mkdir(project.Cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := mod.ProxyTools(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if srv.callReq.GetProjectIdentity().GetRelativePath() != "nested/" {
			t.Fatal("nested identity dropped its selected prefix")
		}
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || string(before) != string(after) {
		t.Fatal("memory requests mutated fixture Git index")
	}
	for _, marker := range []string{".engram-project", ".engram-project-v2.json"} {
		if _, err := os.Stat(filepath.Join(project.Cwd, marker)); !os.IsNotExist(err) {
			t.Fatal("memory requests created a selected-directory marker")
		}
	}
}

func TestProxyLegacyWarmScopeNeedsNoGitAndRechecksMarker(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	marker := filepath.Join(project.Cwd, ".engram-project")
	if err := os.WriteFile(marker, []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("warm identity still requires Git: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mod.ProxyHandleTool(cancelled, project, "recall", json.RawMessage(`{}`)); err == nil {
		t.Fatal("cancelled warm request accepted")
	}
	if err := os.WriteFile(marker, []byte(`{"version":3,"name":"partial"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
		t.Fatal("changed marker reused legacy cache")
	}
	if err := os.WriteFile(marker, []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", originalPath)
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	mod.OnProjectRemoved(project.ID)
	t.Setenv("PATH", t.TempDir())
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
		t.Fatal("Forget retained a warm legacy classification")
	}
}

func TestProxyLegacyNameOnlyWorkspaceUsesExistingV2Scope(t *testing.T) {
	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "recall"}}},
		callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)},
	}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", project.Cwd, "remote", "set-url", "origin", "https://github.com/thebtf/engram.git").CombinedOutput(); err != nil {
		t.Fatalf("set fixture origin: %v: %s", err, output)
	}
	before, err := os.ReadFile(filepath.Join(project.Cwd, ".engram-project"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if srv.initReq.GetProject() != "67e398f8" || srv.callReq.GetProject() != "67e398f8" {
		t.Fatalf("legacy scope changed: initialize=%v call=%v", srv.initReq, srv.callReq)
	}
	for _, identity := range []*pb.ProjectIdentityV2{srv.initReq.GetProjectIdentity(), srv.callReq.GetProjectIdentity()} {
		if identity.GetVersion() != 2 || identity.GetGitRemote() != "https://github.com/thebtf/engram.git" || identity.GetRelativePath() != "" {
			t.Fatalf("legacy metadata changed: %v", identity)
		}
	}
	if srv.initReq.GetProjectIdentityV3() != nil || srv.callReq.GetProjectIdentityV3() != nil {
		t.Fatal("legacy route carried V3 authority")
	}
	after, err := os.ReadFile(filepath.Join(project.Cwd, ".engram-project"))
	if err != nil || string(before) != string(after) {
		t.Fatal("legacy marker was mutated")
	}
	if _, err := os.Stat(filepath.Join(project.Cwd, ".engram-project-v2.json")); !os.IsNotExist(err) {
		t.Fatal("legacy dispatch minted a non-Git anchor")
	}
}

func TestProxyLegacyNameOnlyWorkspaceRejectsInvalidAnchors(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"name":""}`, `{"name":42}`, `{"name":" padded "}`,
		`{"name":"line\nfeed"}`, `{"name":"engram","extra":true}`,
		`{"name":"engram","version":2}`, `{"name":"engram","version":3}`,
		`{"name":"engram","project_id":"invalid","scope":"repository","version":3}`,
		`{"name":"engram","project_id":"22222222-2222-4222-8222-222222222222","scope":"invalid","version":3}`,
		`{"name":"engram","name":"other"}`, `{"name":"engram"} {}`, `{`,
	} {
		t.Run(raw, func(t *testing.T) {
			srv := &mockEngramServer{}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
			mod.v3ClientInstanceID = "fixture-daemon-install"
			project.Cwd = daemonV3Repository(t)
			if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := mod.ProxyTools(context.Background(), project); err == nil {
				t.Fatal("invalid discovery accepted")
			}
			if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
				t.Fatal("invalid call accepted")
			}
			if srv.initReq != nil || srv.callReq != nil {
				t.Fatal("invalid anchor reached transport")
			}
		})
	}
}

func TestProxyLegacyNameOnlyWorkspaceRequiresTrackedGitScope(t *testing.T) {
	for _, boundary := range []string{"missing", "untracked", "missing-origin", "conflicting-directory-anchor"} {
		t.Run(boundary, func(t *testing.T) {
			srv := &mockEngramServer{}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
			mod.v3ClientInstanceID = "fixture-daemon-install"
			project.Cwd = t.TempDir()
			for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", "https://github.com/thebtf/engram.git"}} {
				if output, err := exec.Command("git", append([]string{"-C", project.Cwd}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("fixture git: %v: %s", err, output)
				}
			}
			if boundary != "missing" {
				if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if boundary != "missing" && boundary != "untracked" {
				if output, err := exec.Command("git", "-C", project.Cwd, "add", ".engram-project").CombinedOutput(); err != nil {
					t.Fatalf("track marker: %v: %s", err, output)
				}
			}
			if boundary == "missing-origin" {
				if output, err := exec.Command("git", "-C", project.Cwd, "remote", "remove", "origin").CombinedOutput(); err != nil {
					t.Fatalf("remove fixture origin: %v: %s", err, output)
				}
			}
			if boundary == "conflicting-directory-anchor" {
				project.Cwd = filepath.Join(project.Cwd, "nested")
				if err := os.Mkdir(project.Cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"version":3,"name":"partial"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Existing unborn-repository discovery may remain authority-free;
			// no missing or invalid scope may reach a V2 memory request.
			_, _ = mod.ProxyTools(context.Background(), project)
			if srv.initReq != nil && (srv.initReq.GetProject() != "" || srv.initReq.GetProjectIdentity() != nil) {
				t.Fatal("invalid scope reached V2 discovery")
			}
			if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
				t.Fatal("invalid scope reached memory")
			}
			if srv.callReq != nil {
				t.Fatal("invalid scope reached CallTool")
			}
			if _, err := os.Stat(filepath.Join(project.Cwd, ".engram-project-v2.json")); !os.IsNotExist(err) {
				t.Fatal("invalid scope minted a V2 anchor")
			}
		})
	}
}

func TestProxyV3DescriptorForwardsWithoutV2Fallback(t *testing.T) {
	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{
			Tools:               []*pb.ToolDefinition{{Name: "recall", Description: "recall"}},
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
		callResp: &pb.CallToolResponse{
			ContentJson:         []byte(`[]`),
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
	}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"

	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatalf("V3 Initialize: %v", err)
	}
	assertV3InitializeRequest(t, srv.initReq)

	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("V3 CallTool: %v", err)
	}
	assertV3CallRequest(t, srv.callReq)
}

func TestProxyV3UnbornRepositoryAllowsOnlyUnscopedUCIDiscovery(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{
		{Name: "recall", Description: "scoped memory tool"},
		{Name: "codebase_context", Description: "select an authorized checkout"},
	}}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.Cwd = daemonV3UnbornRepository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"

	tools, err := mod.ProxyTools(context.Background(), project)
	if err != nil {
		t.Fatalf("unborn V3 discovery: %v", err)
	}
	if srv.initReq == nil || srv.initReq.GetProjectIdentityV3() != nil || srv.initReq.GetProject() != "" || srv.initReq.GetProjectIdentity() != nil {
		t.Fatalf("unborn discovery invented project authority: %#v", srv.initReq)
	}
	if len(tools) != 1 || tools[0].Name != "codebase_context" {
		t.Fatalf("unborn discovery tools=%#v, want only UCI checkout selection", tools)
	}
	_, err = mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_ANCHOR_INVALID" || srv.callReq != nil {
		t.Fatalf("unborn scoped proxy error=%v call=%#v, want local anchor refusal", err, srv.callReq)
	}

	anchorless := project
	anchorless.Cwd = t.TempDir()
	tools, err = mod.ProxyTools(context.Background(), anchorless)
	if err != nil || len(tools) != 1 || tools[0].Name != "codebase_context" {
		t.Fatalf("anchorless discovery tools=%#v error=%v, want only UCI checkout selection", tools, err)
	}
	_, err = mod.ProxyHandleTool(context.Background(), anchorless, "recall", json.RawMessage(`{}`))
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_ANCHOR_INVALID" {
		t.Fatalf("anchorless scoped proxy error=%v, want PROJECT_ANCHOR_INVALID", err)
	}
}

func TestProxyV3CarriesStableDaemonComparisonMetadata(t *testing.T) {
	srv := &mockEngramServer{
		initResp:     &pb.InitializeResponse{CanonicalProject: daemonV3CanonicalProject, ProjectResolutionV3: resolvedV3Response()},
		callResp:     &pb.CallToolResponse{ContentJson: []byte(`[]`), CanonicalProject: daemonV3CanonicalProject, ProjectResolutionV3: resolvedV3Response()},
		registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: resolvedV3Response()},
	}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.Cwd = daemonV3Repository(t)
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.HandleTool(context.Background(), project, projectIdentityV3RegistrationTool, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, received := range []metadata.MD{srv.initMetadata, srv.callMetadata, srv.registerMetadata} {
		assertDaemonComparisonMetadata(t, received)
	}

	first := daemonComparisonContextV3(context.Background())
	second := daemonComparisonContextV3(first)
	firstMetadata, _ := metadata.FromOutgoingContext(first)
	secondMetadata, _ := metadata.FromOutgoingContext(second)
	if !reflect.DeepEqual(firstMetadata.Get("x-request-id"), secondMetadata.Get("x-request-id")) {
		t.Fatalf("replay request IDs differ: first=%q second=%q", firstMetadata.Get("x-request-id"), secondMetadata.Get("x-request-id"))
	}
}

func assertDaemonComparisonMetadata(t *testing.T, received metadata.MD) {
	t.Helper()
	if got := received.Get("x-engram-project-identity-adapter"); len(got) != 1 || got[0] != "daemon" {
		t.Fatalf("adapter metadata=%q", got)
	}
	requestIDs := received.Get("x-request-id")
	if len(requestIDs) != 1 {
		t.Fatalf("request IDs=%q", requestIDs)
	}
	if _, err := uuid.Parse(requestIDs[0]); err != nil {
		t.Fatalf("request ID=%q err=%v", requestIDs[0], err)
	}
}

func TestProxyV3RefusalClearsCompatibilityCacheAndExposesOnlyTypedOutcome(t *testing.T) {
	srv := &mockEngramServer{callErr: typedV3Refusal(t, "PROJECT_ONBOARDING_REQUIRED")}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	_, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) {
		t.Fatalf("error=%v, want typed module error", err)
	}
	if moduleErr.Code != "PROJECT_ONBOARDING_REQUIRED" || moduleErr.Message != "project identity resolution refused" {
		t.Fatalf("typed refusal=%#v", moduleErr)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("V3 refusal retained a compatibility cache entry")
	}
	if strings.Contains(err.Error(), project.ID) || strings.Contains(err.Error(), project.Cwd) || strings.Contains(err.Error(), "candidate") || strings.Contains(err.Error(), "credential") {
		t.Fatalf("refusal leaked private identity material: %v", err)
	}
	assertV3CallRequest(t, srv.callReq)
}

func TestV3OnboardingKeepsStaticRegistrationVisibleAndResumesProxy(t *testing.T) {
	srv := &mockEngramServer{
		initErr:      typedV3Refusal(t, "PROJECT_ONBOARDING_REQUIRED"),
		registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: resolvedV3Response()},
		callResp: &pb.CallToolResponse{
			ContentJson:         []byte(`[]`),
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
	}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	invoke := func(request []byte) ([]byte, error) {
		return dispatcher.HandleRequest(context.Background(), project, request)
	}
	assertCache := func() bool {
		return mod.cache.HasEntry(project.ID)
	}
	assertV3OnboardingStaticRegistration(t, srv, project.ID, project.Cwd, assertCache, invoke)
	assertV3OnboardingProxy(t, srv, assertCache, invoke)
}

func assertV3OnboardingStaticRegistration(t *testing.T, srv *mockEngramServer, projectID, projectCWD string, hasCache func() bool, invoke func([]byte) ([]byte, error)) {
	before, err := invoke(jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("onboarding tools/list: %v", err)
	}
	assertOnlyRegistrationTool(t, before)
	if hasCache() {
		t.Fatal("onboarding refusal retained a compatibility cache entry")
	}
	if strings.Contains(string(before), projectID) || strings.Contains(string(before), projectCWD) {
		t.Fatalf("onboarding tools/list leaked local identity material: %s", before)
	}
	for id := 2; id <= 3; id++ {
		registration, err := invoke(jsonrpcCallReq(id, "project_identity.register_v3"))
		if err != nil {
			t.Fatalf("registration %d: %v", id, err)
		}
		assertRegistrationResolved(t, registration)
	}
	srv.mu.Lock()
	srv.initErr = nil
	srv.initResp = &pb.InitializeResponse{
		Tools:               []*pb.ToolDefinition{{Name: "recall", Description: "recall"}},
		CanonicalProject:    daemonV3CanonicalProject,
		ProjectResolutionV3: resolvedV3Response(),
	}
	registerCalls := srv.registerCalls
	registerReq := srv.registerReq
	initCalls := srv.initCalls
	callReq := srv.callReq
	srv.mu.Unlock()
	if initCalls != 1 || callReq != nil {
		t.Fatalf("registration bypassed static dispatch: Initialize calls=%d CallTool request=%#v", initCalls, callReq)
	}
	if registerCalls != 2 {
		t.Fatalf("registration calls=%d, want 2 to preserve server idempotency", registerCalls)
	}
	if registerReq == nil || registerReq.GetProjectIdentityV3() == nil {
		t.Fatalf("registration request=%#v, want descriptor-only request", registerReq)
	}
	assertV3Descriptor(t, registerReq.GetProjectIdentityV3())
	if strings.Contains(registerReq.String(), projectID) || strings.Contains(registerReq.String(), projectCWD) {
		t.Fatalf("registration request leaked raw mux identity: %s", registerReq)
	}
}

func assertV3OnboardingProxy(t *testing.T, srv *mockEngramServer, hasCache func() bool, invoke func([]byte) ([]byte, error)) {
	after, err := invoke(jsonrpcListReq(4))
	if err != nil {
		t.Fatalf("post-registration tools/list: %v", err)
	}
	assertRegistrationAndProxyTool(t, after, "recall")
	if _, err := invoke(jsonrpcCallReq(5, "recall")); err != nil {
		t.Fatalf("post-registration V3 tool: %v", err)
	}
	assertV3InitializeRequest(t, srv.initReq)
	assertV3CallRequest(t, srv.callReq)
	if hasCache() {
		t.Fatal("V3 registration or normal proxy route populated a compatibility cache entry")
	}
}

func TestV3RegistrationRejectsNonEmptyOrNullArgumentsWithoutCallingGRPC(t *testing.T) {
	srv := &mockEngramServer{}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-reach-registration")

	for _, args := range []json.RawMessage{
		json.RawMessage(`{"project_key":"must-not-be-accepted"}`),
		json.RawMessage(`null`),
	} {
		_, err := mod.HandleTool(context.Background(), project, projectIdentityV3RegistrationTool, args)
		var moduleErr *module.ModuleError
		if !errors.As(err, &moduleErr) || moduleErr.Code != "tool_input_invalid" {
			t.Fatalf("registration input error=%v, want safe typed refusal", err)
		}
		if strings.Contains(err.Error(), project.ID) || strings.Contains(err.Error(), project.Cwd) || strings.Contains(err.Error(), "must-not-be-accepted") {
			t.Fatalf("registration argument refusal leaked private input: %v", err)
		}
	}
	srv.mu.Lock()
	registerCalls := srv.registerCalls
	srv.mu.Unlock()
	if registerCalls != 0 {
		t.Fatalf("registration RPC calls=%d, want 0", registerCalls)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("registration argument refusal retained a compatibility cache entry")
	}
}

func TestV3NonOnboardingRefusalFailsClosedWithoutProxyTools(t *testing.T) {
	srv := &mockEngramServer{initErr: typedV3Refusal(t, "PROJECT_SCOPE_MISMATCH")}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("non-onboarding tools/list: %v", err)
	}
	assertToolsListServiceUnavailable(t, response)
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("non-onboarding V3 refusal retained a compatibility cache entry")
	}
}

func TestV3MixedOnboardingRefusalFailsClosedWithoutProxyTools(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "project identity resolution refused").WithDetails(
		&errdetails.ErrorInfo{
			Domain: "engram.project_identity.v3",
			Reason: string(projectidentity.ProjectOnboardingRequiredOutcomeV3),
		},
		&errdetails.ErrorInfo{
			Domain: "engram.project_identity.v3",
			Reason: string(projectidentity.ProjectScopeMismatchOutcomeV3),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	srv := &mockEngramServer{initErr: st.Err()}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("mixed onboarding tools/list: %v", err)
	}
	assertToolsListServiceUnavailable(t, response)
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("mixed V3 refusal retained a compatibility cache entry")
	}
}

func TestV2DoesNotExposeRegistrationOrFallBackToV2(t *testing.T) {
	srv := &mockEngramServer{}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)

	if tools := mod.Tools(); len(tools) != 0 {
		t.Fatalf("V2 static tools=%v, want unchanged surface", tools)
	}
	_, err := mod.HandleTool(context.Background(), project, projectIdentityV3RegistrationTool, json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_DESCRIPTOR_UNSUPPORTED" {
		t.Fatalf("V2 registration error=%v, want V3-only refusal", err)
	}
	srv.mu.Lock()
	registerCalls := srv.registerCalls
	srv.mu.Unlock()
	if registerCalls != 0 {
		t.Fatalf("V2 registration RPC calls=%d, want 0", registerCalls)
	}
}

func TestV2DynamicRegistrationToolIsHiddenAndNeverProxied(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{
		{Name: projectIdentityV3RegistrationTool},
		{Name: "recall"},
	}}}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildContractDispatcher(t, grpcAddr)
	mod.cache.ForceCacheEntry(project, "cached-v2-selector")

	listed, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("V2 tools/list: %v", err)
	}
	var tools struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listed, &tools); err != nil {
		t.Fatalf("unmarshal V2 tools/list: %v", err)
	}
	if len(tools.Result.Tools) != 1 || tools.Result.Tools[0].Name != "recall" {
		t.Fatalf("V2 tools/list=%s, want only backend recall", listed)
	}

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(2, projectIdentityV3RegistrationTool))
	if err != nil {
		t.Fatalf("reserved V2 tools/call: %v", err)
	}
	if !strings.Contains(string(response), "PROJECT_DESCRIPTOR_UNSUPPORTED") {
		t.Fatalf("reserved V2 tools/call=%s, want safe V3-only refusal", response)
	}
	srv.mu.Lock()
	callReq := srv.callReq
	srv.mu.Unlock()
	if callReq != nil {
		t.Fatalf("reserved V2 tool reached backend CallTool: %#v", callReq)
	}
	if !mod.cache.HasEntry(project.ID) {
		t.Fatal("reserved V2 tool mutated the compatibility selector cache")
	}
}

type v3RegistrationFailureCase struct {
	name       string
	resolution *pb.ProjectResolutionResultV3
	mustHide   []string
}

func TestV3RegistrationResponseValidationFailsClosed(t *testing.T) {
	projectKey := daemonV3CanonicalProject
	repositoryScope := "repository"
	directoryScope := "directory"
	for _, test := range v3RegistrationFailureCases(projectKey, repositoryScope, directoryScope) {
		t.Run(test.name, func(t *testing.T) {
			runV3RegistrationFailureCase(t, test)
		})
	}
}

func v3RegistrationFailureCases(projectKey, repositoryScope, directoryScope string) []v3RegistrationFailureCase {
	return []v3RegistrationFailureCase{
		{name: "nil resolution"},
		{name: "refusal with authority", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_SCOPE_MISMATCH, Correlation: "refusal-correlation", ProjectKey: &projectKey, ResolvedScope: &repositoryScope}, mustHide: []string{projectKey}},
		{name: "invalid canonical key", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, Correlation: "invalid-key-correlation", ProjectKey: stringPtr("private-client-asserted-key"), ResolvedScope: &repositoryScope}, mustHide: []string{"private-client-asserted-key"}},
		{name: "descriptor scope mismatch", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, Correlation: "scope-mismatch-correlation", ProjectKey: &projectKey, ResolvedScope: &directoryScope}, mustHide: []string{projectKey}},
		{name: "unsafe correlation", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, Correlation: "private/path/correlation", ProjectKey: &projectKey, ResolvedScope: &repositoryScope}, mustHide: []string{"private/path/correlation", projectKey}},
		{name: "resolved outcome with redirect reference", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, Correlation: "resolved-redirect-correlation", ProjectKey: &projectKey, ResolvedScope: &repositoryScope, RedirectReference: stringPtr("redirect-audit-17")}, mustHide: []string{"redirect-audit-17", projectKey}},
		{name: "redirected outcome without reference", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_REDIRECTED, Correlation: "redirect-correlation", ProjectKey: &projectKey, ResolvedScope: &repositoryScope}, mustHide: []string{projectKey}},
	}
}

func runV3RegistrationFailureCase(t *testing.T, test v3RegistrationFailureCase) {
	srv := &mockEngramServer{registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: test.resolution}}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-invalid-registration")
	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(1, projectIdentityV3RegistrationTool))
	if err != nil {
		t.Fatalf("registration: %v", err)
	}
	if !strings.Contains(string(response), `"isError":true`) || !strings.Contains(string(response), "PROJECT_RESOLUTION_UNAVAILABLE") {
		t.Fatalf("registration response=%s, want safe typed error", response)
	}
	for _, private := range append(test.mustHide, project.ID, project.Cwd) {
		if strings.Contains(string(response), private) {
			t.Fatalf("invalid registration leaked %q: %s", private, response)
		}
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("invalid registration retained a compatibility cache entry")
	}
}

func TestV3RegistrationValidResponseSerializesResolution(t *testing.T) {
	projectKey := daemonV3CanonicalProject
	repositoryScope := "repository"
	for _, test := range []struct {
		name       string
		resolution *pb.ProjectResolutionResultV3
		outcome    string
	}{
		{name: "resolved", resolution: resolvedV3Response(), outcome: "PROJECT_RESOLVED"},
		{
			name: "redirected",
			resolution: &pb.ProjectResolutionResultV3{
				Outcome:           pb.ProjectResolutionOutcomeV3_PROJECT_REDIRECTED,
				Correlation:       "redirected-correlation",
				ProjectKey:        &projectKey,
				ResolvedScope:     &repositoryScope,
				RedirectReference: stringPtr("redirect-audit-17"),
			},
			outcome: "PROJECT_REDIRECTED",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := &mockEngramServer{registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: test.resolution}}
			grpcAddr := startMockGRPC(t, srv)
			dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
			project.Cwd = daemonV3Repository(t)
			response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(1, projectIdentityV3RegistrationTool))
			if err != nil {
				t.Fatalf("registration: %v", err)
			}
			if !strings.Contains(string(response), `"isError":false`) || !strings.Contains(string(response), test.outcome) {
				t.Fatalf("registration response=%s, want %s success", response, test.outcome)
			}
			if mod.cache.HasEntry(project.ID) {
				t.Fatal("valid V3 registration populated a compatibility cache entry")
			}
		})
	}
}

func stringPtr(value string) *string {
	return &value
}

func assertOnlyRegistrationTool(t *testing.T, response []byte) {
	t.Helper()
	var got struct {
		Error  any `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if got.Error != nil || len(got.Result.Tools) != 1 || got.Result.Tools[0].Name != "project_identity.register_v3" {
		t.Fatalf("onboarding tools/list=%s, want static registration tool only", response)
	}
}

func assertRegistrationAndProxyTool(t *testing.T, response []byte, proxyName string) {
	t.Helper()
	var got struct {
		Error  any `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if got.Error != nil || len(got.Result.Tools) != 2 || got.Result.Tools[0].Name != "project_identity.register_v3" || got.Result.Tools[1].Name != proxyName {
		t.Fatalf("post-registration tools/list=%s, want static registration and proxy tool", response)
	}
}

func assertRegistrationResolved(t *testing.T, response []byte) {
	t.Helper()
	if !strings.Contains(string(response), `"isError":false`) || !strings.Contains(string(response), `"PROJECT_RESOLVED"`) {
		t.Fatalf("registration response=%s, want typed resolved outcome", response)
	}
}

func TestProxyV3RejectsCanonicalProjectNotIssuedByResolution(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{
		ContentJson:         []byte(`[]`),
		CanonicalProject:    "client-selected-project-must-not-enter-context",
		ProjectResolutionV3: resolvedV3Response(),
	}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"

	_, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_RESOLUTION_UNAVAILABLE" {
		t.Fatalf("error=%v, want server-resolution-only refusal", err)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("noncanonical V3 response retained a compatibility cache entry")
	}
	assertV3CallRequest(t, srv.callReq)
}

func assertV3InitializeRequest(t *testing.T, req *pb.InitializeRequest) {
	t.Helper()
	if req == nil || req.GetProjectIdentityV3() == nil {
		t.Fatalf("Initialize request=%#v, want V3 descriptor", req)
	}
	if req.GetProject() != "" || req.GetProjectIdentity() != nil {
		t.Fatalf("Initialize used V2/raw fallback: %#v", req)
	}
	assertV3Descriptor(t, req.GetProjectIdentityV3())
}

func assertV3CallRequest(t *testing.T, req *pb.CallToolRequest) {
	t.Helper()
	if req == nil || req.GetProjectIdentityV3() == nil {
		t.Fatalf("CallTool request=%#v, want V3 descriptor", req)
	}
	if req.GetProject() != "" || req.GetProjectIdentity() != nil {
		t.Fatalf("CallTool used V2/raw fallback: %#v", req)
	}
	assertV3Descriptor(t, req.GetProjectIdentityV3())
}

func assertV3Descriptor(t *testing.T, identity *pb.ProjectIdentityV3) {
	t.Helper()
	if identity.GetVersion() != 3 || identity.GetAnchorProjectId() != "22222222-2222-4222-8222-222222222222" || identity.GetName() != "daemon-v3" || identity.GetScope() != "repository" || identity.GetClientInstanceId() != "fixture-daemon-install" {
		t.Fatalf("descriptor=%#v", identity)
	}
	if got := identity.GetNormalizedGitRemotes(); len(got) != 1 || got[0] != "git.example.test/Platform/Daemon" {
		t.Fatalf("normalized remotes=%q", got)
	}
	if len(identity.GetLegacyIdentifiers()) != 0 {
		t.Fatalf("legacy identifiers=%#v, want none", identity.GetLegacyIdentifiers())
	}
}

func resolvedV3Response() *pb.ProjectResolutionResultV3 {
	projectKey := daemonV3CanonicalProject
	scope := "repository"
	return &pb.ProjectResolutionResultV3{
		Outcome:       pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED,
		Correlation:   "daemon-v3-correlation",
		ProjectKey:    &projectKey,
		ResolvedScope: &scope,
	}
}

func typedV3Refusal(t *testing.T, outcome string) error {
	t.Helper()
	st, err := status.New(codes.FailedPrecondition, "project identity resolution refused").WithDetails(&errdetails.ErrorInfo{
		Domain:   "engram.project_identity.v3",
		Reason:   outcome,
		Metadata: map[string]string{"correlation": "daemon-v3-correlation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func daemonV3Repository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"remote", "add", "origin", "https://git.example.test/Platform/Daemon.git"},
		{"add", ".engram-project"},
	} {
		if args[0] == "add" {
			if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte("{\"version\":3,\"project_id\":\"22222222-2222-4222-8222-222222222222\",\"name\":\"daemon-v3\",\"scope\":\"repository\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}

func daemonV3UnbornRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("initialize unborn repository: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "unborn.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write unborn fixture: %v", err)
	}
	if output, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("unborn fixture unexpectedly has HEAD: %s", output)
	}
	return root
}
