package mcp

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestExtractProjectFromHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("X-Engram-Project", "engram_67e398f8")
	got := extractProjectFromHeader(r)
	if got != "engram_67e398f8" {
		t.Errorf("expected engram_67e398f8, got %q", got)
	}
}

func TestExtractProjectFromHeader_Missing(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	got := extractProjectFromHeader(r)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestProjectFromContext_RoundTrip(t *testing.T) {
	ctx := contextWithProject(context.Background(), "test_abc12345")
	got := projectFromContext(ctx)
	if got != "test_abc12345" {
		t.Errorf("expected test_abc12345, got %q", got)
	}
}

func TestProjectFromContext_Empty(t *testing.T) {
	got := projectFromContext(context.Background())
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
