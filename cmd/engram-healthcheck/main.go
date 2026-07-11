// engram-healthcheck is a shell-free Docker readiness probe. It succeeds only
// when the configured endpoint returns HTTP 200 and exactly {"status":"ready"}.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	probeTimeout    = 3 * time.Second
	maxResponseBody = 4096
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stderr))
}

func run(parent context.Context, args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: engram-healthcheck http://host:port/api/ready")
		return 2
	}
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	if err := checkReady(ctx, args[0]); err != nil {
		fmt.Fprintln(stderr, "readiness check failed:", err)
		return 1
	}
	return 0
}

func checkReady(ctx context.Context, endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("endpoint must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/api/ready" {
		return errors.New("endpoint must contain only the /api/ready path")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return errors.New("request construction failed")
	}
	request.Header.Set("Accept", "application/json")

	client := &http.Client{
		Timeout: probeTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirects are not readiness")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}

	limited := io.LimitReader(response.Body, maxResponseBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return errors.New("response read failed")
	}
	if len(body) > maxResponseBody {
		return errors.New("response exceeds size limit")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("response must be a JSON object")
	}
	if !decoder.More() {
		return errors.New("response status is missing")
	}
	key, err := decoder.Token()
	if err != nil || key != "status" {
		return errors.New("response must contain only status")
	}
	var status string
	if err := decoder.Decode(&status); err != nil {
		return errors.New("response status must be a string")
	}
	if decoder.More() {
		return errors.New("response must contain exactly one status field")
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("response object is incomplete")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing data")
	}
	if status != "ready" {
		return errors.New("response status is not ready")
	}
	return nil
}
