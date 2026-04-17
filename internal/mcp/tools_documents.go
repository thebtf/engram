// Package mcp provides document/collection MCP tool handlers.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog/log"
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

// handleIngestDocument ingests a document into a collection.
// Embedding/chunk storage removed in v5 (content_chunks table dropped).
// In v5, only document metadata is upserted; chunk-level search is unavailable.
func (s *Server) handleIngestDocument(ctx context.Context, args json.RawMessage) (string, error) {
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

	// Upsert document metadata only (embedding pipeline removed in v5).
	_, err = s.documentStore.UpsertDocument(ctx, params.Collection, params.Path, params.Title, params.Content)
	if err != nil {
		return "", fmt.Errorf("upsert document: %w", err)
	}

	hashBytes := sha256.Sum256([]byte(params.Content))
	newHash := hex.EncodeToString(hashBytes[:])
	return fmt.Sprintf("Document %s/%s ingested (metadata only, hash %s; chunk embeddings removed in v5).", params.Collection, params.Path, newHash[:12]), nil
}

// handleSearchCollection searches document chunks in a collection.
// Chunk-level vector search removed in v5 (content_chunks table dropped).
func (s *Server) handleSearchCollection(_ context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Query      string
		Collection string
	}
	params.Query = coerceString(m["query"], "")
	params.Collection = coerceString(m["collection"], "")
	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	// Vector chunk search removed in v5 (content_chunks table dropped).
	msg := "Document chunk search removed in v5; use find_relevant_memories for observation-level FTS retrieval"
	if params.Collection != "" {
		msg += fmt.Sprintf(" (collection %q)", params.Collection)
	}
	return msg + ".", nil
}
