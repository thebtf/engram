package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

type cancellationAwareContextCatalog struct {
	waitForCancelCheckout string
	*uciCodebaseContextCatalogFake
}

func (catalog *cancellationAwareContextCatalog) LoadContext(ctx context.Context, ref uci.ContextRef) (uci.ContextRecord, error) {
	if ref.CheckoutID == catalog.waitForCancelCheckout {
		<-ctx.Done()
		return uci.ContextRecord{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return uci.ContextRecord{}, err
	}
	return catalog.uciCodebaseContextCatalogFake.LoadContext(ctx, ref)
}

type blockingExposureStore struct {
	*uciCodebaseExposureStoreFake
	entered chan struct{}
	release chan struct{}
}

func (store *blockingExposureStore) AppendExposure(ctx context.Context, record uci.ExposureRecord) (uci.ExposureRecord, error) {
	close(store.entered)
	select {
	case <-store.release:
		return store.uciCodebaseExposureStoreFake.AppendExposure(ctx, record)
	case <-ctx.Done():
		return uci.ExposureRecord{}, ctx.Err()
	}
}

func TestUCIDefaultResolverCancellationDuringOtherClientExposure(t *testing.T) {
	fixture := newUCICodeReadFixture(t)
	catalog := &cancellationAwareContextCatalog{uciCodebaseContextCatalogFake: fixture.catalog}
	fixture.uciCodebaseContextFixture.application.resolver = uci.NewContextResolver(catalog, fixture.authorizer, catalog)
	fixture.selectContext(t, fixture.clientA, fixture.refA)
	fixture.selectContext(t, fixture.clientB, fixture.refB)
	catalog.waitForCancelCheckout = fixture.refB.CheckoutID

	artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
	fixture.application.queryResponses[fixture.refA.CheckoutID] = uciCodeReadQueryResponse(t, fixture.refA, &artifact, uci.QueryStatusOK, "", nil, false)
	store := &blockingExposureStore{
		uciCodebaseExposureStoreFake: &uciCodebaseExposureStoreFake{},
		entered:                      make(chan struct{}),
		release:                      make(chan struct{}),
	}
	fixture.server.SetUCIExposureRecorder(uci.NewExposureRecorder(store, uci.NewExposureHealthController(true)))
	params, err := json.Marshal(map[string]any{"name": "codebase_search", "arguments": map[string]any{"query": "StoredVersion"}})
	require.NoError(t, err)

	aDone := make(chan *Response, 1)
	bDone := make(chan error, 1)
	bStarted := false
	aFinished, bFinished := false, false
	defer func() {
		close(store.release)
		if !aFinished {
			select {
			case <-aDone:
			case <-time.After(5 * time.Second):
				t.Error("client A did not finish after releasing the exposure append")
			}
		}
		if bStarted && !bFinished {
			select {
			case <-bDone:
			case <-time.After(5 * time.Second):
				t.Error("client B did not finish after releasing the exposure append")
			}
		}
	}()
	go func() {
		aDone <- fixture.server.HandleRequest(fixture.clientA, &Request{JSONRPC: "2.0", ID: "blocked-a", Method: "tools/call", Params: params})
	}()
	select {
	case <-store.entered:
	case response := <-aDone:
		aFinished = true
		t.Fatalf("client A returned before entering exposure append: %+v", response)
	case <-time.After(5 * time.Second):
		t.Fatal("client A did not enter exposure append")
	}

	ctx, cancel := context.WithCancel(fixture.clientB)
	defer cancel()
	started := make(chan struct{})
	bStarted = true
	go func() {
		close(started)
		_, err := fixture.uciCodebaseContextFixture.application.resolver.Resolve(ctx, uci.ResolveContextInput{
			ClientSessionID: "mcp-client-b", AuthRealm: uciCodebaseContextTestRealm, Principal: "agent/b",
		})
		bDone <- err
	}()
	<-started
	cancel()
	select {
	case err := <-bDone:
		bFinished = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("client B resolve error = %v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("client B default resolver did not finish after cancellation while client A's exposure append was blocked")
	}
}
