package crystallization

import (
	"testing"
)

func TestExtractDecisions_DecidedPattern(t *testing.T) {
	text := "After reviewing the options, I decided to use PostgreSQL because it supports pgvector natively."
	results := ExtractDecisions(text)
	if len(results) == 0 {
		t.Fatal("expected at least 1 decision, got 0")
	}
	if results[0].Position < 0 {
		t.Errorf("position should be non-negative, got %d", results[0].Position)
	}
}

func TestExtractDecisions_ChoseOverPattern(t *testing.T) {
	text := "We chose Redis over Memcached for the caching layer."
	results := ExtractDecisions(text)
	if len(results) == 0 {
		t.Fatal("expected at least 1 decision for 'chose X over Y' pattern")
	}
}

func TestExtractDecisions_GoingForward(t *testing.T) {
	text := "Going forward, all new migrations will use the expand-and-contract pattern."
	results := ExtractDecisions(text)
	if len(results) == 0 {
		t.Fatal("expected at least 1 decision for 'going forward' pattern")
	}
}

func TestExtractDecisions_NoPatterns(t *testing.T) {
	text := "The server is running on port 37777. Everything looks good."
	results := ExtractDecisions(text)
	if len(results) != 0 {
		t.Errorf("expected 0 decisions, got %d", len(results))
	}
}

func TestExtractDecisions_EmptyInput(t *testing.T) {
	results := ExtractDecisions("")
	if results != nil {
		t.Errorf("empty input should return nil, got %d results", len(results))
	}
}

func TestExtractDecisions_Dedup(t *testing.T) {
	text := "I decided to use Go. Later, I decided to use Go again."
	results := ExtractDecisions(text)
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].Text == results[j].Text {
				t.Errorf("duplicate decision found: %q", results[i].Text)
			}
		}
	}
}
