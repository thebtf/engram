package worker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

const (
	defaultBrowserBindingLeaseTTL = 2 * time.Minute
	defaultBrowserBindingTTL      = 24 * time.Hour
)

// ErrBrowserBindingDenied is deliberately non-disclosing. HTTP and release
// owners map it to their common denied response without revealing why a
// binding, proof, token, or session was refused.
var ErrBrowserBindingDenied = errors.New("browser binding denied")

// BrowserBindingTransitionState is the only state returned by a binding
// transition. It never includes a pinned ContextRef.
type BrowserBindingTransitionState string

const (
	BrowserBindingReady         BrowserBindingTransitionState = "TAB_BINDING_READY"
	BrowserBindingCollision     BrowserBindingTransitionState = "TAB_BINDING_COLLISION"
	BrowserBindingAmbiguous     BrowserBindingTransitionState = "TAB_BOOTSTRAP_AMBIGUOUS"
	BrowserBindingReloadPending BrowserBindingTransitionState = "RELOAD_PENDING"
)

// BrowserBindingHandshakeInput is the browser-classified handshake input. The
// browser's opener/navigation classifier is not access authority; Ambiguous
// only selects the contract's fresh, unselected fallback.
type BrowserBindingHandshakeInput struct {
	DocumentNonce      string
	CopiedTabBindingID string
	CopiedResumeNonce  string
	Ambiguous          bool
}

// BrowserBindingResumeInput is accepted only after the browser's normalized
// no-opener reload classifier selected resume.
type BrowserBindingResumeInput struct {
	TabBindingID  string
	ResumeNonce   string
	ReloadToken   string
	DocumentNonce string
}

// BrowserBindingTransition contains browser-held material returned only for a
// newly live document. Its values are never written by the persistence store.
type BrowserBindingTransition struct {
	State         BrowserBindingTransitionState
	TabBindingID  string
	DocumentProof string
	ResumeNonce   string
	ReloadToken   string
}

// BrowserBindingProof is the binding ID plus current document proof presented
// by every post-transition Code request and document lease operation.
type BrowserBindingProof struct {
	TabBindingID  string
	DocumentProof string
}

// BrowserBindingContext is the exact UCI ContextRef projection that the HTTP/UCI
// owners authorize before invoking Pin. A binding itself is never authority.
type BrowserBindingContext struct {
	SourceID          string
	CheckoutID        string
	ViewID            string
	AnalysisProfileID string
	Generation        int64
}

// BrowserBindingGuarded is private-to-composition binding state. Callers must
// independently reauthorize its optional pin before any contextual output.
type BrowserBindingGuarded struct {
	TabBindingID string
	Pinned       *BrowserBindingContext
}

type browserTabBindingStore interface {
	Create(context.Context, gormdb.BrowserTabBindingCreate) (gormdb.BrowserTabBinding, error)
	CreateFromCopy(context.Context, gormdb.BrowserTabBindingCopyCreate) (gormdb.BrowserTabBinding, bool, error)
	Resume(context.Context, gormdb.BrowserTabBindingResume) (gormdb.BrowserTabBinding, gormdb.BrowserTabBindingResumeState, error)
	Guard(context.Context, gormdb.BrowserTabBindingGuard) (gormdb.BrowserTabBinding, error)
	Renew(context.Context, gormdb.BrowserTabBindingLease) error
	Close(context.Context, gormdb.BrowserTabBindingLease) error
	Pin(context.Context, gormdb.BrowserTabBindingPin) error
	DestroySession(context.Context, string) error
}

// BrowserBindingApplication owns tab binding transitions and no other browser
// authority. In particular, it neither issues grants nor serializes routes.
type BrowserBindingApplication struct {
	store      browserTabBindingStore
	leaseTTL   time.Duration
	bindingTTL time.Duration
	now        func() time.Time
	material   func() (string, error)
}

// NewBrowserBindingApplication creates the server-owned browser binding state
// machine. The composition owner wires it to the binding persistence store.
func NewBrowserBindingApplication(store *gormdb.BrowserTabBindingStore) *BrowserBindingApplication {
	return &BrowserBindingApplication{
		store:      store,
		leaseTTL:   defaultBrowserBindingLeaseTTL,
		bindingTTL: defaultBrowserBindingTTL,
		now:        func() time.Time { return time.Now().UTC() },
		material:   newBrowserBindingMaterial,
	}
}

// Handshake creates a fresh binding for a direct document or a fresh opener. A
// complete copied pair takes the collision path; an incomplete/invalid pair or
// browser-classified ambiguity is deliberately fresh and unselected.
func (a *BrowserBindingApplication) Handshake(ctx context.Context, identity auth.Identity, sessionID string, in BrowserBindingHandshakeInput) (BrowserBindingTransition, error) {
	caller, err := a.caller(identity, sessionID)
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	if err := validateBrowserBindingDocumentNonce(in.DocumentNonce); err != nil {
		return BrowserBindingTransition{}, err
	}
	if err := a.requireStore(); err != nil {
		return BrowserBindingTransition{}, err
	}
	proof, resumeNonce, reloadToken, err := a.issueDocumentMaterial()
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	create := gormdb.BrowserTabBindingCreate{
		Caller:                 caller,
		DocumentProofDigest:    browserBindingDigest(proof),
		ResumeNonceDigest:      browserBindingDigest(resumeNonce),
		ReloadTokenDigest:      browserBindingDigest(reloadToken),
		DocumentLeaseExpiresAt: a.currentTime().Add(a.leaseDuration()),
		BindingExpiresAt:       a.currentTime().Add(a.bindingDuration()),
	}

	result := BrowserBindingTransition{
		State:         BrowserBindingReady,
		DocumentProof: proof,
		ResumeNonce:   resumeNonce,
		ReloadToken:   reloadToken,
	}
	if in.Ambiguous || (in.CopiedTabBindingID == "") != (in.CopiedResumeNonce == "") {
		binding, err := a.store.Create(ctx, create)
		if err != nil {
			return BrowserBindingTransition{}, browserBindingStoreError(err)
		}
		result.TabBindingID = binding.TabBindingID
		if in.Ambiguous || in.CopiedTabBindingID != "" || in.CopiedResumeNonce != "" {
			result.State = BrowserBindingAmbiguous
		}
		return result, nil
	}
	if in.CopiedTabBindingID == "" {
		binding, err := a.store.Create(ctx, create)
		if err != nil {
			return BrowserBindingTransition{}, browserBindingStoreError(err)
		}
		result.TabBindingID = binding.TabBindingID
		return result, nil
	}

	binding, collision, err := a.store.CreateFromCopy(ctx, gormdb.BrowserTabBindingCopyCreate{
		Create:                  create,
		CopiedTabBindingID:      in.CopiedTabBindingID,
		CopiedResumeNonceDigest: browserBindingDigest(in.CopiedResumeNonce),
	})
	if err != nil {
		return BrowserBindingTransition{}, browserBindingStoreError(err)
	}
	result.TabBindingID = binding.TabBindingID
	if collision {
		result.State = BrowserBindingCollision
	} else {
		result.State = BrowserBindingAmbiguous
	}
	return result, nil
}

// Resume creates a new current document only after a closed or expired prior
// lease. A pending result exposes no proof, token, or pin and does not consume
// the current token, so the browser retries the same material after close/expiry.
func (a *BrowserBindingApplication) Resume(ctx context.Context, identity auth.Identity, sessionID string, in BrowserBindingResumeInput) (BrowserBindingTransition, error) {
	caller, err := a.caller(identity, sessionID)
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	if err := validateBrowserBindingResumeInput(in); err != nil {
		return BrowserBindingTransition{}, err
	}
	if err := a.requireStore(); err != nil {
		return BrowserBindingTransition{}, err
	}
	proof, err := a.nextMaterial()
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	reloadToken, err := a.nextMaterial()
	if err != nil {
		return BrowserBindingTransition{}, err
	}
	binding, state, err := a.store.Resume(ctx, gormdb.BrowserTabBindingResume{
		Caller:                 caller,
		TabBindingID:           in.TabBindingID,
		ResumeNonceDigest:      browserBindingDigest(in.ResumeNonce),
		ReloadTokenDigest:      browserBindingDigest(in.ReloadToken),
		NewDocumentProofDigest: browserBindingDigest(proof),
		NewReloadTokenDigest:   browserBindingDigest(reloadToken),
		DocumentLeaseExpiresAt: a.currentTime().Add(a.leaseDuration()),
	})
	if err != nil {
		return BrowserBindingTransition{}, browserBindingStoreError(err)
	}
	if state == gormdb.BrowserTabBindingReloadPending {
		return BrowserBindingTransition{State: BrowserBindingReloadPending}, nil
	}
	if state != gormdb.BrowserTabBindingResumed {
		return BrowserBindingTransition{}, ErrBrowserBindingDenied
	}
	return BrowserBindingTransition{
		State:         BrowserBindingReady,
		TabBindingID:  binding.TabBindingID,
		DocumentProof: proof,
		ResumeNonce:   in.ResumeNonce,
		ReloadToken:   reloadToken,
	}, nil
}

// Guard proves one current document and returns only the binding's optional pin
// to trusted composition code. It does not authorize that pin or release data.
func (a *BrowserBindingApplication) Guard(ctx context.Context, identity auth.Identity, sessionID string, proof BrowserBindingProof) (BrowserBindingGuarded, error) {
	caller, err := a.caller(identity, sessionID)
	if err != nil {
		return BrowserBindingGuarded{}, err
	}
	if err := validateBrowserBindingProof(proof); err != nil {
		return BrowserBindingGuarded{}, err
	}
	if err := a.requireStore(); err != nil {
		return BrowserBindingGuarded{}, err
	}
	binding, err := a.store.Guard(ctx, gormdb.BrowserTabBindingGuard{
		Caller:              caller,
		TabBindingID:        proof.TabBindingID,
		DocumentProofDigest: browserBindingDigest(proof.DocumentProof),
	})
	if err != nil {
		return BrowserBindingGuarded{}, browserBindingStoreError(err)
	}
	guarded := BrowserBindingGuarded{TabBindingID: binding.TabBindingID}
	if pinned, ok := browserBindingContextFromStore(binding); ok {
		guarded.Pinned = &pinned
	}
	return guarded, nil
}

// Renew extends only the current live document lease.
func (a *BrowserBindingApplication) Renew(ctx context.Context, identity auth.Identity, sessionID string, proof BrowserBindingProof) error {
	caller, err := a.caller(identity, sessionID)
	if err != nil {
		return err
	}
	if err := validateBrowserBindingProof(proof); err != nil {
		return err
	}
	if err := a.requireStore(); err != nil {
		return err
	}
	return browserBindingStoreError(a.store.Renew(ctx, gormdb.BrowserTabBindingLease{
		BrowserTabBindingGuard: gormdb.BrowserTabBindingGuard{
			Caller:              caller,
			TabBindingID:        proof.TabBindingID,
			DocumentProofDigest: browserBindingDigest(proof.DocumentProof),
		},
		DocumentLeaseExpiresAt: a.currentTime().Add(a.leaseDuration()),
	}))
}

// Close acknowledges only the current live document lease.
func (a *BrowserBindingApplication) Close(ctx context.Context, identity auth.Identity, sessionID string, proof BrowserBindingProof) error {
	caller, err := a.caller(identity, sessionID)
	if err != nil {
		return err
	}
	if err := validateBrowserBindingProof(proof); err != nil {
		return err
	}
	if err := a.requireStore(); err != nil {
		return err
	}
	return browserBindingStoreError(a.store.Close(ctx, gormdb.BrowserTabBindingLease{
		BrowserTabBindingGuard: gormdb.BrowserTabBindingGuard{
			Caller:              caller,
			TabBindingID:        proof.TabBindingID,
			DocumentProofDigest: browserBindingDigest(proof.DocumentProof),
		},
	}))
}

// Pin stores an exact already-authorized ContextRef projection only for the
// current document. The caller that invokes it owns grant and UCI validation.
func (a *BrowserBindingApplication) Pin(ctx context.Context, identity auth.Identity, sessionID string, proof BrowserBindingProof, pinned BrowserBindingContext) error {
	caller, err := a.caller(identity, sessionID)
	if err != nil {
		return err
	}
	if err := validateBrowserBindingProof(proof); err != nil {
		return err
	}
	if err := a.requireStore(); err != nil {
		return err
	}
	return browserBindingStoreError(a.store.Pin(ctx, gormdb.BrowserTabBindingPin{
		BrowserTabBindingGuard: gormdb.BrowserTabBindingGuard{
			Caller:              caller,
			TabBindingID:        proof.TabBindingID,
			DocumentProofDigest: browserBindingDigest(proof.DocumentProof),
		},
		Context: gormdb.BrowserTabBindingContext{
			SourceID:          pinned.SourceID,
			CheckoutID:        pinned.CheckoutID,
			ViewID:            pinned.ViewID,
			AnalysisProfileID: pinned.AnalysisProfileID,
			Generation:        pinned.Generation,
		},
	}))
}

// DestroySession clears bindings when the authenticated browser session ends.
// It is intended for the authenticated session/logout lifecycle owner.
func (a *BrowserBindingApplication) DestroySession(ctx context.Context, sessionID string) error {
	if err := a.requireStore(); err != nil {
		return err
	}
	return browserBindingStoreError(a.store.DestroySession(ctx, sessionID))
}

const browserBindingForbiddenDelimiters = "\x00\r\n"

func (a *BrowserBindingApplication) caller(identity auth.Identity, sessionID string) (gormdb.BrowserTabBindingCaller, error) {
	subject, ok := identity.SessionBrowserSubject()
	if !ok || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID || strings.ContainsAny(sessionID, browserBindingForbiddenDelimiters) {
		return gormdb.BrowserTabBindingCaller{}, ErrBrowserBindingDenied
	}
	return gormdb.BrowserTabBindingCaller{SubjectUserID: subject.UserID, SessionID: sessionID}, nil
}

func (a *BrowserBindingApplication) requireStore() error {
	if a == nil || a.store == nil {
		return fmt.Errorf("browser binding application is not configured")
	}
	return nil
}

func (a *BrowserBindingApplication) issueDocumentMaterial() (proof, resumeNonce, reloadToken string, err error) {
	proof, err = a.nextMaterial()
	if err != nil {
		return "", "", "", err
	}
	resumeNonce, err = a.nextMaterial()
	if err != nil {
		return "", "", "", err
	}
	reloadToken, err = a.nextMaterial()
	if err != nil {
		return "", "", "", err
	}
	return proof, resumeNonce, reloadToken, nil
}

func (a *BrowserBindingApplication) nextMaterial() (string, error) {
	if a.material != nil {
		return a.material()
	}
	return newBrowserBindingMaterial()
}

func (a *BrowserBindingApplication) currentTime() time.Time {
	if a.now != nil {
		return a.now().UTC()
	}
	return time.Now().UTC()
}

func (a *BrowserBindingApplication) leaseDuration() time.Duration {
	if a.leaseTTL > 0 {
		return a.leaseTTL
	}
	return defaultBrowserBindingLeaseTTL
}

func (a *BrowserBindingApplication) bindingDuration() time.Duration {
	if a.bindingTTL > a.leaseDuration() {
		return a.bindingTTL
	}
	return defaultBrowserBindingTTL
}

func browserBindingStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gormdb.ErrBrowserTabBindingDenied) {
		return ErrBrowserBindingDenied
	}
	return err
}

func browserBindingDigest(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func newBrowserBindingMaterial() (string, error) {
	var material [32]byte
	if _, err := rand.Read(material[:]); err != nil {
		return "", fmt.Errorf("browser binding material: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(material[:]), nil
}

func validateBrowserBindingDocumentNonce(value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, browserBindingForbiddenDelimiters) {
		return ErrBrowserBindingDenied
	}
	return nil
}

func validateBrowserBindingResumeInput(in BrowserBindingResumeInput) error {
	if err := validateBrowserBindingDocumentNonce(in.DocumentNonce); err != nil {
		return err
	}
	if strings.TrimSpace(in.TabBindingID) == "" || strings.TrimSpace(in.ResumeNonce) == "" || strings.TrimSpace(in.ReloadToken) == "" ||
		strings.TrimSpace(in.TabBindingID) != in.TabBindingID || strings.TrimSpace(in.ResumeNonce) != in.ResumeNonce || strings.TrimSpace(in.ReloadToken) != in.ReloadToken ||
		strings.ContainsAny(in.TabBindingID+in.ResumeNonce+in.ReloadToken, browserBindingForbiddenDelimiters) {
		return ErrBrowserBindingDenied
	}
	return nil
}

func validateBrowserBindingProof(proof BrowserBindingProof) error {
	if strings.TrimSpace(proof.TabBindingID) == "" || strings.TrimSpace(proof.DocumentProof) == "" ||
		strings.TrimSpace(proof.TabBindingID) != proof.TabBindingID || strings.TrimSpace(proof.DocumentProof) != proof.DocumentProof ||
		strings.ContainsAny(proof.TabBindingID+proof.DocumentProof, browserBindingForbiddenDelimiters) {
		return ErrBrowserBindingDenied
	}
	return nil
}

func browserBindingContextFromStore(binding gormdb.BrowserTabBinding) (BrowserBindingContext, bool) {
	if binding.PinnedSourceID == nil || binding.PinnedCheckoutID == nil || binding.PinnedViewID == nil || binding.PinnedAnalysisProfileID == nil || binding.PinnedGeneration == nil {
		return BrowserBindingContext{}, false
	}
	return BrowserBindingContext{
		SourceID:          *binding.PinnedSourceID,
		CheckoutID:        *binding.PinnedCheckoutID,
		ViewID:            *binding.PinnedViewID,
		AnalysisProfileID: *binding.PinnedAnalysisProfileID,
		Generation:        *binding.PinnedGeneration,
	}, true
}
