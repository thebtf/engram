package instincts

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// InstinctFile represents a single instinct file sent over the wire.
type InstinctFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// ParseContent parses a single instinct from its raw markdown content.
func ParseContent(name, content string) (*Instinct, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, fmt.Errorf("missing YAML frontmatter in %s", name)
	}

	// Locate the closing delimiter after the opening "---\n".
	// Using "\n---\n" prevents a "---" inside a YAML value from being
	// mistaken for the closing marker.
	rest := content[4:] // skip opening "---\n"
	sep := "\n---\n"
	idx := strings.Index(rest, sep)
	if idx == -1 {
		return nil, fmt.Errorf("malformed YAML frontmatter in %s", name)
	}
	frontmatter := rest[:idx]
	body := rest[idx+len(sep):]

	var inst Instinct
	if err := yaml.Unmarshal([]byte(frontmatter), &inst); err != nil {
		return nil, fmt.Errorf("parse YAML frontmatter in %s: %w", name, err)
	}

	inst.Body = strings.TrimSpace(body)
	inst.FilePath = name

	return &inst, nil
}

// ImportFromContent imports instincts from file content sent over the wire.
// v5 (US3): ObservationStore removed; import is disabled until chunk 3 wires
// the MemoryStore replacement. Returns an error explaining the situation.
func ImportFromContent(ctx context.Context, files []InstinctFile) (*ImportResult, error) {
	_ = ctx
	_ = files
	return nil, fmt.Errorf("instinct import disabled in v5 (US3) — MemoryStore wiring pending in chunk 3")
}
