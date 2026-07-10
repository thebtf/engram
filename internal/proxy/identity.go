package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	// ProjectIdentityVersionV2 is the first full, versioned identity contract.
	ProjectIdentityVersionV2 uint32 = 2
	projectIdentityV2File           = ".engram-project-v2.json"
)

var strictAnchorV2 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ProjectIdentityV2 is complete project metadata sent before the first data
// access. Exactly one source form is valid: git remote+relative path, or a
// cryptographically random non-git anchor with explicit sharing presence.
type ProjectIdentityV2 struct {
	Version         uint32 `json:"version"`
	LegacyProjectID string `json:"legacy_project_id,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
	GitRemote       string `json:"git_remote,omitempty"`
	RelativePath    string `json:"relative_path,omitempty"`
	NonGitAnchor    string `json:"non_git_anchor,omitempty"`
	AnchorShared    *bool  `json:"anchor_shared,omitempty"`
}

type projectAnchorV2 struct {
	Version uint32 `json:"version"`
	Anchor  string `json:"anchor"`
	Shared  bool   `json:"shared"`
}

// ValidateProjectIdentityV2 validates the wire contract without filesystem or
// database access. Errors carry a stable machine-readable prefix.
func ValidateProjectIdentityV2(identity ProjectIdentityV2) error {
	invalid := func(reason string) error {
		return fmt.Errorf("PROJECT_IDENTITY_INVALID: %s", reason)
	}
	if identity.Version != ProjectIdentityVersionV2 {
		return invalid("unsupported version")
	}
	if len(identity.LegacyProjectID) > 256 || len(identity.DisplayName) > 256 ||
		strings.TrimSpace(identity.LegacyProjectID) != identity.LegacyProjectID ||
		containsProjectIdentityControl(identity.LegacyProjectID) || containsProjectIdentityControl(identity.DisplayName) {
		return invalid("selector or display name too long")
	}
	hasGit := identity.GitRemote != "" || identity.RelativePath != ""
	hasAnchor := identity.NonGitAnchor != "" || identity.AnchorShared != nil
	if hasGit == hasAnchor {
		return invalid("exactly one identity source is required")
	}
	if hasGit {
		if identity.GitRemote == "" || len(identity.GitRemote) > 2048 {
			return invalid("git_remote is required and bounded")
		}
		if strings.TrimSpace(identity.GitRemote) != identity.GitRemote || containsProjectIdentityControl(identity.GitRemote) {
			return invalid("git_remote is not normalized")
		}
		if identity.NonGitAnchor != "" || identity.AnchorShared != nil {
			return invalid("git identity cannot carry an anchor")
		}
		if len(identity.RelativePath) > 4096 || strings.Contains(identity.RelativePath, "\\") || strings.HasPrefix(identity.RelativePath, "/") || containsProjectIdentityControl(identity.RelativePath) {
			return invalid("relative_path is not normalized POSIX relative form")
		}
		for _, part := range strings.Split(identity.RelativePath, "/") {
			if part == ".." || part == "." {
				return invalid("relative_path contains traversal")
			}
		}
		return nil
	}
	if !strictAnchorV2.MatchString(identity.NonGitAnchor) {
		return invalid("non_git_anchor must be 128-bit lowercase hex")
	}
	if identity.AnchorShared == nil {
		return invalid("anchor_shared presence is required")
	}
	if identity.GitRemote != "" || identity.RelativePath != "" {
		return invalid("non-git identity cannot carry git metadata")
	}
	return nil
}

func containsProjectIdentityControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

// ResolveProjectIdentityV2 builds full metadata for cwd. Git projects are
// content-addressed by normalized remote+relative path. Non-git projects use a
// strict additive anchor file created with O_EXCL so concurrent first use
// converges without overwriting another process's identity.
func ResolveProjectIdentityV2(cwd string) (ProjectIdentityV2, error) {
	resolved, err := filepath.Abs(cwd)
	if err != nil {
		return ProjectIdentityV2{}, fmt.Errorf("resolve cwd: %w", err)
	}
	selector, displayName, _, err := ResolveProjectSlug(resolved)
	if err != nil {
		return ProjectIdentityV2{}, err
	}
	legacyID := filepath.Base(resolved) + "_" + sha256Hex(resolved)[:6]
	remote, relativePath, gitErr := getGitInfo(resolved)
	if gitErr == nil && remote != "" {
		identity := ProjectIdentityV2{
			Version:         ProjectIdentityVersionV2,
			LegacyProjectID: legacyID,
			DisplayName:     displayName,
			GitRemote:       strings.TrimSpace(remote),
			RelativePath:    strings.ReplaceAll(strings.TrimSpace(relativePath), "\\", "/"),
		}
		if err := ValidateProjectIdentityV2(identity); err != nil {
			return ProjectIdentityV2{}, err
		}
		_ = selector // selector remains the outer compatibility field.
		return identity, nil
	}

	anchor, err := readOrCreateProjectAnchorV2(resolved)
	if err != nil {
		return ProjectIdentityV2{}, err
	}
	shared := anchor.Shared
	identity := ProjectIdentityV2{
		Version:         ProjectIdentityVersionV2,
		LegacyProjectID: legacyID,
		DisplayName:     displayName,
		NonGitAnchor:    anchor.Anchor,
		AnchorShared:    &shared,
	}
	if err := ValidateProjectIdentityV2(identity); err != nil {
		return ProjectIdentityV2{}, err
	}
	return identity, nil
}

func readOrCreateProjectAnchorV2(dir string) (projectAnchorV2, error) {
	anchorPath := filepath.Join(dir, projectIdentityV2File)
	for {
		data, err := os.ReadFile(anchorPath)
		if err == nil {
			var anchor projectAnchorV2
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if decodeErr := decoder.Decode(&anchor); decodeErr != nil {
				return projectAnchorV2{}, fmt.Errorf("PROJECT_IDENTITY_INVALID: decode %s: %w", projectIdentityV2File, decodeErr)
			}
			if trailingErr := decoder.Decode(&struct{}{}); trailingErr != io.EOF {
				return projectAnchorV2{}, fmt.Errorf("PROJECT_IDENTITY_INVALID: trailing data in %s", projectIdentityV2File)
			}
			if anchor.Version != ProjectIdentityVersionV2 || !strictAnchorV2.MatchString(anchor.Anchor) {
				return projectAnchorV2{}, fmt.Errorf("PROJECT_IDENTITY_INVALID: malformed %s", projectIdentityV2File)
			}
			return anchor, nil
		}
		if !os.IsNotExist(err) {
			return projectAnchorV2{}, fmt.Errorf("read %s: %w", projectIdentityV2File, err)
		}

		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return projectAnchorV2{}, fmt.Errorf("generate project anchor: %w", err)
		}
		anchor := projectAnchorV2{Version: ProjectIdentityVersionV2, Anchor: hex.EncodeToString(random), Shared: false}
		data, err = json.MarshalIndent(anchor, "", "  ")
		if err != nil {
			return projectAnchorV2{}, fmt.Errorf("encode project anchor: %w", err)
		}
		file, err := os.OpenFile(anchorPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return projectAnchorV2{}, fmt.Errorf("create %s: %w", projectIdentityV2File, err)
		}
		_, writeErr := file.Write(append(data, '\n'))
		closeErr := file.Close()
		if writeErr != nil {
			return projectAnchorV2{}, fmt.Errorf("write %s: %w", projectIdentityV2File, writeErr)
		}
		if closeErr != nil {
			return projectAnchorV2{}, fmt.Errorf("close %s: %w", projectIdentityV2File, closeErr)
		}
		return anchor, nil
	}
}

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
