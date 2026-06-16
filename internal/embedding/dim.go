package embedding

// EmbeddingDim is the single source of truth for the embedding vector dimension
// used across BOTH subsystems — memory (content_chunks) and code (code_chunks).
//
// The operator runs ONE embedding model (Qwen3-Embedding-8B on Nebius, MRL-capable)
// serving both subsystems, so a single dimension is mandatory — there is no
// two-model / two-dimension configuration. 1536 was chosen over the model's native
// 4096 because:
//   - pgvector HNSW/IVFFlat indexes cap at 2000 dims; 1536 uses a native HNSW index
//     while 4096 requires DiskANN/pgvectorscale for the engram vector index.
//   - ~2.7x less storage and dot-product compute.
//   - Quality: server-side MRL truncation to 1536 retained 96.1% of ranking margin
//     with 0 inversions across a 12-triplet EN/RU/code/cross-lingual probe — the
//     accuracy delta is in the diminishing-returns zone.
//
// This constant feeds three consumers, eliminating the previously-duplicated
// dimension literal:
//  1. the embedding client request (dimensions param sent to the LiteLLM proxy),
//  2. the code backfill dimension-mismatch guard,
//  3. the startup column assert (information_schema vector(N) == EmbeddingDim).
//
// Changing the embedding dimension is therefore: edit this constant, add a schema
// migration that ALTERs both columns, and re-embed. The startup assert guarantees
// the constant, the DDL, and the GORM struct tags cannot silently drift apart.
const EmbeddingDim = 1536
