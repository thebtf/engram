package instincts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultBaseDir returns the default base directory for instinct files:
// ~/.claude/homunculus/instincts/
func DefaultBaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "homunculus", "instincts"), nil
}

// ResolveDir validates and resolves a requested path against the
// allowed base directory. If requested is empty, returns the base directory.
// Returns an error if the resolved path escapes the base directory.
func ResolveDir(requested string) (string, error) {
	base, err := DefaultBaseDir()
	if err != nil {
		return "", err
	}

	if requested == "" {
		return base, nil
	}

	// Clean and resolve the requested path
	resolved := filepath.Clean(requested)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(base, resolved)
	}

	// Ensure resolved path is within the base directory
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve base: %w", err)
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	if absResolved != absBase && !strings.HasPrefix(absResolved, absBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside allowed base directory", requested)
	}

	return absResolved, nil
}
