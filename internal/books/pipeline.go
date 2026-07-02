package books

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/chunking"
	mdchunking "github.com/thebtf/engram/internal/chunking/markdown"
)

// Status is the lifecycle state of a book-ingestion job.
type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"
)

// Job is the books bounded-context aggregate stored in books_jobs.
type Job struct {
	ID        int64
	Status    Status
	SourceRef string
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Store persists the books job lifecycle.
type Store interface {
	Create(ctx context.Context, sourceRef string) (*Job, error)
	GetStatus(ctx context.Context, id int64) (*Job, error)
	UpdateStatus(ctx context.Context, id int64, status Status, errorMessage string) (*Job, error)
}

// DocumentWriter writes produced chunks into the existing VersionedDocumentStore
// and can compensate partial writes by source_book_job_id.
type DocumentWriter interface {
	Create(ctx context.Context, path, project, content, docType, metadata, author string) (int64, error)
	DeleteBySourceBookJobID(ctx context.Context, jobID int64) (int64, error)
}

// ProcessRequest carries the source material for one books job execution.
type ProcessRequest struct {
	JobID     int64
	SourceRef string
	Content   string
	Project   string
	Author    string
}

// Pipeline adapts prose/book text into markdown chunks, then writes them into
// VersionedDocumentStore with source_book_job_id provenance metadata.
type Pipeline struct {
	store        Store
	documents    DocumentWriter
	chunker      *mdchunking.Chunker
	maxChunkSize int
}

// NewPipeline constructs the books pipeline using the PASS-WITH-ADAPTER verdict
// from T015: direct markdown chunker reuse through a thin books-side adapter.
func NewPipeline(store Store, documents DocumentWriter) *Pipeline {
	opts := chunking.DefaultChunkOptions()
	return &Pipeline{
		store:        store,
		documents:    documents,
		chunker:      mdchunking.NewChunker(opts),
		maxChunkSize: opts.MaxChunkSize,
	}
}

// DocumentPathPrefix returns the stable path prefix under which a job writes its
// produced versioned documents. The prefix becomes the visible tag in the
// Documents surface, while metadata stores source_book_job_id explicitly.
func DocumentPathPrefix(jobID int64) string {
	return fmt.Sprintf("books/jobs/%d/", jobID)
}

// Process runs one uploaded source through extract/normalize -> markdown chunk
// adapter -> VersionedDocumentStore writes -> terminal status update.
func (p *Pipeline) Process(ctx context.Context, req ProcessRequest) error {
	if p == nil || p.store == nil || p.documents == nil || p.chunker == nil {
		return fmt.Errorf("books pipeline not configured")
	}
	if req.JobID <= 0 {
		return fmt.Errorf("job_id must be positive")
	}

	sourceRef := strings.TrimSpace(req.SourceRef)
	if sourceRef == "" {
		return p.failJob(ctx, req.JobID, fmt.Errorf("source_ref required"))
	}
	if strings.TrimSpace(req.Project) == "" {
		req.Project = "engram"
	}
	if strings.TrimSpace(req.Author) == "" {
		req.Author = "books-pipeline"
	}

	if _, err := p.store.UpdateStatus(ctx, req.JobID, StatusProcessing, ""); err != nil {
		return fmt.Errorf("mark job processing: %w", err)
	}

	normalized, virtualPath, err := normalizeBookContent(sourceRef, req.Content)
	if err != nil {
		return p.failJob(ctx, req.JobID, fmt.Errorf("extract source: %w", err))
	}

	chunks, err := p.chunkMarkdown(ctx, virtualPath, normalized)
	if err != nil {
		return p.failJob(ctx, req.JobID, fmt.Errorf("chunk source: %w", err))
	}

	prefix := DocumentPathPrefix(req.JobID) + slugSegment(humanizeSourceTitle(sourceRef)) + "/"
	for idx, chunk := range chunks {
		metadata, err := bookDocumentMetadata(req, chunk, idx+1, len(chunks))
		if err != nil {
			return p.failJob(ctx, req.JobID, fmt.Errorf("encode metadata: %w", err))
		}

		path := buildDocumentPath(prefix, idx+1, chunk.Name)
		if _, err := p.documents.Create(ctx, path, req.Project, chunk.Content, "markdown", metadata, req.Author); err != nil {
			if cleanupErr := p.cleanupDocuments(ctx, req.JobID); cleanupErr != nil {
				return p.failJob(ctx, req.JobID, fmt.Errorf("write document %d: %w (cleanup failed: %v)", idx+1, err, cleanupErr))
			}
			return p.failJob(ctx, req.JobID, fmt.Errorf("write document %d: %w", idx+1, err))
		}
	}

	if _, err := p.store.UpdateStatus(ctx, req.JobID, StatusDone, ""); err != nil {
		return fmt.Errorf("mark job done: %w", err)
	}
	return nil
}

func (p *Pipeline) failJob(ctx context.Context, jobID int64, cause error) error {
	message := truncateError(strings.TrimSpace(cause.Error()))
	if _, err := p.store.UpdateStatus(ctx, jobID, StatusFailed, message); err != nil {
		return fmt.Errorf("%w (persist failed status: %v)", cause, err)
	}
	return cause
}

func (p *Pipeline) cleanupDocuments(ctx context.Context, jobID int64) error {
	if p.documents == nil || jobID <= 0 {
		return nil
	}
	_, err := p.documents.DeleteBySourceBookJobID(ctx, jobID)
	return err
}

func (p *Pipeline) chunkMarkdown(ctx context.Context, virtualPath, content string) ([]chunking.Chunk, error) {
	chunks, err := p.chunker.ChunkContent(ctx, virtualPath, content)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no chunks produced")
	}
	if p.maxChunkSize > 0 {
		chunks = splitOversizeChunks(chunks, p.maxChunkSize)
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no chunks produced after fallback splitting")
	}
	return chunks, nil
}

func normalizeBookContent(sourceRef, content string) (string, string, error) {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(sourceRef)))
	switch ext {
	case "", ".md", ".mdx", ".markdown", ".txt", ".text":
	default:
		return "", "", fmt.Errorf("unsupported source format %q", ext)
	}
	if !utf8.ValidString(content) {
		return "", "", fmt.Errorf("content is not valid UTF-8")
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return "", "", fmt.Errorf("content is empty after normalization")
	}

	if !startsWithMarkdownHeading(normalized) {
		normalized = "# " + humanizeSourceTitle(sourceRef) + "\n\n" + normalized
	}
	return normalized, virtualMarkdownPath(sourceRef), nil
}

func startsWithMarkdownHeading(content string) bool {
	for rest := content; ; {
		line, next, found := strings.Cut(rest, "\n")
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return strings.HasPrefix(trimmed, "#")
		}
		if !found {
			return false
		}
		rest = next
	}
}

func virtualMarkdownPath(sourceRef string) string {
	base := strings.TrimSpace(filepath.Base(sourceRef))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		base = "book"
	}
	return base + ".md"
}

func humanizeSourceTitle(sourceRef string) string {
	base := strings.TrimSpace(filepath.Base(sourceRef))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.ReplaceAll(base, "_", " ")
	base = strings.ReplaceAll(base, "-", " ")
	base = strings.TrimSpace(base)
	if base == "" {
		return "Imported book"
	}
	return base
}

func buildDocumentPath(prefix string, chunkIndex int, chunkName string) string {
	return fmt.Sprintf("%s%04d-%s.md", prefix, chunkIndex, slugSegment(chunkName))
}

func bookDocumentMetadata(req ProcessRequest, chunk chunking.Chunk, chunkIndex, chunkTotal int) (string, error) {
	payload := map[string]any{
		"source":             "book",
		"source_book_job_id": req.JobID,
		"source_ref":         strings.TrimSpace(req.SourceRef),
		"project":            strings.TrimSpace(req.Project),
		"chunk_index":        chunkIndex,
		"chunk_total":        chunkTotal,
		"chunk_name":         chunk.Name,
		"start_line":         chunk.StartLine,
		"end_line":           chunk.EndLine,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func splitOversizeChunks(chunks []chunking.Chunk, maxBytes int) []chunking.Chunk {
	if maxBytes <= 0 {
		return chunks
	}

	out := make([]chunking.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		if len(chunk.Content) <= maxBytes {
			out = append(out, chunk)
			continue
		}
		out = append(out, splitOversizeChunk(chunk, maxBytes)...)
	}
	return out
}

func splitOversizeChunk(chunk chunking.Chunk, maxBytes int) []chunking.Chunk {
	groups := paragraphGroups(chunk.Content)
	if len(groups) <= 1 {
		return splitChunkByLines(chunk, maxBytes)
	}

	startLine := chunk.StartLine
	var current []string
	var parts []chunking.Chunk

	flushCurrent := func() {
		if len(current) == 0 {
			return
		}
		parts = append(parts, buildSplitChunk(chunk, current, startLine, len(parts)+1, 0))
		startLine += len(current)
		current = nil
	}

	for _, group := range groups {
		if joinedLinesSize(group) > maxBytes {
			flushCurrent()
			lineParts := splitChunkByLines(chunking.Chunk{
				FilePath:  chunk.FilePath,
				Language:  chunk.Language,
				Type:      chunk.Type,
				Name:      chunk.Name,
				Content:   strings.Join(group, "\n"),
				StartLine: startLine,
				EndLine:   startLine + len(group) - 1,
				Metadata:  cloneChunkMetadata(chunk.Metadata),
			}, maxBytes)
			for _, part := range lineParts {
				parts = append(parts, buildSplitChunk(part, strings.Split(part.Content, "\n"), part.StartLine, len(parts)+1, 0))
				startLine = part.EndLine + 1
			}
			continue
		}

		candidate := append(append([]string(nil), current...), group...)
		if len(current) > 0 && joinedLinesSize(candidate) > maxBytes {
			flushCurrent()
		}
		current = append(current, group...)
	}
	flushCurrent()

	if len(parts) <= 1 {
		return splitChunkByLines(chunk, maxBytes)
	}
	for idx := range parts {
		parts[idx].Name = splitChunkName(chunk.Name, idx+1, len(parts))
	}
	return parts
}

func splitChunkByLines(chunk chunking.Chunk, maxBytes int) []chunking.Chunk {
	if maxBytes <= 0 {
		return []chunking.Chunk{chunk}
	}

	lines := strings.Split(chunk.Content, "\n")
	if len(lines) <= 1 {
		return []chunking.Chunk{chunk}
	}

	startLine := chunk.StartLine
	var current []string
	var parts []chunking.Chunk
	flushCurrent := func() {
		if len(current) == 0 {
			return
		}
		parts = append(parts, buildSplitChunk(chunk, current, startLine, len(parts)+1, 0))
		startLine += len(current)
		current = nil
	}

	for _, line := range lines {
		candidate := append(append([]string(nil), current...), line)
		if len(current) > 0 && joinedLinesSize(candidate) > maxBytes {
			flushCurrent()
		}
		current = append(current, line)
	}
	flushCurrent()

	if len(parts) <= 1 {
		return []chunking.Chunk{chunk}
	}
	for idx := range parts {
		parts[idx].Name = splitChunkName(chunk.Name, idx+1, len(parts))
	}
	return parts
}

func paragraphGroups(content string) [][]string {
	lines := strings.Split(content, "\n")
	groups := make([][]string, 0, len(lines))
	var current []string
	for _, line := range lines {
		current = append(current, line)
		if strings.TrimSpace(line) == "" {
			groups = append(groups, current)
			current = nil
		}
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

func joinedLinesSize(lines []string) int {
	size := 0
	for i, line := range lines {
		size += len(line)
		if i < len(lines)-1 {
			size++
		}
	}
	return size
}

func buildSplitChunk(base chunking.Chunk, lines []string, startLine, partIndex, totalParts int) chunking.Chunk {
	name := base.Name
	if totalParts > 1 {
		name = splitChunkName(base.Name, partIndex, totalParts)
	}
	if name == "" {
		name = splitChunkName("untitled", partIndex, totalParts)
	}
	endLine := startLine + len(lines) - 1
	return chunking.Chunk{
		FilePath:  base.FilePath,
		Language:  base.Language,
		Type:      base.Type,
		Name:      name,
		Content:   strings.Join(lines, "\n"),
		StartLine: startLine,
		EndLine:   endLine,
		Metadata:  cloneChunkMetadata(base.Metadata),
	}
}

func splitChunkName(name string, partIndex, totalParts int) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "untitled"
	}
	if totalParts <= 1 {
		return base
	}
	return fmt.Sprintf("%s-part-%d", base, partIndex)
}

func cloneChunkMetadata(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func slugSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "untitled"
	}

	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevDash = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '.' || r == '/' || r == '\\':
			if b.Len() > 0 && !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	return out
}

func truncateError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= 500 {
		return message
	}
	return message[:500]
}
