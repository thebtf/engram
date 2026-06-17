package reranking

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestNewClient_DisabledWhenURLUnset(t *testing.T) {
	// Ensure the var is unset for this test regardless of ambient env.
	old, had := os.LookupEnv("ENGRAM_RERANK_URL")
	os.Unsetenv("ENGRAM_RERANK_URL")
	t.Cleanup(func() {
		if had {
			os.Setenv("ENGRAM_RERANK_URL", old)
		}
	})

	_, err := NewClient()
	if !errors.Is(err, ErrRerankDisabled) {
		t.Fatalf("NewClient() err = %v, want ErrRerankDisabled when ENGRAM_RERANK_URL unset", err)
	}
}

func TestNewClient_DefaultsModelAndEnables(t *testing.T) {
	t.Setenv("ENGRAM_RERANK_URL", "https://llm.example.test/v1/rerank")
	os.Unsetenv("ENGRAM_RERANK_MODEL")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient() err = %v, want nil", err)
	}
	if c.Model() != "bge-reranker" {
		t.Errorf("default model = %q, want %q (NOT the v5 -v2-m3 alias)", c.Model(), "bge-reranker")
	}
	// baseURL must have the /v1/rerank suffix stripped so the canonical path re-appends cleanly.
	if c.baseURL != "https://llm.example.test" {
		t.Errorf("baseURL = %q, want %q (canonical path suffix stripped)", c.baseURL, "https://llm.example.test")
	}
}

func TestNormalizeRerankBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://host":               "https://host",
		"https://host/":              "https://host",
		"https://host/v1":            "https://host",
		"https://host/v1/":           "https://host",
		"https://host/rerank":        "https://host",
		"https://host/v1/rerank":     "https://host",
		"https://host/v1/rerank/":    "https://host",
		"https://v1.example.com":     "https://v1.example.com", // host 'v1' must survive
		"https://host/api/v2/rerank": "https://host/api/v2",    // only the documented suffixes are stripped
	}
	for in, want := range cases {
		if got := normalizeRerankBaseURL(in); got != want {
			t.Errorf("normalizeRerankBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRank_ShortCircuitsOnTrivialInput(t *testing.T) {
	t.Setenv("ENGRAM_RERANK_URL", "https://unreachable.invalid/v1/rerank")
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient() err = %v", err)
	}
	// 0 and 1 passage must NOT make an HTTP call (the URL is unreachable; a call would
	// error). They short-circuit to a trivial result.
	for _, n := range []int{0, 1} {
		passages := make([]string, n)
		for i := range passages {
			passages[i] = "x"
		}
		got, rErr := c.Rank(context.Background(), "q", passages)
		if rErr != nil {
			t.Errorf("Rank(%d passages) err = %v, want nil (must short-circuit, no HTTP)", n, rErr)
		}
		if len(got) != n {
			t.Errorf("Rank(%d passages) returned %d scores, want %d", n, len(got), n)
		}
	}
}
