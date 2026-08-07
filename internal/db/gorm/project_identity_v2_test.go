package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gormio "gorm.io/gorm"
)

type invalidIdentityVector struct {
	Name            string `json:"name"`
	InvalidTarget   string `json:"invalid_target"`
	Selector        string `json:"selector"`
	DisplayName     string `json:"display_name"`
	LegacyProjectID string `json:"legacy_project_id"`
	GitRemote       string `json:"git_remote"`
	RelativePath    string `json:"relative_path"`
	NonGitAnchor    string `json:"non_git_anchor"`
	AnchorShared    *bool  `json:"anchor_shared"`
}

func loadInvalidIdentityVectors(t *testing.T) []invalidIdentityVector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".agent", "specs", "security-project-identity", "evidence", "project-identity-v2-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Invalid []invalidIdentityVector `json:"invalid_vectors"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus.Invalid
}

func gitIdentityV2(legacy, remote string) *ProjectIdentityV2 {
	return &ProjectIdentityV2{
		Version:         ProjectIdentityVersionV2,
		LegacyProjectID: legacy,
		DisplayName:     "identity-v2-test",
		GitRemote:       remote,
		RelativePath:    "packages/core/",
	}
}

func TestProjectIdentityError_TypedNilFailsClosed(t *testing.T) {
	var typedNil *ProjectIdentityError
	var err error = typedNil
	if got := ProjectIdentityPublicMessage(err); got != "project identity registration is unavailable" {
		t.Fatalf("public message=%q", got)
	}
	wrapped := projectIdentityWriteError(err)
	var identityErr *ProjectIdentityError
	if !errors.As(wrapped, &identityErr) || identityErr == nil || identityErr.Code != ProjectIdentityUnavailable {
		t.Fatalf("wrapped error=%T %v", wrapped, wrapped)
	}
}

func TestRegisterAndResolve_RejectsInvalidBeforeDatabaseAccess(t *testing.T) {
	shared := false
	_, err := RegisterAndResolve(context.Background(), nil, "selector", &ProjectIdentityV2{
		Version:         ProjectIdentityVersionV2,
		LegacyProjectID: "selector",
		NonGitAnchor:    "weak",
		AnchorShared:    &shared,
	})
	var identityErr *ProjectIdentityError
	if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityInvalid {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestRegisterAndResolve_RejectsRawVsNormalizedSelectorsAndMetadata(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		identity *ProjectIdentityV2
	}{
		{name: "selector whitespace", selector: " selector ", identity: nil},
		{name: "selector control", selector: "selector\tother", identity: nil},
		{name: "legacy selector whitespace", selector: "selector", identity: gitIdentityV2(" legacy ", "https://example.invalid/acme/mono.git")},
		{name: "relative path control", selector: "selector", identity: &ProjectIdentityV2{Version: 2, GitRemote: "https://example.invalid/acme/mono.git", RelativePath: "packages\t/core/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RegisterAndResolve(context.Background(), nil, tt.selector, tt.identity)
			var identityErr *ProjectIdentityError
			if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityInvalid {
				t.Fatalf("error=%T %v, want PROJECT_IDENTITY_INVALID before DB access", err, err)
			}
		})
	}
	for _, vector := range loadInvalidIdentityVectors(t) {
		vector := vector
		t.Run("shared-vector/"+vector.Name, func(t *testing.T) {
			_, err := RegisterAndResolve(context.Background(), nil, vector.Selector, &ProjectIdentityV2{
				Version:         ProjectIdentityVersionV2,
				LegacyProjectID: vector.LegacyProjectID,
				DisplayName:     vector.DisplayName,
				GitRemote:       vector.GitRemote,
				RelativePath:    vector.RelativePath,
				NonGitAnchor:    vector.NonGitAnchor,
				AnchorShared:    vector.AnchorShared,
			})
			var identityErr *ProjectIdentityError
			if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityInvalid {
				t.Fatalf("error=%T %v, want PROJECT_IDENTITY_INVALID before DB access", err, err)
			}
		})
	}
}

func TestRegisterAndResolve_StrictOuterSelectorRejectsBeforeDatabaseAccess(t *testing.T) {
	tests := []struct {
		name     string
		selector string
	}{
		{name: "empty", selector: ""},
		{name: "internal whitespace", selector: "a b"},
		{name: "traversal", selector: "../x"},
		{name: "illegal punctuation", selector: "repo?segment"},
		{name: "control", selector: "repo\u0001segment"},
		{name: "edge whitespace", selector: " repo"},
		{name: "over 256 bytes", selector: strings.Repeat("a", 257)},
		{name: "reserved git binding", selector: "p2g_00112233445566778899aabbccddeeff"},
		{name: "reserved non-git binding", selector: "p2n_00112233445566778899aabbccddeeff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RegisterAndResolve(context.Background(), nil, tt.selector, nil)
			var identityErr *ProjectIdentityError
			if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityInvalid || identityErr.UpgradeAction != UpgradeActionRegenerateProjectIdentityV2 {
				t.Fatalf("error=%T %v, want PROJECT_IDENTITY_INVALID before DB access", err, err)
			}
		})
	}
}

func TestAttachLegacyAlias_RejectsReservedBindingNamespaceBeforeDatabaseAccess(t *testing.T) {
	for _, alias := range []string{
		"p2g_00112233445566778899aabbccddeeff",
		"p2n_00112233445566778899aabbccddeeff",
	} {
		_, err := RegisterAndResolve(context.Background(), nil, alias, nil)
		var identityErr *ProjectIdentityError
		if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityInvalid {
			t.Fatalf("outer selector %q error=%T %v", alias, err, err)
		}
		err = AttachLegacyAlias(context.Background(), nil, "canonical", alias)
		if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityInvalid {
			t.Fatalf("legacy alias %q error=%T %v", alias, err, err)
		}
	}
}

func TestRegisterAndResolve_StrictOuterSelectorPreservesCompatibility(t *testing.T) {
	selectors := []string{
		"repo:segment",
		`repo\segment`,
		"repo/segment",
		"repo.segment",
		"repo-segment",
		"repo_segment",
		"p2g_workspace",
		"p2n_workspace",
	}
	for _, selector := range selectors {
		t.Run(selector, func(t *testing.T) {
			_, err := RegisterAndResolve(context.Background(), nil, selector, nil)
			var identityErr *ProjectIdentityError
			if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityUnavailable {
				t.Fatalf("error=%T %v, want selector accepted through nil-DB seam", err, err)
			}
		})
	}

	_, err := RegisterAndResolve(context.Background(), nil, "repo:segment", gitIdentityV2("legacy alias", "https://example.invalid/acme/mono.git"))
	var identityErr *ProjectIdentityError
	if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityUnavailable {
		t.Fatalf("legacy alias with internal whitespace was globally tightened: %T %v", err, err)
	}
}

func TestRegisterAndResolve_ExistingLegacyCanonicalAndContradiction(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	selector := "prc-v2-existing-selector"
	canonical := "prc-v2-existing-canonical"
	contradictory := gitIdentityV2(selector, "https://example.invalid/acme/other.git")
	contradictoryBinding := projectIdentityBindingKey(selector, *contradictory)
	defer func() {
		db.Exec(`DELETE FROM projects WHERE id = ? OR id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, canonical, contradictoryBinding, selector)
		db.Exec(`DELETE FROM projects WHERE id LIKE 'p2_%' AND COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, selector)
	}()
	db.Exec(`DELETE FROM projects WHERE id = ? OR id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, canonical, contradictoryBinding, selector)
	if err := UpsertProject(ctx, db, canonical, selector, "", "", "existing"); err != nil {
		t.Fatalf("seed existing canonical: %v", err)
	}

	first, err := RegisterAndResolve(ctx, db, selector, gitIdentityV2(selector, "https://example.invalid/acme/mono.git"))
	if err != nil {
		t.Fatalf("register first identity: %v", err)
	}
	if first.CanonicalProjectID != canonical {
		t.Fatalf("canonical=%q, want existing %q", first.CanonicalProjectID, canonical)
	}

	adoptedAliases := readProjectAliases(t, db, canonical)

	conflict, err := RegisterAndResolve(ctx, db, selector, contradictory)
	if err != nil {
		t.Fatalf("register contradictory identity: %v", err)
	}
	if conflict.CanonicalProjectID != contradictoryBinding {
		t.Fatalf("canonical=%q, want fresh binding %q", conflict.CanonicalProjectID, contradictoryBinding)
	}
	if got := readProjectAliases(t, db, canonical); !slices.Equal(got, adoptedAliases) {
		t.Fatalf("adopted legacy row mutated: got %#v want %#v", got, adoptedAliases)
	}
	if got := readProjectAliases(t, db, contradictoryBinding); len(got) != 0 {
		t.Fatalf("fresh binding stole an owned alias: %#v", got)
	}

	stable, err := RegisterAndResolve(ctx, db, selector, contradictory)
	if err != nil {
		t.Fatalf("second contradictory registration: %v", err)
	}
	if stable.CanonicalProjectID != conflict.CanonicalProjectID {
		t.Fatalf("second registration diverged: %q != %q", stable.CanonicalProjectID, conflict.CanonicalProjectID)
	}

	// The selector still belongs to the adopted legacy row, so an identity-less
	// client keeps resolving there instead of finding two owners.
	legacyOnly, err := RegisterAndResolve(ctx, db, selector, nil)
	if err != nil {
		t.Fatalf("legacy-only resolution: %v", err)
	}
	if legacyOnly.CanonicalProjectID != canonical {
		t.Fatalf("legacy-only canonical=%q, want %q", legacyOnly.CanonicalProjectID, canonical)
	}
}

// A full git identity defines the tenant, so legacy rows that merely shadow the
// selector and the legacy id cannot deny service: the registration mints its own
// binding row and leaves both legacy rows exactly as they were.
func TestRegisterAndResolve_ConflictingSelectorAndLegacyAliasMintsBindingWithoutMutation(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	prefix := "prc-v2-alias-conflict-"
	selector := prefix + "selector"
	legacyID := prefix + "legacy"
	selectorCanonical := prefix + "selector-canonical"
	legacyCanonical := prefix + "legacy-canonical"
	identity := gitIdentityV2(legacyID, "https://example.invalid/acme/conflict.git")
	bindingKey := projectIdentityBindingKey(selector, *identity)
	defer db.Exec(`DELETE FROM projects WHERE id = ? OR id LIKE ? OR EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, bindingKey, prefix+"%", prefix+"%")
	db.Exec(`DELETE FROM projects WHERE id = ? OR id LIKE ? OR EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, bindingKey, prefix+"%", prefix+"%")
	if err := UpsertProject(ctx, db, selectorCanonical, selector, "", "", "selector owner"); err != nil {
		t.Fatalf("seed selector owner: %v", err)
	}
	if err := UpsertProject(ctx, db, legacyCanonical, legacyID, "", "", "legacy owner"); err != nil {
		t.Fatalf("seed legacy owner: %v", err)
	}

	minted, err := RegisterAndResolve(ctx, db, selector, identity)
	if err != nil {
		t.Fatalf("register against conflicting legacy rows: %v", err)
	}
	if minted.CanonicalProjectID != bindingKey {
		t.Fatalf("canonical=%q, want fresh binding %q", minted.CanonicalProjectID, bindingKey)
	}

	for canonical, wantAlias := range map[string]string{
		selectorCanonical: selector,
		legacyCanonical:   legacyID,
	} {
		var persisted Project
		if err := db.Where("id = ?", canonical).First(&persisted).Error; err != nil {
			t.Fatalf("read %s: %v", canonical, err)
		}
		if len(persisted.LegacyIDs) != 1 || persisted.LegacyIDs[0] != wantAlias {
			t.Fatalf("%s aliases mutated: %#v", canonical, persisted.LegacyIDs)
		}
	}
	var binding Project
	if err := db.Where("id = ?", bindingKey).First(&binding).Error; err != nil {
		t.Fatalf("read minted binding: %v", err)
	}
	if slices.Contains(binding.LegacyIDs, selector) || slices.Contains(binding.LegacyIDs, legacyID) {
		t.Fatalf("minted binding stole an owned alias: %#v", binding.LegacyIDs)
	}

	stable, err := RegisterAndResolve(ctx, db, selector, identity)
	if err != nil {
		t.Fatalf("second registration: %v", err)
	}
	if stable.CanonicalProjectID != minted.CanonicalProjectID {
		t.Fatalf("second registration diverged: %q != %q", stable.CanonicalProjectID, minted.CanonicalProjectID)
	}
}

// An early identity match resolves on the git identity the client presented and
// never steals an alias another live row owns — both when the binding key is
// already stored and when only the git identity matches.
func TestRegisterAndResolve_EarlyIdentityMatchesNeverStealForeignAliases(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	readAliases := func(id string) []string {
		t.Helper()
		var project Project
		if err := db.Where("id = ?", id).First(&project).Error; err != nil {
			t.Fatalf("read project %s: %v", id, err)
		}
		return append([]string(nil), project.LegacyIDs...)
	}
	countBindings := func(remote string) int64 {
		t.Helper()
		var count int64
		if err := db.Model(&Project{}).
			Where("removed_at IS NULL AND id LIKE 'p2g_%' AND git_remote = ?", remote).
			Count(&count).Error; err != nil {
			t.Fatalf("count binding rows for %s: %v", remote, err)
		}
		return count
	}

	t.Run("existing binding", func(t *testing.T) {
		prefix := fmt.Sprintf("prc-v2-bound-conflict-%d-", time.Now().UnixNano())
		defer db.Exec(`DELETE FROM projects WHERE id LIKE ? OR EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, prefix+"%", prefix+"%")
		remote := "https://example.invalid/acme/" + prefix + "repo.git"
		ownerSelector := prefix + "owner-selector"
		ownerLegacy := prefix + "owner-legacy"
		ownerIdentity := gitIdentityV2(ownerLegacy, remote)
		owner, err := RegisterAndResolve(ctx, db, ownerSelector, ownerIdentity)
		if err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}
		foreignSelector := prefix + "foreign-selector"
		foreignCanonical := prefix + "foreign-canonical"
		if err := UpsertProject(ctx, db, foreignCanonical, foreignSelector, "", "", "foreign"); err != nil {
			t.Fatalf("seed foreign selector owner: %v", err)
		}

		ownerAliases := readAliases(owner.CanonicalProjectID)
		foreignAliases := readAliases(foreignCanonical)
		bindingRows := countBindings(remote)
		resolved, err := RegisterAndResolve(ctx, db, foreignSelector, gitIdentityV2(ownerLegacy, remote))
		if err != nil {
			t.Fatalf("bound identity with a shadowed selector: %v", err)
		}
		if resolved.CanonicalProjectID != owner.CanonicalProjectID {
			t.Fatalf("canonical=%q, want bound owner %q", resolved.CanonicalProjectID, owner.CanonicalProjectID)
		}
		if got := readAliases(owner.CanonicalProjectID); !slices.Equal(got, ownerAliases) {
			t.Fatalf("bound owner aliases mutated: got %#v want %#v", got, ownerAliases)
		}
		if got := readAliases(foreignCanonical); !slices.Equal(got, foreignAliases) {
			t.Fatalf("foreign owner aliases mutated: got %#v want %#v", got, foreignAliases)
		}
		if got := countBindings(remote); got != bindingRows {
			t.Fatalf("binding rows changed: got %d want %d", got, bindingRows)
		}
	})

	t.Run("exact git identity", func(t *testing.T) {
		prefix := fmt.Sprintf("prc-v2-exact-conflict-%d-", time.Now().UnixNano())
		defer db.Exec(`DELETE FROM projects WHERE id LIKE ? OR EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, prefix+"%", prefix+"%")
		remote := "https://example.invalid/acme/" + prefix + "repo.git"
		exactCanonical := prefix + "exact-canonical"
		exactLegacy := prefix + "exact-legacy"
		if err := UpsertProject(ctx, db, exactCanonical, exactLegacy, remote, "packages/core/", "exact"); err != nil {
			t.Fatalf("seed exact git owner: %v", err)
		}
		foreignSelector := prefix + "foreign-selector"
		foreignCanonical := prefix + "foreign-canonical"
		if err := UpsertProject(ctx, db, foreignCanonical, foreignSelector, "", "", "foreign"); err != nil {
			t.Fatalf("seed foreign selector owner: %v", err)
		}

		exactAliases := readAliases(exactCanonical)
		foreignAliases := readAliases(foreignCanonical)
		bindingRows := countBindings(remote)
		identity := gitIdentityV2(exactLegacy, remote)
		bindingKey := projectIdentityBindingKey(foreignSelector, *identity)
		resolved, err := RegisterAndResolve(ctx, db, foreignSelector, identity)
		if err != nil {
			t.Fatalf("exact git identity with a shadowed selector: %v", err)
		}
		if resolved.CanonicalProjectID != exactCanonical {
			t.Fatalf("canonical=%q, want exact git owner %q", resolved.CanonicalProjectID, exactCanonical)
		}
		// The binding key is the only alias the resolution may add: the shadowed
		// selector stays with the row that owns it.
		if got := readAliases(exactCanonical); !slices.Equal(got, append(append([]string(nil), exactAliases...), bindingKey)) {
			t.Fatalf("exact owner aliases mutated: got %#v want %#v plus %q", got, exactAliases, bindingKey)
		}
		if got := readAliases(foreignCanonical); !slices.Equal(got, foreignAliases) {
			t.Fatalf("foreign owner aliases mutated: got %#v want %#v", got, foreignAliases)
		}
		if got := countBindings(remote); got != bindingRows {
			t.Fatalf("binding rows changed: got %d want %d", got, bindingRows)
		}
	})
}

func TestRegisterAndResolve_ConcurrentCallsConvergeAndSharingIsExplicit(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	prefix := "prc-v2-concurrent-"
	defer func() {
		db.Exec(`DELETE FROM projects WHERE id LIKE ? OR EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, prefix+"%", prefix+"%")
		db.Exec(`DELETE FROM projects WHERE id LIKE 'p2_%' AND EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, prefix+"%")
	}()
	db.Exec(`DELETE FROM projects WHERE EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, prefix+"%")

	selector := prefix + "same"
	identity := gitIdentityV2(selector, "https://example.invalid/concurrent.git")
	const callers = 16
	results := make([]ProjectIdentityResolution, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = RegisterAndResolve(ctx, db, selector, identity)
		}(i)
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i].CanonicalProjectID != results[0].CanonicalProjectID {
			t.Fatalf("caller %d diverged: %q != %q", i, results[i].CanonicalProjectID, results[0].CanonicalProjectID)
		}
	}

	anchor := "00112233445566778899aabbccddeeff"
	unshared := false
	shared := true
	makeNonGit := func(selector string, sharing *bool) *ProjectIdentityV2 {
		return &ProjectIdentityV2{Version: 2, LegacyProjectID: selector, DisplayName: selector, NonGitAnchor: anchor, AnchorShared: sharing}
	}
	a, err := RegisterAndResolve(ctx, db, prefix+"unshared-a", makeNonGit(prefix+"unshared-a", &unshared))
	if err != nil {
		t.Fatal(err)
	}
	b, err := RegisterAndResolve(ctx, db, prefix+"unshared-b", makeNonGit(prefix+"unshared-b", &unshared))
	if err != nil {
		t.Fatal(err)
	}
	if a.CanonicalProjectID == b.CanonicalProjectID {
		t.Fatal("copied unshared anchors converged")
	}
	crossClientLegacyID := prefix + "same-local-workspace"
	crossClientIdentity := &ProjectIdentityV2{
		Version:         2,
		LegacyProjectID: crossClientLegacyID,
		DisplayName:     "same-local-workspace",
		NonGitAnchor:    anchor,
		AnchorShared:    &unshared,
	}
	e, err := RegisterAndResolve(ctx, db, prefix+"client-a-selector", crossClientIdentity)
	if err != nil {
		t.Fatal(err)
	}
	f, err := RegisterAndResolve(ctx, db, prefix+"client-b-selector", crossClientIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if e.CanonicalProjectID != f.CanonicalProjectID {
		t.Fatalf("same unshared workspace diverged across client selectors: %q != %q", e.CanonicalProjectID, f.CanonicalProjectID)
	}
	c, err := RegisterAndResolve(ctx, db, prefix+"shared-a", makeNonGit(prefix+"shared-a", &shared))
	if err != nil {
		t.Fatal(err)
	}
	d, err := RegisterAndResolve(ctx, db, prefix+"shared-b", makeNonGit(prefix+"shared-b", &shared))
	if err != nil {
		t.Fatal(err)
	}
	if c.CanonicalProjectID != d.CanonicalProjectID {
		t.Fatalf("explicit shared anchors did not converge: %q != %q", c.CanonicalProjectID, d.CanonicalProjectID)
	}
	var persisted Project
	if err := db.Where("id = ?", c.CanonicalProjectID).First(&persisted).Error; err != nil {
		t.Fatalf("load shared canonical: %v", err)
	}
	if persisted.GitRemote.Valid || persisted.RelativePath.Valid {
		t.Fatalf("non-git binding polluted git metadata: %#v", persisted)
	}
	for _, alias := range persisted.LegacyIDs {
		if alias == anchor {
			t.Fatal("raw non-git anchor was persisted")
		}
	}

	// Restart/rollback compatibility: a legacy-only request sees the ordinary
	// projects row after the v2 registration; no v2-only table is required.
	restarted, err := RegisterAndResolve(ctx, db.Session(&gormio.Session{NewDB: true}), selector, nil)
	if err != nil {
		t.Fatalf("legacy-only resolution after logical restart: %v", err)
	}
	if restarted.CanonicalProjectID != results[0].CanonicalProjectID {
		t.Fatalf("restart canonical=%q, want %q", restarted.CanonicalProjectID, results[0].CanonicalProjectID)
	}
}

func TestRegisterAndResolve_ConcurrentDifferentBindingsDoNotClaimSameLegacyRow(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	prefix := "prc-v2-legacy-race-"
	legacyID := prefix + "canonical"
	aliases := []string{prefix + "alias-a", prefix + "alias-b"}
	defer db.Exec(`DELETE FROM projects WHERE id = ? OR id LIKE 'p2_%' AND EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, legacyID, prefix+"%")
	db.Exec(`DELETE FROM projects WHERE id = ? OR EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, legacyID, prefix+"%")
	seed := Project{ID: legacyID, LegacyIDs: aliases}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}

	functionName := fmt.Sprintf("engram_test_identity_delay_%d", time.Now().UnixNano())
	triggerName := functionName + "_trigger"
	functionSQL := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.id = '%s' THEN PERFORM pg_sleep(0.5); END IF;
  RETURN NEW;
END $$`, functionName, legacyID)
	if err := db.Exec(functionSQL).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
	if err := db.Exec(fmt.Sprintf("CREATE TRIGGER %s BEFORE UPDATE OF legacy_ids ON projects FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName)).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON projects", triggerName))

	shared := true
	identities := []*ProjectIdentityV2{
		{Version: 2, LegacyProjectID: aliases[0], DisplayName: "binding-a", NonGitAnchor: "11111111111111111111111111111111", AnchorShared: &shared},
		{Version: 2, LegacyProjectID: aliases[1], DisplayName: "binding-b", NonGitAnchor: "22222222222222222222222222222222", AnchorShared: &shared},
	}
	results := make([]ProjectIdentityResolution, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range identities {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = RegisterAndResolve(ctx, db, aliases[i], identities[i])
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if results[0].CanonicalProjectID == results[1].CanonicalProjectID {
		t.Fatalf("different bindings claimed the same legacy row: %#v", results)
	}
	legacyClaims := 0
	for _, result := range results {
		if result.CanonicalProjectID == legacyID {
			legacyClaims++
		}
	}
	if legacyClaims != 1 {
		t.Fatalf("legacy row claims=%d, want exactly one: %#v", legacyClaims, results)
	}
}

func TestRegisterAndResolve_FailsClosedOnSoftDeletedBindingCollision(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	selector := "prc-v2-removed-binding"
	identity := gitIdentityV2(selector, "https://example.invalid/removed-binding.git")
	bindingKey := projectIdentityBindingKey(selector, *identity)
	now := time.Now().UTC()
	db.Exec(`DELETE FROM projects WHERE id = ?`, bindingKey)
	defer db.Exec(`DELETE FROM projects WHERE id = ?`, bindingKey)
	if err := db.Create(&Project{ID: bindingKey, RemovedAt: &now}).Error; err != nil {
		t.Fatalf("seed removed binding: %v", err)
	}

	_, err := RegisterAndResolve(context.Background(), db, selector, identity)
	var identityErr *ProjectIdentityError
	if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityUnavailable {
		t.Fatalf("error=%T %v, want fail-closed unavailable", err, err)
	}
	var persisted Project
	if err := db.Where("id = ?", bindingKey).First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RemovedAt == nil {
		t.Fatal("registration silently revived a soft-deleted binding")
	}
	if len(persisted.LegacyIDs) != 0 {
		t.Fatalf("registration mutated removed binding aliases: %#v", persisted.LegacyIDs)
	}
}

func TestRegisterAndResolve_LegacyOnlySoftDeletedCanonicalFailsWithoutMutation(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	selector := "prc-v2-r2-removed-legacy"
	now := time.Now().UTC()
	db.Unscoped().Exec(`DELETE FROM projects WHERE id = ?`, selector)
	defer db.Unscoped().Exec(`DELETE FROM projects WHERE id = ?`, selector)
	seed := Project{ID: selector, LegacyIDs: []string{"preserve-existing-alias"}, RemovedAt: &now}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}

	_, err := RegisterAndResolve(context.Background(), db, selector, nil)
	var identityErr *ProjectIdentityError
	if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityUnavailable || identityErr.UpgradeAction != UpgradeActionRetryProjectRegistration {
		t.Fatalf("error=%T %v, want stable PROJECT_IDENTITY_UNAVAILABLE", err, err)
	}

	var rows []Project
	if err := db.Unscoped().Where("id = ?", selector).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RemovedAt == nil {
		t.Fatalf("soft-deleted canonical changed: %#v", rows)
	}
	if len(rows[0].LegacyIDs) != 1 || rows[0].LegacyIDs[0] != "preserve-existing-alias" {
		t.Fatalf("aliases mutated: %#v", rows[0].LegacyIDs)
	}
}

// The live nvmd-devops constellation: the repo moved orgs, so two rows carry
// different git_remotes and both still list the same legacy path-hash alias. The
// client presents the current remote, matching exactly one row, and the shared
// legacy alias owned by the other row must not deny service.
func TestProjectIdentity_MovedRepoSharedLegacyAliasResolves(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	prefix := fmt.Sprintf("prc-v2-moved-repo-%d-", time.Now().UnixNano())
	currentRow := prefix + "4a8aca29"
	staleRow := prefix + "01be8f28"
	sharedLegacy := prefix + "nvmd-devops_9aa4cc"
	staleOnlyAlias := prefix + "nvmd-devops_01be8f28"
	currentRemote := "https://github.invalid/nv-md/" + prefix + "nvmd-devops.git"
	staleRemote := "https://github.invalid/thebtf/" + prefix + "nvmd-devops.git"
	identity := &ProjectIdentityV2{
		Version:         ProjectIdentityVersionV2,
		LegacyProjectID: sharedLegacy,
		DisplayName:     "nvmd-devops",
		GitRemote:       currentRemote,
	}
	bindingKey := projectIdentityBindingKey(currentRow, *identity)
	defer db.Exec(`DELETE FROM projects WHERE id = ? OR id LIKE ?`, bindingKey, prefix+"%")
	if err := UpsertProject(ctx, db, currentRow, sharedLegacy, currentRemote, "", "current"); err != nil {
		t.Fatalf("seed current row: %v", err)
	}
	if err := UpsertProject(ctx, db, staleRow, sharedLegacy, staleRemote, "", "stale"); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}
	if err := UpsertProject(ctx, db, staleRow, staleOnlyAlias, staleRemote, "", "stale"); err != nil {
		t.Fatalf("seed stale row alias: %v", err)
	}
	staleAliases := readProjectAliases(t, db, staleRow)
	if !slices.Contains(staleAliases, sharedLegacy) {
		t.Fatalf("fixture does not reproduce the shared legacy alias: %#v", staleAliases)
	}

	first, err := RegisterAndResolve(ctx, db, currentRow, identity)
	if err != nil {
		t.Fatalf("register moved-repo identity: %v", err)
	}
	if first.CanonicalProjectID != currentRow {
		t.Fatalf("canonical=%q, want the row storing the current remote %q", first.CanonicalProjectID, currentRow)
	}
	if got := readProjectAliases(t, db, staleRow); !slices.Equal(got, staleAliases) {
		t.Fatalf("stale row mutated: got %#v want %#v", got, staleAliases)
	}
	if got := readProjectAliases(t, db, currentRow); !slices.Equal(got, []string{sharedLegacy, bindingKey}) {
		t.Fatalf("current row aliases=%#v, want the seed alias plus %q", got, bindingKey)
	}

	second, err := RegisterAndResolve(ctx, db, currentRow, identity)
	if err != nil {
		t.Fatalf("second registration: %v", err)
	}
	if second.CanonicalProjectID != first.CanonicalProjectID {
		t.Fatalf("second registration diverged: %q != %q", second.CanonicalProjectID, first.CanonicalProjectID)
	}
	if got := readProjectAliases(t, db, staleRow); !slices.Equal(got, staleAliases) {
		t.Fatalf("stale row mutated on re-registration: got %#v want %#v", got, staleAliases)
	}
}

func seedProjectMemories(t *testing.T, db *gormio.DB, project string, count int) {
	t.Helper()
	for range count {
		if err := db.Exec(`INSERT INTO memories (project, content) VALUES (?, ?)`, project, "identity-v2 collapse fixture").Error; err != nil {
			t.Fatalf("seed memory for %s: %v", project, err)
		}
	}
}

func readProjectAliases(t *testing.T, db *gormio.DB, id string) []string {
	t.Helper()
	var project Project
	if err := db.Where("id = ?", id).First(&project).Error; err != nil {
		t.Fatalf("read project %s: %v", id, err)
	}
	return append([]string(nil), project.LegacyIDs...)
}

// Legacy dirName and path-hash registrations leave several rows carrying one
// git_remote + relative_path. The v2 contract says that is one tenant, so the
// registration collapses onto the row holding the most live memories instead of
// failing PROJECT_IDENTITY_AMBIGUOUS, which left the MCP surface dead.
//
// The fixture is a repo-root identity on purpose: idx_projects_remote_path is
// UNIQUE over (git_remote, relative_path), and only a NULL relative_path — what
// a repo-root project stores — lets duplicate git rows exist at all.
func TestProjectIdentity_DuplicateGitRowsCollapseToDensestCanonical(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	prefix := fmt.Sprintf("prc-v2-dup-git-%d-", time.Now().UnixNano())
	remote := "https://example.invalid/acme/" + prefix + "repo.git"
	// "a-" sorts before "b-", so a densest pick proves count beats id ordering.
	sparse := prefix + "a-dirname"
	dense := prefix + "b-path-hash"
	selector := prefix + "selector"
	legacyID := prefix + "legacy"
	identity := &ProjectIdentityV2{
		Version:         ProjectIdentityVersionV2,
		LegacyProjectID: legacyID,
		DisplayName:     "identity-v2-test",
		GitRemote:       remote,
	}
	bindingKey := projectIdentityBindingKey(selector, *identity)
	defer func() {
		db.Exec(`DELETE FROM memories WHERE project LIKE ?`, prefix+"%")
		db.Exec(`DELETE FROM projects WHERE id = ? OR id LIKE ?`, bindingKey, prefix+"%")
	}()
	for _, id := range []string{sparse, dense} {
		if err := UpsertProject(ctx, db, id, "", remote, "", id); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seedProjectMemories(t, db, sparse, 1)
	seedProjectMemories(t, db, dense, 3)

	first, err := RegisterAndResolve(ctx, db, selector, identity)
	if err != nil {
		t.Fatalf("register duplicated git identity: %v", err)
	}
	if first.CanonicalProjectID != dense {
		t.Fatalf("canonical=%q, want densest row %q", first.CanonicalProjectID, dense)
	}
	if aliases := readProjectAliases(t, db, sparse); len(aliases) != 0 {
		t.Fatalf("shadowed row mutated: %#v", aliases)
	}
	// The binding key lands on the chosen row, so the next call short-circuits
	// through the bound branch instead of re-running the collapse.
	if aliases := readProjectAliases(t, db, dense); !slices.Contains(aliases, bindingKey) {
		t.Fatalf("binding key missing from canonical: %#v", aliases)
	}

	second, err := RegisterAndResolve(ctx, db, selector, identity)
	if err != nil {
		t.Fatalf("second registration: %v", err)
	}
	if second.CanonicalProjectID != first.CanonicalProjectID {
		t.Fatalf("second registration diverged: %q != %q", second.CanonicalProjectID, first.CanonicalProjectID)
	}
}

// The collapse must not steal an alias another live row owns, and skipping one
// must not abort the remaining appends.
func TestProjectIdentity_LenientAliasAppendSkipsOwnedAlias(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	prefix := fmt.Sprintf("prc-v2-lenient-%d-", time.Now().UnixNano())
	owner := prefix + "owner"
	target := prefix + "target"
	ownedAlias := prefix + "owned-alias"
	freshAlias := prefix + "fresh-alias"
	defer db.Exec(`DELETE FROM projects WHERE id LIKE ?`, prefix+"%")
	if err := UpsertProject(ctx, db, owner, ownedAlias, "", "", "owner"); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if err := UpsertProject(ctx, db, target, "", "", "", "target"); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	err := appendProjectAliases(ctx, db, target, ownedAlias)
	var identityErr *ProjectIdentityError
	if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityAmbiguous {
		t.Fatalf("strict append error=%T %v, want PROJECT_IDENTITY_AMBIGUOUS", err, err)
	}

	if err := appendProjectAliasesOwned(ctx, db, target, true, ownedAlias, freshAlias); err != nil {
		t.Fatalf("lenient append: %v", err)
	}
	aliases := readProjectAliases(t, db, target)
	if slices.Contains(aliases, ownedAlias) {
		t.Fatalf("lenient append stole an owned alias: %#v", aliases)
	}
	if !slices.Contains(aliases, freshAlias) {
		t.Fatalf("skipped alias aborted the remaining appends: %#v", aliases)
	}
	if got := readProjectAliases(t, db, owner); len(got) != 1 || got[0] != ownedAlias {
		t.Fatalf("owner aliases mutated: %#v", got)
	}
}

// Leniency is scoped to full git identities. Non-git anchors and binding-key
// contradictions stay fail-closed.
func TestProjectIdentity_NonGitAndContradictoryBindingsStillFailLoud(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	assertAmbiguous := func(t *testing.T, err error) {
		t.Helper()
		var identityErr *ProjectIdentityError
		if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityAmbiguous || identityErr.UpgradeAction != UpgradeActionSendProjectIdentityV2 {
			t.Fatalf("error=%T %v, want PROJECT_IDENTITY_AMBIGUOUS", err, err)
		}
	}

	t.Run("non-git anchor ambiguity", func(t *testing.T) {
		prefix := fmt.Sprintf("prc-v2-nongit-ambig-%d-", time.Now().UnixNano())
		selector := prefix + "selector"
		legacyID := prefix + "legacy"
		defer db.Exec(`DELETE FROM projects WHERE id LIKE ?`, prefix+"%")
		if err := UpsertProject(ctx, db, prefix+"selector-owner", selector, "", "", "selector owner"); err != nil {
			t.Fatalf("seed selector owner: %v", err)
		}
		if err := UpsertProject(ctx, db, prefix+"legacy-owner", legacyID, "", "", "legacy owner"); err != nil {
			t.Fatalf("seed legacy owner: %v", err)
		}
		shared := true
		_, err := RegisterAndResolve(ctx, db, selector, &ProjectIdentityV2{
			Version:         ProjectIdentityVersionV2,
			LegacyProjectID: legacyID,
			DisplayName:     "non-git",
			NonGitAnchor:    "aabbccddeeff00112233445566778899",
			AnchorShared:    &shared,
		})
		assertAmbiguous(t, err)
	})

	t.Run("legacy selector without identity", func(t *testing.T) {
		prefix := fmt.Sprintf("prc-v2-legacy-ambig-%d-", time.Now().UnixNano())
		selector := prefix + "selector"
		defer db.Exec(`DELETE FROM projects WHERE id LIKE ?`, prefix+"%")
		for _, id := range []string{prefix + "owner-a", prefix + "owner-b"} {
			if err := UpsertProject(ctx, db, id, selector, "", "", id); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}
		_, err := RegisterAndResolve(ctx, db, selector, nil)
		assertAmbiguous(t, err)
	})

	t.Run("binding key conflicts with stored git identity", func(t *testing.T) {
		prefix := fmt.Sprintf("prc-v2-binding-conflict-%d-", time.Now().UnixNano())
		selector := prefix + "selector"
		identity := gitIdentityV2(prefix+"legacy", "https://example.invalid/acme/"+prefix+"repo.git")
		bindingKey := projectIdentityBindingKey(selector, *identity)
		defer db.Exec(`DELETE FROM projects WHERE id = ? OR id LIKE ?`, bindingKey, prefix+"%")
		if err := UpsertProject(ctx, db, bindingKey, "", "https://example.invalid/acme/"+prefix+"other.git", "packages/core/", "stored"); err != nil {
			t.Fatalf("seed contradictory binding: %v", err)
		}
		_, err := RegisterAndResolve(ctx, db, selector, identity)
		assertAmbiguous(t, err)
	})
}
