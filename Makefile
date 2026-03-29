# Engram Makefile

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
# Pass version to both main package and hooks package
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -s -w" -buildvcs=false
BUILD_DIR := bin
PLUGIN_DIR := plugin

# Go settings
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# CGO settings (required for test build tags)
export CGO_ENABLED=1
BUILD_TAGS := -tags "fts5"

.PHONY: all build clean test install lint worker mcp stop-worker start-worker restart-worker dashboard website dev-website setup-libs

all: build

# Legacy target (ONNX runtime libraries no longer required)
setup-libs:
	@echo "ONNX runtime libraries are no longer required. Skipping."

# Build all binaries
build: dashboard worker mcp

# Build Vue dashboard
dashboard:
	@echo "Building Vue dashboard..."
	@sed 's/{{ .Version }}/$(VERSION)/g' ui/package.json.tpl > ui/package.json
	@cd ui && npm install --silent && npm run build
	@rm -rf internal/worker/static
	@mkdir -p internal/worker/static
	@touch internal/worker/static/placeholder.html
	@cp -r ui/dist/* internal/worker/static/

# Build worker service
worker:
	@echo "Building worker..."
	@mkdir -p $(BUILD_DIR)
	swag init -g cmd/worker/main.go -o docs --parseDependency --parseInternal 2>/dev/null || true
	go build $(BUILD_TAGS) $(LDFLAGS) -o $(BUILD_DIR)/engram-server ./cmd/worker

# Build MCP server
mcp:
	@echo "Building MCP server..."
	@mkdir -p $(BUILD_DIR)
	go build $(BUILD_TAGS) $(LDFLAGS) -o $(BUILD_DIR)/engram-mcp ./cmd/mcp

# Build for all platforms
build-all: build-linux build-darwin build-windows

build-linux:
	@echo "Building for Linux..."
	@mkdir -p $(BUILD_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/linux-amd64/engram-server ./cmd/worker
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/linux-amd64/engram-mcp ./cmd/mcp

build-darwin:
	@echo "Building for macOS..."
	@mkdir -p $(BUILD_DIR)/darwin-amd64 $(BUILD_DIR)/darwin-arm64
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/darwin-amd64/engram-server ./cmd/worker
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/darwin-arm64/engram-server ./cmd/worker
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/darwin-amd64/engram-mcp ./cmd/mcp
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/darwin-arm64/engram-mcp ./cmd/mcp

build-windows:
	@echo "Building for Windows..."
	@mkdir -p $(BUILD_DIR)/windows-amd64
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/windows-amd64/engram-server.exe ./cmd/worker
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/windows-amd64/engram-mcp.exe ./cmd/mcp

# Stop any running worker
stop-worker:
	@echo "Stopping worker..."
	@-pkill -9 -f 'engram.*engram-server' 2>/dev/null || true
	@-pkill -9 -f '\.claude/plugins/.*/worker' 2>/dev/null || true
	@-lsof -ti :37777 | xargs kill -9 2>/dev/null || true
	@sleep 1

# Start worker in background
start-worker:
	@echo "Starting worker..."
	@# Prefer cache directory (where Claude Code looks), fall back to marketplaces
	@if [ -f "$(HOME)/.claude/plugins/cache/engram/engram/$(VERSION)/worker" ]; then \
		nohup $(HOME)/.claude/plugins/cache/engram/engram/$(VERSION)/engram-server > /tmp/engram-server.log 2>&1 & \
	else \
		nohup $(HOME)/.claude/plugins/marketplaces/engram/engram-server > /tmp/engram-server.log 2>&1 & \
	fi
	@sleep 1
	@if curl -s http://localhost:37777/health > /dev/null 2>&1; then \
		echo "Worker started successfully (http://localhost:37777)"; \
	else \
		echo "Warning: Worker may not have started. Check /tmp/engram-server.log"; \
	fi

# Restart worker
restart-worker: stop-worker start-worker

# Install to Claude plugins directory
install: build stop-worker
	@echo "Installing to Claude plugins directory..."
	@# Install to marketplaces directory (for direct installs)
	@mkdir -p $(HOME)/.claude/plugins/marketplaces/engram/hooks
	@mkdir -p $(HOME)/.claude/plugins/marketplaces/engram/.claude-plugin
	@mkdir -p $(HOME)/.claude/plugins/marketplaces/engram/commands
	cp $(BUILD_DIR)/engram-server $(HOME)/.claude/plugins/marketplaces/engram/
	cp $(BUILD_DIR)/engram-mcp $(HOME)/.claude/plugins/marketplaces/engram/
	cp $(PLUGIN_DIR)/engram/hooks/* $(HOME)/.claude/plugins/marketplaces/engram/hooks/
	@# Copy slash commands if they exist
	@if [ -d "$(PLUGIN_DIR)/commands" ]; then cp -r $(PLUGIN_DIR)/commands/* $(HOME)/.claude/plugins/marketplaces/engram/commands/ 2>/dev/null || true; fi
	@# Copy static plugin metadata
	cp $(PLUGIN_DIR)/.claude-plugin/plugin.json $(HOME)/.claude/plugins/marketplaces/engram/.claude-plugin/plugin.json
	cp $(PLUGIN_DIR)/.claude-plugin/marketplace.json $(HOME)/.claude/plugins/marketplaces/engram/.claude-plugin/marketplace.json
	@echo "Registering plugin with Claude Code..."
	@./scripts/register-plugin.sh "$(VERSION)"
	@$(MAKE) start-worker
	@echo "Installation complete!"

# Uninstall
uninstall: stop-worker
	@echo "Uninstalling..."
	@./scripts/unregister-plugin.sh
	rm -rf $(HOME)/.claude/plugins/marketplaces/engram
	@echo "Uninstallation complete!"

# Run tests (with FTS5 support)
test:
	go test $(BUILD_TAGS) -v -race ./...

# Run tests with coverage (with FTS5 support)
test-coverage:
	go test $(BUILD_TAGS) -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -1

# Run benchmarks
bench:
	go test -bench=. -benchmem ./...

# Run linter
lint:
	golangci-lint run ./...

# Format code
fmt:
	go fmt ./...

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# Run worker in development mode
dev: worker
	./$(BUILD_DIR)/engram-server

# Download dependencies
deps:
	go mod download
	go mod tidy

# Build website for production
website:
	@echo "Building website..."
	@cd docs && npm install --silent && npm run build
	@echo "Website built to docs/dist/"

# Run website in development mode
dev-website:
	@echo "Starting website dev server..."
	@cd docs && npm install --silent && npm run dev

# Show help
help:
	@echo "Engram Build System"
	@echo ""
	@echo "Usage:"
	@echo "  make build          - Build all binaries"
	@echo "  make worker         - Build worker service only"
	@echo "  make mcp            - Build MCP server only"
	@echo "  make build-all      - Build for all platforms"
	@echo "  make install        - Install to Claude plugins directory (restarts worker)"
	@echo "  make uninstall      - Remove from Claude plugins directory"
	@echo "  make stop-worker    - Stop the running worker"
	@echo "  make start-worker   - Start the worker in background"
	@echo "  make restart-worker - Restart the worker"
	@echo "  make test           - Run tests"
	@echo "  make bench          - Run benchmarks"
	@echo "  make lint           - Run linter"
	@echo "  make fmt            - Format code"
	@echo "  make clean          - Clean build artifacts"
	@echo "  make dev            - Run worker in development mode"
	@echo "  make deps           - Download dependencies"
	@echo "  make website        - Build website for production"
	@echo "  make dev-website    - Run website dev server"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION=$(VERSION)"
	@echo "  GOOS=$(GOOS)"
	@echo "  GOARCH=$(GOARCH)"
