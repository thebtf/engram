package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
