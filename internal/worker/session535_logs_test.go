package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/logbuf"
)

const (
	session535BrowserBearer  = "a535a535a535a535" + "a535a535a535a535" + "a535a535a535a535" + "a535a535a535a535"
	session535SDKSessionID   = "b535b535b535b535" + "b535b535b535b535" + "b535b535b535b535" + "b535b535b535b535"
	session535AgentSessionID = "c535c535c535c535" + "c535c535c535c535" + "c535c535c535c535" + "c535c535c535c535"
	session535LogTimestamp   = int64(1700000535)
)

type session535LogFixture struct {
	name            string
	raw             string
	level           string
	message         string
	messageParts    []string
	messageHasRoute bool
	fieldsHaveRoute bool
	wantFields      map[string]any
}

func TestSession535LogsSnapshotRedactsAlreadyBufferedBrowserBearers(t *testing.T) {
	for _, fixture := range session535BrowserLogFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			buffer := session535NewLegacyLogBuffer(t, fixture)
			entries := session535ReadLogSnapshot(t, buffer)
			if len(entries) != 1 {
				t.Fatalf("snapshot lost the retained diagnostic: got %d records", len(entries))
			}
			session535AssertLogDiagnostic(t, entries[0], fixture)
		})
	}
}

func TestSession535LogsSnapshotRedactsNewBrowserRecords(t *testing.T) {
	for _, fixture := range session535BrowserLogFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			buffer := logbuf.NewRingBuffer(1)
			session535WriteLog(t, buffer, fixture)
			entries := session535ReadLogSnapshot(t, buffer)
			if len(entries) != 1 {
				t.Fatalf("snapshot lost the new diagnostic: got %d records", len(entries))
			}
			session535AssertLogDiagnostic(t, entries[0], fixture)
		})
	}
}

func TestSession535LogsSSERedactsNewBrowserRecords(t *testing.T) {
	for _, fixture := range session535BrowserLogFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			buffer := logbuf.NewRingBuffer(1)
			entries := session535ReadLogStream(t, buffer, 1, func() {
				session535WriteLog(t, buffer, fixture)
			})
			if len(entries) != 1 {
				t.Fatalf("SSE lost the new diagnostic: got %d records", len(entries))
			}
			session535AssertLogDiagnostic(t, entries[0], fixture)
		})
	}
}

func TestSession535LogsSSERedactsAlreadyQueuedBrowserRecords(t *testing.T) {
	for _, fixture := range session535BrowserLogFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			buffer := logbuf.NewRingBuffer(1)
			entries := session535ReadLogStream(t, buffer, 1, func() {
				session535QueueLegacyLog(t, buffer, session535LegacyLogEntry(t, fixture))
			})
			if len(entries) != 1 {
				t.Fatalf("SSE lost the queued diagnostic: got %d records", len(entries))
			}
			session535AssertLogDiagnostic(t, entries[0], fixture)
		})
	}
}

func TestSession535LogsPreserveSDKAndAgentSessionCorrelations(t *testing.T) {
	for _, reader := range []string{"snapshot", "snapshot_legacy", "sse", "sse_queued"} {
		t.Run(reader, func(t *testing.T) {
			for _, fixture := range session535CorrelationLogFixtures(t) {
				t.Run(fixture.name, func(t *testing.T) {
					buffer := logbuf.NewRingBuffer(1)
					var entries []logbuf.LogEntry
					switch reader {
					case "snapshot":
						session535WriteLog(t, buffer, fixture)
						entries = session535ReadLogSnapshot(t, buffer)
					case "snapshot_legacy":
						buffer = session535NewLegacyLogBuffer(t, fixture)
						entries = session535ReadLogSnapshot(t, buffer)
					case "sse":
						entries = session535ReadLogStream(t, buffer, 1, func() {
							session535WriteLog(t, buffer, fixture)
						})
					case "sse_queued":
						entries = session535ReadLogStream(t, buffer, 1, func() {
							session535QueueLegacyLog(t, buffer, session535LegacyLogEntry(t, fixture))
						})
					}
					if len(entries) != 1 {
						t.Fatalf("reader lost the session diagnostic: got %d records", len(entries))
					}
					session535AssertLogDiagnostic(t, entries[0], fixture)
				})
			}
		})
	}
}

func TestSession535LogsDebugRequestLoggerRedactsBrowserRevokeURL(t *testing.T) {
	captured := session535CaptureProducerLogs(t)
	path := "/api/access/sessions/" + session535BrowserBearer + "/revoke"
	diagnostic := "synthetic storage failure"
	called := false
	handler := debugRequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != path {
			t.Error("request logger changed the path delivered to the real handler")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(diagnostic))
	}))
	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.RemoteAddr = "192.0.2.53:535"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !called || response.Code != http.StatusServiceUnavailable {
		t.Fatal("request logger did not preserve the handler's response status")
	}
	if request.URL.Path != path {
		t.Error("request logger changed the original routing path")
	}
	records := session535ReadProducerRecords(t, captured, 1)
	session535AssertProducerDiagnostic(t, records[0], "debug", "HTTP request")
	session535AssertHTTPRequestDiagnostic(t, records[0], http.StatusServiceUnavailable, len(diagnostic))
}

func TestSession535LogsTokenAuthWarnDenialsRedactBrowserRevokeURL(t *testing.T) {
	for _, denial := range []struct {
		name       string
		message    string
		bearer     bool
		unwired    bool
		wantError  bool
		wantRemote bool
	}{
		{
			name: "unauthenticated", message: "auth: rejected unauthenticated request", wantRemote: true,
		},
		{
			name: "validator_not_wired", message: "auth: validator not yet wired, rejecting bearer",
			bearer: true, unwired: true,
		},
		{
			name: "bearer_rejected", message: "auth: bearer rejected",
			bearer: true, wantError: true, wantRemote: true,
		},
	} {
		t.Run(denial.name, func(t *testing.T) {
			t.Setenv("ENGRAM_AUTH_DISABLED", "false")
			auth, err := NewTokenAuth("session535-synthetic-master-token")
			if err != nil {
				t.Fatal("could not construct the in-memory token-auth fixture")
			}
			if denial.unwired {
				auth.SetValidator(nil)
			}
			captured := session535CaptureProducerLogs(t)
			called := false
			handler := debugRequestLogger(auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})))
			path := "/api/access/sessions/" + session535BrowserBearer + "/revoke"
			request := httptest.NewRequest(http.MethodPost, path, nil)
			request.RemoteAddr = "192.0.2.53:535"
			if denial.bearer {
				// A browser session ID fails the validator's keycard shape gate
				// without accessing a token store or invoking bcrypt.
				request.Header.Set("Authorization", "Bearer "+session535BrowserBearer)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if called || response.Code != http.StatusUnauthorized {
				t.Fatal("token-auth denial did not preserve the unauthorized response")
			}
			if request.URL.Path != path {
				t.Error("token-auth logging changed the original routing path")
			}
			records := session535ReadProducerRecords(t, captured, 2)
			session535AssertProducerDiagnostic(t, records[0], "warn", denial.message)
			session535AssertProducerDiagnostic(t, records[1], "debug", "HTTP request")
			session535AssertHTTPRequestDiagnostic(t, records[1], http.StatusUnauthorized, response.Body.Len())
			if denial.wantRemote && records[0]["remote_addr"] != request.RemoteAddr {
				t.Error("auth denial lost its useful remote-address diagnostic")
			}
			if denial.wantError {
				if message, ok := records[0]["error"].(string); !ok || message == "" {
					t.Error("auth denial suppressed the validation error diagnostic")
				}
			}
		})
	}
}

func TestSession535LogsSnapshotFiltersSanitizedBrowserRecords(t *testing.T) {
	for _, fixture := range session535QueryLogFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			for _, query := range session535LogQueries(fixture) {
				t.Run(query.name, func(t *testing.T) {
					buffer := session535NewLegacyLogBuffer(t, fixture)
					entries := session535ReadLogSnapshotQuery(t, buffer, query.text)
					if len(entries) != query.wantRecords {
						t.Fatalf("snapshot query matched %d records, want %d", len(entries), query.wantRecords)
					}
					if len(entries) == 1 {
						session535AssertLogDiagnostic(t, entries[0], fixture)
					}
				})
			}
		})
	}
}

func TestSession535LogsSSEFiltersSanitizedBrowserRecords(t *testing.T) {
	for _, fixture := range session535QueryLogFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			for _, query := range session535LogQueries(fixture) {
				t.Run(query.name, func(t *testing.T) {
					buffer := logbuf.NewRingBuffer(1)
					entries := session535ReadLogStreamQuery(t, buffer, query.text, 0, func() {
						session535QueueLegacyLog(t, buffer, session535LegacyLogEntry(t, fixture))
						session535CloseLegacyLogStream(t, buffer)
					})
					if len(entries) != query.wantRecords {
						t.Fatalf("SSE query matched %d records, want %d", len(entries), query.wantRecords)
					}
					if len(entries) == 1 {
						session535AssertLogDiagnostic(t, entries[0], fixture)
					}
				})
			}
		})
	}
}

func session535BrowserLogFixtures(t *testing.T) []session535LogFixture {
	t.Helper()
	path := "/api/access/sessions/" + session535BrowserBearer + "/revoke"
	var fixtures []session535LogFixture
	for _, level := range []string{"debug", "warn", "error"} {
		fixtures = append(fixtures,
			session535MakeLogFixture(t, level+"/request_path", level, "HTTP request", map[string]any{
				"path": path, "method": "POST", "status": 503, "bytes": 47,
				"duration": 12.5, "from": "192.0.2.53:535",
			}, []string{"path"}, nil, false),
			session535MakeLogFixture(t, level+"/auth_load_session_field", level,
				"auth: failed to load session", map[string]any{"session_id": session535BrowserBearer},
				[]string{"session_id"}, nil, false),
			session535MakeLogFixture(t, level+"/auth_revoke_session_field", level,
				"auth: failed to revoke session", map[string]any{"session_id": session535BrowserBearer},
				[]string{"session_id"}, nil, false),
			session535MakeLogFixture(t, level+"/request_path_in_message", level,
				"HTTP request POST "+path+"; diagnostic retained", nil, nil,
				[]string{"HTTP request POST ", "; diagnostic retained"}, false),
			session535MakeLogFixture(t, level+"/browser_session_in_message", level,
				"auth: failed to revoke session; session_id="+session535BrowserBearer+"; diagnostic retained",
				nil, nil, []string{"auth: failed to revoke session", "; diagnostic retained"}, false),
			session535MakeLogFixture(t, level+"/escaped_structured_record", level,
				"auth: failed to revoke session", map[string]any{
					"session_id": session535BrowserBearer, "path": path, "method": "POST", "status": 503,
				}, []string{"session_id", "path"}, nil, true),
		)
	}
	return fixtures
}

func session535CorrelationLogFixtures(t *testing.T) []session535LogFixture {
	t.Helper()
	return []session535LogFixture{
		session535MakeLogFixture(t, "sdk", "warn", "backfill: failed to create SDK session", map[string]any{
			"session_id": session535SDKSessionID, "sdk_session_id": session535SDKSessionID,
			"project": "session535-fixture",
		}, nil, nil, false),
		session535MakeLogFixture(t, "agent", "error", "session_end: failed to persist transcript", map[string]any{
			"session_id": session535AgentSessionID, "source_session_id": session535AgentSessionID,
			"principal": "agent:session535-fixture", "project": "session535-fixture",
		}, nil, nil, false),
	}
}

func session535MakeLogFixture(t *testing.T, name, level, message string, fields map[string]any, secretFields, messageParts []string, escaped bool) session535LogFixture {
	t.Helper()
	record := map[string]any{
		"time": session535LogTimestamp, "level": level, "message": message,
		"request_id": "session535-diagnostic", "error": "synthetic storage failure; retry retained",
	}
	for key, value := range fields {
		record[key] = value
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal("could not encode synthetic log fixture")
	}
	var wantFields map[string]any
	if json.Unmarshal(data, &wantFields) != nil {
		t.Fatal("could not decode synthetic diagnostic fields")
	}
	for _, key := range append([]string{"time", "level", "message"}, secretFields...) {
		delete(wantFields, key)
	}
	raw := string(data)
	if escaped {
		raw = strings.ReplaceAll(raw, "/", `\/`)
		raw = strings.ReplaceAll(raw, "session_id", `session\u005fid`)
		raw = strings.ReplaceAll(raw, session535BrowserBearer, strings.ReplaceAll(session535BrowserBearer, "5", `\u0035`))
	}
	_, fieldsHaveRoute := fields["path"]
	return session535LogFixture{
		name: name, raw: raw, level: level, message: message, messageParts: messageParts,
		messageHasRoute: strings.Contains(message, "/api/access/sessions/"),
		fieldsHaveRoute: fieldsHaveRoute, wantFields: wantFields,
	}
}

func session535WriteLog(t *testing.T, buffer *logbuf.RingBuffer, fixture session535LogFixture) {
	t.Helper()
	data := []byte(fixture.raw + "\n")
	written, err := buffer.Write(data)
	if err != nil || written != len(data) {
		t.Fatal("could not write synthetic log fixture")
	}
}

func session535LegacyLogEntry(t *testing.T, fixture session535LogFixture) logbuf.LogEntry {
	t.Helper()
	var fields map[string]any
	if json.Unmarshal([]byte(fixture.raw), &fields) != nil {
		t.Fatal("synthetic legacy log is not structured JSON")
	}
	for _, key := range []string{"time", "level", "message"} {
		delete(fields, key)
	}
	return logbuf.LogEntry{
		Timestamp: session535LogTimestamp, Level: fixture.level, Message: fixture.message,
		Fields: fields, Raw: fixture.raw,
	}
}

// Public Write alone cannot model records retained before a write-time sanitizer.
// These fixtures only access fresh, privately constructed buffers. The observed
// entries/subscribers shapes are checked, and their mutex protects each injection.
func session535NewLegacyLogBuffer(t *testing.T, fixture session535LogFixture) *logbuf.RingBuffer {
	t.Helper()
	buffer := logbuf.NewRingBuffer(1)
	if _, err := buffer.Write([]byte(`{"level":"info","message":"legacy slot"}`)); err != nil {
		t.Fatal("could not prime synthetic legacy buffer")
	}
	mu := session535BufferMutex(t, buffer)
	mu.Lock()
	defer mu.Unlock()
	entries := session535BufferField(t, buffer, "entries", reflect.TypeOf([]logbuf.LogEntry{})).Interface().([]logbuf.LogEntry)
	subscribers := session535BufferField(t, buffer, "subscribers", reflect.TypeOf([]chan logbuf.LogEntry{})).Interface().([]chan logbuf.LogEntry)
	if len(entries) != 1 || cap(entries) != 1 || len(subscribers) != 0 {
		t.Fatal("synthetic legacy buffer shape or capacity changed")
	}
	entries[0] = session535LegacyLogEntry(t, fixture)
	return buffer
}

func session535QueueLegacyLog(t *testing.T, buffer *logbuf.RingBuffer, entry logbuf.LogEntry) {
	t.Helper()
	mu := session535BufferMutex(t, buffer)
	mu.Lock()
	defer mu.Unlock()
	subscribers := session535BufferField(t, buffer, "subscribers", reflect.TypeOf([]chan logbuf.LogEntry{})).Interface().([]chan logbuf.LogEntry)
	if len(subscribers) != 1 || subscribers[0] == nil || cap(subscribers[0]) == 0 || len(subscribers[0]) != 0 {
		t.Fatal("synthetic stream does not have one empty buffered subscriber")
	}
	select {
	case subscribers[0] <- entry:
	default:
		t.Fatal("could not queue synthetic legacy log")
	}
}

// Closing only this fixture's detached subscriber makes a zero-event result
// observable by draining the queue, not by waiting for an arbitrary timeout.
func session535CloseLegacyLogStream(t *testing.T, buffer *logbuf.RingBuffer) {
	t.Helper()
	mu := session535BufferMutex(t, buffer)
	mu.Lock()
	defer mu.Unlock()
	field := session535BufferField(t, buffer, "subscribers", reflect.TypeOf([]chan logbuf.LogEntry{}))
	subscribers := field.Interface().([]chan logbuf.LogEntry)
	if len(subscribers) != 1 || subscribers[0] == nil || cap(subscribers[0]) == 0 || len(subscribers[0]) != 1 {
		t.Fatal("synthetic finite stream does not have one queued diagnostic")
	}
	channel := subscribers[0]
	// The handler's deferred unsubscribe must not close the same channel again.
	field.Set(reflect.Zero(field.Type()))
	close(channel)
}

func session535BufferMutex(t *testing.T, buffer *logbuf.RingBuffer) *sync.Mutex {
	t.Helper()
	return session535BufferField(t, buffer, "mu", reflect.TypeOf(sync.Mutex{})).Addr().Interface().(*sync.Mutex)
}

func session535BufferField(t *testing.T, buffer *logbuf.RingBuffer, name string, want reflect.Type) reflect.Value {
	t.Helper()
	field := reflect.ValueOf(buffer).Elem().FieldByName(name)
	if !field.IsValid() || field.Type() != want || !field.CanAddr() {
		t.Fatal("synthetic RingBuffer fixture no longer matches the observed storage shape")
	}
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
}

func session535ReadLogSnapshot(t *testing.T, buffer *logbuf.RingBuffer) []logbuf.LogEntry {
	t.Helper()
	return session535ReadLogSnapshotQuery(t, buffer, "")
}

func session535ReadLogSnapshotQuery(t *testing.T, buffer *logbuf.RingBuffer, query string) []logbuf.LogEntry {
	t.Helper()
	service := &Service{logBuffer: buffer}
	response := httptest.NewRecorder()
	values := url.Values{"lines": {"100"}, "query": {query}}
	service.handleGetLogs(response, httptest.NewRequest(http.MethodGet, "/api/logs?"+values.Encode(), nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatal("snapshot did not return the expected JSON response")
	}
	if session535ContainsBrowserBearer(response.Body.String()) {
		t.Error("JSON response exposes the synthetic browser bearer")
	}
	var entries []logbuf.LogEntry
	if json.Unmarshal(response.Body.Bytes(), &entries) != nil {
		t.Fatal("snapshot response is not a log-entry array")
	}
	return entries
}

type session535SSEWriter struct {
	*httptest.ResponseRecorder
	onConnected func()
	cancel      context.CancelFunc
	wantEvents  int
	events      int
	offset      int
}

func (writer *session535SSEWriter) Flush() {
	chunk := writer.Body.String()[writer.offset:]
	writer.offset = writer.Body.Len()
	writer.ResponseRecorder.Flush()
	writer.events += strings.Count("\n"+chunk, "\ndata: ")
	if writer.wantEvents > 0 && writer.events >= writer.wantEvents {
		writer.cancel()
	}
	if writer.onConnected != nil {
		produce := writer.onConnected
		writer.onConnected = nil
		produce()
	}
}

func session535ReadLogStream(t *testing.T, buffer *logbuf.RingBuffer, count int, produce func()) []logbuf.LogEntry {
	t.Helper()
	return session535ReadLogStreamQuery(t, buffer, "", count, produce)
}

func session535ReadLogStreamQuery(t *testing.T, buffer *logbuf.RingBuffer, query string, count int, produce func()) []logbuf.LogEntry {
	t.Helper()
	// The initial connected-frame flush happens after Subscribe. Produce then,
	// synchronously, before the handler consumes its queue; no sleep or server.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response := &session535SSEWriter{
		ResponseRecorder: httptest.NewRecorder(), onConnected: produce, cancel: cancel, wantEvents: count,
	}
	service := &Service{logBuffer: buffer}
	values := url.Values{"follow": {"true"}, "query": {query}}
	request := httptest.NewRequest(http.MethodGet, "/api/logs?"+values.Encode(), nil).WithContext(ctx)
	service.handleGetLogs(response, request)
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("SSE did not deliver the synthetic diagnostic before the failure deadline")
	}
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatal("follow request did not return the expected SSE response")
	}
	var entries []logbuf.LogEntry
	scanner := bufio.NewScanner(strings.NewReader(response.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if session535ContainsBrowserBearer(data) {
			t.Error("SSE event exposes the synthetic browser bearer")
		}
		var entry logbuf.LogEntry
		if json.Unmarshal([]byte(data), &entry) != nil {
			t.Fatal("SSE event is not a structured log entry")
		}
		entries = append(entries, entry)
	}
	if scanner.Err() != nil {
		t.Fatal("could not read synthetic SSE frames")
	}
	return entries
}

func session535AssertLogDiagnostic(t *testing.T, entry logbuf.LogEntry, fixture session535LogFixture) {
	t.Helper()
	if entry.Timestamp != session535LogTimestamp || entry.Level != fixture.level {
		t.Error("reader changed the diagnostic timestamp or severity")
	}
	if session535ContainsBrowserBearer(entry.Message) {
		t.Error("log message exposes the synthetic browser bearer")
	}
	if session535ContainsBrowserBearer(entry.Fields) {
		t.Error("structured fields expose the synthetic browser bearer")
	}
	if session535ContainsBrowserBearer(entry.Raw) {
		t.Error("raw log record exposes the synthetic browser bearer")
	}
	var raw map[string]any
	if json.Unmarshal([]byte(entry.Raw), &raw) != nil {
		t.Fatal("reader discarded the structured raw diagnostic")
	}
	rawMessage, ok := raw["message"].(string)
	if !ok {
		t.Fatal("raw diagnostic lost its message")
	}
	for _, message := range []string{entry.Message, rawMessage} {
		if fixture.messageParts == nil {
			if message != fixture.message {
				t.Error("reader changed a harmless diagnostic message")
			}
		} else {
			for _, part := range fixture.messageParts {
				if !strings.Contains(message, part) {
					t.Error("reader suppressed useful diagnostic message text")
				}
			}
		}
		if fixture.messageHasRoute && (!strings.Contains(message, "/api/access/sessions/") || !strings.Contains(message, "/revoke")) {
			t.Error("reader suppressed the browser revoke route in the diagnostic message")
		}
	}
	for key, want := range fixture.wantFields {
		if !reflect.DeepEqual(entry.Fields[key], want) {
			t.Errorf("reader changed useful structured diagnostic field %s", key)
		}
		if !reflect.DeepEqual(raw[key], want) {
			t.Errorf("reader changed useful raw diagnostic field %s", key)
		}
	}
	if fixture.fieldsHaveRoute {
		for _, fields := range []map[string]any{entry.Fields, raw} {
			path, ok := fields["path"].(string)
			if !ok || !strings.HasPrefix(path, "/api/access/sessions/") || !strings.HasSuffix(path, "/revoke") {
				t.Error("reader suppressed the useful browser revoke route")
			}
		}
	}
}

func session535ContainsBrowserBearer(value any) bool {
	switch value := value.(type) {
	case string:
		if strings.Contains(value, session535BrowserBearer) {
			return true
		}
		// Raw is itself JSON inside the endpoint's JSON. Decode each supported
		// layer so escaped slashes, keys and Unicode cannot hide a bearer.
		var decoded any
		if json.Unmarshal([]byte(value), &decoded) == nil {
			return session535ContainsBrowserBearer(decoded)
		}
	case map[string]any:
		for key, item := range value {
			if session535ContainsBrowserBearer(key) || session535ContainsBrowserBearer(item) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if session535ContainsBrowserBearer(item) {
				return true
			}
		}
	}
	return false
}

func session535CaptureProducerLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var captured bytes.Buffer
	originalLogger := log.Logger
	originalLevel := zerolog.GlobalLevel()
	t.Cleanup(func() {
		log.Logger = originalLogger
		zerolog.SetGlobalLevel(originalLevel)
	})
	// These tests are deliberately nonparallel: only the synchronous request
	// uses this private logger, and Cleanup restores both global settings.
	log.Logger = zerolog.New(&captured).Level(zerolog.DebugLevel).With().
		Str("session_id", session535SDKSessionID).
		Str("sdk_session_id", session535SDKSessionID).
		Str("source_session_id", session535AgentSessionID).
		Str("request_id", "session535-producer-diagnostic").Logger()
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	return &captured
}

func session535ReadProducerRecords(t *testing.T, captured *bytes.Buffer, count int) []map[string]any {
	t.Helper()
	var records []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(captured.Bytes()))
	for scanner.Scan() {
		if session535ContainsBrowserBearer(scanner.Text()) {
			t.Error("actual log producer exposes the synthetic browser bearer")
		}
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			t.Fatal("actual producer did not emit structured JSON logs")
		}
		records = append(records, record)
	}
	if scanner.Err() != nil {
		t.Fatal("could not read private producer-log capture")
	}
	if len(records) != count {
		t.Fatalf("actual producer emitted %d records, want %d", len(records), count)
	}
	return records
}

func session535AssertProducerDiagnostic(t *testing.T, record map[string]any, level, message string) {
	t.Helper()
	if record["level"] != level || record["message"] != message {
		t.Error("actual producer changed the useful diagnostic severity or message")
	}
	path, ok := record["path"].(string)
	if !ok || !strings.HasPrefix(path, "/api/access/sessions/") || !strings.HasSuffix(path, "/revoke") {
		t.Error("actual producer suppressed the useful browser revoke route")
	}
	for key, want := range map[string]string{
		"session_id": session535SDKSessionID, "sdk_session_id": session535SDKSessionID,
		"source_session_id": session535AgentSessionID, "request_id": "session535-producer-diagnostic",
	} {
		if record[key] != want {
			t.Errorf("actual producer changed useful correlation field %s", key)
		}
	}
}

func session535AssertHTTPRequestDiagnostic(t *testing.T, record map[string]any, status, size int) {
	t.Helper()
	if record["method"] != http.MethodPost || record["status"] != float64(status) || record["bytes"] != float64(size) {
		t.Error("HTTP access log lost useful method, status, or response-size diagnostics")
	}
	if record["from"] != "192.0.2.53:535" {
		t.Error("HTTP access log lost its useful remote-address diagnostic")
	}
	if duration, ok := record["duration"].(float64); !ok || duration < 0 {
		t.Error("HTTP access log lost its useful duration diagnostic")
	}
}

func session535QueryLogFixtures(t *testing.T) []session535LogFixture {
	t.Helper()
	var fixtures []session535LogFixture
	for _, fixture := range session535BrowserLogFixtures(t) {
		switch fixture.name {
		case "debug/request_path", "debug/auth_revoke_session_field", "debug/request_path_in_message":
			fixtures = append(fixtures, fixture)
		}
	}
	if len(fixtures) != 3 {
		t.Fatal("query fixtures must cover retained raw paths, browser session fields, and messages")
	}
	return fixtures
}

type session535LogQuery struct {
	name        string
	text        string
	wantRecords int
}

func session535LogQueries(fixture session535LogFixture) []session535LogQuery {
	queries := []session535LogQuery{
		{name: "secret_middle_fragment", text: session535BrowserBearer[9:25], wantRecords: 0},
		{name: "harmless_diagnostic", text: "synthetic storage failure", wantRecords: 1},
	}
	if fixture.fieldsHaveRoute || fixture.messageHasRoute {
		queries = append(queries,
			session535LogQuery{
				name: "secret_route_prefix", text: "/api/access/sessions/" + session535BrowserBearer[:7], wantRecords: 0,
			},
			session535LogQuery{
				name: "retained_route_case_insensitive", text: "/API/ACCESS/SESSIONS/", wantRecords: 1,
			},
		)
	}
	return queries
}
