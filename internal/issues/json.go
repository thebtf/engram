package issues

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/issues/contract"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/redaction"
)

const (
	MetadataBytes = 1 << 20
	MetadataDepth = 256
)

func NormalizeLabels(labels []string) []string { return contract.NormalizeLabels(labels) }

// DecodeReceivedObject validates the received duplicate/numeric/Unicode boundary
// before an adapter renames native comment/reason fields. Raw member values stay
// intact. This grants no authority and does not replace Application admission.
func DecodeReceivedObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if _, err := requestObject(raw); err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return nil, Failure("INVALID_ARGUMENT")
	}
	return members, nil
}

// EncodeReceivedObject keeps admitted raw values from growing through HTML
// escaping while an adapter renames members or wraps the received arguments.
// It grants no authority and does not replace received-object admission.
func EncodeReceivedObject(members map[string]json.RawMessage) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(members); err != nil {
		return nil, Failure("INVALID_ARGUMENT")
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'}), nil
}

func decodeObject(raw []byte, target any, rules []redaction.CompiledRule) (map[string]any, error) {
	object, err := requestObject(raw)
	if err != nil {
		return nil, err
	}
	// JSON null is not omission for nonnullable fields. Nullable labels and the
	// optional actor descriptor have their own explicit semantics.
	for _, key := range []string{"title", "body", "priority", "type", "status", "target_project", "source_project", "source_agent", "created_by_session", "labels", "request_key", "expected_revision", "comment", "expected_actor"} {
		if value, present := object[key]; present && value == nil {
			return nil, Failure("INVALID_ARGUMENT")
		}
	}
	if labels, present := object["labels"]; present {
		array, ok := labels.([]any)
		if !ok {
			return nil, Failure("INVALID_ARGUMENT")
		}
		for _, label := range array {
			if _, ok := label.(string); !ok {
				return nil, Failure("INVALID_ARGUMENT")
			}
		}
	}
	if err := refusePrivate(object, rules); err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, target) != nil {
		return nil, Failure("INVALID_ARGUMENT")
	}
	return object, nil
}

var requestMembers = []string{
	"title", "body", "priority", "type", "status", "target_project", "source_project", "source_agent", "created_by_session",
	"labels", "metadata", "owner_label", "recipient_label", "request_key", "expected_revision", "comment", "comment_metadata", "expected_actor",
	"action", "id", "format", "project",
}

func requestObject(raw []byte) (map[string]any, error) {
	value, err := parseJSON(raw, MetadataDepth+2)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, Failure("INVALID_ARGUMENT")
	}
	if err := rejectMemberAliases(object, requestMembers); err != nil {
		return nil, err
	}
	if actor, ok := object["expected_actor"].(map[string]any); ok {
		if err := rejectMemberAliases(actor, []string{"kind", "reference"}); err != nil {
			return nil, err
		}
	}
	return object, nil
}

// encoding/json matches struct fields with Unicode case folding. Reject those
// alternate names before exact-name validation or adapter remapping. This check
// applies only to DTO objects, never unknown metadata keys or string values.
func rejectMemberAliases(object map[string]any, names []string) error {
	for key := range object {
		for _, name := range names {
			if key != name && strings.EqualFold(key, name) {
				return Failure("INVALID_ARGUMENT")
			}
		}
	}
	return nil
}

func parseJSON(raw []byte, depth int) (any, error) {
	if !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return nil, Failure("INVALID_ARGUMENT")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readValue(decoder, 0, depth)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, Failure("INVALID_ARGUMENT")
	}
	return value, nil
}

func readValue(decoder *json.Decoder, depth, maxDepth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, Failure("INVALID_ARGUMENT")
	}
	switch value := token.(type) {
	case json.Delim:
		if depth >= maxDepth {
			return nil, Failure("PAYLOAD_TOO_LARGE")
		}
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || strings.ContainsRune(key, 0) {
					return nil, Failure("INVALID_ARGUMENT")
				}
				if _, duplicate := object[key]; duplicate {
					return nil, Failure("INVALID_ARGUMENT")
				}
				child, err := readValue(decoder, depth+1, maxDepth)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, Failure("INVALID_ARGUMENT")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				child, err := readValue(decoder, depth+1, maxDepth)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, Failure("INVALID_ARGUMENT")
			}
			return array, nil
		default:
			return nil, Failure("INVALID_ARGUMENT")
		}
	case json.Number:
		return admittedNumber(string(value))
	case string:
		if strings.ContainsRune(value, 0) {
			return nil, Failure("INVALID_ARGUMENT")
		}
		return value, nil
	default:
		return value, nil
	}
}

func admittedNumber(token string) (float64, error) {
	value, err := strconv.ParseFloat(token, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, Failure("INVALID_ARGUMENT")
	}
	if value == 0 {
		coefficient := strings.FieldsFunc(token, func(r rune) bool { return r == 'e' || r == 'E' })[0]
		if strings.Trim(coefficient, "-0.") != "" {
			return 0, Failure("INVALID_ARGUMENT")
		}
		return 0, nil
	}
	original, ok := new(big.Rat).SetString(token)
	if !ok {
		return 0, Failure("INVALID_ARGUMENT")
	}
	if original.IsInt() && math.Abs(value) > 9007199254740991 {
		return 0, Failure("INVALID_ARGUMENT")
	}
	shortest, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	if !ok || original.Cmp(shortest) != 0 {
		return 0, Failure("INVALID_ARGUMENT")
	}
	return value, nil
}

func validateMetadata(raw json.RawMessage) error {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return Failure("INVALID_ARGUMENT")
	}
	if compact.Len() > MetadataBytes {
		return Failure("PAYLOAD_TOO_LARGE")
	}
	value, err := parseJSON(raw, MetadataDepth)
	if err != nil {
		return err
	}
	if _, ok := value.(map[string]any); !ok {
		return Failure("INVALID_ARGUMENT")
	}
	return nil
}

func refusePrivate(value any, rules []redaction.CompiledRule) error {
	check := func(text string) error {
		if privacy.ContainsSecrets(text) {
			return Failure("PRIVACY_REJECTED")
		}
		if len(rules) != 0 {
			_, matched, err := redaction.ScrubCompiled(text, rules)
			if err != nil || len(matched) != 0 {
				return Failure("PRIVACY_REJECTED")
			}
		}
		return nil
	}
	switch value := value.(type) {
	case string:
		return check(value)
	case []any:
		for _, child := range value {
			if err := refusePrivate(child, rules); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, child := range value {
			if err := check(key); err != nil {
				return err
			}
			if text, ok := child.(string); ok {
				quoted, _ := json.Marshal(text)
				if err := check(key + ":" + string(quoted)); err != nil {
					return err
				}
			}
			if err := refusePrivate(child, rules); err != nil {
				return err
			}
		}
	}
	return nil
}
