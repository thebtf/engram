// Package main provides a PoC for historical session backfill.
// It reads JSONL session files, pre-filters and chunks them,
// sends chunks to an LLM for observation extraction, and validates results.
package main

import (
	"bufio"
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/backfill"
	"github.com/thebtf/engram/internal/learning"
)

func main() {
	manifestPtr := flag.String("manifest", "cmd/poc-backfill/testdata/session_manifest.txt", "Path to session manifest file")
	dirPtr := flag.String("dir", "", "Directory containing .jsonl files (overrides manifest)")
	modelPtr := flag.String("model", "", "LLM model override (default: from ENGRAM_LLM_MODEL)")
	dryRun := flag.Bool("dry-run", false, "Show what would be processed without calling LLM")
	flag.Parse()

	// Resolve session files
	var files []string
	if *dirPtr != "" {
		var err error
		files, err = filepath.Glob(filepath.Join(*dirPtr, "*.jsonl"))
		if err != nil {
			log.Fatal(err)
		}
	} else {
		var err error
		files, err = readManifest(*manifestPtr)
		if err != nil {
			log.Fatalf("Failed to read manifest: %v", err)
		}
	}

	if len(files) == 0 {
		log.Fatal("No session files found")
	}

	log.Printf("Found %d session files to process", len(files))

	// LLM client (5 min timeout for slow local models)
	llmCfg := learning.DefaultOpenAIConfig()
	llmCfg.Timeout = 5 * time.Minute
	if *modelPtr != "" {
		llmCfg.Model = *modelPtr
	}
	llmClient := learning.NewOpenAIClient(llmCfg)

	cfg := backfill.DefaultConfig()
	cfg.DryRun = *dryRun || !llmClient.IsConfigured()

	if cfg.DryRun && !*dryRun {
		log.Println("LLM not configured (set ENGRAM_LLM_URL + ENGRAM_LLM_API_KEY), running in dry-run mode")
	}

	runner := backfill.NewRunner(llmClient, cfg)
	sessionResults, m, err := runner.ProcessFiles(context.Background(), files)
	if err != nil {
		log.Fatalf("Backfill failed: %v", err)
	}

	// Print report
	report := m.Report()
	log.Print(report)

	// Print extracted observations summary
	obsCount := 0
	for _, sr := range sessionResults {
		if sr.ParseError != nil {
			log.Printf("  Error: %v", sr.ParseError)
			continue
		}
		for _, obs := range sr.Observations {
			obsCount++
			log.Printf("  [%d] %s — %s", obsCount, obs.Observation.Title, obs.ProjectPath)
		}
	}
}

func readManifest(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var files []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, err := os.Stat(line); err != nil {
			log.Printf("Warning: manifest file not found: %s", line)
			continue
		}
		files = append(files, line)
	}
	return files, scanner.Err()
}
