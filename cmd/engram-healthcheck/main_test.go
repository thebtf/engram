package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRun_UsageAndReadinessExitCodes(t *testing.T) {
	t.Parallel()

	t.Run("usage", func(t *testing.T) {
		var stderr bytes.Buffer
		if got := run(context.Background(), nil, &stderr); got != 2 {
			t.Fatalf("usage exit code = %d, want 2", got)
		}
		if !strings.Contains(stderr.String(), "usage: engram-healthcheck") {
			t.Fatalf("usage error missing: %q", stderr.String())
		}
	})

	t.Run("ready", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		}))
		t.Cleanup(server.Close)
		var stderr bytes.Buffer
		if got := run(context.Background(), []string{server.URL + "/api/ready"}, &stderr); got != 0 {
			t.Fatalf("ready exit code = %d, stderr = %q", got, stderr.String())
		}
	})

	t.Run("not ready", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"error"}`))
		}))
		t.Cleanup(server.Close)
		var stderr bytes.Buffer
		if got := run(context.Background(), []string{server.URL + "/api/ready"}, &stderr); got != 1 {
			t.Fatalf("not-ready exit code = %d, want 1", got)
		}
		if !strings.Contains(stderr.String(), "readiness check failed") {
			t.Fatalf("readiness error missing: %q", stderr.String())
		}
	})
}

func TestContainerReadiness_ExactReadySucceeds(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	t.Cleanup(server.Close)

	if err := checkReady(context.Background(), server.URL+"/api/ready"); err != nil {
		t.Fatalf("exact semantic readiness must pass: %v", err)
	}
}

func TestContainerReadiness_NonReadyResponsesFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "error status", status: http.StatusOK, body: `{"status":"error"}`},
		{name: "wrong case", status: http.StatusOK, body: `{"status":"READY"}`},
		{name: "missing status", status: http.StatusOK, body: `{}`},
		{name: "extra field", status: http.StatusOK, body: `{"status":"ready","version":"dev"}`},
		{name: "duplicate status", status: http.StatusOK, body: `{"status":"error","status":"ready"}`},
		{name: "malformed json", status: http.StatusOK, body: `{"status":`},
		{name: "trailing json", status: http.StatusOK, body: `{"status":"ready"}{}`},
		{name: "http failure", status: http.StatusServiceUnavailable, body: `{"status":"ready"}`},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)

			if err := checkReady(context.Background(), server.URL+"/api/ready"); err == nil {
				t.Fatal("non-ready response must fail closed")
			}
		})
	}

	t.Run("oversized body", func(t *testing.T) {
		t.Parallel()

		const ready = `{"status":"ready"}`
		body := ready + strings.Repeat(" ", maxResponseBody-len(ready))
		for _, test := range []struct {
			name    string
			body    string
			wantErr string
		}{
			{name: "at limit", body: body},
			{name: "over limit", body: body + " ", wantErr: "response exceeds size limit"},
		} {
			t.Run(test.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(test.body))
				}))
				t.Cleanup(server.Close)
				err := checkReady(context.Background(), server.URL+"/api/ready")
				if test.wantErr == "" {
					if err != nil {
						t.Fatalf("valid readiness response at size limit: %v", err)
					}
				} else if err == nil || err.Error() != test.wantErr {
					t.Fatalf("oversized valid readiness response: got %v, want %q", err, test.wantErr)
				}
			})
		}
	})

	t.Run("redirect", func(t *testing.T) {
		t.Parallel()

		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		}))
		t.Cleanup(target.Close)
		redirect := httptest.NewServer(http.RedirectHandler(target.URL+"/api/ready", http.StatusFound))
		t.Cleanup(redirect.Close)

		if err := checkReady(context.Background(), redirect.URL+"/api/ready"); err == nil {
			t.Fatal("redirect must not turn a different endpoint into readiness")
		}
	})

	t.Run("cancelled request", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := checkReady(ctx, "http://127.0.0.1:1/api/ready"); err == nil {
			t.Fatal("cancelled request must fail closed")
		}
	})

	t.Run("invalid endpoint", func(t *testing.T) {
		t.Parallel()

		for _, test := range []struct {
			endpoint string
			wantErr  string
		}{
			{endpoint: "file:///tmp/ready", wantErr: "endpoint must be an absolute HTTP(S) URL"},
			{endpoint: "http://user:secret@example.test/api/ready", wantErr: "endpoint must contain only the /api/ready path"},
			{endpoint: "http://example.test/health", wantErr: "endpoint must contain only the /api/ready path"},
			{endpoint: "http://example.test/api/ready?token=secret", wantErr: "endpoint must contain only the /api/ready path"},
		} {
			test := test
			t.Run(fmt.Sprintf("%x", test.endpoint), func(t *testing.T) {
				t.Parallel()
				var requests atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					_, _ = w.Write([]byte(`{"status":"ready"}`))
				}))
				t.Cleanup(server.Close)
				endpoint := strings.ReplaceAll(test.endpoint, "example.test", strings.TrimPrefix(server.URL, "http://"))
				if err := checkReady(context.Background(), endpoint); err == nil || err.Error() != test.wantErr {
					t.Fatalf("invalid endpoint: got %v, want %q", err, test.wantErr)
				}
				if got := requests.Load(); got != 0 {
					t.Fatalf("invalid endpoint sent %d requests", got)
				}
			})
		}
	})
}
