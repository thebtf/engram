package worker

import (
	"embed"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	pathpkg "path"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

//go:embed static/*
var staticFS embed.FS

// staticSubFS holds the rooted subtree used for all asset lookups.
var staticSubFS fs.FS

// staticInitErr is non-nil when the embedded subtree could not be rooted at
// startup; every handler checks this before attempting reads.
var staticInitErr error

var (
	operatorConsoleProxyOnce   sync.Once
	operatorConsoleProxy       *httputil.ReverseProxy
	operatorConsoleProxyErr    error
	operatorConsoleProxyTarget string
)

func init() {
	staticSubFS, staticInitErr = fs.Sub(staticFS, "static")
	if staticInitErr != nil {
		log.Warn().Err(staticInitErr).Msg("Static filesystem initialization failed - dashboard will be unavailable")
	}
}

func loadOperatorConsoleProxy() (*httputil.ReverseProxy, string, error) {
	operatorConsoleProxyOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("ENGRAM_OPERATOR_CONSOLE_URL"))
		if raw == "" {
			return
		}

		target, err := url.Parse(raw)
		if err != nil {
			operatorConsoleProxyErr = fmt.Errorf("parse ENGRAM_OPERATOR_CONSOLE_URL: %w", err)
			return
		}
		if target.Scheme == "" || target.Host == "" {
			operatorConsoleProxyErr = fmt.Errorf("ENGRAM_OPERATOR_CONSOLE_URL must include scheme and host, got %q", raw)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Operator Console upstream unavailable: "+err.Error(), http.StatusBadGateway)
		}

		operatorConsoleProxy = proxy
		operatorConsoleProxyTarget = raw
		log.Info().Str("upstream", raw).Msg("operator-console: worker root proxy enabled")
	})

	return operatorConsoleProxy, operatorConsoleProxyTarget, operatorConsoleProxyErr
}

func serveOperatorConsole(w http.ResponseWriter, r *http.Request) bool {
	proxy, target, err := loadOperatorConsoleProxy()
	if target == "" {
		return false
	}
	if err != nil {
		http.Error(w, "Operator Console proxy misconfigured: "+err.Error(), http.StatusServiceUnavailable)
		return true
	}

	proxy.ServeHTTP(w, r)
	return true
}

// serveIndex writes the embedded index.html for the dashboard root.
func serveIndex(w http.ResponseWriter, r *http.Request) {
	if serveOperatorConsole(w, r) {
		return
	}
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

func serveOperatorConsoleOnly(w http.ResponseWriter, r *http.Request) {
	if serveOperatorConsole(w, r) {
		return
	}
	http.NotFound(w, r)
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

	if contentType := mime.TypeByExtension(pathpkg.Ext(path)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_, _ = w.Write(content)
}
