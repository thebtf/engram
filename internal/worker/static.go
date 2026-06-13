package worker

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

//go:embed static/*
var staticFS embed.FS

// staticSubFS holds the rooted subtree used for all asset lookups.
var staticSubFS fs.FS

// staticInitErr is non-nil when the embedded subtree could not be rooted at
// startup; every handler checks this before attempting reads.
var staticInitErr error

func init() {
	staticSubFS, staticInitErr = fs.Sub(staticFS, "static")
	if staticInitErr != nil {
		log.Warn().Err(staticInitErr).Msg("Static filesystem initialization failed - dashboard will be unavailable")
	}
}

// serveIndex writes the embedded index.html for the dashboard root.
func serveIndex(w http.ResponseWriter, r *http.Request) {
	if staticInitErr != nil {
		http.Error(w, "Dashboard unavailable: static files not initialized", http.StatusServiceUnavailable)
		return
	}
	content, err := fs.ReadFile(staticSubFS, "index.html")
	if err != nil {
		http.Error(w, "Dashboard not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_, _ = w.Write(content)
}

// serveAssets serves embedded JS and CSS assets by stripping the leading
// slash and reading from the rooted subtree.
func serveAssets(w http.ResponseWriter, r *http.Request) {
	if staticInitErr != nil {
		http.Error(w, "Assets unavailable: static files not initialized", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")

	content, err := fs.ReadFile(staticSubFS, path)
	if err != nil {
		http.Error(w, "Asset not found", http.StatusNotFound)
		return
	}

	switch {
	case strings.HasSuffix(path, ".js"):
		w.Header().Set("Content-Type", "application/javascript")
	case strings.HasSuffix(path, ".css"):
		w.Header().Set("Content-Type", "text/css")
	}

	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_, _ = w.Write(content)
}
