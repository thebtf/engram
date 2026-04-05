package backfill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/backfill/extract"
	"github.com/thebtf/engram/internal/learning"
)

// FeedbackImportResult holds summary statistics for a feedback import run.
type FeedbackImportResult struct {
	Processed int
	Imported  int
	Skipped   int
	Errors    int
}

// FeedbackImportItem pairs an extracted observation with its source file path.
type FeedbackImportItem struct {
	SourceFile  string
	Observation *extract.XMLObservation
}

// ImportFeedbackFiles scans dirs for feedback_*.md files, processes each through
// the LLM to extract a structured TRIGGER→RULE→REASON observation, and returns
// the extracted items for the caller to store.
//
// Files that are empty, unparseable, or trigger LLM errors are counted in
// FeedbackImportResult.Errors / FeedbackImportResult.Skipped and do not abort
// the run — processing continues with the remaining files.
func ImportFeedbackFiles(ctx context.Context, dirs []string, llm learning.LLMClient) ([]FeedbackImportItem, *FeedbackImportResult, error) {
	result := &FeedbackImportResult{}

	// Collect all feedback_*.md files across the supplied directories.
	var files []string
	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, "feedback_*.md"))
		if err != nil {
			log.Warn().Err(err).Str("dir", dir).Msg("feedback import: glob failed")
			continue
		}
		files = append(files, matches...)
	}

	log.Info().Int("files", len(files)).Msg("feedback import: found files")

	var items []FeedbackImportItem

	for _, file := range files {
		if ctx.Err() != nil {
			return items, result, ctx.Err()
		}

		result.Processed++

		content, err := os.ReadFile(file)
		if err != nil {
			log.Warn().Err(err).Str("file", file).Msg("feedback import: failed to read file")
			result.Errors++
			continue
		}

		text := strings.TrimSpace(string(content))
		if text == "" {
			result.Skipped++
			continue
		}

		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		response, err := llm.Complete(callCtx, extract.FeedbackImportSystemPrompt, text)
		cancel()

		if err != nil {
			log.Warn().Err(err).Str("file", file).Msg("feedback import: LLM call failed")
			result.Errors++
			continue
		}

		obs := extract.ParseSingleObservation(response)
		if obs == nil {
			log.Warn().Str("file", file).Msg("feedback import: failed to parse LLM response as observation")
			result.Errors++
			continue
		}

		result.Imported++
		log.Info().
			Str("file", filepath.Base(file)).
			Str("title", obs.Title).
			Msg("feedback import: extracted rule")

		items = append(items, FeedbackImportItem{
			SourceFile:  file,
			Observation: obs,
		})
	}

	return items, result, nil
}
