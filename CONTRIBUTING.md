# Contributing to Engram

Engram is persistent shared memory infrastructure for Claude Code workstations. Contributions are welcome.

## How to Contribute

1. **Bugs and features** -- open an issue describing the problem or proposal.
2. **Questions** -- use GitHub Discussions.
3. **Code changes** -- fork the repo, create a branch, submit a PR.

## Development Setup

### Prerequisites

- Go 1.25+
- PostgreSQL 17 with pgvector extension
- Docker and Docker Compose (optional, for local PostgreSQL)
- CGO enabled (required for test build tags)

### Getting Started

```bash
# Clone your fork
git clone https://github.com/<your-user>/engram.git
cd engram

# Copy environment config
cp .env.example .env
# Edit .env -- configure DATABASE_DSN, embedding provider, etc.

# Start PostgreSQL (if using Docker)
docker compose up -d db

# Download dependencies
make deps

# Build all binaries (worker, MCP server, stdio proxy)
make build

# Run worker in foreground (development mode)
make dev
```

The worker listens on `http://localhost:37777` by default.

## Running Tests

```bash
# Run all tests with race detector
make test

# Run tests with coverage report
make test-coverage
# Opens coverage.html with per-function breakdown

# Run benchmarks
make bench
```

Tests require CGO (`CGO_ENABLED=1`). Some integration tests need a running PostgreSQL instance configured via `DATABASE_DSN`.

## Code Style

- **Formatting**: `gofmt` / `go fmt ./...`
- **Linting**: `make lint` (uses golangci-lint)
- **Router**: chi (`github.com/go-chi/chi/v5`)
- **Database**: GORM with PostgreSQL driver
- **Logging**: zerolog
- **Error handling**: wrap errors with context (`fmt.Errorf("doing X: %w", err)`)
- **File organization**: by domain -- `internal/search/`, `internal/scoring/`, `internal/embedding/`, etc.
- **File size**: keep files focused, ideally under 800 lines
- **Immutability**: prefer creating new objects over mutating existing ones

## Commit Messages

Follow conventional commit format:

```
<type>: <description>
```

Types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`

Examples:
```
feat: add bulk delete endpoint for observations
fix: handle nil pointer in reranker when no results
refactor: extract truncate utilities to pkg/strutil
test: add integration tests for search scoring
```

Keep descriptions concise. Add a body for non-obvious changes.

## Pull Request Process

1. Create a feature branch from `main`.
2. Make your changes. Include tests for new functionality.
3. Run `make test` and `make lint` -- both must pass.
4. Push your branch and open a PR against `main`.
5. Describe what the PR does and why. Include a test plan if applicable.
6. PRs are squash-merged.

## Project Architecture

```
cmd/
  worker/          -- HTTP API server (main entry point)
  mcp/             -- MCP server (stdio transport)
  mcp-stdio-proxy/ -- stdio-to-SSE proxy
  hooks/           -- Claude Code lifecycle hooks
internal/
  search/          -- search and retrieval logic
  scoring/         -- observation scoring and ranking
  embedding/       -- vector embedding (OpenAI-compatible REST API)
  reranking/       -- result reranking (API-based cross-encoder)
  mcp/             -- MCP protocol implementation and tool handlers
  worker/          -- HTTP handlers, middleware, server setup
  consolidation/   -- observation merging and maintenance
  selflearn/       -- self-learning signal detection
pkg/
  models/          -- shared data models
  hooks/           -- hook definitions and utilities
  strutil/         -- string utility functions
plugin/            -- Claude Code plugin definition and metadata
```

## Adding MCP Tools

1. Register the tool in `internal/mcp/server.go` inside the `handleToolsList` method. Define the tool name, description, and input schema.
2. Add a `case` for the tool name in the `callTool` dispatcher (same file).
3. Implement the handler. If it fits an existing domain, add it to the corresponding `tools_*.go` file (e.g., `tools_documents.go`). Otherwise, create a new `tools_<domain>.go` file.
4. Wire any required dependencies (database, search service, etc.) through the `Server` struct.
5. Add tests for the new tool handler.

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
