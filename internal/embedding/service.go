// Package embedding provides text embedding generation with swappable models.
package embedding

import (
	"fmt"

	"github.com/thebtf/engram/internal/config"
)

// EmbeddingDim is the dimension of embeddings produced by the current model.
// Both all-MiniLM-L6-v2 and bge-small-en-v1.5 produce 384-dimensional embeddings.
const EmbeddingDim = 384

// Model version constants
const (
	// BGEModelVersion is the version string for bge-small-en-v1.5
	BGEModelVersion = "bge-v1.5"
	// BGEModelName is the human-readable name for bge-small-en-v1.5
	BGEModelName = "bge-small-en-v1.5"
	// DefaultModelVersion is the default model to use
	DefaultModelVersion = BGEModelVersion
)

// MaxSequenceLength is the maximum token sequence length for the model.
const MaxSequenceLength = 512

// Service provides thread-safe text embedding generation with model abstraction.
type Service struct {
	model EmbeddingModel
}

// NewService creates a new embedding service using the default model.
func NewService() (*Service, error) {
	return NewServiceWithModel(DefaultModelVersion)
}

// NewServiceWithModel creates a new embedding service using the specified model.
func NewServiceWithModel(version string) (*Service, error) {
	if version == "" {
		version = DefaultModelVersion
	}

	model, err := GetModel(version)
	if err != nil {
		return nil, fmt.Errorf("get model %s: %w", version, err)
	}

	return &Service{model: model}, nil
}

// NewServiceFromConfig creates an embedding service based on EMBEDDING_PROVIDER config.
// Uses "openai" provider when EMBEDDING_PROVIDER=openai, builtin ONNX otherwise.
func NewServiceFromConfig() (*Service, error) {
	provider := config.GetEmbeddingProvider()
	switch provider {
	case "openai":
		return NewServiceWithModel(OpenAIModelVersion)
	default:
		return NewService() // builtin BGE (not available on Windows)
	}
}

// Name returns the human-readable model name.
func (s *Service) Name() string {
	return s.model.Name()
}

// Version returns the short version string for storage.
func (s *Service) Version() string {
	return s.model.Version()
}

// Dimensions returns the embedding vector size.
func (s *Service) Dimensions() int {
	return s.model.Dimensions()
}

// Embed generates an embedding for a single text.
func (s *Service) Embed(text string) ([]float32, error) {
	return s.model.Embed(text)
}

// EmbedBatch generates embeddings for multiple texts.
func (s *Service) EmbedBatch(texts []string) ([][]float32, error) {
	return s.model.EmbedBatch(texts)
}

// Close releases model resources.
func (s *Service) Close() error {
	return s.model.Close()
}

// meanPooling applies mean pooling over token embeddings, weighted by attention mask.
// Input shape: [batch, seq_len, hidden], attention mask: [batch, seq_len]
// Output shape: [batch, hidden]
func meanPooling(embeddings []float32, attentionMask []int64, batchSize, seqLen, hiddenSize int) [][]float32 {
	results := make([][]float32, batchSize)

	for b := 0; b < batchSize; b++ {
		result := make([]float32, hiddenSize)
		var maskSum float32

		// Sum embeddings weighted by attention mask
		for s := 0; s < seqLen; s++ {
			maskVal := float32(attentionMask[b*seqLen+s])
			maskSum += maskVal

			if maskVal > 0 {
				embOffset := (b*seqLen + s) * hiddenSize
				for h := 0; h < hiddenSize; h++ {
					result[h] += embeddings[embOffset+h] * maskVal
				}
			}
		}

		// Normalize by mask sum (avoid division by zero)
		if maskSum > 0 {
			for h := 0; h < hiddenSize; h++ {
				result[h] /= maskSum
			}
		}

		results[b] = result
	}

	return results
}

// clsPooling extracts the [CLS] token embedding (first token).
// Input shape: [batch, seq_len, hidden]
// Output shape: [batch, hidden]
func clsPooling(embeddings []float32, batchSize, seqLen, hiddenSize int) [][]float32 {
	results := make([][]float32, batchSize)

	for b := 0; b < batchSize; b++ {
		result := make([]float32, hiddenSize)
		// CLS token is at position 0
		embOffset := b * seqLen * hiddenSize
		copy(result, embeddings[embOffset:embOffset+hiddenSize])
		results[b] = result
	}

	return results
}
