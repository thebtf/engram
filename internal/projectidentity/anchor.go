package projectidentity

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const anchorFilenameV3 = ".engram-project"

var (
	errAnchorInvalidV3     = errors.New("invalid V3 project anchor")
	errDescriptorInvalidV3 = errors.New("invalid V3 project descriptor")
)

// AnchorV3 is the complete portable project anchor. ProjectID and Scope are
// identity facts; Name is display metadata.
type AnchorV3 struct {
	Version   int    `json:"version"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
}

// RemoteDisposition classifies remote evidence without retaining rejected input.
type RemoteDisposition string

const (
	RemoteNormalizedV3 RemoteDisposition = "normalized"
	RemoteOmittedV3    RemoteDisposition = "omitted"
	RemoteRefusedV3    RemoteDisposition = "refused"
)

// LegacyIdentifierSchemeV3 identifies the source format of legacy evidence.
type LegacyIdentifierSchemeV3 string

// LegacyIdentifierProvenanceV3 identifies how legacy evidence was obtained.
type LegacyIdentifierProvenanceV3 string

// LegacyIdentifierV3 is non-authoritative compatibility evidence.
type LegacyIdentifierV3 struct {
	Scheme     LegacyIdentifierSchemeV3     `json:"scheme"`
	Value      string                       `json:"value"`
	Provenance LegacyIdentifierProvenanceV3 `json:"provenance"`
}

// DescriptorV3 carries portable V3 identity evidence and never a canonical key.
type DescriptorV3 struct {
	Version              int                  `json:"version"`
	AnchorProjectID      string               `json:"anchor_project_id"`
	Name                 string               `json:"name"`
	Scope                string               `json:"scope"`
	NormalizedGitRemotes []string             `json:"normalized_git_remotes"`
	LegacyIdentifiers    []LegacyIdentifierV3 `json:"legacy_identifiers"`
	ClientInstanceID     string               `json:"client_instance_id"`
}

// ParseAnchorV3 validates the complete, closed V3 anchor document.
func ParseAnchorV3(raw []byte) (AnchorV3, error) {
	var wire struct {
		Version   *int    `json:"version"`
		ProjectID *string `json:"project_id"`
		Name      *string `json:"name"`
		Scope     *string `json:"scope"`
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return AnchorV3{}, errAnchorInvalidV3
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return AnchorV3{}, errAnchorInvalidV3
	}
	if wire.Version == nil || wire.ProjectID == nil || wire.Name == nil || wire.Scope == nil {
		return AnchorV3{}, errAnchorInvalidV3
	}

	anchor := AnchorV3{Version: *wire.Version, ProjectID: *wire.ProjectID, Name: *wire.Name, Scope: *wire.Scope}
	if !validAnchorV3(anchor) {
		return AnchorV3{}, errAnchorInvalidV3
	}
	return anchor, nil
}

func validAnchorV3(anchor AnchorV3) bool {
	if anchor.Version != 3 || !validUUID(anchor.ProjectID) || anchor.Name == "" || utf8.RuneCountInString(anchor.Name) > 256 {
		return false
	}
	return anchor.Scope == "repository" || anchor.Scope == "directory"
}

func validUUID(raw string) bool {
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return false
	}
	_, err := uuid.Parse(raw)
	return err == nil
}

// DiscoverAnchorV3 reads only the anchor directly under the explicitly selected root.
func DiscoverAnchorV3(root, scope string) (AnchorV3, error) {
	if scope != "repository" && scope != "directory" {
		return AnchorV3{}, errAnchorInvalidV3
	}
	selectedRoot, err := filepath.Abs(root)
	if err != nil {
		return AnchorV3{}, errAnchorInvalidV3
	}
	info, err := os.Stat(selectedRoot)
	if err != nil || !info.IsDir() {
		return AnchorV3{}, errAnchorInvalidV3
	}
	if scope == "repository" && !isSelectedGitRoot(selectedRoot) {
		return AnchorV3{}, errAnchorInvalidV3
	}

	raw, err := os.ReadFile(filepath.Join(selectedRoot, anchorFilenameV3))
	if err != nil {
		return AnchorV3{}, errAnchorInvalidV3
	}
	anchor, err := ParseAnchorV3(raw)
	if err != nil || anchor.Scope != scope {
		return AnchorV3{}, errAnchorInvalidV3
	}
	if scope == "repository" && !isTrackedAnchor(selectedRoot) {
		return AnchorV3{}, errAnchorInvalidV3
	}
	return anchor, nil
}

func isSelectedGitRoot(root string) bool {
	output, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}
	gitRoot := strings.TrimSpace(string(output))
	return samePath(root, gitRoot)
}

func isTrackedAnchor(root string) bool {
	return exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", anchorFilenameV3).Run() == nil
}

func samePath(left, right string) bool {
	left = canonicalPath(left)
	right = canonicalPath(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(absolute)
}

// NormalizeGitRemoteV3 returns credential-free portable remote evidence.
func NormalizeGitRemoteV3(raw string) (string, RemoteDisposition, error) {
	value := strings.TrimSpace(raw)
	if value == "" || isLocalRemote(value) {
		return "", RemoteOmittedV3, nil
	}

	if normalized, disposition, matched := normalizeSCPRemote(value); matched {
		return normalized, disposition, nil
	}
	return normalizeURLRemote(value)
}

func isLocalRemote(value string) bool {
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "file:") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") {
		return true
	}
	return len(value) >= 3 && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func normalizeSCPRemote(value string) (string, RemoteDisposition, bool) {
	colon := strings.IndexByte(value, ':')
	if strings.Contains(value, "://") || colon <= 0 || strings.Contains(value[:colon], "/") {
		return "", "", false
	}
	hostPart, path := value[:colon], value[colon+1:]
	if strings.Contains(path, "@") {
		return "", RemoteRefusedV3, true
	}
	if path == "" {
		return "", RemoteOmittedV3, true
	}
	if at := strings.LastIndexByte(hostPart, '@'); at >= 0 {
		user := hostPart[:at]
		if user == "" || strings.Contains(user, ":") {
			return "", RemoteRefusedV3, true
		}
		hostPart = hostPart[at+1:]
	}
	if hostPart == "" || strings.ContainsAny(hostPart, "@?#[\\]") {
		return "", RemoteOmittedV3, true
	}
	normalized, ok := normalizedRemote(hostPart, path)
	if !ok {
		return "", RemoteOmittedV3, true
	}
	return normalized, RemoteNormalizedV3, true
}

func normalizeURLRemote(value string) (string, RemoteDisposition, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return "", RemoteOmittedV3, nil
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "file" || (scheme != "http" && scheme != "https" && scheme != "ssh") {
		return "", RemoteOmittedV3, nil
	}
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if hasPassword || scheme != "ssh" {
			return "", RemoteRefusedV3, nil
		}
	}
	host := parsed.Hostname()
	if host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", RemoteOmittedV3, nil
	}
	port := parsed.Port()
	if port != "" && !isDefaultPort(scheme, port) {
		host += ":" + port
	}
	normalized, ok := normalizedRemote(host, parsed.EscapedPath())
	if !ok {
		return "", RemoteOmittedV3, nil
	}
	return normalized, RemoteNormalizedV3, nil
}

func isDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443") || (scheme == "ssh" && port == "22")
}

func normalizedRemote(host, rawPath string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", false
	}
	parts := strings.FieldsFunc(strings.ReplaceAll(rawPath, "\\", "/"), func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return "", false
	}
	path := strings.Join(parts, "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return "", false
	}
	return host + "/" + path, true
}

// BuildDescriptorV3 constructs the non-authoritative, non-persistent V3 request descriptor.
func BuildDescriptorV3(anchor AnchorV3, remotes []string, legacy []LegacyIdentifierV3, clientInstanceID string) (DescriptorV3, error) {
	if !validAnchorV3(anchor) || !validClientInstanceIDV3(clientInstanceID) {
		return DescriptorV3{}, errDescriptorInvalidV3
	}
	for _, remote := range remotes {
		if !validNormalizedRemote(remote) {
			return DescriptorV3{}, errDescriptorInvalidV3
		}
	}
	for _, identifier := range legacy {
		if !validLegacyIdentifierSchemeV3(identifier.Scheme) || !validDescriptorEvidence(identifier.Value) || !validDescriptorEvidence(string(identifier.Provenance)) {
			return DescriptorV3{}, errDescriptorInvalidV3
		}
	}
	if remotes == nil {
		remotes = []string{}
	}
	if legacy == nil {
		legacy = []LegacyIdentifierV3{}
	}
	return DescriptorV3{
		Version:              3,
		AnchorProjectID:      anchor.ProjectID,
		Name:                 anchor.Name,
		Scope:                anchor.Scope,
		NormalizedGitRemotes: remotes,
		LegacyIdentifiers:    legacy,
		ClientInstanceID:     clientInstanceID,
	}, nil
}

func validLegacyIdentifierSchemeV3(scheme LegacyIdentifierSchemeV3) bool {
	switch scheme {
	case "anchor_v3", "binding_v2", "git_remote_relative_v2", "git_hash_v2", "path_hash_v1", "legacy_slug", "non_git_anchor_v2", "manual_alias":
		return true
	default:
		return false
	}
}

func validDescriptorEvidence(value string) bool {
	return value != "" && strings.IndexFunc(value, unicode.IsSpace) == -1 && strings.IndexFunc(value, unicode.IsControl) == -1 && !strings.Contains(value, "@") && !strings.Contains(value, "://")
}

func validNormalizedRemote(remote string) bool {
	if remote == "" || strings.TrimSpace(remote) != remote || strings.ContainsAny(remote, " \t\r\n@?#\\") || strings.Contains(remote, "://") {
		return false
	}
	host, path, ok := strings.Cut(remote, "/")
	if !ok || host == "" || path == "" || host != strings.ToLower(host) || strings.HasPrefix(path, "/") || strings.HasSuffix(path, ".git") || strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return false
	}
	hostname, port, hasPort := strings.Cut(host, ":")
	if hostname == "" || strings.Contains(hostname, ":") || (hasPort && (port == "" || strings.Trim(port, "0123456789") != "")) {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}
