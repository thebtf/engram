// Package vectordim owns the schema-bound vector dimension shared by memory
// and code embeddings without importing either subsystem.
package vectordim

// Dimension is the single source of truth for content_chunks and code_chunks.
// Changing it requires a paired schema migration and complete re-embedding.
const Dimension = 1536
