// Package backfill provides the top-level orchestrator for historical session backfill.
// It coordinates session parsing, chunking, LLM extraction, validation, and deduplication.
package backfill

import (
	"context"
	"fmt"
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
	// Project is the project directory from the session metadata.
	Project string
	// Outcome is the LLM-classified outcome (active_pattern, failed_experiment, superseded).
	Outcome string
	// RawXML is the raw XML returned by the LLM for this observation's chunk.
	RawXML string
	// Observation is the converted ParsedObservation ready for storage.
	Observation *models.ParsedObservation
}

// Result holds the output of a complete backfill run.
type Result struct {
	// Observations contains all extracted (unique, validated) observations across all sessions.
	Observations []ExtractedObservation
	// Summary is the session-level retrospective, if the retrospective pass ran.
	Summary *extract.SessionRetrospective
	// Metrics contains quality and progress statistics.
	Metrics *metrics.Metrics
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

// Run processes a list of .jsonl session files sequentially.
// It returns a Result containing all extracted observations and accumulated metrics.
// Errors from individual sessions are logged but do not abort the run.
func (r *Runner) Run(ctx context.Context, files []string) (*Result, error) {
	m := &metrics.Metrics{}
	var observations []ExtractedObservation

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return &Result{Observations: observations, Metrics: m}, fmt.Errorf("context cancelled: %w", err)
		}

		extracted := r.processFile(ctx, file, m)
		observations = append(observations, extracted...)
	}

	return &Result{Observations: observations, Metrics: m}, nil
}

// ProcessSession processes a pre-parsed session and returns extracted observations.
// This is the server-side entry point — the caller provides a parsed SessionMeta
// (e.g. from ParseSessionReader) instead of a file path.
func (r *Runner) ProcessSession(ctx context.Context, sess *sessions.SessionMeta) (*Result, error) {
	m := &metrics.Metrics{}
	observations, summary := r.processSession(ctx, sess, "", m)
	return &Result{Observations: observations, Summary: summary, Metrics: m}, nil
}

// processFile processes a single session file and returns extracted observations.
func (r *Runner) processFile(ctx context.Context, file string, m *metrics.Metrics) []ExtractedObservation {
	m.Add("total_sessions", 1)

	sess, err := sessions.ParseSession(file)
	if err != nil {
		return nil
	}

	observations, _ := r.processSession(ctx, sess, file, m)
	return observations
}

// processSession is the shared extraction logic for both file-based and content-based processing.
// Returns extracted observations and an optional session-level retrospective.
func (r *Runner) processSession(ctx context.Context, sess *sessions.SessionMeta, sourceFile string, m *metrics.Metrics) ([]ExtractedObservation, *extract.SessionRetrospective) {
	m.Add("total_sessions", 1)

	durationMin := 0
	if !sess.FirstMsgAt.IsZero() && !sess.LastMsgAt.IsZero() {
		durationMin = int(sess.LastMsgAt.Sub(sess.FirstMsgAt).Minutes())
	}

	// Skip tiny sessions.
	if sess.ExchangeCount < 3 && durationMin < 5 {
		m.Add("skipped_tiny", 1)
		return nil, nil
	}

	m.Add("processed", 1)

	chunks := chunk.Exchanges(sess.Exchanges, r.cfg.MaxChunkChars, r.cfg.OverlapExchanges)

	if r.cfg.DryRun {
		m.Add("total_chunks", len(chunks))
		return nil, nil
	}

	seenTitles := make(map[string]bool)
	var extractedTitles []string
	var observations []ExtractedObservation

	for ci, ch := range chunks {
		if err := ctx.Err(); err != nil {
			break
		}

		m.Add("total_chunks", 1)

		chunkInfo := fmt.Sprintf("chunk %d of %d (exchanges %d-%d)",
			ci+1, len(chunks), ch.StartExchange, ch.EndExchange)
		prompt := extract.BuildChunkUserPrompt(
			sess.ProjectPath, sess.ExchangeCount,
			chunkInfo, ch.Text,
		)

		start := time.Now()
		xmlOutput, llmErr := r.llm.Complete(ctx, extract.ChunkExtractionSystemPrompt, prompt)
		m.RecordDuration(time.Since(start))

		if llmErr != nil {
			m.Add("llm_errors", 1)
			continue
		}

		vr := extract.ValidateXML(xmlOutput)

		if vr.IsMalformedXML {
			m.Add("malformed_xml", 1)
			continue
		}
		if vr.IsNoObservations {
			m.Add("no_obs_responses", 1)
			continue
		}

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
			observations = append(observations, ExtractedObservation{
				SessionFile: sourceFile,
				Project:     sess.ProjectPath,
				Outcome:     xo.Outcome,
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

	// Retrospective pass: run a session-level synthesis after all chunks are processed.
	retro := r.runRetrospective(ctx, sess, durationMin, extractedTitles, observations)

	// Append any session-level observations from the retrospective to the main list.
	if retro != nil {
		for _, xo := range retro.SessionObservations {
			normTitle := strings.ToLower(strings.TrimSpace(xo.Title))
			if seenTitles[normTitle] {
				continue
			}
			seenTitles[normTitle] = true
			obs := extract.ConvertToObservation(xo, sess.ProjectPath)
			observations = append(observations, ExtractedObservation{
				SessionFile: sourceFile,
				Project:     sess.ProjectPath,
				Outcome:     xo.Outcome,
				RawXML:      "",
				Observation: obs,
			})
		}
	}

	return observations, retro
}

// runRetrospective performs a session-level retrospective LLM call.
// Returns nil on error or if the context is cancelled — backfill continues regardless.
func (r *Runner) runRetrospective(ctx context.Context, sess *sessions.SessionMeta, durationMin int, extractedTitles []string, observations []ExtractedObservation) *extract.SessionRetrospective {
	// Build retrospective inputs.
	userMessages := 0
	for _, ex := range sess.Exchanges {
		if strings.TrimSpace(ex.UserText) != "" {
			userMessages++
		}
	}

	// Count commits (rough estimate — grep "git commit" in assistant text).
	commits := 0
	for _, ex := range sess.Exchanges {
		if strings.Contains(ex.AssistantText, "git commit") {
			commits++
		}
	}

	// Count unique file paths from extracted observations.
	seenFiles := make(map[string]bool)
	for _, eo := range observations {
		if eo.Observation != nil {
			for _, f := range eo.Observation.FilesRead {
				seenFiles[f] = true
			}
		}
	}
	filesModified := len(seenFiles)

	// Format already-extracted observations as text.
	var alreadyExtractedBuf strings.Builder
	for _, title := range extractedTitles {
		alreadyExtractedBuf.WriteString("- " + title + "\n")
	}

	// Build opening/closing exchange text (first 3 and last 3).
	sessionOpening := buildExchangeText(sess.Exchanges, 0, 3)
	closingStart := len(sess.Exchanges) - 3
	if closingStart < 3 {
		closingStart = 3
	}
	sessionClosing := buildExchangeText(sess.Exchanges, closingStart, len(sess.Exchanges))

	retroPrompt := fmt.Sprintf(extract.RetrospectiveUserTemplate,
		sess.ProjectPath,
		durationMin,
		sess.ExchangeCount,
		userMessages,
		commits,
		filesModified,
		alreadyExtractedBuf.String(),
		sessionOpening,
		sessionClosing,
	)

	// Use a 30-second timeout for the retrospective call.
	retroCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	xmlOutput, err := r.llm.Complete(retroCtx, extract.RetrospectiveSystemPrompt, retroPrompt)
	if err != nil {
		return nil
	}

	return extract.ParseRetrospective(xmlOutput)
}

// buildExchangeText formats a slice of exchanges as plain text for LLM consumption.
func buildExchangeText(exchanges []sessions.Exchange, start, end int) string {
	if start >= len(exchanges) {
		return ""
	}
	if end > len(exchanges) {
		end = len(exchanges)
	}
	var buf strings.Builder
	for i := start; i < end; i++ {
		ex := exchanges[i]
		if t := strings.TrimSpace(ex.UserText); t != "" {
			buf.WriteString("User: ")
			buf.WriteString(t)
			buf.WriteString("\n")
		}
		if t := strings.TrimSpace(ex.AssistantText); t != "" {
			buf.WriteString("Assistant: ")
			// Truncate very long assistant responses to keep prompt size reasonable.
			if len(t) > 500 {
				t = t[:500] + "..."
			}
			buf.WriteString(t)
			buf.WriteString("\n")
		}
	}
	return buf.String()
}
