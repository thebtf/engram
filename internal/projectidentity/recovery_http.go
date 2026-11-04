package projectidentity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/url"
	"os"
)

// projectRecoveryGET applies the installed client's gRPC trust policy to both
// recovery readers: custom CA requires verified TLS regardless of URL scheme.
// A fresh direct transport cannot inherit proxy or insecure DefaultTransport
// settings. The caller closes the response body before releasing idle sockets.
func projectRecoveryGET(ctx context.Context, endpoint *url.URL, token string) (*http.Response, func(), error) {
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, nil, logicalError("PROJECT_RESOLUTION_UNAVAILABLE")
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
