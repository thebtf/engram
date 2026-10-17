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

type LogicalError struct{ Code string }

func (e *LogicalError) Error() string { return e.Code }
func logicalError(code string) error  { return &LogicalError{Code: code} }

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

func ToolRequiresProject(name string) bool {
	switch name {
	case "store", "recall", "recall_memory", "feedback", "store_rule", "list_rules", "get_memory_brief", "query_principal_memory", "continuity_slot", "issues", "docs", "vault", "ingest", "get_state", "set_state", "lifecycle", "list_candidates", "get_candidate", "promote_candidate", "reject_candidate", "supersede_candidate", "bulk_promote", "bulk_delete", "bulk_supersede", "rollback_snapshot", "list_snapshots", "pin_snapshot":
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
	for candidate := selected; ; candidate = filepath.Dir(candidate) {
		marker, present, err := readLogicalMarker(filepath.Join(candidate, anchorFilenameV3))
		if err != nil {
			return Project{}, err
		}
		if present && marker.ProjectID != "" {
			if common != "" && samePath(candidate, root) {
				return pinLogicalCache(common, marker)
			}
			return marker, nil
		}
		if common != "" && samePath(candidate, root) || filepath.Dir(candidate) == candidate {
			break
		}
	}
	if common == "" {
		return Project{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	cachePath := filepath.Join(common, "engram-project.json")
	cached, present, err := readLogicalMarker(cachePath)
	if err != nil {
		return Project{}, err
	}
	if present && cached.ProjectID != "" {
		return cached, nil
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
	legacy, _, err := readLogicalMarker(filepath.Join(root, anchorFilenameV3))
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

func readLogicalMarker(path string) (Project, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Project{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return Project{}, true, logicalError("PROJECT_IDENTITY_INVALID")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Project{}, true, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
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
		if field != "project_id" && field != "name" && field != "version" && field != "scope" {
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

func pinLogicalCache(common string, project Project) (Project, error) {
	path := filepath.Join(common, "engram-project.json")
	cached, present, err := readLogicalMarker(path)
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
	current, present, err := readLogicalMarker(path)
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
	current, present, readErr := readLogicalMarker(path)
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
	legacy, _, err := readLogicalMarker(filepath.Join(root, anchorFilenameV3))
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
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/api/projects/bootstrap", bytes.NewReader(raw))
		if err != nil {
			return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
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
		var project Project
		decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
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
