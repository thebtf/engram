package gorm

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTombstoneLint_RemovedInVersionCommentsDoNotNameLiveTables(t *testing.T) {
	schema := migrationSchema(t)
	root := repositoryRoot(t)
	liveTables := schema.LiveTables()
	sort.Slice(liveTables, func(i, j int) bool {
		return len(liveTables[i].Name) > len(liveTables[j].Name)
	})

	tombstonePattern := regexp.MustCompile(`(?i)\b(?:removed|dropped)\s+in\s+v[0-9]+`)
	var violations []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() || filepath.Ext(path) != ".go" {
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
				// line or wrap onto the immediately following comment line — so
				// when the next line reads as a grammatical continuation (e.g.
				// "table dropped (migration 085)."), include it in the scan
				// window. The continuation line is NOT required to re-match the
				// tombstone phrase (that conjunction would be unsatisfiable and
				// made the window dead code; PR review HIGH-1).
				if !tombstonePattern.MatchString(commentText) {
					continue
				}
				candidateText := commentText
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

func violationMentions(violations []string, table string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, table) {
			return true
		}
	}
	return false
}

func lineString(line int) string {
	return strconv.Itoa(line)
}
