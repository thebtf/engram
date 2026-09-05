package uci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const uciQueryResponseContractsDir = "specs/010-unified-code-intelligence/contracts"

var uciQueryResponseExampleNames = []string{
	"synthetic_hybrid_hit",
	"synthetic_lexical_empty",
	"synthetic_graph",
	"synthetic_after_barrier_timeout",
	"synthetic_verified_supported_host_partial_callback",
	"synthetic_authorized_source_unavailable",
	"synthetic_exposure_recorder_unavailable",
	"synthetic_exposure_idempotency_mismatch",
}

type uciQueryResponseFixture struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

func TestUCIQueryResponseContractExamplesRoundTrip(t *testing.T) {
	for _, fixture := range uciLoadQueryResponseFixtures(t) {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			uciRequireQueryResponseAccepted(t, fixture.Response)
		})
	}
}

func TestUCIQueryResponseContractKeepsResultCoverageAndCompletionDistinct(t *testing.T) {
	fixtures := uciQueryResponseFixturesByName(t)

	for _, tc := range []struct {
		name           string
		fixture        string
		wantStatus     string
		wantRetrieval  string
		wantCoverage   string
		wantCompletion string
	}{
		{
			name:           "healthy hybrid response has unknown completion",
			fixture:        "synthetic_hybrid_hit",
			wantStatus:     "ok",
			wantRetrieval:  "hybrid",
			wantCoverage:   "complete",
			wantCompletion: "unknown",
		},
		{
			name:           "stale retrieval does not manufacture completion",
			fixture:        "synthetic_after_barrier_timeout",
			wantStatus:     "stale",
			wantRetrieval:  "lexical",
			wantCoverage:   "partial",
			wantCompletion: "unknown",
		},
		{
			name:           "verified partial completion leaves result healthy",
			fixture:        "synthetic_verified_supported_host_partial_callback",
			wantStatus:     "ok",
			wantRetrieval:  "exact",
			wantCoverage:   "complete",
			wantCompletion: "partial",
		},
		{
			name:           "authorized unavailable result remains no callback",
			fixture:        "synthetic_authorized_source_unavailable",
			wantStatus:     "unavailable",
			wantRetrieval:  "unavailable",
			wantCoverage:   "unavailable",
			wantCompletion: "unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := fixtures[tc.fixture]
			uciRequireQueryResponseAccepted(t, payload)

			response := uciQueryResponseFixtureObject(t, fixtures, tc.fixture)
			if got := uciQueryResponseString(t, response, "status"); got != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got, tc.wantStatus)
			}
			if got := uciQueryResponseString(t, uciQueryResponseObject(t, response["retrieval"], "retrieval"), "mode"); got != tc.wantRetrieval {
				t.Fatalf("retrieval.mode = %q, want %q", got, tc.wantRetrieval)
			}
			if got := uciQueryResponseString(t, uciQueryResponseObject(t, response["coverage"], "coverage"), "structural"); got != tc.wantCoverage {
				t.Fatalf("coverage.structural = %q, want %q", got, tc.wantCoverage)
			}
			if got := uciQueryResponseString(t, uciQueryResponseObject(t, response["exposure"], "exposure"), "completion_state"); got != tc.wantCompletion {
				t.Fatalf("exposure.completion_state = %q, want %q", got, tc.wantCompletion)
			}
		})
	}
}

func TestUCIQueryResponseContractSeparatesSecurityEnvelopes(t *testing.T) {
	fixtures := uciQueryResponseFixturesByName(t)

	for _, tc := range []struct {
		name               string
		fixture            string
		wantErrorCode      string
		wantContextualBody bool
	}{
		{
			name:               "recordable authorized unavailable result",
			fixture:            "synthetic_authorized_source_unavailable",
			wantErrorCode:      "SOURCE_UNAVAILABLE",
			wantContextualBody: true,
		},
		{
			name:               "initial recorder failure suppression",
			fixture:            "synthetic_exposure_recorder_unavailable",
			wantErrorCode:      "EXPOSURE_UNAVAILABLE",
			wantContextualBody: false,
		},
		{
			name:               "idempotency mismatch suppression",
			fixture:            "synthetic_exposure_idempotency_mismatch",
			wantErrorCode:      "IDEMPOTENCY_MISMATCH",
			wantContextualBody: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := fixtures[tc.fixture]
			uciRequireQueryResponseAccepted(t, payload)

			response := uciQueryResponseFixtureObject(t, fixtures, tc.fixture)
			if got := uciQueryResponseString(t, uciQueryResponseObject(t, response["error"], "error"), "code"); got != tc.wantErrorCode {
				t.Fatalf("error.code = %q, want %q", got, tc.wantErrorCode)
			}

			exposure, found := response["exposure"]
			if !found {
				t.Fatal("response omitted exposure")
			}
			if tc.wantContextualBody {
				if exposure == nil {
					t.Fatal("recordable authorized result omitted exposure receipt")
				}
				for _, field := range []string{"contexts", "freshness", "retrieval", "coverage", "items", "truncated", "warnings", "continuation"} {
					if _, found := response[field]; !found {
						t.Fatalf("recordable authorized result omitted %q", field)
					}
				}
				if items := uciQueryResponseArray(t, response["items"], "items"); len(items) != 0 {
					t.Fatalf("authorized unavailable items = %d, want 0", len(items))
				}
				return
			}

			if exposure != nil {
				t.Fatalf("suppressed failure returned stale exposure receipt %#v", exposure)
			}
			for _, field := range []string{"contexts", "freshness", "retrieval", "coverage", "items", "truncated", "warnings", "continuation", "graph"} {
				if _, found := response[field]; found {
					t.Fatalf("suppressed failure disclosed contextual field %q", field)
				}
			}
		})
	}
}

func TestUCIQueryResponseContractRejectsInvalidMutations(t *testing.T) {
	fixtures := uciQueryResponseFixturesByName(t)

	for _, tc := range []struct {
		name    string
		fixture string
		mutate  func(t *testing.T, response map[string]any, fixtures map[string]json.RawMessage)
	}{
		{
			name:    "unknown response field",
			fixture: "synthetic_hybrid_hit",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["private_locator"] = "opaque"
			},
		},
		{
			name:    "unknown exposure field",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseObject(t, response["exposure"], "exposure")["source_id"] = "11111111-1111-4111-8111-111111111111"
			},
		},
		{
			name:    "wrong schema version",
			fixture: "synthetic_hybrid_hit",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["schema"] = "engram.code-query/2"
			},
		},
		{
			name:    "unknown status",
			fixture: "synthetic_hybrid_hit",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["status"] = "completed"
			},
		},
		{
			name:    "context has malformed source identifier",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseFirstContext(t, response)["source_id"] = "not-a-uuid"
			},
		},
		{
			name:    "context has zero generation",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseFirstContext(t, response)["generation"] = 0
			},
		},
		{
			name:    "path hash freshness lacks barrier",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				freshness := uciQueryResponseObject(t, response["freshness"], "freshness")
				freshness["method"] = "path_hash_barrier"
				freshness["barrier"] = nil
			},
		},
		{
			name:    "freshness barrier exceeds path cap",
			fixture: "synthetic_after_barrier_timeout",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				freshness := uciQueryResponseObject(t, response["freshness"], "freshness")
				barrier := uciQueryResponseObject(t, freshness["barrier"], "freshness.barrier")
				uciQueryResponseObject(t, barrier["scope"], "freshness.barrier.scope")["path_count"] = 51
			},
		},
		{
			name:    "retrieval coverage exceeds one",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseObject(t, response["retrieval"], "retrieval")["vector_coverage"] = 1.01
			},
		},
		{
			name:    "unknown retrieval mode",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseObject(t, response["retrieval"], "retrieval")["mode"] = "semantic"
			},
		},
		{
			name:    "overlong degradation reason",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseObject(t, response["retrieval"], "retrieval")["degradation_reasons"] = []any{strings.Repeat("x", 129)}
			},
		},
		{
			name:    "unknown coverage state",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseObject(t, response["coverage"], "coverage")["structural"] = "unknown"
			},
		},
		{
			name:    "overlong continuation",
			fixture: "synthetic_hybrid_hit",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["truncated"] = true
				response["continuation"] = strings.Repeat("x", 2049)
			},
		},
		{
			name:    "continuation without truncation",
			fixture: "synthetic_hybrid_hit",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["continuation"] = "opaque-next-page"
			},
		},
		{
			name:    "nonincreasing byte span",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				item := uciQueryResponseFirstItem(t, response)
				uciQueryResponseObject(t, item["span"], "items[0].span")["byte_end"] = 0
			},
		},
		{
			name:    "inverted line span",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				span := uciQueryResponseObject(t, uciQueryResponseFirstItem(t, response)["span"], "items[0].span")
				span["line_start"] = 2
				span["line_end"] = 1
			},
		},
		{
			name:    "item reference outside contextual view",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				item := uciQueryResponseFirstItem(t, response)
				uciQueryResponseObject(t, item["ref"], "items[0].ref")["source_id"] = "55555555-5555-4555-8555-555555555555"
			},
		},
		{
			name:    "graph node outside contextual view",
			fixture: "synthetic_graph",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				graph := uciQueryResponseObject(t, response["graph"], "graph")
				nodes := uciQueryResponseArray(t, graph["nodes"], "graph.nodes")
				uciQueryResponseObject(t, nodes[0], "graph.nodes[0]")["view_id"] = "55555555-5555-4555-8555-555555555555"
			},
		},
		{
			name:    "graph edge endpoint outside contextual view",
			fixture: "synthetic_graph",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				edge := uciQueryResponseFirstGraphEdge(t, response)
				uciQueryResponseObject(t, edge["to"], "graph.edges[0].to")["source_id"] = "55555555-5555-4555-8555-555555555555"
			},
		},
		{
			name:    "graph evidence reference outside contextual view",
			fixture: "synthetic_graph",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				edge := uciQueryResponseFirstGraphEdge(t, response)
				evidenceRefs := uciQueryResponseArray(t, edge["evidence_refs"], "graph.edges[0].evidence_refs")
				uciQueryResponseObject(t, evidenceRefs[0], "graph.edges[0].evidence_refs[0]")["view_id"] = "55555555-5555-4555-8555-555555555555"
			},
		},
		{
			name:    "graph exceeds node cap",
			fixture: "synthetic_graph",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				graph := uciQueryResponseObject(t, response["graph"], "graph")
				nodes := uciQueryResponseArray(t, graph["nodes"], "graph.nodes")
				for len(nodes) <= 200 {
					nodes = append(nodes, nodes[0])
				}
				graph["nodes"] = nodes
			},
		},
		{
			name:    "graph exceeds edge cap",
			fixture: "synthetic_graph",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				graph := uciQueryResponseObject(t, response["graph"], "graph")
				edges := uciQueryResponseArray(t, graph["edges"], "graph.edges")
				for len(edges) <= 400 {
					edges = append(edges, edges[0])
				}
				graph["edges"] = edges
			},
		},
		{
			name:    "unknown graph relation",
			fixture: "synthetic_graph",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseFirstGraphEdge(t, response)["relation"] = "owns"
			},
		},
		{
			name:    "exposure has nonopaque reference",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseObject(t, response["exposure"], "exposure")["exposure_ref"] = "exposure-1"
			},
		},
		{
			name:    "unknown completion outcome",
			fixture: "synthetic_hybrid_hit",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseObject(t, response["exposure"], "exposure")["completion_state"] = "complete"
			},
		},
		{
			name:    "empty result contains an item",
			fixture: "synthetic_hybrid_hit",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["status"] = "empty"
			},
		},
		{
			name:    "authorized unavailable omits recordable receipt",
			fixture: "synthetic_authorized_source_unavailable",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["exposure"] = nil
			},
		},
		{
			name:    "authorized unavailable contains an item",
			fixture: "synthetic_hybrid_hit",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["status"] = "unavailable"
				response["error"] = map[string]any{"code": "SOURCE_UNAVAILABLE"}
			},
		},
		{
			name:    "authorized unavailable omits contextual envelope",
			fixture: "synthetic_authorized_source_unavailable",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				delete(response, "contexts")
			},
		},
		{
			name:    "recorder failure discloses result body",
			fixture: "synthetic_exposure_recorder_unavailable",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["items"] = []any{}
			},
		},
		{
			name:    "idempotency mismatch discloses contextual envelope",
			fixture: "synthetic_exposure_idempotency_mismatch",
			mutate: func(t *testing.T, response map[string]any, fixtures map[string]json.RawMessage) {
				fromHealthyResponse := uciQueryResponseFixtureObject(t, fixtures, "synthetic_hybrid_hit")
				response["contexts"] = uciQueryResponseArray(t, fromHealthyResponse["contexts"], "healthy.contexts")
			},
		},
		{
			name:    "idempotency mismatch returns stale receipt",
			fixture: "synthetic_exposure_idempotency_mismatch",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["exposure"] = map[string]any{
					"exposure_ref":     "uci-exp_stale",
					"completion_state": "unknown",
				}
			},
		},
		{
			name:    "context refusal discloses contextual identifier",
			fixture: "synthetic_exposure_recorder_unavailable",
			mutate: func(t *testing.T, response map[string]any, fixtures map[string]json.RawMessage) {
				response["status"] = "context_required"
				response["error"] = map[string]any{"code": "CONTEXT_REQUIRED"}
				fromHealthyResponse := uciQueryResponseFixtureObject(t, fixtures, "synthetic_hybrid_hit")
				response["contexts"] = uciQueryResponseArray(t, fromHealthyResponse["contexts"], "healthy.contexts")
			},
		},
		{
			name:    "forbidden response uses source error",
			fixture: "synthetic_exposure_recorder_unavailable",
			mutate: func(_ *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				response["status"] = "forbidden"
				response["error"] = map[string]any{"code": "SOURCE_UNAVAILABLE"}
			},
		},
		{
			name:    "query exposes callback only error",
			fixture: "synthetic_exposure_recorder_unavailable",
			mutate: func(t *testing.T, response map[string]any, _ map[string]json.RawMessage) {
				uciQueryResponseObject(t, response["error"], "error")["code"] = "COMPLETION_EVIDENCE_UNAVAILABLE"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := uciQueryResponseFixtureObject(t, fixtures, tc.fixture)
			tc.mutate(t, response, fixtures)
			uciRequireQueryResponseRejected(t, uciMarshalQueryResponseFixture(t, response))
		})
	}
}

func uciLoadQueryResponseFixtures(t *testing.T) []uciQueryResponseFixture {
	t.Helper()
	payload, err := os.ReadFile(uciQueryResponseContractFile(t, "examples.json"))
	if err != nil {
		t.Fatalf("read query response examples: %v", err)
	}

	var fixtures []uciQueryResponseFixture
	if err := json.Unmarshal(payload, &fixtures); err != nil {
		t.Fatalf("decode query response examples: %v", err)
	}
	if len(fixtures) != len(uciQueryResponseExampleNames) {
		t.Fatalf("query response examples = %d, want %d", len(fixtures), len(uciQueryResponseExampleNames))
	}

	expected := make(map[string]struct{}, len(uciQueryResponseExampleNames))
	for _, name := range uciQueryResponseExampleNames {
		expected[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		if _, known := expected[fixture.Name]; !known {
			t.Fatalf("unexpected query response example %q", fixture.Name)
		}
		if _, duplicate := seen[fixture.Name]; duplicate {
			t.Fatalf("duplicate query response example %q", fixture.Name)
		}
		if len(fixture.Response) == 0 {
			t.Fatalf("query response example %q has no response", fixture.Name)
		}
		seen[fixture.Name] = struct{}{}
	}
	for _, name := range uciQueryResponseExampleNames {
		if _, found := seen[name]; !found {
			t.Fatalf("missing query response example %q", name)
		}
	}
	return fixtures
}

func uciQueryResponseFixturesByName(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	fixtures := uciLoadQueryResponseFixtures(t)
	byName := make(map[string]json.RawMessage, len(fixtures))
	for _, fixture := range fixtures {
		byName[fixture.Name] = fixture.Response
	}
	return byName
}

func uciQueryResponseContractFile(t *testing.T, filename string) string {
	t.Helper()
	starts := make([]string, 0, 2)
	if _, testFile, _, ok := runtime.Caller(0); ok {
		starts = append(starts, filepath.Dir(testFile))
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		starts = append(starts, workingDirectory)
	}

	for _, start := range starts {
		directory, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for {
			candidate := filepath.Join(directory, uciQueryResponseContractsDir, filename)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
			directory = parent
		}
	}

	t.Fatalf("cannot locate repository-relative query response fixture %q", filename)
	return ""
}

func uciRequireQueryResponseAccepted(t *testing.T, payload []byte) {
	t.Helper()
	var response QueryResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	uciRequireJSONEquivalent(t, payload, encoded)
}

func uciRequireQueryResponseRejected(t *testing.T, payload []byte) {
	t.Helper()
	var response QueryResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return
	}
	if err := response.Validate(); err != nil {
		return
	}
	t.Fatalf("invalid query response was accepted: %s", payload)
}

func uciRequireJSONEquivalent(t *testing.T, wantPayload, gotPayload []byte) {
	t.Helper()
	var want any
	if err := json.Unmarshal(wantPayload, &want); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	var got any
	if err := json.Unmarshal(gotPayload, &got); err != nil {
		t.Fatalf("decode marshaled JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON round trip = %#v, want %#v", got, want)
	}
}

func uciQueryResponseFixtureObject(t *testing.T, fixtures map[string]json.RawMessage, name string) map[string]any {
	t.Helper()
	payload, found := fixtures[name]
	if !found {
		t.Fatalf("missing fixture %q", name)
	}
	var response map[string]any
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode fixture %q: %v", name, err)
	}
	return response
}

func uciMarshalQueryResponseFixture(t *testing.T, response map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode mutated response: %v", err)
	}
	return payload
}

func uciQueryResponseObject(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want JSON object", label, value)
	}
	return object
}

func uciQueryResponseArray(t *testing.T, value any, label string) []any {
	t.Helper()
	array, ok := value.([]any)
	if !ok {
		t.Fatalf("%s = %#v, want JSON array", label, value)
	}
	return array
}

func uciQueryResponseString(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, found := object[key]
	if !found {
		t.Fatalf("JSON object omitted %q", key)
	}
	stringValue, ok := value.(string)
	if !ok {
		t.Fatalf("%q = %#v, want string", key, value)
	}
	return stringValue
}

func uciQueryResponseFirstContext(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	contexts := uciQueryResponseArray(t, response["contexts"], "contexts")
	if len(contexts) == 0 {
		t.Fatal("contexts is empty")
	}
	return uciQueryResponseObject(t, contexts[0], "contexts[0]")
}

func uciQueryResponseFirstItem(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	items := uciQueryResponseArray(t, response["items"], "items")
	if len(items) == 0 {
		t.Fatal("items is empty")
	}
	return uciQueryResponseObject(t, items[0], "items[0]")
}

func uciQueryResponseFirstGraphEdge(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	graph := uciQueryResponseObject(t, response["graph"], "graph")
	edges := uciQueryResponseArray(t, graph["edges"], "graph.edges")
	if len(edges) == 0 {
		t.Fatal("graph.edges is empty")
	}
	return uciQueryResponseObject(t, edges[0], "graph.edges[0]")
}
