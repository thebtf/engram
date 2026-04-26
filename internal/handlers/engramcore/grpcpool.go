package engramcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// connKey identifies a pooled gRPC connection. The tokenHash axis (FR-7 /
// Plan ADR-005) prevents two sessions on the same workstation that present
// distinct keycards from sharing a single connection — without it, a request
// authenticated for keycard A could ride a connection that already cached
// keycard B's interceptor closure, silently mis-attributing audit logs and
// (after per-keycard scope rules) crossing scope boundaries.
//
// The tlsMode axis stays in the key so changing ENGRAM_TLS_CA mid-session
// does not silently reuse a stale connection against the new TLS policy.
//
// The tlsCAHash axis distinguishes two distinct custom-CA paths under the
// same tlsMode = "custom-ca". Without it, switching ENGRAM_TLS_CA from
// /etc/ca-A.pem to /etc/ca-B.pem leaves the connection still bound to the
// trust store loaded from ca-A. Hashed (rather than stored as plaintext)
// because pool keys end up in heap dumps; a stable short hash is enough to
// distinguish CA versions without leaking the path layout.
type connKey struct {
	addr       string
	tlsMode    string // "custom-ca", "system-tls", "plaintext"
	tlsCAHash  string // first 16 hex chars of sha256(ENGRAM_TLS_CA); empty for non-custom-ca
	tokenHash  string // first 16 hex chars of sha256(token); empty for empty token
}

// hashToken returns a stable short identifier for a credential. The full
// token is NEVER stored in the pool key — only an opaque hash, so memory
// dumps cannot recover credentials. Empty token → empty hash (the no-auth
// degenerate case is allowed for tests / ENGRAM_AUTH_DISABLED deployments).
func hashToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8]) // 8 bytes = 16 hex chars; collision-safe at this scale
}

// grpcPool is a lightweight pool keyed by (host:port, tls mode). Connections
// are created lazily on first use and shared across concurrent sessions. The
// sync.Map uses LoadOrStore so racing dials collapse into a single winner.
//
// Behaviour ported verbatim from engramHandler v4.2.0 — see git history of
// cmd/engram/main.go prior to this commit.
type grpcPool struct {
	conns sync.Map // connKey → *grpc.ClientConn
}

// getOrDialGRPC returns a pooled gRPC connection for the given server URL
// and token. The tls mode is derived from ENGRAM_TLS_CA / URL scheme.
func (p *grpcPool) getOrDialGRPC(serverURL, token string) (*grpc.ClientConn, error) {
	grpcAddr, err := parseGRPCAddr(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	tlsCA := os.Getenv("ENGRAM_TLS_CA")
	tlsMode := "plaintext"
	tlsCAHash := ""
	switch {
	case tlsCA != "":
		tlsMode = "custom-ca"
		tlsCAHash = hashToken(tlsCA) // reuse the same short-hash helper
	case strings.HasPrefix(serverURL, "https"):
		tlsMode = "system-tls"
	}

	key := connKey{
		addr:      grpcAddr,
		tlsMode:   tlsMode,
		tlsCAHash: tlsCAHash,
		tokenHash: hashToken(token),
	}
	if existing, ok := p.conns.Load(key); ok {
		return existing.(*grpc.ClientConn), nil
	}

	conn, err := dialGRPC(grpcAddr, serverURL, token)
	if err != nil {
		return nil, err
	}

	actual, loaded := p.conns.LoadOrStore(key, conn)
	if loaded {
		// Another goroutine created the connection first — close ours.
		conn.Close()
		return actual.(*grpc.ClientConn), nil
	}
	return conn, nil
}

// closeAll closes every pooled connection. Called by Module.Shutdown.
func (p *grpcPool) closeAll() {
	p.conns.Range(func(_, v any) bool {
		if c, ok := v.(*grpc.ClientConn); ok {
			_ = c.Close()
		}
		return true
	})
}

// parseGRPCAddr extracts host:port from a URL. Ported verbatim.
//
// Example: "http://unleashed.lan:37777" → "unleashed.lan:37777".
func parseGRPCAddr(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		default:
			port = "37777" // default engram gRPC port
		}
	}
	return host + ":" + port, nil
}

// dialGRPC creates a gRPC client connection with keepalive and TLS settings.
// Ported verbatim from engramHandler v4.2.0. Behaviour rules:
//
//  1. ENGRAM_TLS_CA set → TLS with custom CA file (overrides scheme).
//  2. https://         → TLS with system CA pool.
//  3. http:// / none    → plaintext (no TLS).
func dialGRPC(addr, serverURL, token string) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(16<<20), // 16 MB
			grpc.MaxCallSendMsgSize(16<<20),
		),
	}

	tlsCA := os.Getenv("ENGRAM_TLS_CA")

	switch {
	case tlsCA != "":
		creds, err := credentials.NewClientTLSFromFile(tlsCA, "")
		if err != nil {
			return nil, fmt.Errorf("load TLS CA: %w", err)
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
		fmt.Fprintf(os.Stderr, "[engram] gRPC: TLS with custom CA\n")

	case strings.HasPrefix(serverURL, "https"):
		creds := credentials.NewClientTLSFromCert(nil, "")
		opts = append(opts, grpc.WithTransportCredentials(creds))
		fmt.Fprintf(os.Stderr, "[engram] gRPC: TLS with system CA\n")

	default:
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		fmt.Fprintf(os.Stderr, "[engram] gRPC: plaintext\n")
	}

	if token != "" {
		opts = append(opts, grpc.WithUnaryInterceptor(tokenInterceptor(token)))
	}

	return grpc.NewClient(addr, opts...)
}

// tokenInterceptor injects the Bearer token into every outgoing RPC. Ported
// verbatim.
func tokenInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// safeRemoteURL strips any embedded userinfo before the URL is written to
// logs. Ported verbatim.
func safeRemoteURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// envOrDefault returns the value from the session env map if present,
// falling back to os.Getenv. Ported verbatim. Session env takes priority
// so per-project env (set via CC plugin hook) overrides host env.
func envOrDefault(env map[string]string, key string) string {
	if env != nil {
		if v, ok := env[key]; ok && v != "" {
			return v
		}
	}
	return os.Getenv(key)
}
