package serverevents

import (
	"fmt"
	"net/url"
	"strings"
)

// parseGRPCAddr extracts host:port from a server URL.
// Mirrors the logic in internal/handlers/engramcore/grpcpool.go.
//
// Returns an explicit error if the URL has no host, so dial failures carry
// meaningful diagnostic information instead of surfacing as opaque
// "dial :37777" errors from a later call site.
func parseGRPCAddr(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("invalid server URL %q: missing host", safeURL(serverURL))
	}
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "37777"
		}
	}
	return host + ":" + port, nil
}

// isHTTPS reports whether the URL uses the https scheme.
// Parses the URL and performs a case-insensitive scheme check so that
// malformed inputs like "httpsfoo://bar" are not misclassified as HTTPS.
func isHTTPS(serverURL string) bool {
	u, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "https")
}

// safeURL strips userinfo before the URL appears in log output. On parse
// failure it returns a placeholder instead of the raw input so that any
// embedded credentials (user:pass@host) cannot leak through structured logs.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	u.User = nil
	return u.String()
}
