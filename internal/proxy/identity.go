package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ResolveProjectSlug computes a stable, cross-platform project identity for the
// given working directory. The algorithm mirrors plugin/engram/hooks/lib.js:37-66
// exactly so that JS hooks and Go server agree on project identity.
//
// Primary path (git repo with remote):
//   - key = remoteURL + "/" + relativePathWithinRepo
//   - id = SHA-256(key), first 8 hex chars (pure hash, no dirName prefix)
//   - displayName = dirName (or overridden by .engram-project anchor file)
//
// Fallback path (non-git dir or no remote):
//   - key = absolute path of cwd
//   - id = SHA-256(key), first 6 hex chars (matches LegacyProjectID in lib.js:62-66)
//   - displayName = dirName (or overridden by .engram-project anchor file)
//   - gitRemote = ""
//   - err = nil  (fallback is not an error)
//
// In both cases, a .engram-project JSON anchor file in the directory may override
// displayName and, for non-git projects, the id itself.
func ResolveProjectSlug(cwd string) (id string, displayName string, gitRemote string, err error) {
	resolved, resolveErr := filepath.Abs(cwd)
	if resolveErr != nil {
		return "", "", "", fmt.Errorf("resolve cwd: %w", resolveErr)
	}

	dirName := filepath.Base(resolved)

	remoteURL, relativePath, gitErr := getGitInfo(resolved)
	if gitErr == nil && remoteURL != "" {
		// Primary: git-remote-based ID (8 hex chars)
		key := remoteURL + "/" + relativePath
		hash := sha256Hex(key)
		id = hash[:8]
		displayName = dirName
		id, displayName = applyAnchorFile(resolved, id, displayName, false)
		return id, displayName, remoteURL, nil
	}

	// Fallback: path-based ID (6 hex chars, matches LegacyProjectID)
	hash := sha256Hex(resolved)
	id = hash[:6]
	displayName = dirName
	id, displayName = applyAnchorFile(resolved, id, displayName, true)
	return id, displayName, "", nil
}

// applyAnchorFile reads or creates the .engram-project anchor file in dir.
// It returns the (possibly updated) id and displayName.
// storeID controls whether to persist the id in the anchor file (non-git projects only).
func applyAnchorFile(dir, id, displayName string, storeID bool) (string, string) {
	anchorPath := filepath.Join(dir, ".engram-project")
	data, readErr := os.ReadFile(anchorPath)
	if readErr == nil {
		var anchor struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(data, &anchor) == nil {
			if anchor.Name != "" {
				displayName = anchor.Name
			}
			// For non-git projects, restore a stored ID so it stays stable.
			if storeID && anchor.ID != "" {
				id = anchor.ID
			}
		}
		return id, displayName
	}

	if !os.IsNotExist(readErr) {
		return id, displayName
	}

	// Auto-create .engram-project anchor file.
	anchor := map[string]string{"name": displayName}
	if storeID {
		anchor["id"] = id
	}
	if fileData, marshalErr := json.MarshalIndent(anchor, "", "  "); marshalErr == nil {
		_ = os.WriteFile(anchorPath, append(fileData, '\n'), 0644)
		// Auto-stage in git if we are inside a repo.
		if !storeID {
			exec.Command("git", "-C", dir, "add", ".engram-project").Run() //nolint:errcheck
		}
	}
	return id, displayName
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
