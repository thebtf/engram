package codeindex

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dirSkipList is the set of directory names that are always ignored when
// walking a repository tree, regardless of .gitignore contents.
// This covers the most common generated/vendored directories without requiring
// an external gitignore-parsing library.
var dirSkipList = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	".agent":       {},
	".idea":        {},
	".vscode":      {},
	"__pycache__":  {},
	".mypy_cache":  {},
	".pytest_cache":{},
	"target":       {},  // Rust / Maven
	"out":          {},
	".next":        {},  // Next.js
	".nuxt":        {},
	"coverage":     {},
}

// binaryExtensions is the set of file extensions that are always skipped
// because they are binary artifacts rather than source text.
var binaryExtensions = map[string]struct{}{
	".exe": {}, ".dll": {}, ".so":  {}, ".dylib": {}, ".a":   {},
	".o":   {}, ".obj": {}, ".lib": {}, ".bin":   {}, ".elf": {},
	".png": {}, ".jpg": {}, ".jpeg":{}, ".gif":   {}, ".bmp": {},
	".ico": {}, ".svg": {}, ".webp":{}, ".tiff":  {},
	".mp3": {}, ".mp4": {}, ".wav": {}, ".ogg":   {}, ".avi": {},
	".mov": {}, ".mkv": {},
	".zip": {}, ".tar": {}, ".gz":  {}, ".bz2":   {}, ".xz":  {},
	".7z":  {}, ".rar": {},
	".pdf": {}, ".doc": {}, ".docx":{}, ".xls":   {}, ".xlsx":{},
	".ppt": {}, ".pptx":{},
	".wasm":{},
	".pyc": {}, ".pyo": {},
	".class":{},
}

// ignoreRules holds the parsed patterns from .gitignore or .engramignore files.
type ignoreRules struct {
	// each entry is (pattern, negate) — negate=true means "!pattern"
	rules []ignoreRule
	// root is the directory the file was read from (for anchored patterns)
	root string
}

type ignoreRule struct {
	pattern string
	negate  bool
	dirOnly bool // pattern ends with /
}

// parseIgnoreFile reads ignore patterns from a .gitignore-style file.
// Blank lines and lines starting with '#' are skipped.
// Patterns starting with '!' are negation rules.
// Patterns ending with '/' match directories only (recorded but not
// specially enforced during the WalkDir phase — we treat dir-only patterns
// as also matching file names to stay conservative).
func parseIgnoreFile(path string) ([]ignoreRule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rules []ignoreRule
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Strip inline comment (not strictly gitignore-compliant but harmless
		// for the common case) — actually gitignore does NOT strip inline
		// comments, so we only skip full-line comments.
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		negate := false
		if strings.HasPrefix(line, "!") {
			negate = true
			line = line[1:]
		}
		dirOnly := false
		if strings.HasSuffix(line, "/") {
			dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		rules = append(rules, ignoreRule{
			pattern: line,
			negate:  negate,
			dirOnly: dirOnly,
		})
	}
	return rules, scanner.Err()
}

// matchesIgnore reports whether relPath (repo-relative, forward-slash) is
// ignored by rules. Rules are evaluated in order; a later negation can
// un-ignore an earlier positive match (standard gitignore semantics).
func matchesIgnore(rules []ignoreRule, relPath string) bool {
	ignored := false
	base := filepath.Base(relPath)
	for _, r := range rules {
		pat := r.pattern
		var matched bool
		if strings.Contains(pat, "/") {
			// Anchored or slash-containing pattern: match against full relPath.
			matched, _ = filepath.Match(strings.TrimPrefix(pat, "/"), relPath)
			if !matched {
				// Also try matching with filepath.Match on the slash-path directly.
				matched, _ = filepath.Match(pat, relPath)
			}
		} else {
			// Simple pattern: match against the base name only.
			matched, _ = filepath.Match(pat, base)
		}
		if matched {
			if r.negate {
				ignored = false
			} else {
				ignored = true
			}
		}
	}
	return ignored
}

// BuildManifest walks root, chunks every non-ignored text file, and returns
// a deterministic (sorted) Manifest and the full Chunk slice.
//
// The Manifest contains only metadata (no Content); the Chunk slice carries
// Content. CR-003 can pass subsets of file paths to ChunkFile directly when
// it needs only delta chunks after a negotiate response.
//
// Ignore rules are loaded from root/.gitignore and root/.engramignore when
// those files exist. The built-in dirSkipList and binaryExtensions are always
// applied first.
func BuildManifest(root string, opts Options) (Manifest, []Chunk, error) {
	if opts.LinesPerBlock <= 0 {
		opts.LinesPerBlock = DefaultOptions().LinesPerBlock
	}
	if opts.MaxChunkBytes <= 0 {
		opts.MaxChunkBytes = DefaultOptions().MaxChunkBytes
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultOptions().MaxFileBytes
	}
	if opts.MinifiedAvgLineLen <= 0 {
		opts.MinifiedAvgLineLen = DefaultOptions().MinifiedAvgLineLen
	}
	if opts.MinifiedSingleLineBytes <= 0 {
		opts.MinifiedSingleLineBytes = DefaultOptions().MinifiedSingleLineBytes
	}

	// Load ignore rules from .gitignore and .engramignore at the root.
	var ignoreRuleList []ignoreRule
	for _, name := range []string{".gitignore", ".engramignore"} {
		p := filepath.Join(root, name)
		rules, err := parseIgnoreFile(p)
		if err == nil {
			ignoreRuleList = append(ignoreRuleList, rules...)
		}
		// If the file doesn't exist, parseIgnoreFile returns an error; we
		// silently ignore that — ignore files are optional.
	}

	var allChunks []Chunk
	// Collect file paths first for deterministic ordering.
	var filePaths []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip paths that can't be accessed.
			return nil
		}

		// Compute repo-relative forward-slash path.
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			if rel == "." {
				return nil
			}
			// Check built-in skip list for the directory base name.
			if _, skip := dirSkipList[d.Name()]; skip {
				return filepath.SkipDir
			}
			// Check ignore rules.
			if matchesIgnore(ignoreRuleList, rel) {
				return filepath.SkipDir
			}
			return nil
		}

		// It's a file.
		// Check binary extension.
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if _, skip := binaryExtensions[ext]; skip {
			return nil
		}

		// Check ignore rules for the file.
		if matchesIgnore(ignoreRuleList, rel) {
			return nil
		}

		// Check file size.
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if opts.MaxFileBytes > 0 && info.Size() > int64(opts.MaxFileBytes) {
			return nil
		}

		filePaths = append(filePaths, path)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// Sort file paths for deterministic output.
	sort.Strings(filePaths)

	for _, absPath := range filePaths {
		rel, _ := filepath.Rel(root, absPath)
		relSlash := filepath.ToSlash(rel)

		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			continue
		}

		// Binary content heuristic (NUL byte in first 8 KB).
		if isBinaryContent(content) {
			continue
		}

		chunks, chunkErr := ChunkFile(relSlash, content, opts)
		if chunkErr != nil || chunks == nil {
			// nil chunks = minified/skipped; not a hard error.
			continue
		}
		allChunks = append(allChunks, chunks...)
	}

	// allChunks is already sorted by (filePath, byteStart) because we
	// walked sorted filePaths and ChunkFile produces ordered chunks.
	manifest := BuildManifestFromChunks(allChunks)
	return manifest, allChunks, nil
}
