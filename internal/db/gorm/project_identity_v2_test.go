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

func TestRegisterAndResolve_StrictOuterSelectorPreservesCompatibility(t *testing.T) {
	selectors := []string{
		"repo:segment",
		`repo\segment`,
		"repo/segment",
		"repo.segment",
		"repo-segment",
		"repo_segment",
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
	defer func() {
		db.Exec(`DELETE FROM projects WHERE id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, canonical, selector)
		db.Exec(`DELETE FROM projects WHERE id LIKE 'p2_%' AND COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, selector)
	}()
	db.Exec(`DELETE FROM projects WHERE id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, canonical, selector)
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

	conflict, err := RegisterAndResolve(ctx, db, selector, gitIdentityV2(selector, "https://example.invalid/acme/other.git"))
	if err != nil {
		t.Fatalf("register contradictory identity: %v", err)
	}
	if conflict.CanonicalProjectID == canonical {
		t.Fatal("contradictory full identities merged")
	}
	if conflict.CanonicalProjectID == "" {
		t.Fatal("conflict canonical is empty")
	}

	_, err = RegisterAndResolve(ctx, db, selector, nil)
	var identityErr *ProjectIdentityError
	if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityAmbiguous || identityErr.UpgradeAction != UpgradeActionSendProjectIdentityV2 {
		t.Fatalf("legacy-only ambiguity error=%T %v", err, err)
	}
}

func TestRegisterAndResolve_ConflictingSelectorAndLegacyAliasFailWithoutMutation(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	prefix := "prc-v2-alias-conflict-"
	selector := prefix + "selector"
	legacyID := prefix + "legacy"
	selectorCanonical := prefix + "selector-canonical"
	legacyCanonical := prefix + "legacy-canonical"
	defer db.Exec(`DELETE FROM projects WHERE id LIKE ? OR EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, prefix+"%", prefix+"%")
	db.Exec(`DELETE FROM projects WHERE id LIKE ? OR EXISTS (SELECT 1 FROM unnest(COALESCE(legacy_ids, ARRAY[]::TEXT[])) alias WHERE alias LIKE ?)`, prefix+"%", prefix+"%")
	if err := UpsertProject(ctx, db, selectorCanonical, selector, "", "", "selector owner"); err != nil {
		t.Fatalf("seed selector owner: %v", err)
	}
	if err := UpsertProject(ctx, db, legacyCanonical, legacyID, "", "", "legacy owner"); err != nil {
		t.Fatalf("seed legacy owner: %v", err)
	}

	_, err := RegisterAndResolve(ctx, db, selector, gitIdentityV2(legacyID, "https://example.invalid/acme/conflict.git"))
	var identityErr *ProjectIdentityError
	if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityAmbiguous || identityErr.UpgradeAction != UpgradeActionSendProjectIdentityV2 {
		t.Fatalf("conflicting aliases error=%T %v", err, err)
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
	var created int64
	if err := db.Model(&Project{}).Where("id LIKE ?", "p2g_%").Where(`COALESCE(legacy_ids, ARRAY[]::TEXT[]) && ARRAY[?, ?]::TEXT[]`, selector, legacyID).Count(&created).Error; err != nil {
		t.Fatalf("count conflicting binding rows: %v", err)
	}
	if created != 0 {
		t.Fatalf("conflicting aliases created %d binding rows", created)
	}
}

func TestRegisterAndResolve_EarlyIdentityMatchesRejectForeignAliasesWithoutMutation(t *testing.T) {
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
	assertAmbiguous := func(err error) {
		t.Helper()
		var identityErr *ProjectIdentityError
		if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityAmbiguous || identityErr.UpgradeAction != UpgradeActionSendProjectIdentityV2 {
			t.Fatalf("conflicting alias error=%T %v", err, err)
		}
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
		_, err = RegisterAndResolve(ctx, db, foreignSelector, gitIdentityV2(ownerLegacy, remote))
		assertAmbiguous(err)
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
		_, err := RegisterAndResolve(ctx, db, foreignSelector, gitIdentityV2(exactLegacy, remote))
		assertAmbiguous(err)
		if got := readAliases(exactCanonical); !slices.Equal(got, exactAliases) {
			t.Fatalf("exact owner aliases mutated: got %#v want %#v", got, exactAliases)
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
