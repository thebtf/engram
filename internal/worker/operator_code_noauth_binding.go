package worker

import (
	"context"
	"crypto/subtle"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
)

// noAuthCodeBindings is process-local document state, not a persisted user or
// browser grant. A restart requires a new handshake; UCI sources remain durable.
type noAuthCodeBindings struct {
	mu      sync.Mutex
	tabs    map[string]noAuthCodeTab
	cursors map[string]noAuthCodeCursor
}

type noAuthCodeCursor struct {
	binding gormdb.BrowserCodeContinuationBinding
	service string
	expires time.Time
}

type noAuthCodeTab struct {
	proof, resume, reload string
	lease, expires        time.Time
	pinned                *BrowserBindingContext
}

func newNoAuthCodeBindings() *noAuthCodeBindings {
	return &noAuthCodeBindings{tabs: make(map[string]noAuthCodeTab), cursors: make(map[string]noAuthCodeCursor)}
}

func (b *noAuthCodeBindings) Handshake(_ context.Context, identity auth.Identity, _ string, input BrowserBindingHandshakeInput) (BrowserBindingTransition, error) {
	if identity.Source != auth.SourceAuthDisabled || validateBrowserBindingDocumentNonce(input.DocumentNonce) != nil {
		return BrowserBindingTransition{}, ErrBrowserBindingDenied
	}
	proof, err := newBrowserBindingMaterial()
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	resume, err := newBrowserBindingMaterial()
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	reload, err := newBrowserBindingMaterial()
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	id := uuid.NewString()
	b.mu.Lock()
	now := time.Now()
	atCapacity := len(b.tabs) >= 1024
	for key, tab := range b.tabs {
		if now.After(tab.expires) || (atCapacity && now.After(tab.lease)) {
			delete(b.tabs, key)
		}
	}
	if len(b.tabs) >= 1024 {
		b.mu.Unlock()
		return BrowserBindingTransition{}, ErrBrowserBindingDenied
	}
	b.tabs[id] = noAuthCodeTab{proof: proof, resume: resume, reload: reload, lease: now.Add(defaultBrowserBindingLeaseTTL), expires: now.Add(defaultBrowserBindingTTL)}
	b.mu.Unlock()
	return BrowserBindingTransition{State: BrowserBindingReady, TabBindingID: id, DocumentProof: proof, ResumeNonce: resume, ReloadToken: reload}, nil
}

func (b *noAuthCodeBindings) Guard(_ context.Context, identity auth.Identity, _ string, proof BrowserBindingProof) (BrowserBindingGuarded, error) {
	if identity.Source != auth.SourceAuthDisabled {
		return BrowserBindingGuarded{}, ErrBrowserBindingDenied
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tab, ok := b.tabs[proof.TabBindingID]
	if !ok || time.Now().After(tab.lease) || time.Now().After(tab.expires) || subtle.ConstantTimeCompare([]byte(tab.proof), []byte(proof.DocumentProof)) != 1 {
		return BrowserBindingGuarded{}, ErrBrowserBindingDenied
	}
	return BrowserBindingGuarded{TabBindingID: proof.TabBindingID, Pinned: tab.pinned}, nil
}

func (b *noAuthCodeBindings) Pin(ctx context.Context, identity auth.Identity, proof BrowserBindingProof, ref uci.ContextRef) error {
	if _, err := b.Guard(ctx, identity, "", proof); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tab, ok := b.tabs[proof.TabBindingID]
	if !ok || time.Now().After(tab.lease) || subtle.ConstantTimeCompare([]byte(tab.proof), []byte(proof.DocumentProof)) != 1 {
		return ErrBrowserBindingDenied
	}
	tab.pinned = &BrowserBindingContext{SourceID: ref.SourceID, CheckoutID: ref.CheckoutID, ViewID: ref.ViewID, AnalysisProfileID: ref.AnalysisProfileID, Generation: ref.Generation}
	b.tabs[proof.TabBindingID] = tab
	return nil
}

func (b *noAuthCodeBindings) Renew(ctx context.Context, identity auth.Identity, session string, proof BrowserBindingProof) error {
	if _, err := b.Guard(ctx, identity, session, proof); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tab, ok := b.tabs[proof.TabBindingID]
	if !ok || time.Now().After(tab.lease) || time.Now().After(tab.expires) || subtle.ConstantTimeCompare([]byte(tab.proof), []byte(proof.DocumentProof)) != 1 {
		return ErrBrowserBindingDenied
	}
	tab.lease = time.Now().Add(defaultBrowserBindingLeaseTTL)
	b.tabs[proof.TabBindingID] = tab
	return nil
}

func (b *noAuthCodeBindings) Close(ctx context.Context, identity auth.Identity, session string, proof BrowserBindingProof) error {
	if _, err := b.Guard(ctx, identity, session, proof); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tab, ok := b.tabs[proof.TabBindingID]
	if !ok || time.Now().After(tab.lease) || subtle.ConstantTimeCompare([]byte(tab.proof), []byte(proof.DocumentProof)) != 1 {
		return ErrBrowserBindingDenied
	}
	tab.proof = ""
	tab.lease = time.Time{}
	b.tabs[proof.TabBindingID] = tab
	return nil
}

func (b *noAuthCodeBindings) Resume(_ context.Context, identity auth.Identity, _ string, input BrowserBindingResumeInput) (BrowserBindingTransition, error) {
	if identity.Source != auth.SourceAuthDisabled || validateBrowserBindingResumeInput(input) != nil {
		return BrowserBindingTransition{}, ErrBrowserBindingDenied
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tab, ok := b.tabs[input.TabBindingID]
	if !ok || time.Now().After(tab.expires) || subtle.ConstantTimeCompare([]byte(tab.resume), []byte(input.ResumeNonce)) != 1 || subtle.ConstantTimeCompare([]byte(tab.reload), []byte(input.ReloadToken)) != 1 {
		return BrowserBindingTransition{}, ErrBrowserBindingDenied
	}
	if time.Now().Before(tab.lease) {
		return BrowserBindingTransition{State: BrowserBindingReloadPending}, nil
	}
	proof, err := newBrowserBindingMaterial()
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	reload, err := newBrowserBindingMaterial()
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	tab.proof, tab.reload, tab.lease = proof, reload, time.Now().Add(defaultBrowserBindingLeaseTTL)
	b.tabs[input.TabBindingID] = tab
	return BrowserBindingTransition{State: BrowserBindingReady, TabBindingID: input.TabBindingID, DocumentProof: proof, ResumeNonce: tab.resume, ReloadToken: reload}, nil
}

var _ operatorCodeBindingApplication = (*noAuthCodeBindings)(nil)

func (b *noAuthCodeBindings) cursor(ref string, binding gormdb.BrowserCodeContinuationBinding) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cursor, ok := b.cursors[ref]
	if !ok || cursor.binding != binding || time.Now().After(cursor.expires) {
		return "", gormdb.ErrBrowserCodeContinuationDenied
	}
	return cursor.service, nil
}

func (b *noAuthCodeBindings) advanceCursor(previous string, binding gormdb.BrowserCodeContinuationBinding, service string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if previous != "" {
		cursor, ok := b.cursors[previous]
		if !ok || cursor.binding != binding || time.Now().After(cursor.expires) {
			return "", gormdb.ErrBrowserCodeContinuationDenied
		}
		delete(b.cursors, previous)
	}
	if service == "" {
		return "", nil
	}
	now := time.Now()
	oldest := ""
	var oldestExpiry time.Time
	for key, cursor := range b.cursors {
		if !now.Before(cursor.expires) {
			delete(b.cursors, key)
			continue
		}
		if oldest == "" || cursor.expires.Before(oldestExpiry) {
			oldest, oldestExpiry = key, cursor.expires
		}
	}
	if len(b.cursors) >= 2048 {
		delete(b.cursors, oldest)
	}
	ref := uuid.NewString()
	b.cursors[ref] = noAuthCodeCursor{binding: binding, service: service, expires: now.Add(10 * time.Minute)}
	return ref, nil
}
