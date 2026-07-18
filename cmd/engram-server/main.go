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
	"github.com/thebtf/engram/internal/module/obs"
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
	telemetryCtx, telemetryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	telemetry, err := obs.Init(telemetryCtx, Version)
	telemetryCancel()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize observability")
	}
	obs.RecordRuntimeEvent(context.Background(), "startup", "started")

	// Create service with version and log buffer
	svc, err := worker.NewService(Version, logRing)
	if err != nil {
		obs.RecordRuntimeEvent(context.Background(), "worker", "initialization_error")
		flushTelemetry(telemetry)
		log.Fatal().Err(err).Msg("Failed to create service")
	}
	obs.RecordRuntimeEvent(context.Background(), "worker", "initialized")

	// Bring up listeners and background workers.
	if err := svc.Start(); err != nil {
		obs.RecordRuntimeEvent(context.Background(), "server", "initialization_error")
		flushTelemetry(telemetry)
		log.Fatal().Err(err).Msg("Failed to start service")
	}
	obs.RecordRuntimeEvent(context.Background(), "server", "started")

	// Block until the OS delivers SIGINT or SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("Received shutdown signal")

	// Allow in-flight requests up to 30 s before forcing teardown.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := svc.Shutdown(ctx); err != nil {
		obs.RecordRuntimeEvent(ctx, "worker", "shutdown_error")
		log.Error().Err(err).Msg("Shutdown error")
	} else {
		obs.RecordRuntimeEvent(ctx, "worker", "shutdown_complete")
	}

	telemetryCtx, telemetryCancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer telemetryCancel()
	if err := telemetry.Shutdown(telemetryCtx); err != nil {
		log.Error().Msg("Observability shutdown failed; check collector availability")
	}

	log.Info().Msg("Worker shutdown complete")
}

func flushTelemetry(telemetry *obs.Runtime) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := telemetry.ForceFlush(ctx); err != nil {
		log.Warn().Msg("Observability flush failed; check collector availability")
	}
}
