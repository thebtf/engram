// Package main provides a stdio proxy for MCP endpoints.
// Supports both SSE (legacy) and Streamable HTTP transports.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/proxy"
)

// maxProbeBodySize caps the probe response read so an untrusted server cannot
// exhaust proxy memory by returning an arbitrarily large body.
const maxProbeBodySize = 64 * 1024 // 64 KB

// safeRemoteURL strips any embedded userinfo (e.g. tokens in
// https://token@host/path) before the URL is written to logs.
func safeRemoteURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		// Not a parseable URL — return as-is rather than hiding it entirely.
		return raw
	}
	u.User = nil
	return u.String()
}

// httpClient is used for short-lived requests (probe ping, Streamable HTTP
// POSTs). A 30 s timeout prevents hanging on an unresponsive server.
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// sseClient is used for the long-lived SSE connection. It has no response
// timeout (which would kill the stream after 30 s) but its transport enforces
// a 15 s dial + TLS-handshake timeout so the initial connection still fails fast.
var sseClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

func main() {
	baseURL := flag.String("url", "", "Base URL for MCP endpoint (overrides ENGRAM_URL)")
	token := flag.String("token", "", "Authorization token (overrides ENGRAM_API_TOKEN)")
	flag.Parse()

	// T003: env var fallback
	serverURL := strings.TrimSpace(*baseURL)
	if serverURL == "" {
		serverURL = strings.TrimSpace(os.Getenv("ENGRAM_URL"))
	}
	if serverURL == "" {
		fmt.Fprintln(os.Stderr, "error: server URL required (--url flag or ENGRAM_URL env var)")
		os.Exit(1)
	}

	authToken := strings.TrimSpace(*token)
	if authToken == "" {
		authToken = strings.TrimSpace(os.Getenv("ENGRAM_API_TOKEN"))
	}

	// T004: resolve git project identity
	slug, remote, err := proxy.ResolveProjectSlug(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[engram] warning: git identity failed: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "[engram] project: %s\n", slug)
	if remote != "" {
		fmt.Fprintf(os.Stderr, "[engram] git remote: %s\n", safeRemoteURL(remote))
	}

	// T006: try Streamable HTTP first, fall back to SSE
	if tryStreamableHTTP(serverURL, authToken, slug) {
		runStreamableHTTP(serverURL, authToken, slug)
	} else {
		runSSE(serverURL, authToken, slug)
	}
}

// tryStreamableHTTP probes <url>/mcp with a JSON-RPC ping.
// Returns true if the server responds with HTTP 200 and a JSON body.
func tryStreamableHTTP(serverURL, authToken, projectSlug string) bool {
	mcpURL := strings.TrimRight(serverURL, "/") + "/mcp"

	ping := []byte(`{"jsonrpc":"2.0","id":0,"method":"ping"}`)
	req, err := http.NewRequest(http.MethodPost, mcpURL, bytes.NewReader(ping))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// T005: project header
	if projectSlug != "" {
		req.Header.Set("X-Engram-Project", projectSlug)
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodySize))
	if err != nil {
		return false
	}

	// Verify response is JSON
	var check json.RawMessage
	return json.Unmarshal(body, &check) == nil
}

// runStreamableHTTP reads JSON-RPC lines from stdin, POSTs each to <url>/mcp,
// and writes the response body to stdout.
func runStreamableHTTP(serverURL, authToken, projectSlug string) {
	mcpURL := strings.TrimRight(serverURL, "/") + "/mcp"
	fmt.Fprintf(os.Stderr, "[engram] transport: streamable-http\n")

	scanner := bufio.NewScanner(os.Stdin)
	// Increase buffer to 4 MB to accommodate large JSON-RPC messages.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		req, err := http.NewRequest(http.MethodPost, mcpURL, strings.NewReader(line))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[engram] request error: %v\n", err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		// T005: project header
		if projectSlug != "" {
			req.Header.Set("X-Engram-Project", projectSlug)
		}
		if authToken != "" {
			req.Header.Set("Authorization", "Bearer "+authToken)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[engram] request failed: %v\n", err)
			os.Exit(1)
		}

		_, _ = io.Copy(os.Stdout, resp.Body)
		_ = resp.Body.Close()
		// json.NewEncoder on the server side appends a newline after each
		// JSON document, so no additional separator is needed here.
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(0)
}

// runSSE connects to the SSE endpoint and bridges it to stdio.
func runSSE(serverURL, authToken, projectSlug string) {
	fmt.Fprintf(os.Stderr, "[engram] transport: sse\n")

	sseURL := strings.TrimRight(serverURL, "/") + "/sse"
	req, err := http.NewRequest(http.MethodGet, sseURL, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// T005: project header
	if projectSlug != "" {
		req.Header.Set("X-Engram-Project", projectSlug)
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := sseClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "unexpected SSE response status: %d\n", resp.StatusCode)
		os.Exit(1)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer to 4 MB to handle large SSE data payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	currentEvent := ""
	messageData := ""
	messageEndpoint := ""
	var stdinDone chan struct{}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			if currentEvent == "message" && messageData != "" {
				fmt.Fprintln(os.Stdout, messageData)
			}
			currentEvent = ""
			messageData = ""
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		switch currentEvent {
		case "endpoint":
			if messageEndpoint == "" {
				messageEndpoint = resolveMessageEndpoint(serverURL, data)
				if err := validateEndpoint(serverURL, messageEndpoint); err != nil {
					fmt.Fprintf(os.Stderr, "[engram] rejected SSE endpoint (SSRF guard): %v\n", err)
					os.Exit(1)
				}
				stdinDone = make(chan struct{})
				go forwardStdin(messageEndpoint, authToken, projectSlug, stdinDone)
			}
		case "message":
			if messageData == "" {
				messageData = data
			} else if data != "" {
				messageData += "\n" + data
			}
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	// Wait for the forwardStdin goroutine to finish so all in-flight POSTs
	// complete before the process exits.
	if stdinDone != nil {
		<-stdinDone
	}
	os.Exit(0)
}

// forwardStdin reads JSON-RPC lines from stdin and POSTs each to the SSE message endpoint.
// It signals done on the provided channel when it finishes (EOF or error) so the
// caller can drain any remaining SSE output before exiting instead of calling
// os.Exit from a goroutine.
func forwardStdin(endpoint, token, projectSlug string, done chan<- struct{}) {
	defer close(done)

	scanner := bufio.NewScanner(os.Stdin)
	// Increase buffer to 4 MB to accommodate large JSON-RPC messages.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(line))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[engram] forward error: %v\n", err)
			return
		}

		req.Header.Set("Content-Type", "application/json")
		// T005: project header
		if projectSlug != "" {
			req.Header.Set("X-Engram-Project", projectSlug)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := sseClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[engram] forward failed: %v\n", err)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// resolveMessageEndpoint converts a relative SSE endpoint path to an absolute URL.
func resolveMessageEndpoint(baseURL, path string) string {
	cleanBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	cleanPath := strings.TrimSpace(path)

	hasPath := false
	if idx := strings.Index(cleanBase, "://"); idx != -1 {
		rest := cleanBase[idx+3:]
		if slash := strings.Index(rest, "/"); slash != -1 && slash < len(rest)-1 {
			hasPath = true
		}
	}

	if hasPath && strings.HasPrefix(cleanPath, "/message") {
		cleanPath = strings.TrimPrefix(cleanPath, "/message")
	}

	if cleanPath == "" {
		return cleanBase
	}

	if cleanPath[0] == '?' {
		return cleanBase + cleanPath
	}

	if cleanPath[0] != '/' {
		return cleanBase + "/" + cleanPath
	}

	return cleanBase + cleanPath
}

// validateEndpoint guards against SSRF by rejecting SSE-supplied endpoints
// that do not share the same host (and optional port) as the configured server.
// Relative paths and paths with an empty host always pass because they inherit
// the base origin in resolveMessageEndpoint.
func validateEndpoint(baseURL, endpoint string) error {
	base, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid base URL: %w", err)
	}
	ep, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	// Only reject when the endpoint carries an explicit host that differs from
	// the base. An empty ep.Host means it is a relative reference — safe.
	if ep.Host != "" && ep.Host != base.Host {
		return fmt.Errorf("endpoint host %q differs from configured server %q", ep.Host, base.Host)
	}
	return nil
}
