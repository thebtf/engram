package embedding

import "testing"

// TestNormalizeEmbeddingBaseURL verifies that the helper strips a trailing
// "/v1" path segment (and surrounding slashes) without touching hosts that
// contain "v1" in the hostname itself.
func TestNormalizeEmbeddingBaseURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Plain host — no trailing slash, no /v1
		{name: "plain_host", in: "https://host", want: "https://host"},
		// Trailing slash only
		{name: "trailing_slash", in: "https://host/", want: "https://host"},
		// /v1 suffix — the canonical operator mistake
		{name: "v1_suffix", in: "https://host/v1", want: "https://host"},
		// /v1/ with trailing slash
		{name: "v1_trailing_slash", in: "https://host/v1/", want: "https://host"},
		// "v1" in hostname must NOT be stripped
		{name: "v1_in_hostname", in: "https://v1.example.com", want: "https://v1.example.com"},
		// "v1" in hostname WITH /v1 path — only path segment stripped
		{name: "v1_hostname_and_path", in: "https://v1.example.com/v1", want: "https://v1.example.com"},
		// "v1" in hostname WITH /v1/ path
		{name: "v1_hostname_and_path_slash", in: "https://v1.example.com/v1/", want: "https://v1.example.com"},
		// Subdirectory after host that is not /v1 — must be kept
		{name: "non_v1_path", in: "https://host/api", want: "https://host/api"},
		// Port in URL
		{name: "with_port", in: "http://localhost:8080/v1", want: "http://localhost:8080"},
		// Port with trailing slash
		{name: "with_port_slash", in: "http://localhost:8080/v1/", want: "http://localhost:8080"},
		// Double /v1 path — only the last /v1 segment is ever appended by Embed;
		// normalizer must collapse repeated /v1 so callers supplying "https://host/v1/v1"
		// still produce a single-segment base URL (one-segment-only invariant).
		{name: "double_v1_path", in: "https://host/v1/v1", want: "https://host/v1"},
		// Double trailing slash (edge case)
		{name: "double_trailing_slash", in: "https://host//", want: "https://host"},
		// Empty string remains empty
		{name: "empty", in: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeEmbeddingBaseURL(tc.in)
			if got != tc.want {
				t.Errorf("normalizeEmbeddingBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNewClient_NormalizesV1URL verifies that NewClient itself stores the
// normalized URL — an httptest server registered without /v1 responds correctly
// even when ENGRAM_EMBEDDING_URL is set with a trailing /v1.
func TestNewClient_NormalizesV1URL(t *testing.T) {
	authChan := make(chan string, 1)
	srv := captureServer(t, authChan)
	defer srv.Close()

	// Supply the URL with a trailing /v1 — NewClient must strip it so that
	// Embed appends "/v1/embeddings" and hits the test server's root path.
	t.Setenv("ENGRAM_EMBEDDING_URL", srv.URL+"/v1")
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != srv.URL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, srv.URL)
	}
	// Embed must succeed — the constructed URL must hit the test server.
	if _, err := c.Embed(t.Context(), []string{"ping"}); err != nil {
		t.Fatalf("Embed after /v1 normalization: %v", err)
	}
}
