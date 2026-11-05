package projectidentity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// ExistingProjectChoice is a current server-authorized catalog entry, not a
// claim that historical membership has been resolved or reassigned.
type ExistingProjectChoice struct {
	ProjectID           string `json:"project_id"`
	Name                string `json:"name"`
	RepositoryQualifier string `json:"repository_qualifier,omitempty"`
}

// ReadExistingProjectChoices fetches the current catalog using the backend's authorization policy.
func ReadExistingProjectChoices(ctx context.Context, serverURL, token string) ([]ExistingProjectChoice, error) {
	endpoint, err := url.Parse(strings.TrimRight(serverURL, "/") + "/api/projects")
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, release, err := projectRecoveryGET(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	defer release()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	var result struct {
		Items []ExistingProjectChoice `json:"items"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Items == nil || len(result.Items) > 4096 {
		return nil, logicalError("PROJECT_IDENTITY_INVALID")
	}
	seen := make(map[string]bool, len(result.Items))
	for _, item := range result.Items {
		if seen[item.ProjectID] || ValidateProject(Project{ProjectID: item.ProjectID, Name: item.Name}) != nil || item.Name == "" || (token != "" && (strings.Contains(item.Name, token) || strings.Contains(item.RepositoryQualifier, token))) || len(item.RepositoryQualifier) > 2048 || strings.IndexFunc(item.RepositoryQualifier, unicode.IsControl) >= 0 || (item.RepositoryQualifier != "" && ValidateRepositoryLocator(item.RepositoryQualifier) != nil) {
			return nil, logicalError("PROJECT_IDENTITY_INVALID")
		}
		seen[item.ProjectID] = true
	}
	return result.Items, nil
}

// ResolveHistoricalProject reads the server mapping without changing history.
func ResolveHistoricalProject(ctx context.Context, serverURL, token, legacyID string) (Project, bool, error) {
	if !validLegacyMarkerID(legacyID) {
		return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
	}
	endpoint, err := url.Parse(strings.TrimRight(serverURL, "/") + "/api/projects/resolve")
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	query := endpoint.Query()
	query.Set("legacy_id", legacyID)
	endpoint.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, release, err := projectRecoveryGET(ctx, endpoint, token)
	if err != nil {
		return Project{}, false, err
	}
	defer release()
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	if response.StatusCode == http.StatusNotFound {
		var refusal struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &refusal) == nil && refusal.Error == "PROJECT_ADOPTION_UNKNOWN_REFERENCE" {
			return Project{}, false, nil
		}
		return Project{}, false, logicalError("PROJECT_IDENTITY_CONFLICT")
	}
	if response.StatusCode != http.StatusOK {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	var result struct {
		Status    string `json:"status"`
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
	}
	switch result.Status {
	case "unresolved":
		if result.ProjectID != "" || result.Name != "" {
			return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
		}
		return Project{}, false, nil
	case "resolved":
		project := Project{ProjectID: result.ProjectID, Name: result.Name}
		if err := ValidateProject(project); err != nil {
			return Project{}, false, err
		}
		return project, true, nil
	default:
		return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
	}
}

// RepositoryCandidatesUnknownError identifies a validated conflict response
// that names no candidate projects; it is not an absence or a mapping.
type RepositoryCandidatesUnknownError struct{}

func (*RepositoryCandidatesUnknownError) Error() string { return "PROJECT_IDENTITY_CONFLICT" }
func (*RepositoryCandidatesUnknownError) Unwrap() error {
	return &LogicalError{Code: "PROJECT_IDENTITY_CONFLICT"}
}

// ResolveRepositoryProject checks one normalized primary origin through the
// server's existing read-only resolver. Conflict is never treated as absence.
func ResolveRepositoryProject(ctx context.Context, serverURL, token, locator string) (Project, bool, error) {
	if err := ValidateRepositoryLocator(locator); err != nil {
		return Project{}, false, err
	}
	endpoint, err := url.Parse(strings.TrimRight(serverURL, "/") + "/api/projects/resolve")
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	query := endpoint.Query()
	query.Set("repository_locator", locator)
	endpoint.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, release, err := projectRecoveryGET(ctx, endpoint, token)
	if err != nil {
		return Project{}, false, err
	}
	defer release()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return Project{}, false, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	var inspection ProjectInspection
	if json.Unmarshal(raw, &inspection) != nil || inspection.Validate() != nil {
		return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
	}
	switch inspection.Status {
	case "unbound":
		return Project{}, false, nil
	case "resolved":
		if inspection.Binding.Match != "manual_alias" && inspection.Binding.Match != "git_remote" {
			return Project{}, false, logicalError("PROJECT_IDENTITY_INVALID")
		}
		return Project{ProjectID: inspection.Binding.ID}, true, nil
	case "conflict":
		if inspection.Binding != nil {
			return Project{}, false, logicalError("PROJECT_IDENTITY_CONFLICT")
		}
		return Project{}, false, &RepositoryCandidatesUnknownError{}
	default:
		return Project{}, false, logicalError("PROJECT_IDENTITY_CONFLICT")
	}
}
