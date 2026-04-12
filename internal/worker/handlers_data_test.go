package worker

import (
	"strings"
	"testing"
)

func TestBuildHitRateAnalyticsResponsePreservesTitleSpacesFlagsAndTypes(t *testing.T) {
	rows := []hitRateAnalyticsRow{
		{ID: 101, Title: "Multi Word Noise Title", Type: "guidance", Flag: "noise_candidate"},
		{ID: 202, Title: "High Value Title With Spaces", Type: "decision", Flag: "high_value"},
	}

	response := buildHitRateAnalyticsResponse(rows)

	if response["noise_candidates"] != 1 {
		t.Fatalf("expected noise_candidates=1, got %v", response["noise_candidates"])
	}
	if response["high_value"] != 1 {
		t.Fatalf("expected high_value=1, got %v", response["high_value"])
	}
	if response["total"] != 2 {
		t.Fatalf("expected total=2, got %v", response["total"])
	}

	observations, ok := response["observations"].([]map[string]any)
	if !ok {
		t.Fatalf("expected observations to be []map[string]any, got %T", response["observations"])
	}
	if len(observations) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(observations))
	}

	if observations[0]["title"] != "Multi Word Noise Title" {
		t.Fatalf("expected first title to preserve spaces, got %q", observations[0]["title"])
	}
	if observations[0]["flag"] != "noise_candidate" {
		t.Fatalf("expected first flag noise_candidate, got %q", observations[0]["flag"])
	}
	if observations[0]["type"] != "guidance" {
		t.Fatalf("expected first type guidance, got %q", observations[0]["type"])
	}

	if observations[1]["title"] != "High Value Title With Spaces" {
		t.Fatalf("expected second title to preserve spaces, got %q", observations[1]["title"])
	}
	if observations[1]["flag"] != "high_value" {
		t.Fatalf("expected second flag high_value, got %q", observations[1]["flag"])
	}
	if observations[1]["type"] != "decision" {
		t.Fatalf("expected second type decision, got %q", observations[1]["type"])
	}
}

func TestFormatHitRateAnalyticsMarkdownRendersFromStructuredRows(t *testing.T) {
	rows := []hitRateAnalyticsRow{
		{ID: 101, Title: "Multi Word Noise Title", Type: "guidance", Flag: "noise_candidate"},
		{ID: 202, Title: "High Value Title With Spaces", Type: "decision", Flag: "high_value"},
	}

	text := formatHitRateAnalyticsMarkdown(rows)

	if !strings.Contains(text, "## Hit Rate Analytics (2 observations)") {
		t.Fatalf("expected markdown header with observation count, got %q", text)
	}
	if !strings.Contains(text, "- [101] Multi Word Noise Title (guidance)") {
		t.Fatalf("expected markdown to include noise candidate row, got %q", text)
	}
	if !strings.Contains(text, "- [202] High Value Title With Spaces (decision)") {
		t.Fatalf("expected markdown to include high value row, got %q", text)
	}
}
