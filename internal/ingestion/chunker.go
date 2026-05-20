// Package ingestion provides document chunking and ingestion for knowledge storage.
package ingestion

import (
	"strings"
)

// ChunkStrategy determines how a document is split.
type ChunkStrategy string

const (
	StrategyParagraphs ChunkStrategy = "paragraphs"
	StrategySections   ChunkStrategy = "sections"
	StrategyFixed      ChunkStrategy = "fixed"
)

// Chunk is a single piece of a chunked document.
type Chunk struct {
	Index   int    `json:"index"`
	Text    string `json:"text"`
	Section string `json:"section,omitempty"`
}

// ChunkDocument splits a document into chunks using the given strategy.
func ChunkDocument(content string, strategy ChunkStrategy) []Chunk {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	switch strategy {
	case StrategySections:
		return chunkBySections(content)
	case StrategyFixed:
		return chunkByFixed(content, 1000, 200)
	default:
		return chunkByParagraphs(content)
	}
}

func chunkByParagraphs(content string) []Chunk {
	parts := strings.Split(content, "\n\n")
	var chunks []Chunk
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		chunks = append(chunks, Chunk{Index: len(chunks), Text: p})
	}
	return chunks
}

func chunkBySections(content string) []Chunk {
	lines := strings.Split(content, "\n")
	var chunks []Chunk
	var current strings.Builder
	currentSection := ""

	flush := func() {
		text := strings.TrimSpace(current.String())
		if text != "" {
			chunks = append(chunks, Chunk{
				Index:   len(chunks),
				Text:    text,
				Section: currentSection,
			})
		}
		current.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			flush()
			currentSection = trimmed
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	flush()
	return chunks
}

func chunkByFixed(content string, size, overlap int) []Chunk {
	runes := []rune(content)
	var chunks []Chunk
	for start := 0; start < len(runes); start += size - overlap {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		text := strings.TrimSpace(string(runes[start:end]))
		if text != "" {
			chunks = append(chunks, Chunk{Index: len(chunks), Text: text})
		}
		if end == len(runes) {
			break
		}
	}
	return chunks
}
