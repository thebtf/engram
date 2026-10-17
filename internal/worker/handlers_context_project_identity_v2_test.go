package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestContextInject_PinnedUUIDDoesNotRegister(t *testing.T) {
	project := "11111111-1111-4111-8111-111111111111"
	for range 2 {
		payload, err := json.Marshal(map[string]any{"project": project, "identity_only": true})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		(&Service{}).handleContextInject(rec, httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var response struct {
			CanonicalProject string `json:"canonical_project"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.CanonicalProject != project {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	}
}

func TestContextInject_FailsClosedWithoutIdentityStore(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"project": "prc-http-no-store"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&Service{}).handleContextInject(rec, httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Error struct {
			Code          string `json:"code"`
			UpgradeAction string `json:"upgrade_action"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != gormdb.ProjectIdentityUnavailable || response.Error.UpgradeAction != gormdb.UpgradeActionRetryProjectRegistration {
		t.Fatalf("error response=%+v", response.Error)
	}
}

func TestProjectIdentityHTTPError_DoesNotExposeDatabaseDiagnostics(t *testing.T) {
	rec := httptest.NewRecorder()
	writeProjectIdentityHTTPError(rec, &gormdb.ProjectIdentityError{
		Code:          gormdb.ProjectIdentityUnavailable,
		UpgradeAction: gormdb.UpgradeActionRetryProjectRegistration,
		Err:           errors.New("postgres internal-token-do-not-leak relation projects"),
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("do-not-leak")) || bytes.Contains(rec.Body.Bytes(), []byte("relation projects")) {
		t.Fatalf("database diagnostics leaked: %s", rec.Body.String())
	}
}

func TestProjectIdentityHTTPError_TypedNilFailsClosed(t *testing.T) {
	var typedNil *gormdb.ProjectIdentityError
	var err error = typedNil
	rec := httptest.NewRecorder()
	writeProjectIdentityHTTPError(rec, err)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(gormdb.ProjectIdentityUnavailable)) {
		t.Fatalf("typed-nil error did not fail closed: %s", rec.Body.String())
	}
}

func TestProjectAdoptionHTTPError_PreservesTypedRefusal(t *testing.T) {
	rec := httptest.NewRecorder()
	writeProjectIdentityHTTPError(rec, &gormdb.ConsolidationError{Code: "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Error struct {
			Code          string `json:"code"`
			UpgradeAction string `json:"upgrade_action"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP" || response.Error.UpgradeAction != "send_canonical_project_id" {
		t.Fatalf("error response=%+v", response.Error)
	}
}

func TestContextInject_RejectsRawSelectorAndMetadataBeforeProjectMutation(t *testing.T) {
	db, cleanup := setupProjectTestDB(t)
	defer cleanup()
	cases := []struct {
		name string
		body map[string]any
		key  string
	}{
		{
			name: "outer selector with internal whitespace",
			key:  "prc-http-r2-invalid selector",
			body: map[string]any{"project": "prc-http-r2-invalid selector", "identity_only": true},
		},
		{
			name: "legacy alias with edge whitespace",
			key:  "prc-http-r2-invalid-legacy-alias",
			body: map[string]any{
				"project": "prc-http-r2-invalid-legacy-alias", "legacy_project": " legacy-alias ", "identity_only": true,
			},
		},
		{
			name: "display name with leading whitespace",
			key:  "prc-http-r2-invalid-display",
			body: map[string]any{
				"project": "prc-http-r2-invalid-display", "identity_only": true,
				"project_identity": map[string]any{
					"version": 2, "legacy_project_id": "prc-http-r2-invalid-display", "display_name": " display",
					"git_remote": "https://example.invalid/r2/display.git", "relative_path": "packages/core/",
				},
			},
		},
		{
			name: "relative path with empty segment",
			key:  "prc-http-r2-invalid-relative",
			body: map[string]any{
				"project": "prc-http-r2-invalid-relative", "identity_only": true,
				"project_identity": map[string]any{
					"version": 2, "legacy_project_id": "prc-http-r2-invalid-relative", "display_name": "relative",
					"git_remote": "https://example.invalid/r2/relative.git", "relative_path": "packages//core/",
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db.Unscoped().Exec(`DELETE FROM projects WHERE id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, tc.key, tc.key)
			defer db.Unscoped().Exec(`DELETE FROM projects WHERE id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, tc.key, tc.key)
			payload, _ := json.Marshal(tc.body)
			rec := httptest.NewRecorder()
			svc := &Service{store: &gormdb.Store{DB: db}}
			svc.handleContextInject(rec, httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var count int64
			if err := db.Unscoped().Model(&gormdb.Project{}).
				Where(`id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, tc.key, tc.key).
				Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("invalid request mutated %d project rows", count)
			}
		})
	}
}
