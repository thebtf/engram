package uci

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type gateWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *gateWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Done()
}

func TestUCIContextResolverCanceledSelectionDoesNotBind(t *testing.T) {
	resolver := NewContextResolver(nil, nil, nil)
	ref := contextResolverTestRefA()
	selector, err := ContextIndexBindingSelector(ref)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := resolver.bindSelector(ctx, contextResolverTestClientA, selector); !errors.Is(err, context.Canceled) {
		t.Fatalf("bind error = %v, want cancellation", err)
	}
	if _, _, found := resolver.BoundSelectorSnapshot(contextResolverTestClientA); found {
		t.Fatal("canceled selection created a binding")
	}
}

func TestUCIContextResolverCanceledReplacementDoesNotBindAfterExposure(t *testing.T) {
	resolver := NewContextResolver(nil, nil, nil)
	refA, refB := contextResolverTestRefA(), contextResolverTestRefB()
	selectorA, err := ContextIndexBindingSelector(refA)
	if err != nil {
		t.Fatal(err)
	}
	selectorB, err := ContextIndexBindingSelector(refB)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.bindSelector(context.Background(), contextResolverTestClientA, selectorA); err != nil {
		t.Fatal(err)
	}
	_, version, _ := resolver.BoundSelectorSnapshot(contextResolverTestClientA)
	entered, release, exposureDone := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	go func() {
		exposureDone <- resolver.WithBoundSelector(contextResolverTestClientA, version, func() bool {
			close(entered)
			<-release
			return true
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("exposure did not start")
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &gateWaitContext{Context: base, entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- resolver.bindSelector(ctx, contextResolverTestClientA, selectorB) }()
	select {
	case <-ctx.entered:
	case err := <-done:
		close(release)
		t.Fatalf("replacement returned before waiting at the gate: %v", err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("replacement did not reach the gate")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			close(release)
			t.Fatalf("bind error = %v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("canceled replacement waited for exposure")
	}
	close(release)
	if !<-exposureDone {
		t.Fatal("exposure failed")
	}
	selected, currentVersion, found := resolver.BoundSelectorSnapshot(contextResolverTestClientA)
	if !found || currentVersion != version || !sameIndexBindingSelector(selected, selectorA) {
		t.Fatal("canceled replacement changed the selected binding")
	}
}
