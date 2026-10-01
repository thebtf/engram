package gorm

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

func TestCanonicalUCILocalGitLocatorPreservesWorktreeBoundaries(t *testing.T) {
	canonical, valid := canonicalUCILocalGitLocator("file:///worktrees/./%61")
	require.True(t, valid)
	require.Equal(t, "file:///worktrees/a", canonical)
	other, valid := canonicalUCILocalGitLocator("file:///worktrees/b")
	require.True(t, valid)
	require.NotEqual(t, canonical, other)
	for _, locator := range []string{
		"file:///worktrees/link/../a",
		"file:///worktrees/link/%2e%2e/a",
		"file:////remote/share",
		"file:///worktrees/a%5Cb",
		"file://remote/worktrees/a",
	} {
		_, valid := canonicalUCILocalGitLocator(locator)
		require.False(t, valid, locator)
	}
}

func TestRegisterLocalGitLabelsDistinguishRealmsWorktreesAndDevices(t *testing.T) {
	for _, realm := range []string{uci.NoAuthCodeRealm, "client"} {
		t.Run(realm, func(t *testing.T) {
			db, store := openUCIContextMigrationStore(t)
			ctx := context.Background()
			principal := uci.NoAuthCodePrincipal
			var ownerID int64
			if realm == "client" {
				owner := &User{Email: "chooser-" + uuid.NewString() + "@example.test", PasswordHash: "fixture-hash", Role: DashboardRoleOperator, CreatedAt: time.Now().UTC()}
				require.NoError(t, db.Create(owner).Error)
				ownerID, principal = owner.ID, fmt.Sprintf("browser-user/%d", owner.ID)
			}
			input := RegisterLocalGitInput{AuthRealm: realm, Principal: principal, WorkstationID: "device-one", SourceLabel: "repository", Locator: "file:///private/one/repo"}
			first, err := store.RegisterLocalGit(ctx, input)
			require.NoError(t, err)
			input.Locator = "file:///private/two/repo"
			second, err := store.RegisterLocalGit(ctx, input)
			require.NoError(t, err)
			input.Locator = "file:///private/one/repo"
			input.WorkstationID = "device-two"
			third, err := store.RegisterLocalGit(ctx, input)
			require.NoError(t, err)
			labels := make(map[string]string)
			for _, registered := range []RegisteredLocalGit{first, second, third} {
				var label string
				require.NoError(t, db.Raw("SELECT COALESCE(display_name, '') FROM ci_checkouts WHERE checkout_id = ?", registered.CheckoutID).Scan(&label).Error)
				require.NotEmpty(t, label)
				labels[registered.CheckoutID] = label
				require.True(t, validBrowserCodeCheckoutDisplayLabel(label))
				require.NotContains(t, label, "/private/")
				require.NotContains(t, label, principal)
			}
			require.Contains(t, labels[first.CheckoutID], "one › repo · Device ")
			require.Contains(t, labels[second.CheckoutID], "two › repo · Device ")
			require.NotEqual(t, labels[first.CheckoutID], labels[second.CheckoutID])
			require.NotEqual(t, labels[first.CheckoutID], labels[third.CheckoutID])
			if realm == uci.NoAuthCodeRealm {
				entries, err := NewBrowserCodeContextStore(db).ListNoAuthCatalog(ctx)
				require.NoError(t, err)
				require.Len(t, entries, 3)
				for _, entry := range entries {
					require.Equal(t, labels[entry.CheckoutID], entry.CheckoutLabel)
				}
			} else {
				grants := NewBrowserReadGrantStore(db)
				choices, err := grants.ListOwnerChoices(ctx, ownerID, principal)
				require.NoError(t, err)
				require.Len(t, choices, 3)
				for _, choice := range choices {
					require.Equal(t, labels[choice.ChoiceRef], choice.WorkingCopyLabel)
				}
				_, err = grants.SetOwnerChoiceLabel(ctx, ownerID, principal, first.CheckoutID, "My studio worktree")
				require.NoError(t, err)
			}
			input.WorkstationID = "device-one"
			replayed, err := store.RegisterLocalGit(ctx, input)
			require.NoError(t, err)
			require.Equal(t, first, replayed)
			input.SourceID, input.SourceLabel = first.SourceID, ""
			replayedByID, err := store.RegisterLocalGit(ctx, input)
			require.NoError(t, err)
			require.Equal(t, first, replayedByID)
			var after string
			require.NoError(t, db.Raw("SELECT COALESCE(display_name, '') FROM ci_checkouts WHERE checkout_id = ?", first.CheckoutID).Scan(&after).Error)
			if realm == "client" {
				require.Equal(t, "My studio worktree", after)
			} else {
				require.Equal(t, labels[first.CheckoutID], after)
			}
		})
	}
}

func TestRegisterLocalGitLabelBoundsUnsafeCanonicalComponents(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	input := RegisterLocalGitInput{
		AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal,
		WorkstationID: "device-one", SourceLabel: "repository",
		Locator: (&url.URL{Scheme: "file", Path: "/private/" + strings.Repeat("漫", 100) + "/repo\n\u202esecret"}).String(),
	}
	registered, err := store.RegisterLocalGit(context.Background(), input)
	require.NoError(t, err)
	var label string
	require.NoError(t, db.Raw("SELECT COALESCE(display_name, '') FROM ci_checkouts WHERE checkout_id = ?", registered.CheckoutID).Scan(&label).Error)
	require.True(t, validBrowserCodeCheckoutDisplayLabel(label))
	require.LessOrEqual(t, len(label), 256)
	require.Contains(t, label, "repo--secret")
	require.NotContains(t, label, "\n")
	require.NotContains(t, label, "\u202e")
	require.NotContains(t, label, "/private/")
}

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

func TestRegisterLocalGitParserProfileAdmitsCrossHostSemanticArtifact(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	parser := true
	registered, err := store.RegisterLocalGit(ctx, RegisterLocalGitInput{
		AuthRealm: "client", Principal: "browser-user/41", WorkstationID: "keycard-41",
		SourceLabel: "cross-host-parser", Locator: "file:///worktrees/cross-host-parser", ParserBundle: &parser,
	})
	require.NoError(t, err)
	var stored UCIAnalysisProfile
	require.NoError(t, db.Where("profile_id = ?", registered.ProfileID).First(&stored).Error)
	require.Equal(t, string(uci.TreeSitterSemanticContractDigest()), stored.ParserBundleDigest)

	goProfile := uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"}
	profile, err := uci.GoIndexAdmissionArtifactProfile(goProfile)
	require.NoError(t, err)
	profile.ExtractionProfileDigest = uci.TreeSitterSemanticContractDigest()
	body := []byte("package crosshost\n\nfunc First() {}\n")
	artifact, err := uci.NewIndexAdmissionArtifactFromGo(registered.SourceID, profile, body, uci.ExtractGo(body, goProfile))
	require.NoError(t, err)
	frame := uci.IndexAdmissionFrame{
		Version:   uci.IndexAdmissionFrameVersion,
		Profile:   uci.IndexAdmissionProfile{ID: registered.ProfileID},
		Artifacts: []uci.IndexAdmissionArtifact{artifact},
		Memberships: []uci.IndexAdmissionMembership{{
			PathKey: "first.go", DisplayPath: "first.go", Mode: "100644",
			State: uci.IndexAdmissionMembershipPresent, ArtifactID: &artifact.ArtifactID,
		}},
	}
	projection := NewUCIProjectionStore(db)
	_, err = projection.AdmitIndexFrames(ctx, registered.SourceID, registered.ProfileID, []uci.IndexAdmissionFrame{frame})
	require.NoError(t, err)
	parserFrame := uciIndexAdmissionTypeScriptFixtureFrame(t, &uciPublicationFixture{
		source: &UCISource{SourceID: registered.SourceID}, profile: &stored,
	})
	parserFrame.Artifacts[0].Profile.GrammarDigest = uci.TreeSitterBundleDigest()
	badGrammarID, err := uci.DeriveIndexAdmissionArtifactID(registered.SourceID, parserFrame.Artifacts[0].ContentDigest, parserFrame.Artifacts[0].Profile)
	require.NoError(t, err)
	parserFrame.Artifacts[0].FactsDigest, err = uci.DigestIndexAdmissionArtifactFacts(parserFrame.Artifacts[0])
	require.NoError(t, err)
	parserFrame.Artifacts[0].ArtifactID = badGrammarID
	parserFrame.Memberships[0].ArtifactID = &badGrammarID
	_, err = projection.AdmitIndexFrames(ctx, registered.SourceID, registered.ProfileID, []uci.IndexAdmissionFrame{parserFrame})
	require.ErrorContains(t, err, "artifact grammar does not match authorized parser contract")
	validParserFrame := uciIndexAdmissionTypeScriptFixtureFrame(t, &uciPublicationFixture{
		source: &UCISource{SourceID: registered.SourceID}, profile: &stored,
	})
	_, err = projection.AdmitIndexFrames(ctx, registered.SourceID, registered.ProfileID, []uci.IndexAdmissionFrame{validParserFrame})
	require.NoError(t, err)
	wrongProfile := profile
	wrongProfile.ExtractionProfileDigest = uci.TreeSitterBundleDigest()
	wrongArtifact, err := uci.NewIndexAdmissionArtifactFromGo(registered.SourceID, wrongProfile, body, uci.ExtractGo(body, goProfile))
	require.NoError(t, err)
	wrong := frame
	wrong.Artifacts = []uci.IndexAdmissionArtifact{wrongArtifact}
	wrong.Memberships = []uci.IndexAdmissionMembership{{
		PathKey: "first.go", DisplayPath: "first.go", Mode: "100644",
		State: uci.IndexAdmissionMembershipPresent, ArtifactID: &wrongArtifact.ArtifactID,
	}}
	_, err = projection.AdmitIndexFrames(ctx, registered.SourceID, registered.ProfileID, []uci.IndexAdmissionFrame{wrong})
	require.ErrorContains(t, err, "artifact profile does not match authorized profile")
}

func TestRegisterLocalGitGoOnlyProfileStagesNativeArtifactAndView(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	registered, err := store.RegisterLocalGit(ctx, RegisterLocalGitInput{
		AuthRealm: "client", Principal: "browser-user/41", WorkstationID: "keycard-41",
		SourceLabel: "native-go", Locator: "file:///worktrees/native-go",
	})
	require.NoError(t, err)
	var stored UCIAnalysisProfile
	require.NoError(t, db.Where("profile_id = ?", registered.ProfileID).First(&stored).Error)
	goProfile := uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"}
	profile, err := uci.GoIndexAdmissionArtifactProfile(goProfile)
	require.NoError(t, err)
	require.Equal(t, string(profile.ExtractionProfileDigest), stored.ParserBundleDigest)
	body := []byte("package native\nfunc Native() {}\n")
	artifact, err := uci.NewIndexAdmissionArtifactFromGo(registered.SourceID, profile, body, uci.ExtractGo(body, goProfile))
	require.NoError(t, err)
	frame := uci.IndexAdmissionFrame{Version: uci.IndexAdmissionFrameVersion, Profile: uci.IndexAdmissionProfile{ID: registered.ProfileID}, Artifacts: []uci.IndexAdmissionArtifact{artifact}, Memberships: []uci.IndexAdmissionMembership{{PathKey: "native.go", DisplayPath: "native.go", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &artifact.ArtifactID}}}
	_, err = NewUCIProjectionStore(db).AdmitIndexFrames(ctx, registered.SourceID, registered.ProfileID, []uci.IndexAdmissionFrame{frame})
	require.NoError(t, err)
	checkout, err := store.GetCheckout(ctx, registered.CheckoutID)
	require.NoError(t, err)
	view, err := store.CreateView(ctx, newUCIContextMigrationViewInput(checkout, &stored, 1, uuid.NewString()))
	require.NoError(t, err)
	require.Equal(t, registered.ProfileID, view.ProfileID)
}

func TestRegisterLocalGitLegacyParserReplayPreservesPinnedViewAndStagesNewProfile(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	parser := true
	in := RegisterLocalGitInput{
		AuthRealm: "client", Principal: "browser-user/41", WorkstationID: "keycard-41",
		SourceLabel: "legacy-parser", Locator: "file:///worktrees/legacy-parser", ParserBundle: &parser,
	}
	original, err := store.RegisterLocalGit(ctx, in)
	require.NoError(t, err)
	legacyDigest := string(uci.TreeSitterBundleDigest())
	require.NotEqual(t, string(uci.TreeSitterSemanticContractDigest()), legacyDigest)
	require.NoError(t, db.Model(&UCIAnalysisProfile{}).Where("profile_id = ?", original.ProfileID).Update("parser_bundle_digest", legacyDigest).Error)
	checkout, err := store.GetCheckout(ctx, original.CheckoutID)
	require.NoError(t, err)
	var legacyProfile UCIAnalysisProfile
	require.NoError(t, db.Where("profile_id = ?", original.ProfileID).First(&legacyProfile).Error)
	oldView, err := store.CreateView(ctx, newUCIContextMigrationViewInput(checkout, &legacyProfile, 1, uuid.NewString()))
	require.NoError(t, err)

	for index, replay := range []RegisterLocalGitInput{func() RegisterLocalGitInput {
		omitted := in
		omitted.ParserBundle = nil
		omitted.DefaultParserBundle = true
		return omitted
	}(), in} {
		updated, replayErr := store.RegisterLocalGit(ctx, replay)
		require.NoError(t, replayErr)
		require.Equal(t, original.SourceID, updated.SourceID)
		require.Equal(t, original.CheckoutID, updated.CheckoutID)
		require.Equal(t, original.IncarnationID, updated.IncarnationID)
		require.NotEqual(t, original.ProfileID, updated.ProfileID)
		var next UCIAnalysisProfile
		require.NoError(t, db.Where("profile_id = ?", updated.ProfileID).First(&next).Error)
		require.Equal(t, string(uci.TreeSitterSemanticContractDigest()), next.ParserBundleDigest)
		var pinned UCIView
		require.NoError(t, db.Where("view_id = ?", oldView.ViewID).First(&pinned).Error)
		require.Equal(t, original.ProfileID, pinned.ProfileID)
		var old UCIAnalysisProfile
		require.NoError(t, db.Where("profile_id = ?", original.ProfileID).First(&old).Error)
		require.Equal(t, legacyDigest, old.ParserBundleDigest)
		if index != 0 {
			continue
		}
		body := []byte("package legacy\nfunc NewVersion() {}\n")
		goProfile := uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"}
		admission, profileErr := uci.GoIndexAdmissionArtifactProfile(goProfile)
		require.NoError(t, profileErr)
		admission.ExtractionProfileDigest = uci.TreeSitterSemanticContractDigest()
		artifact, artifactErr := uci.NewIndexAdmissionArtifactFromGo(updated.SourceID, admission, body, uci.ExtractGo(body, goProfile))
		require.NoError(t, artifactErr)
		frame := uci.IndexAdmissionFrame{Version: uci.IndexAdmissionFrameVersion, Profile: uci.IndexAdmissionProfile{ID: updated.ProfileID}, Artifacts: []uci.IndexAdmissionArtifact{artifact}, Memberships: []uci.IndexAdmissionMembership{{PathKey: "new.go", DisplayPath: "new.go", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &artifact.ArtifactID}}}
		_, stageErr := NewUCIProjectionStore(db).AdmitIndexFrames(ctx, updated.SourceID, updated.ProfileID, []uci.IndexAdmissionFrame{frame})
		require.NoError(t, stageErr)
		checkout, checkoutErr := store.GetCheckout(ctx, updated.CheckoutID)
		require.NoError(t, checkoutErr)
		_, viewErr := store.CreateView(ctx, newUCIContextMigrationViewInput(checkout, &next, 2, uuid.NewString()))
		require.NoError(t, viewErr)
	}
}

func TestRegisterLocalGitLegacyParserTransitionPublishesOverPinnedView(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()
	legacyDigest := string(uci.TreeSitterBundleDigest())
	require.NoError(t, fixture.db.Model(&UCIAnalysisProfile{}).Where("profile_id = ?", fixture.profile.ProfileID).Update("parser_bundle_digest", legacyDigest).Error)
	fixture.profile.ParserBundleDigest = legacyDigest
	require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Update("registration_profile_id", fixture.profile.ProfileID).Error)
	legacy := fixture.admitArtifact(t, fixture.source.SourceID, "legacy", "func Legacy() {}\n", UCIParseArtifactComplete)
	oldMembership := []uci.IndexMembership{uciPublicationPresentMembership("old.go", legacy)}
	oldEdges := []uci.IndexEdgeReplacement{{SourcePath: "old.go"}}
	oldDraft := newUCIPublicationDraft([]uci.IndexPart{uciPublicationPart([]uciPublicationArtifact{legacy}, oldMembership, nil, oldEdges)}, oldMembership, oldEdges)
	caller := fixture.caller("legacy-owner")
	_, pinned := fixture.publish(t, fixture.publisher, caller, fixture.publishInput("legacy-build", fixture.checkout, fixture.profile.ProfileID, nil, uci.IndexManifestFull, uci.IndexJobInitial, oldDraft))
	parser := true
	in := RegisterLocalGitInput{AuthRealm: fixture.realm, Principal: fixture.principal, WorkstationID: fixture.checkout.WorkstationID, SourceID: fixture.source.SourceID, Locator: fixture.checkout.LocatorRef, ParserBundle: &parser}
	updated, err := fixture.context.RegisterLocalGit(ctx, in)
	require.NoError(t, err)
	require.NotEqual(t, fixture.profile.ProfileID, updated.ProfileID)
	var semantic UCIAnalysisProfile
	require.NoError(t, fixture.db.Where("profile_id = ?", updated.ProfileID).First(&semantic).Error)
	require.Equal(t, string(uci.TreeSitterSemanticContractDigest()), semantic.ParserBundleDigest)
	oldView, err := fixture.context.GetCurrentView(ctx, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, pinned.Context.ViewID, oldView.ViewID)
	require.Equal(t, fixture.profile.ProfileID, oldView.ProfileID)
	var oldFact UCIParseArtifact
	require.NoError(t, fixture.db.Where("artifact_id = ?", legacy.Artifact.ArtifactID).First(&oldFact).Error)
	require.Equal(t, legacyDigest, oldFact.ExtractionProfileDigest)

	forged := fixture.beginInput("forged-parent", fixture.checkout, updated.ProfileID, uciPublicationParent(pinned), uci.IndexManifestFull, uci.IndexJobReconcile)
	forged.ExpectedParent.ViewID = uuid.NewString()
	_, err = fixture.publisher.Begin(ctx, caller, forged)
	require.Error(t, err)
	forged = fixture.beginInput("forged-profile", fixture.checkout, updated.ProfileID, uciPublicationParent(pinned), uci.IndexManifestFull, uci.IndexJobReconcile)
	forged.ExpectedParent.AnalysisProfileID = uuid.NewString()
	_, err = fixture.publisher.Begin(ctx, caller, forged)
	require.Error(t, err)

	fixture.profile = &semantic
	frame := uciIndexAdmissionFixtureFrame(t, fixture, "next.go", "package next\nfunc Next() {}\n")
	_, err = fixture.projection.AdmitIndexFrames(ctx, updated.SourceID, updated.ProfileID, []uci.IndexAdmissionFrame{frame})
	require.NoError(t, err)
	nextArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "successor", "func Successor() {}\n", UCIParseArtifactComplete)
	nextMembership := []uci.IndexMembership{uciPublicationPresentMembership("next.go", nextArtifact)}
	nextEdges := []uci.IndexEdgeReplacement{{SourcePath: "next.go"}}
	nextDraft := newUCIPublicationDraft([]uci.IndexPart{uciPublicationPart([]uciPublicationArtifact{nextArtifact}, nextMembership, nil, nextEdges)}, nextMembership, nextEdges)
	_, successor := fixture.publish(t, fixture.publisher, caller, fixture.publishInput("semantic-build", fixture.checkout, updated.ProfileID, uciPublicationParent(pinned), uci.IndexManifestFull, uci.IndexJobReconcile, nextDraft))
	require.Equal(t, pinned.Context.Generation+1, successor.Context.Generation)
	require.NotEqual(t, pinned.Context.ViewID, successor.Context.ViewID)
	oldView, err = fixture.context.GetView(ctx, pinned.Context.ViewID)
	require.NoError(t, err)
	require.Equal(t, legacyDigest, oldFact.ExtractionProfileDigest)
	var oldMembershipRow UCIMembership
	require.NoError(t, fixture.db.Where("checkout_id = ? AND path_key = ? AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)", fixture.checkout.CheckoutID, "old.go", pinned.Context.Generation, pinned.Context.Generation).First(&oldMembershipRow).Error)
	require.NotNil(t, oldMembershipRow.ArtifactID)
	require.Equal(t, legacy.Artifact.ArtifactID, *oldMembershipRow.ArtifactID)
	require.Equal(t, pinned.Context.AnalysisProfileID, oldView.ProfileID)
	current, err := fixture.context.GetCurrentView(ctx, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, successor.Context.ViewID, current.ViewID)
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
	alias := owner
	alias.Locator = strings.TrimSuffix(owner.Locator, "/a") + "/./%61"
	if alias.Locator == owner.Locator {
		t.Fatal("test locator must differ")
	}
	aliasReplay, err := store.RegisterLocalGit(ctx, alias)
	require.NoError(t, err)
	require.Equal(t, first, aliasReplay, "equivalent URI must not register another source")
	alias.SourceID, alias.SourceLabel = first.SourceID, ""
	aliasReplay, err = store.RegisterLocalGit(ctx, alias)
	require.NoError(t, err)
	require.Equal(t, first, aliasReplay, "equivalent URI under source ID must not register another checkout")
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
	require.Equal(t, string(uci.TreeSitterSemanticContractDigest()), parserProfile.ParserBundleDigest)
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
	require.Equal(t, string(uci.TreeSitterSemanticContractDigest()), automaticProfile.ParserBundleDigest)
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
