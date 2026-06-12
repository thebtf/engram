# Engram build system
#
# Primary targets:
#   make build       — build all binaries (dashboard + server + MCP client)
#   make install     — build, then install to ~/.claude/plugins/
#   make test        — run the full test suite
#   make lint        — run golangci-lint
#   make clean       — remove build artefacts

VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
# Pass version into both the server entry point and the internal version package.
LDFLAGS  := -ldflags "-X main.Version=$(VERSION) -X github.com/thebtf/engram/internal/version.Daemon=$(VERSION) -s -w" -buildvcs=false
BUILD_DIR := bin
PLUGIN_DIR := plugin

# Honour host Go environment; allow caller overrides.
GOOS   ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# CGO is required for the FTS5 search extension (go-sqlite3).
export CGO_ENABLED=1
BUILD_TAGS := -tags "fts5"

.PHONY: all build clean test install lint \
        worker engram stop-worker start-worker restart-worker \
        dashboard website dev-website \
        setup-libs proto rebaseline-v6 \
        build-all build-linux build-darwin build-windows \
        test-coverage bench fmt deps dev help

all: build

# ---------------------------------------------------------------------------
# Fixture rebaseline (FR-9 byte-identity baseline)
#
# Captures normalised payloads from a v6.3.0 binary into an isolated
# worktree so the current branch is never mutated. Run only when the
# acceptance-test baseline legitimately needs to move (see Clarify C3 and
# TD-008 in TECHNICAL_DEBT.md before rebaselining blindly).
#
# Prerequisites: PostgreSQL test stand, ENGRAM_AUTH_DISABLED=true, jq, curl,
#                scripts/capture-baseline.sh present.
# ---------------------------------------------------------------------------
rebaseline-v6:
	@echo "[rebaseline-v6] Materialising v6.3.0 in isolated worktree..."
	git worktree add --detach /tmp/engram-v6-rebaseline v6.3.0
	@echo "[rebaseline-v6] Building v6.3.0 binary..."
	cd /tmp/engram-v6-rebaseline && go build -o /tmp/engram-v6-bin ./cmd/engram-server
	@echo "[rebaseline-v6] Capturing normalised baseline payloads..."
	./scripts/capture-baseline.sh /tmp/engram-v6-bin > /tmp/baseline.tar
	@echo "[rebaseline-v6] Extracting fixtures..."
	tar -xf /tmp/baseline.tar -C internal/cognitive/core/testdata/v6_3_0_baseline/
	@echo "[rebaseline-v6] Cleaning up worktree..."
	git worktree remove /tmp/engram-v6-rebaseline
	@echo "[rebaseline-v6] Done. Commit with explicit ADR amendment + PR review per Clarify C3."

# Generate protobuf Go bindings
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/engram/v1/engram.proto

# ONNX runtime was removed in an earlier refactor; target kept for scripts
# that still call it so they do not fail with "no rule to make target".
setup-libs:
	@echo "ONNX runtime libraries are no longer required. Skipping."

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

# Build all three output binaries in dependency order.
build: dashboard worker engram

# Vue dashboard — embedded into the server binary via internal/worker/static/.
dashboard:
	@echo "Building Vue dashboard..."
	@sed 's/{{ .Version }}/$(VERSION)/g' ui/package.json.tpl > ui/package.json
	@cd ui && npm install --silent && npm run build
	@rm -rf internal/worker/static
	@mkdir -p internal/worker/static
	@touch internal/worker/static/placeholder.html
	@cp -r ui/dist/* internal/worker/static/

# Server binary — exposes HTTP REST + gRPC + embedded dashboard on :37777.
worker:
	@echo "Building worker..."
	@mkdir -p $(BUILD_DIR)
	swag init -g cmd/engram-server/main.go -o docs --parseDependency --parseInternal 2>/dev/null || true
	go build $(BUILD_TAGS) $(LDFLAGS) -o $(BUILD_DIR)/engram-server ./cmd/engram-server

# MCP stdio client — the binary Claude Code launches as a subprocess.
# CGO is disabled here because the client has no SQLite dependency.
engram:
	@echo "Building MCP client..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BUILD_DIR)/engram ./cmd/engram

# Cross-platform builds (CI / release verification)
build-all: build-linux build-darwin build-windows

build-linux:
	@echo "Building for Linux..."
	@mkdir -p $(BUILD_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 go build $(BUILD_TAGS) $(LDFLAGS) \
		-o $(BUILD_DIR)/linux-amd64/engram-server ./cmd/engram-server

build-darwin:
	@echo "Building for macOS..."
	@mkdir -p $(BUILD_DIR)/darwin-amd64 $(BUILD_DIR)/darwin-arm64
	GOOS=darwin GOARCH=amd64 go build $(BUILD_TAGS) $(LDFLAGS) \
		-o $(BUILD_DIR)/darwin-amd64/engram-server ./cmd/engram-server
	GOOS=darwin GOARCH=arm64 go build $(BUILD_TAGS) $(LDFLAGS) \
		-o $(BUILD_DIR)/darwin-arm64/engram-server ./cmd/engram-server

build-windows:
	@echo "Building for Windows..."
	@mkdir -p $(BUILD_DIR)/windows-amd64
	GOOS=windows GOARCH=amd64 go build $(BUILD_TAGS) $(LDFLAGS) \
		-o $(BUILD_DIR)/windows-amd64/engram-server.exe ./cmd/engram-server

# ---------------------------------------------------------------------------
# Worker lifecycle helpers (local development)
# ---------------------------------------------------------------------------

stop-worker:
	@echo "Stopping worker..."
	@-pkill -9 -f 'engram.*engram-server' 2>/dev/null || true
	@-pkill -9 -f '\.claude/plugins/.*/worker' 2>/dev/null || true
	@-lsof -ti :37777 | xargs kill -9 2>/dev/null || true
	@sleep 1

start-worker:
	@echo "Starting worker..."
	@# Prefer the versioned cache path (where Claude Code resolves the binary);
	@# fall back to the marketplaces directory for bare installs.
	@if [ -f "$(HOME)/.claude/plugins/cache/engram/engram/$(VERSION)/worker" ]; then \
		nohup $(HOME)/.claude/plugins/cache/engram/engram/$(VERSION)/engram-server \
			> /tmp/engram-server.log 2>&1 & \
	else \
		nohup $(HOME)/.claude/plugins/marketplaces/engram/engram-server \
			> /tmp/engram-server.log 2>&1 & \
	fi
	@sleep 1
	@if curl -s http://localhost:37777/health > /dev/null 2>&1; then \
		echo "Worker started (http://localhost:37777)"; \
	else \
		echo "Warning: worker may not have started — check /tmp/engram-server.log"; \
	fi

restart-worker: stop-worker start-worker

# ---------------------------------------------------------------------------
# Install / uninstall (local Claude Code plugin)
# ---------------------------------------------------------------------------

install: build stop-worker
	@echo "Installing to Claude plugins directory..."
	@mkdir -p $(HOME)/.claude/plugins/marketplaces/engram/hooks
	@mkdir -p $(HOME)/.claude/plugins/marketplaces/engram/.claude-plugin
	@mkdir -p $(HOME)/.claude/plugins/marketplaces/engram/commands
	cp $(BUILD_DIR)/engram-server $(HOME)/.claude/plugins/marketplaces/engram/
	cp $(PLUGIN_DIR)/engram/hooks/* $(HOME)/.claude/plugins/marketplaces/engram/hooks/
	@if [ -d "$(PLUGIN_DIR)/commands" ]; then \
		cp -r $(PLUGIN_DIR)/commands/* \
			$(HOME)/.claude/plugins/marketplaces/engram/commands/ 2>/dev/null || true; \
	fi
	cp $(PLUGIN_DIR)/.claude-plugin/plugin.json \
		$(HOME)/.claude/plugins/marketplaces/engram/.claude-plugin/plugin.json
	cp $(PLUGIN_DIR)/.claude-plugin/marketplace.json \
		$(HOME)/.claude/plugins/marketplaces/engram/.claude-plugin/marketplace.json
	@echo "Registering plugin with Claude Code..."
	@./scripts/register-plugin.sh "$(VERSION)"
	@$(MAKE) start-worker
	@echo "Installation complete!"

uninstall: stop-worker
	@echo "Uninstalling..."
	@./scripts/unregister-plugin.sh
	rm -rf $(HOME)/.claude/plugins/marketplaces/engram
	@echo "Uninstallation complete!"

# ---------------------------------------------------------------------------
# Test, lint, format
# ---------------------------------------------------------------------------

# FTS5 tag enables the SQLite full-text search extension used by memory search.
test:
	go test $(BUILD_TAGS) -v -race ./...

test-coverage:
	go test $(BUILD_TAGS) -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -1

bench:
	go test -bench=. -benchmem ./...

lint:
	golangci-lint run ./...

fmt:
	go fmt ./...

# ---------------------------------------------------------------------------
# Artefact cleanup
# ---------------------------------------------------------------------------

clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# ---------------------------------------------------------------------------
# Documentation website
# ---------------------------------------------------------------------------

website:
	@echo "Building website..."
	@cd docs && npm install --silent && npm run build
	@echo "Website built to docs/dist/"

dev-website:
	@echo "Starting website dev server..."
	@cd docs && npm install --silent && npm run dev

# ---------------------------------------------------------------------------
# Development mode (server in foreground with live logs)
# ---------------------------------------------------------------------------

dev: worker
	./$(BUILD_DIR)/engram-server

deps:
	go mod download
	go mod tidy

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

help:
	@echo "Engram Build System"
	@echo ""
	@echo "Usage:"
	@echo "  make build          Build all binaries"
	@echo "  make worker         Build server only"
	@echo "  make build-all      Build for all platforms"
	@echo "  make install        Install to Claude plugins directory (restarts worker)"
	@echo "  make uninstall      Remove from Claude plugins directory"
	@echo "  make stop-worker    Stop the running worker"
	@echo "  make start-worker   Start the worker in background"
	@echo "  make restart-worker Restart the worker"
	@echo "  make test           Run tests"
	@echo "  make bench          Run benchmarks"
	@echo "  make lint           Run linter"
	@echo "  make fmt            Format code"
	@echo "  make clean          Remove build artefacts"
	@echo "  make dev            Run worker in foreground (development)"
	@echo "  make deps           Download Go module dependencies"
	@echo "  make website        Build documentation website"
	@echo "  make dev-website    Run documentation website dev server"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION=$(VERSION)"
	@echo "  GOOS=$(GOOS)"
	@echo "  GOARCH=$(GOARCH)"
