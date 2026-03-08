// Package main provides the engram CLI tool.
// Currently supports the "backfill" subcommand for processing historical session files.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebtf/engram/internal/backfill"
)

const version = "0.3.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "backfill":
		runBackfill(os.Args[2:])
	case "version":
		fmt.Printf("engram-cli %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: engram-cli <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  backfill    Process historical session files via server-side LLM extraction")
	fmt.Println("  version     Print version")
	fmt.Println("  help        Show this help")
}

// backfillSessionRequest mirrors the server's BackfillSessionRequest.
type backfillSessionRequest struct {
	SessionID string `json:"session_id"`
	Project   string `json:"project"`
	RunID     string `json:"run_id"`
	Content   string `json:"content"`
}

// backfillSessionResponse mirrors the server's BackfillSessionResponse.
type backfillSessionResponse struct {
	Stored                int    `json:"stored"`
	Skipped               int    `json:"skipped"`
	Errors                int    `json:"errors"`
	ObservationsExtracted int    `json:"observations_extracted"`
	MetricsReport         string `json:"metrics_report,omitempty"`
}

// sessionResult holds the result of processing a single session file.
type sessionResult struct {
	file      string
	resp      backfillSessionResponse
	err       error
	httpError bool
}

func runBackfill(args []string) {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	dirPtr := fs.String("dir", "", "Directory containing .jsonl session files")
	serverPtr := fs.String("server", "http://localhost:37777", "Engram server URL")
	dryRun := fs.Bool("dry-run", false, "List files that would be processed without sending to server")
	runID := fs.String("run-id", "", "Unique run ID for grouping observations (auto-generated if empty)")
	limitPtr := fs.Int("limit", 0, "Maximum number of sessions to process (0 = unlimited)")
	tokenPtr := fs.String("token", "", "API token for server authentication (or set ENGRAM_API_TOKEN)")
	resume := fs.Bool("resume", false, "Resume from last checkpoint")
	statePath := fs.String("state-file", backfill.DefaultProgressPath(), "Path to progress state file")
	concurrency := fs.Int("concurrency", 3, "Number of sessions to process in parallel")

	fs.Parse(args)

	if *dirPtr == "" {
		home, _ := os.UserHomeDir()
		*dirPtr = filepath.Join(home, ".claude", "projects")
		log.Printf("No --dir specified, defaulting to %s", *dirPtr)
	}

	if *runID == "" {
		*runID = fmt.Sprintf("run-%d", time.Now().Unix())
	}

	if *concurrency < 1 {
		*concurrency = 1
	}
	if *concurrency > 10 {
		*concurrency = 10
	}

	apiToken := *tokenPtr
	if apiToken == "" {
		apiToken = os.Getenv("ENGRAM_API_TOKEN")
	}

	// Load progress state for resumability
	progress, err := backfill.LoadProgress(*statePath)
	if err != nil {
		log.Fatalf("Failed to load progress: %v", err)
	}

	if *resume && progress.RunID != "" {
		*runID = progress.RunID
		log.Printf("Resuming run %s (%d files already processed)", *runID, len(progress.ProcessedFiles))
	} else {
		progress.RunID = *runID
		progress.StartedAt = time.Now()
	}

	// Find session files
	files, err := findSessionFiles(*dirPtr)
	if err != nil {
		log.Fatalf("Failed to find session files: %v", err)
	}
	if len(files) == 0 {
		log.Fatal("No .jsonl session files found")
	}

	// Filter already processed files when resuming
	if *resume {
		files = progress.FilterUnprocessed(files)
		log.Printf("After filtering processed files: %d remaining", len(files))
	}

	progress.TotalFiles = len(files) + len(progress.ProcessedFiles)

	if *limitPtr > 0 && len(files) > *limitPtr {
		files = files[:*limitPtr]
	}

	log.Printf("Found %d session files to process (run_id: %s, concurrency: %d)", len(files), *runID, *concurrency)

	if *dryRun {
		var totalSize int64
		for _, f := range files {
			info, _ := os.Stat(f)
			if info != nil {
				totalSize += info.Size()
			}
		}
		log.Printf("Dry run: %d files, total size %.1f MB", len(files), float64(totalSize)/(1024*1024))
		for _, f := range files {
			log.Printf("  %s", f)
		}
		return
	}

	// Handle graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	httpClient := &http.Client{Timeout: 10 * time.Minute}
	var totalStored, totalErrors, totalExtracted atomic.Int64

	// Channel for files to process and results to collect
	fileCh := make(chan indexedFile, *concurrency)
	resultCh := make(chan sessionResult, *concurrency)

	// Progress tracking must be serialized (single writer)
	var progressMu sync.Mutex

	// Launch worker goroutines
	var wg sync.WaitGroup
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range fileCh {
				result := processOneSession(ctx, httpClient, *serverPtr, apiToken, *runID, item)
				resultCh <- result
			}
		}()
	}

	// Launch result collector in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		for result := range resultCh {
			if result.err != nil {
				totalErrors.Add(1)
				log.Printf("    Error %s: %v", filepath.Base(result.file), result.err)
			} else if result.httpError {
				totalErrors.Add(1)
			} else {
				totalStored.Add(int64(result.resp.Stored))
				totalExtracted.Add(int64(result.resp.ObservationsExtracted))
				log.Printf("    %s: extracted=%d, stored=%d, skipped=%d, errors=%d",
					filepath.Base(result.file),
					result.resp.ObservationsExtracted, result.resp.Stored,
					result.resp.Skipped, result.resp.Errors)
			}

			// Update progress (serialized)
			progressMu.Lock()
			if result.err == nil && !result.httpError {
				progress.StoredCount += result.resp.Stored
				progress.SkippedCount += result.resp.Skipped
				progress.ErrorCount += result.resp.Errors
			} else {
				progress.ErrorCount++
			}
			progress.MarkProcessed(result.file)
			if saveErr := progress.Save(*statePath); saveErr != nil {
				log.Printf("    Warning: failed to save progress: %v", saveErr)
			}
			progressMu.Unlock()
		}
	}()

	// Feed files to workers
	for i, f := range files {
		if ctx.Err() != nil {
			log.Printf("Interrupted after %d/%d files", i, len(files))
			break
		}
		log.Printf("  [%d/%d] Queuing %s", i+1, len(files), filepath.Base(f))
		fileCh <- indexedFile{index: i, total: len(files), path: f}
	}
	close(fileCh)

	// Wait for all workers to finish
	wg.Wait()
	close(resultCh)
	<-done

	// Save final progress
	progressMu.Lock()
	if saveErr := progress.Save(*statePath); saveErr != nil {
		log.Printf("Warning: failed to save final progress: %v", saveErr)
	}
	progressMu.Unlock()

	log.Printf("\n=== Backfill Complete ===")
	log.Printf("Run ID:      %s", *runID)
	log.Printf("Sessions:    %d", len(files))
	log.Printf("Concurrency: %d", *concurrency)
	log.Printf("Extracted:   %d", totalExtracted.Load())
	log.Printf("Stored:      %d", totalStored.Load())
	log.Printf("Errors:      %d", totalErrors.Load())
	log.Printf("State file:  %s", *statePath)
}

type indexedFile struct {
	index int
	total int
	path  string
}

// processOneSession sends a single session file to the server for extraction.
func processOneSession(ctx context.Context, client *http.Client, server, token, runID string, item indexedFile) sessionResult {
	content, err := os.ReadFile(item.path)
	if err != nil {
		return sessionResult{file: item.path, err: fmt.Errorf("read file: %w", err)}
	}

	sessionID := filepath.Base(strings.TrimSuffix(item.path, ".jsonl"))

	reqBody := backfillSessionRequest{
		SessionID: sessionID,
		RunID:     runID,
		Content:   string(content),
	}

	body, _ := json.Marshal(reqBody)
	url := strings.TrimRight(server, "/") + "/api/backfill/session"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, httpErr := client.Do(req)
	if httpErr != nil {
		return sessionResult{file: item.path, err: httpErr}
	}

	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("    Server error for %s: %s %s", sessionID, resp.Status, string(respBody))
		return sessionResult{file: item.path, httpError: true}
	}

	var sessionResp backfillSessionResponse
	json.Unmarshal(respBody, &sessionResp)

	return sessionResult{file: item.path, resp: sessionResp}
}

// findSessionFiles recursively finds all .jsonl files in a directory.
func findSessionFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip inaccessible directories
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}
