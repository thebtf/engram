package uci

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// Query behavior stops before ExposureRecorder ownership. These assertions bind
// the existing response DTO's contextual fields without exercising that later boundary.
func TestUCIQueryScopesCandidateUniverseBeforeRanking(t *testing.T) {
	fixture := newQueryTestFixture()
	store := fixture.store()
	service := NewQueryService(store)

	result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), QuerySpec{
		ClientSessionID: "query-client-a",
		Mode:            QueryModeFTS,
		Text:            "SharedSymbol",
		Filter:          QueryFilter{Languages: []string{"go"}},
		Order:           QueryOrderRelevance,
		Limit:           1,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	items := queryTestItems(t, result)
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	queryTestAssertResponseBoundTo(t, result.Response, fixture.contextA)
	queryTestAssertCitation(t, items[0], fixture.sharedA, QueryMatchFTS)
	queryTestAssertStoreCall(t, store, fixture.contextA)
}

func TestUCIExactLookupModesRemainDistinct(t *testing.T) {
	fixture := newQueryTestFixture()

	for _, tc := range []struct {
		name string
		mode QueryMode
		text string
	}{
		{name: "local name", mode: QueryModeExactLocalName, text: fixture.sharedA.LocalName},
		{name: "qualified symbol", mode: QueryModeExactQualifiedSymbol, text: fixture.sharedA.QualifiedSymbol},
		{name: "relative path", mode: QueryModeExactRelativePath, text: fixture.sharedA.RelativePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := fixture.store()
			service := NewQueryService(store)

			result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), QuerySpec{
				ClientSessionID: "query-client-a",
				Mode:            tc.mode,
				Text:            tc.text,
				Filter:          QueryFilter{Languages: []string{"go"}},
				Order:           QueryOrderPath,
				Limit:           1,
			})
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}

			items := queryTestItems(t, result)
			if len(items) != 1 {
				t.Fatalf("item count = %d, want 1", len(items))
			}
			queryTestAssertResponseBoundTo(t, result.Response, fixture.contextA)
			queryTestAssertCitation(t, items[0], fixture.sharedA, QueryMatchExact)
			if got := store.calls[0].Spec.Mode; got != tc.mode {
				t.Fatalf("store query mode = %q, want %q", got, tc.mode)
			}
		})
	}
}

func TestUCIFTSIdentifierAndCyrillicTokenization(t *testing.T) {
	fixture := newQueryTestFixture()

	for _, tc := range []struct {
		name string
		text string
		want QueryCandidate
	}{
		{name: "original identifier", text: "HTTPServer", want: fixture.httpServer},
		{name: "camel case token", text: "config", want: fixture.parseConfig},
		{name: "snake case token", text: "cache", want: fixture.refreshCache},
		{name: "cyrillic text", text: "сохранение", want: fixture.cyrillic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := fixture.store()
			service := NewQueryService(store)

			result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), QuerySpec{
				ClientSessionID: "query-client-a",
				Mode:            QueryModeFTS,
				Text:            tc.text,
				Filter:          QueryFilter{Languages: []string{"go"}},
				Order:           QueryOrderPath,
				Limit:           1,
			})
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}

			items := queryTestItems(t, result)
			if len(items) != 1 {
				t.Fatalf("item count = %d, want 1", len(items))
			}
			queryTestAssertCitation(t, items[0], tc.want, QueryMatchFTS)
		})
	}
}

func TestUCIQueryDistinguishesCompleteEmptyPartialAndUnavailable(t *testing.T) {
	fixture := newQueryTestFixture()

	for _, tc := range []struct {
		name        string
		coverage    IndexCoverageState
		unavailable *QueryError
		wantStatus  QueryResponseStatus
		wantError   *QueryError
	}{
		{
			name:       "complete no hit is empty",
			coverage:   IndexCoverageComplete,
			wantStatus: QueryStatusEmpty,
		},
		{
			name:       "partial corpus is partial",
			coverage:   IndexCoveragePartial,
			wantStatus: QueryStatusPartial,
		},
		{
			name:        "unavailable capability is unavailable",
			coverage:    IndexCoverageUnavailable,
			unavailable: &QueryError{Code: QueryErrorParserUnsupported},
			wantStatus:  QueryStatusUnavailable,
			wantError:   &QueryError{Code: QueryErrorParserUnsupported},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := fixture.store()
			store.coverage = tc.coverage
			store.unavailable = tc.unavailable
			service := NewQueryService(store)

			result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), QuerySpec{
				ClientSessionID: "query-client-a",
				Mode:            QueryModeFTS,
				Text:            "absent-token",
				Filter:          QueryFilter{Languages: []string{"go"}},
				Order:           QueryOrderPath,
				Limit:           1,
			})
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}

			if got := result.Response.Status; got != tc.wantStatus {
				t.Fatalf("response status = %q, want %q", got, tc.wantStatus)
			}
			if got := queryTestItems(t, result); len(got) != 0 {
				t.Fatalf("item count = %d, want 0", len(got))
			}
			if result.Response.Coverage == nil || result.Response.Coverage.Structural != tc.coverage {
				t.Fatalf("response coverage = %#v, want structural %q", result.Response.Coverage, tc.coverage)
			}
			if !reflect.DeepEqual(result.Response.Error, tc.wantError) {
				t.Fatalf("response error = %#v, want %#v", result.Response.Error, tc.wantError)
			}
		})
	}
}

func TestUCIQueryOrdersDeterministicallyAndBoundsResults(t *testing.T) {
	fixture := newQueryTestFixture()
	store := fixture.store()
	service := NewQueryService(store)

	result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), QuerySpec{
		ClientSessionID: "query-client-a",
		Mode:            QueryModeFTS,
		Text:            "ordered",
		Filter:          QueryFilter{Languages: []string{"go"}},
		Order:           QueryOrderPath,
		Limit:           2,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	items := queryTestItems(t, result)
	if len(items) != 2 {
		t.Fatalf("item count = %d, want bounded count 2", len(items))
	}
	if got, want := []string{items[0].Path, items[1].Path}, []string{"a/ordered.go", "b/ordered.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered paths = %#v, want %#v", got, want)
	}
	if result.Response.Truncated == nil || !*result.Response.Truncated {
		t.Fatalf("truncated = %#v, want true", result.Response.Truncated)
	}
	if result.Response.Continuation == nil || result.Response.Continuation.Value == nil {
		t.Fatalf("continuation = %#v, want opaque next-page token", result.Response.Continuation)
	}
	queryTestAssertResponseBoundTo(t, result.Response, fixture.contextA)
	queryTestAssertStoreCall(t, store, fixture.contextA)
}

func TestUCIQueryContinuationTraversesBeyondOneStoreWindow(t *testing.T) {
	fixture := newQueryTestFixture()
	store := &queryTestStore{coverage: IndexCoverageComplete}
	const candidateCount = 65
	for index := range candidateCount {
		store.candidates = append(store.candidates, queryTestCandidate(queryTestCandidateInput{
			contextRef:      fixture.contextA,
			artifactID:      fmt.Sprintf("70000000-0000-4000-8000-%012x", index+100),
			digestCharacter: "a",
			localName:       fmt.Sprintf("Symbol%02d", index),
			qualifiedSymbol: fmt.Sprintf("fixture.Symbol%02d", index),
			relativePath:    fmt.Sprintf("pkg/%02d.go", index),
			text:            fmt.Sprintf("func Symbol%02d() {}", index),
			score:           float64(candidateCount - index),
		}))
	}
	service := NewQueryService(store)
	spec := QuerySpec{
		ClientSessionID: "query-client-a",
		Mode:            QueryModeFTS,
		Text:            "Symbol",
		Filter:          QueryFilter{Languages: []string{"go"}},
		Order:           QueryOrderPath,
		Limit:           10,
	}
	seen := make(map[string]struct{}, candidateCount)
	for {
		result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), spec)
		if err != nil {
			t.Fatalf("paged Query() error = %v", err)
		}
		for _, item := range queryTestItems(t, result) {
			if _, duplicate := seen[item.Ref.EntityKey]; duplicate {
				t.Fatalf("duplicate paged entity %q", item.Ref.EntityKey)
			}
			seen[item.Ref.EntityKey] = struct{}{}
		}
		if result.Response.Continuation == nil || result.Response.Continuation.Value == nil {
			break
		}
		token := *result.Response.Continuation.Value
		spec.Continuation = &token
	}
	if got := len(seen); got != candidateCount {
		t.Fatalf("paged candidates = %d, want %d", got, candidateCount)
	}
	for index, call := range store.calls {
		if want := index * spec.Limit; call.Spec.Offset != want {
			t.Fatalf("store call %d offset = %d, want %d", index, call.Spec.Offset, want)
		}
	}
}

func TestUCIQueryRejectsLimitAboveMaximumInsteadOfClamping(t *testing.T) {
	fixture := newQueryTestFixture()
	store := fixture.store()
	service := NewQueryService(store)

	_, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), QuerySpec{
		ClientSessionID: "query-client-a",
		Mode:            QueryModeFTS,
		Text:            "ordered",
		Filter:          QueryFilter{Languages: []string{"go"}},
		Order:           QueryOrderPath,
		Limit:           queryMaxItems + 1,
	})
	if err == nil {
		t.Fatal("Query() error = nil, want limit rejection")
	}
	if len(store.calls) != 0 {
		t.Fatalf("store calls = %d, want validation before candidate selection", len(store.calls))
	}
}

func TestUCIQueryContinuationBindsClientViewProfileQueryFilterAndOrder(t *testing.T) {
	fixture := newQueryTestFixture()
	store := fixture.store()
	service := NewQueryService(store)
	authorizedA := newAuthorizedContext(fixture.contextA)

	firstSpec := QuerySpec{
		ClientSessionID: "query-client-a",
		Mode:            QueryModeFTS,
		Text:            "ordered",
		Filter:          QueryFilter{Languages: []string{"go"}},
		Order:           QueryOrderPath,
		Limit:           1,
	}
	first, err := service.Query(context.Background(), authorizedA, firstSpec)
	if err != nil {
		t.Fatalf("first Query() error = %v", err)
	}
	token := queryTestContinuation(t, first)

	nextSpec := firstSpec
	nextSpec.Continuation = &token
	second, err := service.Query(context.Background(), authorizedA, nextSpec)
	if err != nil {
		t.Fatalf("continued Query() error = %v", err)
	}
	retry, err := service.Query(context.Background(), authorizedA, nextSpec)
	if err != nil {
		t.Fatalf("exact continuation retry error = %v", err)
	}
	if got, want := queryTestItems(t, retry), queryTestItems(t, second); !reflect.DeepEqual(got, want) {
		t.Fatalf("retry items = %#v, want exact page %#v", got, want)
	}

	callCount := len(store.calls)
	for _, tc := range []struct {
		name       string
		authorized AuthorizedContext
		spec       QuerySpec
	}{
		{
			name:       "client",
			authorized: authorizedA,
			spec: func() QuerySpec {
				changed := nextSpec
				changed.ClientSessionID = "query-client-b"
				return changed
			}(),
		},
		{
			name:       "view",
			authorized: newAuthorizedContext(fixture.contextB),
			spec:       nextSpec,
		},
		{
			name:       "profile",
			authorized: newAuthorizedContext(fixture.contextAOtherProfile),
			spec:       nextSpec,
		},
		{
			name:       "query",
			authorized: authorizedA,
			spec: func() QuerySpec {
				changed := nextSpec
				changed.Text = "SharedSymbol"
				return changed
			}(),
		},
		{
			name:       "filter",
			authorized: authorizedA,
			spec: func() QuerySpec {
				changed := nextSpec
				changed.Filter = QueryFilter{Languages: []string{"markdown"}}
				return changed
			}(),
		},
		{
			name:       "path prefix",
			authorized: authorizedA,
			spec: func() QuerySpec {
				changed := nextSpec
				changed.Filter.PathPrefix = "ordered"
				return changed
			}(),
		},
		{
			name:       "order",
			authorized: authorizedA,
			spec: func() QuerySpec {
				changed := nextSpec
				changed.Order = QueryOrderRelevance
				return changed
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.Query(context.Background(), tc.authorized, tc.spec); err == nil {
				t.Fatal("Query() error = nil, want continuation binding rejection")
			}
		})
	}
	if got := len(store.calls); got != callCount {
		t.Fatalf("store calls after rejected continuations = %d, want %d", got, callCount)
	}
}

func TestUCIQueryPathPrefixNormalizesAndScopesCandidates(t *testing.T) {
	fixture := newQueryTestFixture()
	store := &queryTestStore{candidates: []QueryCandidate{
		queryTestCandidate(queryTestCandidateInput{contextRef: fixture.contextA, artifactID: "70000000-0000-4000-8000-000000000051", digestCharacter: "a", localName: "PathPrefixNeedle", qualifiedSymbol: "fixture.api.Handler", relativePath: "src/api/handler.go", text: "func PathPrefixNeedle() {}", score: 1}),
		queryTestCandidate(queryTestCandidateInput{contextRef: fixture.contextA, artifactID: "70000000-0000-4000-8000-000000000052", digestCharacter: "b", localName: "PathPrefixNeedle", qualifiedSymbol: "fixture.api.Router", relativePath: "src/api/nested/router.go", text: "func PathPrefixNeedle() {}", score: 2}),
		queryTestCandidate(queryTestCandidateInput{contextRef: fixture.contextA, artifactID: "70000000-0000-4000-8000-000000000053", digestCharacter: "c", localName: "PathPrefixNeedle", qualifiedSymbol: "fixture.apix.Sibling", relativePath: "src/apix/sibling.go", text: "func PathPrefixNeedle() {}", score: 3}),
		queryTestCandidate(queryTestCandidateInput{contextRef: fixture.contextA, artifactID: "70000000-0000-4000-8000-000000000054", digestCharacter: "d", localName: "PathPrefixNeedle", qualifiedSymbol: "fixture.api.File", relativePath: "src/api.go", text: "func PathPrefixNeedle() {}", score: 4}),
	}}
	service := NewQueryService(store)
	spec := QuerySpec{
		ClientSessionID: "query-client-a",
		Mode:            QueryModeFTS,
		Text:            "PathPrefixNeedle",
		Filter:          QueryFilter{PathPrefix: "./src//api/", Languages: []string{"go"}},
		Order:           QueryOrderPath,
		Limit:           10,
	}

	result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), spec)
	if err != nil {
		t.Fatalf("Query() directory prefix error = %v", err)
	}
	paths := make([]string, 0, len(queryTestItems(t, result)))
	for _, item := range queryTestItems(t, result) {
		paths = append(paths, item.Path)
	}
	if want := []string{"src/api/handler.go", "src/api/nested/router.go"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("directory prefix paths = %#v, want %#v", paths, want)
	}
	if got := store.calls[0].Spec.Filter.PathPrefix; got != "src/api" {
		t.Fatalf("store path prefix = %q, want normalized %q", got, "src/api")
	}
	if got := spec.Filter.PathPrefix; got != "./src//api/" {
		t.Fatalf("caller path prefix mutated to %q", got)
	}

	fileSpec := spec
	fileSpec.Filter.PathPrefix = "src/api/handler.go"
	fileResult, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), fileSpec)
	if err != nil {
		t.Fatalf("Query() file prefix error = %v", err)
	}
	fileItems := queryTestItems(t, fileResult)
	if len(fileItems) != 1 || fileItems[0].Path != "src/api/handler.go" {
		t.Fatalf("file prefix items = %#v, want exact file", fileItems)
	}
}

func TestUCIPathPrefixNormalization(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty", input: "", want: ""},
		{name: "current directory", input: ".", want: ""},
		{name: "slash normalized directory", input: "./internal//uci/", want: "internal/uci"},
		{name: "literal like metacharacters", input: "src/special%_dir", want: "src/special%_dir"},
		{name: "absolute", input: "/workspace/internal", wantErr: true},
		{name: "traversal", input: "internal/../outside", wantErr: true},
		{name: "parent", input: "..", wantErr: true},
		{name: "windows drive", input: "C:/workspace/internal", wantErr: true},
		{name: "windows backslash", input: `internal\uci`, wantErr: true},
		{name: "unc", input: `\\server\share`, wantErr: true},
		{name: "nul", input: "internal\x00uci", wantErr: true},
		{name: "control", input: "internal/\u0085", wantErr: true},
		{name: "invalid utf8", input: string([]byte{'a', 0xff}), wantErr: true},
		{name: "overlong", input: strings.Repeat("a", queryMaxPath+1), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeQueryPathPrefix(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeQueryPathPrefix(%q) error = nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeQueryPathPrefix(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeQueryPathPrefix(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestUCIQueryRejectsStaleAndForeignCandidates(t *testing.T) {
	fixture := newQueryTestFixture()
	store := fixture.store()
	stale := fixture.sharedA
	stale.Context.Generation--
	store.injected = []QueryCandidate{stale, fixture.sharedB}
	service := NewQueryService(store)

	result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), QuerySpec{
		ClientSessionID: "query-client-a",
		Mode:            QueryModeExactLocalName,
		Text:            fixture.sharedA.LocalName,
		Filter:          QueryFilter{Languages: []string{"go"}},
		Order:           QueryOrderPath,
		Limit:           3,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	items := queryTestItems(t, result)
	if len(items) != 1 {
		t.Fatalf("item count = %d, want only the current selected-view candidate", len(items))
	}
	queryTestAssertResponseBoundTo(t, result.Response, fixture.contextA)
	queryTestAssertCitation(t, items[0], fixture.sharedA, QueryMatchExact)
}

type queryTestStoreCall struct {
	Context AuthorizedContext
	Spec    QuerySpec
}

type queryTestStore struct {
	candidates  []QueryCandidate
	coverage    IndexCoverageState
	unavailable *QueryError
	injected    []QueryCandidate
	calls       []queryTestStoreCall
}

var _ QueryStore = (*queryTestStore)(nil)

func (store *queryTestStore) SelectCandidates(_ context.Context, authorized AuthorizedContext, spec QuerySpec) (QueryStoreResult, error) {
	store.calls = append(store.calls, queryTestStoreCall{Context: authorized, Spec: spec})

	result := QueryStoreResult{
		Coverage:    store.coverage,
		Unavailable: store.unavailable,
	}
	if result.Coverage == "" {
		result.Coverage = IndexCoverageComplete
	}

	selected := authorized.Ref()
	for _, candidate := range store.candidates {
		if !queryTestContextRefsEqual(candidate.Context, selected) || !queryTestCandidateMatches(candidate, spec) {
			continue
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	result.Candidates = append(result.Candidates, store.injected...)
	queryTestOrderCandidates(result.Candidates, spec.Order)
	if spec.Offset > len(result.Candidates) {
		result.Candidates = nil
		return result, nil
	}
	end := spec.Offset + spec.Limit + 1
	if end > len(result.Candidates) {
		end = len(result.Candidates)
	}
	result.Candidates = append([]QueryCandidate(nil), result.Candidates[spec.Offset:end]...)
	return result, nil
}

type queryTestFixture struct {
	contextA             ContextRef
	contextAOtherProfile ContextRef
	contextB             ContextRef
	sharedA              QueryCandidate
	sharedB              QueryCandidate
	httpServer           QueryCandidate
	parseConfig          QueryCandidate
	refreshCache         QueryCandidate
	cyrillic             QueryCandidate
	ordered              []QueryCandidate
	candidates           []QueryCandidate
}

func newQueryTestFixture() queryTestFixture {
	contextA := queryTestContextRef(
		"20000000-0000-4000-8000-000000000031",
		"30000000-0000-4000-8000-000000000031",
		"40000000-0000-4000-8000-000000000031",
		"50000000-0000-4000-8000-000000000031",
		31,
	)
	contextB := queryTestContextRef(
		contextA.SourceID,
		"30000000-0000-4000-8000-000000000032",
		"40000000-0000-4000-8000-000000000032",
		contextA.AnalysisProfileID,
		32,
	)
	contextAOtherProfile := contextA
	contextAOtherProfile.AnalysisProfileID = "50000000-0000-4000-8000-000000000032"

	fixture := queryTestFixture{
		contextA:             contextA,
		contextAOtherProfile: contextAOtherProfile,
		contextB:             contextB,
		sharedA: queryTestCandidate(queryTestCandidateInput{
			contextRef: contextA, artifactID: "70000000-0000-4000-8000-000000000031", digestCharacter: "a",
			localName: "SharedSymbol", qualifiedSymbol: "shared.SharedSymbol", relativePath: "pkg/shared.go",
			text: "func SharedSymbol() string { return \"A-current-body\" }", score: 1,
		}),
		sharedB: queryTestCandidate(queryTestCandidateInput{
			contextRef: contextB, artifactID: "70000000-0000-4000-8000-000000000032", digestCharacter: "b",
			localName: "SharedSymbol", qualifiedSymbol: "shared.SharedSymbol", relativePath: "pkg/shared.go",
			text: "func SharedSymbol() string { return \"B-foreign-body\" }", score: 99,
		}),
		httpServer: queryTestCandidate(queryTestCandidateInput{
			contextRef: contextA, artifactID: "70000000-0000-4000-8000-000000000033", digestCharacter: "c",
			localName: "HTTPServer", qualifiedSymbol: "net.HTTPServer", relativePath: "net/http_server.go",
			text: "type HTTPServer struct{}", score: 3,
		}),
		parseConfig: queryTestCandidate(queryTestCandidateInput{
			contextRef: contextA, artifactID: "70000000-0000-4000-8000-000000000034", digestCharacter: "d",
			localName: "parseConfig", qualifiedSymbol: "config.parseConfig", relativePath: "config/parse.go",
			text: "func parseConfig() {}", score: 4,
		}),
		refreshCache: queryTestCandidate(queryTestCandidateInput{
			contextRef: contextA, artifactID: "70000000-0000-4000-8000-000000000035", digestCharacter: "e",
			localName: "refresh_cache", qualifiedSymbol: "cache.refresh_cache", relativePath: "cache/refresh.go",
			text: "func refresh_cache() {}", score: 5,
		}),
		cyrillic: queryTestCandidate(queryTestCandidateInput{
			contextRef: contextA, artifactID: "70000000-0000-4000-8000-000000000036", digestCharacter: "f",
			localName: "СохранитьОтчёт", qualifiedSymbol: "report.СохранитьОтчёт", relativePath: "report/save.go",
			text: "// Сохранение отчёта\nfunc СохранитьОтчёт() {}", score: 6,
		}),
	}
	fixture.ordered = []QueryCandidate{
		queryTestCandidate(queryTestCandidateInput{contextRef: contextA, artifactID: "70000000-0000-4000-8000-000000000037", digestCharacter: "1", localName: "OrderedGamma", qualifiedSymbol: "ordered.OrderedGamma", relativePath: "c/ordered.go", text: "func OrderedGamma() { /* ordered */ }", score: 3}),
		queryTestCandidate(queryTestCandidateInput{contextRef: contextA, artifactID: "70000000-0000-4000-8000-000000000038", digestCharacter: "2", localName: "OrderedBeta", qualifiedSymbol: "ordered.OrderedBeta", relativePath: "b/ordered.go", text: "func OrderedBeta() { /* ordered */ }", score: 2}),
		queryTestCandidate(queryTestCandidateInput{contextRef: contextA, artifactID: "70000000-0000-4000-8000-000000000039", digestCharacter: "3", localName: "OrderedAlpha", qualifiedSymbol: "ordered.OrderedAlpha", relativePath: "a/ordered.go", text: "func OrderedAlpha() { /* ordered */ }", score: 1}),
	}
	fixture.candidates = append(fixture.candidates,
		fixture.sharedA,
		fixture.sharedB,
		fixture.httpServer,
		fixture.parseConfig,
		fixture.refreshCache,
		fixture.cyrillic,
	)
	fixture.candidates = append(fixture.candidates, fixture.ordered...)
	return fixture
}

func (fixture queryTestFixture) store() *queryTestStore {
	return &queryTestStore{candidates: append([]QueryCandidate(nil), fixture.candidates...)}
}

func queryTestContextRef(sourceID, checkoutID, viewID, profileID string, generation int64) ContextRef {
	spaceID := "10000000-0000-4000-8000-000000000031"
	return ContextRef{
		SpaceID:           &spaceID,
		SourceID:          sourceID,
		CheckoutID:        checkoutID,
		ViewID:            viewID,
		AnalysisProfileID: profileID,
		Generation:        generation,
	}
}

type queryTestCandidateInput struct {
	contextRef      ContextRef
	artifactID      string
	digestCharacter string
	localName       string
	qualifiedSymbol string
	relativePath    string
	text            string
	score           float64
}

func queryTestCandidate(input queryTestCandidateInput) QueryCandidate {
	return QueryCandidate{
		Context: input.contextRef,
		Proof: IndexArtifactProof{
			ArtifactID:         input.artifactID,
			ContentDigest:      queryTestDigest(input.digestCharacter),
			FactsDigest:        queryTestDigest("f"),
			DefinitionCount:    1,
			ReferenceSiteCount: 0,
			ChunkCount:         1,
		},
		EntityKey:       input.qualifiedSymbol,
		LocalName:       input.localName,
		QualifiedSymbol: input.qualifiedSymbol,
		RelativePath:    input.relativePath,
		Span: IndexSpan{
			ByteStart: 0,
			ByteEnd:   int64(len(input.text)),
			LineStart: 1,
			LineEnd:   1,
		},
		Text:     input.text,
		Kind:     QueryItemCode,
		Language: "go",
		Score:    input.score,
	}
}

func queryTestDigest(character string) IndexDigest {
	return IndexDigest("sha256:" + strings.Repeat(character, 64))
}

func queryTestCandidateMatches(candidate QueryCandidate, spec QuerySpec) bool {
	if len(spec.Filter.Languages) != 0 && !queryTestContains(spec.Filter.Languages, candidate.Language) {
		return false
	}
	if prefix := spec.Filter.PathPrefix; prefix != "" && candidate.RelativePath != prefix && !strings.HasPrefix(candidate.RelativePath, prefix+"/") {
		return false
	}

	switch spec.Mode {
	case QueryModeExactLocalName:
		return candidate.LocalName == spec.Text
	case QueryModeExactQualifiedSymbol:
		return candidate.QualifiedSymbol == spec.Text
	case QueryModeExactRelativePath:
		return candidate.RelativePath == spec.Text
	case QueryModeFTS:
		return queryTestFTSMatches(candidate, spec.Text)
	default:
		return false
	}
}

func queryTestFTSMatches(candidate QueryCandidate, text string) bool {
	terms := queryTestTokens(text)
	if len(terms) == 0 {
		return false
	}
	corpus := queryTestTokens(strings.Join([]string{
		candidate.LocalName,
		candidate.QualifiedSymbol,
		candidate.RelativePath,
		candidate.Text,
	}, " "))
	for term := range terms {
		if _, found := corpus[term]; !found {
			return false
		}
	}
	return true
}

func queryTestTokens(text string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, raw := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if raw == "" {
			continue
		}
		tokens[strings.ToLower(raw)] = struct{}{}
		for _, token := range queryTestIdentifierTokens(raw) {
			tokens[strings.ToLower(token)] = struct{}{}
		}
	}
	return tokens
}

func queryTestIdentifierTokens(identifier string) []string {
	runes := []rune(identifier)
	if len(runes) == 0 {
		return nil
	}

	start := 0
	var tokens []string
	for offset := range runes[1:] {
		index := offset + 1
		previous := runes[index-1]
		current := runes[index]
		nextIsLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
		boundary := (unicode.IsLower(previous) && unicode.IsUpper(current)) ||
			(unicode.IsUpper(previous) && unicode.IsUpper(current) && nextIsLower) ||
			(unicode.IsDigit(previous) != unicode.IsDigit(current))
		if !boundary {
			continue
		}
		tokens = append(tokens, string(runes[start:index]))
		start = index
	}
	return append(tokens, string(runes[start:]))
}

func queryTestOrderCandidates(candidates []QueryCandidate, order QueryOrder) {
	sort.SliceStable(candidates, func(left, right int) bool {
		if order == QueryOrderRelevance && candidates[left].Score != candidates[right].Score {
			return candidates[left].Score > candidates[right].Score
		}
		if candidates[left].RelativePath != candidates[right].RelativePath {
			return candidates[left].RelativePath < candidates[right].RelativePath
		}
		if candidates[left].Span.ByteStart != candidates[right].Span.ByteStart {
			return candidates[left].Span.ByteStart < candidates[right].Span.ByteStart
		}
		return candidates[left].EntityKey < candidates[right].EntityKey
	})
}

func queryTestItems(t *testing.T, result QueryResult) []QueryItem {
	t.Helper()
	if result.Response.Items == nil {
		t.Fatal("response items = nil")
	}
	return *result.Response.Items
}

func queryTestContinuation(t *testing.T, result QueryResult) string {
	t.Helper()
	if result.Response.Continuation == nil || result.Response.Continuation.Value == nil {
		t.Fatalf("continuation = %#v, want opaque value", result.Response.Continuation)
	}
	return *result.Response.Continuation.Value
}

func queryTestAssertStoreCall(t *testing.T, store *queryTestStore, want ContextRef) {
	t.Helper()
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1", len(store.calls))
	}
	if got := store.calls[0].Context.Ref(); !queryTestContextRefsEqual(got, want) {
		t.Fatalf("store authorized context = %#v, want %#v", got, want)
	}
	queryTestRequireNoProjectString(t, store.calls[0])
}

func queryTestAssertResponseBoundTo(t *testing.T, response QueryResponse, want ContextRef) {
	t.Helper()
	if response.Contexts == nil || len(*response.Contexts) != 1 {
		t.Fatalf("response contexts = %#v, want one selected context", response.Contexts)
	}
	got := (*response.Contexts)[0]
	if got.SourceID != want.SourceID ||
		got.CheckoutID != want.CheckoutID ||
		got.ViewID != want.ViewID ||
		got.Generation != want.Generation ||
		got.ProfileID != want.AnalysisProfileID ||
		!queryTestOptionalStringsEqual(got.SpaceID, want.SpaceID) {
		t.Fatalf("response context = %#v, want %#v", got, want)
	}
}

func queryTestAssertCitation(t *testing.T, item QueryItem, candidate QueryCandidate, source QueryMatchSource) {
	t.Helper()
	if got, want := item.Ref, (QueryEntityRef{
		SourceID:  candidate.Context.SourceID,
		ViewID:    candidate.Context.ViewID,
		EntityKey: candidate.EntityKey,
	}); got != want {
		t.Fatalf("item ref = %#v, want %#v", got, want)
	}
	if got, want := item.Path, candidate.RelativePath; got != want {
		t.Fatalf("item path = %q, want %q", got, want)
	}
	if got, want := item.Span, (QuerySpan{
		ByteStart: candidate.Span.ByteStart,
		ByteEnd:   candidate.Span.ByteEnd,
		LineStart: int64(candidate.Span.LineStart),
		LineEnd:   int64(candidate.Span.LineEnd),
	}); got != want {
		t.Fatalf("item span = %#v, want %#v", got, want)
	}
	wantDigest, ok := queryBareContentDigest(candidate.Proof.ContentDigest)
	if !ok {
		t.Fatalf("candidate content digest %q is invalid", candidate.Proof.ContentDigest)
	}
	if got := item.ContentDigest; got != wantDigest {
		t.Fatalf("item content digest = %q, want %q", got, wantDigest)
	}
	if got, want := item.Kind, candidate.Kind; got != want {
		t.Fatalf("item kind = %q, want %q", got, want)
	}
	if got, want := item.Language, candidate.Language; got != want {
		t.Fatalf("item language = %q, want %q", got, want)
	}
	if got, want := item.MatchSources, []QueryMatchSource{source}; !reflect.DeepEqual(got, want) {
		t.Fatalf("item match sources = %#v, want %#v", got, want)
	}
}

func queryTestRequireNoProjectString(t *testing.T, value any) {
	t.Helper()
	queryTestRequireNoProjectStringType(t, reflect.TypeOf(value), make(map[reflect.Type]bool))
}

func queryTestRequireNoProjectStringType(t *testing.T, typ reflect.Type, visited map[reflect.Type]bool) {
	t.Helper()
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	if visited[typ] {
		return
	}
	visited[typ] = true

	switch typ.Kind() {
	case reflect.Struct:
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			fieldType := field.Type
			for fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}
			if strings.Contains(strings.ToLower(field.Name), "project") && fieldType.Kind() == reflect.String {
				t.Fatalf("store call type %s exposes raw project string field %q", typ, field.Name)
			}
			queryTestRequireNoProjectStringType(t, field.Type, visited)
		}
	case reflect.Map:
		queryTestRequireNoProjectStringType(t, typ.Key(), visited)
		queryTestRequireNoProjectStringType(t, typ.Elem(), visited)
	}
}

func queryTestContextRefsEqual(left, right ContextRef) bool {
	return queryTestOptionalStringsEqual(left.SpaceID, right.SpaceID) &&
		left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}

func queryTestOptionalStringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func queryTestContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
