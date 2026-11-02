package projectidentity

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type nativeGitContextKey struct{}
type nativeGitDirectories struct{ selected, root, common string }

// NativeGitContext discovers the actual worktree once under a sanitized Git
// environment. Its sealed, request-owned facts can be consumed by
// CheckLogicalProject; they grant no project or source access and are not cached.
func NativeGitContext(ctx context.Context, cwd string) (context.Context, string, error) {
	refuse := func() (context.Context, string, error) {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		return nil, "", logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	if ctx.Err() != nil || !nativeGitPath(cwd) {
		return refuse()
	}
	selected, err := filepath.EvalSymlinks(filepath.Clean(cwd))
	if err != nil {
		return refuse()
	}
	environment := make([]string, 0, 9)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP":
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	command := exec.CommandContext(ctx, "git", "-C", selected, "-c", "core.fsmonitor=false", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--is-bare-repository")
	command.Env = environment
	command.WaitDelay = 100 * time.Millisecond
	output, err := command.Output()
	if err != nil || len(output) > 3*4096+32 {
		return refuse()
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r"), "\n")
	if len(parts) != 3 || strings.TrimSuffix(parts[2], "\r") != "false" {
		return refuse()
	}
	root, common := strings.TrimSuffix(parts[0], "\r"), strings.TrimSuffix(parts[1], "\r")
	if !nativeGitPath(root) || !nativeGitPath(common) {
		return refuse()
	}
	root, err = filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return refuse()
	}
	common, err = filepath.EvalSymlinks(filepath.Clean(common))
	if err != nil {
		return refuse()
	}
	relative, err := filepath.Rel(root, selected)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return refuse()
	}
	path := filepath.ToSlash(root)
	if strings.HasPrefix(path, "//") {
		return refuse()
	}
	if runtime.GOOS == "windows" {
		path = "/" + path
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return context.WithValue(ctx, nativeGitContextKey{}, nativeGitDirectories{selected, root, common}), (&url.URL{Scheme: "file", Path: path}).String(), nil
}

func nativeGitPath(path string) bool {
	return filepath.IsAbs(path) && len(path) <= 4096 && utf8.ValidString(path) && strings.IndexFunc(path, unicode.IsControl) < 0
}

func nativeGitDirectoriesFor(ctx context.Context, selected string) (string, string, bool) {
	facts, ok := ctx.Value(nativeGitContextKey{}).(nativeGitDirectories)
	if !ok {
		return "", "", false
	}
	physical, err := filepath.EvalSymlinks(filepath.Clean(selected))
	if err != nil || !samePath(physical, facts.selected) {
		return "", "", false
	}
	return facts.root, facts.common, true
}
