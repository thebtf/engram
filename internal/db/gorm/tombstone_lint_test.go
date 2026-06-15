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
						violations = append(violations, rel+":"+lineString(fset.Position(comment.Pos()).Line)+" names live table "+table.Name+" in tombstone comment")
					}
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	sort.Strings(violations)

	require.NotEmpty(t, violations, "RED guardrail defect: expected current tree to expose stale tombstone comments")
	require.True(t, violationMentions(violations, "content_chunks"), "expected stale content_chunks tombstone comment, got %v", violations)
	require.Empty(t, violations, "tombstone comments must not name live tables; found %v", violations)
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
