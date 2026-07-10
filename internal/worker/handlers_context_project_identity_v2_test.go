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

func TestContextInject_IdentityOnlyRegistersSynchronouslyAndIdempotently(t *testing.T) {
	db, cleanup := setupProjectTestDB(t)
	defer cleanup()
	selector := "prc-http-identity-v2"
	db.Exec(`DELETE FROM projects WHERE id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, selector, selector)
	defer func() {
		db.Exec(`DELETE FROM projects WHERE id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, selector, selector)
	}()

	svc := &Service{store: &gormdb.Store{DB: db}}
	body := map[string]any{
		"project":       selector,
		"identity_only": true,
		"project_identity": map[string]any{
			"version":           2,
			"legacy_project_id": selector,
			"display_name":      "http-v2",
			"git_remote":        "https://example.invalid/acme/http-v2.git",
			"relative_path":     "packages/core/",
		},
	}

	var canonical string
	for i := 0; i < 2; i++ {
		payload, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload))
		rec := httptest.NewRecorder()
		svc.handleContextInject(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		var response struct {
			CanonicalProject string `json:"canonical_project"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.CanonicalProject == "" {
			t.Fatal("empty canonical project")
		}
		if i == 0 {
			canonical = response.CanonicalProject
		}
		if response.CanonicalProject != canonical {
			t.Fatalf("attempt %d diverged: %q != %q", i, response.CanonicalProject, canonical)
		}
	}
}

func TestContextInject_LegacyMetadataPreservesOuterCanonical(t *testing.T) {
	db, cleanup := setupProjectTestDB(t)
	defer cleanup()
	canonical := "prc-http-legacy-outer"
	legacy := "prc-http-legacy-path"
	db.Exec(`DELETE FROM projects WHERE id IN (?, ?) OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, canonical, legacy, legacy)
	defer db.Exec(`DELETE FROM projects WHERE id IN (?, ?) OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, canonical, legacy, legacy)

	svc := &Service{store: &gormdb.Store{DB: db}}
	payload, _ := json.Marshal(map[string]any{
		"project":        canonical,
		"legacy_project": legacy,
		"identity_only":  true,
	})
	rec := httptest.NewRecorder()
	svc.handleContextInject(rec, httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		CanonicalProject string `json:"canonical_project"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.CanonicalProject != canonical {
		t.Fatalf("canonical=%q, want outer selector %q", response.CanonicalProject, canonical)
	}
	if resolved := gormdb.ResolveProjectID(t.Context(), db, legacy); resolved != canonical {
		t.Fatalf("legacy alias resolves to %q, want %q", resolved, canonical)
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

func TestContextInject_AmbiguousLegacyFailsWithUpgradeActionBeforeAccess(t *testing.T) {
	db, cleanup := setupProjectTestDB(t)
	defer cleanup()
	selector := "prc-http-ambiguous-v2"
	db.Exec(`DELETE FROM projects WHERE id IN (?, ?)`, selector+"-a", selector+"-b")
	defer func() { db.Exec(`DELETE FROM projects WHERE id IN (?, ?)`, selector+"-a", selector+"-b") }()
	if err := gormdb.UpsertProject(t.Context(), db, selector+"-a", selector, "", "", "a"); err != nil {
		t.Fatal(err)
	}
	if err := gormdb.UpsertProject(t.Context(), db, selector+"-b", selector, "", "", "b"); err != nil {
		t.Fatal(err)
	}

	svc := &Service{store: &gormdb.Store{DB: db}}
	payload, _ := json.Marshal(map[string]any{"project": selector, "identity_only": true})
	req := httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	svc.handleContextInject(rec, req)
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
	if response.Error.Code != gormdb.ProjectIdentityAmbiguous || response.Error.UpgradeAction != gormdb.UpgradeActionSendProjectIdentityV2 {
		t.Fatalf("error response=%+v", response.Error)
	}
}
