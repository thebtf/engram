package legacyrelay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	requestKeys = map[string]struct{}{
		"protocol": {}, "requestId": {}, "daemonGeneration": {}, "adapter": {},
		"route": {}, "deadlineUnixMs": {}, "body": {},
	}
	adapterKeys = map[string]struct{}{
		"revision": {}, "installedArtifactSha256": {},
	}
	identityBodyKeys = map[string]struct{}{
		"hostSessionRef": {}, "projectIdentityV3": {},
	}
	sessionBodyKeys = map[string]struct{}{
		"hostSessionRef": {}, "sessionCapability": {},
	}
	ambientBodyKeys = map[string]struct{}{
		"hostSessionRef": {}, "sessionCapability": {}, "queryText": {},
	}
	descriptorKeys = map[string]struct{}{
		"version": {}, "anchor_project_id": {}, "name": {}, "scope": {},
		"normalized_git_remotes": {}, "legacy_identifiers": {}, "client_instance_id": {},
	}
	legacyIdentifierKeys = map[string]struct{}{
		"scheme": {}, "value": {}, "provenance": {},
	}
)

// ReadRequestFrame consumes exactly one capped UTF-8 JSON object terminated by
// LF. A caller must create reader with a buffer no larger than maxBytes+1;
// Relay.ServeConn does so before accepting untrusted bytes.
func ReadRequestFrame(reader *bufio.Reader, maxBytes int, now time.Time) (IncomingRequest, error) {
	if reader == nil {
		return nil, fmt.Errorf("%w: nil reader", ErrInvalidFrame)
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: invalid frame cap", ErrInvalidFrame)
	}
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxBytes+1 {
		return nil, ErrFrameTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("%w: request line must end with LF", ErrInvalidFrame)
	}
	if len(line) == 1 {
		return nil, fmt.Errorf("%w: empty request", ErrInvalidFrame)
	}
	line = line[:len(line)-1]
	if len(line) > maxBytes {
		return nil, ErrFrameTooLarge
	}
	if reader.Buffered() != 0 {
		return nil, fmt.Errorf("%w: trailing buffered data", ErrInvalidFrame)
	}
	request, err := ParseRequestFrame(line)
	if err != nil {
		return nil, err
	}
	if now.IsZero() || request.DeadlineUnixMs() <= now.UnixMilli() {
		return nil, ErrDeadlineElapsed
	}
	return request, nil
}

// ParseRequestFrame validates a complete request object. It never retains a
// raw request body beyond the returned bounded V3 descriptor value.
func ParseRequestFrame(raw []byte) (IncomingRequest, error) {
	if len(raw) == 0 || len(raw) > DefaultMaxFrameBytes || !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: invalid frame bytes", ErrInvalidFrame)
	}
	object, err := decodeExactObject(raw, requestKeys)
	if err != nil {
		return nil, err
	}
	protocol, err := requiredString(object, "protocol")
	if err != nil || protocol != Protocol {
		return nil, fmt.Errorf("%w: unsupported protocol", ErrInvalidFrame)
	}
	requestID, err := requiredString(object, "requestId")
	if err != nil {
		return nil, err
	}
	parsedRequestID, err := newRequestID(requestID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	generationRaw, err := requiredString(object, "daemonGeneration")
	if err != nil {
		return nil, err
	}
	generation, err := NewDaemonGeneration(generationRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	adapter, err := parseAdapter(object["adapter"])
	if err != nil {
		return nil, err
	}
	routeRaw, err := requiredString(object, "route")
	if err != nil {
		return nil, err
	}
	route, err := parseRoute(routeRaw)
	if err != nil {
		return nil, err
	}
	deadline, err := requiredInt64(object, "deadlineUnixMs")
	if err != nil || deadline <= 0 {
		return nil, fmt.Errorf("%w: invalid absolute deadline", ErrInvalidFrame)
	}
	header := requestHeader{requestID: parsedRequestID, generation: generation, adapter: adapter, deadlineMs: deadline}
	if !header.valid() {
		return nil, fmt.Errorf("%w: malformed request header", ErrInvalidFrame)
	}
	body, ok := object["body"]
	if !ok {
		return nil, fmt.Errorf("%w: missing body", ErrInvalidFrame)
	}
	switch route {
	case RouteIdentityRegistration:
		return parseIdentityRequest(header, body)
	case RouteSessionStartContext:
		return parseSessionStartRequest(header, body)
	case RouteAmbientCandidates:
		return parseAmbientRequest(header, body)
	default:
		return nil, fmt.Errorf("%w: unsupported route", ErrInvalidFrame)
	}
}

func parseAdapter(raw json.RawMessage) (AdapterAttestation, error) {
	object, err := decodeExactObject(raw, adapterKeys)
	if err != nil {
		return AdapterAttestation{}, err
	}
	revision, err := requiredString(object, "revision")
	if err != nil {
		return AdapterAttestation{}, err
	}
	digest, err := requiredString(object, "installedArtifactSha256")
	if err != nil {
		return AdapterAttestation{}, err
	}
	adapter, err := NewAdapterAttestation(revision, digest)
	if err != nil {
		return AdapterAttestation{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	return adapter, nil
}

func parseIdentityRequest(header requestHeader, raw json.RawMessage) (IncomingRequest, error) {
	object, err := decodeExactObject(raw, identityBodyKeys)
	if err != nil {
		return nil, err
	}
	hostRaw, err := requiredString(object, "hostSessionRef")
	if err != nil {
		return nil, err
	}
	host, err := NewHostSessionRef(hostRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	descriptorRaw, ok := object["projectIdentityV3"]
	if !ok {
		return nil, fmt.Errorf("%w: missing V3 descriptor", ErrInvalidFrame)
	}
	descriptor, err := parseProjectIdentityV3Descriptor(descriptorRaw)
	if err != nil {
		return nil, err
	}
	return IdentityRegistrationRequest{header: header, hostSession: host, descriptor: descriptor}, nil
}

func parseSessionStartRequest(header requestHeader, raw json.RawMessage) (IncomingRequest, error) {
	object, err := decodeExactObject(raw, sessionBodyKeys)
	if err != nil {
		return nil, err
	}
	hostRaw, err := requiredString(object, "hostSessionRef")
	if err != nil {
		return nil, err
	}
	host, err := NewHostSessionRef(hostRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	capabilityRaw, err := requiredString(object, "sessionCapability")
	if err != nil {
		return nil, err
	}
	capability, err := parseCapability(capabilityRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	return SessionStartRequest{header: header, hostSession: host, capability: capability}, nil
}

func parseAmbientRequest(header requestHeader, raw json.RawMessage) (IncomingRequest, error) {
	object, err := decodeExactObject(raw, ambientBodyKeys)
	if err != nil {
		return nil, err
	}
	hostRaw, err := requiredString(object, "hostSessionRef")
	if err != nil {
		return nil, err
	}
	host, err := NewHostSessionRef(hostRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	capabilityRaw, err := requiredString(object, "sessionCapability")
	if err != nil {
		return nil, err
	}
	capability, err := parseCapability(capabilityRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	query, err := requiredString(object, "queryText")
	if err != nil || !utf8.ValidString(query) || len(query) > maxAdditionalContextBytes {
		return nil, fmt.Errorf("%w: invalid ambient query", ErrInvalidFrame)
	}
	return AmbientRequest{header: header, hostSession: host, capability: capability, queryText: query}, nil
}

func parseProjectIdentityV3Descriptor(raw json.RawMessage) (ProjectIdentityV3Descriptor, error) {
	if len(raw) == 0 || len(raw) > maxDescriptorBytes || !utf8.Valid(raw) {
		return ProjectIdentityV3Descriptor{}, fmt.Errorf("%w: invalid V3 descriptor size", ErrInvalidFrame)
	}
	object, err := decodeExactObject(raw, descriptorKeys)
	if err != nil {
		return ProjectIdentityV3Descriptor{}, err
	}
	version, err := requiredInt64(object, "version")
	if err != nil || version != 3 {
		return ProjectIdentityV3Descriptor{}, fmt.Errorf("%w: unsupported V3 descriptor", ErrInvalidFrame)
	}
	projectID, err := requiredString(object, "anchor_project_id")
	if err != nil || !validUUID(projectID) {
		return ProjectIdentityV3Descriptor{}, fmt.Errorf("%w: invalid V3 anchor project ID", ErrInvalidFrame)
	}
	name, err := requiredString(object, "name")
	if err != nil || !validDescriptorText(name, 256) {
		return ProjectIdentityV3Descriptor{}, fmt.Errorf("%w: invalid V3 project name", ErrInvalidFrame)
	}
	scope, err := requiredString(object, "scope")
	if err != nil || (scope != "repository" && scope != "directory") {
		return ProjectIdentityV3Descriptor{}, fmt.Errorf("%w: invalid V3 project scope", ErrInvalidFrame)
	}
	clientID, err := requiredString(object, "client_instance_id")
	if err != nil || !validClientInstanceID(clientID) {
		return ProjectIdentityV3Descriptor{}, fmt.Errorf("%w: invalid V3 client instance", ErrInvalidFrame)
	}
	if err := validateStringArray(object["normalized_git_remotes"], 32, 2048, true); err != nil {
		return ProjectIdentityV3Descriptor{}, err
	}
	if err := validateLegacyIdentifiers(object["legacy_identifiers"]); err != nil {
		return ProjectIdentityV3Descriptor{}, err
	}
	return ProjectIdentityV3Descriptor{raw: bytes.Clone(raw)}, nil
}

func validateStringArray(raw json.RawMessage, maxItems, maxItemBytes int, rejectCredentialShape bool) error {
	items, err := decodeArray(raw)
	if err != nil || len(items) > maxItems {
		return fmt.Errorf("%w: invalid descriptor array", ErrInvalidFrame)
	}
	for _, item := range items {
		var value string
		if err := json.Unmarshal(item, &value); err != nil || !validDescriptorText(value, maxItemBytes) || (rejectCredentialShape && hasCredentialShape(value)) {
			return fmt.Errorf("%w: invalid descriptor array value", ErrInvalidFrame)
		}
	}
	return nil
}

func validateLegacyIdentifiers(raw json.RawMessage) error {
	items, err := decodeArray(raw)
	if err != nil || len(items) > 32 {
		return fmt.Errorf("%w: invalid legacy identifiers", ErrInvalidFrame)
	}
	for _, rawItem := range items {
		item, err := decodeExactObject(rawItem, legacyIdentifierKeys)
		if err != nil {
			return err
		}
		scheme, err := requiredString(item, "scheme")
		if err != nil || !validLegacyScheme(scheme) {
			return fmt.Errorf("%w: invalid legacy identifier scheme", ErrInvalidFrame)
		}
		value, err := requiredString(item, "value")
		if err != nil || !validDescriptorText(value, 2048) || hasCredentialShape(value) || (scheme == "manual_alias" && strings.IndexFunc(value, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) >= 0) {
			return fmt.Errorf("%w: invalid legacy identifier value", ErrInvalidFrame)
		}
		provenance, err := requiredString(item, "provenance")
		if err != nil || !validDescriptorText(provenance, 2048) || hasCredentialShape(provenance) {
			return fmt.Errorf("%w: invalid legacy identifier provenance", ErrInvalidFrame)
		}
	}
	return nil
}

func validLegacyScheme(value string) bool {
	switch value {
	case "anchor_v3", "binding_v2", "git_remote_relative_v2", "git_hash_v2", "path_hash_v1", "legacy_slug", "non_git_anchor_v2", "manual_alias":
		return true
	default:
		return false
	}
}

func validDescriptorText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

func validClientInstanceID(value string) bool {
	if !validDescriptorText(value, maxOpaqueReferenceBytes) || strings.ContainsAny(value, " /\\@") {
		return false
	}
	if !asciiLetter(value[0]) {
		return true
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		switch {
		case asciiLetter(character), character >= '0' && character <= '9', character == '+', character == '.', character == '-':
			continue
		case character == ':':
			return false
		default:
			return true
		}
	}
	return true
}

func asciiLetter(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func hasCredentialShape(value string) bool {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") {
		before, after, found := strings.Cut(lower, "://")
		if found && before != "ssh" && strings.Contains(after, "@") {
			return true
		}
	}
	at := strings.IndexByte(value, '@')
	if at <= 0 {
		return false
	}
	return strings.Contains(value[:at], ":")
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
				return false
			}
		}
	}
	return value[14] >= '1' && value[14] <= '5' && (value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b' || value[19] == 'A' || value[19] == 'B')
}

func decodeExactObject(raw []byte, expected map[string]struct{}) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: invalid UTF-8", ErrInvalidFrame)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: malformed JSON", ErrInvalidFrame)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, fmt.Errorf("%w: expected object", ErrInvalidFrame)
	}
	result := make(map[string]json.RawMessage, len(expected))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: malformed object key", ErrInvalidFrame)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("%w: object key is not a string", ErrInvalidFrame)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrInvalidFrame, key)
		}
		if expected != nil {
			if _, allowed := expected[key]; !allowed {
				return nil, fmt.Errorf("%w: unexpected field %q", ErrInvalidFrame, key)
			}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: malformed field %q", ErrInvalidFrame, key)
		}
		result[key] = bytes.Clone(value)
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: unterminated object", ErrInvalidFrame)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("%w: malformed object", ErrInvalidFrame)
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return nil, fmt.Errorf("%w: multiple JSON values", ErrInvalidFrame)
	}
	if expected != nil && len(result) != len(expected) {
		return nil, fmt.Errorf("%w: missing required field", ErrInvalidFrame)
	}
	return result, nil
}

func validateJSONObject(raw []byte) error {
	_, err := decodeExactObject(raw, nil)
	return err
}

func decodeArray(raw []byte) ([]json.RawMessage, error) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return nil, ErrInvalidFrame
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrInvalidFrame
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return nil, ErrInvalidFrame
	}
	var values []json.RawMessage
	for decoder.More() {
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrInvalidFrame
		}
		values = append(values, bytes.Clone(value))
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, ErrInvalidFrame
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != ']' {
		return nil, ErrInvalidFrame
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return nil, ErrInvalidFrame
	}
	return values, nil
}

func requiredString(object map[string]json.RawMessage, key string) (string, error) {
	raw, ok := object[key]
	if !ok || len(raw) == 0 || bytes.TrimSpace(raw)[0] != '"' {
		return "", fmt.Errorf("%w: %s must be a string", ErrInvalidFrame, key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: %s must be a string", ErrInvalidFrame, key)
	}
	return value, nil
}

func requiredInt64(object map[string]json.RawMessage, key string) (int64, error) {
	raw, ok := object[key]
	if !ok {
		return 0, fmt.Errorf("%w: missing %s", ErrInvalidFrame, key)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, ".eE") {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrInvalidFrame, key)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrInvalidFrame, key)
	}
	return parsed, nil
}

// EncodeResponseFrame serializes one exact terminal response object without a
// trailing LF. ServeConn appends LF and then closes the connection.
func EncodeResponseFrame(response responseEnvelope) ([]byte, error) {
	if !response.requestID.valid() || !response.generation.valid() || !validRoute(response.route) || response.result == nil {
		return nil, errors.New("invalid relay response")
	}
	switch result := response.result.(type) {
	case identityDelivery:
		if !result.capability.valid() || !result.project.valid() {
			return nil, errors.New("invalid identity response")
		}
		return json.Marshal(struct {
			Protocol            string `json:"protocol"`
			RequestID           string `json:"requestId"`
			DaemonGeneration    string `json:"daemonGeneration"`
			Route               string `json:"route"`
			Kind                string `json:"kind"`
			SessionCapability   string `json:"sessionCapability"`
			CanonicalProjectRef string `json:"canonicalProjectRef"`
		}{Protocol, response.requestID.Value(), response.generation.Value(), response.route.String(), "OK", result.capability.wireValue(), result.project.Value()})
	case sessionStartDelivery:
		if !result.payload.valid() {
			return nil, errors.New("invalid session-start response")
		}
		return json.Marshal(struct {
			Protocol         string          `json:"protocol"`
			RequestID        string          `json:"requestId"`
			DaemonGeneration string          `json:"daemonGeneration"`
			Route            string          `json:"route"`
			Kind             string          `json:"kind"`
			Payload          json.RawMessage `json:"payload"`
		}{Protocol, response.requestID.Value(), response.generation.Value(), response.route.String(), "OK", result.payload.RawJSON()})
	case ambientDelivery:
		if !result.context.valid() {
			return nil, errors.New("invalid ambient response")
		}
		return json.Marshal(struct {
			Protocol          string `json:"protocol"`
			RequestID         string `json:"requestId"`
			DaemonGeneration  string `json:"daemonGeneration"`
			Route             string `json:"route"`
			Kind              string `json:"kind"`
			AdditionalContext string `json:"additionalContext"`
		}{Protocol, response.requestID.Value(), response.generation.Value(), response.route.String(), "OK", result.context.Value()})
	case noDelivery:
		if result.reason.String() == "" {
			return nil, errors.New("invalid no-delivery response")
		}
		return json.Marshal(struct {
			Protocol         string `json:"protocol"`
			RequestID        string `json:"requestId"`
			DaemonGeneration string `json:"daemonGeneration"`
			Route            string `json:"route"`
			Kind             string `json:"kind"`
			Reason           string `json:"reason"`
		}{Protocol, response.requestID.Value(), response.generation.Value(), response.route.String(), "NO_DELIVERY", result.reason.String()})
	case rejected:
		if result.reason.String() == "" {
			return nil, errors.New("invalid rejected response")
		}
		return json.Marshal(struct {
			Protocol         string `json:"protocol"`
			RequestID        string `json:"requestId"`
			DaemonGeneration string `json:"daemonGeneration"`
			Route            string `json:"route"`
			Kind             string `json:"kind"`
			Reason           string `json:"reason"`
		}{Protocol, response.requestID.Value(), response.generation.Value(), response.route.String(), "REJECTED", result.reason.String()})
	default:
		return nil, errors.New("unknown relay result")
	}
}
