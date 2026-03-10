# Phase 0: Network & Auth Hardening

## Goal
Make the engram worker usable from remote workstations.
This is a Go project (single binary). Language: English only.

## Codebase Root
`D:\Dev\forks\engram`

## Files to Modify

### 1. internal/config/config.go
Add two new fields to Config struct and corresponding getter functions.

**Current Config struct ends at line 75:**
```go
type Config struct {
    ContextFullField          string   `json:"context_full_field"`
    DBPath                    string   `json:"db_path"`
    // ... 35 more fields ...
    WorkerPort                int      `json:"worker_port"`
    // ...
    CleanupStaleObservations  bool     `json:"cleanup_stale_observations"`
}
```

**Changes needed:**
1. Add `WorkerHost string` field — no json tag needed (not settable via JSON file, env-only)
2. Add `WorkerToken string` field — no json tag needed (security: never in JSON file, env-only)

In `Default()` function (line 141): set `WorkerHost: "127.0.0.1"`

After `GetWorkerPort()` function (line 304), add:
```go
// GetWorkerHost returns the worker bind host from environment or config.
// Defaults to 127.0.0.1 for local-only access.
func GetWorkerHost() string {
    if host := os.Getenv("ENGRAM_WORKER_HOST"); host != "" {
        return host
    }
    cfg := Get()
    if cfg.WorkerHost != "" {
        return cfg.WorkerHost
    }
    return "127.0.0.1"
}

// GetWorkerToken returns the API auth token from environment.
// Returns empty string if authentication is not configured.
func GetWorkerToken() string {
    return os.Getenv("ENGRAM_API_TOKEN")
}
```

### 2. pkg/hooks/worker.go
Replace 5 hardcoded `127.0.0.1` with `GetWorkerHost()` calls.

The file imports config package already. Exact replacements (line numbers may shift):
- Line 45: `fmt.Sprintf("http://127.0.0.1:%d/api/health", port)` → `fmt.Sprintf("http://%s:%d/api/health", config.GetWorkerHost(), port)`
- Line 132: `fmt.Sprintf("http://127.0.0.1:%d/api/version", port)` → `fmt.Sprintf("http://%s:%d/api/version", config.GetWorkerHost(), port)`
- Line 152: `fmt.Sprintf("127.0.0.1:%d", port)` → `fmt.Sprintf("%s:%d", config.GetWorkerHost(), port)`
- Line 239: `fmt.Sprintf("http://127.0.0.1:%d%s", port, path)` → `fmt.Sprintf("http://%s:%d%s", config.GetWorkerHost(), port, path)`
- Line 265: `fmt.Sprintf("http://127.0.0.1:%d%s", port, path)` → `fmt.Sprintf("http://%s:%d%s", config.GetWorkerHost(), port, path)`

### 3. internal/worker/service.go

**Service struct (line 103-154):** Add `tokenAuth *TokenAuth` field after `rateLimiter`:
```go
rateLimiter        *PerClientRateLimiter
tokenAuth          *TokenAuth
expensiveOpLimiter *ExpensiveOperationLimiter
```

**NewService() (line 357-373):** Initialize tokenAuth BEFORE calling setupMiddleware():
```go
// Initialize token auth (enabled when ENGRAM_API_TOKEN is set)
ta, err := NewTokenAuth(cfg.GetWorkerToken())
if err != nil {
    return nil, fmt.Errorf("init token auth: %w", err)
}

svc := &Service{
    // ... existing fields ...
    tokenAuth: ta,
}
```

**setupMiddleware() (line 1150-1178):** Wire TokenAuth after existing middleware:
```go
// Apply token authentication if configured
if s.tokenAuth != nil {
    s.router.Use(s.tokenAuth.Middleware)
}
```
Add this AFTER the rate limiter block (around line 1174), BEFORE the closing comment.

### 4. internal/worker/middleware.go — Modify NewTokenAuth

Current signature: `func NewTokenAuth(enabled bool) (*TokenAuth, error)`

Change to accept a fixed token:
```go
// NewTokenAuth creates a new TokenAuth.
// If token is non-empty, auth is enabled with that specific token.
// If token is empty, auth is disabled (local development mode).
func NewTokenAuth(token string) (*TokenAuth, error) {
    ta := &TokenAuth{
        enabled: token != "",
        token:   token,
        ExemptPaths: map[string]bool{
            "/health":     true,
            "/api/health": true,
            "/api/ready":  true,
        },
    }
    return ta, nil
}
```
This removes the `rand.Read()` call entirely — the old random token generation is removed.
Token is now always user-provided (via env) or auth is disabled.

### 5. Dockerfile (new file in repo root)

Multi-stage build. This binary requires CGO because sqlite-vec uses C extensions.

```dockerfile
# Stage 1: Build
FROM golang:1.24-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO required for sqlite-vec
ENV CGO_ENABLED=1
RUN go build -o /out/engram-worker ./cmd/worker

# Stage 2: Runtime (Debian slim keeps glibc for CGO)
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/engram-worker /usr/local/bin/engram-worker

# Data directory
VOLUME ["/data"]

ENV ENGRAM_DB_PATH=/data/engram.db
ENV ENGRAM_WORKER_HOST=0.0.0.0
ENV ENGRAM_WORKER_PORT=37777

EXPOSE 37777

ENTRYPOINT ["engram-worker"]
```

**Note on DB_PATH:** Need to check if config.go supports `ENGRAM_DB_PATH` env var override.
If not, add it to GetDBPath() or similar. Look at how DBPath is currently handled.

### 6. docker-compose.yml (new file in repo root)

```yaml
services:
  mnemonic:
    build: .
    ports:
      - "37777:37777"
    environment:
      ENGRAM_WORKER_HOST: "0.0.0.0"
      ENGRAM_WORKER_PORT: "37777"
      # Set API token for remote access security:
      # ENGRAM_API_TOKEN: "your-secret-token-here"
    volumes:
      - mnemonic-data:/data
    restart: unless-stopped

volumes:
  mnemonic-data:
```

Note: PostgreSQL service will be added in Phase 2 when the DB backend is migrated.

## Constraints

- MUST NOT break existing local development (if no env vars set, app runs exactly as before)
- MUST NOT change existing API endpoints
- MUST NOT require auth for `/health`, `/api/health`, `/api/ready` (these are already exempt in ExemptPaths)
- MUST check if config.go has `ENGRAM_DB_PATH` env override, add if missing
- TokenAuth disabled by default (no ENGRAM_API_TOKEN set = auth off)
- Go 1.21+ compatible

## Done When

- `go build ./...` succeeds
- All hardcoded `127.0.0.1` in pkg/hooks/worker.go replaced
- TokenAuth wired in setupMiddleware()
- Dockerfile builds successfully
- docker-compose.yml present
- Report all modified files and line-level summary of changes

## Language
All file content (code, comments) MUST be English. No exceptions.
