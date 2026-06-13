// Package main provides the entry point for the engram server process.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/thebtf/engram/docs"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/logbuf"
	"github.com/thebtf/engram/internal/worker"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var Version = "dev"

// @title Engram API
// @version 1.0.0
// @description Persistent shared memory infrastructure for AI agents. Stores memories + behavioral rules + credentials in PostgreSQL. REST API over HTTP; MCP tools are served via stdio client proxy only (server-side MCP HTTP transports removed in v5). Note: the host below is the default for local development; set ENGRAM_LISTEN_ADDR to change the listen address in production.
// @host localhost:37777
// @BasePath /
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-Auth-Token
func main() {
	// Sub-command dispatch: engram-server backfill-candidates [flags]
	if len(os.Args) > 1 && os.Args[1] == "backfill-candidates" {
		runBackfillCandidates(os.Args[2:])
		return
	}

	// Configure structured logging; attach a ring buffer so /api/logs can
	// tail recent log output without a separate log aggregator.
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	cfg := config.Get()
	bufSize := cfg.LogBufferSize
	if bufSize <= 0 {
		bufSize = logbuf.DefaultCapacity
	}
	logRing := logbuf.NewRingBuffer(bufSize)
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr}
	multi := zerolog.MultiLevelWriter(consoleWriter, logRing)
	log.Logger = log.Output(multi)

	log.Info().
		Str("version", Version).
		Msg("Starting engram server")

	// Create service with version and log buffer
	svc, err := worker.NewService(Version, logRing)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create service")
	}

	// Bring up listeners and background workers.
	if err := svc.Start(); err != nil {
		log.Fatal().Err(err).Msg("Failed to start service")
	}

	// Block until the OS delivers SIGINT or SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("Received shutdown signal")

	// Allow in-flight requests up to 30 s before forcing teardown.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := svc.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("Shutdown error")
	}

	log.Info().Msg("Worker shutdown complete")
}
