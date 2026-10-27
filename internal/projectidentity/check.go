package projectidentity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProjectInspection is diagnostic evidence, never a registration or access grant.
type ProjectInspection struct {
	Status  string          `json:"status"`
	Binding *ProjectBinding `json:"binding,omitempty"`
}

type ProjectBinding struct {
	ID             string  `json:"id"`
	ProjectKey     *string `json:"project_key"`
	IdentityStatus *string `json:"identity_status"`
	Match          string  `json:"match"`
	Removed        bool    `json:"removed,omitempty"`
}

func (inspection ProjectInspection) Validate() error {
	invalid := logicalError("PROJECT_IDENTITY_INVALID")
	switch inspection.Status {
	case "unbound":
		if inspection.Binding != nil {
			return invalid
		}
		return nil
	case "conflict", "resolved", "migration_required":
		if inspection.Status == "conflict" && inspection.Binding == nil {
			return nil
		}
		binding := inspection.Binding
		if binding == nil || binding.ID == "" || len(binding.ID) > 1024 || !utf8.ValidString(binding.ID) || strings.IndexFunc(binding.ID, unicode.IsControl) >= 0 {
			return invalid
		}
		if binding.Match == "legacy_marker" {
			if inspection.Status == "migration_required" && validLegacyMarkerID(binding.ID) && binding.ProjectKey == nil && binding.IdentityStatus == nil && !binding.Removed {
				return nil
			}
			return invalid
		}
		if binding.Match != "manual_alias" && binding.Match != "git_remote" && binding.Match != "portable_marker" && binding.Match != "common_dir_cache" {
			return invalid
		}
		expected := "resolved"
		if binding.Removed || binding.IdentityStatus != nil && *binding.IdentityStatus != "active" {
			expected = "conflict"
		} else if !ValidProjectID(binding.ID) || binding.ProjectKey != nil && *binding.ProjectKey != binding.ID {
			expected = "migration_required"
		}
		if inspection.Status != expected {
			return invalid
		}
		return nil
	default:
		return invalid
	}
}

// CheckLogicalProject deliberately never calls ResolveLogicalProject: even a
// pinned root marker can make that ordinary resolver publish the common cache.
func CheckLogicalProject(ctx context.Context, cwd, serverURL, token string) (ProjectInspection, error) {
	if err := ctx.Err(); err != nil {
		return ProjectInspection{}, err
	}
	if cwd == "" {
		return ProjectInspection{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	selected, err := filepath.Abs(cwd)
	if err != nil {
		return ProjectInspection{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	if info, err := os.Stat(selected); err != nil || !info.IsDir() {
		return ProjectInspection{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	root, common, err := logicalProjectDirectories(ctx, selected)
	if err != nil {
		return ProjectInspection{}, err
	}
	local := func(project Project, match string) ProjectInspection {
		return ProjectInspection{Status: "resolved", Binding: &ProjectBinding{ID: project.ProjectID, Match: match}}
	}
	for candidate := selected; ; candidate = filepath.Dir(candidate) {
		marker, present, err := readLogicalMarker(filepath.Join(candidate, anchorFilenameV3), true)
		if err != nil {
			var historical *LogicalError
			if errors.As(err, &historical) && historical.Code == "PROJECT_MIGRATION_REQUIRED" && historical.legacyID != "" {
				return ProjectInspection{Status: "migration_required", Binding: &ProjectBinding{ID: historical.legacyID, Match: "legacy_marker"}}, nil
			}
			return ProjectInspection{}, err
		}
		if present && marker.ProjectID != "" {
			if common != "" && samePath(candidate, root) {
				cached, cachePresent, err := readLogicalMarker(filepath.Join(common, "engram-project.json"), false)
				if err != nil {
					return ProjectInspection{}, err
				}
				if cachePresent && cached.ProjectID != marker.ProjectID {
					return ProjectInspection{Status: "conflict"}, nil
				}
			}
			return local(marker, "portable_marker"), nil
		}
		if present && !samePath(candidate, root) {
			return ProjectInspection{}, logicalError("PROJECT_CONTEXT_REQUIRED")
		}
		if common != "" && samePath(candidate, root) || filepath.Dir(candidate) == candidate {
			break
		}
	}
	if common == "" {
		return ProjectInspection{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	cached, _, err := readLogicalMarker(filepath.Join(common, "engram-project.json"), false)
	if err != nil {
		return ProjectInspection{}, err
	}
	if cached.ProjectID != "" {
		return local(cached, "common_dir_cache"), nil
	}
	remote, err := anchorGitCommandV3(ctx, root, "remote", "get-url", "origin").Output()
	if err != nil {
		return ProjectInspection{}, logicalError("PROJECT_CONTEXT_REQUIRED")
	}
	locator, err := NormalizeRepositoryLocator(strings.TrimSpace(string(remote)))
	if err != nil {
		return ProjectInspection{}, err
	}
	if serverURL == "" {
		return ProjectInspection{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	endpoint, err := url.Parse(strings.TrimRight(serverURL, "/") + "/api/projects/resolve")
	if err != nil {
		return ProjectInspection{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	query := endpoint.Query()
	query.Set("repository_locator", locator)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return ProjectInspection{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProjectInspection{}, ctxErr
		}
		return ProjectInspection{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ProjectInspection{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	var inspection ProjectInspection
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&inspection) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProjectInspection{}, ctxErr
		}
		return ProjectInspection{}, logicalError("PROJECT_IDENTITY_INVALID")
	}
	if err := ctx.Err(); err != nil {
		return ProjectInspection{}, err
	}
	if inspection.Binding != nil && inspection.Binding.Match == "legacy_marker" {
		return ProjectInspection{}, logicalError("PROJECT_IDENTITY_INVALID")
	}
	if err := inspection.Validate(); err != nil {
		return inspection, err
	}
	if inspection.Status == "resolved" {
		// Ordinary resolution re-reads the cache when pinning after bootstrap.
		current, present, err := readLogicalMarker(filepath.Join(common, "engram-project.json"), false)
		if err != nil {
			return ProjectInspection{}, err
		}
		if present && current.ProjectID != inspection.Binding.ID {
			return ProjectInspection{Status: "conflict"}, nil
		}
	}
	return inspection, nil
}
