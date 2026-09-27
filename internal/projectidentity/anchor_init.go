package projectidentity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// InitRepositoryAnchorV3 creates an anchor only at the selected Git root, or
// returns an existing valid repository anchor without changing it.
func InitRepositoryAnchorV3(root, name string) (anchor AnchorV3, created, tracked bool, err error) {
	selectedRoot, err := filepath.Abs(root)
	if err != nil || !isSelectedGitRoot(selectedRoot) {
		return AnchorV3{}, false, false, fmt.Errorf("project init requires the current Git root")
	}
	path := filepath.Join(selectedRoot, anchorFilenameV3)
	info, statErr := os.Lstat(path)
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return AnchorV3{}, false, false, fmt.Errorf("%s already exists and is not a regular file; refusing to overwrite", path)
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return AnchorV3{}, false, false, fmt.Errorf("read existing %s: %w", path, readErr)
		}
		anchor, parseErr := ParseAnchorV3(raw)
		if parseErr != nil || anchor.Scope != "repository" {
			return AnchorV3{}, false, false, fmt.Errorf("%s already exists but is not a valid repository V3 anchor; refusing to overwrite", path)
		}
		_, discoveredErr := DiscoverAnchorV3(selectedRoot, "repository")
		return anchor, false, discoveredErr == nil, nil
	}
	if !os.IsNotExist(statErr) {
		return AnchorV3{}, false, false, fmt.Errorf("inspect %s: %w", path, statErr)
	}
	if isTrackedAnchor(selectedRoot) {
		return AnchorV3{}, false, false, fmt.Errorf("%s is tracked but missing; refusing to replace it", path)
	}
	anchor = AnchorV3{Version: 3, ProjectID: uuid.NewString(), Name: name, Scope: "repository"}
	raw, err := json.Marshal(anchor)
	if err != nil {
		return AnchorV3{}, false, false, err
	}
	if _, err := ParseAnchorV3(raw); err != nil {
		return AnchorV3{}, false, false, fmt.Errorf("invalid project name (must be 1–256 Unicode characters): %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return AnchorV3{}, false, false, fmt.Errorf("create %s exclusively: %w", path, err)
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return AnchorV3{}, false, false, fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return AnchorV3{}, false, false, fmt.Errorf("close %s: %w", path, err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		return AnchorV3{}, false, false, fmt.Errorf("read created %s: %w", path, err)
	}
	anchor, err = ParseAnchorV3(persisted)
	if err != nil || anchor.Scope != "repository" {
		return AnchorV3{}, false, false, fmt.Errorf("created %s failed V3 validation", path)
	}
	return anchor, true, false, nil
}
