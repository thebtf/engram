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

// handleRemoveDocument atomically deactivates an existing active document.
func (s *Server) handleRemoveDocument(ctx context.Context, args json.RawMessage) (string, error) {
	if s.documentStore == nil {
		return "", fmt.Errorf("document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	collection, collectionPresent, err := optionalStringArg(m, "collection")
	if err != nil {
		return "", fmt.Errorf("remove_document: %w", err)
	}
	path, pathPresent, err := optionalStringArg(m, "path")
	if err != nil {
		return "", fmt.Errorf("remove_document: %w", err)
	}
	if !collectionPresent || !pathPresent || collection == "" || path == "" {
		return "", fmt.Errorf("collection and path are required")
	}
	params := struct{ Collection, Path string }{Collection: collection, Path: path}
	if params.Collection == "" || params.Path == "" {
		return "", fmt.Errorf("collection and path are required")
	}

	deactivated, err := s.documentStore.DeactivateDocument(ctx, params.Collection, params.Path)
	if err != nil {
		return "", fmt.Errorf("deactivate document: %w", err)
	}
	if !deactivated {
		return "", fmt.Errorf("document not found or inactive: %s/%s", params.Collection, params.Path)
	}

	return fmt.Sprintf("Document %s/%s marked inactive; its content-addressed body remains stored.", params.Collection, params.Path), nil
}

// handleIngestDocument stores the full body content-addressably and upserts
// document metadata. Document chunking, embeddings, and search are unavailable.
func (s *Server) handleIngestDocument(ctx context.Context, args json.RawMessage) (string, error) {
	if s.documentStore == nil {
		return "", fmt.Errorf("document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	values := make(map[string]string, 4)
	for _, key := range []string{"collection", "path", "content", "title"} {
		value, _, fieldErr := optionalStringArg(m, key)
		if fieldErr != nil {
			return "", fmt.Errorf("ingest_document: %w", fieldErr)
		}
		values[key] = value
	}
	params := struct{ Collection, Path, Content, Title string }{Collection: values["collection"], Path: values["path"], Content: values["content"], Title: values["title"]}
	if params.Collection == "" || params.Path == "" || params.Content == "" {
		return "", fmt.Errorf("collection, path, and content are required")
	}

	// Store the full content-addressed body and upsert its document metadata.
	_, err = s.documentStore.UpsertDocument(ctx, params.Collection, params.Path, params.Title, params.Content)
	if err != nil {
		return "", fmt.Errorf("upsert document: %w", err)
	}

	hashBytes := sha256.Sum256([]byte(params.Content))
	newHash := hex.EncodeToString(hashBytes[:])
	return fmt.Sprintf("Document %s/%s ingested: full body stored content-addressably (hash %s); document chunks, embeddings, and search remain unavailable.", params.Collection, params.Path, newHash[:12]), nil
}
