package serverevents

import (
	"net/url"
	"strings"
)

// parseGRPCAddr extracts host:port from a server URL.
// Mirrors the logic in internal/handlers/engramcore/grpcpool.go.
func parseGRPCAddr(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "37777"
		}
	}
	return host + ":" + port, nil
}

// isHTTPS reports whether the URL uses the https scheme.
func isHTTPS(serverURL string) bool {
	return strings.HasPrefix(serverURL, "https")
}

// safeURL strips userinfo before the URL appears in log output.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return u.String()
}
