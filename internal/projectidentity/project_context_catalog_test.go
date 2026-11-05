package projectidentity

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadExistingProjectChoices(t *testing.T) {
	const token = "synthetic-catalog-keycard"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("catalog request lost its authenticated read-only scope")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"One"},{"project_id":"22222222-2222-4222-8222-222222222222","name":"Two"}],"unresolved":[{"reference":"urn:engram:migration:unresolved:synthetic"}]}`))
	}))
	defer server.Close()
	t.Setenv("ENGRAM_TLS_CA", "")
	choices, err := ReadExistingProjectChoices(t.Context(), server.URL, token)
	if err != nil || len(choices) != 2 || choices[0].Name != "One" || choices[1].Name != "Two" {
		t.Fatalf("authorized catalog: choices=%+v err=%v", choices, err)
	}
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"must not escape"}]}`))
		}))
		for _, supplied := range []string{"", token} {
			if choices, err := ReadExistingProjectChoices(t.Context(), denied.URL, supplied); err == nil || len(choices) != 0 {
				t.Fatal("denied catalog must not expose choices")
			}
			if _, _, err := ResolveHistoricalProject(t.Context(), denied.URL, supplied, "abcdef"); err == nil {
				t.Fatal("denied historical lookup must not expose a mapping")
			}
		}
		denied.Close()
	}
}

func TestProjectContextAuthDisabledBackend(t *testing.T) {
	t.Setenv("ENGRAM_TLS_CA", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("auth-disabled fixture received unexpected credential or method")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/projects":
			_, _ = w.Write([]byte(`{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}],"unresolved":[]}`))
		case "/api/projects/resolve":
			if r.URL.Query().Get("legacy_id") != "abcdef" {
				t.Error("legacy selector was not preserved")
			}
			_, _ = w.Write([]byte(`{"status":"resolved","project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	choices, err := ReadExistingProjectChoices(t.Context(), server.URL, "")
	if err != nil || len(choices) != 1 || choices[0].Name != "Existing" {
		t.Fatalf("auth-disabled catalog: %+v %v", choices, err)
	}
	project, resolved, err := ResolveHistoricalProject(t.Context(), server.URL, "", "abcdef")
	if err != nil || !resolved || project.ProjectID != choices[0].ProjectID {
		t.Fatalf("auth-disabled historical resolution: %+v %v %v", project, resolved, err)
	}
}
