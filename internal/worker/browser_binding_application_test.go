package worker

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

type recordingBrowserTabBindingStore struct {
	creates     []gormdb.BrowserTabBindingCreate
	copyCreates []gormdb.BrowserTabBindingCopyCreate
	resumes     []gormdb.BrowserTabBindingResume

	copyCollision bool
	resumeState   gormdb.BrowserTabBindingResumeState
}

func (store *recordingBrowserTabBindingStore) Create(_ context.Context, in gormdb.BrowserTabBindingCreate) (gormdb.BrowserTabBinding, error) {
	store.creates = append(store.creates, in)
	return gormdb.BrowserTabBinding{TabBindingID: uuid.NewString()}, nil
}

func (store *recordingBrowserTabBindingStore) CreateFromCopy(_ context.Context, in gormdb.BrowserTabBindingCopyCreate) (gormdb.BrowserTabBinding, bool, error) {
	store.copyCreates = append(store.copyCreates, in)
	return gormdb.BrowserTabBinding{TabBindingID: uuid.NewString()}, store.copyCollision, nil
}

func (store *recordingBrowserTabBindingStore) Resume(_ context.Context, in gormdb.BrowserTabBindingResume) (gormdb.BrowserTabBinding, gormdb.BrowserTabBindingResumeState, error) {
	store.resumes = append(store.resumes, in)
	return gormdb.BrowserTabBinding{TabBindingID: in.TabBindingID}, store.resumeState, nil
}

func (store *recordingBrowserTabBindingStore) Guard(context.Context, gormdb.BrowserTabBindingGuard) (gormdb.BrowserTabBinding, error) {
	return gormdb.BrowserTabBinding{}, nil
}

func (store *recordingBrowserTabBindingStore) Renew(context.Context, gormdb.BrowserTabBindingLease) error {
	return nil
}

func (store *recordingBrowserTabBindingStore) Close(context.Context, gormdb.BrowserTabBindingLease) error {
	return nil
}

func (store *recordingBrowserTabBindingStore) Pin(context.Context, gormdb.BrowserTabBindingPin) error {
	return nil
}

func (store *recordingBrowserTabBindingStore) DestroySession(context.Context, string) error {
	return nil
}

func TestBrowserBindingApplication_HandshakeOnlyPassesDigests(t *testing.T) {
	store := &recordingBrowserTabBindingStore{}
	app := &BrowserBindingApplication{store: store, leaseTTL: time.Minute, bindingTTL: time.Hour}
	caller := auth.SessionForBrowserUser("operator", 41)

	result, err := app.Handshake(context.Background(), caller, "browser-session-1", BrowserBindingHandshakeInput{
		DocumentNonce: "document-nonce-1",
	})
	require.NoError(t, err)
	require.Equal(t, BrowserBindingReady, result.State)
	require.NotEmpty(t, result.TabBindingID)
	require.NotEmpty(t, result.DocumentProof)
	require.NotEmpty(t, result.ResumeNonce)
	require.NotEmpty(t, result.ReloadToken)
	require.Len(t, store.creates, 1)
	require.Empty(t, store.copyCreates)

	created := store.creates[0]
	require.Equal(t, int64(41), created.Caller.SubjectUserID)
	require.Equal(t, "browser-session-1", created.Caller.SessionID)
	require.Equal(t, browserBindingTestDigest(result.DocumentProof), created.DocumentProofDigest)
	require.Equal(t, browserBindingTestDigest(result.ResumeNonce), created.ResumeNonceDigest)
	require.Equal(t, browserBindingTestDigest(result.ReloadToken), created.ReloadTokenDigest)
	require.NotContains(t, string(created.DocumentProofDigest), result.DocumentProof)
	require.NotContains(t, string(created.ResumeNonceDigest), result.ResumeNonce)
	require.NotContains(t, string(created.ReloadTokenDigest), result.ReloadToken)
}

func TestBrowserBindingApplication_ClassifiesCopyAndAmbiguityWithoutReplacingOriginal(t *testing.T) {
	caller := auth.SessionForBrowserUser("operator", 41)
	input := BrowserBindingHandshakeInput{
		DocumentNonce:      "document-nonce-copy",
		CopiedTabBindingID: uuid.NewString(),
		CopiedResumeNonce:  "copied-resume-nonce",
	}

	store := &recordingBrowserTabBindingStore{copyCollision: true}
	app := &BrowserBindingApplication{store: store, leaseTTL: time.Minute, bindingTTL: time.Hour}
	collision, err := app.Handshake(context.Background(), caller, "browser-session-1", input)
	require.NoError(t, err)
	require.Equal(t, BrowserBindingCollision, collision.State)
	require.Len(t, store.copyCreates, 1)
	require.Empty(t, store.creates)

	store.copyCollision = false
	ambiguousCopy, err := app.Handshake(context.Background(), caller, "browser-session-1", input)
	require.NoError(t, err)
	require.Equal(t, BrowserBindingAmbiguous, ambiguousCopy.State)

	halfPair, err := app.Handshake(context.Background(), caller, "browser-session-1", BrowserBindingHandshakeInput{
		DocumentNonce:      "document-nonce-half-pair",
		CopiedTabBindingID: uuid.NewString(),
	})
	require.NoError(t, err)
	require.Equal(t, BrowserBindingAmbiguous, halfPair.State)
	require.Len(t, store.creates, 1, "an invalid copied pair creates a new binding, never touches the original")
}

func TestBrowserBindingApplication_ResumePendingKeepsRawMaterialPrivateAndSuccessRotatesToken(t *testing.T) {
	store := &recordingBrowserTabBindingStore{resumeState: gormdb.BrowserTabBindingReloadPending}
	app := &BrowserBindingApplication{store: store, leaseTTL: time.Minute, bindingTTL: time.Hour}
	caller := auth.SessionForBrowserUser("operator", 41)
	resume := BrowserBindingResumeInput{
		TabBindingID:  uuid.NewString(),
		ResumeNonce:   "resume-nonce-1",
		ReloadToken:   "reload-token-1",
		DocumentNonce: "document-nonce-reload",
	}

	pending, err := app.Resume(context.Background(), caller, "browser-session-1", resume)
	require.NoError(t, err)
	require.Equal(t, BrowserBindingReloadPending, pending.State)
	require.Empty(t, pending.DocumentProof)
	require.Empty(t, pending.ResumeNonce)
	require.Empty(t, pending.ReloadToken)
	require.Len(t, store.resumes, 1)

	store.resumeState = gormdb.BrowserTabBindingResumed
	resumed, err := app.Resume(context.Background(), caller, "browser-session-1", resume)
	require.NoError(t, err)
	require.Equal(t, BrowserBindingReady, resumed.State)
	require.NotEmpty(t, resumed.DocumentProof)
	require.Equal(t, resume.ResumeNonce, resumed.ResumeNonce, "the session-storage nonce identifies the binding; the one-time cookie rotates")
	require.NotEmpty(t, resumed.ReloadToken)
	require.NotEqual(t, resume.ReloadToken, resumed.ReloadToken)
	require.Len(t, store.resumes, 2)
	persisted := store.resumes[1]
	require.Equal(t, browserBindingTestDigest(resumed.DocumentProof), persisted.NewDocumentProofDigest)
	require.Equal(t, browserBindingTestDigest(resumed.ReloadToken), persisted.NewReloadTokenDigest)
}

func TestBrowserBindingApplication_DeniesNonBrowserSessionBeforeAnyTransition(t *testing.T) {
	store := &recordingBrowserTabBindingStore{}
	app := &BrowserBindingApplication{store: store, leaseTTL: time.Minute, bindingTTL: time.Hour}

	for name, caller := range map[string]auth.Identity{
		"master":        auth.Admin(),
		"auth disabled": auth.AuthDisabled(),
		"keycard":       auth.Client("read-write", uuid.NewString()),
		"hmac session":  auth.Session("operator"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := app.Handshake(context.Background(), caller, "browser-session-1", BrowserBindingHandshakeInput{DocumentNonce: "document-nonce"})
			require.ErrorIs(t, err, ErrBrowserBindingDenied)
		})
	}
	require.Empty(t, store.creates)
	require.Empty(t, store.copyCreates)
	require.Empty(t, store.resumes)
}

func browserBindingTestDigest(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}
