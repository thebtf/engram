package projectidentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ProjectMarkerState is local evidence for a host-mediated marker edit. It is
// not a grant to write and is never itself returned to the remote service.
type ProjectMarkerState struct {
	RootDigest        string
	TargetRootDigest  string
	Target            string
	MarkerDigest      string
	EvidenceDigest    string
	Present           bool
	LegacyID          string
	PinnedID          string
	PortableID        string
	CacheID           string
	RepositoryLocator string
}

func markerEvidenceDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func readRecoveryMarker(path string) ([]byte, bool, error) {
	return readBoundedRecoveryFile(path, 4096)
}

func readBoundedRecoveryFile(path string, limit int64) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, true, logicalError("PROJECT_SELECTION_STALE")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, true, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) || int64(len(raw)) > limit || current.Size() != int64(len(raw)) || !current.ModTime().Equal(opened.ModTime()) {
		return nil, true, logicalError("PROJECT_SELECTION_STALE")
	}
	return raw, true, nil
}

// InspectProjectMarker follows the nearest marker only inside the caller's Git
// worktree, or the exact caller directory when it is not in a Git worktree.
func InspectProjectMarker(ctx context.Context, cwd string) (ProjectMarkerState, error) {
	if cwd == "" {
		return ProjectMarkerState{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return ProjectMarkerState{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ProjectMarkerState{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	gitRoot, common, err := logicalProjectDirectories(ctx, root)
	if err != nil {
		return ProjectMarkerState{}, err
	}
	targetRoot := root
	if common != "" {
		relative, relErr := filepath.Rel(gitRoot, root)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return ProjectMarkerState{}, logicalError("PROJECT_CONTEXT_REQUIRED")
		}
		for candidate := root; ; candidate = filepath.Dir(candidate) {
			_, exists, readErr := readRecoveryMarker(filepath.Join(candidate, anchorFilenameV3))
			if readErr != nil {
				return ProjectMarkerState{}, readErr
			}
			if exists || samePath(candidate, gitRoot) {
				targetRoot = candidate
				break
			}
			if filepath.Dir(candidate) == candidate {
				return ProjectMarkerState{}, logicalError("PROJECT_CONTEXT_REQUIRED")
			}
		}
	}
	path := filepath.Join(targetRoot, anchorFilenameV3)
	raw, present, err := readRecoveryMarker(path)
	if err != nil {
		return ProjectMarkerState{}, err
	}
	relativeTarget, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relativeTarget) {
		return ProjectMarkerState{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	state := ProjectMarkerState{RootDigest: markerEvidenceDigest([]byte(cwd)), TargetRootDigest: markerEvidenceDigest([]byte(targetRoot)), Target: filepath.ToSlash(relativeTarget), Present: present, MarkerDigest: markerEvidenceDigest(raw)}
	if present {
		project, _, parseErr := parseLogicalMarker(raw, true)
		if parseErr != nil {
			var legacy *LogicalError
			if !errors.As(parseErr, &legacy) || legacy.Code != "PROJECT_MIGRATION_REQUIRED" || !validLegacyMarkerID(legacy.legacyID) {
				return ProjectMarkerState{}, parseErr
			}
			state.LegacyID = legacy.legacyID
		} else {
			state.PinnedID = project.ProjectID
			state.PortableID = project.ProjectID
		}
		current, stillPresent, readErr := readRecoveryMarker(path)
		if readErr != nil || !stillPresent || markerEvidenceDigest(current) != state.MarkerDigest {
			return ProjectMarkerState{}, logicalError("PROJECT_SELECTION_STALE")
		}
	}
	cacheDigest := "not-applicable"
	if common != "" && samePath(targetRoot, gitRoot) {
		remotes, remoteErr := anchorGitCommandV3(ctx, gitRoot, "remote").Output()
		if remoteErr != nil {
			return ProjectMarkerState{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		for _, remote := range strings.Split(strings.TrimSpace(string(remotes)), "\n") {
			if remote != "origin" {
				continue
			}
			value, remoteErr := anchorGitCommandV3(ctx, gitRoot, "remote", "get-url", "origin").Output()
			if remoteErr != nil {
				return ProjectMarkerState{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
			}
			state.RepositoryLocator, remoteErr = NormalizeRepositoryLocator(strings.TrimSpace(string(value)))
			if remoteErr != nil {
				return ProjectMarkerState{}, remoteErr
			}
			break
		}
		cachePath := filepath.Join(common, "engram-project.json")
		cache, exists, cacheErr := readRecoveryMarker(cachePath)
		if cacheErr != nil {
			return ProjectMarkerState{}, cacheErr
		}
		cacheDigest = markerEvidenceDigest(cache)
		if exists {
			cached, _, cacheErr := parseLogicalMarker(cache, false)
			if cacheErr != nil {
				return ProjectMarkerState{}, cacheErr
			}
			state.CacheID = cached.ProjectID
			if state.PinnedID == "" {
				state.PinnedID = cached.ProjectID
			}
		}
	}
	state.EvidenceDigest = markerEvidenceDigest([]byte(state.RootDigest + "\x00" + state.TargetRootDigest + "\x00" + state.Target + "\x00" + state.MarkerDigest + "\x00" + cacheDigest + "\x00" + state.RepositoryLocator))
	return state, nil
}
