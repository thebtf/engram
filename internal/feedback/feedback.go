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

// openInjectedTagRe matches the OPENING of an engram-owned wrapper. Tag set verified against
// current hook source (2026-06-17):
//   - <engram-static-memories>   session-start.js:60   (memory text)
//   - <user-behavior-rules>      session-start.js:33    (behavioral-rule text)
//   - <open-issues ...>          lib.js:778             (issue text)
// The `engram-[a-z-]+` arm also covers any FUTURE <engram-*> wrapper without a regex edit (the
// demolition/staleness trap: a hard-coded tag list silently goes stale). It additionally covers
// the latent <engram-reinjection> XML form produced by pre-compact.js formatReinjectionBlock —
// exported for future CC versions but NOT on the live path today (the live pre-compact path
// writes markdown, handled by stripReinjectionMarkdown below). Non-engram-prefixed wrappers
// (user-behavior-rules, open-issues) are named explicitly because they carry no prefix.
var openInjectedTagRe = regexp.MustCompile(`<(engram-[a-z-]+|user-behavior-rules|open-issues)\b[^>]*>`)

// closeInjectedTagRe matches the CLOSING of any engram-owned wrapper. RE2 has no backreferences,
// so a single open/close regex cannot force the two tags to share a name; instead
// stripInjectedXMLBlocks pairs each opening tag to its OWN closing tag by name comparison. This
// prevents the false-negative Gemini flagged on PR #297: an unclosed <engram-static-memories>
// followed later by a </user-behavior-rules> must NOT make a greedy/non-greedy match swallow the
// genuine agent prose between them.
var closeInjectedTagRe = regexp.MustCompile(`</\s*(engram-[a-z-]+|user-behavior-rules|open-issues)\s*>`)

// reinjectionMarkdownHeader is the sentinel the pre-compact hook writes at the top of
// .engram/reinjection.md (pre-compact.js:127). That file is the live re-injection surface; its
// body is this header, a "Topic:" line, then a run of "- <memory content>" bullets. If the agent
// quotes the file verbatim in its turn, those bullets would self-cite memories already recorded
// as injected at session-start. stripReinjectionMarkdown removes the sentinel and its contiguous
// Topic/bullet block. (The XML <engram-reinjection> form is exported but unused on the live path.)
const reinjectionMarkdownHeader = "# Engram Re-Injection"

// StripInjectedBlocks removes engram's own injected context from agent output BEFORE citation
// detection (rank-2 anti-poisoning). The stop hook (stop.js) extracts ONLY assistant-role text
// into agent_output_text, so injected context reaches this path only when the agent echoes or
// quotes an injected block verbatim in its own turn. When it does, DetectCitations/DetectViolations
// would match the memory against engram's OWN injection and falsely mark it cited (or violated) —
// inflating citation_count with self-citation rather than genuine usage, and corrupting the rank-1
// feedback signal (and the rank-5/6 reinforcement built on it). Stripping engram's own context
// first ensures only the agent's OWN references to a memory count.
//
// Two surfaces are stripped: XML wrappers (session-start / issues injection) and the markdown
// reinjection sentinel (pre-compact). If neither is present (the common case — the agent rarely
// re-emits injected context) the output is returned unchanged.
func StripInjectedBlocks(agentOutput string) string {
	if agentOutput == "" {
		return agentOutput
	}
	out := stripInjectedXMLBlocks(agentOutput)
	out = stripReinjectionMarkdown(out)
	return out
}

// stripInjectedXMLBlocks removes each engram-owned XML wrapper block by pairing an opening tag to
// the first FOLLOWING closing tag of the SAME name. Prose before an opening tag is preserved; an
// opening tag with no same-name close (truncated/unclosed echo) is kept literal so it cannot
// swallow subsequent genuine prose.
func stripInjectedXMLBlocks(s string) string {
	// Fast path: no engram wrapper opening substring present at all.
	if !strings.Contains(s, "<engram-") &&
		!strings.Contains(s, "<user-behavior-rules") &&
		!strings.Contains(s, "<open-issues") {
		return s
	}

	var buf strings.Builder
	buf.Grow(len(s))
	rem := s
	for {
		open := openInjectedTagRe.FindStringSubmatchIndex(rem)
		if open == nil {
			buf.WriteString(rem)
			break
		}
		name := rem[open[2]:open[3]]
		buf.WriteString(rem[:open[0]]) // prose before the opening tag is preserved
		after := rem[open[1]:]

		closeStart, closeEnd := findMatchingClose(after, name)
		if closeStart < 0 {
			// Unclosed/mismatched: keep the opening tag literal and continue past it, so a
			// truncated echo cannot make the matcher consume the genuine prose that follows.
			buf.WriteString(rem[open[0]:open[1]])
			rem = after
			continue
		}
		buf.WriteByte(' ') // collapse the whole injected block to a single space
		rem = after[closeEnd:]
	}
	return buf.String()
}

// findMatchingClose returns the [start,end) byte offsets within s of the first closing
// engram-wrapper tag whose name equals tagName (case-insensitive). A closing tag of a DIFFERENT
// engram wrapper is skipped, not treated as the boundary, so mismatched tags never bound a block.
// Returns (-1,-1) when no same-name close exists.
func findMatchingClose(s, tagName string) (int, int) {
	base := 0
	for {
		loc := closeInjectedTagRe.FindStringSubmatchIndex(s[base:])
		if loc == nil {
			return -1, -1
		}
		name := s[base+loc[2] : base+loc[3]]
		if strings.EqualFold(name, tagName) {
			return base + loc[0], base + loc[1]
		}
		base += loc[1] // skip this non-matching close and keep searching
	}
}

// stripReinjectionMarkdown removes the pre-compact reinjection sentinel block from s. It deletes a
// line equal to reinjectionMarkdownHeader and the contiguous run of blank, "Topic:", and "- "
// bullet lines that follow it (the exact shape pre-compact.js writes). The first line that is none
// of those ends the block, so genuine agent prose after a quoted block is preserved. Whitespace
// changes here are immaterial: the output only feeds DetectCitations, which normalises whitespace.
func stripReinjectionMarkdown(s string) string {
	if !strings.Contains(s, reinjectionMarkdownHeader) {
		return s
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0] // in-place filter: len(kept) <= read index, so aliasing is safe
	skipping := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == reinjectionMarkdownHeader {
			skipping = true
			continue
		}
		if skipping {
			if t == "" || strings.HasPrefix(t, "Topic:") || strings.HasPrefix(t, "- ") {
				continue // still inside the sentinel block
			}
			skipping = false // first non-block line ends the sentinel region
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
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
