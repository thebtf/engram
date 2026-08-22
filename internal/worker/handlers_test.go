package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type provenanceResponse struct {
	Version      string `json:"version"`
	SourceCommit string `json:"source_commit"`
}

func TestHealthAndVersionExposeFullSourceCommit(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	original := sourceCommit()
	SetSourceCommit(commit)
	t.Cleanup(func() { SetSourceCommit(original) })

	svc := &Service{version: "test-version"}
	for name, handler := range map[string]func(http.ResponseWriter, *http.Request){
		"health":  svc.handleHealth,
		"version": svc.handleVersion,
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler(response, httptest.NewRequest(http.MethodGet, "/api/"+name, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}

			var body provenanceResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Version != "test-version" {
				t.Fatalf("version = %q, want %q", body.Version, "test-version")
			}
			if body.SourceCommit != commit {
				t.Fatalf("source_commit = %q, want %q", body.SourceCommit, commit)
			}
		})
	}
}

func TestHealthDoesNotExposeMissingOrInvalidSourceCommit(t *testing.T) {
	original := sourceCommit()
	t.Cleanup(func() { SetSourceCommit(original) })

	for _, commit := range []string{
		"",
		"dev",
		"0123456789ABCDEF0123456789ABCDEF01234567",
		"0123456789abcdef0123456789abcdef0123456",
		"0123456789abcdef0123456789abcdef0123456g",
	} {
		t.Run(commit, func(t *testing.T) {
			SetSourceCommit(commit)
			response := httptest.NewRecorder()
			(&Service{}).handleHealth(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))

			var body provenanceResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.SourceCommit != "" {
				t.Fatalf("source_commit = %q, want empty for invalid input %q", body.SourceCommit, commit)
			}
		})
	}
}
