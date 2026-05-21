// Package feedback implements citation detection for the closed-loop learning
// pipeline. It identifies which injected memories were actually referenced in
// the agent's output text so the Thompson Sampling priors (TsAlpha/TsBeta) can
// be updated toward more effective memories.
package feedback

import (
	"strings"
	"unicode"

	"github.com/thebtf/engram/pkg/models"
)

// CitationResult records whether a single injected memory was cited in the
// agent's output text.
type CitationResult struct {
	MemoryID int64
	Cited    bool
	Violated bool
	// Excerpt is the first matching phrase from the output, up to 200 runes.
	// Empty when Cited is false.
	Excerpt string
}

// minMatchLength is the minimum number of runes a memory content fragment must
// have before we attempt substring matching. Very short strings produce too many
// false positives.
const minMatchLength = 10

// DetectCitations checks agentOutput for evidence that each memory in memories
// was referenced. A memory is considered cited when a normalised substring of at
// least minMatchLength runes from its content appears in the normalised output.
//
// Normalisation: lowercase, collapse whitespace runs to a single space, trim
// leading/trailing space. This is intentionally conservative — only close
// textual matches count; paraphrase is not detected.
//
// The returned slice has one entry per input memory, in the same order.
func DetectCitations(agentOutput string, memories []*models.Memory) []CitationResult {
	if len(memories) == 0 {
		return nil
	}

	normOutput := normalise(agentOutput)
	results := make([]CitationResult, len(memories))

	for i, m := range memories {
		if m == nil {
			results[i] = CitationResult{MemoryID: 0, Cited: false}
			continue
		}

		// Try to find any phrase from the memory content in the output.
		excerpt, found := findCitation(normOutput, m.Content)
		results[i] = CitationResult{
			MemoryID: m.ID,
			Cited:    found,
			Excerpt:  excerpt,
		}
	}

	return results
}

// findCitation checks whether any fragment of content can be found in the
// normalised output. Priority order per FR-A1:
//   (a) Title match — first line of content appears in output.
//   (b) Full content or clause match — sliding window fallback.
//
// Returns (excerpt, true) on the first match; ("", false) if no match is found.
func findCitation(normOutput, content string) (string, bool) {
	normContent := normalise(content)
	if len([]rune(normContent)) < minMatchLength {
		return "", false
	}

	// (a) Title match: first line of content (FR-A1 bullet 3a).
	title := extractTitle(content)
	normTitle := normalise(title)
	if len([]rune(normTitle)) >= minMatchLength && strings.Contains(normOutput, normTitle) {
		return excerpt(normTitle, 200), true
	}

	// (b) Full content match.
	if strings.Contains(normOutput, normContent) {
		return excerpt(normContent, 200), true
	}

	// (b) Clause-level fallback.
	clauses := splitClauses(normContent)
	for _, clause := range clauses {
		if len([]rune(clause)) < minMatchLength {
			continue
		}
		if strings.Contains(normOutput, clause) {
			return excerpt(clause, 200), true
		}
	}

	return "", false
}

// extractTitle returns the first line of content, trimmed.
func extractTitle(content string) string {
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		return strings.TrimSpace(content[:idx])
	}
	return strings.TrimSpace(content)
}

// normalise lowercases the string and collapses all whitespace runs to a single
// space.
func normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// splitClauses splits on common sentence/clause terminators (.!?;:) and returns
// the trimmed non-empty parts. Each part is already normalised (lowercased +
// collapsed) because the input s is the output of normalise().
func splitClauses(s string) []string {
	delimiters := ".!?;:"
	var parts []string
	start := 0
	runes := []rune(s)
	for i, r := range runes {
		if strings.ContainsRune(delimiters, r) {
			part := strings.TrimSpace(string(runes[start:i]))
			if part != "" {
				parts = append(parts, part)
			}
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}

// negativeKeywords are phrases that indicate a prohibition in a behavioral rule.
var negativeKeywords = []string{
	"never ", "don't ", "do not ", "avoid ", "must not ", "should not ",
	"не ", "никогда ", "запрещ", "нельзя ",
}

// DetectViolations checks whether guidance-type memories were violated in the
// agent output. A violation is detected when a behavioral rule contains a
// negative directive (e.g., "never use stubs") and the prohibited pattern
// appears in the agent output.
//
// Only memories tagged as "guidance" or with EpistemicType "guidance" are
// candidates for violation detection.
func DetectViolations(agentOutput string, results []CitationResult, memories []*models.Memory) []CitationResult {
	if len(results) == 0 || agentOutput == "" {
		return results
	}

	memMap := make(map[int64]*models.Memory, len(memories))
	for _, m := range memories {
		if m != nil {
			memMap[m.ID] = m
		}
	}

	normOutput := normalise(agentOutput)

	for i := range results {
		if results[i].Cited {
			continue
		}
		mem := memMap[results[i].MemoryID]
		if mem == nil {
			continue
		}
		if !isGuidanceMemory(mem) {
			continue
		}
		if detectViolation(normOutput, mem.Content) {
			results[i].Violated = true
		}
	}
	return results
}

func isGuidanceMemory(m *models.Memory) bool {
	if m.EpistemicType == "guidance" {
		return true
	}
	for _, tag := range m.Tags {
		if tag == "guidance" || tag == "rule" || tag == "behavioral" {
			return true
		}
	}
	return false
}

func detectViolation(normOutput, content string) bool {
	normContent := normalise(content)
	lines := strings.Split(normContent, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		for _, kw := range negativeKeywords {
			idx := strings.Index(line, kw)
			if idx < 0 {
				continue
			}
			prohibited := strings.TrimSpace(line[idx+len(kw):])
			if len([]rune(prohibited)) < minMatchLength {
				continue
			}
			clauses := splitClauses(prohibited)
			for _, clause := range clauses {
				if len([]rune(clause)) < minMatchLength {
					continue
				}
				if strings.Contains(normOutput, clause) {
					return true
				}
			}
		}
	}
	return false
}

// excerpt returns up to maxRunes runes from s, appending "…" if truncated.
func excerpt(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}
