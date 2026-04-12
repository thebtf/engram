package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ResolveProjectSlug computes a stable, cross-platform project slug for the
// given working directory. The algorithm mirrors plugin/engram/hooks/lib.js:37-66
// exactly so that JS hooks and Go server agree on project identity.
//
// Primary path (git repo with remote):
//   - key = remoteURL + "/" + relativePathWithinRepo
//   - hash = SHA-256(key), first 8 hex chars
//   - slug = dirName + "_" + hash
//
// Fallback path (non-git dir or no remote):
//   - key = absolute path of cwd
//   - hash = SHA-256(key), first 6 hex chars  (matches LegacyProjectID in lib.js:62-66)
//   - slug = dirName + "_" + hash
//   - gitRemote = ""
//   - err = nil  (fallback is not an error)
func ResolveProjectSlug(cwd string) (slug string, gitRemote string, err error) {
	resolved, err := filepath.Abs(cwd)
	if err != nil {
		return "", "", fmt.Errorf("resolve cwd: %w", err)
	}

	dirName := filepath.Base(resolved)

	remoteURL, relativePath, gitErr := getGitInfo(resolved)
	if gitErr == nil && remoteURL != "" {
		// Primary: git-remote-based slug (8 hex chars)
		key := remoteURL + "/" + relativePath
		hash := sha256Hex(key)
		return dirName + "_" + hash[:8], remoteURL, nil
	}

	// Fallback: path-based slug (6 hex chars, matches LegacyProjectID)
	hash := sha256Hex(resolved)
	return dirName + "_" + hash[:6], "", nil
}

// getGitInfo runs the two git commands needed for the primary slug.
// Both commands share a single context so the total timeout is bounded.
// Returns (remoteURL, relativePath, error).
func getGitInfo(cwd string) (remoteURL, relativePath string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rawRemote, err := runGit(ctx, cwd, "remote", "get-url", "origin")
	if err != nil {
		return "", "", err
	}
	remoteURL = strings.TrimSpace(rawRemote)
	if remoteURL == "" {
		return "", "", fmt.Errorf("empty remote URL")
	}

	rawPrefix, err := runGit(ctx, cwd, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	relativePath = strings.TrimSpace(rawPrefix)

	return remoteURL, relativePath, nil
}

// runGit executes a git command in the given directory and returns stdout.
// On failure the error message includes stderr so callers get diagnostic detail
// (e.g. "not a git repository") rather than a bare exit-status string.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return string(out), nil
}

// sha256Hex returns the full lowercase hex-encoded SHA-256 hash of s.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum)
}
