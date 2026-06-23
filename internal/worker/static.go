package worker

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// Nuxt/Vite may emit chunk files whose basenames start with "_".
// The plain directory-walk form excludes those files from embed.FS, so the
// server image must use all:static to keep the generated asset graph intact.
//
//go:embed all:static
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

const staleNuxtChunkReloadModule = `const key = "engram:operator-console:stale-chunk-reload";
const reloadParam = "engram_chunk_reload";
const now = Date.now();
let state = { count: 0, at: 0 };
let storageWorking = false;
try {
  sessionStorage.setItem(key + ":probe", "1");
  sessionStorage.removeItem(key + ":probe");
  storageWorking = true;
} catch {}
if (storageWorking) {
  try {
    state = JSON.parse(sessionStorage.getItem(key) || "null") || state;
  } catch {
    state = { count: 0, at: 0 };
  }
  if (!state.at || now - state.at > 30000) {
    state = { count: 0, at: now };
  }
  if (state.count < 1) {
    try {
      sessionStorage.setItem(key, JSON.stringify({ count: state.count + 1, at: now }));
    } catch {}
    window.location.reload();
  } else {
    console.error("Engram operator console stale Nuxt chunk is still missing after reload.");
  }
} else {
  const url = new URL(window.location.href);
  if (!url.searchParams.has(reloadParam)) {
    url.searchParams.set(reloadParam, "1");
    window.location.replace(url.toString());
  } else {
    console.error("Engram operator console stale Nuxt chunk is still missing after reload without storage.");
  }
}
export default {};
`

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
		proxy.ModifyResponse = setOperatorConsoleProxySecurityHeaders
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

func setOperatorConsoleProxySecurityHeaders(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return nil
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	setOperatorConsoleHTMLSecurityHeaders(resp.Header, content)
	resp.Body = io.NopCloser(bytes.NewReader(content))
	resp.ContentLength = int64(len(content))
	resp.Header.Set("Content-Length", strconv.Itoa(len(content)))
	resp.Header.Del("Content-Encoding")
	return nil
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

	setOperatorConsoleHTMLSecurityHeaders(w.Header(), content)
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
		if errors.Is(err, fs.ErrNotExist) {
			if isNuxtJSChunk(path) {
				serveStaleNuxtChunkReload(w)
				return
			}
			http.Error(w, "Asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Asset read failed", http.StatusInternalServerError)
		return
	}

	contentType := mime.TypeByExtension(pathpkg.Ext(path))
	if contentType == "" {
		switch pathpkg.Ext(path) {
		case ".json":
			contentType = "application/json"
		case ".js":
			contentType = "application/javascript"
		case ".css":
			contentType = "text/css"
		case ".svg":
			contentType = "image/svg+xml"
		case ".woff":
			contentType = "font/woff"
		case ".woff2":
			contentType = "font/woff2"
		}
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_, _ = w.Write(content)
}

func isNuxtJSChunk(path string) bool {
	return strings.HasPrefix(path, "_nuxt/") && pathpkg.Ext(path) == ".js"
}

func serveStaleNuxtChunkReload(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(staleNuxtChunkReloadModule))
}
