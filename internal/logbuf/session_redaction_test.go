package logbuf

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRedactBrowserSessionEntryCopiesNestedFieldsAndIsIdempotent(t *testing.T) {
	credential := strings.Repeat("a535", 16)
	sdk := strings.Repeat("b535", 16)
	agent := strings.Repeat("c535", 16)
	path := "/api/access/sessions/" + credential + "/revoke"
	encoded := `{"path":"` + path + `","diagnostic":"nested retained"}`
	encoded = strings.ReplaceAll(encoded, "/", `\/`)
	encoded = strings.ReplaceAll(encoded, credential, strings.ReplaceAll(credential, "5", `\u0035`))
	fields := map[string]any{
		"path": path, "session_id": sdk, "sdk_session_id": sdk, "source_session_id": agent,
		"encoded": encoded, "harmless_encoded": `{ "diagnostic": "retained exactly" }`,
		"nested": []any{[]any{map[string]any{"error": "storage failure for " + credential, "status": 503, "session_id": sdk, "source_session_id": agent}}},
	}
	raw := `{"time":1700000535,"level":"warn","message":"HTTP request","path":"` + path + `","large":9007199254740993}`
	raw = strings.ReplaceAll(raw, "/", `\/`)
	raw = strings.ReplaceAll(raw, credential, strings.ReplaceAll(credential, "5", `\u0035`))
	input := LogEntry{Timestamp: 1700000535, Level: "warn", Message: "HTTP request", Fields: fields, Raw: raw}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	output := RedactBrowserSessionEntry(input)
	after, err := json.Marshal(input)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("projection mutated the retained input")
	}
	if output.Timestamp != input.Timestamp || output.Level != input.Level || output.Message != input.Message {
		t.Fatal("projection changed harmless diagnostic metadata")
	}
	for _, key := range []string{"session_id", "sdk_session_id", "source_session_id", "harmless_encoded"} {
		if output.Fields[key] != input.Fields[key] {
			t.Fatalf("projection changed harmless field %s", key)
		}
	}
	if output.Fields["path"] != "/api/access/sessions/[REDACTED]/revoke" {
		t.Fatal("projection did not preserve a safe route")
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(output.Fields["encoded"].(string)), &decoded) != nil || decoded["path"] != output.Fields["path"] || decoded["diagnostic"] != "nested retained" {
		t.Fatal("projection did not sanitize the escaped nested JSON string")
	}
	var structured map[string]json.RawMessage
	if json.Unmarshal([]byte(output.Raw), &structured) != nil || string(structured["large"]) != "9007199254740993" {
		t.Fatal("projection changed raw diagnostic number precision")
	}
	if json.Unmarshal(structured["path"], new(string)) != nil || strings.Contains(output.Raw, credential) || strings.Contains(output.Raw, `\u0035`) {
		t.Fatal("projection retained an escaped browser credential")
	}
	if !reflect.DeepEqual(output, RedactBrowserSessionEntry(output)) {
		t.Fatal("projection is not idempotent")
	}

	projectedNested := output.Fields["nested"].([]any)[0].([]any)[0].(map[string]any)
	if projectedNested["error"] != "storage failure for [REDACTED]" || projectedNested["status"] != 503 || projectedNested["session_id"] != sdk || projectedNested["source_session_id"] != agent {
		t.Fatal("projection lost nested diagnostics")
	}
	projectedNested["error"] = "consumer mutation"
	if fields["nested"].([]any)[0].([]any)[0].(map[string]any)["error"] != "storage failure for "+credential {
		t.Fatal("projected nested map still aliases the changed input branch")
	}
}

func TestRedactBrowserSessionEntryProjectsPlaintextLegacyMessages(t *testing.T) {
	credential := strings.Repeat("a535", 16)
	sdk := strings.Repeat("b535", 16)
	message := "auth: failed to revoke session; session_id=" + credential + "; diagnostic retained"
	input := LogEntry{
		Timestamp: 1700000535, Level: "error", Message: message,
		Fields: map[string]any{
			"session_id": credential, "error": "synthetic storage failure",
			"nested": map[string]any{"session_id": sdk, "sdk_session_id": sdk},
		},
		Raw: message,
	}
	output := RedactBrowserSessionEntry(input)
	if strings.Contains(output.Message, credential) || strings.Contains(output.Raw, credential) || output.Fields["session_id"] != browserSessionRedacted {
		t.Fatal("plaintext projection retained a browser credential")
	}
	if !strings.Contains(output.Message, "diagnostic retained") || output.Fields["error"] != "synthetic storage failure" {
		t.Fatal("plaintext projection suppressed useful diagnostics")
	}
	if output.Fields["nested"].(map[string]any)["session_id"] != sdk {
		t.Fatal("browser error context redacted an unrelated nested SDK session")
	}
	if input.Fields["session_id"] != credential || input.Raw != message || input.Message != message {
		t.Fatal("plaintext projection changed the retained input")
	}
	if !reflect.DeepEqual(output, RedactBrowserSessionEntry(output)) {
		t.Fatal("plaintext projection is not idempotent")
	}
}

func TestRedactBrowserSessionErrorRetainsDiagnosticsAndIsIdempotent(t *testing.T) {
	credential := strings.Repeat("a535", 16)
	path := "/api/access/sessions/" + credential + "/revoke"
	original := errors.New("storage failure for " + credential + " at " + path + "; retry retained")
	safe := RedactBrowserSessionError(original, path, credential, "")
	if strings.Contains(safe, credential) || !strings.Contains(safe, "storage failure") || !strings.Contains(safe, "; retry retained") || !strings.Contains(safe, "/api/access/sessions/[REDACTED]/revoke") {
		t.Fatal("error projection leaked a credential or suppressed useful diagnostics")
	}
	if RedactBrowserSessionError(errors.New(safe), path, credential) != safe {
		t.Fatal("error projection is not idempotent")
	}
	if RedactBrowserSessionError(nil, path, credential) != "" || !strings.Contains(original.Error(), credential) {
		t.Fatal("error projection changed the original error or nil semantics")
	}
}

func TestRedactBrowserSessionShortRouteSelectorPreservesDiagnostics(t *testing.T) {
	const path = "/api/access/sessions/a/revoke"
	const safePath = "/api/access/sessions/[REDACTED]/revoke"
	const sdk = "sdk-session-alpha-correlation"
	const agent = "agent-session-alpha-correlation"
	const diagnostic = "database unavailable; diagnostic retained"

	t.Run("entry", func(t *testing.T) {
		fields := map[string]any{
			"path": path, "session_id": sdk, "sdk_session_id": sdk,
			"source_session_id": agent, "error": diagnostic, "status": 503,
		}
		record := map[string]any{"time": 1700000535, "level": "warn", "message": diagnostic}
		for key, value := range fields {
			record[key] = value
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		input := LogEntry{Timestamp: 1700000535, Level: "warn", Message: diagnostic, Fields: fields, Raw: string(raw)}
		output := RedactBrowserSessionEntry(input)
		if output.Message != diagnostic || output.Timestamp != input.Timestamp || output.Level != input.Level {
			t.Error("short rejected selector changed harmless diagnostic metadata")
		}
		for _, key := range []string{"session_id", "sdk_session_id", "source_session_id", "error", "status"} {
			if output.Fields[key] != fields[key] {
				t.Errorf("short rejected selector changed unrelated field %s", key)
			}
		}
		if output.Fields["path"] != safePath {
			t.Error("short rejected selector was not normalized in the logging path")
		}
		var projectedRaw map[string]any
		if json.Unmarshal([]byte(output.Raw), &projectedRaw) != nil {
			t.Fatal("short-selector projection lost structured raw diagnostics")
		}
		for key, want := range map[string]any{
			"path": safePath, "message": diagnostic, "session_id": sdk,
			"sdk_session_id": sdk, "source_session_id": agent, "error": diagnostic, "status": float64(503),
		} {
			if projectedRaw[key] != want {
				t.Errorf("short rejected selector changed raw diagnostic field %s", key)
			}
		}
		if input.Fields["path"] != path || input.Message != diagnostic || input.Raw != string(raw) {
			t.Error("short-selector projection mutated the retained input")
		}
	})

	t.Run("error", func(t *testing.T) {
		original := errors.New(diagnostic + " at " + path + "; sdk_session_id=" + sdk)
		want := diagnostic + " at " + safePath + "; sdk_session_id=" + sdk
		if RedactBrowserSessionError(original, path) != want {
			t.Error("short rejected selector changed harmless error text or SDK correlation")
		}
	})

	t.Run("legacy_auth_field", func(t *testing.T) {
		const message = "auth: failed to load session"
		fields := map[string]any{
			"session_id": "a", "sdk_session_id": sdk, "source_session_id": agent,
			"error": diagnostic, "status": 503,
		}
		record := map[string]any{"time": 1700000535, "level": "error", "message": message}
		wantFields := make(map[string]any, len(fields))
		for key, value := range fields {
			record[key] = value
			wantFields[key] = value
		}
		wantFields["session_id"] = browserSessionRedacted
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		input := LogEntry{Timestamp: 1700000535, Level: "error", Message: message, Fields: fields, Raw: string(raw)}
		before, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		output := RedactBrowserSessionEntry(input)
		if output.Message != message || output.Timestamp != input.Timestamp || output.Level != input.Level {
			t.Error("short browser field changed harmless auth diagnostic metadata")
		}
		if !reflect.DeepEqual(output.Fields, wantFields) {
			t.Error("short browser field was not hidden independently of harmless errors and SDK/agent correlation")
		}
		var wantRaw, projectedRaw map[string]any
		if json.Unmarshal(raw, &wantRaw) != nil || json.Unmarshal([]byte(output.Raw), &projectedRaw) != nil {
			t.Fatal("short browser field projection lost structured raw diagnostics")
		}
		wantRaw["session_id"] = browserSessionRedacted
		if !reflect.DeepEqual(projectedRaw, wantRaw) {
			t.Error("raw projection did not hide only the short browser field")
		}
		after, err := json.Marshal(input)
		if err != nil || !bytes.Equal(before, after) {
			t.Error("short browser field projection mutated the retained input")
		}
	})
}

func TestRedactBrowserSessionEntryTruncatedQuotedSessionIDPreservesDiagnostics(t *testing.T) {
	credential := strings.Repeat("d535", 16)
	const sdk = "sdk-session-alpha-correlation"
	const agent = "agent-session-alpha-correlation"
	for _, fixture := range []struct {
		name string
		raw  string
	}{
		{
			name: "truncated_json",
			raw:  `{"level":"error","message":"auth: failed to revoke session","session_id":"` + credential + `","sdk_session_id":"` + sdk + `","error":"synthetic storage failure"`,
		},
		{
			name: "plaintext_quoted_field",
			raw:  `auth: failed to revoke session; "session_id":"` + credential + `"; sdk_session_id="` + sdk + `"; synthetic storage failure`,
		},
		{
			name: "unterminated_session_id_value",
			raw:  `{"level":"error","message":"auth: failed to revoke session","sdk_session_id":"` + sdk + `","error":"synthetic storage failure","session_id":"` + credential,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if json.Valid([]byte(fixture.raw)) {
				t.Fatal("fixture must exercise the non-JSON fallback")
			}
			input := LogEntry{
				Timestamp: 1700000535, Level: "error", Message: fixture.raw, Raw: fixture.raw,
				Fields: map[string]any{"sdk_session_id": sdk, "source_session_id": agent, "error": "synthetic storage failure"},
			}
			output := RedactBrowserSessionEntry(input)
			for _, text := range []string{output.Message, output.Raw} {
				if strings.Contains(text, credential) {
					t.Error("non-JSON quoted session_id exposed the synthetic browser credential")
				}
				for _, retained := range []string{"auth: failed to revoke session", "synthetic storage failure", sdk} {
					if !strings.Contains(text, retained) {
						t.Error("non-JSON projection suppressed harmless diagnostics or SDK correlation")
					}
				}
			}
			if !reflect.DeepEqual(output.Fields, input.Fields) || output.Timestamp != input.Timestamp || output.Level != input.Level {
				t.Error("non-JSON projection changed harmless fields or diagnostic metadata")
			}
			if input.Message != fixture.raw || input.Raw != fixture.raw {
				t.Error("non-JSON projection mutated the retained input")
			}
		})
	}
}

func TestRingBufferSnapshotLatestLevelProjection(t *testing.T) {
	credential := strings.Repeat("a535", 16)
	sdk := strings.Repeat("b535", 16)
	agent := strings.Repeat("c535", 16)
	fixtures := []struct {
		level   string
		message string
	}{
		{"warn", "overwritten diagnostic"},
		{"error", "overwritten error diagnostic"},
		{"warn", "older matching diagnostic"},
		{"error", "auth: failed to revoke session"},
		{"debug", "excluded debug request"},
		{"warn", "retained harmless diagnostic"},
		{"error", "HTTP request"},
		{"info", "excluded info request"},
	}
	buffer := NewRingBuffer(6)
	for seq, fixture := range fixtures {
		record := map[string]any{
			"time": int64(1700000535 + seq), "level": fixture.level, "message": fixture.message,
			"seq": seq, "session_id": sdk, "sdk_session_id": sdk, "source_session_id": agent,
			"error": "synthetic storage failure",
		}
		if seq == 3 {
			record["session_id"] = credential
		}
		if seq == 4 || seq == 6 {
			record["path"] = "/api/access/sessions/" + credential + "/revoke"
		}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if written, err := buffer.Write(data); err != nil || written != len(data) {
			t.Fatal("could not populate the wrapped log buffer")
		}
	}
	buffer.mu.Lock()
	before, err := json.Marshal(buffer.entries)
	buffer.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name  string
		limit int
		level string
		query string
		seqs  []int
	}{
		{"latest_after_level", 3, "warn", "", []int{3, 5, 6}},
		{"all_after_level", 0, "warn", "", []int{2, 3, 5, 6}},
		{"negative_limit", -1, "warn", "", []int{2, 3, 5, 6}},
		{"oversized_limit", 100, "warn", "", []int{2, 3, 5, 6}},
		{"latest_without_level", 2, "", "", []int{6, 7}},
		{"no_level_matches", 3, "fatal", "", nil},
		{"query_before_limit", 1, "warn", "older matching", []int{2}},
		{"safe_route_query", 1, "warn", "/API/ACCESS/SESSIONS/", []int{6}},
		{"secret_query", 3, "warn", credential[9:25], nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			entries := buffer.Snapshot(fixture.limit, fixture.level, fixture.query)
			if entries == nil || len(entries) != len(fixture.seqs) {
				t.Fatal("snapshot changed the matching count or nonnil empty-result contract")
			}
			for index, seq := range fixture.seqs {
				entry := entries[index]
				if entry.Fields["seq"] != float64(seq) || entry.Timestamp != int64(1700000535+seq) || entry.Level != fixtures[seq].level || entry.Message != fixtures[seq].message {
					t.Fatal("snapshot changed chronology, level filtering, latest selection or diagnostic metadata")
				}
				wantSession := sdk
				if seq == 3 {
					wantSession = browserSessionRedacted
				}
				if entry.Fields["session_id"] != wantSession || entry.Fields["sdk_session_id"] != sdk || entry.Fields["source_session_id"] != agent || entry.Fields["error"] != "synthetic storage failure" {
					t.Fatal("snapshot lost safe browser projection or harmless diagnostics and SDK/agent correlation")
				}
				if seq == 6 && entry.Fields["path"] != "/api/access/sessions/[REDACTED]/revoke" {
					t.Fatal("snapshot returned an unsafe browser route")
				}
				var raw map[string]any
				if json.Unmarshal([]byte(entry.Raw), &raw) != nil || raw["message"] != entry.Message {
					t.Fatal("snapshot lost structured raw diagnostics")
				}
				for key, want := range entry.Fields {
					if !reflect.DeepEqual(raw[key], want) {
						t.Fatal("snapshot raw and field projections disagree")
					}
				}
			}
			data, err := json.Marshal(entries)
			if err != nil || bytes.Contains(data, []byte(credential)) {
				t.Fatal("snapshot returned a retained browser credential")
			}
		})
	}
	buffer.mu.Lock()
	after, err := json.Marshal(buffer.entries)
	buffer.mu.Unlock()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("snapshot mutated retained entries or shared fields")
	}
}
