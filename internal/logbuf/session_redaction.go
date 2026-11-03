package logbuf

import (
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"strings"
)

const browserSessionRedacted = "[REDACTED]"

var (
	browserSessionRoute       = regexp.MustCompile(`/api/access/sessions/([^/?#\s"'<>;\\]+)`)
	browserSessionAssignment  = regexp.MustCompile(`\bsession_id\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s;,"'\\]+))`)
	browserSessionQuotedField = regexp.MustCompile(`("(?:[^"\\]|\\.)*")\s*:\s*("(?:[^"\\]|\\.)*)("|$)`)
)

// RedactBrowserSessionPath returns a logging-only path; routing still uses the original.
func RedactBrowserSessionPath(path string) string {
	if !strings.Contains(path, "/api/access/sessions/") {
		return path
	}
	return browserSessionRoute.ReplaceAllString(path, "/api/access/sessions/"+browserSessionRedacted)
}

// RedactBrowserSessionError retains error diagnostics without browser credentials.
// Its result is log text only; callers must keep using err for error classification.
func RedactBrowserSessionError(err error, requestPath string, browserCredentials ...string) string {
	if err == nil {
		return ""
	}
	var redactor browserSessionLogRedactor
	for _, credential := range browserCredentials {
		redactor.add(credential)
	}
	redactor.collect(requestPath, false)
	text := err.Error()
	redactor.collect(text, false)
	redactor.prepare()
	return redactor.text(text)
}

// RedactBrowserSessionEntry projects old or queued records without modifying shared fields.
func RedactBrowserSessionEntry(entry LogEntry) LogEntry {
	var redactor browserSessionLogRedactor
	redactor.collect(entry.Message, false)
	redactor.collect(entry.Fields, browserSessionLogMessage(entry.Message))
	raw, structured := decodeBrowserSessionLogJSON(entry.Raw)
	if structured {
		redactor.collect(raw, false)
	} else {
		redactor.collect(entry.Raw, false)
	}
	redactor.prepare()

	entry.Message = redactor.text(entry.Message)
	if fields, changed := redactor.value(entry.Fields, browserSessionLogMessage(entry.Message)); changed {
		entry.Fields = fields.(map[string]any)
	}
	if structured {
		safe, _ := redactor.value(raw, false)
		// Decoded JSON contains only JSON-compatible values. Re-encoding also
		// removes shadowed duplicate members rather than retaining unsafe bytes.
		if data, err := json.Marshal(safe); err == nil {
			entry.Raw = string(data)
		} else {
			entry.Raw = redactor.text(entry.Raw)
		}
	} else {
		entry.Raw = redactor.text(entry.Raw)
	}
	return entry
}

type browserSessionLogRedactor struct {
	credentials []string
}

func (r *browserSessionLogRedactor) add(value string) {
	if value == "" || value == browserSessionRedacted || slices.Contains(r.credentials, value) {
		return
	}
	// Only issued bearer literals, not malformed selectors, propagate to other fields.
	if !browserSessionRouteCredential(value) {
		var decoded string
		if !strings.Contains(value, `\`) || json.Unmarshal([]byte(`"`+value+`"`), &decoded) != nil || !browserSessionRouteCredential(decoded) {
			return
		}
	}
	r.credentials = append(r.credentials, value)
}

func (r *browserSessionLogRedactor) prepare() {
	slices.SortFunc(r.credentials, func(a, b string) int { return len(b) - len(a) })
}

func browserSessionLogMessage(message string) bool {
	for _, prefix := range []string{"auth: failed to load session", "auth: failed to revoke session"} {
		if strings.HasPrefix(message, prefix) && (len(message) == len(prefix) || strings.ContainsRune(" ;:.,\t\r\n", rune(message[len(prefix)]))) {
			return true
		}
	}
	return false
}

func browserSessionRouteCredential(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := range value {
		c := value[index]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (r *browserSessionLogRedactor) collect(value any, browserContext bool) {
	switch value := value.(type) {
	case map[string]any:
		message, _ := value["message"].(string)
		browserContext = browserContext || browserSessionLogMessage(message)
		if browserContext {
			credential, _ := value["session_id"].(string)
			r.add(credential)
		}
		for key, item := range value {
			r.collect(key, false)
			r.collect(item, false)
		}
	case []any:
		for _, item := range value {
			r.collect(item, browserContext)
		}
	case string:
		for _, match := range browserSessionRoute.FindAllStringSubmatch(value, -1) {
			// Rejected selectors and public handles still get route normalization,
			// but must not become global text replacements like the character "a".
			r.add(match[1])
		}
		if decoded, ok := decodeBrowserSessionLogJSON(value); ok {
			r.collect(decoded, browserContext)
			return
		}
		r.collectFallback(value, browserContext)
	}
}

type browserSessionLogValue struct {
	start int
	end   int
	value string
}

func browserSessionFallbackValues(text string, browserContext bool) []browserSessionLogValue {
	browserContext = browserContext || browserSessionLogMessage(text)
	var values []browserSessionLogValue
	for _, match := range browserSessionQuotedField.FindAllStringSubmatchIndex(text, -1) {
		var key string
		if json.Unmarshal([]byte(text[match[2]:match[3]]), &key) != nil || (key != "message" && key != "session_id") {
			continue
		}
		var value string
		// Close only the temporary decode input, not the retained diagnostic.
		if json.Unmarshal([]byte(text[match[4]:match[5]]+`"`), &value) != nil {
			continue
		}
		if key == "message" {
			browserContext = browserContext || browserSessionLogMessage(value)
			continue
		}
		values = append(values, browserSessionLogValue{start: match[4] + 1, end: match[5], value: value})
	}
	if !browserContext {
		return nil
	}
	for _, match := range browserSessionAssignment.FindAllStringSubmatchIndex(text, -1) {
		for index := 2; index < len(match); index += 2 {
			if match[index] >= 0 {
				values = append(values, browserSessionLogValue{start: match[index], end: match[index+1], value: text[match[index]:match[index+1]]})
				break
			}
		}
	}
	return values
}

func (r *browserSessionLogRedactor) collectFallback(text string, browserContext bool) {
	for _, value := range browserSessionFallbackValues(text, browserContext) {
		r.add(value.value)
		encoded := text[value.start:value.end]
		if encoded != value.value {
			r.add(encoded)
		}
	}
}

func redactBrowserSessionFallback(text string) string {
	values := browserSessionFallbackValues(text, false)
	slices.SortFunc(values, func(a, b browserSessionLogValue) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end
	})
	var output strings.Builder
	end := 0
	for _, value := range values {
		if value.start < end || value.start == value.end || text[value.start:value.end] == browserSessionRedacted {
			continue
		}
		output.WriteString(text[end:value.start])
		output.WriteString(browserSessionRedacted)
		end = value.end
	}
	if end == 0 {
		return text
	}
	output.WriteString(text[end:])
	return output.String()
}

func (r *browserSessionLogRedactor) text(text string) string {
	if decoded, ok := decodeBrowserSessionLogJSON(text); ok {
		safe, changed := r.value(decoded, false)
		if changed {
			if data, err := json.Marshal(safe); err == nil {
				return string(data)
			}
		}
	}
	text = redactBrowserSessionFallback(text)
	text = RedactBrowserSessionPath(text)
	for _, credential := range r.credentials {
		text = strings.ReplaceAll(text, credential, browserSessionRedacted)
	}
	return text
}

func (r *browserSessionLogRedactor) value(value any, browserContext bool) (any, bool) {
	switch value := value.(type) {
	case string:
		safe := r.text(value)
		return safe, safe != value
	case map[string]any:
		message, _ := value["message"].(string)
		browserContext = browserContext || browserSessionLogMessage(message)
		var output map[string]any
		for key, item := range value {
			safeKey := r.text(key)
			var safe any
			var changed bool
			if original, ok := item.(string); browserContext && key == "session_id" && ok && original != "" {
				safe = browserSessionRedacted
				changed = original != browserSessionRedacted
			} else {
				safe, changed = r.value(item, false)
			}
			if safeKey == key && !changed {
				continue
			}
			if output == nil {
				output = make(map[string]any, len(value))
				for originalKey, originalValue := range value {
					output[originalKey] = originalValue
				}
			}
			if safeKey != key {
				delete(output, key)
			}
			output[safeKey] = safe
		}
		if output != nil {
			return output, true
		}
	case []any:
		var output []any
		for index, item := range value {
			safe, changed := r.value(item, false)
			if !changed {
				continue
			}
			if output == nil {
				output = append([]any(nil), value...)
			}
			output[index] = safe
		}
		if output != nil {
			return output, true
		}
	}
	return value, false
}

func decodeBrowserSessionLogJSON(text string) (any, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.ContainsRune("{[\"", rune(trimmed[0])) {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, false
	}
	return value, true
}
