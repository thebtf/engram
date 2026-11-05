package projectidentity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"os"
)

func projectRecoveryStatusError(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return logicalError("PROJECT_AUTH_REQUIRED")
	case http.StatusForbidden:
		return logicalError("PROJECT_ACCESS_DENIED")
	default:
		return logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
}

// projectRecoveryGET follows the installed client's TLS trust configuration and
// never forwards credentials through an HTTP redirect or an inherited proxy.
func projectRecoveryGET(ctx context.Context, endpoint *url.URL, token string) (*http.Response, func(), error) {
	return projectRecoveryRequest(ctx, endpoint, token, http.MethodGet, nil)
}

func projectRecoveryRequest(ctx context.Context, endpoint *url.URL, token, method string, body io.Reader) (*http.Response, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	requestURL := *endpoint
	var tlsConfig *tls.Config
	if caPath := os.Getenv("ENGRAM_TLS_CA"); caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
		}
		requestURL.Scheme = "https"
		tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	} else if requestURL.Scheme == "https" {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig}
	client := http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		transport.CloseIdleConnections()
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	return response, transport.CloseIdleConnections, nil
}
