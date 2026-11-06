package projectidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const projectRecoverySeedFilename = "project-recovery-seeds-v1.json"

type approvedProjectSeed struct {
	RootSHA256     string `json:"root_sha256"`
	ServerSHA256   string `json:"server_sha256"`
	MarkerSHA256   string `json:"marker_sha256"`
	V2SHA256       string `json:"v2_sha256"`
	ProjectID      string `json:"project_id"`
	ApprovalSHA256 string `json:"approval_sha256"`
}

func validRecoveryDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// rejectDuplicateSeedJSON rejects duplicate object keys before typed decoding.
func rejectDuplicateSeedJSON(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("seed nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			name = strings.ToLower(name)
			if err != nil || !ok || seen[name] {
				return errors.New("duplicate seed key")
			}
			seen[name] = true
			if err := rejectDuplicateSeedJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return errors.New("invalid seed object")
		}
	case json.Delim('['):
		for decoder.More() {
			if err := rejectDuplicateSeedJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return errors.New("invalid seed array")
		}
	}
	return nil
}

// ResolveApprovedProjectSeed reads the exact existing private module-data owner.
// It does not publish files, grant authority, or modify historical membership.
// The publisher's approval provenance and actual OS ownership/ACL are a host
// delivery prerequisite; the client still validates every binding and catalog.
func ResolveApprovedProjectSeed(ctx context.Context, storageDir, serverURL, cwd string, state ProjectMarkerState) (Project, bool, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, false, err
	}
	if storageDir == "" {
		return Project{}, false, nil
	}
	if !filepath.IsAbs(storageDir) {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	storageDir = filepath.Clean(storageDir)
	info, err := os.Lstat(storageDir)
	if errors.Is(err, os.ErrNotExist) {
		return Project{}, false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	resolved, err := filepath.EvalSymlinks(storageDir)
	if err != nil {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	same := filepath.Clean(resolved) == storageDir
	if runtime.GOOS == "windows" {
		same = strings.EqualFold(filepath.Clean(resolved), storageDir)
	}
	if !same {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	path := filepath.Join(storageDir, projectRecoverySeedFilename)
	raw, present, err := readBoundedRecoveryFile(path, 65536)
	if err != nil || !present {
		return Project{}, false, err
	}
	check := json.NewDecoder(bytes.NewReader(raw))
	if rejectDuplicateSeedJSON(check, 0) != nil || check.Decode(&struct{}{}) != io.EOF {
		return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
	}
	var file struct {
		Version int                   `json:"version"`
		Entries []approvedProjectSeed `json:"entries"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&file) != nil || decoder.Decode(&struct{}{}) != io.EOF || file.Version != 1 || file.Entries == nil || len(file.Entries) > 256 {
		return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
	}
	seen := make(map[string]bool, len(file.Entries))
	var matched *approvedProjectSeed
	for i := range file.Entries {
		entry := &file.Entries[i]
		if !validRecoveryDigest(entry.RootSHA256) || !validRecoveryDigest(entry.ServerSHA256) || !validRecoveryDigest(entry.MarkerSHA256) || !validRecoveryDigest(entry.V2SHA256) || !validRecoveryDigest(entry.ApprovalSHA256) || !ValidProjectID(entry.ProjectID) || seen[entry.RootSHA256] {
			return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
		}
		seen[entry.RootSHA256] = true
		if entry.RootSHA256 == state.TargetRootDigest {
			matched = entry
		}
	}
	if matched == nil {
		return Project{}, false, nil
	}
	if matched.ServerSHA256 != markerEvidenceDigest([]byte(serverURL)) || !state.Present || matched.MarkerSHA256 != state.MarkerDigest {
		return Project{}, false, logicalError("PROJECT_SELECTION_STALE")
	}
	target, err := filepath.Abs(filepath.Join(cwd, filepath.FromSlash(state.Target)))
	if err != nil || markerEvidenceDigest([]byte(filepath.Dir(target))) != state.TargetRootDigest || filepath.Base(target) != anchorFilenameV3 {
		return Project{}, false, logicalError("PROJECT_SELECTION_STALE")
	}
	anchor, anchorPresent, err := readRecoveryMarker(filepath.Join(filepath.Dir(target), ".engram-project-v2.json"))
	if err != nil {
		return Project{}, false, err
	}
	if !anchorPresent || matched.V2SHA256 != markerEvidenceDigest(anchor) {
		return Project{}, false, logicalError("PROJECT_SELECTION_STALE")
	}
	current, stillPresent, err := readBoundedRecoveryFile(path, 65536)
	if err != nil || !stillPresent || !bytes.Equal(current, raw) {
		return Project{}, false, logicalError("PROJECT_SELECTION_STALE")
	}
	return Project{ProjectID: matched.ProjectID}, true, nil
}

// RecoveryV2Precondition returns caller-relative evidence the host must check
// when applying an action derived from an approved local seed.
func RecoveryV2Precondition(cwd string, state ProjectMarkerState) (string, string, error) {
	target, err := filepath.Abs(filepath.Join(cwd, filepath.FromSlash(state.Target)))
	if err != nil || filepath.Base(target) != anchorFilenameV3 || markerEvidenceDigest([]byte(filepath.Dir(target))) != state.TargetRootDigest {
		return "", "", logicalError("PROJECT_SELECTION_STALE")
	}
	path := filepath.Join(filepath.Dir(target), ".engram-project-v2.json")
	raw, present, err := readRecoveryMarker(path)
	if err != nil {
		return "", "", err
	}
	if !present {
		return "", "", logicalError("PROJECT_SELECTION_STALE")
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil || filepath.IsAbs(relative) {
		return "", "", logicalError("PROJECT_SELECTION_STALE")
	}
	return filepath.ToSlash(relative), markerEvidenceDigest(raw), nil
}
