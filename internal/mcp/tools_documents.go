// Package mcp provides document/collection MCP tool handlers.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	pgvec "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
)

// handleListCollections returns all configured collections with document counts.
func (s *Server) handleListCollections(ctx context.Context) (string, error) {
	if s.collectionRegistry == nil {
		return "No collections configured.", nil
	}

	collections := s.collectionRegistry.All()
	if len(collections) == 0 {
		return "No collections configured.", nil
	}

	var counts map[string]int64
	if s.documentStore != nil {
		var err error
		counts, err = s.documentStore.CollectionDocCounts(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get collection doc counts")
		}
	}

	type collectionInfo struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		DocCount    int64  `json:"doc_count"`
	}

	var result []collectionInfo
	for _, c := range collections {
		info := collectionInfo{
			Name:        c.Name,
			Description: c.Description,
		}
		if counts != nil {
			info.DocCount = counts[c.Name]
		}
		result = append(result, info)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal collections: %w", err)
	}

	return string(out), nil
}

// handleListDocuments lists documents in a collection.
func (s *Server) handleListDocuments(ctx context.Context, args json.RawMessage) (string, error) {
	if s.documentStore == nil {
		return "", fmt.Errorf("document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Collection string
	}
	params.Collection = coerceString(m["collection"], "")
	if params.Collection == "" {
		return "", fmt.Errorf("collection is required")
	}

	docs, err := s.documentStore.ListDocuments(ctx, params.Collection, true)
	if err != nil {
		return "", fmt.Errorf("list documents: %w", err)
	}

	if len(docs) == 0 {
		return fmt.Sprintf("No documents in collection %q.", params.Collection), nil
	}

	type docInfo struct {
		Path      string `json:"path"`
		Title     string `json:"title,omitempty"`
		Hash      string `json:"hash,omitempty"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}

	var result []docInfo
	for _, d := range docs {
		info := docInfo{
			Path:      d.Path,
			CreatedAt: d.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: d.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if d.Title.Valid {
			info.Title = d.Title.String
		}
		if d.Hash.Valid {
			info.Hash = d.Hash.String[:12] // truncated for display
		}
		result = append(result, info)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal documents: %w", err)
	}

	return string(out), nil
}

// handleGetDocument retrieves full document content.
func (s *Server) handleGetDocument(ctx context.Context, args json.RawMessage) (string, error) {
	if s.documentStore == nil {
		return "", fmt.Errorf("document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Collection string
		Path       string
	}
	params.Collection = coerceString(m["collection"], "")
	params.Path = coerceString(m["path"], "")
	if params.Collection == "" || params.Path == "" {
		return "", fmt.Errorf("collection and path are required")
	}

	doc, err := s.documentStore.GetDocument(ctx, params.Collection, params.Path)
	if err != nil {
		return "", fmt.Errorf("get document: %w", err)
	}
	if doc == nil {
		return fmt.Sprintf("Document not found: %s/%s", params.Collection, params.Path), nil
	}

	if !doc.Hash.Valid {
		return fmt.Sprintf("Document %s/%s has no content hash.", params.Collection, params.Path), nil
	}

	content, err := s.documentStore.GetContent(ctx, doc.Hash.String)
	if err != nil {
		return "", fmt.Errorf("get content: %w", err)
	}
	if content == nil {
		return fmt.Sprintf("Content not found for hash %s.", doc.Hash.String[:12]), nil
	}

	return content.Doc, nil
}

// handleRemoveDocument deactivates a document.
func (s *Server) handleRemoveDocument(ctx context.Context, args json.RawMessage) (string, error) {
	if s.documentStore == nil {
		return "", fmt.Errorf("document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Collection string
		Path       string
	}
	params.Collection = coerceString(m["collection"], "")
	params.Path = coerceString(m["path"], "")
	if params.Collection == "" || params.Path == "" {
		return "", fmt.Errorf("collection and path are required")
	}

	if err := s.documentStore.DeactivateDocument(ctx, params.Collection, params.Path); err != nil {
		return "", fmt.Errorf("deactivate document: %w", err)
	}

	return fmt.Sprintf("Document %s/%s deactivated.", params.Collection, params.Path), nil
}

// handleIngestDocument ingests a document: upserts content, chunks, embeds, stores chunks.
func (s *Server) handleIngestDocument(ctx context.Context, args json.RawMessage) (string, error) {
	if s.documentStore == nil {
		return "", fmt.Errorf("document store not available")
	}
	if s.embedSvc == nil {
		return "", fmt.Errorf("embedding service not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Collection string
		Path       string
		Content    string
		Title      string
	}
	params.Collection = coerceString(m["collection"], "")
	params.Path = coerceString(m["path"], "")
	params.Content = coerceString(m["content"], "")
	params.Title = coerceString(m["title"], "")
	if params.Collection == "" || params.Path == "" || params.Content == "" {
		return "", fmt.Errorf("collection, path, and content are required")
	}

	// Upsert document (content-addressable via SHA-256)
	doc, err := s.documentStore.UpsertDocument(ctx, params.Collection, params.Path, params.Title, params.Content)
	if err != nil {
		return "", fmt.Errorf("upsert document: %w", err)
	}

	// Check if content hash changed — skip re-chunking if same
	hashBytes := sha256.Sum256([]byte(params.Content))
	newHash := hex.EncodeToString(hashBytes[:])
	if doc.Hash.Valid && doc.Hash.String == newHash {
		// Check if chunks already exist for this exact content hash
		exists, err := s.documentStore.ChunksExist(ctx, newHash)
		if err == nil && exists {
			return fmt.Sprintf("Document %s/%s already up-to-date (hash %s).", params.Collection, params.Path, newHash[:12]), nil
		}
	}

	// Chunk the content using markdown chunker (works for most text content)
	var textChunks []string
	if s.chunkManager != nil {
		// Write to temp file for chunker (chunkers expect file paths)
		ext := filepath.Ext(params.Path)
		if ext == "" {
			ext = ".md" // default to markdown
		}
		tmpFile, err := os.CreateTemp("", "engram-ingest-*"+ext)
		if err != nil {
			return "", fmt.Errorf("create temp file: %w", err)
		}
		tmpPath := tmpFile.Name()
		defer os.Remove(tmpPath)

		if _, err := tmpFile.WriteString(params.Content); err != nil {
			_ = tmpFile.Close()
			return "", fmt.Errorf("write temp file: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			return "", fmt.Errorf("close temp file: %w", err)
		}

		if s.chunkManager.SupportsFile(tmpPath) {
			chunks, chunkErr := s.chunkManager.ChunkFile(ctx, tmpPath)
			if chunkErr != nil {
				log.Warn().Err(chunkErr).Str("path", params.Path).Msg("Chunking failed, using full content")
				textChunks = []string{params.Content}
			} else {
				for _, c := range chunks {
					text := c.SearchableContent()
					if text != "" {
						textChunks = append(textChunks, text)
					}
				}
			}
		} else {
			// No chunker available — use full content as single chunk
			textChunks = []string{params.Content}
		}
	} else {
		textChunks = []string{params.Content}
	}

	if len(textChunks) == 0 {
		return fmt.Sprintf("Document %s/%s ingested but produced no chunks.", params.Collection, params.Path), nil
	}

	// Generate embeddings and create chunk records
	dbChunks := make([]gorm.ContentChunk, 0, len(textChunks))
	for i, text := range textChunks {
		emb, err := s.embedSvc.Embed(text)
		if err != nil {
			log.Warn().Err(err).Int("chunk", i).Msg("Embedding failed for chunk, skipping")
			continue
		}

		dbChunks = append(dbChunks, gorm.ContentChunk{
			Hash:      newHash,
			Seq:       i,
			Text:      text,
			Pos:       i,
			Model:     s.embedSvc.Version(),
			Embedding: pgvec.NewVector(emb),
		})
	}

	if err := s.documentStore.UpsertChunks(ctx, newHash, dbChunks); err != nil {
		return "", fmt.Errorf("upsert chunks: %w", err)
	}

	return fmt.Sprintf("Document %s/%s ingested: %d chunks embedded.", params.Collection, params.Path, len(dbChunks)), nil
}

// handleSearchCollection searches document chunks in a collection.
func (s *Server) handleSearchCollection(ctx context.Context, args json.RawMessage) (string, error) {
	if s.documentStore == nil {
		return "", fmt.Errorf("document store not available")
	}
	if s.embedSvc == nil {
		return "", fmt.Errorf("embedding service not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Query      string
		Collection string
		Limit      int
	}
	params.Query = coerceString(m["query"], "")
	params.Collection = coerceString(m["collection"], "")
	params.Limit = coerceInt(m["limit"], 0)
	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}
	if params.Limit > 50 {
		params.Limit = 50
	}

	// Embed the query
	queryEmb, err := s.embedSvc.Embed(params.Query)
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}

	chunks, err := s.documentStore.SearchChunks(ctx, queryEmb, params.Collection, params.Limit)
	if err != nil {
		return "", fmt.Errorf("search chunks: %w", err)
	}

	if len(chunks) == 0 {
		msg := "No matching document chunks found"
		if params.Collection != "" {
			msg += fmt.Sprintf(" in collection %q", params.Collection)
		}
		return msg + ".", nil
	}

	type chunkResult struct {
		Hash string `json:"hash"`
		Seq  int    `json:"seq"`
		Text string `json:"text"`
	}

	results := make([]chunkResult, 0, len(chunks))
	for _, c := range chunks {
		results = append(results, chunkResult{
			Hash: c.Hash[:12],
			Seq:  c.Seq,
			Text: c.Text,
		})
	}

	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal results: %w", err)
	}

	return string(out), nil
}
