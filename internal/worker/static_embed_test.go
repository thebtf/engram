package worker

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticEmbedIncludesUnderscoreNuxtChunks(t *testing.T) {
	diskMatches, err := filepath.Glob(filepath.Join("static", "_nuxt", "_*.js"))
	if err != nil {
		t.Fatalf("glob disk underscore Nuxt chunks: %v", err)
	}
	if len(diskMatches) == 0 {
		t.Skip("no underscore-prefixed generated Nuxt chunks are present in the source checkout")
	}

	matches, err := fs.Glob(staticSubFS, "_nuxt/_*.js")
	if err != nil {
		t.Fatalf("glob underscore Nuxt chunks: %v", err)
	}
	embedded := map[string]bool{}
	for _, match := range matches {
		embedded[filepath.ToSlash(match)] = true
	}

	for _, diskMatch := range diskMatches {
		rel := filepath.ToSlash(strings.TrimPrefix(diskMatch, "static"+string(filepath.Separator)))
		if !embedded[rel] {
			t.Fatalf("underscore-prefixed Nuxt chunk %q was present on disk but not embedded", rel)
		}
	}
}
