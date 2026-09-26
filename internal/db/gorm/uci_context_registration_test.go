package gorm

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

func TestRegisterLocalGitUnboundLegacyCheckoutRefusesRecovery(t *testing.T) {
	for _, withWrongView := range []bool{false, true} {
		name := "zero_views"
		if withWrongView {
			name = "one_unrelated_profile_view"
		}
		t.Run(name, func(t *testing.T) {
			db, store := openUCIContextMigrationStore(t)
			ctx := context.Background()
			owner := RegisterLocalGitInput{AuthRealm: "client", Principal: "browser-user/41", WorkstationID: "keycard-41", SourceLabel: "legacy", Locator: "file:///legacy/worktree"}
			original, err := store.RegisterLocalGit(ctx, owner)
			require.NoError(t, err)
			if withWrongView {
				checkout, err := store.GetCheckout(ctx, original.CheckoutID)
				require.NoError(t, err)
				wrongProfile, err := store.CreateProfile(ctx, newUCIContextMigrationProfileInput(uuid.NewString()))
				require.NoError(t, err)
				require.NotEqual(t, original.ProfileID, wrongProfile.ProfileID)
				_, err = store.CreateView(ctx, newUCIContextMigrationViewInput(checkout, wrongProfile, 1, uuid.NewString()))
				require.NoError(t, err)
			}
			require.NoError(t, db.Model(&UCICheckout{}).Where("checkout_id = ?", original.CheckoutID).Update("registration_profile_id", nil).Error)
			byID := owner
			byID.SourceID, byID.SourceLabel = original.SourceID, ""
			for _, retry := range []RegisterLocalGitInput{owner, byID} {
				_, err := store.RegisterLocalGit(ctx, retry)
				var contextErr *uci.ContextError
				require.ErrorAs(t, err, &contextErr)
				require.Equal(t, uci.RegistrationProfileUnbound, contextErr.Code())
			}
			var sourceCount, checkoutCount, profileCount, viewCount int64
			require.NoError(t, db.Model(&UCISource{}).Count(&sourceCount).Error)
			require.NoError(t, db.Model(&UCICheckout{}).Count(&checkoutCount).Error)
			require.NoError(t, db.Model(&UCIAnalysisProfile{}).Count(&profileCount).Error)
			require.NoError(t, db.Model(&UCIView{}).Count(&viewCount).Error)
			require.EqualValues(t, 1, sourceCount)
			require.EqualValues(t, 1, checkoutCount)
			require.EqualValues(t, 1+viewCount, profileCount)
			if withWrongView {
				require.EqualValues(t, 1, viewCount)
			} else {
				require.Zero(t, viewCount)
			}
			var unchanged UCICheckout
			require.NoError(t, db.Where("checkout_id = ?", original.CheckoutID).First(&unchanged).Error)
			require.Nil(t, unchanged.RegistrationProfileID)
			require.Equal(t, original.IncarnationID, unchanged.IncarnationID)
			fresh := owner
			fresh.SourceLabel = "new-registration"
			newIdentity, err := store.RegisterLocalGit(ctx, fresh)
			require.NoError(t, err)
			require.NotEqual(t, original.SourceID, newIdentity.SourceID)
			require.NotEqual(t, original.CheckoutID, newIdentity.CheckoutID)
			require.NotEqual(t, original.ProfileID, newIdentity.ProfileID)
			replayed, err := store.RegisterLocalGit(ctx, fresh)
			require.NoError(t, err)
			require.Equal(t, newIdentity, replayed)
		})
	}
}

func TestRegisterLocalGitTwoDirtyWorktreesOwnerIsolation(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	git("init", "-q", a)
	require.NoError(t, os.WriteFile(filepath.Join(a, "shared.go"), []byte("package main\n"), 0o600))
	git("-C", a, "add", "shared.go")
	git("-C", a, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-qm", "initial")
	git("-C", a, "worktree", "add", "-qb", "other", b)
	require.NoError(t, os.WriteFile(filepath.Join(a, "dirty-a.go"), []byte("package a\nfunc FirstUse() {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(b, "dirty-b.go"), []byte("package b\n"), 0o600))

	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	user := &User{Email: "onboarding-" + uuid.NewString() + "@example.test", PasswordHash: "fixture-password-hash", Role: DashboardRoleOperator, CreatedAt: time.Now().UTC()}
	require.NoError(t, db.Create(user).Error)
	ownerPrincipal := fmt.Sprintf("browser-user/%d", user.ID)
	locator := func(path string) string {
		p := filepath.ToSlash(path)
		if filepath.VolumeName(path) != "" {
			p = "/" + p
		}
		return (&url.URL{Scheme: "file", Path: p}).String()
	}
	owner := RegisterLocalGitInput{AuthRealm: "client", Principal: ownerPrincipal, WorkstationID: "keycard-41", SourceLabel: "engram", Locator: locator(a)}
	first, err := store.RegisterLocalGit(ctx, owner)
	require.NoError(t, err)
	var goProfile UCIAnalysisProfile
	require.NoError(t, db.Where("profile_id = ?", first.ProfileID).First(&goProfile).Error)
	require.Equal(t, string(localGitGoProfileDigest()), goProfile.ParserBundleDigest)
	goSource, err := os.ReadFile(filepath.Join(a, "dirty-a.go"))
	require.NoError(t, err)
	nativeProfile := uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"}
	admissionProfile, err := uci.GoIndexAdmissionArtifactProfile(nativeProfile)
	require.NoError(t, err)
	require.Equal(t, goProfile.ParserBundleDigest, string(admissionProfile.ExtractionProfileDigest))
	artifact, err := uci.NewIndexAdmissionArtifactFromGo(first.SourceID, admissionProfile, goSource, uci.ExtractGo(goSource, nativeProfile))
	require.NoError(t, err)
	require.NotEmpty(t, artifact.Definitions)
	require.Equal(t, goProfile.ParserBundleDigest, string(artifact.Profile.ExtractionProfileDigest))
	parserRequest := owner
	parserBundle := true
	parserRequest.ParserBundle = &parserBundle
	_, err = store.RegisterLocalGit(ctx, parserRequest)
	var profileMismatch *uci.ContextError
	require.ErrorAs(t, err, &profileMismatch)
	require.Equal(t, uci.ContextMismatch, profileMismatch.Code())
	replayed, err := store.RegisterLocalGit(ctx, owner)
	require.NoError(t, err, "lost first response must recover the original registration")
	require.Equal(t, first, replayed)
	require.NoError(t, localGitRegistrationRetryMigration185().Rollback(db))
	require.NoError(t, localGitRegistrationRetryMigration185().Migrate(db))
	replayed, err = store.RegisterLocalGit(ctx, owner)
	require.NoError(t, err, "binary rollback and re-upgrade must retain the checkout's registration profile")
	require.Equal(t, first, replayed)
	byID := owner
	byID.SourceID, byID.SourceLabel = first.SourceID, ""
	replayed, err = store.RegisterLocalGit(ctx, byID)
	require.NoError(t, err)
	require.Equal(t, first, replayed)
	for _, invalid := range []RegisterLocalGitInput{
		{AuthRealm: owner.AuthRealm, Principal: owner.Principal, WorkstationID: owner.WorkstationID, SourceLabel: owner.SourceLabel, Locator: "https://private/checkout"},
		{AuthRealm: owner.AuthRealm, Principal: owner.Principal, WorkstationID: owner.WorkstationID, SourceLabel: " invalid ", Locator: owner.Locator},
		{AuthRealm: owner.AuthRealm, Principal: owner.Principal, WorkstationID: owner.WorkstationID, SourceID: "not-a-uuid", Locator: owner.Locator},
		{AuthRealm: owner.AuthRealm, Principal: owner.Principal, WorkstationID: owner.WorkstationID, SourceID: first.SourceID, SourceLabel: owner.SourceLabel, Locator: owner.Locator},
	} {
		_, err = store.RegisterLocalGit(ctx, invalid)
		var contextErr *uci.ContextError
		require.ErrorAs(t, err, &contextErr)
		require.Equal(t, uci.ContextMismatch, contextErr.Code())
	}
	require.NotEqual(t, first.SourceID, first.CheckoutID)
	owner.SourceID, owner.SourceLabel, owner.Locator = first.SourceID, "", locator(b)
	second, err := store.RegisterLocalGit(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, first.SourceID, second.SourceID)
	require.NotEqual(t, first.CheckoutID, second.CheckoutID)
	require.NotEqual(t, first.IncarnationID, second.IncarnationID)
	parserRequest.SourceID, parserRequest.SourceLabel, parserRequest.Locator = first.SourceID, "", locator(filepath.Join(root, "parser"))
	parserCheckout, err := store.RegisterLocalGit(ctx, parserRequest)
	require.NoError(t, err)
	var parserProfile UCIAnalysisProfile
	require.NoError(t, db.Where("profile_id = ?", parserCheckout.ProfileID).First(&parserProfile).Error)
	require.Equal(t, string(uci.TreeSitterBundleDigest()), parserProfile.ParserBundleDigest)
	legacyReplay := parserRequest
	legacyReplay.ParserBundle = nil
	replayedParser, err := store.RegisterLocalGit(ctx, legacyReplay)
	require.NoError(t, err)
	require.Equal(t, parserCheckout, replayedParser)
	defaultParser := parserRequest
	defaultParser.ParserBundle = nil
	defaultParser.DefaultParserBundle = true
	defaultParser.Locator = locator(filepath.Join(root, "automatic-parser"))
	automatic, err := store.RegisterLocalGit(ctx, defaultParser)
	require.NoError(t, err)
	var automaticProfile UCIAnalysisProfile
	require.NoError(t, db.Where("profile_id = ?", automatic.ProfileID).First(&automaticProfile).Error)
	require.Equal(t, string(uci.TreeSitterBundleDigest()), automaticProfile.ParserBundleDigest)
	defaultParser.DefaultParserBundle = false
	replayedAutomatic, err := store.RegisterLocalGit(ctx, defaultParser)
	require.NoError(t, err)
	require.Equal(t, automatic, replayedAutomatic)
	explicitGo := false
	defaultParser.ParserBundle = &explicitGo
	_, err = store.RegisterLocalGit(ctx, defaultParser)
	require.ErrorAs(t, err, &profileMismatch)
	require.Equal(t, uci.ContextMismatch, profileMismatch.Code())
	defaultParser.ParserBundle = nil
	defaultParser.Locator = locator(filepath.Join(root, "existing-go"))
	defaultParser.DefaultParserBundle = false
	previousGo, err := store.RegisterLocalGit(ctx, defaultParser)
	require.NoError(t, err)
	defaultParser.DefaultParserBundle = true
	replayedGo, err := store.RegisterLocalGit(ctx, defaultParser)
	require.NoError(t, err)
	require.Equal(t, previousGo, replayedGo)
	parserBundle = false
	_, err = store.RegisterLocalGit(ctx, parserRequest)
	require.ErrorAs(t, err, &profileMismatch)
	require.Equal(t, uci.ContextMismatch, profileMismatch.Code())

	for _, registered := range []RegisteredLocalGit{first, second} {
		selector, err := uci.CheckoutIndexBindingSelector(uci.RegisteredCheckoutSelector{
			Scope: uci.IndexScope{SourceID: registered.SourceID, CheckoutID: registered.CheckoutID, IncarnationID: registered.IncarnationID}, ProfileID: registered.ProfileID,
		})
		require.NoError(t, err)
		binding, err := store.LoadIndexBinding(ctx, selector)
		require.NoError(t, err)
		require.Nil(t, binding.Context)
		require.Equal(t, registered.CheckoutID, binding.Scope.CheckoutID)
		require.Equal(t, registered.ProfileID, binding.ProfileID)
	}
	foreign := owner
	foreign.Principal = "browser-user/99"
	foreign.Locator = locator(a)
	_, err = store.RegisterLocalGit(ctx, foreign)
	require.ErrorIs(t, err, errUCIContextAuthorizationDenied)
	wrongRealm := owner
	wrongRealm.AuthRealm = "session"
	_, err = store.RegisterLocalGit(ctx, wrongRealm)
	require.ErrorIs(t, err, errUCIContextAuthorizationDenied)
	require.NoError(t, db.Model(&UCISource{}).Where("source_id = ?", first.SourceID).Update("state", UCISourceOffline).Error)
	_, err = store.RegisterLocalGit(ctx, byID)
	require.ErrorIs(t, err, errUCIContextAuthorizationDenied)
	require.NoError(t, db.Model(&UCISource{}).Where("source_id = ?", first.SourceID).Update("state", UCISourceActive).Error)
	var count int64
	require.NoError(t, db.Model(&UCICheckout{}).Where("source_id = ?", first.SourceID).Count(&count).Error)
	require.EqualValues(t, 5, count)
	grants := NewBrowserReadGrantStore(db)
	grant, err := grants.Issue(ctx, BrowserReadGrantIssue{IssuerUserID: user.ID, IssuerPrincipal: ownerPrincipal, TargetUserID: user.ID, SourceID: first.SourceID, CheckoutID: first.CheckoutID})
	require.NoError(t, err)
	require.Equal(t, first.CheckoutID, grant.CheckoutID)
	readA, err := grants.CanRead(ctx, user.ID, first.SourceID, first.CheckoutID)
	require.NoError(t, err)
	require.True(t, readA)
	readB, err := grants.CanRead(ctx, user.ID, second.SourceID, second.CheckoutID)
	require.NoError(t, err)
	require.False(t, readB)
	catalog := NewBrowserCodeContextStore(db)
	materials := newBrowserTabBindingMaterials("onboarding")
	caller := BrowserTabBindingCaller{SubjectUserID: user.ID, SessionID: "onboarding-" + first.CheckoutID}
	tab, err := NewBrowserTabBindingStore(db).Create(ctx, browserTabBindingTestCreate(caller, materials))
	require.NoError(t, err)
	request := func(registered RegisteredLocalGit) BrowserCodeIndexIntentTarget {
		return BrowserCodeIndexIntentTarget{Caller: caller, TabBindingID: tab.TabBindingID, DocumentProofDigest: browserTabBindingTestDigest(materials.documentProof), SourceID: registered.SourceID, CheckoutID: registered.CheckoutID, ProfileID: registered.ProfileID}
	}
	firstIntent, err := catalog.AuthorizeInitialIndexIntent(ctx, request(first))
	require.NoError(t, err)
	require.Equal(t, first.CheckoutID, firstIntent.Scope.CheckoutID)
	_, err = catalog.AuthorizeInitialIndexIntent(ctx, request(second))
	require.ErrorIs(t, err, ErrBrowserCodeContextDenied)
	entries, err := catalog.ListCatalog(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, first.CheckoutID, entries[0].CheckoutID)
	require.Nil(t, entries[0].Context)
	require.True(t, entries[0].IndexIntentAvailable)
	_, err = grants.Issue(ctx, BrowserReadGrantIssue{IssuerUserID: user.ID, IssuerPrincipal: ownerPrincipal, TargetUserID: user.ID, SourceID: second.SourceID, CheckoutID: second.CheckoutID})
	require.NoError(t, err)
	readB, err = grants.CanRead(ctx, user.ID, second.SourceID, second.CheckoutID)
	require.NoError(t, err)
	require.True(t, readB)
	entries, err = catalog.ListCatalog(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.NotEqual(t, entries[0].CheckoutID, entries[1].CheckoutID)
	require.Nil(t, entries[1].Context)
	secondIntent, err := catalog.AuthorizeInitialIndexIntent(ctx, request(second))
	require.NoError(t, err)
	require.Equal(t, second.CheckoutID, secondIntent.Scope.CheckoutID)
	require.NotEqual(t, firstIntent.Scope, secondIntent.Scope)
	require.True(t, entries[1].IndexIntentAvailable)
}
