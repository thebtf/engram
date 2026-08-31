package embedding

import "github.com/thebtf/engram/internal/vectordim"

// EmbeddingDim is the embedding package's schema-bound vector dimension.
// The cycle-free authority lives in vectordim so retrieval consumers can share
// the value without importing the embedding client and its storage adapters.
const EmbeddingDim = vectordim.Dimension
