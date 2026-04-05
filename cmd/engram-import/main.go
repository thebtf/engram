// Command engram-import provides CLI utilities for importing feedback files
// and triggering server-side purge-rebuild operations.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	fmt.Println("  import-feedback   Send feedback_*.md files to engram server for LLM processing.")
	fmt.Println("  purge-rebuild     Print instructions for the server-side purge-rebuild operation.")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  ENGRAM_URL        Server URL (default: http://localhost:37777)")
	fmt.Println("  ENGRAM_API_TOKEN  Authentication token")
}

func runImportFeedback(args []string) {
	fs := flag.NewFlagSet("import-feedback", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "List files that would be imported without sending")
	server := fs.String("server", "", "Server URL (overrides ENGRAM_URL)")
	_ = fs.Parse(args)

	serverURL := *server
	if serverURL == "" {
		serverURL = os.Getenv("ENGRAM_URL")
	}
	if serverURL == "" {
		serverURL = "http://localhost:37777"
	}
	serverURL = strings.TrimRight(serverURL, "/")
	token := os.Getenv("ENGRAM_API_TOKEN")

	dirs := findMemoryDirs()
	if len(dirs) == 0 {
		fmt.Println("No memory directories found under ~/.claude/projects/")
		os.Exit(0)
	}

	var files []string
	for _, dir := range dirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "feedback_*.md"))
		files = append(files, matches...)
	}

	fmt.Printf("Found %d feedback files across %d project directories\n", len(files), len(dirs))

	if *dryRun {
		for _, f := range files {
			fmt.Printf("  %s\n", f)
		}
		return
	}

	if token == "" {
		fmt.Fprintln(os.Stderr, "warning: ENGRAM_API_TOKEN not set — requests may fail with 401")
	}

	imported, dupes, skipped, errors := 0, 0, 0, 0
	client := &http.Client{Timeout: 90 * time.Second}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			fmt.Printf("  ERROR read %s: %v\n", filepath.Base(file), err)
			errors++
			continue
		}
		if strings.TrimSpace(string(content)) == "" {
			skipped++
			continue
		}

		body, _ := json.Marshal(map[string]string{
			"content":     string(content),
			"source_file": filepath.Base(file),
		})

		req, _ := http.NewRequest("POST", serverURL+"/api/import/feedback", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("  ERROR %s: %v\n", filepath.Base(file), err)
			errors++
			continue
		}

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		status, _ := result["status"].(string)
		title, _ := result["title"].(string)

		switch status {
		case "imported":
			fmt.Printf("  IMPORTED %s → %s\n", filepath.Base(file), title)
			imported++
		case "duplicate":
			sim, _ := result["similarity"].(float64)
			fmt.Printf("  DUPLICATE %s (%.0f%% similar)\n", filepath.Base(file), sim*100)
			dupes++
		case "skipped":
			reason, _ := result["reason"].(string)
			fmt.Printf("  SKIPPED %s: %s\n", filepath.Base(file), reason)
			skipped++
		default:
			fmt.Printf("  ERROR %s: HTTP %d\n", filepath.Base(file), resp.StatusCode)
			errors++
		}
	}

	fmt.Printf("\nResults: %d imported, %d duplicates, %d skipped, %d errors\n",
		imported, dupes, skipped, errors)
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
	fmt.Println("  Preserves: manual observations, credential observations")
	fmt.Println("  Deletes: everything else (vectors, patterns, auto-extracted observations)")
}

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
