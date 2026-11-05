package projectidentity

import (
	"bytes"
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

const (
	maxRecoveryCatalogBytes = 16 << 20
	maxRecoveryCatalogDepth = 64
	maxRecoveryCatalogItems = 4096
)

type recoveryCatalogReader struct {
	source io.Reader
	err    error
}

func (reader *recoveryCatalogReader) Read(p []byte) (int, error) {
	n, err := reader.source.Read(p)
	if err != nil && err != io.EOF {
		reader.err = err
	}
	return n, err
}

// skipRecoveryCatalogValue validates and discards metadata without retaining
// the whole unresolved array. Case folding is needed only for item fields,
// whose Go struct decoder accepts case-insensitive field names.
func skipRecoveryCatalogValue(decoder *json.Decoder, depth int, foldKeys bool) bool {
	if depth > maxRecoveryCatalogDepth {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return false
			}
			if foldKeys {
				name = strings.ToLower(name)
			}
			if seen[name] {
				return false
			}
			seen[name] = true
			if !skipRecoveryCatalogValue(decoder, depth+1, false) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case json.Delim('['):
		for decoder.More() {
			if !skipRecoveryCatalogValue(decoder, depth+1, false) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	case json.Delim('}'), json.Delim(']'):
		return false
	}
	return true
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
		return nil, projectRecoveryStatusError(response.StatusCode)
	}
	tracked := &recoveryCatalogReader{source: response.Body}
	limited := &io.LimitedReader{R: tracked, N: maxRecoveryCatalogBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	invalid := func() ([]ExistingProjectChoice, error) {
		if limited.N == 0 || tracked.err != nil || ctx.Err() != nil {
			return nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		return nil, logicalError("PROJECT_IDENTITY_INVALID")
	}
	if opener, err := decoder.Token(); err != nil || opener != json.Delim('{') {
		return invalid()
	}
	seenFields := make(map[string]bool)
	seenProjects := make(map[string]bool)
	var items []ExistingProjectChoice
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return invalid()
		}
		field := name
		if strings.EqualFold(name, "items") {
			field = "items"
		}
		if seenFields[field] {
			return invalid()
		}
		seenFields[field] = true
		if field != "items" {
			if !skipRecoveryCatalogValue(decoder, 1, false) {
				return invalid()
			}
			continue
		}
		if opener, err := decoder.Token(); err != nil || opener != json.Delim('[') {
			return invalid()
		}
		items = make([]ExistingProjectChoice, 0)
		for decoder.More() {
			if len(items) >= maxRecoveryCatalogItems {
				return invalid()
			}
			var raw json.RawMessage
			if decoder.Decode(&raw) != nil {
				return invalid()
			}
			check := json.NewDecoder(bytes.NewReader(raw))
			check.UseNumber()
			if !skipRecoveryCatalogValue(check, 2, true) {
				return invalid()
			}
			if _, err := check.Token(); err != io.EOF {
				return invalid()
			}
			var item ExistingProjectChoice
			if json.Unmarshal(raw, &item) != nil || seenProjects[item.ProjectID] || ValidateProject(Project{ProjectID: item.ProjectID, Name: item.Name}) != nil || item.Name == "" || (token != "" && (strings.Contains(item.Name, token) || strings.Contains(item.RepositoryQualifier, token))) || len(item.RepositoryQualifier) > 2048 || strings.IndexFunc(item.RepositoryQualifier, unicode.IsControl) >= 0 || (item.RepositoryQualifier != "" && ValidateRepositoryLocator(item.RepositoryQualifier) != nil) {
				return invalid()
			}
			seenProjects[item.ProjectID] = true
			items = append(items, item)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return invalid()
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return invalid()
	}
	if _, err := decoder.Token(); err != io.EOF || limited.N == 0 || !seenFields["items"] {
		return invalid()
	}
	return items, nil
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
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound {
		return Project{}, false, projectRecoveryStatusError(response.StatusCode)
	}
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
		return Project{}, false, projectRecoveryStatusError(response.StatusCode)
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
