package projectidentity

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestAutomaticOriginResolution(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name, want string
		posts      int32
	}{
		{"unbound", "", 1},
		{"lost-bootstrap-response", "", 1},
		{"unavailable", "PROJECT_RESOLUTION_UNAVAILABLE", 0},
		{"denied", "PROJECT_ACCESS_DENIED", 0},
		{"conflict", "PROJECT_IDENTITY_CONFLICT", 0},
		{"malformed", "PROJECT_IDENTITY_INVALID", 0},
		{"duplicate", "PROJECT_IDENTITY_INVALID", 0},
		{"stale-origin", "PROJECT_SELECTION_STALE", 0},
		{"stale-marker", "PROJECT_SELECTION_STALE", 0},
		{"v2-unbound", "PROJECT_CONTEXT_REQUIRED", 0},
		{"incomplete-active", "PROJECT_CONTEXT_REQUIRED", 0},
		{"not-found", "PROJECT_RESOLUTION_UNAVAILABLE", 0},
		{"bootstrap-denied", "PROJECT_ACCESS_DENIED", 1},
		{"bootstrap-redirect", "PROJECT_RESOLUTION_UNAVAILABLE", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_TLS_CA", "")
			root := t.TempDir()
			gitLogical(t, root, "init", "--quiet", "--template=")
			gitLogical(t, root, "remote", "add", "origin", "https://example.test/Fixture/Repo.git")
			marker := filepath.Join(root, ".engram-project")
			original := []byte(`{"name":"Existing"}`)
			if err := os.WriteFile(marker, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.name == "v2-unbound" {
				if err := os.WriteFile(filepath.Join(root, ".engram-project-v2.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var gets, posts atomic.Int32
			var redirected atomic.Int32
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); w.WriteHeader(http.StatusOK) }))
			defer sink.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-keycard" {
					w.WriteHeader(403)
					return
				}
				if r.Method == http.MethodPost {
					posts.Add(1)
					if tc.name == "bootstrap-denied" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if tc.name == "bootstrap-redirect" {
						http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
						return
					}
					if tc.name == "lost-bootstrap-response" {
						h, ok := w.(http.Hijacker)
						if !ok {
							t.Error("no hijacker")
							return
						}
						c, _, err := h.Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = c.Close()
						return
					}
					fmt.Fprintf(w, `{"project_id":"%s","name":"Existing"}`, id)
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/api/projects/resolve" || r.URL.Query().Get("repository_locator") != "example.test/Fixture/Repo" {
					t.Error("wrong lookup")
					w.WriteHeader(400)
					return
				}
				n := gets.Add(1)
				switch tc.name {
				case "unavailable":
					w.WriteHeader(503)
					return
				case "denied":
					w.WriteHeader(403)
					return
				case "conflict":
					fmt.Fprint(w, `{"status":"conflict"}`)
					return
				case "malformed":
					fmt.Fprint(w, `{"status":"resolved"}`)
					return
				case "duplicate":
					fmt.Fprint(w, `{"status":"unbound","status":"unbound"}`)
					return
				case "not-found":
					w.WriteHeader(http.StatusNotFound)
					return
				case "incomplete-active":
					fmt.Fprintf(w, `{"status":"resolved","binding":{"id":"%s","project_key":null,"identity_status":null,"match":"manual_alias"}}`, id)
					return
				case "stale-marker":
					if err := os.WriteFile(marker, []byte(`{"name":"Changed"}`), 0o600); err != nil {
						t.Error(err)
					}
				case "stale-origin":
					gitLogical(t, root, "remote", "set-url", "origin", "https://example.test/Other/Repo.git")
				}
				if ((tc.name == "unbound" || tc.name == "lost-bootstrap-response" || tc.name == "v2-unbound" || tc.name == "bootstrap-denied") && n == 1) || tc.name == "bootstrap-redirect" {
					fmt.Fprint(w, `{"status":"unbound"}`)
					return
				}
				fmt.Fprintf(w, `{"status":"resolved","binding":{"id":"%s","project_key":"%s","identity_status":"active","match":"manual_alias"}}`, id, id)
			}))
			defer server.Close()
			project, err := ResolveLogicalProjectForBackend(t.Context(), root, server.URL, "fixture-keycard")
			if tc.want == "" {
				if err != nil || project.ProjectID != id {
					t.Fatalf("recovery=%+v err=%v", project, err)
				}
				if _, err = ResolveLogicalProjectForBackend(t.Context(), root, server.URL, "fixture-keycard"); err != nil {
					t.Fatalf("reuse: %v", err)
				}
			} else if ProjectErrorCode(err) != tc.want {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
			if posts.Load() != tc.posts {
				t.Fatalf("bootstrap posts=%d want=%d", posts.Load(), tc.posts)
			}
			if redirected.Load() != 0 {
				t.Fatal("bootstrap followed redirect")
			}
			if tc.name != "stale-marker" {
				current, err := os.ReadFile(marker)
				if err != nil || string(current) != string(original) {
					t.Fatal("marker changed")
				}
			}
			if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
				t.Fatal("cache changed")
			}
		})
	}
}
