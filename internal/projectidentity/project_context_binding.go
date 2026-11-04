package projectidentity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const projectContextBindingBytes = 4096

// This is local caller selection evidence, not a portable project marker, alias,
// data migration or permission. Only fingerprints of private configuration persist.
type projectContextBinding struct {
	Version      int    `json:"version"`
	ProjectID    string `json:"project_id"`
	ScopeDigest  string `json:"scope_digest"`
	MarkerDigest string `json:"marker_digest"`
	SelectionRef string `json:"selection_ref"`
}

type projectContextSnapshot struct {
	root          string
	bindingPath   string
	scopeDigest   string
	markerDigest  string
	bindingDigest string
	pinnedID      string
	legacyID      string
	binding       *projectContextBinding
}

func (snapshot projectContextSnapshot) stateDigest() string {
	return projectContextDigest(snapshot.scopeDigest, snapshot.markerDigest, snapshot.bindingDigest)
}

func projectContextPath(caller ProjectContextCaller) (string, error) {
	if caller.Cwd == "" {
		return "", logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	root, err := filepath.Abs(caller.Cwd)
	if err != nil {
		return "", logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	return filepath.Join(root, ".engram-context."+projectContextScopeDigest(caller, root)+".json"), nil
}

func projectContextScopeDigest(caller ProjectContextCaller, root string) string {
	serverReference := caller.ServerURL
	if endpoint, err := projectContextEndpoint(caller.ServerURL); err == nil {
		serverReference = endpoint.String()
	}
	// An absent-sidecar check must not introduce an HTTP/config dependency into
	// ordinary canonical-marker resolution. Inspection validates the endpoint.
	return projectContextDigest("project-context-v1", caller.Cwd, root, caller.CallerID,
		serverReference, projectContextDigest(caller.Token))
}

func inspectProjectContext(ctx context.Context, caller ProjectContextCaller) (projectContextSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return projectContextSnapshot{}, err
	}
	bindingPath, err := projectContextPath(caller)
	if err != nil {
		return projectContextSnapshot{}, err
	}
	if _, err := projectContextEndpoint(caller.ServerURL); err != nil {
		return projectContextSnapshot{}, err
	}
	selected := filepath.Dir(bindingPath)
	snapshot := projectContextSnapshot{
		root: selected, bindingPath: bindingPath,
		scopeDigest: projectContextScopeDigest(caller, selected),
	}
	root, common, err := logicalProjectDirectories(ctx, selected)
	if err != nil {
		return projectContextSnapshot{}, err
	}
	useCommonCache := common != ""
	// Use the ordinary resolver's nearest-marker and Git-root boundaries. Even
	// an absent marker contributes evidence, so a later new boundary is stale.
	for candidate := selected; ; candidate = filepath.Dir(candidate) {
		if err := ctx.Err(); err != nil {
			return projectContextSnapshot{}, err
		}
		path := filepath.Join(candidate, anchorFilenameV3)
		raw, present, err := readProjectContextFile(path, projectContextBindingBytes)
		if err != nil {
			return projectContextSnapshot{}, err
		}
		snapshot.markerDigest = projectContextDigest(snapshot.markerDigest, path, projectContextFileDigest(raw, present))
		if present {
			project, _, err := parseLogicalMarker(raw, true)
			if err != nil {
				var historical *LogicalError
				if !errors.As(err, &historical) || historical.Code != "PROJECT_MIGRATION_REQUIRED" || !validLegacyMarkerID(historical.legacyID) {
					return projectContextSnapshot{}, err
				}
				snapshot.legacyID = historical.legacyID
				useCommonCache = common != "" && samePath(candidate, root)
				break
			}
			if project.ProjectID != "" {
				snapshot.pinnedID = project.ProjectID
				useCommonCache = common != "" && samePath(candidate, root)
				break
			}
		}
		if common != "" && samePath(candidate, root) || filepath.Dir(candidate) == candidate {
			break
		}
	}
	if useCommonCache {
		path := filepath.Join(common, "engram-project.json")
		raw, present, err := readProjectContextFile(path, projectContextBindingBytes)
		if err != nil {
			return projectContextSnapshot{}, err
		}
		snapshot.markerDigest = projectContextDigest(snapshot.markerDigest, path, projectContextFileDigest(raw, present))
		if present {
			cached, _, err := parseLogicalMarker(raw, false)
			if err != nil {
				return projectContextSnapshot{}, err
			}
			if cached.ProjectID == "" || snapshot.pinnedID != "" && snapshot.pinnedID != cached.ProjectID {
				return projectContextSnapshot{}, logicalError("PROJECT_IDENTITY_CONFLICT")
			}
			snapshot.pinnedID = cached.ProjectID
		}
	}
	raw, present, err := readProjectContextFile(bindingPath, projectContextBindingBytes)
	if err != nil {
		return projectContextSnapshot{}, err
	}
	snapshot.bindingDigest = projectContextFileDigest(raw, present)
	if !present {
		return snapshot, nil
	}
	var binding projectContextBinding
	if decodeProjectContextJSON(raw, &binding) != nil || binding.Version != 1 || !ValidProjectID(binding.ProjectID) || !validProjectContextDigest(binding.ScopeDigest) || !validProjectContextDigest(binding.MarkerDigest) || !validProjectSelectionReference(binding.SelectionRef) {
		return projectContextSnapshot{}, logicalError("PROJECT_IDENTITY_INVALID")
	}
	if binding.ScopeDigest != snapshot.scopeDigest ||
		snapshot.pinnedID != "" && snapshot.pinnedID != binding.ProjectID ||
		binding.MarkerDigest != snapshot.markerDigest && (snapshot.pinnedID == "" || snapshot.legacyID != "") {
		return projectContextSnapshot{}, logicalError("PROJECT_IDENTITY_CONFLICT")
	}
	snapshot.binding = &binding
	return snapshot, nil
}

func projectContextFileDigest(raw []byte, present bool) string {
	if !present {
		return projectContextDigest("absent")
	}
	return projectContextDigest("present", string(raw))
}

// Bounded regular-file reads reject symlinks and a replacement observed during
// the read. Errors stay typed, without leaking a raw path or credential.
func readProjectContextFile(path string, limit int64) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, true, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || int64(len(raw)) > limit {
		return nil, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	return raw, true, nil
}

func publishProjectContext(ctx context.Context, caller ProjectContextCaller, before projectContextSnapshot, projectID, selectionRef string) error {
	// The destination belongs to this caller scope; other caller scopes are independent.
	path := before.bindingPath
	current, err := inspectProjectContext(ctx, caller)
	if err != nil {
		return err
	}
	if before.stateDigest() != current.stateDigest() {
		return logicalError("PROJECT_SELECTION_STALE")
	}
	if current.binding != nil {
		if before.binding != nil && *before.binding == *current.binding && current.binding.ProjectID == projectID {
			return nil
		}
		return logicalError("PROJECT_IDENTITY_CONFLICT")
	}
	binding := projectContextBinding{
		Version: 1, ProjectID: projectID, ScopeDigest: current.scopeDigest,
		MarkerDigest: current.markerDigest, SelectionRef: selectionRef,
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	file, err := os.CreateTemp(before.root, ".engram-context-*")
	if err != nil {
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	if err := file.Close(); err != nil {
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	current, err = inspectProjectContext(ctx, caller)
	if err != nil {
		return err
	}
	if before.stateDigest() != current.stateDigest() {
		return logicalError("PROJECT_SELECTION_STALE")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Link is an atomic absent->bound CAS. Never rename over an existing file,
	// so another writer's bytes (including malformed records) remain preserved.
	if err := os.Link(temporary, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return logicalError("PROJECT_IDENTITY_CONFLICT")
		}
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	return nil
}
