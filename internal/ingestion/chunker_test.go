package ingestion

import (
	"strings"
	"testing"
)

func TestChunkByParagraphs(t *testing.T) {
	doc := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph."
	chunks := ChunkDocument(doc, StrategyParagraphs)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if chunks[0].Text != "First paragraph." {
		t.Errorf("chunk 0 = %q, want 'First paragraph.'", chunks[0].Text)
	}
	if chunks[2].Index != 2 {
		t.Errorf("chunk 2 index = %d, want 2", chunks[2].Index)
	}
}

func TestChunkBySections(t *testing.T) {
	doc := "# Intro\nSome text.\n\n## Details\nMore text.\nExtra line.\n\n### Sub\nFinal."
	chunks := ChunkDocument(doc, StrategySections)
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 section chunks, got %d", len(chunks))
	}
	if chunks[0].Section != "# Intro" {
		t.Errorf("first chunk section = %q, want '# Intro'", chunks[0].Section)
	}
}

func TestChunkByFixed(t *testing.T) {
	doc := strings.Repeat("word ", 300)
	chunks := ChunkDocument(doc, StrategyFixed)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 fixed chunks for 1500 chars, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len([]rune(c.Text)) > 1001 {
			t.Errorf("chunk %d exceeds fixed size: %d runes", c.Index, len([]rune(c.Text)))
		}
	}
}

func TestChunkEmptyDocument(t *testing.T) {
	chunks := ChunkDocument("", StrategyParagraphs)
	if chunks != nil {
		t.Errorf("empty document should produce nil chunks, got %d", len(chunks))
	}
}

func TestChunkWhitespaceOnly(t *testing.T) {
	chunks := ChunkDocument("   \n\n   ", StrategyParagraphs)
	if chunks != nil {
		t.Errorf("whitespace-only should produce nil chunks, got %d", len(chunks))
	}
}
