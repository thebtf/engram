package mining

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// MineOptions configures the mining pipeline.
type MineOptions struct {
	Project    string
	SourceFile string
	MaxResults int // 0 = unlimited
}

// MiningResult is one extracted observation candidate.
type MiningResult struct {
	Text       string
	Type       string   // decision, preference, milestone, problem, emotional, general
	Concepts   []string
	SourceHash string // SHA-256 hex digest of Text for deduplication
	Confidence float64
}

// Mine runs the full zero-API extraction pipeline:
//
//  1. Normalize: strip ANSI, unify line endings, collapse blank lines, detect format
//  2. FilterCode: remove fenced blocks and code definition lines
//  3. Segment: if format != Generic use ExtractExchanges; otherwise ChunkText
//  4. Classify each segment; discard those below the confidence threshold
//  5. DetectConcepts for surviving segments
//  6. Hash text for deduplication; deduplicate by hash
//  7. Honour MaxResults cap if set
func Mine(text string, opts MineOptions) ([]MiningResult, error) {
	// Step 1: normalise.
	clean, format := Normalize(text)

	// Step 2: remove code.
	filtered := FilterCode(clean)

	// Step 3: segment.
	type piece struct {
		text string
	}
	var pieces []piece

	if format != FormatGeneric {
		exchanges := ExtractExchanges(filtered, format)
		for _, ex := range exchanges {
			if t := strings.TrimSpace(ex.UserTurn); t != "" {
				pieces = append(pieces, piece{text: t})
			}
			if t := strings.TrimSpace(ex.AITurn); t != "" {
				pieces = append(pieces, piece{text: t})
			}
		}
	} else {
		chunks := ChunkText(filtered, 800, 100)
		for _, ch := range chunks {
			if t := strings.TrimSpace(ch.Text); t != "" {
				pieces = append(pieces, piece{text: t})
			}
		}
	}

	// Steps 4–6: classify, concept-detect, hash, dedup.
	seen := make(map[string]struct{})
	var results []MiningResult

	buckets := DefaultBuckets()

	for _, p := range pieces {
		memType, conf := Classify(p.text)
		if conf < classifyThreshold {
			continue
		}

		concepts := DetectConcepts(p.text, buckets)
		hash := hashText(p.text)

		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}

		results = append(results, MiningResult{
			Text:       p.text,
			Type:       memType,
			Concepts:   concepts,
			SourceHash: hash,
			Confidence: conf,
		})

		// Step 7: honour cap.
		if opts.MaxResults > 0 && len(results) >= opts.MaxResults {
			break
		}
	}

	return results, nil
}

// hashText returns the SHA-256 hex digest of the given string.
func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum)
}
