// Package main provides the MCP SSE server entry point for claude-mnemonic.
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/thebtf/claude-mnemonic-plus/internal/collections"
	"github.com/thebtf/claude-mnemonic-plus/internal/config"
	"github.com/thebtf/claude-mnemonic-plus/internal/db/gorm"
	"github.com/thebtf/claude-mnemonic-plus/internal/embedding"
	"github.com/thebtf/claude-mnemonic-plus/internal/mcp"
	"github.com/thebtf/claude-mnemonic-plus/internal/scoring"
	"github.com/thebtf/claude-mnemonic-plus/internal/search"
	"github.com/thebtf/claude-mnemonic-plus/internal/sessions"
	"github.com/thebtf/claude-mnemonic-plus/internal/vector"
	"github.com/thebtf/claude-mnemonic-plus/internal/vector/pgvector"
	"github.com/thebtf/claude-mnemonic-plus/internal/watcher"
	"github.com/thebtf/claude-mnemonic-plus/pkg/models"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Version is set at build time via ldflags.
var Version = "dev"

func main() {
	// Parse flags
	port := flag.Int("port", 37778, "HTTP port for MCP SSE")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	// Setup logging - MCP uses stdout for communication, so log to stderr
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true})

	listenPort := *port
	if envPort := os.Getenv("CLAUDE_MNEMONIC_MCP_SSE_PORT"); envPort != "" {
		if parsed, err := strconv.Atoi(envPort); err == nil && parsed > 0 {
			listenPort = parsed
		}
	}

	// Ensure data directory and settings exist
	if err := config.EnsureAll(); err != nil {
		log.Fatal().Err(err).Msg("Failed to ensure data directories")
	}

	// Load config
	cfg, err := config.Load()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load config, using defaults")
		cfg = config.Default()
	}

	collectionRegistry, err := collections.Load(config.GetCollectionConfigPath())
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load collection config, collections disabled")
		collectionRegistry = &collections.Registry{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info().Msg("Shutting down MCP SSE server")
		cancel()
	}()

	// Initialize database store (migrations run automatically)
	storeCfg := gorm.Config{
		DSN:      cfg.DatabaseDSN,
		MaxConns: cfg.DatabaseMaxConns,
	}
	store, err := gorm.NewStore(storeCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize database store")
	}
	defer store.Close()

	// Initialize stores
	observationStore := gorm.NewObservationStore(store, nil, nil, nil)
	summaryStore := gorm.NewSummaryStore(store)
	promptStore := gorm.NewPromptStore(store, nil)
	patternStore := gorm.NewPatternStore(store)
	relationStore := gorm.NewRelationStore(store)
	sessionStore := gorm.NewSessionStore(store)

	// Initialize session indexer
	sessionIdxStore := sessions.NewStore(store)
	wsID := config.GetWorkstationID()
	if wsID == "" {
		wsID = sessions.WorkstationID()
	}
	sessionsDir := config.GetSessionsDir()
	sessionIndexer := sessions.NewIndexer(sessionIdxStore, sessionsDir, wsID, log.Logger)

	go func() {
		count, err := sessionIndexer.IndexAll(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("Session indexing failed")
		} else if count > 0 {
			log.Info().Int("indexed", count).Msg("Session indexing complete")
		}
	}()

	// Initialize embedding service and vector client
	var vectorClient vector.Client
	embedSvc, err := embedding.NewService()
	if err != nil {
		log.Warn().Err(err).Msg("Embedding service unavailable, vector search disabled")
	} else {
		defer embedSvc.Close()
		vectorClient, err = pgvector.NewClient(pgvector.Config{DB: store.DB, EmbedSvc: embedSvc})
		if err != nil {
			log.Warn().Err(err).Msg("Vector client unavailable, vector search disabled")
		} else {
			log.Info().Msg("Vector search enabled via pgvector")
		}
	}

	// Initialize scoring components
	scoreConfig := models.DefaultScoringConfig()
	scoreCalculator := scoring.NewCalculator(scoreConfig)
	recalculator := scoring.NewRecalculator(observationStore, scoreCalculator, log.Logger)
	go recalculator.Start(ctx)
	defer recalculator.Stop()

	// Initialize search manager
	searchMgr := search.NewManager(observationStore, summaryStore, promptStore, vectorClient)

	// Start file watchers
	startWatchers(ctx)

	// Create MCP server and SSE HTTP handler
	server := mcp.NewServer(
		searchMgr,
		Version,
		observationStore,
		patternStore,
		relationStore,
		sessionStore,
		vectorClient,
		scoreCalculator,
		recalculator,
		nil, // maintenanceService - handled by worker
		collectionRegistry,
		sessionIdxStore,
		nil, // consolidationScheduler - not available in standalone MCP mode
	)

	sseHandler := mcp.NewSSEHandler(server)
	var handler http.Handler = sseHandler
	token := config.GetWorkerToken()
	if token != "" {
		handler = tokenAuthMiddleware(token)(handler)
	}

	mux := http.NewServeMux()
	mux.Handle("/sse", handler)
	mux.Handle("/message", handler)

	addr := fmt.Sprintf(":%d", listenPort)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	httpErrCh := make(chan error, 1)
	go func() {
		httpErrCh <- httpServer.ListenAndServe()
	}()

	log.Info().
		Int("port", listenPort).
		Bool("tokenAuthEnabled", token != "").
		Msg("Starting MCP SSE server")

	select {
	case err := <-httpErrCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("MCP SSE server error")
		}
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("MCP SSE server shutdown failed")
		}

		sseHandler.Close()
	}
}

func tokenAuthMiddleware(token string) func(http.Handler) http.Handler {
	if token == "" {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			providedToken := r.Header.Get("X-Auth-Token")
			if providedToken == "" {
				if authHeader := r.Header.Get("Authorization"); len(authHeader) >= 7 && authHeader[:7] == "Bearer " {
					providedToken = authHeader[7:]
				}
			}

			if subtle.ConstantTimeCompare([]byte(providedToken), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// startWatchers initializes file watchers for config.
func startWatchers(ctx context.Context) {
	// Watch config file for changes (triggers process exit for restart)
	configPath := config.SettingsPath()
	configWatcher, err := watcher.New(configPath, func() {
		log.Warn().Str("path", configPath).Msg("Config file changed, exiting for restart...")
		time.Sleep(100 * time.Millisecond) // Give logs time to flush
		os.Exit(0)
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create config watcher")
	} else {
		if err := configWatcher.Start(); err != nil {
			log.Warn().Err(err).Msg("Failed to start config watcher")
		} else {
			log.Info().Str("path", configPath).Msg("Config file watcher started")
		}
	}
}
