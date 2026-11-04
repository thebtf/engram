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
	"unicode/utf8"
)

const legacyProjectLookupTimeout = 15 * time.Second

// ResolveLogicalProjectWithRecovery revalidates an explicit caller binding, or
// uses the ordinary resolver and its safe existing legacy mapping recovery.
// Recovery never publishes a marker/cache, creates a project or binds an alias.
// The original caller retains responsibility for its operation and provenance.
func ResolveLogicalProjectWithRecovery(ctx context.Context, caller ProjectContextCaller) (Project, error) {
	project, present, err := resolveSelectedProjectContext(ctx, caller)
	if err != nil || present {
		return project, err
	}
	return resolveLogicalProject(ctx, caller.Cwd, HTTPBootstrap(caller.ServerURL, caller.Token), func(ctx context.Context, legacyID string) (Project, error) {
		before, err := inspectProjectContext(ctx, caller)
		if err != nil {
			return Project{}, err
		}
		if before.legacyID != legacyID || before.binding != nil {
			return Project{}, logicalError("PROJECT_SELECTION_STALE")
		}
		resolved, err := lookupLegacyProject(ctx, caller.ServerURL, caller.Token, legacyID)
		if err != nil {
			return Project{}, err
		}
		current, err := inspectProjectContext(ctx, caller)
		if err != nil {
			return Project{}, err
		}
		if before.stateDigest() != current.stateDigest() {
			return Project{}, logicalError("PROJECT_SELECTION_STALE")
		}
		if current.pinnedID != "" && current.pinnedID != resolved.ProjectID {
			return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
		}
		return resolved, nil
	})
}

func lookupLegacyProject(ctx context.Context, serverURL, token, legacyID string) (Project, error) {
	if !validLegacyMarkerID(legacyID) {
		return Project{}, logicalError("PROJECT_IDENTITY_INVALID")
	}
	if serverURL == "" {
		return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	ctx, cancel := context.WithTimeout(ctx, legacyProjectLookupTimeout)
	defer cancel()
	endpoint, err := url.Parse(strings.TrimRight(serverURL, "/") + "/api/projects/resolve")
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	query := endpoint.Query()
	query.Set("legacy_id", legacyID)
	endpoint.RawQuery = query.Encode()
	response, releaseTransport, err := projectRecoveryGET(ctx, endpoint, token)
	if err != nil {
		return Project{}, err
	}
	defer releaseTransport()
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Project{}, logicalError("PROJECT_MIGRATION_REQUIRED")
	case http.StatusConflict:
		return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
	default:
		return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	if err != nil {
		return Project{}, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	if len(raw) > 4096 || !utf8.Valid(raw) {
		return Project{}, logicalError("PROJECT_IDENTITY_INVALID")
	}
	return parseLegacyProjectMapping(raw)
}

func parseLegacyProjectMapping(raw []byte) (Project, error) {
	const (
		statusField uint8 = 1 << iota
		projectIDField
		nameField
		referenceField
		reasonField
	)
	invalid := func() (Project, error) { return Project{}, logicalError("PROJECT_IDENTITY_INVALID") }
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return invalid()
	}
	var seen uint8
	var status string
	var project Project
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalid()
		}
		var field uint8
		switch key {
		case "status":
			field = statusField
		case "project_id":
			field = projectIDField
		case "name":
			field = nameField
		case "reference":
			field = referenceField
		case "reason":
			field = reasonField
		default:
			return invalid()
		}
		if seen&field != 0 {
			return invalid()
		}
		seen |= field
		token, err := decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return invalid()
		}
		switch field {
		case statusField:
			status = value
		case projectIDField:
			project.ProjectID = value
		case nameField:
			project.Name = value
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return invalid()
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return invalid()
	}
	switch status {
	case "unresolved":
		if seen&(projectIDField|nameField) != 0 {
			return invalid()
		}
		return Project{}, logicalError("PROJECT_MIGRATION_REQUIRED")
	case "resolved":
		if seen&(referenceField|reasonField) != 0 {
			return invalid()
		}
		if err := ValidateProject(project); err != nil {
			return Project{}, err
		}
		return project, nil
	default:
		return invalid()
	}
}
