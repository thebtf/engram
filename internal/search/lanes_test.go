package search

import (
	"testing"
)

func TestDefaultSearchLanes_GuidanceDefaults(t *testing.T) {
	cfg, ok := DefaultSearchLanes["guidance"]
	if !ok {
		t.Fatal("guidance lane missing")
	}
	if cfg.MinScore != 0.20 || cfg.TopK != 5 || cfg.RerankerWeight != 1.5 {
		t.Fatalf("unexpected guidance config: %+v", cfg)
	}
}

func TestDefaultSearchLanes_DecisionDefaults(t *testing.T) {
	cfg, ok := DefaultSearchLanes["decision"]
	if !ok {
		t.Fatal("decision lane missing")
	}
	if cfg.MinScore != 0.55 || cfg.TopK != 3 || cfg.RerankerWeight != 1.0 {
		t.Fatalf("unexpected decision config: %+v", cfg)
	}
}

func TestDefaultSearchLanes_DefaultKeyPresent(t *testing.T) {
	cfg, ok := DefaultSearchLanes["default"]
	if !ok {
		t.Fatal("default lane missing")
	}
	if cfg.MinScore != 0.35 || cfg.TopK != 10 || cfg.RerankerWeight != 1.0 {
		t.Fatalf("unexpected default config: %+v", cfg)
	}
}
