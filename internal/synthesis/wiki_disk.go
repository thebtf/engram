package synthesis

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// slugRE matches non-alphanumeric characters for slug generation.
var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// EntitySlug converts an entity name to a filesystem-safe slug.
func EntitySlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = slugRE.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if runes := []rune(slug); len(runes) > 60 {
		slug = string(runes[:60])
	}
	if slug == "" {
		slug = "unknown"
	}
	return slug
}

// escapeMarkdownCell escapes characters that would break a markdown table cell.
func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "[", `\[`)
	s = strings.ReplaceAll(s, "]", `\]`)
	return s
}

// WriteWikiToDisk writes a wiki page as markdown to {dataDir}/wiki/{slug}.md.
// Creates the wiki directory if it doesn't exist.
func WriteWikiToDisk(dataDir, entityName, entityType, content string, sourceCount int) error {
	if dataDir == "" {
		return fmt.Errorf("dataDir cannot be empty")
	}
	wikiDir := filepath.Join(dataDir, "wiki")
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		return fmt.Errorf("create wiki directory: %w", err)
	}

	slug := EntitySlug(entityName)
	filePath := filepath.Join(wikiDir, slug+".md")

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", entityName))
	sb.WriteString(fmt.Sprintf("**Type:** %s  \n", entityType))
	sb.WriteString(fmt.Sprintf("**Sources:** %d observations  \n", sourceCount))
	sb.WriteString(fmt.Sprintf("**Generated:** %s  \n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC")))
	sb.WriteString("---\n\n")
	sb.WriteString(content)
	sb.WriteString("\n")

	return os.WriteFile(filePath, []byte(sb.String()), 0644)
}

// UpdateWikiIndex regenerates the index.md file listing all wiki pages.
func UpdateWikiIndex(dataDir string, entries []WikiIndexEntry) error {
	if dataDir == "" {
		return fmt.Errorf("dataDir cannot be empty")
	}
	wikiDir := filepath.Join(dataDir, "wiki")
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		return fmt.Errorf("create wiki directory: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Engram Knowledge Wiki\n\n")
	sb.WriteString(fmt.Sprintf("**Generated:** %s  \n", time.Now().UTC().Format("2006-01-02 15:04 UTC")))
	sb.WriteString(fmt.Sprintf("**Entities:** %d  \n\n", len(entries)))
	sb.WriteString("| Entity | Type | Sources | Page |\n")
	sb.WriteString("|--------|------|---------|------|\n")

	for _, e := range entries {
		slug := EntitySlug(e.EntityName)
		sb.WriteString(fmt.Sprintf("| %s | %s | %d | [%s.md](%s.md) |\n",
			escapeMarkdownCell(e.EntityName), escapeMarkdownCell(e.EntityType), e.SourceCount, slug, slug))
	}

	sb.WriteString("\n")
	indexPath := filepath.Join(wikiDir, "index.md")
	return os.WriteFile(indexPath, []byte(sb.String()), 0644)
}
