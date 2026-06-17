package reranking

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

// fakeResolver is an in-memory SettingsResolver for precedence tests (no DB).
type fakeResolver map[string]string

func (f fakeResolver) Get(_ context.Context, key string) (string, bool) {
	v, ok := f[key]
	return v, ok
}

// TestNewClientWithSettings_EnvFirstPrecedence pins the CR-2 (#259) read-path contract:
// env wins over the settings-store; the store fills in when env is empty; the built-in
// default applies only when both are empty.
func TestNewClientWithSettings_EnvFirstPrecedence(t *testing.T) {
	// Start from a clean slate so ambient env never leaks into the cases.
	os.Unsetenv("ENGRAM_RERANK_URL")
	os.Unsetenv("ENGRAM_RERANK_MODEL")

	t.Run("env wins over settings", func(t *testing.T) {
		t.Setenv("ENGRAM_RERANK_URL", "https://env.example.test/v1/rerank")
		t.Setenv("ENGRAM_RERANK_MODEL", "env-model")
		res := fakeResolver{
			SettingKeyRerankURL:   "https://store.example.test/v1/rerank",
			SettingKeyRerankModel: "store-model",
		}
		c, err := NewClientWithSettings(context.Background(), res)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if c.baseURL != "https://env.example.test" {
			t.Errorf("baseURL = %q, want env value to win", c.baseURL)
		}
		if c.Model() != "env-model" {
			t.Errorf("model = %q, want env value to win", c.Model())
		}
	})

	t.Run("settings fill in when env empty", func(t *testing.T) {
		os.Unsetenv("ENGRAM_RERANK_URL")
		os.Unsetenv("ENGRAM_RERANK_MODEL")
		res := fakeResolver{
			SettingKeyRerankURL:   "https://store.example.test/v1/rerank",
			SettingKeyRerankModel: "store-model",
		}
		c, err := NewClientWithSettings(context.Background(), res)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if c.baseURL != "https://store.example.test" {
			t.Errorf("baseURL = %q, want store value when env unset", c.baseURL)
		}
		if c.Model() != "store-model" {
			t.Errorf("model = %q, want store value when env unset", c.Model())
		}
	})

	t.Run("model default when both empty", func(t *testing.T) {
		os.Unsetenv("ENGRAM_RERANK_MODEL")
		// URL comes from the store so the client enables; model is absent everywhere.
		res := fakeResolver{SettingKeyRerankURL: "https://store.example.test"}
		c, err := NewClientWithSettings(context.Background(), res)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if c.Model() != "bge-reranker" {
			t.Errorf("model = %q, want built-in default when env+store empty", c.Model())
		}
	})

	t.Run("disabled when URL absent everywhere", func(t *testing.T) {
		os.Unsetenv("ENGRAM_RERANK_URL")
		// Empty resolver: no URL in env or store.
		_, err := NewClientWithSettings(context.Background(), fakeResolver{})
		if !errors.Is(err, ErrRerankDisabled) {
			t.Fatalf("err = %v, want ErrRerankDisabled when URL absent in env+store", err)
		}
	})

	t.Run("nil resolver is env-only", func(t *testing.T) {
		os.Unsetenv("ENGRAM_RERANK_URL")
		_, err := NewClientWithSettings(context.Background(), nil)
		if !errors.Is(err, ErrRerankDisabled) {
			t.Fatalf("err = %v, want ErrRerankDisabled with nil resolver and no env", err)
		}
	})
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
		// gemini review: query/fragment must be stripped so the re-appended /v1/rerank
		// path is not corrupted into ".../v1?foo=bar/v1/rerank" etc.
		"https://host/v1?foo=bar":    "https://host",
		"https://host/rerank#frag":   "https://host",
		"https://host/v1/rerank?x=1": "https://host",
	}
	for in, want := range cases {
		if got := normalizeRerankBaseURL(in); got != want {
			t.Errorf("normalizeRerankBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRank_DefendsUntrustedResponse confirms the client hardens against a malformed
// endpoint response: out-of-range and duplicate indices are dropped (keeping the first
// occurrence), so a misbehaving reranker cannot corrupt the downstream reorder.
func TestRank_DefendsUntrustedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 3 passages sent; respond with a duplicate index (1) and an out-of-range (9).
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"index":2,"relevance_score":0.9},
			{"index":1,"relevance_score":0.8},
			{"index":1,"relevance_score":0.4},
			{"index":9,"relevance_score":0.99},
			{"index":0,"relevance_score":0.1}
		]}`))
	}))
	defer srv.Close()

	t.Setenv("ENGRAM_RERANK_URL", srv.URL)
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient() err = %v", err)
	}

	got, rErr := c.Rank(context.Background(), "q", []string{"a", "b", "c"})
	if rErr != nil {
		t.Fatalf("Rank() err = %v, want nil", rErr)
	}
	// Expect indices [2,1,0] — duplicate 1 collapsed to first occurrence, 9 dropped.
	wantIdx := []int{2, 1, 0}
	if len(got) != len(wantIdx) {
		t.Fatalf("got %d results, want %d: %+v", len(got), len(wantIdx), got)
	}
	for i, w := range wantIdx {
		if got[i].Index != w {
			t.Errorf("result[%d].Index = %d, want %d (dedup/range hardening)", i, got[i].Index, w)
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
