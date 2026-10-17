package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/projectidentity"
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
		if vector.InvalidTarget == "selector" {
			continue
		}
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

func TestValidateLogicalProject_RejectsHistoricalSelectorVectors(t *testing.T) {
	for _, vector := range loadInvalidIdentityVectors(t) {
		if vector.InvalidTarget != "selector" {
			continue
		}
		t.Run(vector.Name, func(t *testing.T) {
			err := projectidentity.ValidateProject(projectidentity.Project{ProjectID: vector.Selector})
			var identityErr *projectidentity.LogicalError
			if !errors.As(err, &identityErr) || identityErr.Code != "PROJECT_IDENTITY_INVALID" {
				t.Fatalf("error=%T %v, want historical selector rejected as project UUID", err, err)
			}
		})
	}
}

func TestBootstrapLogicalProject_RejectsInvalidLocatorBeforeDatabaseAccess(t *testing.T) {
	for _, locator := range []string{
		" example.invalid/acme/repo",
		"example.invalid/acme\trepo",
		"example.invalid/acme/../repo",
		"fixture-user:fixture-credential@example.invalid/acme/repo",
		"fixture-user:fixture-credential@example.invalid/%zz",
	} {
		_, err := BootstrapLogicalProject(context.Background(), nil, projectidentity.BootstrapRequest{RepositoryLocator: locator})
		var identityErr *projectidentity.LogicalError
		if !errors.As(err, &identityErr) || identityErr.Code != "PROJECT_LOCATOR_INVALID" || strings.Contains(err.Error(), "fixture-credential") {
			t.Fatal("invalid bootstrap locator reached the database boundary or leaked")
		}
	}
}

func TestRegisterAndResolve_RejectsGitRemoteUserinfoBeforeDatabaseAccess(t *testing.T) {
	for _, rawRemote := range []string{
		"https://fixture-user:fixture-credential@example.invalid/acme/identity.git",
		"//fixture-user:fixture-credential@example.invalid/%zz",
	} {
		_, err := RegisterAndResolve(context.Background(), nil, "selector", gitIdentityV2("selector", rawRemote))
		var identityErr *ProjectIdentityError
		if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityInvalid || strings.Contains(err.Error(), "fixture-credential") {
			t.Fatal("userinfo remote reached the database boundary or leaked")
		}
	}
}

func TestValidateProjectIdentityV2_AcceptsScpAndLocalRemotesWithAt(t *testing.T) {
	for _, remote := range []string{"fixture-user@example.invalid:repo.git", "./fixture@directory:repo.git"} {
		if err := ValidateProjectIdentityV2(*gitIdentityV2("selector", remote)); err != nil {
			t.Fatal("credential-free Git remote was rejected")
		}
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

func TestUpsertProject_RejectsGitRemoteUserinfoBeforeWrite(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	for index, remote := range []string{
		"https://fixture-user:fixture-credential@example.invalid/acme/identity.git",
		"//fixture-user:fixture-credential@example.invalid/%zz",
	} {
		projectID := fmt.Sprintf("project-identity-userinfo-write-fence-%d", index)
		defer db.Exec(`DELETE FROM projects WHERE id = ?`, projectID)

		err := UpsertProject(context.Background(), db, projectID, "", remote, "", "identity")
		var identityErr *ProjectIdentityError
		if !errors.As(err, &identityErr) || identityErr.Code != ProjectIdentityInvalid || strings.Contains(err.Error(), "fixture-credential") {
			t.Fatal("userinfo remote reached the durable write boundary or leaked")
		}
		var count int64
		if err := db.Model(&Project{}).Where("id = ?", projectID).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("userinfo remote wrote a project row")
		}
	}
}

func TestAttachLegacyAlias_RejectsReservedBindingNamespaceBeforeDatabaseAccess(t *testing.T) {
	for _, alias := range []string{
		"p2g_00112233445566778899aabbccddeeff",
		"p2n_00112233445566778899aabbccddeeff",
	} {
		err := AttachLegacyAlias(context.Background(), nil, "canonical", alias)
		var identityErr *ProjectIdentityError
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

func TestRegisterAndResolve_PinnedUUIDNeedsNoRegistration(t *testing.T) {
	for _, project := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		resolved, err := RegisterAndResolve(context.Background(), nil, project, nil)
		if err != nil || resolved.CanonicalProjectID != project {
			t.Fatalf("pinned UUID=%q resolved=%q error=%v", project, resolved.CanonicalProjectID, err)
		}
	}
}
