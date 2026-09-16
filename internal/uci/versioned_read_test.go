package uci

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestUCIVersionedReadReturnsExactPreExposureResponse(t *testing.T) {
	fixture := newVersionedReadTestFixture()
	store := &versionedReadTestStore{result: VersionedReadStoreResult{Hit: &fixture.hit, Coverage: IndexCoverageComplete}}
	service := NewVersionedReadService(store)

	response, err := service.Read(context.Background(), newAuthorizedContext(fixture.context), fixture.spec)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	versionedReadRequireExactResponse(t, response, fixture, store)
}

func versionedReadRequireExactResponse(t *testing.T, response QueryResponse, fixture versionedReadTestFixture, store *versionedReadTestStore) {
	t.Helper()
	if err := response.ValidatePreExposure(); err != nil {
		t.Fatalf("ValidatePreExposure() error = %v", err)
	}
	versionedReadRequireExactMetadata(t, response)
	versionedReadRequireExactItem(t, response, fixture)
	versionedReadRequireExactStoreCall(t, store, fixture)
}

func versionedReadRequireExactMetadata(t *testing.T, response QueryResponse) {
	t.Helper()
	if response.Exposure != nil {
		t.Fatalf("exposure = %#v, want nil before the MCP boundary", response.Exposure)
	}
	if response.Status != QueryStatusOK {
		t.Fatalf("status = %q, want %q", response.Status, QueryStatusOK)
	}
	if response.Retrieval == nil || response.Retrieval.Mode != QueryRetrievalExact {
		t.Fatalf("retrieval = %#v, want exact", response.Retrieval)
	}
	if response.Coverage == nil || response.Coverage.Structural != IndexCoverageComplete {
		t.Fatalf("coverage = %#v, want complete", response.Coverage)
	}
	if response.Freshness == nil || response.Freshness.State != QueryFreshnessHistorical || response.Freshness.Method != QueryFreshnessPinnedHistory {
		t.Fatalf("freshness = %#v, want pinned historical evidence", response.Freshness)
	}
}

func versionedReadRequireExactItem(t *testing.T, response QueryResponse, fixture versionedReadTestFixture) {
	t.Helper()
	if response.Items == nil || len(*response.Items) != 1 {
		t.Fatalf("items = %#v, want one exact item", response.Items)
	}
	item := (*response.Items)[0]
	if item.Ref != fixture.spec.Entity || item.Span != fixture.spec.Span || item.ContentDigest != fixture.spec.ContentDigest {
		t.Fatalf("item citation = %#v, want entity/span/digest from exact spec", item)
	}
	if item.Excerpt != fixture.hit.Text || item.MatchSources[0] != QueryMatchExact {
		t.Fatalf("item = %#v, want persisted exact text", item)
	}
}

func versionedReadRequireExactStoreCall(t *testing.T, store *versionedReadTestStore, fixture versionedReadTestFixture) {
	t.Helper()
	if len(store.calls) != 1 || store.calls[0].authorized.Ref() != fixture.context || store.calls[0].spec != fixture.spec {
		t.Fatalf("store calls = %#v, want one unchanged authorized exact read", store.calls)
	}
}

func TestUCIVersionedReadMapsClosedMissCoverageAndUnavailableStates(t *testing.T) {
	fixture := newVersionedReadTestFixture()
	for _, test := range []struct {
		name         string
		result       VersionedReadStoreResult
		wantStatus   QueryResponseStatus
		wantError    *QueryError
		wantCoverage IndexCoverageState
	}{
		{
			name:         "complete miss is empty",
			result:       VersionedReadStoreResult{Coverage: IndexCoverageComplete},
			wantStatus:   QueryStatusEmpty,
			wantCoverage: IndexCoverageComplete,
		},
		{
			name:         "partial miss remains partial",
			result:       VersionedReadStoreResult{Coverage: IndexCoveragePartial},
			wantStatus:   QueryStatusPartial,
			wantCoverage: IndexCoveragePartial,
		},
		{
			name: "unavailable view is closed",
			result: VersionedReadStoreResult{
				Coverage:    IndexCoverageUnavailable,
				Unavailable: &QueryError{Code: QueryErrorBuildIncomplete},
			},
			wantStatus:   QueryStatusUnavailable,
			wantError:    &QueryError{Code: QueryErrorBuildIncomplete},
			wantCoverage: IndexCoverageUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewVersionedReadService(&versionedReadTestStore{result: test.result})
			response, err := service.Read(context.Background(), newAuthorizedContext(fixture.context), fixture.spec)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			versionedReadRequireClosedMiss(t, response, test.wantStatus, test.wantError, test.wantCoverage)
		})
	}
}

func versionedReadRequireClosedMiss(t *testing.T, response QueryResponse, wantStatus QueryResponseStatus, wantError *QueryError, wantCoverage IndexCoverageState) {
	t.Helper()
	if err := response.ValidatePreExposure(); err != nil {
		t.Fatalf("ValidatePreExposure() error = %v", err)
	}
	if response.Exposure != nil || response.Status != wantStatus || response.Error == nil && wantError != nil || response.Error != nil && (wantError == nil || *response.Error != *wantError) {
		t.Fatalf("response = %#v, want closed status %q and error %#v", response, wantStatus, wantError)
	}
	if response.Coverage == nil || response.Coverage.Structural != wantCoverage {
		t.Fatalf("coverage = %#v, want %q", response.Coverage, wantCoverage)
	}
	if response.Items == nil || len(*response.Items) != 0 {
		t.Fatalf("items = %#v, want no body on non-hit", response.Items)
	}
}

func TestUCIVersionedReadValidatesIdentifiersSpanDigestAndBounds(t *testing.T) {
	fixture := newVersionedReadTestFixture()
	for _, test := range []struct {
		name   string
		mutate func(*VersionedReadSpec)
	}{
		{
			name: "entity source",
			mutate: func(spec *VersionedReadSpec) {
				spec.Entity.SourceID = ""
			},
		},
		{
			name: "empty span",
			mutate: func(spec *VersionedReadSpec) {
				spec.Span.ByteEnd = spec.Span.ByteStart
			},
		},
		{
			name: "non bare digest",
			mutate: func(spec *VersionedReadSpec) {
				spec.ContentDigest = QueryContentDigest("sha256:" + string(spec.ContentDigest))
			},
		},
		{
			name: "zero max bytes",
			mutate: func(spec *VersionedReadSpec) {
				spec.MaxBytes = 0
			},
		},
		{
			name: "maximum bytes",
			mutate: func(spec *VersionedReadSpec) {
				spec.MaxBytes = VersionedReadMaxBytes + 1
			},
		},
		{
			name: "span exceeds max bytes",
			mutate: func(spec *VersionedReadSpec) {
				spec.MaxBytes = int(spec.Span.ByteEnd-spec.Span.ByteStart) - 1
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := fixture.spec
			test.mutate(&spec)
			store := &versionedReadTestStore{result: VersionedReadStoreResult{Hit: &fixture.hit, Coverage: IndexCoverageComplete}}
			_, err := NewVersionedReadService(store).Read(context.Background(), newAuthorizedContext(fixture.context), spec)
			if err == nil {
				t.Fatal("Read() error = nil, want invalid exact-read request error")
			}
			if len(store.calls) != 0 {
				t.Fatalf("store calls = %#v, invalid request must not reach the store", store.calls)
			}
		})
	}
}

func TestUCIVersionedReadKeepsWorkingCopyIntentMetadataOnly(t *testing.T) {
	fixture := newVersionedReadTestFixture()
	fixture.spec.VerifyWorkingCopy = true
	store := &versionedReadTestStore{result: VersionedReadStoreResult{Hit: &fixture.hit, Coverage: IndexCoverageComplete}}

	response, err := NewVersionedReadService(store).Read(context.Background(), newAuthorizedContext(fixture.context), fixture.spec)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if err := response.ValidatePreExposure(); err != nil {
		t.Fatalf("ValidatePreExposure() error = %v", err)
	}
	if response.Warnings == nil || len(*response.Warnings) != 1 || (*response.Warnings)[0] != VersionedReadWorkingCopyNotVerifiedWarning {
		t.Fatalf("warnings = %#v, want metadata-only working-copy warning", response.Warnings)
	}
	if response.Items == nil || len(*response.Items) != 1 || (*response.Items)[0].Excerpt != fixture.hit.Text {
		t.Fatalf("items = %#v, want the persisted store hit without a disk fallback", response.Items)
	}
	if len(store.calls) != 1 || !store.calls[0].spec.VerifyWorkingCopy {
		t.Fatalf("store calls = %#v, want the intent forwarded without any working-copy bytes", store.calls)
	}
}

type versionedReadTestStoreCall struct {
	authorized AuthorizedContext
	spec       VersionedReadSpec
}

type versionedReadTestStore struct {
	result VersionedReadStoreResult
	calls  []versionedReadTestStoreCall
}

var _ VersionedReadStore = (*versionedReadTestStore)(nil)

func (store *versionedReadTestStore) ReadExact(_ context.Context, authorized AuthorizedContext, spec VersionedReadSpec) (VersionedReadStoreResult, error) {
	store.calls = append(store.calls, versionedReadTestStoreCall{authorized: authorized, spec: spec})
	return store.result, nil
}

type versionedReadTestFixture struct {
	context ContextRef
	spec    VersionedReadSpec
	hit     VersionedReadHit
}

func newVersionedReadTestFixture() versionedReadTestFixture {
	const body = "func VersionedReadFixture() string { return \"persisted body\" }"
	digest := sha256.Sum256([]byte(body))
	contentDigest := QueryContentDigest(fmt.Sprintf("%x", digest))
	contextRef := ContextRef{
		SourceID:          "20000000-0000-4000-8000-000000000091",
		CheckoutID:        "30000000-0000-4000-8000-000000000091",
		ViewID:            "40000000-0000-4000-8000-000000000091",
		AnalysisProfileID: "50000000-0000-4000-8000-000000000091",
		Generation:        91,
	}
	span := QuerySpan{ByteStart: 0, ByteEnd: int64(len(body)), LineStart: 1, LineEnd: 1}
	entity := QueryEntityRef{SourceID: contextRef.SourceID, ViewID: contextRef.ViewID, EntityKey: "fixture.VersionedReadFixture"}
	spec := VersionedReadSpec{
		Entity:        entity,
		Span:          span,
		ContentDigest: contentDigest,
		MaxBytes:      len(body),
	}
	return versionedReadTestFixture{
		context: contextRef,
		spec:    spec,
		hit: VersionedReadHit{
			Entity:           entity,
			Path:             "fixture/versioned_read.go",
			Span:             span,
			ContentDigest:    contentDigest,
			Kind:             QueryItemCode,
			Language:         "go",
			SourceByteLength: int64(len(body)),
			Text:             body,
		},
	}
}

func TestUCIVersionedReadStaleOrForeignEvidenceCannotLeakFakeStoreBody(t *testing.T) {
	fixture := newVersionedReadTestFixture()
	for _, test := range []struct {
		name   string
		mutate func(*VersionedReadSpec)
	}{
		{
			name: "stale digest",
			mutate: func(spec *VersionedReadSpec) {
				spec.ContentDigest = QueryContentDigest(strings.Repeat("0", 64))
			},
		},
		{
			name: "foreign view",
			mutate: func(spec *VersionedReadSpec) {
				spec.Entity.ViewID = "40000000-0000-4000-8000-000000000092"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := fixture.spec
			test.mutate(&spec)
			store := &versionedReadTestStore{result: VersionedReadStoreResult{Coverage: IndexCoverageComplete}}
			response, err := NewVersionedReadService(store).Read(context.Background(), newAuthorizedContext(fixture.context), spec)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if response.Status != QueryStatusEmpty || response.Items == nil || len(*response.Items) != 0 || response.Exposure != nil {
				t.Fatalf("response = %#v, want an exposure-free empty result", response)
			}
		})
	}
}
