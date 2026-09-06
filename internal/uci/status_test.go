package uci

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestUCIIndexStatusValidatesClosedStatusAndFreshnessVocabularies(t *testing.T) {
	ref := uciIndexStatusTestRef()
	for _, tc := range []struct {
		name     string
		snapshot IndexStatusSnapshot
	}{
		{
			name:     "observed current",
			snapshot: uciIndexStatusTestSnapshot(ref),
		},
		{
			name: "catching up",
			snapshot: func() IndexStatusSnapshot {
				snapshot := uciIndexStatusTestSnapshot(ref)
				targetGeneration := int64(8)
				snapshot.CheckoutState = IndexStatusCheckoutCatchingUp
				snapshot.PendingPublicationJobCount = 1
				snapshot.PublicationJob = &IndexStatusJob{State: IndexStatusJobRunning, TargetGeneration: &targetGeneration}
				snapshot.Freshness = QueryFreshness{
					State:  QueryFreshnessCatchingUp,
					Method: QueryFreshnessWatchWatermark,
					EnrichmentWatermark: QueryEnrichmentWatermark{
						Sequence: snapshot.ObservedFSSeq,
						State:    QueryEnrichmentPending,
					},
				}
				return snapshot
			}(),
		},
		{
			name: "offline",
			snapshot: func() IndexStatusSnapshot {
				snapshot := uciIndexStatusTestSnapshot(ref)
				snapshot.SourceState = IndexStatusSourceOffline
				snapshot.Freshness = QueryFreshness{
					State:  QueryFreshnessOffline,
					Method: QueryFreshnessNone,
					EnrichmentWatermark: QueryEnrichmentWatermark{
						Sequence: snapshot.ObservedFSSeq,
						State:    QueryEnrichmentUnavailable,
					},
				}
				return snapshot
			}(),
		},
		{
			name: "pinned history",
			snapshot: func() IndexStatusSnapshot {
				snapshot := uciIndexStatusTestSnapshot(ref)
				snapshot.ViewState = IndexStatusViewSuperseded
				snapshot.CurrentViewRelation = IndexStatusCurrentViewDifferent
				snapshot.Freshness = QueryFreshness{
					State:  QueryFreshnessHistorical,
					Method: QueryFreshnessPinnedHistory,
					EnrichmentWatermark: QueryEnrichmentWatermark{
						Sequence: snapshot.ObservedFSSeq,
						State:    QueryEnrichmentCurrent,
					},
				}
				return snapshot
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.snapshot.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	invalid := uciIndexStatusTestSnapshot(ref)
	invalid.SourceState = IndexStatusSourceState("retired")
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() succeeded for a source state outside the closed vocabulary")
	}
}

func TestUCIIndexStatusServiceReturnsEmptyTokenStatusFromExactStore(t *testing.T) {
	ref := uciIndexStatusTestRef()
	want := uciIndexStatusTestSnapshot(ref)
	store := &uciIndexStatusTestStore{snapshot: want}
	service := NewIndexStatusService(store, nil)

	got, err := service.Status(context.Background(), newAuthorizedContext(ref), "")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Status() = %#v, want %#v", got, want)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
	if !indexStatusContextEqual(store.authorized.Ref(), ref) {
		t.Fatalf("store context = %#v, want %#v", store.authorized.Ref(), ref)
	}
}

func TestUCIIndexStatusServiceRefusesBarrierWithoutSyntheticAuthority(t *testing.T) {
	ref := uciIndexStatusTestRef()
	store := &uciIndexStatusTestStore{snapshot: uciIndexStatusTestSnapshot(ref)}
	service := NewIndexStatusService(store, nil)

	_, err := service.Status(context.Background(), newAuthorizedContext(ref), "opaque-daemon-barrier")
	if err == nil {
		t.Fatal("Status() succeeded for a nonempty barrier token")
	}
	if !errors.Is(err, ErrIndexStatusBarrierUnavailable) {
		t.Fatalf("Status() error = %v, want BARRIER_UNAVAILABLE", err)
	}
	var statusErr *IndexStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Status() error type = %T, want *IndexStatusError", err)
	}
	if statusErr.Code() != IndexStatusBarrierUnavailable {
		t.Fatalf("Status() code = %q, want %q", statusErr.Code(), IndexStatusBarrierUnavailable)
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d, want 0; status storage cannot fabricate a barrier", store.calls)
	}
}

type uciIndexStatusTestStore struct {
	snapshot   IndexStatusSnapshot
	err        error
	calls      int
	authorized AuthorizedContext
}

func (store *uciIndexStatusTestStore) LoadIndexStatus(_ context.Context, authorized AuthorizedContext, _ *VectorProfile) (IndexStatusSnapshot, error) {
	store.calls++
	store.authorized = authorized
	return store.snapshot.Clone(), store.err
}

func uciIndexStatusTestRef() ContextRef {
	return ContextRef{
		SourceID:          "11111111-1111-4111-8111-111111111111",
		CheckoutID:        "22222222-2222-4222-8222-222222222222",
		ViewID:            "33333333-3333-4333-8333-333333333333",
		AnalysisProfileID: "44444444-4444-4444-8444-444444444444",
		Generation:        7,
	}
}

func uciIndexStatusTestSnapshot(ref ContextRef) IndexStatusSnapshot {
	zero := int64(0)
	scanStartedAt := time.Date(2026, time.September, 6, 1, 2, 3, 0, time.UTC)
	return IndexStatusSnapshot{
		Context:             ref,
		SourceState:         IndexStatusSourceActive,
		CheckoutState:       IndexStatusCheckoutWatching,
		ViewState:           IndexStatusViewPublished,
		CurrentViewRelation: IndexStatusCurrentViewSelected,
		Dirty:               true,
		ObservedFSSeq:       41,
		Coverage: IndexCoverage{
			Structural:           IndexCoveragePartial,
			Lexical:              IndexCoverageComplete,
			Vector:               IndexCoverageUnavailable,
			ExcludedFiles:        2,
			UnreadableFiles:      3,
			UnresolvedReferences: 5,
		},
		PublishedAt:         scanStartedAt.Add(time.Minute),
		ScanStartedAt:       scanStartedAt,
		ScanCompletedAt:     scanStartedAt.Add(30 * time.Second),
		ChunkCount:          11,
		ReadyEmbeddingCount: 7,
		Embedding: EmbeddingStatus{
			Coverage: IndexCoverageUnavailable,
		},
		Freshness: QueryFreshness{
			State:          QueryFreshnessObservedCurrent,
			Method:         QueryFreshnessWatchWatermark,
			PendingChanges: &zero,
			EnrichmentWatermark: QueryEnrichmentWatermark{
				Sequence: 41,
				State:    QueryEnrichmentCurrent,
			},
		},
	}
}
