package projectidentity

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestReadExistingProjectChoicesLargeUnresolvedEnvelope(t *testing.T) {
	t.Setenv("ENGRAM_TLS_CA", "")
	item := `{"project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}`
	fragment := `{"reference":"urn:fixture:` + strings.Repeat("x", 320) + `","kind":"memory"},`
	large := `{"items":[` + item + `],"unresolved":[` + strings.Repeat(fragment, 6200) + `{"reference":"urn:fixture:last"}]}`
	if len(large) <= 1024*1024 {
		t.Fatal("large catalog fixture must exceed former cap")
	}
	var tooMany strings.Builder
	tooMany.WriteString(`{"items":[`)
	for i := range 4097 {
		if i != 0 {
			tooMany.WriteByte(',')
		}
		fmt.Fprintf(&tooMany, `{"project_id":"%08x-1111-4111-8111-111111111111","name":"Existing"}`, i+1)
	}
	tooMany.WriteString(`],"unresolved":[]}`)
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"large-current-catalog", http.StatusOK, large, true},
		{"large-unresolved-first", http.StatusOK, `{"unresolved":[` + strings.Repeat(fragment, 6200) + `{"reference":"urn:fixture:last"}],"items":[` + item + `]}`, true},
		{"too-many-items", http.StatusOK, tooMany.String(), false},
		{"oversized", http.StatusOK, `{"items":[` + item + `],"unresolved":["` + strings.Repeat("x", 17*1024*1024) + `"]}`, false},
		{"malformed-trailing", http.StatusOK, large + ` {}`, false},
		{"duplicate-project", http.StatusOK, `{"items":[` + item + `,` + item + `],"unresolved":[]}`, false},
		{"duplicate-items-key", http.StatusOK, `{"items":[` + item + `],"items":[` + item + `]}`, false},
		{"duplicate-item-field", http.StatusOK, `{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"One","name":"Two"}]}`, false},
		{"duplicate-unresolved-field", http.StatusOK, `{"items":[` + item + `],"unresolved":[{"reference":"one","reference":"two"}]}`, false},
		{"deep-unresolved", http.StatusOK, `{"items":[` + item + `],"unresolved":` + strings.Repeat("[", 70) + `0` + strings.Repeat("]", 70) + `}`, false},
		{"malformed-unresolved", http.StatusOK, `{"items":[` + item + `],"unresolved":[{"reference":}]}`, false},
		{"denied", http.StatusForbidden, large, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-keycard" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			choices, err := ReadExistingProjectChoices(t.Context(), server.URL, "synthetic-keycard")
			if tc.want {
				if err != nil || len(choices) != 1 || choices[0].Name != "Existing" {
					t.Fatalf("large authorized catalog should retain current item: count=%d err=%v", len(choices), err)
				}
			} else if err == nil || len(choices) != 0 {
				t.Fatalf("invalid catalog accepted: count=%d err=%v", len(choices), err)
			}
		})
	}
}

func TestAutomaticHistoricalRecoveryTransport(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"id":"abcdef","name":"Existing"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, initialErr := ResolveLogicalProject(t.Context(), root, nil)
	if ProjectErrorCode(initialErr) != "PROJECT_MIGRATION_REQUIRED" {
		t.Fatalf("legacy baseline: %v", initialErr)
	}
	t.Setenv("ENGRAM_TLS_CA", "")
	var sinkCalls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sinkCalls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer sink.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL, http.StatusFound) }))
	defer redirect.Close()
	if _, err := ResolveAutomaticHistoricalProject(t.Context(), root, redirect.URL, "fixture-keycard", initialErr); ProjectErrorCode(err) != "PROJECT_RESOLUTION_UNAVAILABLE" || sinkCalls.Load() != 0 {
		t.Fatalf("redirect was followed or accepted: %v", err)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects/resolve" || r.URL.Query().Get("legacy_id") != "abcdef" || r.Header.Get("Authorization") != "Bearer fixture-keycard" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"status":"resolved","project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}`))
	}))
	defer plain.Close()
	configuredURL := strings.Replace(plain.URL, "127.0.0.1", "localhost.", 1)
	plainProject, err := ResolveAutomaticHistoricalProject(t.Context(), root, configuredURL, "fixture-keycard", initialErr)
	if err != nil || plainProject.ProjectID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("configured HTTP hostname did not resolve mapping: %v", err)
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-keycard" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"status":"resolved","project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}`))
	}))
	defer tlsServer.Close()
	if _, err := ResolveAutomaticHistoricalProject(t.Context(), root, tlsServer.URL, "fixture-keycard", initialErr); ProjectErrorCode(err) != "PROJECT_RESOLUTION_UNAVAILABLE" {
		t.Fatalf("untrusted TLS accepted: %v", err)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_TLS_CA", ca)
	project, err := ResolveAutomaticHistoricalProject(t.Context(), root, tlsServer.URL, "fixture-keycard", initialErr)
	if err != nil || project.ProjectID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("configured CA did not authorize mapping: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"id":"abcdef","name":"Changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAutomaticHistoricalProject(t.Context(), root, tlsServer.URL, "fixture-keycard", initialErr); ProjectErrorCode(err) != "PROJECT_SELECTION_STALE" {
		t.Fatalf("changed initial marker accepted: %v", err)
	}
}
