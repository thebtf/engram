package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var errGitIdentityAbsent = errors.New("git repository or origin remote is absent")

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
func ResolveProjectSlug(ctx context.Context, cwd string) (id string, displayName string, gitRemote string, err error) {
	resolved, resolveErr := filepath.Abs(cwd)
	if resolveErr != nil {
		return "", "", "", fmt.Errorf("resolve cwd: %w", resolveErr)
	}

	dirName := filepath.Base(resolved)

	remoteURL, relativePath, gitErr := getGitInfo(ctx, resolved)
	if gitErr == nil && remoteURL != "" {
		// Primary: git-remote-based ID (8 hex chars)
		key := remoteURL + "/" + relativePath
		hash := sha256Hex(key)
		id = hash[:8]
		displayName = dirName
		id, displayName = applyAnchorFile(resolved, id, displayName, false)
		return id, displayName, remoteURL, nil
	}
	if gitErr != nil && !errors.Is(gitErr, errGitIdentityAbsent) {
		return "", "", "", fmt.Errorf("resolve git identity: %w", gitErr)
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
		_ = os.WriteFile(anchorPath, append(fileData, '\n'), 0o644)
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
func getGitInfo(ctx context.Context, cwd string) (remoteURL, relativePath string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	remoteURL, err = getGitRemote(ctx, cwd)
	if err != nil {
		return "", "", err
	}

	rawPrefix, err := runGit(ctx, cwd, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	relativePath = strings.TrimSpace(rawPrefix)

	return remoteURL, relativePath, nil
}

func getGitRemote(ctx context.Context, cwd string) (string, error) {
	rawRemote, err := runGit(ctx, cwd, "remote", "get-url", "origin")
	if err != nil {
		if isMissingGitIdentityError(err) {
			return "", fmt.Errorf("%w: %v", errGitIdentityAbsent, err)
		}
		return "", err
	}
	remote, err := normalizeGitRemote(strings.TrimSpace(rawRemote))
	if err != nil {
		return "", err
	}
	if remote == "" {
		return "", errGitIdentityAbsent
	}
	return remote, nil
}

func normalizeGitRemote(value string) (string, error) {
	if !gitRemoteHasUserinfo(value) {
		return value, nil
	}
	remoteURL, err := url.Parse(value)
	if err != nil {
		return "", errors.New("git remote URL is malformed")
	}
	remoteURL.User = nil
	return remoteURL.String(), nil
}

func gitRemoteHasUserinfo(value string) bool {
	authority, ok := gitRemoteAuthority(value)
	return ok && strings.Contains(authority, "@")
}

func gitRemoteAuthority(value string) (string, bool) {
	if strings.HasPrefix(value, "//") {
		value = value[2:]
	} else {
		schemeEnd := strings.Index(value, "://")
		if schemeEnd <= 0 || !isURLScheme(value[:schemeEnd]) {
			return "", false
		}
		value = value[schemeEnd+3:]
	}
	if end := strings.IndexAny(value, "/?#"); end >= 0 {
		value = value[:end]
	}
	return value, true
}

func isURLScheme(value string) bool {
	for i := range len(value) {
		char := value[i]
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			i > 0 && (char >= '0' && char <= '9' || char == '+' || char == '-' || char == '.') {
			continue
		}
		return false
	}
	return true
}

func isMissingGitIdentityError(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not a git repository") || strings.Contains(message, "no such remote")
}

// runGit executes a git command in the given directory and returns stdout.
// On failure the error message includes stderr so callers get diagnostic detail
// (e.g. "not a git repository") rather than a bare exit-status string.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
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
