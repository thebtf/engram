package worker

import (
	"fmt"
	"testing"
)

func TestParseHitRateAnalyticsTextPreservesTitleSpacesAndFlags(t *testing.T) {
	sampleText := `## Hit Rate Analytics (2 observations)

### Noise Candidates (injected 10+ times, never cited)
- [101] Multi Word Noise Title (guidance)

### High Value (injected 5+ times, >50% citation rate)
- [202] High Value Title With Spaces (decision)`

	observations, err := parseHitRateAnalyticsText(sampleText)
	if err != nil {
		t.Fatalf("parseHitRateAnalyticsText returned error: %v", err)
	}

	if len(observations) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(observations))
	}

	noiseCount := 0
	highValueCount := 0
	var noiseObs, highValueObs map[string]any

	for _, obs := range observations {
		flag := fmt.Sprintf("%v", obs["flag"])
		switch flag {
		case "noise_candidate":
			noiseCount++
			noiseObs = obs
		case "high_value":
			highValueCount++
			highValueObs = obs
		}
	}

	if noiseCount != 1 {
		t.Fatalf("expected exactly 1 noise_candidate, got %d", noiseCount)
	}

	if highValueCount != 1 {
		t.Fatalf("expected exactly 1 high_value, got %d", highValueCount)
	}

	if fmt.Sprintf("%v", noiseObs["title"]) != "Multi Word Noise Title" {
		t.Fatalf("expected noise candidate title 'Multi Word Noise Title', got %q", noiseObs["title"])
	}

	if fmt.Sprintf("%v", highValueObs["title"]) != "High Value Title With Spaces" {
		t.Fatalf("expected high value title 'High Value Title With Spaces', got %q", highValueObs["title"])
	}

	if fmt.Sprintf("%v", noiseObs["type"]) != "guidance" {
		t.Fatalf("expected noise candidate type 'guidance', got %q", noiseObs["type"])
	}

	if fmt.Sprintf("%v", highValueObs["type"]) != "decision" {
		t.Fatalf("expected high value type 'decision', got %q", highValueObs["type"])
	}
}
