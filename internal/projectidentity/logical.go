package projectidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Project is the portable identity DTO. It conveys no access rights.
type Project struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name,omitempty"`
}

type BootstrapRequest struct {
	RepositoryLocator string `json:"repository_locator"`
	Name              string `json:"name,omitempty"`
}

type LogicalError struct {
	Code               string
	legacyID           string
	legacyMarkerPath   string
	legacyMarkerDigest string
	nameMarkerPath     string
	nameMarkerDigest   string
}

func (e *LogicalError) Error() string {
	if guidance := RecoveryGuidance(e.Code); guidance != "" {
		return e.Code + ": " + guidance
	}
	return e.Code
}
func logicalError(code string) error { return &LogicalError{Code: code} }

func ValidProjectID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func ValidateProject(project Project) error {
	if !ValidProjectID(project.ProjectID) || utf8.RuneCountInString(project.Name) > 256 || strings.IndexFunc(project.Name, unicode.IsControl) >= 0 {
		return logicalError("PROJECT_IDENTITY_INVALID")
	}
	return nil
}

// NormalizeRepositoryLocator preserves case-sensitive paths and nondefault ports.
// Only the primary remote is used; an upstream is never a fallback.
func NormalizeRepositoryLocator(raw string) (string, error) {
	invalid := func() (string, error) { return "", logicalError("PROJECT_LOCATOR_INVALID") }
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.IndexFunc(raw, unicode.IsControl) >= 0 || strings.Contains(raw, "\\") {
		return invalid()
	}
	if !strings.Contains(raw, "://") {
		before, after, ok := strings.Cut(raw, ":")
		if !ok || strings.Contains(before, "/") || len(before) == 1 {
			return invalid()
		}
		if _, host, found := strings.Cut(before, "@"); found {
			before = host
		}
		raw = "ssh://" + before + "/" + after
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.Fragment != "" {
		return invalid()
	}
	if u.Scheme != "ssh" && u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "git" {
		return invalid()
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return invalid()
		}
		if (u.Scheme == "ssh" && port == "22") || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") || (u.Scheme == "git" && port == "9418") {
			port = ""
		}
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	path := strings.TrimSuffix(strings.TrimSuffix(u.EscapedPath(), "/"), ".git")
	if path == "" || path == "/" || strings.Contains(path, "//") {
		return invalid()
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
			return invalid()
		}
	}
	return host + path, nil
}

// ValidateRepositoryLocator checks the already-normalized bootstrap DTO.
func ValidateRepositoryLocator(locator string) error {
	u, err := url.Parse("//" + locator)
	if err != nil || len(locator) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ToLower(u.Host) != u.Host || strings.TrimSpace(locator) != locator || strings.IndexFunc(locator, unicode.IsControl) >= 0 || strings.ContainsAny(locator, "\\?#") {
		return logicalError("PROJECT_LOCATOR_INVALID")
	}
	if u.Port() != "" {
		n, err := strconv.Atoi(u.Port())
		if err != nil || n < 1 || n > 65535 {
			return logicalError("PROJECT_LOCATOR_INVALID")
		}
	}
	if u.Path == "" || u.Path == "/" || strings.HasSuffix(u.Path, "/") || strings.HasSuffix(u.Path, ".git") {
		return logicalError("PROJECT_LOCATOR_INVALID")
	}
	for _, part := range strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/") {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
			return logicalError("PROJECT_LOCATOR_INVALID")
		}
	}
	return nil
}

// ToolRequiresProject exempts only recognized project-free operations, not whole tools.
func ToolRequiresProject(name string, arguments []byte) bool {
	switch name {
	case "codebase_context":
		var values struct {
			Action string `json:"action"`
		}
		return json.Unmarshal(arguments, &values) == nil && values.Action == "register"
	case "issues", "vault":
		var values map[string]any
		if err := json.Unmarshal(arguments, &values); err != nil || values == nil {
			return true
		}
		for _, key := range [...]string{"action", "scope", "project", "source_project", "target_project", "source_agent"} {
			if value, present := values[key]; present {
				if _, valid := value.(string); !valid {
					return true
				}
			}
		}
		action, _ := values["action"].(string)
		scope, _ := values["scope"].(string)
		project, _ := values["project"].(string)
		if scope != "" && scope != "global" && scope != "project" {
			return true
		}
		if name == "issues" {
			if _, present := values["action"]; !present {
				action = "list"
			}
			return action != "list" && action != "get"
		}
		switch action {
		case "status":
			return false
		case "list":
			// List uses the project selector even when scope is global.
			return project != ""
		case "store":
			return scope != "global"
		case "get", "delete":
			return scope != "global" && (scope == "project" || project != "")
		default:
			return true
		}
	case "store", "recall", "recall_memory", "feedback", "store_rule", "list_rules", "get_memory_brief", "query_principal_memory", "continuity_slot", "docs", "ingest", "get_state", "set_state", "lifecycle", "list_candidates", "get_candidate", "promote_candidate", "reject_candidate", "supersede_candidate", "bulk_promote", "bulk_delete", "bulk_supersede", "rollback_snapshot", "list_snapshots", "pin_snapshot":
		return true
	default:
		return false
	}
}

// ResolveLogicalProject reads the actual caller root, never the daemon cwd.
// bootstrap is consulted only before an ID is pinned; nil supports offline use.
func ResolveLogicalProject(ctx context.Context, cwd string, bootstrap func(context.Context, BootstrapRequest) (Project, error)) (Project, error) {
	if cwd == "" {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	selected, err := filepath.Abs(cwd)
	if err != nil {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	info, err := os.Stat(selected)
	if err != nil || !info.IsDir() {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	root, common, err := logicalProjectDirectories(ctx, selected)
	if err != nil {
		return Project{}, err
	}
	// A nested portable marker is an explicit boundary. A subdirectory alone is not.
	nameOnly := false
	var nameMarkerPath, nameMarkerDigest string
	for candidate := selected; ; candidate = filepath.Dir(candidate) {
		marker, present, err := readLogicalMarker(filepath.Join(candidate, anchorFilenameV3), true)
		if err != nil {
			return Project{}, err
		}
		if present && marker.ProjectID != "" {
			if common != "" && samePath(candidate, root) {
				cached, present, err := readLogicalMarker(filepath.Join(common, "engram-project.json"), false)
				if err != nil {
					return Project{}, err
				}
				if present && cached.ProjectID != marker.ProjectID {
					return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
				}
			}
			return marker, nil
		}
		if present {
			if common == "" || !samePath(candidate, root) {
				return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
			}
			nameMarkerPath = filepath.Join(candidate, anchorFilenameV3)
			raw, exists, readErr := readRecoveryMarker(nameMarkerPath)
			if readErr != nil || !exists {
				return Project{}, logicalError("PROJECT_SELECTION_STALE")
			}
			again, _, parseErr := parseLogicalMarker(raw, true)
			if parseErr != nil || again != marker {
				return Project{}, logicalError("PROJECT_SELECTION_STALE")
			}
			nameMarkerDigest = markerEvidenceDigest(raw)
			nameOnly = true
			break
		}
		if common != "" && samePath(candidate, root) || filepath.Dir(candidate) == candidate {
			break
		}
	}
	if common == "" {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	cachePath := filepath.Join(common, "engram-project.json")
	cached, present, err := readLogicalMarker(cachePath, false)
	if err != nil {
		return Project{}, err
	}
	if present && cached.ProjectID != "" {
		return cached, nil
	}
	if nameOnly {
		return Project{}, &LogicalError{Code: "PROJECT_CONTEXT_REQUIRED", nameMarkerPath: nameMarkerPath, nameMarkerDigest: nameMarkerDigest}
	}
	if bootstrap == nil {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	remote, err := anchorGitCommandV3(ctx, root, "remote", "get-url", "origin").Output()
	if err != nil {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	locator, err := NormalizeRepositoryLocator(strings.TrimSpace(string(remote)))
	if err != nil {
		return Project{}, err
	}
	legacy, _, err := readLogicalMarker(filepath.Join(root, anchorFilenameV3), true)
	if err != nil {
		return Project{}, err
	}
	name := legacy.Name
	if name == "" {
		name = filepath.Base(root)
	}
	project, err := bootstrap(ctx, BootstrapRequest{RepositoryLocator: locator, Name: name})
	if err != nil {
		return Project{}, err
	}
	if err := ValidateProject(project); err != nil {
		return Project{}, err
	}
	project, err = pinLogicalCache(common, project)
	if err != nil {
		return Project{}, err
	}
	if err := publishLogicalMarker(filepath.Join(root, anchorFilenameV3), project); err != nil {
		return Project{}, err
	}
	return project, nil
}

// ResolveLogicalProjectForBackend preserves ordinary resolution and recovers a
// name-only root using only its authenticated primary-origin binding.
func ResolveLogicalProjectForBackend(ctx context.Context, cwd, serverURL, token string) (Project, error) {
	project, err := ResolveLogicalProject(ctx, cwd, HTTPBootstrap(serverURL, token))
	if ProjectErrorCode(err) != "PROJECT_CONTEXT_REQUIRED" || serverURL == "" {
		return project, err
	}
	return resolveNameOnlyOrigin(ctx, cwd, serverURL, token, err)
}

func resolveNameOnlyOrigin(ctx context.Context, cwd, serverURL, token string, originalErr error) (Project, error) {
	var initial *LogicalError
	if !errors.As(originalErr, &initial) || initial.nameMarkerDigest == "" {
		return Project{}, originalErr
	}
	state, err := InspectProjectMarker(ctx, cwd)
	if err != nil {
		return Project{}, err
	}
	if !state.Present || state.LegacyID != "" || state.PinnedID != "" || state.CacheID != "" || state.RepositoryLocator == "" {
		return Project{}, originalErr
	}
	selected, err := filepath.Abs(cwd)
	if err != nil {
		return Project{}, originalErr
	}
	gitRoot, common, err := logicalProjectDirectories(ctx, selected)
	if err != nil {
		return Project{}, err
	}
	markerPath := filepath.Clean(filepath.Join(selected, filepath.FromSlash(state.Target)))
	if markerPath != filepath.Clean(initial.nameMarkerPath) || state.MarkerDigest != initial.nameMarkerDigest {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	if common == "" || !samePath(filepath.Dir(markerPath), gitRoot) {
		return Project{}, originalErr
	}
	callerInfo, err := os.Stat(selected)
	if err != nil || !callerInfo.IsDir() {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	commonInfo, err := os.Stat(common)
	if err != nil || !commonInfo.IsDir() {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	raw, present, err := readRecoveryMarker(markerPath)
	if err != nil || !present || markerEvidenceDigest(raw) != state.MarkerDigest {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	marker, _, err := parseLogicalMarker(raw, true)
	if err != nil || marker.ProjectID != "" {
		return Project{}, originalErr
	}
	cachePath := filepath.Join(common, "engram-project.json")
	if _, exists, err := readRecoveryMarker(cachePath); err != nil {
		return Project{}, err
	} else if exists {
		return Project{}, originalErr
	}
	revalidate := func() error {
		current, err := InspectProjectMarker(ctx, cwd)
		if err != nil {
			return err
		}
		caller, statErr := os.Stat(selected)
		root, currentCommon, gitErr := logicalProjectDirectories(ctx, selected)
		commonNow, commonErr := os.Stat(common)
		if current != state || statErr != nil || !os.SameFile(callerInfo, caller) || gitErr != nil || root != gitRoot || currentCommon != common || commonErr != nil || !os.SameFile(commonInfo, commonNow) {
			return logicalError("PROJECT_SELECTION_STALE")
		}
		return nil
	}
	inspection, err := resolveRepositoryInspection(ctx, serverURL, token, state.RepositoryLocator)
	if err != nil {
		return Project{}, err
	}
	if err := revalidate(); err != nil {
		return Project{}, err
	}
	active := func(value ProjectInspection) (Project, bool) {
		b := value.Binding
		if value.Status != "resolved" || b == nil || (b.Match != "manual_alias" && b.Match != "git_remote") || b.ProjectKey == nil || *b.ProjectKey != b.ID || b.IdentityStatus == nil || *b.IdentityStatus != "active" {
			return Project{}, false
		}
		return Project{ProjectID: b.ID}, true
	}
	if project, ok := active(inspection); ok {
		return project, nil
	}
	if inspection.Status == "resolved" {
		return Project{}, originalErr
	}
	if inspection.Status != "unbound" {
		return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
	}
	v2Path := filepath.Join(gitRoot, ".engram-project-v2.json")
	if _, exists, err := readRecoveryMarker(v2Path); err != nil {
		return Project{}, err
	} else if exists {
		return Project{}, originalErr
	}
	if err := revalidate(); err != nil {
		return Project{}, err
	}
	created, bootstrapErr := HTTPBootstrap(serverURL, token)(ctx, BootstrapRequest{RepositoryLocator: state.RepositoryLocator, Name: marker.Name})
	if bootstrapErr != nil && ProjectErrorCode(bootstrapErr) != "PROJECT_RESOLUTION_UNAVAILABLE" {
		return Project{}, bootstrapErr
	}
	if err := revalidate(); err != nil {
		return Project{}, err
	}
	if _, exists, err := readRecoveryMarker(v2Path); err != nil {
		return Project{}, err
	} else if exists {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	confirmed, err := resolveRepositoryInspection(ctx, serverURL, token, state.RepositoryLocator)
	if err != nil {
		return Project{}, err
	}
	project, ok := active(confirmed)
	if !ok {
		if bootstrapErr != nil {
			return Project{}, bootstrapErr
		}
		return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
	}
	if bootstrapErr == nil && project.ProjectID != created.ProjectID {
		return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
	}
	if err := revalidate(); err != nil {
		return Project{}, err
	}
	return project, nil
}

// ResolveAutomaticHistoricalProject only reads a verified historical mapping.
// It never changes identity files or forwards the caller's operation.
func ResolveAutomaticHistoricalProject(ctx context.Context, cwd, serverURL, token string, initialErr error) (Project, error) {
	var initial *LogicalError
	if !errors.As(initialErr, &initial) || initial.Code != "PROJECT_MIGRATION_REQUIRED" {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	if initial.legacyID == "" || initial.legacyMarkerDigest == "" {
		return Project{}, initialErr
	}
	endpoint, err := url.Parse(serverURL)
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	before, err := InspectProjectMarker(ctx, cwd)
	if err != nil {
		return Project{}, err
	}
	if before.LegacyID != initial.legacyID || before.MarkerDigest != initial.legacyMarkerDigest {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	selected, err := filepath.Abs(cwd)
	if err != nil {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	if filepath.Clean(filepath.Join(selected, filepath.FromSlash(before.Target))) != filepath.Clean(initial.legacyMarkerPath) {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	callerInfo, err := os.Stat(selected)
	if err != nil || !callerInfo.IsDir() {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	gitRoot, common, err := logicalProjectDirectories(ctx, selected)
	if err != nil {
		return Project{}, err
	}
	var commonInfo os.FileInfo
	if common != "" {
		commonInfo, err = os.Stat(common)
		if err != nil || !commonInfo.IsDir() {
			return Project{}, logicalError("PROJECT_SELECTION_STALE")
		}
	}
	project, resolved, err := ResolveHistoricalProject(ctx, serverURL, token, before.LegacyID)
	if err != nil {
		return Project{}, err
	}
	if !resolved {
		return Project{}, logicalError("PROJECT_MIGRATION_REQUIRED")
	}
	after, err := InspectProjectMarker(ctx, cwd)
	if err != nil {
		return Project{}, err
	}
	currentCaller, statErr := os.Stat(selected)
	currentRoot, currentCommon, gitErr := logicalProjectDirectories(ctx, selected)
	if statErr != nil || !os.SameFile(callerInfo, currentCaller) || gitErr != nil || gitRoot != currentRoot || common != currentCommon {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	if common != "" {
		currentInfo, err := os.Stat(common)
		if err != nil || !os.SameFile(commonInfo, currentInfo) {
			return Project{}, logicalError("PROJECT_SELECTION_STALE")
		}
	}
	if before != after {
		return Project{}, logicalError("PROJECT_SELECTION_STALE")
	}
	if after.CacheID != "" && after.CacheID != project.ProjectID {
		return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
	}
	return project, nil
}

func logicalProjectDirectories(ctx context.Context, selected string) (string, string, error) {
	root, common := selected, ""
	output, gitErr := anchorGitCommandV3(ctx, selected, "rev-parse", "--path-format=absolute", "--git-common-dir", "--is-bare-repository").Output()
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if gitErr == nil {
		parts := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(parts) != 2 {
			return "", "", logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		common = strings.TrimSpace(parts[0])
		if strings.TrimSpace(parts[1]) == "true" {
			root = common
		} else {
			output, err := anchorGitCommandV3(ctx, selected, "rev-parse", "--show-toplevel").Output()
			if err != nil {
				return "", "", logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
			}
			root = strings.TrimSpace(string(output))
		}
	}
	return root, common, nil
}

func readLogicalMarker(path string, allowLegacyMarker bool) (Project, bool, error) {
	raw, present, err := readRecoveryMarker(path)
	if err != nil || !present {
		return Project{}, present, err
	}
	project, markerPresent, parseErr := parseLogicalMarker(raw, allowLegacyMarker)
	var legacy *LogicalError
	if errors.As(parseErr, &legacy) && legacy.Code == "PROJECT_MIGRATION_REQUIRED" {
		legacy.legacyMarkerPath = path
		legacy.legacyMarkerDigest = markerEvidenceDigest(raw)
	}
	return project, markerPresent, parseErr
}

func parseLogicalMarker(raw []byte, allowLegacyMarker bool) (Project, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	fields := make(map[string]json.RawMessage)
	// Old name-only and V3 markers are transition inputs, not alternate algorithms.
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok {
			return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
		}
		if _, duplicate := fields[field]; duplicate {
			return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
		}
		if field != "project_id" && field != "name" && field != "version" && field != "scope" && !(allowLegacyMarker && field == "id") {
			return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
		}
		fields[field] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	var project Project
	if json.Unmarshal(raw, &project) != nil {
		return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	if utf8.RuneCountInString(project.Name) > 256 || strings.IndexFunc(project.Name, unicode.IsControl) >= 0 {
		return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	if fields["id"] != nil {
		var id string
		name := fields["name"]
		if len(fields) != 2 || len(name) == 0 || name[0] != '"' || !utf8.Valid(raw) || json.Unmarshal(fields["id"], &id) != nil || !validLegacyMarkerID(id) {
			return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
		}
		return Project{}, true, &LogicalError{Code: "PROJECT_MIGRATION_REQUIRED", legacyID: id}
	}
	if fields["version"] != nil || fields["scope"] != nil {
		if _, err := ParseAnchorV3(raw); err != nil {
			return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
		}
	}
	if project.ProjectID == "" {
		if len(fields) != 1 || fields["name"] == nil {
			return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
		}
		return project, true, nil
	}
	return project, true, ValidateProject(project)
}

func validLegacyMarkerID(id string) bool {
	if len(id) != 6 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func pinLogicalCache(common string, project Project) (Project, error) {
	path := filepath.Join(common, "engram-project.json")
	cached, present, err := readLogicalMarker(path, false)
	if err != nil {
		return Project{}, err
	}
	if present {
		if cached.ProjectID != project.ProjectID {
			return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
		}
		return project, nil
	}
	if err := publishLogicalMarker(path, project); err != nil {
		return Project{}, err
	}
	return project, nil
}

// Publish complete JSON under a local writer lock; never replace a pinned ID.
func publishLogicalMarker(path string, project Project) error {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	lock.Close()
	defer os.Remove(path + ".lock")
	current, present, err := readLogicalMarker(path, filepath.Base(path) == anchorFilenameV3)
	if err != nil {
		return err
	}
	if present {
		if current.ProjectID == project.ProjectID {
			return nil
		}
		if current.ProjectID != "" {
			return logicalError("PROJECT_IDENTITY_CONFLICT")
		}
	}
	raw, err := json.Marshal(project)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".engram-project-*")
	if err != nil {
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err = file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if present && current.ProjectID == "" {
		// Only the known name-only transition may replace existing JSON.
		if err := os.Rename(temp, path); err != nil {
			return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		return nil
	}
	if err = os.Link(temp, path); err == nil {
		return nil
	}
	current, present, readErr := readLogicalMarker(path, filepath.Base(path) == anchorFilenameV3)
	if readErr == nil && present && current.ProjectID == project.ProjectID {
		return nil
	}
	if present {
		return logicalError("PROJECT_IDENTITY_CONFLICT")
	}
	return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
}

// InitLogicalProject is an explicit new-project action, including non-Git roots.
func InitLogicalProject(ctx context.Context, root, name string) (Project, error) {
	project, err := ResolveLogicalProject(ctx, root, nil)
	if err == nil {
		return project, nil
	}
	var typed *LogicalError
	if !errors.As(err, &typed) || typed.Code != "PROJECT_CONTEXT_REQUIRED" {
		return Project{}, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	legacy, _, err := readLogicalMarker(filepath.Join(root, anchorFilenameV3), true)
	if err != nil {
		return Project{}, err
	}
	if name == "" {
		name = legacy.Name
	}
	project = Project{ProjectID: uuid.NewString(), Name: name}
	if err := ValidateProject(project); err != nil {
		return Project{}, err
	}
	gitRoot, common, err := logicalProjectDirectories(ctx, root)
	if err != nil {
		return Project{}, err
	}
	if common != "" && samePath(root, gitRoot) {
		// All writers pin the common cache before publishing its root marker.
		if _, err := pinLogicalCache(common, project); err != nil {
			return Project{}, err
		}
	}
	if err := publishLogicalMarker(filepath.Join(root, anchorFilenameV3), project); err != nil {
		return Project{}, err
	}
	return ResolveLogicalProject(ctx, root, nil)
}

func HTTPBootstrap(serverURL, token string) func(context.Context, BootstrapRequest) (Project, error) {
	return func(ctx context.Context, input BootstrapRequest) (Project, error) {
		if serverURL == "" {
			return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return Project{}, err
		}
		endpoint, err := url.Parse(strings.TrimRight(serverURL, "/") + "/api/projects/bootstrap")
		if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		response, release, err := projectRecoveryRequest(ctx, endpoint, token, http.MethodPost, bytes.NewReader(raw))
		if err != nil {
			return Project{}, err
		}
		defer release()
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
				return Project{}, projectRecoveryStatusError(response.StatusCode)
			}
			var body struct {
				Code string `json:"code"`
			}
			if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body) == nil {
				switch body.Code {
				case "PROJECT_IDENTITY_CONFLICT", "PROJECT_MIGRATION_REQUIRED", "PROJECT_LOCATOR_INVALID":
					return Project{}, logicalError(body.Code)
				}
			}
			return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
		if err != nil || len(body) > 4096 || !utf8.Valid(body) {
			return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		check := json.NewDecoder(bytes.NewReader(body))
		if !skipRecoveryCatalogValue(check, 0, true) {
			return Project{}, logicalError("PROJECT_IDENTITY_INVALID")
		}
		if _, err := check.Token(); err != io.EOF {
			return Project{}, logicalError("PROJECT_IDENTITY_INVALID")
		}
		var project Project
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&project); err != nil {
			return Project{}, logicalError("PROJECT_IDENTITY_INVALID")
		}
		if err := ValidateProject(project); err != nil {
			return Project{}, err
		}
		return project, nil
	}
}

func ProjectErrorCode(err error) string {
	var typed *LogicalError
	if errors.As(err, &typed) {
		return typed.Code
	}
	return "PROJECT_RESOLUTION_UNAVAILABLE"
}
