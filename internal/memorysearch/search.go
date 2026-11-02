// Package memorysearch shares read-only memory retrieval between REST and MCP.
package memorysearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

var ErrIncludeSuperseded = errors.New("include_superseded is not supported by hybrid retrieval; omit include_superseded")

// Dependencies contains only the existing retrieval engine and its providers.
// Callers must leave optional store interfaces nil when their concrete store is nil.
type Dependencies struct {
	Memories       retrieval.MemoryStoreInterface
	Embeddings     retrieval.EmbeddingStoreInterface
	Graph          retrieval.GraphStoreInterface
	Embedder       *embedding.Client
	Reranker       *reranking.Client
	LiteralMatches LiteralStore
}

// Request carries caller-derived visibility separately from retrieval options.
// Session/principal widening remains the responsibility of each public adapter.
type Request struct {
	Project           string
	Query             string
	Limit             int
	Options           retrieval.HybridOptions
	Caller            scope.KeycardContext
	Visibility        scope.MemoryVisibilityOptions
	Type              string
	Tags              []string
	ConfidenceMin     float64
	IncludeSuperseded bool
}

type Result struct {
	Matches      []retrieval.ScoredMemory
	Explanations []retrieval.RankingExplanation
}

// Search embeds, retrieves, and filters before the final visible limit. It never
// performs reconsolidation or any other memory mutation; MCP owns those effects.
func Search(ctx context.Context, deps Dependencies, req Request) (result Result, resultErr error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	defer func() {
		if err := ctx.Err(); err != nil {
			result = Result{}
			if resultErr == nil {
				resultErr = err
			}
		}
	}()

	if req.Limit <= 0 {
		req.Limit = 10
	}
	opts := req.Options
	if deps.Embedder != nil {
		embedCtx := ctx
		var cancel context.CancelFunc
		ftsAllowed := len(opts.TierFilter) == 0
		for _, tier := range opts.TierFilter {
			if tier == "tier1_fts" {
				ftsAllowed = true
				break
			}
		}
		if deadline, ok := ctx.Deadline(); ok && ftsAllowed {
			now := time.Now()
			remaining := deadline.Sub(now)
			if remaining <= 0 {
				return Result{}, context.DeadlineExceeded
			}
			embedCtx, cancel = context.WithDeadline(ctx, now.Add(remaining/2))
		}
		vectors, err := deps.Embedder.Embed(embedCtx, []string{req.Query})
		if cancel != nil {
			if err == nil {
				err = embedCtx.Err()
			}
			cancel()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		if err == nil && len(vectors) > 0 {
			opts.QueryVec = vectors[0]
		}
		if err != nil {
			log.Debug().Err(err).Msg("Memory search embedding unavailable, falling back to FTS-only")
		}
	}
	if req.IncludeSuperseded {
		return Result{}, ErrIncludeSuperseded
	}
	if deps.Reranker != nil {
		opts.Reranker = rerankAdapter{client: deps.Reranker}
	}
	filter := memoryFilter{caller: req.Caller, visibility: req.Visibility, confidenceMin: req.ConfidenceMin}
	if req.Type != "" {
		filter.typeTag = strings.ToLower("type:" + req.Type)
	}
	if len(req.Tags) > 0 {
		filter.tags = make(map[string]struct{}, len(req.Tags))
		for _, tag := range req.Tags {
			filter.tags[strings.ToLower(tag)] = struct{}{}
		}
	}
	memories := deps.Memories
	var text *textCandidates
	if deps.LiteralMatches != nil {
		text = &textCandidates{MemoryStoreInterface: memories, literal: deps.LiteralMatches, filter: &filter}
		memories = text
	}

	const filterCandidateMultiplier = 4
	fetchLimit := min(req.Limit*filterCandidateMultiplier, 200)
	scored, explanations, err := retrieval.HybridSearch(ctx, req.Project, req.Query,
		fetchLimit, memories, deps.Embeddings, deps.Graph, opts)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	if text != nil && text.literalErr != nil {
		return Result{}, fmt.Errorf("literal memory search: %w", text.literalErr)
	}
	if err != nil {
		return Result{}, fmt.Errorf("hybrid: %w", err)
	}

	hadCandidates := len(scored) > 0
	matches := selectMatches(scored, req.Limit, &filter)
	// An invisible or otherwise filtered exact hit must not prevent Tier1 recall.
	if hadCandidates && len(matches) == 0 && !opts.SkipTier0 {
		opts.SkipTier0 = true
		scored, explanations, err = retrieval.HybridSearch(ctx, req.Project, req.Query,
			fetchLimit, memories, deps.Embeddings, deps.Graph, opts)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		if text != nil && text.literalErr != nil {
			return Result{}, fmt.Errorf("literal memory search: %w", text.literalErr)
		}
		if err != nil {
			return Result{}, fmt.Errorf("hybrid (tier0 fallthrough): %w", err)
		}
		matches = selectMatches(scored, req.Limit, &filter)
	}
	return Result{Matches: matches, Explanations: explanations}, nil
}

func selectMatches(scored []retrieval.ScoredMemory, limit int, filter *memoryFilter) []retrieval.ScoredMemory {
	matches := scored[:0]
	for _, candidate := range scored {
		if !filter.matches(candidate.Memory) {
			continue
		}
		matches = append(matches, candidate)
		if len(matches) == limit {
			break
		}
	}
	return matches
}

type memoryFilter struct {
	caller        scope.KeycardContext
	visibility    scope.MemoryVisibilityOptions
	typeTag       string
	tags          map[string]struct{}
	confidenceMin float64
}

func (f *memoryFilter) matches(mem *models.Memory) bool {
	if f.typeTag != "" {
		matched := false
		for _, tag := range mem.Tags {
			if strings.ToLower(tag) == f.typeTag {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(f.tags) > 0 {
		matched := false
		for _, tag := range mem.Tags {
			if _, ok := f.tags[strings.ToLower(tag)]; ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return scope.ResolveMemory(f.caller, mem, f.visibility) &&
		!(f.confidenceMin > 0 && mem.Confidence < f.confidenceMin)
}

type rerankAdapter struct {
	client *reranking.Client
}

func (a rerankAdapter) Rank(ctx context.Context, query string, passages []string) ([]retrieval.RerankResult, error) {
	scores, err := a.client.Rank(ctx, query, passages)
	if err != nil {
		return nil, err
	}
	out := make([]retrieval.RerankResult, len(scores))
	for i, score := range scores {
		out[i] = retrieval.RerankResult{Index: score.Index, RelevanceScore: score.RelevanceScore}
	}
	return out, nil
}
