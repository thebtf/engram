package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionMemoryProjectRecoveryDispatch(t *testing.T) {
	scratch := clientIdentityTempDir(t)
	root := memoryTestRepository(t, scratch, "recovery", memoryTestProjectA)
	marker := filepath.Join(root, ".engram-project")
	legacy := []byte(`{"id":"abcdef","name":"synthetic legacy"}`)
	if err := os.WriteFile(marker, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+memoryTestTokenA {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/projects/resolve" && r.URL.Query().Get("legacy_id") == "abcdef":
			_, _ = w.Write([]byte(`{"status":"resolved","project_id":"` + memoryTestProjectA + `","name":"Existing"}`))
		case r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`{"items":[{"project_id":"` + memoryTestProjectA + `","name":"Existing"}],"unresolved":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, _ := startMemoryTestSession(t, root, filepath.Join(scratch, "installation"), server.URL)
	memoryTestInitialize(t, client)
	response := client.call(t, "recovery", "tools/call", map[string]any{"name": "project_context", "arguments": map[string]any{"action": "repair"}})
	if memoryTestRefused(response) {
		t.Fatalf("session-memory local recovery unavailable: %v", response)
	}
	result := memoryTestUnwrap(response)
	if result["project_id"] != memoryTestProjectA || result["target"] != ".engram-project" || result["marker_content"] == "" {
		t.Fatalf("invalid host marker action: %v", response)
	}
	if current, err := os.ReadFile(marker); err != nil || string(current) != string(legacy) {
		t.Fatal("session-memory recovery wrote the marker")
	}
}
