// Command engram-import provides CLI utilities for importing feedback files
// and triggering server-side purge-rebuild operations.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thebtf/engram/internal/backfill"
	"github.com/thebtf/engram/internal/learning"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "import-feedback":
		runImportFeedback(os.Args[2:])
	case "purge-rebuild":
		runPurgeRebuild()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: engram-import <command> [flags]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  import-feedback   Scan ~/.claude/projects/**/memory/ for feedback_*.md")
	fmt.Println("                    files and extract structured rules via LLM.")
	fmt.Println("  purge-rebuild     Print instructions for the server-side purge-rebuild")
	fmt.Println("                    operation (POST /api/maintenance/purge-rebuild).")
}

func runImportFeedback(args []string) {
	fs := flag.NewFlagSet("import-feedback", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "Print extracted rules without storing them")
	_ = fs.Parse(args)

	dirs := findMemoryDirs()
	if len(dirs) == 0 {
		fmt.Println("No memory directories found under ~/.claude/projects/")
		os.Exit(0)
	}

	llmCfg := learning.DefaultOpenAIConfig()
	llm := learning.NewOpenAIClient(llmCfg)
	if !llm.IsConfigured() {
		fmt.Fprintln(os.Stderr, "error: LLM not configured (set ENGRAM_LLM_URL)")
		os.Exit(1)
	}

	items, result, err := backfill.ImportFeedbackFiles(context.Background(), dirs, llm)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Processed: %d  Imported: %d  Skipped: %d  Errors: %d\n",
		result.Processed, result.Imported, result.Skipped, result.Errors)

	if *dryRun {
		fmt.Println()
		fmt.Println("Extracted rules (dry-run — not stored):")
		for _, item := range items {
			fmt.Printf("  [%s] %s\n", filepath.Base(item.SourceFile), item.Observation.Title)
		}
		return
	}

	// Storing is deferred: the caller should POST extracted observations to the
	// engram server via /api/backfill or use the server-side endpoint directly.
	// This CLI focuses on extraction; storage requires an authenticated API call.
	if len(items) > 0 {
		fmt.Printf("\nExtracted %d rules. POST them to /api/backfill to store.\n", len(items))
		fmt.Println("(Use --dry-run to preview without storing.)")
	}
}

func runPurgeRebuild() {
	fmt.Println("purge-rebuild is executed via the engram server API.")
	fmt.Println()
	fmt.Println("Preview (dry run):")
	fmt.Println("  curl -X POST 'http://localhost:37777/api/maintenance/purge-rebuild?dry_run=true' \\")
	fmt.Println("       -H 'Authorization: Bearer <token>'")
	fmt.Println()
	fmt.Println("Execute:")
	fmt.Println("  curl -X POST 'http://localhost:37777/api/maintenance/purge-rebuild' \\")
	fmt.Println("       -H 'Authorization: Bearer <token>'")
	fmt.Println()
	fmt.Println("WARNING: This operation is DESTRUCTIVE.")
	fmt.Println("  - Truncates: vectors, patterns, observation_relations, session_summaries,")
	fmt.Println("    user_prompts, injection_log")
	fmt.Println("  - Deletes: all auto-extracted observations (source_type != 'manual' AND type != 'credential')")
	fmt.Println("  - Preserves: manual observations, credential observations")
	fmt.Println()
	fmt.Println("After purge, run backfill to rebuild from session history.")
}

// findMemoryDirs returns all ~/.claude/projects/<project>/memory directories that exist.
func findMemoryDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	projectsRoot := filepath.Join(home, ".claude", "projects")
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return nil
	}

	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		memDir := filepath.Join(projectsRoot, entry.Name(), "memory")
		if info, err := os.Stat(memDir); err == nil && info.IsDir() {
			dirs = append(dirs, memDir)
		}
	}
	return dirs
}
