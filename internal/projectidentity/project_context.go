package projectidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	projectContextCatalogBytes = 1024 * 1024
	projectContextCatalogItems = 4096
	projectSelectionPrefix     = "engram-project-choice-v1:"
)

// ProjectContextCaller is supplied by the actual transport, never by tool args.
// CallerID is an existing installation/transport namespace, never a worktree
// hash, actor, session or authorization grant. An unknown namespace stays empty.
// None of this configuration is part of a public tool response or local binding.
type ProjectContextCaller struct {
	Cwd       string `json:"-"`
	ServerURL string `json:"-"`
	Token     string `json:"-"`
	CallerID  string `json:"-"`
}

// ProjectChoice keeps the current catalog label separate from selection evidence.
type ProjectChoice struct {
	ProjectID           string `json:"project_id"`
	Name                string `json:"name"`
	RepositoryQualifier string `json:"repository_qualifier,omitempty"`
	SelectionRef        string `json:"selection_ref"`
}

type ProjectChoices struct {
	Choices []ProjectChoice `json:"choices"`
}

type contextCatalogProject struct {
	ProjectID           string `json:"project_id"`
	Name                string `json:"name"`
	RepositoryQualifier string `json:"repository_qualifier,omitempty"`
}

// ProjectContextChoices is read-only, including local selection storage.
func ProjectContextChoices(ctx context.Context, caller ProjectContextCaller) (ProjectChoices, error) {
	before, err := inspectProjectContext(ctx, caller)
	if err != nil {
		return ProjectChoices{}, err
	}
	catalog, digest, err := readProjectContextCatalog(ctx, caller, before.root)
	if err != nil {
		return ProjectChoices{}, err
	}
	current, err := inspectProjectContext(ctx, caller)
	if err != nil {
		return ProjectChoices{}, err
	}
	if before.stateDigest() != current.stateDigest() {
		return ProjectChoices{}, logicalError("PROJECT_SELECTION_STALE")
	}
	choices := make([]ProjectChoice, len(catalog))
	for index, item := range catalog {
		choices[index] = ProjectChoice{
			ProjectID: item.ProjectID, Name: item.Name, RepositoryQualifier: item.RepositoryQualifier,
			SelectionRef: projectSelectionReference(before, digest, item.ProjectID),
		}
	}
	return ProjectChoices{Choices: choices}, nil
}

// SelectProjectContext binds one existing catalog project, without changing the
// historical marker, common cache, project data, permissions or current session.
func SelectProjectContext(ctx context.Context, caller ProjectContextCaller, selectionRef string) (Project, error) {
	if !validProjectSelectionReference(selectionRef) {
		return Project{}, logicalError("PROJECT_SELECTION_INVALID")
	}
	before, err := inspectProjectContext(ctx, caller)
	if err != nil {
		return Project{}, err
	}
	catalog, digest, err := readProjectContextCatalog(ctx, caller, before.root)
	if err != nil {
		return Project{}, err
	}
	for _, item := range catalog {
		if projectSelectionReference(before, digest, item.ProjectID) != selectionRef {
			continue
		}
		if before.pinnedID != "" && before.pinnedID != item.ProjectID {
			return Project{}, logicalError("PROJECT_IDENTITY_CONFLICT")
		}
		if err := publishProjectContext(ctx, caller, before, item.ProjectID, selectionRef); err != nil {
			return Project{}, err
		}
		return Project{ProjectID: item.ProjectID, Name: item.Name}, nil
	}
	return Project{}, logicalError("PROJECT_SELECTION_STALE")
}

func resolveSelectedProjectContext(ctx context.Context, caller ProjectContextCaller) (Project, bool, error) {
	// Do not change the ordinary resolver's canonical-marker/bootstrap behavior
	// when this exact caller scope has no explicit context sidecar.
	path, err := projectContextPath(caller)
	if err != nil {
		return Project{}, false, err
	}
	_, present, err := readProjectContextFile(path, projectContextBindingBytes)
	if err != nil || !present {
		return Project{}, present, err
	}
	before, err := inspectProjectContext(ctx, caller)
	if err != nil {
		return Project{}, true, err
	}
	if before.binding == nil {
		return Project{}, true, logicalError("PROJECT_SELECTION_STALE")
	}
	catalog, _, err := readProjectContextCatalog(ctx, caller, before.root)
	if err != nil {
		return Project{}, true, err
	}
	current, err := inspectProjectContext(ctx, caller)
	if err != nil {
		return Project{}, true, err
	}
	if before.stateDigest() != current.stateDigest() {
		return Project{}, true, logicalError("PROJECT_SELECTION_STALE")
	}
	for _, item := range catalog {
		if item.ProjectID == before.binding.ProjectID {
			return Project{ProjectID: item.ProjectID, Name: item.Name}, true, nil
		}
	}
	return Project{}, true, logicalError("PROJECT_SELECTION_STALE")
}

func projectSelectionReference(snapshot projectContextSnapshot, catalogDigest, projectID string) string {
	return projectSelectionPrefix + projectContextDigest(snapshot.stateDigest(), catalogDigest, projectID)
}

func validProjectSelectionReference(value string) bool {
	if !strings.HasPrefix(value, projectSelectionPrefix) {
		return false
	}
	return validProjectContextDigest(strings.TrimPrefix(value, projectSelectionPrefix))
}

func projectContextDigest(values ...string) string {
	raw, _ := json.Marshal(values)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func validProjectContextDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func projectContextEndpoint(serverURL string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimRight(serverURL, "/") + "/api/projects")
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	return endpoint, nil
}

func readProjectContextCatalog(ctx context.Context, caller ProjectContextCaller, root string) ([]contextCatalogProject, string, error) {
	endpoint, err := projectContextEndpoint(caller.ServerURL)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, legacyProjectLookupTimeout)
	defer cancel()
	response, releaseTransport, err := projectRecoveryGET(ctx, endpoint, caller.Token)
	if err != nil {
		return nil, "", err
	}
	defer releaseTransport()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, projectContextCatalogBytes+1))
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err != nil {
		return nil, "", logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	var catalog struct {
		Items      []contextCatalogProject `json:"items"`
		Unresolved json.RawMessage         `json:"unresolved"`
	}
	if len(raw) > projectContextCatalogBytes || decodeProjectContextJSON(raw, &catalog) != nil || catalog.Items == nil || len(catalog.Items) > projectContextCatalogItems {
		return nil, "", logicalError("PROJECT_IDENTITY_INVALID")
	}
	seen := make(map[string]struct{}, len(catalog.Items))
	for _, item := range catalog.Items {
		if ValidateProject(Project{ProjectID: item.ProjectID, Name: item.Name}) != nil || item.Name == "" || len(item.RepositoryQualifier) > 2048 || strings.IndexFunc(item.RepositoryQualifier, unicode.IsControl) >= 0 {
			return nil, "", logicalError("PROJECT_IDENTITY_INVALID")
		}
		if item.RepositoryQualifier != "" && ValidateRepositoryLocator(item.RepositoryQualifier) != nil {
			return nil, "", logicalError("PROJECT_IDENTITY_INVALID")
		}
		if _, duplicate := seen[item.ProjectID]; duplicate {
			return nil, "", logicalError("PROJECT_IDENTITY_INVALID")
		}
		seen[item.ProjectID] = struct{}{}
		if caller.Token != "" && (strings.Contains(item.Name, caller.Token) || strings.Contains(item.RepositoryQualifier, caller.Token)) || projectContextNameLeaksRoot(item.Name, root) {
			return nil, "", logicalError("PROJECT_IDENTITY_INVALID")
		}
	}
	canonical, _ := json.Marshal(catalog.Items)
	return catalog.Items, projectContextDigest(string(canonical)), nil
}

// A validated repository qualifier is a remote locator. In a name, only the
// path portion of a well-formed remote URL is similarly unambiguous enough
// to exempt; all other occurrences of the local root remain private.
func projectContextNameLeaksRoot(name, root string) bool {
	if root == "" {
		return true
	}
	delimiter := func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("'\"<>()[]{},;", r)
	}
	if strings.IndexFunc(root, delimiter) >= 0 {
		return strings.Contains(name, root)
	}
	for cursor := 0; cursor < len(name); {
		r, size := utf8.DecodeRuneInString(name[cursor:])
		if delimiter(r) {
			cursor += size
			continue
		}
		start := cursor
		open, close := projectContextIPv6Brackets(name[start:])
		for cursor < len(name) {
			r, size = utf8.DecodeRuneInString(name[cursor:])
			if delimiter(r) && cursor-start != open && cursor-start != close {
				break
			}
			cursor += size
		}
		part := name[start:cursor]
		if !strings.Contains(part, root) {
			continue
		}
		u, err := url.Parse(part)
		if err != nil || strings.Contains(part, `\`) || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ssh" && u.Scheme != "git") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
			return true
		}
		if !projectContextValidEscapes(part) {
			return true
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return true
			}
		}
		authorityEnd := len(u.Scheme) + 3
		if !strings.HasPrefix(part, u.Scheme+"://") {
			return true
		}
		if root == "/" && !strings.Contains(part[authorityEnd:], root) {
			continue
		}
		pathOffset := strings.IndexAny(part[authorityEnd:], "/?#")
		if pathOffset < 0 || part[authorityEnd+pathOffset] != '/' {
			return true
		}
		pathStart := authorityEnd + pathOffset
		pathEnd := len(part)
		if end := strings.IndexAny(part[pathStart:], "?#"); end >= 0 {
			pathEnd = pathStart + end
		}
		if strings.Contains(part[authorityEnd:pathStart], root) || strings.Contains(part[pathEnd:], root) || !strings.Contains(part[pathStart:pathEnd], root) {
			return true
		}
	}
	return false
}

func projectContextIPv6Brackets(text string) (int, int) {
	scheme, rest, ok := strings.Cut(text, "://[")
	if !ok || (scheme != "http" && scheme != "https" && scheme != "ssh" && scheme != "git") {
		return -1, -1
	}
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return -1, -1
	}
	address, err := netip.ParseAddr(rest[:end])
	if err != nil || !address.Is6() {
		return -1, -1
	}
	open := len(scheme) + 3
	return open, open + 1 + end
}

func projectContextValidEscapes(text string) bool {
	hex := func(c byte) bool {
		return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '%' {
			if i+2 >= len(text) || !hex(text[i+1]) || !hex(text[i+2]) {
				return false
			}
			i += 2
		}
	}
	return true
}

// Reject duplicate object members before typed decoding; encoding/json otherwise
// accepts the last duplicate. Depth and total input bytes are bounded at callers.
func decodeProjectContextJSON(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return logicalError("PROJECT_IDENTITY_INVALID")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanProjectContextJSON(decoder, 0); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return logicalError("PROJECT_IDENTITY_INVALID")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return logicalError("PROJECT_IDENTITY_INVALID")
	}
	return nil
}

func scanProjectContextJSON(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return logicalError("PROJECT_IDENTITY_INVALID")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return logicalError("PROJECT_IDENTITY_INVALID")
			}
			name = strings.ToLower(name)
			if _, duplicate := seen[name]; duplicate {
				return logicalError("PROJECT_IDENTITY_INVALID")
			}
			seen[name] = struct{}{}
			if err := scanProjectContextJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return logicalError("PROJECT_IDENTITY_INVALID")
		}
	case json.Delim('['):
		for decoder.More() {
			if err := scanProjectContextJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return logicalError("PROJECT_IDENTITY_INVALID")
		}
	case json.Delim('}'), json.Delim(']'):
		return logicalError("PROJECT_IDENTITY_INVALID")
	}
	return nil
}
