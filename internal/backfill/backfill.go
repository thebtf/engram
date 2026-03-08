// Package backfill provides the top-level orchestrator for historical session backfill.
// It coordinates session parsing, chunking, LLM extraction, validation, and deduplication.
package backfill

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/backfill/chunk"
	"github.com/thebtf/engram/internal/backfill/extract"
	"github.com/thebtf/engram/internal/backfill/metrics"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/internal/sessions"
	"github.com/thebtf/engram/pkg/models"
)

// Config holds configuration for a backfill run.
type Config struct {
	// Dir is the directory containing .jsonl session files (unused by Runner — caller resolves files).
	Dir string
	// Server is the target engram server URL for storing observations (unused by Runner — caller stores).
	Server string
	// Model is the LLM model override. Empty means use the client's default.
	Model string
	// DryRun skips actual LLM calls and only reports what would be processed.
	DryRun bool
	// MaxChunkChars is the maximum character count per chunk. 0 = use default.
	MaxChunkChars int
	// OverlapExchanges is the number of exchanges to overlap between chunks. 0 = use default.
	OverlapExchanges int
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxChunkChars:    chunk.DefaultMaxChunkChars,
		OverlapExchanges: chunk.DefaultOverlapExchanges,
	}
}

// ExtractedObservation pairs a parsed observation with its source session metadata.
type ExtractedObservation struct {
	// SessionFile is the path to the source .jsonl file.
	SessionFile string
	// ProjectPath is the project directory from the session metadata.
	ProjectPath string
	// RawXML is the raw XML returned by the LLM for this observation's chunk.
	RawXML string
	// Observation is the converted ParsedObservation ready for storage.
	Observation *models.ParsedObservation
}

// ChunkResult holds per-chunk output for the caller (used in dry-run and normal mode).
type ChunkResult struct {
	// ChunkIndex is 1-indexed chunk number.
	ChunkIndex int
	// TotalChunks is the total number of chunks for this session.
	TotalChunks int
	// StartExchange is the first exchange index (1-indexed) in this chunk.
	StartExchange int
	// EndExchange is the last exchange index in this chunk.
	EndExchange int
	// PromptChars is the number of characters in the built user prompt (dry-run info).
	PromptChars int
	// RawXML is the LLM response. Empty in dry-run mode.
	RawXML string
	// Validation is the result of XML validation. Zero value in dry-run mode.
	Validation extract.ValidationResult
	// LLMError is non-nil if the LLM call failed.
	LLMError error
	// ElapsedTime is the LLM call duration. Zero in dry-run mode.
	ElapsedTime time.Duration
}

// SessionResult holds per-session output.
type SessionResult struct {
	// File is the path to the .jsonl file.
	File string
	// Meta is the parsed session metadata.
	Meta *sessions.SessionMeta
	// DurationMin is the session duration in minutes.
	DurationMin int
	// Chunks contains per-chunk results.
	Chunks []ChunkResult
	// Observations contains all extracted and converted observations for this session.
	Observations []ExtractedObservation
	// Skipped is true when the session was too small to process.
	Skipped bool
	// ParseError is non-nil if the session could not be parsed.
	ParseError error
}

// Runner orchestrates the backfill pipeline for a list of session files.
type Runner struct {
	llm learning.LLMClient
	cfg Config
}

// NewRunner creates a new Runner with the given LLM client and config.
func NewRunner(llm learning.LLMClient, cfg Config) *Runner {
	if cfg.MaxChunkChars <= 0 {
		cfg.MaxChunkChars = chunk.DefaultMaxChunkChars
	}
	if cfg.OverlapExchanges < 0 {
		cfg.OverlapExchanges = chunk.DefaultOverlapExchanges
	}
	return &Runner{llm: llm, cfg: cfg}
}

// ProcessFiles processes a list of .jsonl session files sequentially.
// It returns all extracted observations and accumulated metrics.
// Errors from individual sessions are recorded in SessionResult.ParseError and do not abort the run.
func (r *Runner) ProcessFiles(ctx context.Context, files []string) ([]SessionResult, *metrics.Metrics, error) {
	m := &metrics.Metrics{}
	results := make([]SessionResult, 0, len(files))

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return results, m, fmt.Errorf("context cancelled: %w", err)
		}

		sr := r.processFile(ctx, file, m)
		results = append(results, sr)
	}

	return results, m, nil
}

// processFile processes a single session file and returns its result.
func (r *Runner) processFile(ctx context.Context, file string, m *metrics.Metrics) SessionResult {
	m.Add("total_sessions", 1)

	sr := SessionResult{File: file}

	sess, err := sessions.ParseSession(file)
	if err != nil {
		sr.ParseError = fmt.Errorf("parse session %s: %w", filepath.Base(file), err)
		return sr
	}
	sr.Meta = sess

	durationMin := 0
	if !sess.FirstMsgAt.IsZero() && !sess.LastMsgAt.IsZero() {
		durationMin = int(sess.LastMsgAt.Sub(sess.FirstMsgAt).Minutes())
	}
	sr.DurationMin = durationMin

	// Skip tiny sessions.
	if sess.ExchangeCount < 3 && durationMin < 5 {
		sr.Skipped = true
		m.Add("skipped_tiny", 1)
		return sr
	}

	m.Add("processed", 1)

	chunks := chunk.Exchanges(sess.Exchanges, r.cfg.MaxChunkChars, r.cfg.OverlapExchanges)

	seenTitles := make(map[string]bool)
	var extractedTitles []string

	for ci, ch := range chunks {
		if err := ctx.Err(); err != nil {
			break
		}

		m.Add("total_chunks", 1)

		chunkInfo := fmt.Sprintf("chunk %d of %d (exchanges %d-%d)",
			ci+1, len(chunks), ch.StartExchange, ch.EndExchange)
		alreadyExtracted := extract.BuildAlreadyExtracted(extractedTitles)
		prompt := extract.BuildUserPrompt(
			sess.ProjectPath, sess.GitBranch,
			durationMin, sess.ExchangeCount,
			chunkInfo, alreadyExtracted, ch.Text,
		)

		cr := ChunkResult{
			ChunkIndex:    ci + 1,
			TotalChunks:   len(chunks),
			StartExchange: ch.StartExchange,
			EndExchange:   ch.EndExchange,
			PromptChars:   len(prompt),
		}

		if r.cfg.DryRun {
			sr.Chunks = append(sr.Chunks, cr)
			continue
		}

		start := time.Now()
		xmlOutput, llmErr := r.llm.Complete(ctx, extract.SystemPrompt, prompt)
		cr.ElapsedTime = time.Since(start)
		m.RecordDuration(cr.ElapsedTime)

		if llmErr != nil {
			cr.LLMError = llmErr
			m.Add("llm_errors", 1)
			sr.Chunks = append(sr.Chunks, cr)
			continue
		}

		cr.RawXML = xmlOutput
		vr := extract.ValidateXML(xmlOutput)
		cr.Validation = vr

		if vr.IsMalformedXML {
			m.Add("malformed_xml", 1)
		} else if vr.IsNoObservations {
			m.Add("no_obs_responses", 1)
		} else {
			uniqueInChunk := 0
			dupInChunk := 0

			for _, xo := range vr.Observations {
				normTitle := strings.ToLower(strings.TrimSpace(xo.Title))
				if seenTitles[normTitle] {
					dupInChunk++
					continue
				}
				seenTitles[normTitle] = true
				uniqueInChunk++

				obs := extract.ConvertToObservation(xo, sess.ProjectPath)
				sr.Observations = append(sr.Observations, ExtractedObservation{
					SessionFile: file,
					ProjectPath: sess.ProjectPath,
					RawXML:      xmlOutput,
					Observation: obs,
				})
			}

			m.Add("total_obs", vr.ObservationCount)
			m.Add("valid_obs", vr.ValidCount)
			m.Add("unique_obs", uniqueInChunk)
			m.Add("dedup_skipped", dupInChunk)
			m.Add("validation_errs", len(vr.Errors))

			for _, xo := range vr.Observations {
				if t := strings.TrimSpace(xo.Title); t != "" {
					extractedTitles = append(extractedTitles, t)
				}
			}
		}

		sr.Chunks = append(sr.Chunks, cr)
	}

	return sr
}
