package gorm

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// tombstonePattern matches a "removed/dropped in vN" tombstone phrase. Package-level
// so both the walker test and the scanner unit test share one definition.
var tombstonePattern = regexp.MustCompile(`(?i)\b(?:removed|dropped)\s+in\s+v[0-9]+`)

func TestTombstoneLint_RemovedInVersionCommentsDoNotNameLiveTables(t *testing.T) {
	schema := migrationSchema(t)
	root := repositoryRoot(t)
	liveTables := schema.LiveTables()
	sort.Slice(liveTables, func(i, j int) bool {
		return len(liveTables[i].Name) > len(liveTables[j].Name)
	})

	var violations []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		base := filepath.Base(path)
		// migrations.go is the migration ledger: a "dropped in vN" comment there
		// documents a real historical DDL operation for THAT migration (even when a
		// later migration re-creates the table), so it is accurate by construction,
		// not a stale tombstone. _test.go files (including this guardrail's own
		// illustrative examples) are not shipped behavioral contracts. Both are
		// excluded so the guard targets production code comments.
		if base == "migrations.go" || strings.HasSuffix(base, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		require.NoError(t, parseErr, "parse Go comments in %s", path)
		for _, group := range file.Comments {
			comments := group.List
			for i, comment := range comments {
				commentText := cleanComment(comment.Text)
				// A line is a tombstone candidate only if THIS line carries the
				// "removed/dropped in vN" phrase. The table name may sit on this
				// line OR wrap across a line boundary in EITHER direction, so the
				// scan window is [prev, current, next] within the comment group.
				// (PR #273 review, codex: a backward-split tombstone — table name on
				// the line BEFORE the phrase — was a false-green when only
				// [current,next] was scanned.) The previous line is included only
				// when it reads as a tombstone lead-in (ends mid-sentence / with the
				// table name), and the next line only when it reads as a grammatical
				// continuation — both kept narrow to avoid sweeping in unrelated
				// neighbouring comments.
				if !tombstonePattern.MatchString(commentText) {
					continue
				}
				candidateText := commentText
				if i > 0 {
					prevText := cleanComment(comments[i-1].Text)
					if tombstoneLeadIn(prevText) {
						candidateText = prevText + "\n" + candidateText
					}
				}
				if i+1 < len(comments) {
					nextText := cleanComment(comments[i+1].Text)
					if tombstoneContinuation(nextText) {
						candidateText += "\n" + nextText
					}
				}
				for _, table := range liveTables {
					if mentionsTable(candidateText, table.Name) {
						rel, relErr := filepath.Rel(root, path)
						require.NoError(t, relErr)
						// Key on file:table (no line number) so the baseline is
						// stable against unrelated line shifts in the same file.
						violations = append(violations, filepath.ToSlash(rel)+" -> "+table.Name)
					}
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	violations = uniqueSorted(violations)

	// Known-debt baseline (CR-0 decision D1, see .agent/specs/provenance-cleanup/
	// decisions.md). CR-5 (contract honesty) reworded every stale "removed/dropped
	// in vN" comment that named a still-LIVE table (content_chunks restored@108,
	// injection_log restored@106), so the baseline is now EMPTY: the guardrail is
	// pure regression protection — it FAILS on ANY new comment that claims a live
	// table is dead (a fresh lie). Re-populating this list would re-admit drift.
	// Literal pre-cleanup proof: evidence/cr0-red-proof.txt + git history.
	var baseline []string

	require.ElementsMatch(t, baseline, violations,
		"stale-tombstone drift changed vs known-debt baseline (now empty). A NEW entry means a comment "+
			"now claims a LIVE table is dead (a fresh lie) — reword it. Got %v", violations)
}

// TestTombstoneScanner_DetectsSplitTombstones locks the scan-window behavior so the
// empty baseline cannot become a false-green (PR #273 review, codex). It exercises
// the same window assembly the walker uses — prev line via tombstoneLeadIn, current
// line via tombstonePattern, next line via tombstoneContinuation — and asserts a
// live-table name is detected whether it sits on the previous, current, or next line.
func TestTombstoneScanner_DetectsSplitTombstones(t *testing.T) {
	const tbl = "content_chunks"
	// Each case is an ordered set of comment lines; index `phraseAt` is the line
	// carrying the tombstone phrase. wantDetected = the window must surface tbl.
	cases := []struct {
		name         string
		lines        []string
		phraseAt     int
		wantDetected bool
	}{
		{"same line", []string{"the content_chunks table was dropped in v5"}, 0, true},
		{"backward split (name on prev line)",
			[]string{"returned when content_chunks", "table has been dropped in v5"}, 1, true},
		{"forward split (name on next line)",
			[]string{"vector search dropped in v5", "(content_chunks table)."}, 0, true},
		{"unrelated prev line not swept in",
			[]string{"this validates auth tokens.", "the widget cache was dropped in v5"}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, tombstonePattern.MatchString(tc.lines[tc.phraseAt]),
				"test setup: phraseAt line must carry the tombstone phrase")
			candidate := tc.lines[tc.phraseAt]
			if tc.phraseAt > 0 && tombstoneLeadIn(tc.lines[tc.phraseAt-1]) {
				candidate = tc.lines[tc.phraseAt-1] + "\n" + candidate
			}
			if tc.phraseAt+1 < len(tc.lines) && tombstoneContinuation(tc.lines[tc.phraseAt+1]) {
				candidate += "\n" + tc.lines[tc.phraseAt+1]
			}
			require.Equal(t, tc.wantDetected, mentionsTable(candidate, tbl),
				"window=%q", candidate)
		})
	}
}

// uniqueSorted dedupes and sorts a string slice (a file:table pair can be
// produced twice when a 2-line continuation window re-scans the next comment).
func uniqueSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func cleanComment(text string) string {
	text = strings.TrimPrefix(text, "//")
	text = strings.TrimPrefix(text, "/*")
	text = strings.TrimSuffix(text, "*/")
	return strings.TrimSpace(text)
}

// tombstoneContinuation reports whether a comment line reads as the grammatical
// continuation of a tombstone sentence begun on the previous line (where the
// "removed/dropped in vN" phrase lives). It deliberately matches the shapes a
// wrapped tombstone takes — a leading "(", a leading "table ...", or a "has
// been ..." clause — so the table name on the continuation line is still scoped
// to the tombstone. It does NOT match arbitrary following comments (e.g. an
// unrelated FK-audit line), which keeps the false-positive surface small.
func tombstoneContinuation(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(lower, "(") ||
		strings.HasPrefix(lower, "table ") ||
		strings.HasPrefix(lower, "has been ") ||
		strings.Contains(lower, "table has been") ||
		strings.Contains(lower, "table dropped") ||
		strings.Contains(lower, "table was dropped")
}

// tombstoneLeadIn reports whether a comment line reads as the START of a tombstone
// sentence that completes on the NEXT line (which carries the "removed/dropped in
// vN" phrase) — the backward-split shape codex flagged in PR #273, e.g.
//
//	// ErrChunkStorageUnsupported is returned ... when content_chunks
//	// table has been dropped in v5.
//
// It matches only when the line ENDS by naming a table (no terminal punctuation,
// so the sentence clearly continues), keeping the previous-line window narrow so
// an unrelated preceding comment is not swept in. A line ending in '.', ':', or
// ')' is treated as self-contained and excluded.
func tombstoneLeadIn(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	switch t[len(t)-1] {
	case '.', ':', ')', '!', '?':
		return false
	}
	return true
}

func mentionsTable(commentText, table string) bool {
	if strings.Contains(table, "_") {
		pattern := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(table) + `\b`)
		return pattern.MatchString(commentText)
	}
	explicitPatterns := []*regexp.Regexp{
		regexp.MustCompile("(?i)`" + regexp.QuoteMeta(table) + "`"),
		regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(table) + `\s+table\b`),
	}
	for _, pattern := range explicitPatterns {
		if pattern.MatchString(commentText) {
			return true
		}
	}
	return false
}

