package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/ingestion"
	"github.com/thebtf/engram/internal/writegate"
	"github.com/thebtf/engram/pkg/models"
)

type ingestArgs struct {
	Action        string `json:"action"`
	Content       string `json:"content"`
	SourceTitle   string `json:"source_title"`
	SourceType    string `json:"source_type"`
	Project       string `json:"project"`
	ChunkStrategy string `json:"chunk_strategy"`
	DryRun        bool   `json:"dry_run"`
}

func (s *Server) handleIngest(ctx context.Context, args json.RawMessage) (string, error) {
	if s.memoryStore == nil {
		return "", fmt.Errorf("memory store not available")
	}

	var a ingestArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parse ingest args: %w", err)
	}

	switch a.Action {
	case "ingest":
		return s.ingestDocument(ctx, a)
	default:
		return "", fmt.Errorf("unknown ingest action: %s", a.Action)
	}
}

func (s *Server) ingestDocument(ctx context.Context, a ingestArgs) (string, error) {
	if a.Content == "" {
		return "", fmt.Errorf("content required")
	}
	if a.SourceTitle == "" {
		return "", fmt.Errorf("source_title required")
	}
	if a.Project == "" {
		a.Project = projectFromContext(ctx)
	}
	if a.Project == "" {
		return "", fmt.Errorf("project required")
	}

	strategy := ingestion.StrategyParagraphs
	switch a.ChunkStrategy {
	case "sections":
		strategy = ingestion.StrategySections
	case "fixed":
		strategy = ingestion.StrategyFixed
	case "paragraphs", "":
		strategy = ingestion.StrategyParagraphs
	default:
		return "", fmt.Errorf("invalid chunk_strategy: %s (use paragraphs, sections, or fixed)", a.ChunkStrategy)
	}

	chunks := ingestion.ChunkDocument(a.Content, strategy)
	if len(chunks) == 0 {
		return "", fmt.Errorf("document produced no chunks (empty or whitespace-only)")
	}
	const maxChunks = 500
	if len(chunks) > maxChunks {
		return "", fmt.Errorf("document produces %d chunks (max %d) — split into smaller documents", len(chunks), maxChunks)
	}

	if a.DryRun {
		return marshalJSON(map[string]any{
			"dry_run":     true,
			"chunk_count": len(chunks),
			"strategy":    string(strategy),
			"message":     "dry run — no chunks stored",
		})
	}

	var existing []*models.Memory
	vnextEnabled := os.Getenv("ENGRAM_VNEXT_ENABLED") == "true"
	if vnextEnabled && a.Project != "" {
		var listErr error
		existing, listErr = s.memoryStore.List(ctx, a.Project, 100)
		if listErr != nil {
			log.Warn().Err(listErr).Msg("ingest: write gate could not load existing memories, skipping gate")
		}
	}

	stored := 0
	flagged := 0
	lifecycleEnabled := os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true"
	for _, chunk := range chunks {
		var chunkFlagged bool
		if vnextEnabled && existing != nil {
			gateResult := writegate.Check(ctx, chunk.Text, existing)
			if gateResult.Decision == "flag" {
				chunkFlagged = true
			}
		}

		memory := &models.Memory{
			Project:     a.Project,
			Content:     chunk.Text,
			SourceAgent: "ingestion",
			Tags: []string{
				"ingested",
				fmt.Sprintf("source:%s", a.SourceTitle),
				fmt.Sprintf("chunk:%d", chunk.Index),
			},
			Tier:          "semantic",
			EpistemicType: "fact",
			Defeasibility: "slow",
		}
		if chunk.Section != "" {
			memory.Tags = append(memory.Tags, fmt.Sprintf("section:%s", chunk.Section))
		}
		if chunkFlagged {
			memory.Status = "flagged"
		}

		// Ingest supplies lifecycle fields, but the default-off path must leave
		// DB defaults authoritative for ordinary callers.
		var created *models.Memory
		var err error
		if vnextEnabled || lifecycleEnabled {
			created, err = s.memoryStore.CreateWithLifecycle(ctx, memory)
		} else {
			created, err = s.memoryStore.Create(ctx, memory)
		}
		if err != nil {
			log.Error().Err(err).Int("chunk_index", chunk.Index).Msg("ingest: store chunk failed")
			continue
		}
		if created.Status == "flagged" {
			flagged++
		}
		stored++
	}

	return marshalJSON(map[string]any{
		"source_title": a.SourceTitle,
		"strategy":     string(strategy),
		"total_chunks": len(chunks),
		"stored":       stored,
		"flagged":      flagged,
		"message":      fmt.Sprintf("ingested %d chunks from '%s'", stored, a.SourceTitle),
	})
}
