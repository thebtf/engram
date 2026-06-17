// Package feedback implements citation detection for the closed-loop learning
// pipeline. It identifies which injected memories were actually referenced in
// the agent's output text so the Thompson Sampling priors (TsAlpha/TsBeta) can
// be updated toward more effective memories.
package feedback

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/thebtf/engram/pkg/models"
)

// injectedBlockRe matches engram's OWN injected context blocks — the memory- and
// issue-bearing wrappers the session-start and pre-compact hooks emit — together with
// their inner text. Verified against current hook source (2026-06-17):
//   - <engram-static-memories>   session-start.js:60   (memory text)
//   - <engram-reinjection>       pre-compact.js:51      (memory text re-injected at compaction)
//   - <user-behavior-rules>      session-start.js:33    (behavioral-rule text)
//   - <open-issues ...>          lib.js:778             (issue text)
// Plus any FUTURE <engram-*> block, so a newly-added memory-bearing tag is covered without
// editing this regex (the demolition/staleness trap: a hard-coded tag list silently goes
// stale). Non-engram-prefixed wrappers (user-behavior-rules, open-issues) are named
// explicitly because they carry no prefix.
//
// (?s) so '.' spans newlines; non-greedy so adjacent blocks close at the first matching tag.
// RE2 has no backreferences, so open/close tags are not forced to be the same name; the only
// resulting imprecision is over-matching between two mismatched engram tags, which is benign —
// it can only ever remove engram's own injected text, never arbitrary agent prose.
var injectedBlockRe = regexp.MustCompile(`(?s)<(engram-[a-z-]+|user-behavior-rules|open-issues)\b[^>]*>.*?</\s*(engram-[a-z-]+|user-behavior-rules|open-issues)\s*>`)

// StripInjectedBlocks removes engram's own injected context blocks from agent output
// BEFORE citation detection (rank-2 anti-poisoning). The stop hook (stop.js) extracts ONLY
// assistant-role text into agent_output_text, so injected wrappers reach this path only when
// the agent echoes or quotes an injected block verbatim in its own turn. When it does,
// DetectCitations would match the memory against engram's OWN injection and falsely mark it
// "cited" — inflating citation_count with self-citation rather than genuine usage, and
// corrupting the rank-1 feedback signal (and the rank-5/6 reinforcement built on it).
// Stripping the wrappers first ensures only the agent's OWN references to a memory count.
//
// Defensive and lossless for the matcher: it removes only engram-owned tag blocks; if no
// such block is present (the common case — the agent rarely re-emits injected XML) the
// output is returned unchanged.
func StripInjectedBlocks(agentOutput string) string {
	if agentOutput == "" || (!strings.Contains(agentOutput, "<engram-") &&
		!strings.Contains(agentOutput, "<user-behavior-rules") &&
		!strings.Contains(agentOutput, "<open-issues")) {
		// Fast path: no engram wrapper tag present at all — nothing to strip.
		return agentOutput
	}
	return injectedBlockRe.ReplaceAllString(agentOutput, " ")
}

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
	for _, kw := range negativeKeywords {
		normKw := normalise(kw)
		idx := strings.Index(normContent, normKw)
		if idx < 0 {
			continue
		}
		prohibited := strings.TrimSpace(normContent[idx+len(normKw):])
		if len([]rune(prohibited)) < minMatchLength {
			continue
		}
		if strings.Contains(normOutput, prohibited) {
			return true
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
		// Try phrase-level matching: split on "or"/"and" connectors.
		phrases := splitOnConnectors(prohibited)
		for _, phrase := range phrases {
			if len([]rune(phrase)) < minMatchLength {
				continue
			}
			if strings.Contains(normOutput, phrase) {
				return true
			}
		}
		// Try without leading verb (e.g., "use X" → "X").
		if spaceIdx := strings.IndexByte(prohibited, ' '); spaceIdx > 0 {
			tail := strings.TrimSpace(prohibited[spaceIdx:])
			if len([]rune(tail)) >= minMatchLength && strings.Contains(normOutput, tail) {
				return true
			}
		}
	}
	return false
}

func splitOnConnectors(s string) []string {
	connectors := []string{" or ", " and ", " и ", " или "}
	var parts []string
	for _, conn := range connectors {
		if strings.Contains(s, conn) {
			for _, p := range strings.Split(s, conn) {
				p = strings.TrimSpace(p)
				if p != "" {
					parts = append(parts, p)
				}
			}
			return parts
		}
	}
	return nil
}

// excerpt returns up to maxRunes runes from s, appending "…" if truncated.
func excerpt(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}
