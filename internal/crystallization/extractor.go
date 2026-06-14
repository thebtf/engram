// Package crystallization provides session-end decision and pattern extraction.
package crystallization

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/llm"
)

// Extractor is the interface for extracting decisions from a session digest.
// LLMExtractor is the production implementation; tests use fakes.
type Extractor interface {
	Extract(ctx context.Context, digest string) ([]ExtractedDecision, error)
}

// llmDecisionItem is the JSON shape expected from the LLM response array.
// Fields are permissive (all optional from the model's perspective).
type llmDecisionItem struct {
	Text           string   `json:"text"`
	Lang           string   `json:"lang"`
	Confidence     float64  `json:"confidence"`
	Evidence       []string `json:"evidence"`
	ProposedTarget string   `json:"proposed_target"`
}

// extractorSystemPrompt is language-agnostic: the model must extract decisions in
// whatever language they appear in the digest. No translation to English is requested.
const extractorSystemPrompt = `You are a session-crystallization assistant.
Your task is to extract the key decisions, lessons, and behavioral patterns from the
session digest provided by the user.

Rules:
- Extract decisions in the ORIGINAL language they appear in — do NOT translate.
- Return a JSON array (and nothing else) where each element has these fields:
    "text"            — the decision or lesson, verbatim or lightly paraphrased, in its original language
    "lang"            — BCP-47 language code detected for that text (e.g. "en", "ru", "zh", "de", "ja")
    "confidence"      — float in [0,1] representing your confidence that this is a genuine decision or lesson
    "evidence"        — array of short supporting text snippets (may be empty)
    "proposed_target" — "rule" when the decision should become a behavioral rule, "none" otherwise
- Omit entries whose confidence is below 0.2.
- If no decisions are found, return an empty JSON array: []
- Output ONLY the JSON array. Do not include markdown fences, prose, or explanations.`

// LLMExtractor uses an llm.Completer to extract decisions from a digest.
type LLMExtractor struct {
	client llm.Completer
}

// NewLLMExtractor returns an LLMExtractor backed by the given Completer.
func NewLLMExtractor(client llm.Completer) *LLMExtractor {
	return &LLMExtractor{client: client}
}

// Extract sends the digest to the LLM and parses the returned JSON array into
// []ExtractedDecision. It is tolerant of LLM output that wraps the JSON in
// markdown fences or surrounding prose:
//  1. Strip ```json / ``` fences.
//  2. Find the first '[' and last ']' and unmarshal only that slice.
//
// On malformed JSON, Extract logs the error and returns an empty slice with a
// nil error (skip-on-malformed). This prevents a single bad LLM response from
// aborting an entire dream-cycle run.
func (e *LLMExtractor) Extract(ctx context.Context, digest string) ([]ExtractedDecision, error) {
	raw, err := e.client.Complete(ctx, extractorSystemPrompt, digest)
	if err != nil {
		return nil, err
	}

	items, parseErr := tolerantParseJSONArray(raw)
	if parseErr != nil {
		log.Warn().Err(parseErr).
			Str("raw_prefix", truncate(raw, 200)).
			Msg("llm_extractor: malformed JSON from LLM — skipping response")
		return []ExtractedDecision{}, nil
	}

	out := make([]ExtractedDecision, 0, len(items))
	for _, item := range items {
		target := item.ProposedTarget
		if target == "" {
			target = "rule" // default for session decisions
		}
		out = append(out, ExtractedDecision{
			Text:           item.Text,
			Lang:           item.Lang,
			Confidence:     item.Confidence,
			Evidence:       item.Evidence,
			ProposedTarget: target,
		})
	}
	return out, nil
}

// tolerantParseJSONArray parses an LLM response that may contain markdown fences or
// surrounding prose. It strips ```json...``` fences, then locates the first '[' and
// last ']' in the resulting string and unmarshals that slice.
func tolerantParseJSONArray(raw string) ([]llmDecisionItem, error) {
	// Step 1: strip markdown code fences.
	s := raw
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")
	s = strings.TrimSpace(s)

	// Step 2: find the outermost JSON array.
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start == -1 || end == -1 || end < start {
		// No array brackets found; attempt a plain unmarshal as a last resort.
		var items []llmDecisionItem
		if err := json.Unmarshal([]byte(s), &items); err != nil {
			// LLMs occasionally return a single object instead of a one-element
			// array. Try unmarshalling as a single item and wrap it in a slice.
			var single llmDecisionItem
			if errSingle := json.Unmarshal([]byte(s), &single); errSingle == nil {
				return []llmDecisionItem{single}, nil
			}
			return nil, err
		}
		return items, nil
	}

	slice := s[start : end+1]
	var items []llmDecisionItem
	if err := json.Unmarshal([]byte(slice), &items); err != nil {
		// Same single-object fallback for the bracketed case (e.g. stray content
		// after the closing bracket caused the slice to be mis-extracted).
		var single llmDecisionItem
		if errSingle := json.Unmarshal([]byte(slice), &single); errSingle == nil {
			return []llmDecisionItem{single}, nil
		}
		return nil, err
	}
	return items, nil
}

// truncate returns the first n bytes of s, appending "…" when truncation occurs.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
