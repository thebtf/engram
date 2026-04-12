package mining

import (
	"strings"
)

// FilterCode removes fenced code blocks, shell prompt lines, and common
// programming definition lines (import, require, def, class, func/function),
// preserving prose paragraphs intact.
func FilterCode(text string) string {
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))

	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Toggle fenced code block state.
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		// Drop shell prompt lines.
		if strings.HasPrefix(trimmed, "$ ") || strings.HasPrefix(trimmed, "> ") {
			continue
		}

		// Drop programming definition lines.
		if isDefinitionLine(trimmed) {
			continue
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

// isDefinitionLine returns true for lines that are clearly code definitions
// rather than prose: import/require statements and class/func/function/def
// declarations.
func isDefinitionLine(line string) bool {
	lower := strings.ToLower(line)
	prefixes := []string{
		"import ",
		"import(",
		"require(",
		"from ",
		"def ",
		"class ",
		"func ",
		"function ",
		"public class ",
		"private class ",
		"protected class ",
		"export class ",
		"export function ",
		"export default function ",
		"export const ",
		"export let ",
		"export var ",
		"const ",
		"let ",
		"var ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}
