package worker

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticEmbedIncludesUnderscoreNuxtChunks(t *testing.T) {
	source, err := os.ReadFile("static.go")
	if err != nil {
		t.Fatalf("read static.go: %v", err)
	}
	if !bytes.Contains(source, []byte("//go:embed all:static\n")) &&
		!bytes.Contains(source, []byte("//go:embed all:static\r\n")) {
		t.Fatal("static.go must contain the exact //go:embed all:static directive")
	}

	diskMatches, err := filepath.Glob(filepath.Join("static", "_nuxt", "_*.js"))
	if err != nil {
		t.Fatalf("glob disk underscore Nuxt chunks: %v", err)
	}
	if len(diskMatches) == 0 {
		return
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
