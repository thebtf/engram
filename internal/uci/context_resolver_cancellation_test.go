package uci

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cancellationBlockingContextCatalog struct {
	entered chan struct{}
}

func (catalog *cancellationBlockingContextCatalog) LoadContext(ctx context.Context, ref ContextRef) (ContextRecord, error) {
	if ctx.Done() == nil {
		return ContextRecord{Ref: ref, AuthRealm: contextResolverTestRealm}, nil
	}
	close(catalog.entered)
	<-ctx.Done()
	return ContextRecord{}, ctx.Err()
}

func TestUCIContextResolverCancellationDuringSameBindingExposure(t *testing.T) {
	catalog := &cancellationBlockingContextCatalog{entered: make(chan struct{})}
	resolver := NewContextResolver(catalog, &contextResolverAuthorizerFake{}, nil)
	ref := contextResolverTestRefA()
	input := contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, &ref, nil)
	if _, err := resolver.Resolve(context.Background(), input); err != nil {
		t.Fatalf("select context: %v", err)
	}
	_, version, found := resolver.BoundSelectorSnapshot(contextResolverTestClientA)
	if !found {
		t.Fatal("selected binding not found")
	}

	exposureEntered := make(chan struct{})
	releaseExposure := make(chan struct{})
	exposureDone := make(chan bool, 1)
	requestDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	requestStarted := false
	exposureFinished, requestFinished := false, false
	defer func() {
		cancel()
		close(releaseExposure)
		if !exposureFinished {
			select {
			case <-exposureDone:
			case <-time.After(5 * time.Second):
				t.Error("exposure callback did not finish after release")
			}
		}
		if requestStarted && !requestFinished {
			select {
			case <-requestDone:
			case <-time.After(5 * time.Second):
				t.Error("canceled request did not finish after exposure release")
			}
		}
	}()
	go func() {
		exposureDone <- resolver.WithBoundSelector(contextResolverTestClientA, version, func() bool {
			close(exposureEntered)
			<-releaseExposure
			return true
		})
	}()
	select {
	case <-exposureEntered:
	case <-exposureDone:
		exposureFinished = true
		t.Fatal("exposure callback did not acquire the selected binding")
	case <-time.After(5 * time.Second):
		t.Fatal("exposure callback did not start")
	}

	requestStarted = true
	go func() {
		_, err := resolver.Resolve(ctx, contextResolverTestInput(contextResolverTestClientA, contextResolverTestPrincipal, nil, nil))
		requestDone <- err
	}()
	select {
	case <-catalog.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("same-session request did not enter the catalog")
	}
	cancel()
	select {
	case err := <-requestDone:
		requestFinished = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resolve error = %v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("canceled same-session request waited for the blocked exposure callback")
	}
}
